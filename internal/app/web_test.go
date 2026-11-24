package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
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

func TestWatchedPiggybacksOnWarmWeb(t *testing.T) {
	var told []string
	a := &app{signedIn: true, posted: make(chan func(), 16)}
	a.web.a = a
	a.sendWatched = func(id, uid string) error { told = append(told, id+"/"+uid); return nil }

	a.markWatched(dy.Video{ID: "v1", AuthorUID: "u1"})
	if a.watchSending || len(a.watched) != 1 || a.watchTimer == nil {
		t.Fatalf("cold queue: sending=%v waiting=%d timer=%v", a.watchSending, len(a.watched), a.watchTimer != nil)
	}

	a.web.v = &mygo.WebView{}
	defer func() {
		if a.web.idle != nil {
			a.web.idle.Stop()
		}
		if a.watchTimer != nil {
			a.watchTimer.Stop()
		}
	}()
	done := make(chan struct{})
	go func() {
		release := a.web.use()
		release()
		close(done)
	}()
	for i := 0; i < 2; i++ {
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for use() UI callback")
		}
	}
	<-done
	if !a.watchSending || a.watchTimer != nil {
		t.Fatalf("warm release did not flush watched: sending=%v timer=%v", a.watchSending, a.watchTimer != nil)
	}
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("watched flush did not complete")
	}
	if len(told) != 1 || told[0] != "v1/u1" || len(a.watched) != 0 {
		t.Fatalf("flushed=%q waiting=%d", told, len(a.watched))
	}

	// Each send uses the page, which would keep it up as long as videos are
	// watched: what comes after waits for the next batch.
	a.markWatched(dy.Video{ID: "v2", AuthorUID: "u2"})
	if a.watchSending || len(a.watched) != 1 || a.watchTimer == nil || len(told) != 1 {
		t.Fatalf("watched while warm: sending=%v waiting=%d timer=%v told=%q", a.watchSending, len(a.watched), a.watchTimer != nil, told)
	}
}

func TestSignPageStateResetOnDestroy(t *testing.T) {
	a := &app{}
	w := &a.web
	w.a, w.signOnly = a, true
	w.destroy()
	if w.signOnly {
		t.Fatal("destroy left signOnly set")
	}
}
