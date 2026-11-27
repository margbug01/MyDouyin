package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"os"
	"runtime"
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
	// While the comments show, logged in, their replies and likes go through the page (warm).
	if w.busy > 0 || w.a.loginOpen || w.layout || w.a.sheet != nil && w.a.signedIn {
		w.touch()
		return
	}
	w.destroy()
}

// warm makes the page before it is asked for, so that what goes through it
// then takes a page read (0.3–0.4 s) rather than a page made anew first
// (about 1 s more, or several when signPage falls back to the full page;
// measured 2026-11-24).
func (w *web) warm() {
	if w.v != nil || w.closed || w.a == nil || w.a.win == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		w.page(ctx)
	}()
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

func (w *web) evalFullPageNow(ctx context.Context, js string) ([]byte, error) {
	defer w.use()()
	var p *mygo.Page
	var err error
	w.a.call(func() {
		switch {
		case w.v == nil:
			err = errors.New("抖音页面没打开")
		case w.signOnly:
			err = errors.New("当前为轻量签名页")
		default:
			w.touch()
			p = w.v.Page()
		}
	})
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

// readMark is a session cookie kept in WebKit's store for no site: a read
// that holds it is whole. WebKit answers a read with no cookies and no error
// right after its network process restarts (3 runs of 3 on macOS 27.2,
// 2026-11-27), and on WebKit 26 at the first read of a cold start, until a
// cookie is written (Capacitor #8700, measured on iOS 26.5). Taken for the
// store, such a read lost the login at launch: cookies.json went and the
// login showed, every launch (#4). WebView2 has shown neither, and keeps no mark.
var readMark = mygo.Cookie{Name: "mydouyin_whole_read", Value: "1", Domain: "whole-read.mydouyin.invalid", Path: "/"}

func isReadMark(k mygo.Cookie) bool { return k.Name == readMark.Name }

// wholeRead is a read of list that holds the mark, which it sets once when
// the first read misses it; with no mark, list's first answer.
func wholeRead(list func() ([]mygo.Cookie, error), mark func() error) ([]mygo.Cookie, error) {
	cs, err := list()
	if err != nil || mark == nil || slices.ContainsFunc(cs, isReadMark) {
		return cs, err
	}
	if err := mark(); err != nil {
		return nil, err
	}
	if cs, err = list(); err == nil && !slices.ContainsFunc(cs, isReadMark) {
		err = errors.New("WebKit 的 cookie 没读全")
	}
	return cs, err
}

// douyinCookies are the store's douyin.com cookies, from a whole read.
func douyinCookies() ([]mygo.Cookie, error) {
	var mark func() error
	if runtime.GOOS == "darwin" {
		mark = func() error { return mygo.Cookies.Set(readMark) }
	}
	all, err := wholeRead(func() ([]mygo.Cookie, error) { return mygo.Cookies.List(mygo.CookieFilter{}) }, mark)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(k mygo.Cookie) bool {
		host := strings.TrimPrefix(k.Domain, ".")
		return host != "douyin.com" && !strings.HasSuffix(host, ".douyin.com")
	}), nil
}

func (w *web) Cookies() ([]dy.Cookie, error) {
	list, err := douyinCookies()
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

type loginCheck uint8

const (
	loginCheckUnknown loginCheck = iota
	loginCheckValid
	loginCheckInvalid
)

type startupLoginAction uint8

const (
	startupLoginKeep startupLoginAction = iota
	startupLoginSignedIn
	startupLoginLoggedOut
)

func resolveStartupLogin(
	cs []dy.Cookie,
	cookieErr error,
	httpCheck func([]dy.Cookie) (bool, error),
	pageCheck func() loginCheck,
) startupLoginAction {
	if cookieErr != nil {
		return startupLoginKeep
	}
	if !dy.LoggedIn(cs) {
		return startupLoginLoggedOut
	}
	if httpCheck != nil {
		if ok, err := httpCheck(cs); err == nil && ok {
			return startupLoginSignedIn
		}
	}
	if pageCheck != nil && pageCheck() == loginCheckInvalid {
		return startupLoginLoggedOut
	}
	return startupLoginSignedIn
}

// startLogin follows the login WebKit keeps: logged in, the client reads
// with its cookies; not, the client goes on anonymous and the login shows,
// the first time and after a logout.
func (a *app) startLogin() {
	go func() {
		cs, err := a.web.Cookies()
		action := resolveStartupLogin(cs, err, a.c.CheckLogin, func() loginCheck {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return a.web.checkSignedIn(ctx)
		})
		switch action {
		case startupLoginSignedIn:
			err = a.c.SaveCookies(cs)
		case startupLoginLoggedOut:
			err = a.c.Logout() // cookies.json from elsewhere: no login of this device
		}
		a.post(func() {
			if a.isTikTok() {
				if a.dySnap == nil {
					a.dySnap = &platformSnapshot{}
				}
				switch action {
				case startupLoginSignedIn:
					a.dySnap.signedIn = true
					a.loadAccount()
				case startupLoginLoggedOut:
					a.dySnap.signedIn = false
				}
				return
			}
			if err != nil {
				a.toast = "登录状态没读到：" + err.Error()
			}
			switch action {
			case startupLoginKeep:
				a.fill()
			case startupLoginSignedIn:
				a.signedIn = true
				a.fill()
				a.loadAccount()
			case startupLoginLoggedOut:
				a.signedIn = false
				a.fill()
				if a.mode == modeDouyin {
					a.openLogin()
				}
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
	qrSlow bool
	verify bool     // Douyin asks more than the scan: its page shows instead
	hint   string   // over the page, instead of the usual line: its captcha does not load
	stop   func()   // ends what waits for the verification (verify), if any
	retry  []func() // what runs once Douyin's verification is passed
}

// captchaLoadTimeout is how long a captcha may stay hidden before the panel
// says it did not load: Douyin's shows within a second, and stays hidden for
// good when its frame (rmc.bytedance.com) cannot load, as behind a proxy that
// resolved it to a blocking page (2026-11-26).
const captchaLoadTimeout = 8 * time.Second

const captchaHint = "验证码没能载入：网络或代理可能拦截了抖音的验证服务（rmc.bytedance.com）"

// hiddenCaptcha follows looks at the page, one after another, for a captcha
// that stays hidden.
type hiddenCaptcha struct{ since time.Time }

// hint is the panel's hint after this look: captchaHint once the captcha has
// stayed hidden for captchaLoadTimeout, else "".
func (h *hiddenCaptcha) hint(hidden bool, now time.Time) string {
	if !hidden {
		h.since = time.Time{}
		return ""
	}
	if h.since.IsZero() {
		h.since = now
	}
	if now.Sub(h.since) >= captchaLoadTimeout {
		return captchaHint
	}
	return ""
}

// captchaState is whether the page holds Douyin's captcha, and whether it
// shows: its frame stays hidden until its own page has loaded.
type captchaState string

const (
	captchaNone    captchaState = "none"
	captchaShown   captchaState = "shown"
	captchaHidden  captchaState = "hidden"
	captchaUnknown captchaState = "" // the page did not answer
)

const captchaStateJS = `
const el = document.querySelector("iframe[src*='/verifycenter/captcha/'], #vc_captcha_wrapper, .captcha_verify_container, .vc-captcha-verify-container, #secsdk-captcha-drag-wrapper");
if (!el) return "none";
return getComputedStyle(el).visibility === "hidden" ? "hidden" : "shown";`

func (w *web) captchaState(ctx context.Context) captchaState {
	b, err := w.Eval(ctx, captchaStateJS)
	var s captchaState
	if err != nil || json.Unmarshal(b, &s) != nil {
		return captchaUnknown
	}
	return s
}

// loginJS reads the login the page shows: its code, the text about it, and
// whether Douyin asks to verify more than the scan. A code that expired is
// renewed, as its 刷新 does.
const loginJS = `
if (window.__mdSignPage) return { src: "", scanned: false, expired: false, verify: false, signedIn: false };
const vis = e => { if (!e) return false; const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
const img = [...document.querySelectorAll("img")].find(e => { const r = e.getBoundingClientRect(); return r.width >= 100 && Math.abs(r.width - r.height) < 8 && e.src && e.src.startsWith("data:image/"); });
let near = "";
if (img) { let p = img; for (let i = 0; i < 4 && p.parentElement; i++) p = p.parentElement; near = p.innerText || ""; }
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
if (expired) { const r = [...document.querySelectorAll("*")].find(e => e.children.length === 0 && /刷新/.test(e.textContent || "") && vis(e)); if (r) r.click(); }
const hasCaptcha = [...document.querySelectorAll(".captcha_verify_container, .vc-captcha-verify-container, #secsdk-captcha-drag-wrapper, iframe[src*='/verifycenter/captcha/'], #vc_captcha_wrapper")].some(vis)
  || [...document.querySelectorAll("#captcha_container > *")].some(e => e.id !== "nocaptcha-container" && !e.classList.contains("nocaptcha-container") && vis(e));
const verify = !img && (/身份验证|安全验证|短信验证|二次验证/.test(body) || hasCaptcha);
const capFrame = document.querySelector("iframe[src*='/verifycenter/captcha/']");
const captchaHidden = !!capFrame && getComputedStyle(capFrame).visibility === "hidden";
const signedIn = !img && !verify && document.readyState === "complete" && /抖音号/.test(body) && !/未登录|扫码登录/.test(body);
if (!img && !verify && !signedIn && !window.__mdNoTabClick && !window.__mdQRTabClicked) {
  const tab = [...document.querySelectorAll("button, div, span, li, a")].find(e => e.children.length === 0 && (e.textContent || "").trim() === "扫码登录" && vis(e));
  if (tab) { window.__mdQRTabClicked = true; tab.click(); }
}
return { src: img ? img.src : "", scanned: window.__mdQR.status === 2 || /扫码成功|确认登录|手机上确认/.test(near), expired, verify, signedIn, captchaHidden };`

// sessionID is the session cookie of cs, or "".
func sessionID(cs []dy.Cookie) string {
	for _, c := range cs {
		if c.Name == "sessionid" {
			return c.Value
		}
	}
	return ""
}

func (w *web) checkSignedIn(ctx context.Context) loginCheck {
	b, err := w.Eval(ctx, `try {
	const resp = await fetch("/aweme/v1/web/user/profile/self/?device_platform=webapp&aid=6383&channel=channel_pc_web", { credentials: "include" });
	const r = await resp.json();
	if (!r || typeof r.status_code !== "number") return "unknown";
	if (r.status_code === 0 && r.user && r.user.sec_uid) return "valid";
	if (r.status_code === 8 || (r.status_code === 0 && (!r.user || !r.user.sec_uid))) return "invalid";
	return "unknown";
} catch {
	return "unknown";
}`)
	// The reply nests other status codes (user_collect_count's is 0 logged out): only its own counts.
	if err != nil {
		return loginCheckUnknown
	}
	var res string
	if json.Unmarshal(b, &res) != nil {
		return loginCheckUnknown
	}
	switch res {
	case "valid":
		return loginCheckValid
	case "invalid":
		return loginCheckInvalid
	default:
		return loginCheckUnknown
	}
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
	a.pollLogin(a.loginGen)
}

func (a *app) reloadLogin() {
	if !a.loginOpen {
		return
	}
	if err := a.web.open(douyinHome); err != nil {
		a.toast = "登录页打不开：" + err.Error()
		return
	}
	a.web.layout = true
	a.login = login{status: "二维码载入中…"}
	a.loginGen++
	a.pollLogin(a.loginGen)
}

func (a *app) pollLogin(gen int) {
	go func() {
		was, _ := a.web.Cookies() // a session Douyin let go may linger: the login is a new one
		old := sessionID(was)
		started := time.Now()
		checkedExisting := false
		var captcha hiddenCaptcha
		defer a.post(func() {
			if a.loginGen == gen {
				a.web.layout = false
			}
		})
		for {
			time.Sleep(400 * time.Millisecond)
			var open, hasQR, inVerify bool
			a.call(func() {
				open = a.loginOpen && a.loginGen == gen
				hasQR = a.login.qr != nil
				inVerify = a.login.verify
			})
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
				Src                                               string
				Scanned, Expired, Verify, SignedIn, CaptchaHidden bool
			}
			js := loginJS
			if inVerify {
				js = "window.__mdNoTabClick = true;\n" + loginJS
			}
			if b, err := a.web.evalFullPageNow(ctx, js); err == nil {
				json.Unmarshal(b, &st)
			}
			cancel()
			if st.SignedIn {
				a.post(func() {
					if a.loginGen == gen {
						a.loginOpen, a.login.status = false, ""
						a.toast = "已检测到登录状态，正在恢复…"
					}
				})
				a.finishLogin(gen)
				return
			}
			if !checkedExisting && old != "" && !hasQR && !inVerify && !st.Verify && time.Since(started) >= 4*time.Second {
				checkedExisting = true
				cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
				valid := a.web.checkSignedIn(cctx) == loginCheckValid
				ccancel()
				if valid {
					a.post(func() {
						if a.loginGen == gen {
							a.loginOpen, a.login.status = false, ""
							a.toast = "已检测到登录状态，正在恢复…"
						}
					})
					a.finishLogin(gen)
					return
				}
			}
			slow := time.Since(started) >= 10*time.Second
			hint := captcha.hint(st.CaptchaHidden, time.Now())
			a.post(func() {
				if a.loginGen != gen {
					return
				}
				l := &a.login
				if st.Src != "" && st.Src != l.src {
					if data, err := base64.StdEncoding.DecodeString(st.Src[strings.IndexByte(st.Src, ',')+1:]); err == nil {
						if bmp, err := ui.DecodeBitmap(data); err == nil {
							l.src, l.qr, l.qrSlow = st.Src, bmp, false
						}
					}
				}
				if st.Verify {
					l.verify, l.qrSlow = true, false
				}
				l.hint = hint
				switch {
				case st.Expired:
					l.status = "二维码过期了，正在换一张"
				case st.Scanned:
					l.status = "已扫码，请在手机上确认登录"
				case l.qr != nil:
					l.status = "打开抖音 App，点左上角的扫一扫"
				case slow && !l.verify:
					l.qrSlow = true
					l.status = "二维码加载较慢，可重试或改用网页登录"
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
		in = a.web.checkSignedIn(ctx) == loginCheckValid
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
	a.followState, a.followBusy = nil, nil
	a.watched = nil // the account's, not the next one's
	if a.watchTimer != nil {
		a.watchTimer.Stop()
		a.watchTimer = nil
	}
	a.web.destroy()
	go func() {
		err := a.c.Logout()
		if list, lerr := douyinCookies(); lerr == nil {
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
				if l.hint != "" {
					ui.Text(c, l.hint).FontSize(13).FontWeight(600).TextColor(pal.accent).Grow(1)
				} else {
					ui.Text(c, "请在下方完成登录或身份验证").FontSize(14).FontWeight(600).TextColor(pal.strong).Grow(1)
				}
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
					ui.Spinner(c).TextColor(ui.Hex("#6b7280"))
				}
			})
			ui.Text(c, l.status).FontSize(13.5).TextColor(pal.text)
			ui.Text(c, "登录后可以点赞、收藏、发弹幕，看关注和我的").FontSize(11.5).TextColor(pal.faint)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if l.qrSlow && l.qr == nil {
					if ui.Button(c.Key("retry"), "重试").Clicked() {
						a.reloadLogin()
					}
					if ui.Button(c.Key("weblogin"), "网页登录").Clicked() {
						l.verify = true
					}
				}
				if ui.Button(c.Key("later"), "以后再说").Clicked() {
					a.loginOpen = false
				}
			})
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

// verifyFor tells what Douyin held back for its verification: a read the
// page may hold behind its captcha already (pageGet), or a write.
type verifyFor uint8

const (
	verifyRead verifyFor = iota
	// verifyWrite never shows the captcha a write left on the page: once
	// passed, Douyin's scripts would send the held write themselves, and the
	// user, told to send again, would post it twice.
	verifyWrite
)

// verify shows Douyin's verification (dy.ErrVerify) over the window for the
// user to pass, then the page asks path, a read Douyin held back, itself,
// and retry runs once it has its answer. A read's captcha the page holds
// already is the one shown: loading douyinHome instead would throw it away
// and have Douyin give another, each counting toward the attempts it allows
// (likely what its captcha's [5014][502] said, 2026-11-26). Otherwise the page
// loads douyinHome and asks path there, and Douyin's scripts bring up the
// captcha.
func (a *app) verify(path string, retry func(), kind verifyFor) {
	if a.loginOpen {
		if a.login.verify && a.login.stop != nil { // one verification open already: runs this too once passed
			a.login.retry = append(a.login.retry, retry)
		}
		return
	}
	held := kind == verifyRead && a.web.v != nil // may hold the read's captcha
	wasFull := a.web.v != nil && !a.web.signOnly
	if !held {
		if err := a.web.open(douyinHome); err != nil {
			a.toast = "验证页打不开：" + err.Error()
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	a.loginOpen, a.web.layout = true, true
	a.login = login{verify: true, stop: cancel, retry: []func(){retry}}
	a.loginGen++
	gen := a.loginGen
	go func() {
		defer cancel()
		loaded := !held // the page went to douyinHome, rather than keep a captcha
		if held {
			st := a.web.captchaState(ctx)
			loaded = st != captchaShown && st != captchaHidden
		}
		if held && loaded { // no captcha held after all: Douyin's page asks instead
			var err error
			a.call(func() {
				wasFull = a.web.v != nil && !a.web.signOnly
				err = a.web.open(douyinHome)
			})
			if err != nil {
				a.post(func() {
					if a.loginGen == gen {
						a.loginOpen, a.web.layout, a.login = false, false, login{}
						a.loginGen++
						a.toast = "验证页打不开：" + err.Error()
					}
				})
				return
			}
		}
		if loaded && wasFull {
			// Reloading douyinHome over a previous full page: wait past the old
			// document's readyState === "complete" before polling for the new one.
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
		passed := a.passVerification(ctx, gen, path)
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

// passVerification waits while the page's captcha is up, then has the page
// ask path, which brings up a captcha again if Douyin wants one, and tells
// whether Douyin gave the read. A captcha hidden past captchaLoadTimeout,
// whose frame did not load, is told over the page (login.hint).
func (a *app) passVerification(ctx context.Context, gen int, path string) bool {
	q, _ := json.Marshal(path)
	answer := make(chan bool, 1)
	asked := false
	var captcha hiddenCaptcha
	shownHint := ""
	for {
		st := a.web.captchaState(ctx)
		if !asked && st == captchaNone {
			asked = true
			go func() {
				b, err := a.web.Eval(ctx, "const r = await fetch("+string(q)+`, { credentials: "include" });
return r.status === 200 && (await r.text()).length > 0;`)
				answer <- err == nil && string(b) == "true"
			}()
		}
		if hint := captcha.hint(st == captchaHidden, time.Now()); hint != shownHint {
			shownHint = hint
			a.post(func() {
				if a.loginGen == gen {
					a.login.hint = hint
				}
			})
		}
		select {
		case ok := <-answer:
			return ok
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
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
