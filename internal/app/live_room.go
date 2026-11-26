package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
)

type liveRoomView struct {
	room      *dy.Live
	view      *mygo.WebView
	webIdle   *time.Timer
	webGen    int
	webBusy   int
	cancel    context.CancelFunc
	started   time.Time
	signedIn  bool
	status    string
	hidden    bool
	scroll    ui.ScrollState
	messages  []dy.LiveMessage
	seen      map[uint64]bool
	seenOrder []uint64

	draft     string
	input     ui.Handle
	sending   bool
	sendError string
	send      func(string) (dy.LiveMessage, error)
	gifts     liveGiftPanel
}

func (a *app) syncLiveRoom(p *page) {
	var room *dy.Live
	if !a.loginOpen && p.plays() && p.idx < len(p.list) {
		room = p.list[p.idx].v.Live
	}
	if a.liveChat != nil && a.liveChat.room == room && a.liveChat.signedIn == a.signedIn {
		return
	}
	a.closeLiveRoom()
	if room == nil || !liveRoomCode.MatchString(room.WebRID) || a.win == nil || !liveRoomCode.MatchString(room.RoomID) {
		return
	}
	p.list[p.idx].clearDanmaku()
	ctx, cancel := context.WithCancel(context.Background())
	chat := &liveRoomView{room: room, cancel: cancel, started: time.Now(), signedIn: a.signedIn, status: "正在连接直播消息…"}
	a.liveChat = chat
	if chat.signedIn {
		chat.send = func(text string) (dy.LiveMessage, error) {
			eval, done, err := a.liveWritePage(ctx, chat, false)
			if err != nil {
				return dy.LiveMessage{}, err
			}
			defer done()
			return dy.SendLiveComment(ctx, eval, *room, text)
		}
		chat.gifts.load = func() ([]dy.LiveGift, error) {
			eval, done, err := a.liveWritePage(ctx, chat, true)
			if err != nil {
				return nil, err
			}
			defer done()
			return dy.ReadLiveGifts(ctx, eval, *room)
		}
		chat.gifts.send = func(gift dy.LiveGift) error {
			eval, done, err := a.liveWritePage(ctx, chat, true)
			if err != nil {
				return err
			}
			defer done()
			return dy.SendLiveGift(ctx, eval, *room, gift)
		}
	}
	if !chat.signedIn {
		go a.readGuestLiveRoom(ctx, chat)
	} else if a.c != nil {
		go a.readLiveRoom(ctx, chat, a.c.LivePoll(*room))
	}
}

func (a *app) closeLiveRoom() {
	if chat := a.liveChat; chat != nil {
		a.liveChat = nil
		if chat.cancel != nil {
			chat.cancel()
		}
		chat.closeWeb()
	}
}

func (a *app) readLiveRoom(ctx context.Context, chat *liveRoomView, poll func(context.Context) ([]dy.LiveMessage, time.Duration, error)) {
	previous := "正在连接直播消息…"
	for {
		call, cancel := context.WithTimeout(ctx, 8*time.Second)
		messages, wait, err := poll(call)
		cancel()
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		status := ""
		if err != nil {
			status = "正在连接直播消息…"
			if now.Sub(chat.started) > 10*time.Second {
				status = "正在等待直播消息恢复…"
			}
			if wait <= 0 {
				wait = 3 * time.Second
			}
		}
		if len(messages) != 0 || status != previous {
			previous = status
			a.post(func() {
				if a.liveChat != chat {
					return
				}
				chat.status = status
				p := a.cur()
				if p.plays() && p.idx < len(p.list) && p.list[p.idx].v.Live == chat.room {
					a.addLiveMessages(chat, p.list[p.idx], messages, now)
				}
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (a *app) addLiveMessages(chat *liveRoomView, s *slot, messages []dy.LiveMessage, now time.Time) {
	if chat.seen == nil {
		chat.seen = make(map[uint64]bool)
	}
	at := now.Sub(chat.started)
	s.pruneDanmaku(at, a.dmRealFlight())
	for _, msg := range messages {
		if msg.ID != 0 {
			if chat.seen[msg.ID] {
				// The live push can supply badges that were absent from the send receipt.
				if len(msg.Badges) != 0 {
					for i := range chat.messages {
						if previous := &chat.messages[i]; previous.ID == msg.ID {
							previous.Badges = msg.Badges
							break
						}
					}
				}
				continue
			}
			chat.seen[msg.ID] = true
			chat.seenOrder = append(chat.seenOrder, msg.ID)
		}
		chat.messages = append(chat.messages, msg)
		if msg.GiftCount == 0 && !a.paused && !a.dmOff && !a.clear && len(s.danmaku) < 64 {
			d := dy.Danmaku{At: at, Text: msg.Text}
			if n := len(s.danmaku); n != 0 && s.danmaku[n-1].At >= d.At {
				d.At = s.danmaku[n-1].At + time.Millisecond
			}
			s.danmaku = append(s.danmaku, d)
		}
	}
	if len(chat.messages) > 150 {
		n := copy(chat.messages, chat.messages[len(chat.messages)-150:])
		clear(chat.messages[n:])
		chat.messages = chat.messages[:n]
	}
	if len(chat.seenOrder) > 400 {
		cut := len(chat.seenOrder) - 400
		for _, id := range chat.seenOrder[:cut] {
			delete(chat.seen, id)
		}
		chat.seenOrder = chat.seenOrder[:copy(chat.seenOrder, chat.seenOrder[cut:])]
	}
}

func (a *app) liveMessagePanel(c *ui.Context, chat *liveRoomView) {
	ui.Column(c.Key("live-messages:" + chat.room.WebRID)).Width(316).Radius(12).Clip().Background(pal.raised).Children(func() {
		ui.Row(c).Padding(12, 14).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "直播互动").FontSize(14).FontWeight(700).Grow(1)
			if !chat.signedIn {
				ui.Text(c, "登录后可发言和看礼物").FontSize(11).TextColor(pal.muted)
			}
			if ui.Link(c.Key("gift"), "送礼", "").FontSize(12).TextColor(commentLinkColor).Clicked() {
				if chat.signedIn {
					a.openLiveGifts(chat)
				} else {
					a.openLogin()
				}
			}
			if iconButton(c.Key("close"), iconClose, 15, pal.muted, "关闭直播互动").Clicked() {
				chat.hidden = true
			}
		})
		if chat.status != "" || len(chat.messages) == 0 {
			ui.Text(c, onOff(chat.status != "", chat.status, "等待直播消息…")).Padding(4, 14, 8, 14).FontSize(12).TextColor(pal.muted)
		}
		if chat.scroll.Y >= chat.scroll.MaxY-3 {
			chat.scroll.Y = math.MaxFloat32
		}
		ui.Column(c).Grow(1).MinHeight(0).Children(func() { // the list, above the field to write in
			listFoot(c)
			ui.Scroll(c.Key("messages")).Grow(1).Padding(0, 14, listFade, 14).TrackScroll(&chat.scroll).Children(func() {
				for _, m := range chat.messages {
					a.liveMessageLine(c, m)
				}
			})
		})
		a.liveCommentInput(c, chat)
	})
}

func (a *app) liveMessageLine(c *ui.Context, msg dy.LiveMessage) {
	name := onOff(msg.User != "", msg.User, "观众")
	text := name + "：" + msg.Text
	color := pal.text
	if msg.GiftCount > 0 {
		text = fmt.Sprintf("%s 送出 %s ×%d", name, msg.Text, msg.GiftCount)
		color = commentLinkColor
	}
	spans, pics := a.emojiSpans(text, 13, 0)
	if len(msg.Badges) == 0 {
		ui.RichText(c, spans...).FontSize(13).TextColor(color).Label(text).Padding(4, 0).DrawObjects(pics.draw)
		return
	}
	prefix := make([]ui.Span, 0, 2*len(msg.Badges)+len(spans))
	var label strings.Builder
	offset := 0
	for _, badge := range msg.Badges {
		badgeText, background := ui.RGBA(255, 255, 255, 0.92), ui.Hex("#3b3d51")
		switch badge.Kind {
		case 1:
			badgeText, background = ui.Hex("#bcd5ff"), ui.Hex("#293d60")
		case 7:
			badgeText, background = ui.Hex("#ffcfdf"), ui.Hex("#582f47")
		}
		prefix = append(prefix, ui.Span{Text: badge.Label, Size: 11, Weight: 600, Color: badgeText,
			Background: background, Radius: 4, PaddingX: 4, PaddingY: 1}, ui.Span{Text: " "})
		label.WriteString(badge.Label)
		label.WriteByte(' ')
		offset += utf8.RuneCountInString(badge.Label) + 1
	}
	label.WriteString(text)
	ui.RichText(c, append(prefix, spans...)...).FontSize(13).TextColor(color).Label(label.String()).Padding(4, 0).
		DrawObjects(func(p *ui.Painter, at int, r ui.Rect) { pics.draw(p, at-offset, r) })
}

func (a *app) liveCommentInput(c *ui.Context, chat *liveRoomView) {
	ui.Column(c.Key("compose")).Padding(8, 14, 12, 14).Gap(6).Children(func() {
		if !chat.signedIn {
			if ui.Button(c.Key("login"), "登录后发评论").Clicked() {
				a.openLogin()
			}
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			in := ui.TextInputBase(c.Key("input"), &chat.draft).Bind(&chat.input).Grow(1).Shrink(1).MinWidth(0).
				Height(32).PaddingX(9).Radius(7).Background(pal.hover).FontSize(13).Placeholder("说点什么…").Label("直播评论内容")
			n := utf8.RuneCountInString(strings.TrimSpace(chat.draft))
			button := ui.Button(c.Key("send"), onOff(chat.sending, "发送中…", "发送")).Label("发送直播评论").
				Disabled(chat.send == nil || chat.sending || n == 0 || n > dy.LiveCommentLimit)
			if in.Changed() {
				chat.sendError = ""
			}
			if (in.Submitted() && !in.Composing()) || button.Clicked() {
				a.sendLiveComment(chat)
			}
		})
		if n := utf8.RuneCountInString(strings.TrimSpace(chat.draft)); n > dy.LiveCommentLimit {
			ui.Text(c, fmt.Sprintf("最多 %d 个字，已输入 %d 个", dy.LiveCommentLimit, n)).FontSize(11).TextColor(commentLinkColor)
		} else if chat.sendError != "" {
			ui.Text(c, chat.sendError).FontSize(11).TextColor(commentLinkColor)
		}
	})
}

func (a *app) sendLiveComment(chat *liveRoomView) {
	if !a.signedIn || chat.send == nil || chat.sending {
		return
	}
	draft := chat.draft
	text := strings.TrimSpace(draft)
	if n := utf8.RuneCountInString(text); n == 0 || n > dy.LiveCommentLimit {
		return
	}
	chat.sending, chat.sendError = true, ""
	send := chat.send
	go func() {
		msg, err := send(text)
		a.post(func() {
			if a.liveChat != chat {
				return
			}
			chat.sending = false
			if err != nil {
				chat.sendError = err.Error()
				return
			}
			if chat.draft == draft {
				chat.draft = ""
			}
			p := a.cur()
			if msg.ID != 0 && p.plays() && p.idx < len(p.list) && p.list[p.idx].v.Live == chat.room {
				if msg.Text == "" {
					msg.Text = text
				}
				if msg.User == "" {
					msg.User = a.me.Name
				}
				a.addLiveMessages(chat, p.list[p.idx], []dy.LiveMessage{msg}, time.Now())
			}
		})
	}()
}
