package app

import (
	"context"
	"errors"
	"log"
	"os"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
)

// web is the app's Douyin page (dy.Web), in WebKit: the user logs in there
// by scanning its code, and writes go from it, signed and bound to this
// device by Douyin's own scripts. It is made when first wanted, hidden,
// and let go after webIdle without use, unless it shows. Its fields belong
// to the UI goroutine; its dy.Web methods are for the others.
type web struct {
	a      *app
	v      *mygo.WebView
	layout bool // laid out under the app, for a script that finds things by their place
	idle   *time.Timer
}

const (
	douyinHome = "https://www.douyin.com/user/self" // no video plays there; logged out, it shows the login
	webIdle    = 10 * time.Minute
	// safariUA ends WebKit's own user agent as Safari's, which Douyin's pages
	// expect. Fixed, not read from Safari: the user agent is part of the
	// device the login is bound to.
	safariUA = "Version/27.2 Safari/605.1.15"
)

// call runs fn on the UI goroutine and waits for it; not from there.
func (a *app) call(fn func()) {
	done := make(chan struct{})
	a.post(func() { fn(); close(done) })
	<-done
}

// open makes the page at url, unless there is one: there it goes to url.
func (w *web) open(url string) error {
	defer w.touch()
	if w.v != nil {
		if url != "" {
			return w.v.Page().LoadURL(url)
		}
		return nil
	}
	if w.a.win == nil {
		return errors.New("窗口还没打开")
	}
	v, err := w.a.win.NewWebView(mygo.WebViewOptions{URL: url, Page: mygo.PageOptions{UserAgentSuffix: safariUA}})
	if err != nil {
		return err
	}
	v.Page().SetAudioMuted(true)
	w.v = v
	return nil
}

// touch puts off letting the page go.
func (w *web) touch() {
	if w.idle != nil {
		w.idle.Stop()
	}
	w.idle = time.AfterFunc(webIdle, func() {
		w.a.post(func() {
			if w.v == nil {
				return
			}
			if w.a.loginOpen || w.layout {
				w.touch()
				return
			}
			w.v.Destroy()
			w.v = nil
		})
	})
}

// page is the page, made at douyinHome if there is none, once its
// scripts are up: Douyin's security SDK (bdms), which signs what it sends.
func (w *web) page(ctx context.Context) (*mygo.Page, error) {
	var p *mygo.Page
	var err error
	w.a.call(func() {
		if w.v == nil {
			err = w.open(douyinHome)
		}
		if err == nil {
			w.touch()
			p = w.v.Page()
		}
	})
	if err != nil {
		return nil, err
	}
	for {
		up, err := p.EvalContext(ctx, `return document.readyState === "complete" && typeof window.bdms === "object" && location.hostname.endsWith("douyin.com")`)
		if err == nil && up == true {
			return p, nil
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("抖音页面没载入")
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (w *web) Eval(ctx context.Context, js string) ([]byte, error) {
	p, err := w.page(ctx)
	if err != nil {
		return nil, err
	}
	v, err := p.EvalContext(ctx, "return JSON.stringify(await (async () => {\n"+js+"\n})())")
	s, _ := v.(string)
	return []byte(s), err
}

func (w *web) Load(ctx context.Context, url string, layout bool) error {
	var err error
	w.a.call(func() {
		w.layout = layout
		err = w.open(url)
	})
	if err != nil || url == "" {
		return err
	}
	time.Sleep(500 * time.Millisecond) // past the page before, still complete
	_, err = w.page(ctx)
	return err
}

func (w *web) Cookies() ([]dy.Cookie, error) {
	list, err := mygo.Cookies.List(mygo.CookieFilter{Domain: "douyin.com"})
	if err != nil {
		return nil, err
	}
	cs := make([]dy.Cookie, len(list))
	for i, k := range list {
		cs[i] = dy.Cookie{Name: k.Name, Value: k.Value, Domain: k.Domain, Path: k.Path, Expires: k.Expires.Unix(),
			Secure: k.Secure, HTTPOnly: k.HTTPOnly}
		if k.Expires.IsZero() {
			cs[i].Expires = 0
		}
	}
	return cs, nil
}

// startLogin follows the login WebKit keeps: logged in, the client reads
// with its cookies; not, the client goes on anonymous and the login shows,
// the first time and after a logout.
func (a *app) startLogin() {
	go func() {
		cs, err := a.web.Cookies()
		in := err == nil && dy.LoggedIn(cs)
		if in {
			err = a.c.SaveCookies(cs)
		} else {
			err = a.c.Logout() // cookies.json from elsewhere: no login of this device
		}
		a.post(func() {
			a.signedIn = in
			if err != nil {
				a.toast = "登录状态没读到：" + err.Error()
			}
			a.fill()
			if in {
				a.loadAccount()
			} else {
				a.openLogin()
			}
		})
	}()
}

// openLogin shows Douyin's login, its code to scan with the Douyin app,
// and waits for the login to come.
func (a *app) openLogin() {
	if a.loginOpen {
		return
	}
	if err := a.web.open(douyinHome); err != nil {
		a.toast = "登录页打不开：" + err.Error()
		return
	}
	a.loginOpen = true
	a.loginGen++
	gen := a.loginGen
	go func() {
		for {
			time.Sleep(time.Second)
			var open bool
			a.call(func() { open = a.loginOpen && a.loginGen == gen })
			if !open {
				return
			}
			if cs, err := a.web.Cookies(); err != nil || !dy.LoggedIn(cs) {
				continue
			}
			time.Sleep(1500 * time.Millisecond) // the rest of the login's cookies
			cs, err := a.web.Cookies()
			if err == nil {
				err = a.c.SaveCookies(cs)
			}
			a.post(func() {
				a.loginOpen = false
				if err != nil {
					a.toast = "登录没保存：" + err.Error()
					return
				}
				a.signedIn = true
				a.toast = "已登录"
				a.loadAccount()
				a.web.open(douyinHome) // the page Douyin went to after the login may play
			})
			a.c.RefreshGuard()
			return
		}
	}()
}

// logout forgets the login: WebKit's Douyin cookies, the client's, and
// what the account showed.
func (a *app) logout() {
	a.signedIn = false
	if a.isMine(a.cur()) {
		a.root(a.picks)
	}
	a.me, a.follows, a.mine, a.later = dy.User{}, nil, nil, nil
	if a.web.v != nil {
		a.web.v.Destroy()
		a.web.v = nil
	}
	go func() {
		err := a.c.Logout()
		if list, lerr := mygo.Cookies.List(mygo.CookieFilter{Domain: "douyin.com"}); lerr == nil {
			for _, k := range list {
				mygo.Cookies.Delete(k)
			}
		}
		a.post(func() {
			if err != nil {
				a.toast = "退出登录时出错：" + err.Error()
			} else {
				a.toast = "已退出登录"
			}
		})
	}()
}

// loginPanel is Douyin's login page over the window while the login shows.
func (a *app) loginPanel(c *ui.Context) {
	if !a.loginOpen || a.web.v == nil {
		return
	}
	w, h := c.Size()
	ui.Box(c.Key("login")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(ui.RGBA(0, 0, 0, 0.55)).Center().
		HandleInput(func(ev ui.InputEvent) bool { return ev.Kind != ui.InputScroll }).Children(func() {
		ui.Column(c).Width(min(960, w-48)).Height(min(660, h-48)).Radius(14).Clip().Background(ui.Hex("#252632")).
			Border(0.5, ui.RGBA(255, 255, 255, 0.12)).Children(func() {
			ui.Row(c).Height(52).PaddingX(18).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, "登录抖音").FontSize(15).FontWeight(700).TextColor(white)
					ui.Text(c, "打开抖音 App 扫一扫，或者用手机验证码登录").FontSize(12).TextColor(pal.muted)
				})
				if ui.Button(c.Key("later"), "以后再说").Clicked() {
					a.loginOpen = false
				}
			})
			ui.WebView(c, a.web.v).Grow(1)
		})
	})
}

var _ dy.Web = (*web)(nil)

// liveWriteCheck, with DY_LIVE_WRITE naming a video neither liked, saved
// nor in 稍后再看, likes, saves and puts it in 稍后再看 through the page,
// takes each back, checks what Douyin says after each, and quits: 0 when
// all held. It writes to the account, so it runs only when asked.
func (a *app) liveWriteCheck(id string) {
	fail := func(f string, v ...any) { log.Printf("live write: "+f, v...); os.Exit(1) }
	for in := false; !in; time.Sleep(time.Second) {
		a.call(func() { in = a.signedIn })
	}
	c := a.c
	state := func() dy.Video {
		v, err := c.Video(id)
		if err != nil {
			fail("read: %v", err)
		}
		return v
	}
	later := func() bool {
		vs, err := c.WatchLater()()
		if err != nil {
			fail("稍后再看: %v", err)
		}
		return slices.ContainsFunc(vs, func(v dy.Video) bool { return v.ID == id })
	}
	if v := state(); v.Liked || v.Saved || later() {
		fail("pick a video neither liked, saved nor in 稍后再看")
	}
	ok := true
	for _, s := range []struct {
		name         string
		do           func() error
		liked, saved bool
	}{
		{"like", func() error { return c.SetLiked(id, true) }, true, false},
		{"unlike", func() error { return c.SetLiked(id, false) }, false, false},
		{"save", func() error { return c.SetSaved(id, true) }, false, true},
		{"unsave", func() error { return c.SetSaved(id, false) }, false, false},
	} {
		start := time.Now()
		if err := s.do(); err != nil {
			fail("%s: %v", s.name, err)
		}
		took := time.Since(start)
		var v dy.Video
		for range 10 { // what Douyin says of the video catches up in a few seconds
			time.Sleep(time.Second)
			if v = state(); v.Liked == s.liked && v.Saved == s.saved {
				break
			}
		}
		if v.Liked != s.liked || v.Saved != s.saved {
			ok = false
		}
		log.Printf("live write: %s in %v, then liked %v saved %v", s.name, took.Round(10*time.Millisecond), v.Liked, v.Saved)
	}
	for _, on := range []bool{true, false} {
		start := time.Now()
		if err := c.SetWatchLater(id, on); err != nil {
			fail("稍后再看 %v: %v", on, err)
		}
		took := time.Since(start)
		held := false
		for range 15 {
			time.Sleep(time.Second)
			if held = later() == on; held {
				break
			}
		}
		ok = ok && held
		log.Printf("live write: 稍后再看 %v in %v, list follows %v", on, took.Round(10*time.Millisecond), held)
	}
	if _, err := c.Self(); err != nil {
		fail("the login after the writes: %v", err)
	}
	if err := c.RefreshGuard(); err != nil {
		fail("guard: %v", err)
	}
	log.Printf("live write: all held %v", ok)
	if !ok {
		os.Exit(1)
	}
	os.Exit(0)
}
