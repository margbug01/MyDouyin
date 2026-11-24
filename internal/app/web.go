package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
)

// web is the app's Douyin page (dy.Web), in WebKit: the user logs in there
// by scanning its code, and writes go from it, signed and bound to this
// device by Douyin's own scripts. Writes and pageGet start on signPage
// (signpage.go, ~60-85 MB) with Douyin's official bdms, ucSecureConsumerSDK,
// and DTraitSDK, and fall back to the full douyinHome page if local signing
// material is missing or the signer does not reach ready. Login, captcha, and
// ShareTo load the real Douyin page directly. It is let go after webIdle
// without use, unless it shows. Its fields belong to the UI goroutine; its
// dy.Web methods are for the others.
type web struct {
	a        *app
	v        *mygo.WebView
	signOnly bool // loaded signPage rather than a full Douyin page
	layout   bool // laid out under the app, for a script that finds things by their place
	idle     *time.Timer
	idleGen  int
	busy     int // scripts or navigation still using the page
	closed   bool
}

const (
	douyinHome = "https://www.douyin.com/user/self" // no video plays there; logged out, it shows the login
	webIdle    = 10 * time.Second
	// safariUA ends WebKit's own user agent as Safari's, which Douyin's pages
	// expect. Fixed, not read from Safari: the user agent is part of the
	// device the login is bound to.
	safariUA = "Version/27.2 Safari/605.1.15"
)

// call runs fn on the UI goroutine and waits for it; not from there.
// On a real window it uses RunOnMain so non-visual state updates (web busy/idle)
// do not trigger redundant full UI frame redraws.
func (a *app) call(fn func()) {
	if a.win != nil {
		mygo.RunOnMain(fn)
		return
	}
	done := make(chan struct{})
	a.post(func() { fn(); close(done) })
	<-done
}

// open makes the page at url, unless there is one: there it goes to url.
func (w *web) open(url string) error {
	if w.closed {
		return errors.New("窗口已关闭")
	}
	defer w.touch()
	if w.v != nil {
		if url != "" {
			w.signOnly = false
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
	w.signOnly = false
	return nil
}

// touch puts off letting the page go.
func (w *web) touch() {
	w.idleGen++
	if w.idle != nil {
		w.idle.Stop()
		w.idle = nil
	}
	if w.v == nil || w.closed {
		return
	}
	gen := w.idleGen
	w.idle = time.AfterFunc(webIdle, func() {
		w.a.post(func() { w.expire(gen) })
	})
}

func (w *web) expire(gen int) {
	// A stopped timer may already have posted its callback before another use.
	if gen != w.idleGen || w.v == nil {
		return
	}
	if w.busy > 0 || w.a.loginOpen || w.layout {
		w.touch()
		return
	}
	w.destroy()
}

func (w *web) destroy() {
	if w.v != nil {
		w.v.Destroy()
		w.v = nil
	}
	w.signOnly = false
	w.touch()
}

func (w *web) warmIdle() bool {
	return w.v != nil && !w.closed && w.busy == 0 && !w.layout && (w.a == nil || !w.a.loginOpen)
}

// use pins the page until an evaluation or navigation finishes, even past webIdle.
func (w *web) use() func() {
	w.a.call(func() { w.busy++; w.touch() })
	return func() {
		w.a.call(func() {
			w.busy--
			w.touch()
			w.a.flushWatchedIfWarm()
		})
	}
}

const signPageCheckJS = `
if (!location.hostname.endsWith("douyin.com") || document.readyState !== "complete") return "wait";
if (!window.__mdSignPage) return typeof window.bdms === "object" ? "ready" : "wait";
if (typeof window.bdms !== "object" || window.__mdSignReady !== true) return "wait";
if (typeof window.ucSecureConsumerSDK !== "object" || typeof window.DTraitSDK === "undefined") return "fallback";
const st = window.ucSecureConsumerSDK.getStatus && window.ucSecureConsumerSDK.getStatus();
if (!st) return "fallback";
if (st.state === "ready") return "ready";
if (st.state === "starting") return "wait";
return "fallback";
`

// fullPageCheckJS rejects the lightweight signPage so callers that navigated to
// a full Douyin page (verify, Load, signPage fallback) wait until WebKit commits
// the real page rather than evaluating against the dying signPage context.
const fullPageCheckJS = `
if (!location.hostname.endsWith("douyin.com") || document.readyState !== "complete") return "wait";
if (window.__mdSignPage) return "wait";
return typeof window.bdms === "object" ? "ready" : "wait";
`

const signPageTimeout = 6 * time.Second

// page returns the signing page once its security SDKs are ready. When no page
// exists yet it loads signPage first, falling back to full douyinHome if the
// lightweight host cannot initialize ticket-guard signing.
func (w *web) page(ctx context.Context) (*mygo.Page, error) {
	var p *mygo.Page
	var err error
	var startedSign bool
	var wantFull bool
	w.a.call(func() {
		if w.v == nil {
			if err = w.open(""); err == nil {
				w.signOnly = true
				startedSign = true
				w.v.Page().LoadHTML(signPage, douyinHome)
			}
		}
		if err == nil {
			wantFull = !w.signOnly
			w.touch()
			p = w.v.Page()
		}
	})
	if err != nil {
		return nil, err
	}
	startAt := time.Now()
	fallbackAt := startAt.Add(signPageTimeout)
	for {
		checkJS := signPageCheckJS
		if wantFull {
			checkJS = fullPageCheckJS
		}
		state, err := p.EvalContext(ctx, checkJS)
		if err == nil {
			switch state {
			case "ready":
				return p, nil
			case "fallback":
				if startedSign {
					startedSign = false
					wantFull = true
					w.a.call(func() { _ = w.open(douyinHome) })
				}
			}
		}
		if startedSign && time.Now().After(fallbackAt) {
			startedSign = false
			wantFull = true
			w.a.call(func() { _ = w.open(douyinHome) })
		}
		poll := 150 * time.Millisecond
		if time.Since(startAt) < time.Second {
			poll = 60 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("抖音页面没载入")
		case <-time.After(poll):
		}
	}
}

func (w *web) Eval(ctx context.Context, js string) ([]byte, error) {
	defer w.use()()
	p, err := w.page(ctx)
	if err != nil {
		return nil, err
	}
	v, err := p.EvalContext(ctx, "return JSON.stringify(await (async () => {\n"+js+"\n})())")
	s, _ := v.(string)
	return []byte(s), err
}

func (w *web) Load(ctx context.Context, url string, layout bool) error {
	defer w.use()()
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
		if in { // a session cookie Douyin may have let go since: ask over HTTP first
			if ok, cerr := a.c.CheckLogin(cs); cerr == nil {
				in = ok
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				in = a.web.pageSignedIn(ctx)
				cancel()
			}
		}
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

// login is Douyin's login while it shows: its code, which the page under
// the app makes and renews, drawn natively, and how the scan goes.
type login struct {
	src    string     // the code's data URL, as the page shows it
	qr     *ui.Bitmap // the code, drawn
	status string     // what the scan is at, for the panel
	verify bool       // Douyin asks more than the scan: its page shows instead
	stop   func()     // ends what waits for the verification (verifyFor), if any
	retry  []func()   // what runs once Douyin's verification is passed
}

// loginJS reads the login the page shows: its code, the text about it, and
// whether Douyin asks to verify more than the scan. A code that expired is
// renewed, as its 刷新 does.
const loginJS = `
const img = [...document.querySelectorAll("img")].find(e => { const r = e.getBoundingClientRect(); return r.width >= 100 && Math.abs(r.width - r.height) < 8 && e.src.startsWith("data:image/"); });
let near = "";
if (img) { let p = img; for (let i = 0; i < 4 && p.parentElement; i++) p = p.parentElement; near = p.innerText; }
if (!window.__mdQR) {
  window.__mdQR = { status: 0 };
  const seen = (u, t) => { if (/check_qrconnect/.test(String(u))) { try { window.__mdQR.status = Number(JSON.parse(t).data.status) || 0; } catch {} } };
  const of = window.fetch;
  window.fetch = function (input) { const p = of.apply(this, arguments); p.then(r => r.clone().text().then(t => seen(typeof input === "string" ? input : input && input.url, t))).catch(() => {}); return p; };
  const open = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (m, u) { this.addEventListener("load", () => { try { seen(u, this.responseText); } catch {} }); return open.apply(this, arguments); };
}
const body = document.body ? document.body.innerText : "";
const expired = /失效|过期/.test(near);
if (expired) { const r = [...document.querySelectorAll("*")].find(e => e.children.length === 0 && /刷新/.test(e.textContent) && e.getBoundingClientRect().width > 0); if (r) r.click(); }
return { src: img ? img.src : "", scanned: window.__mdQR.status === 2 || /扫码成功|确认登录|手机上确认/.test(near), expired, verify: /身份验证|安全验证|短信验证|二次验证/.test(body) && !img };`

// sessionID is the session cookie of cs, or "".
func sessionID(cs []dy.Cookie) string {
	for _, c := range cs {
		if c.Name == "sessionid" {
			return c.Value
		}
	}
	return ""
}

// pageSignedIn asks Douyin, from the page, whether its login holds.
func (w *web) pageSignedIn(ctx context.Context) bool {
	b, err := w.Eval(ctx, `const r = await (await fetch("/aweme/v1/web/user/profile/self/?device_platform=webapp&aid=6383&channel=channel_pc_web", { credentials: "include" })).json();
return r.status_code === 0 && !!r.user;`)
	// The reply nests other status codes (user_collect_count's is 0 logged out): only its own counts.
	return err == nil && string(b) == "true"
}

// openLogin shows the login: the code of Douyin's login page, which loads
// under the app, to scan with the Douyin app. The login is in once Douyin,
// asked from the page, says so; a cookie alone is not enough.
func (a *app) openLogin() {
	if a.loginOpen {
		return
	}
	if err := a.web.open(douyinHome); err != nil {
		a.toast = "登录页打不开：" + err.Error()
		return
	}
	a.loginOpen, a.web.layout = true, true
	a.login = login{status: "二维码载入中…"}
	a.loginGen++
	gen := a.loginGen
	go func() {
		was, _ := a.web.Cookies() // a session Douyin let go may linger: the login is a new one
		old := sessionID(was)
		defer a.post(func() {
			if a.loginGen == gen {
				a.web.layout = false
			}
		})
		for {
			time.Sleep(400 * time.Millisecond)
			var open bool
			a.call(func() { open = a.loginOpen && a.loginGen == gen })
			if !open {
				return
			}
			// The cookie first, which asks no page: once confirmed on the phone the
			// page goes elsewhere, and an evaluation would wait for it to load.
			if cs, err := a.web.Cookies(); err == nil && dy.LoggedIn(cs) && sessionID(cs) != old {
				a.post(func() {
					if a.loginGen == gen {
						a.loginOpen, a.login.status = false, ""
						a.toast = "扫码成功，正在登录…"
					}
				})
				a.finishLogin(gen)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var st struct {
				Src                      string
				Scanned, Expired, Verify bool
			}
			if b, err := a.web.Eval(ctx, loginJS); err == nil {
				json.Unmarshal(b, &st)
			}
			cancel()
			a.post(func() {
				if a.loginGen != gen {
					return
				}
				l := &a.login
				if st.Src != "" && st.Src != l.src {
					if data, err := base64.StdEncoding.DecodeString(st.Src[strings.IndexByte(st.Src, ',')+1:]); err == nil {
						if bmp, err := ui.DecodeBitmap(data); err == nil {
							l.src, l.qr = st.Src, bmp
						}
					}
				}
				l.verify = st.Verify
				switch {
				case st.Expired:
					l.status = "二维码过期了，正在换一张"
				case st.Scanned:
					l.status = "已扫码，请在手机上确认登录"
				case l.qr != nil:
					l.status = "打开抖音 App，点左上角的扫一扫"
				}
			})
		}
	}()
}

// finishLogin, once a new session cookie came, asks Douyin from the page
// whether it takes it (the page may still be loading the login's redirect),
// then keeps it; one Douyin does not take opens the login again, where its
// page shows what more it asks.
func (a *app) finishLogin(gen int) {
	in := false
	deadline := time.Now().Add(25 * time.Second)
	for !in && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		in = a.web.pageSignedIn(ctx)
		cancel()
		if !in {
			time.Sleep(500 * time.Millisecond)
		}
	}
	var err error
	if in {
		var cs []dy.Cookie
		if cs, err = a.web.Cookies(); err == nil {
			err = a.c.SaveCookies(cs)
		}
	}
	a.post(func() {
		if a.loginGen != gen {
			return
		}
		switch {
		case !in:
			a.toast = "抖音没认这次登录，请按页面提示再试一次"
			a.loginOpen, a.web.layout = true, true
			a.login.verify = true
		case err != nil:
			a.toast = "登录没保存：" + err.Error()
		default:
			a.signedIn = true
			a.toast = "已登录"
			a.loadAccount()
		}
	})
	if in && err == nil {
		a.c.RefreshGuard()
	}
}

// logout forgets the login: WebKit's Douyin cookies, the client's, and
// what the account showed.
func (a *app) logout() {
	a.signedIn = false
	if a.isMine(a.pages[0]) {
		a.root(a.picks)
	}
	a.me, a.follows, a.mine, a.later, a.stacks = dy.User{}, nil, nil, nil, nil
	a.watched = nil // the account's, not the next one's
	if a.watchTimer != nil {
		a.watchTimer.Stop()
		a.watchTimer = nil
	}
	a.web.destroy()
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

// loginPanel is the login over the window while it shows: the code to
// scan and how the scan goes, natively; Douyin's own page only when it asks
// to verify more than the scan.
func (a *app) loginPanel(c *ui.Context) {
	wasOpen := a.loginOpen
	l := &a.login
	verify := l.verify && a.web.v != nil
	ui.DialogBase(c.Key("login"), &a.loginOpen, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.55))
		panel.Radius(16).Clip().Background(pal.popover).Border(1, pal.popoverBd).
			Shadow(0, 10, 30, -6, pal.cardShadow).Label("扫码登录抖音")
		if verify {
			w, h := c.Size()
			panel.Width(min(960, w-48)).Height(min(660, h-48))
			ui.Row(c).Height(52).PaddingX(18).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "抖音要求验证身份，请按页面提示完成").FontSize(14).FontWeight(600).TextColor(pal.strong).Grow(1)
				if ui.Button(c.Key("later"), "以后再说").Clicked() {
					a.loginOpen = false
				}
			})
			ui.WebView(c, a.web.v).Grow(1)
			return
		}
		panel.Width(340).Height(430)
		if a.web.v != nil { // Douyin's page, laid out for its code to show, under what covers it
			ui.WebView(c, a.web.v).Absolute().Top(0).Left(0).Right(0).Bottom(0)
		}
		ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(pal.popover).
			Padding(22, 24, 20, 24).Gap(14).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "扫码登录抖音").FontSize(17).FontWeight(700).TextColor(pal.strong)
			ui.Box(c).Size(220, 220).Radius(12).Background(white).Border(1, pal.hairline).Center().Children(func() {
				if l.qr != nil {
					ui.Image(c, l.qr).Size(196, 196).Opacity(onOff(strings.HasPrefix(l.status, "已扫码"), float32(0.25), 1))
				} else {
					ui.Spinner(c)
				}
			})
			ui.Text(c, l.status).FontSize(13.5).TextColor(pal.text)
			ui.Text(c, "登录后可以点赞、收藏、发弹幕，看关注和我的").FontSize(11.5).TextColor(pal.faint)
			if ui.Button(c.Key("later"), "以后再说").Clicked() {
				a.loginOpen = false
			}
		})
	})
	if wasOpen && !a.loginOpen {
		if a.login.stop != nil {
			a.login.stop()
		}
		a.loginGen++
		a.web.layout = false
		a.login = login{}
		a.web.touch()
	}
}

// verify shows Douyin's verification (dy.ErrVerify) over the window: its
// page asks path, a read Douyin held back, itself, and Douyin's scripts
// bring up the captcha there, for the user to pass. Once the page has its
// answer the verification closes and retry runs.
func (a *app) verify(path string, retry func()) {
	if a.loginOpen {
		if a.login.verify && a.login.stop != nil { // one verification open already: runs this too once passed
			a.login.retry = append(a.login.retry, retry)
		}
		return
	}
	wasFull := a.web.v != nil && !a.web.signOnly
	if err := a.web.open(douyinHome); err != nil {
		a.toast = "验证页打不开：" + err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	a.loginOpen, a.web.layout = true, true
	a.login = login{verify: true, stop: cancel, retry: []func(){retry}}
	a.loginGen++
	gen := a.loginGen
	go func() {
		defer cancel()
		if wasFull {
			// Reloading douyinHome over a previous full page: wait past the old
			// document's readyState === "complete" before polling for the new one.
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
		path, _ := json.Marshal(path)
		b, err := a.web.Eval(ctx, "const r = await fetch("+string(path)+`, { credentials: "include" });
return r.status === 200 && (await r.text()).length > 0;`)
		passed := err == nil && string(b) == "true"
		if passed && a.c != nil {
			a.c.SyncCookies()
			_ = a.c.RefreshGuard()
			// Keep replies routed through the just-verified page during cooldown:
			// Douyin continues holding back direct Go requests right after page verification.
			a.c.KeepReplyPage()
		}
		a.post(func() {
			if a.loginGen != gen { // closed already
				return
			}
			retries := a.login.retry
			a.loginOpen, a.web.layout, a.login = false, false, login{}
			a.loginGen++
			a.web.touch()
			if passed {
				for _, retry := range retries {
					retry()
				}
			} else {
				a.toast = "验证没有完成"
			}
		})
	}()
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
		if s.name == "like" {
			var signOnly bool
			var pid int
			a.call(func() {
				signOnly = a.web.signOnly
				if a.web.v != nil {
					pid = a.web.v.Page().ProcessID()
				}
			})
			log.Printf("live write: web signOnly=%v pid=%d", signOnly, pid)
		}
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
	// A comment's like, read back from the comments' list.
	cms, err := c.Comments(id)()
	if err != nil {
		fail("comments: %v", err)
	}
	i := slices.IndexFunc(cms, func(cm dy.Comment) bool { return !cm.Liked })
	if i < 0 {
		fail("no comment not liked yet")
	}
	cid := cms[i].ID
	for _, on := range []bool{true, false} {
		start := time.Now()
		if err := c.SetCommentLiked(id, cid, false, on); err != nil {
			fail("comment like %v: %v", on, err)
		}
		took := time.Since(start)
		held := false
		for range 10 {
			time.Sleep(time.Second)
			cms, err := c.Comments(id)()
			if err != nil {
				fail("comments: %v", err)
			}
			if j := slices.IndexFunc(cms, func(cm dy.Comment) bool { return cm.ID == cid }); j >= 0 && cms[j].Liked == on {
				held = true
				break
			}
		}
		ok = ok && held
		log.Printf("live write: comment like %v in %v, list follows %v", on, took.Round(10*time.Millisecond), held)
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
