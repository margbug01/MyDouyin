package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func TestLiveRoomCommentsGiftsAndControls(t *testing.T) {
	p := stub("直播", 2)
	s := p.list[0]
	s.v.Live = &dy.Live{WebRID: "123", Viewers: "19"}
	a := withRoots(&app{pages: []*page{p}})
	now := time.Now()
	chat := &liveRoomView{room: s.v.Live, started: now.Add(-2 * time.Second), signedIn: true}
	a.liveChat, a.signedIn = chat, true
	comment := dy.LiveMessage{ID: 10, User: "阿文", Text: "晚上好", Badges: []dy.LiveBadge{{Kind: 1, Label: "荣誉 18"}, {Kind: 7, Label: "粉丝团 5"}}}
	gift := dy.LiveMessage{ID: 11, User: "阿华", Text: "小心心", GiftCount: 3}
	a.addLiveMessages(chat, s, []dy.LiveMessage{comment, gift, comment}, now)
	if len(chat.messages) != 2 || len(s.danmaku) != 1 || s.danmaku[0].Text != comment.Text {
		t.Fatalf("messages %v, danmaku %v", chat.messages, s.danmaku)
	}
	tt := newTester(a)
	if !tt.HasText("荣誉 18 粉丝团 5 阿文：晚上好") || !tt.HasText("阿华 送出 小心心 ×3") {
		t.Fatalf("interaction panel: %v", tt.Texts())
	}
	if err := tt.Click("弹幕"); err != nil || !a.dmOff || len(s.danmaku) != 0 {
		t.Fatalf("disabling danmaku: %v", err)
	}
	a.addLiveMessages(chat, s, []dy.LiveMessage{{ID: 12, Text: "仍然可读"}}, now)
	if err := tt.Click("关闭直播互动"); err != nil || !chat.hidden {
		t.Fatalf("closing panel: %v", err)
	}
	tt.Frame()
	if tt.HasText("阿华 送出") {
		t.Fatal("gift still visible after closing the panel")
	}
	if err := tt.Click("互动"); err != nil || chat.hidden {
		t.Fatalf("reopening panel: %v", err)
	}
	tt.Frame()
	if !tt.HasText("仍然可读") || len(s.danmaku) != 0 {
		t.Fatal("messages should continue when the on-video danmaku is off")
	}
	p.idx = 1
	tt.Frame()
	if a.liveChat != nil || strings.Contains(strings.Join(tt.Texts(), " "), "仍然可读") {
		t.Fatal("previous room's state survived changing videos")
	}
}

func TestLiveCommentSendRetainsDraftAndDeduplicatesEcho(t *testing.T) {
	p := stub("直播", 2)
	s := p.list[0]
	s.v.Live = &dy.Live{WebRID: "123"}
	a := withRoots(&app{pages: []*page{p}, posted: make(chan func(), 1), signedIn: true})
	attempts := make(chan string, 2)
	results := make(chan error, 2)
	chat := &liveRoomView{room: s.v.Live, started: time.Now(), signedIn: true, draft: "晚上好 👋"}
	chat.send = func(text string) (dy.LiveMessage, error) {
		attempts <- text
		return dy.LiveMessage{ID: 42, User: "我", Text: text}, <-results
	}
	a.liveChat = chat
	tt := newTester(a)
	if err := tt.Click("直播评论内容"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	if p.idx != 0 {
		t.Fatal("editing the draft changed the video")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEnter)
	awaitSend := func() {
		t.Helper()
		select {
		case text := <-attempts:
			if text != "晚上好 👋" {
				t.Fatalf("sent %q", text)
			}
		case <-time.After(time.Second):
			t.Fatal("comment was not submitted")
		}
	}
	finish := func(err error) {
		t.Helper()
		results <- err
		select {
		case f := <-a.posted:
			f()
			tt.Frame()
		case <-time.After(time.Second):
			t.Fatal("send did not finish")
		}
	}
	awaitSend()
	finish(errors.New("本直播间暂时无法发言"))
	if !tt.HasText("本直播间暂时无法发言") || chat.draft != "晚上好 👋" || len(chat.messages) != 0 {
		t.Fatal("the send error should retain the draft and leave the feed alone")
	}
	if err := tt.Click("发送直播评论"); err != nil {
		t.Fatal(err)
	}
	awaitSend()
	finish(nil)
	a.addLiveMessages(chat, s, []dy.LiveMessage{{ID: 42, User: "我", Text: "晚上好 👋", Badges: []dy.LiveBadge{{Kind: 1, Label: "荣誉 8"}}}}, time.Now())
	tt.Frame()
	if chat.draft != "" || len(chat.messages) != 1 || !tt.HasText("荣誉 8 我：晚上好 👋") || len(attempts) != 0 {
		t.Fatalf("draft %q, messages %v, extra sends %d", chat.draft, chat.messages, len(attempts))
	}
}
