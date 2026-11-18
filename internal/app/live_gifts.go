package app

import (
	"errors"
	"fmt"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

type liveGiftPanel struct {
	open, loading, sending, unconfirmed bool
	items                               []dy.LiveGift
	selected                            dy.LiveGift
	message                             string
	load                                func() ([]dy.LiveGift, error)
	send                                func(dy.LiveGift) error
}

func (a *app) openLiveGifts(chat *liveRoomView) {
	panel := &chat.gifts
	panel.open = true
	if panel.loading || panel.sending || panel.unconfirmed {
		return
	}
	panel.items, panel.selected = nil, dy.LiveGift{}
	panel.message = ""
	if panel.load == nil {
		panel.message = "直播间还没准备好"
		return
	}
	panel.loading = true
	load := panel.load
	go func() {
		gifts, err := load()
		a.post(func() {
			if a.liveChat != chat {
				return
			}
			panel.loading = false
			if err != nil {
				panel.message = err.Error()
				return
			}
			panel.items = gifts
		})
	}()
}

func (a *app) confirmLiveGift(chat *liveRoomView) {
	panel := &chat.gifts
	if a.liveChat != chat || !a.signedIn || !chat.signedIn || !panel.open || panel.sending || panel.unconfirmed || panel.send == nil || panel.selected.ID == 0 {
		return
	}
	gift := panel.selected
	panel.sending, panel.message = true, ""
	send := panel.send
	go func() {
		err := send(gift)
		a.post(func() {
			panel.sending, panel.selected = false, dy.LiveGift{}
			if err != nil {
				panel.unconfirmed = errors.Is(err, dy.ErrLiveGiftUnconfirmed)
				panel.items = nil
				panel.message = err.Error()
			} else {
				panel.message = fmt.Sprintf("已送出 %s ×1", gift.Name)
			}
			if a.liveChat != chat || !panel.open {
				a.toast = panel.message
			}
		})
	}()
}

func (a *app) liveGiftDialog(c *ui.Context, chat *liveRoomView, author string) {
	p := &chat.gifts
	ui.DialogBase(c.Key("live-gifts"), &p.open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(380).Padding(18, 20).Gap(12).Radius(14).Background(pal.raised).Label("直播送礼")
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "送礼给 "+author).FontSize(16).FontWeight(700).Grow(1).SingleLine()
			if iconButton(c.Key("close"), iconClose, 16, pal.muted, "关闭送礼").Clicked() {
				p.open = false
				if !p.sending {
					p.selected = dy.LiveGift{}
				}
			}
		})
		if p.loading {
			ui.Text(c, "正在读取礼物…").TextColor(pal.muted)
			return
		}
		if p.message != "" {
			ui.Text(c, p.message).FontSize(13).TextColor(pal.text)
		}
		if p.unconfirmed {
			return
		}
		if p.selected.ID != 0 {
			ui.Text(c, fmt.Sprintf("%s ×1 · 扣除 %d 钻", p.selected.Name, p.selected.Price)).FontSize(15).TextColor(commentLinkColor)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("cancel"), "取消").Disabled(p.sending).Clicked() {
					p.selected = dy.LiveGift{}
				}
				if ui.Button(c.Key("confirm"), onOff(p.sending, "发送中…", "确认送出")).Disabled(p.sending).Clicked() {
					a.confirmLiveGift(chat)
				}
			})
			return
		}
		if len(p.items) == 0 {
			if ui.Button(c.Key("reload"), "刷新礼物").Clicked() {
				a.openLiveGifts(chat)
			}
			return
		}
		ui.Scroll(c.Key("list")).Height(float32(min(len(p.items), 6)) * 36).Children(func() {
			for _, gift := range p.items {
				label := fmt.Sprintf("%s · %d 钻", gift.Name, gift.Price)
				if ui.Button(c.Key(fmt.Sprint(gift.ID)), label).Height(32).Margin(0, 0, 4, 0).Disabled(p.send == nil).Clicked() {
					p.selected, p.message = gift, ""
				}
			}
		})
	})
}
