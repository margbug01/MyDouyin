package app

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestWebIdleKeepsPageInUse(t *testing.T) {
	for _, held := range []string{"script", "login", "layout", "stale timer"} {
		t.Run(held, func(t *testing.T) {
			a := &app{}
			w := &a.web
			w.a, w.v = a, &mygo.WebView{}
			w.touch()
			defer func() {
				if w.idle != nil {
					w.idle.Stop()
				}
			}()
			gen := w.idleGen
			switch held {
			case "script":
				w.busy = 1
			case "login":
				a.loginOpen = true
			case "layout":
				w.layout = true
			case "stale timer":
				w.touch()
			}
			page := w.v
			w.expire(gen)
			if w.v != page {
				t.Fatal("idle callback discarded a page still in use")
			}
			if w.idleGen <= gen {
				t.Fatal("idle deadline was not renewed")
			}
		})
	}
}

func TestClosedLoginPanelDoesNotRenewWebIdle(t *testing.T) {
	a := &app{loginGen: 4}
	a.web.a, a.web.layout, a.web.idleGen = a, true, 7
	tt := ui.NewTester(func(c *ui.Context) { a.loginPanel(c) }, 480, 400)
	tt.Frame()
	if a.loginGen != 4 || !a.web.layout || a.web.idleGen != 7 {
		t.Fatal("closed login panel changed a sharing page or renewed its idle deadline")
	}
}

func TestClosedWebCannotReopen(t *testing.T) {
	w := &web{closed: true}
	if err := w.open("https://www.douyin.com/"); err == nil {
		t.Fatal("closed window reopened a page")
	}
	w.touch()
	if w.idle != nil {
		t.Fatal("closed web scheduled another idle timer")
	}
}
