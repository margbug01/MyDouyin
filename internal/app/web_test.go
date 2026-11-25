package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
)

func TestWebIdleKeepsPageInUse(t *testing.T) {
	for _, held := range []string{"script", "login", "layout", "stale timer", "comments"} {
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
			case "comments": // their replies go through the page
				a.signedIn, a.sheet = true, &sheet{}
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

func TestFullPageCheckRejectsSignPage(t *testing.T) {
	if !strings.Contains(fullPageCheckJS, `if (window.__mdSignPage) return "wait"`) {
		t.Fatalf("fullPageCheckJS must wait while signPage is still mounted: %s", fullPageCheckJS)
	}
}

func TestResolveStartupLogin(t *testing.T) {
	signedCookies := []dy.Cookie{{Name: "sessionid", Value: "abc"}}

	t.Run("http valid skips page check", func(t *testing.T) {
		pageCalled := false
		got := resolveStartupLogin(signedCookies, nil,
			func([]dy.Cookie) (bool, error) { return true, nil },
			func() loginCheck { pageCalled = true; return loginCheckInvalid },
		)
		if got != startupLoginSignedIn || pageCalled {
			t.Fatalf("got=%v pageCalled=%v", got, pageCalled)
		}
	})

	t.Run("http false negative confirmed valid by page", func(t *testing.T) {
		pageCalled := false
		got := resolveStartupLogin(signedCookies, nil,
			func([]dy.Cookie) (bool, error) { return false, nil },
			func() loginCheck { pageCalled = true; return loginCheckValid },
		)
		if got != startupLoginSignedIn || !pageCalled {
			t.Fatalf("got=%v pageCalled=%v", got, pageCalled)
		}
	})

	t.Run("inconclusive page check with sessionid keeps login", func(t *testing.T) {
		got := resolveStartupLogin(signedCookies, nil,
			func([]dy.Cookie) (bool, error) { return false, errors.New("timeout") },
			func() loginCheck { return loginCheckUnknown },
		)
		if got != startupLoginSignedIn {
			t.Fatalf("got=%v want startupLoginSignedIn", got)
		}
	})

	t.Run("explicit invalid logs out", func(t *testing.T) {
		got := resolveStartupLogin(signedCookies, nil,
			func([]dy.Cookie) (bool, error) { return false, nil },
			func() loginCheck { return loginCheckInvalid },
		)
		if got != startupLoginLoggedOut {
			t.Fatalf("got=%v want startupLoginLoggedOut", got)
		}
	})

	t.Run("missing session cookie logs out without checks", func(t *testing.T) {
		httpCalled, pageCalled := false, false
		got := resolveStartupLogin(nil, nil,
			func([]dy.Cookie) (bool, error) { httpCalled = true; return true, nil },
			func() loginCheck { pageCalled = true; return loginCheckValid },
		)
		if got != startupLoginLoggedOut || httpCalled || pageCalled {
			t.Fatalf("got=%v http=%v page=%v", got, httpCalled, pageCalled)
		}
	})

	t.Run("cookie read error keeps existing state", func(t *testing.T) {
		httpCalled, pageCalled := false, false
		got := resolveStartupLogin(nil, errors.New("cookie store busy"),
			func([]dy.Cookie) (bool, error) { httpCalled = true; return false, nil },
			func() loginCheck { pageCalled = true; return loginCheckInvalid },
		)
		if got != startupLoginKeep || httpCalled || pageCalled {
			t.Fatalf("got=%v http=%v page=%v", got, httpCalled, pageCalled)
		}
	})
}

func TestLoginLostKeepsLoginUnlessInvalid(t *testing.T) {
	for _, tc := range []struct {
		name         string
		check        loginCheck
		wantSignedIn bool
	}{
		{name: "unknown", check: loginCheckUnknown, wantSignedIn: true},
		{name: "valid", check: loginCheckValid, wantSignedIn: true},
		{name: "invalid", check: loginCheckInvalid, wantSignedIn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{signedIn: true}
			a.web.a = a
			a.applyLoginRecheck("点赞", tc.check)
			if a.signedIn != tc.wantSignedIn {
				t.Fatalf("signedIn=%v want=%v toast=%q", a.signedIn, tc.wantSignedIn, a.toast)
			}
			if tc.wantSignedIn && !strings.Contains(a.toast, "登录还在") {
				t.Fatalf("expected keep-login toast, got %q", a.toast)
			}
		})
	}
}

func TestLoginPanelSlowFallback(t *testing.T) {
	a := &app{
		loginOpen: true,
		login:     login{status: "二维码载入中…"},
	}
	a.web.a, a.web.v = a, &mygo.WebView{}
	tt := ui.NewTester(func(c *ui.Context) { a.loginPanel(c) }, 960, 700)
	tt.Frame()
	if tt.HasText("重试") || tt.HasText("网页登录") {
		t.Fatalf("slow controls shown before timeout: %q", tt.Texts())
	}

	a.login.qrSlow = true
	a.login.status = "二维码加载较慢，可重试或改用网页登录"
	tt.Frame()
	if !tt.HasText("重试") || !tt.HasText("网页登录") || !tt.HasText("以后再说") {
		t.Fatalf("slow login panel missing fallback actions: %q", tt.Texts())
	}
	if err := tt.Click("网页登录"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.login.verify || !tt.HasText("请在下方完成登录或身份验证") {
		t.Fatalf("clicking 网页登录 did not enter web view: verify=%v texts=%q", a.login.verify, tt.Texts())
	}
}
