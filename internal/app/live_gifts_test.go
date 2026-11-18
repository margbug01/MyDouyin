package app

import (
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func TestLiveGiftRequiresNativeConfirmation(t *testing.T) {
	p := stub("直播", 1)
	p.list[0].v.Author = "主播甲"
	room := &dy.Live{WebRID: "123"}
	p.list[0].v.Live = room
	gift := dy.LiveGift{ID: 463, Name: "小心心", Price: 1}
	chat := &liveRoomView{room: room, signedIn: true, started: time.Now()}
	a := withRoots(&app{pages: []*page{p}, liveChat: chat, signedIn: true, posted: make(chan func(), 1)})
	sent := make(chan dy.LiveGift, 2)
	chat.gifts.load = func() ([]dy.LiveGift, error) { return []dy.LiveGift{gift}, nil }
	chat.gifts.send = func(g dy.LiveGift) error { sent <- g; return dy.ErrLiveGiftUnconfirmed }
	tt := newTester(a)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	update := func() {
		t.Helper()
		select {
		case f := <-a.posted:
			f()
			tt.Frame()
		case <-time.After(time.Second):
			t.Fatal("gift operation did not complete")
		}
	}
	click("送礼")
	update()
	click("小心心 · 1 钻")
	if len(tt.OpenedURLs()) != 0 || len(sent) != 0 || !tt.HasText("送礼给 主播甲") || !tt.HasText("扣除 1 钻") {
		t.Fatalf("gift selection must only display confirmation: %v", tt.Texts())
	}
	click("取消")
	click("小心心 · 1 钻")
	click("确认送出")
	a.confirmLiveGift(chat)
	update()
	if len(sent) != 1 || <-sent != gift || !tt.HasText("未收到送礼确认") || tt.HasText("确认送出") {
		t.Fatalf("uncertain purchase must stay blocked: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	click("送礼")
	if !tt.HasText("未收到送礼确认") || tt.HasText("确认送出") {
		t.Fatal("reopening the dialog removed the unconfirmed purchase warning")
	}
}
