package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"douyin/internal/dy"
	"douyin/internal/tiktok"
)

// ttWeb is the on-demand same-origin WebView for TikTok API requests and login.
// Like web (web.go), it stays idle-collected after ttWebIdle so browsing costs
// zero browser memory once idle, and loads a lightweight TikTok page without
// auto-playing videos so TikTok's official security scripts supply X-Bogus /
// X-Gnarly / msToken and browser TLS identity.
type ttWeb struct {
	a           *app
	v           *mygo.WebView
	layout      bool
	login       bool
	importOpen  bool
	importing   bool
	cookieDraft string
	importErr   string
	title       string
	idle        *time.Timer
	idleGen     int
	busy        int
	closed      bool
}

const (
	tiktokSignHome      = "https://www.tiktok.com/explore"
	tiktokLoginURL      = "https://www.tiktok.com/login"
	tiktokLoginEmailURL = "https://www.tiktok.com/login/phone-or-email/email"
	tiktokLoginQRURL    = "https://www.tiktok.com/login/qrcode"
	ttWebIdle           = 20 * time.Second
)

func (w *ttWeb) open(u string) error {
	if w.closed {
		return errors.New("窗口已关闭")
	}
	defer w.touch()
	if w.v != nil {
		if u != "" {
			return w.v.Page().LoadURL(u)
		}
		return nil
	}
	if w.a == nil || w.a.win == nil {
		return errors.New("窗口还没打开")
	}
	pageOpts := mygo.PageOptions{}
	if runtime.GOOS == "darwin" {
		pageOpts.UserAgentSuffix = safariUA
	}
	v, err := w.a.win.NewWebView(mygo.WebViewOptions{
		URL:  u,
		Page: pageOpts,
	})
	if err != nil {
		return err
	}
	v.Page().SetAudioMuted(true)
	v.Page().SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if !w.layout || !allowTikTokPopup(req.URL) {
			return nil
		}
		pw, ph := req.Width, req.Height
		if pw < 360 || pw > 1200 {
			pw = 520
		}
		if ph < 420 || ph > 1000 {
			ph = 680
		}
		return &mygo.WindowOptions{
			Title:  "TikTok 登录",
			Width:  pw,
			Height: ph,
			Page:   pageOpts,
		}
	})
	w.v = v
	return nil
}

// allowTikTokPopup permits OAuth and login popups (e.g. Google/Apple/LINE)
// to open in a related native window so window.opener postMessage callbacks
// complete inside the WebView instead of dropping in an external browser.
func allowTikTokPopup(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || strings.EqualFold(trimmed, "about:blank") {
		return true
	}
	u, err := url.Parse(trimmed)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{
		"tiktok.com",
		"google.com",
		"googleusercontent.com",
		"apple.com",
		"facebook.com",
		"twitter.com",
		"x.com",
		"line.me",
		"kakao.com",
		"vk.com",
	} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func (w *ttWeb) touch() {
	w.idleGen++
	if w.idle != nil {
		w.idle.Stop()
		w.idle = nil
	}
	if w.v == nil || w.closed {
		return
	}
	gen := w.idleGen
	w.idle = time.AfterFunc(ttWebIdle, func() {
		w.a.post(func() { w.expire(gen) })
	})
}

func (w *ttWeb) expire(gen int) {
	if gen != w.idleGen || w.v == nil {
		return
	}
	if w.busy > 0 || w.layout {
		w.touch()
		return
	}
	w.destroy()
}

func (w *ttWeb) destroy() {
	if w.v != nil {
		w.v.Destroy()
		w.v = nil
	}
	w.touch()
}

func (w *ttWeb) use() func() {
	w.a.call(func() { w.busy++; w.touch() })
	return func() {
		w.a.call(func() {
			w.busy--
			w.touch()
		})
	}
}

const ttPageReadyJS = `
if (!location.hostname.endsWith("tiktok.com") || document.readyState !== "complete") return "wait";
if (location.pathname.includes("/hk/")) return "reload";
for (const v of document.querySelectorAll("video")) { try { v.pause(); v.muted = true; } catch {} }
const isHooked = (fn) => typeof fn === "function" && !Function.prototype.toString.call(fn).includes("[native code]");
if (isHooked(window.fetch) || isHooked(XMLHttpRequest.prototype.open) || isHooked(XMLHttpRequest.prototype.send)) return "ready";
if (typeof window.byted_acrawler === "object" || typeof window.webmssdk === "object" || typeof window._SdkGlueInit === "function") return "sdk";
if (document.getElementById("__UNIVERSAL_DATA_FOR_REHYDRATION__")) return "ssr";
return "wait";
`

const ttSDKWaitTimeout = 8 * time.Second

func (w *ttWeb) signHomeURL() string {
	if w.a != nil && w.a.ttRegion != "" {
		if lang := regionDefaultLang(w.a.ttRegion); lang != "" {
			return tiktokSignHome + "?lang=" + url.QueryEscape(lang)
		}
	}
	return tiktokSignHome
}

func (w *ttWeb) page(ctx context.Context) (*mygo.Page, error) {
	var p *mygo.Page
	var err error
	homeURL := w.signHomeURL()
	w.a.call(func() {
		if w.v == nil {
			err = w.open(homeURL)
		}
		if err == nil {
			w.touch()
			p = w.v.Page()
		}
	})
	if err != nil {
		return nil, err
	}
	startAt := time.Now()
	sdkSince := time.Time{}
	ssrSince := time.Time{}
	reloaded := false
	for {
		state, evalErr := p.EvalContext(ctx, ttPageReadyJS)
		if evalErr == nil {
			if state == "reload" && !reloaded {
				reloaded = true
				w.a.call(func() { _ = w.open(homeURL) })
			}
			if state == "ready" {
				return p, nil
			}
			if state == "sdk" {
				if sdkSince.IsZero() {
					sdkSince = time.Now()
				}
				// Wait for TikTok's app bundle to invoke byted_acrawler.init and hook fetch/XHR.
				if time.Since(sdkSince) >= 2500*time.Millisecond || time.Since(startAt) >= ttSDKWaitTimeout {
					return p, nil
				}
			}
			if state == "ssr" {
				if ssrSince.IsZero() {
					ssrSince = time.Now()
				}
				// Give security SDK scripts a moment to hydrate after SSR document complete.
				if time.Since(ssrSince) >= 2000*time.Millisecond || time.Since(startAt) >= ttSDKWaitTimeout {
					return p, nil
				}
			}
		}
		poll := 150 * time.Millisecond
		if time.Since(startAt) < time.Second {
			poll = 80 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("TikTok 页面载入超时，请检查网络代理设置")
		case <-time.After(poll):
		}
	}
}

func (w *ttWeb) Eval(ctx context.Context, js string) ([]byte, error) {
	defer w.use()()
	p, err := w.page(ctx)
	if err != nil {
		return nil, err
	}
	v, err := p.EvalContext(ctx, "return JSON.stringify(await (async () => {\n"+js+"\n})())")
	s, _ := v.(string)
	return []byte(s), err
}

func (w *ttWeb) Load(ctx context.Context, u string, layout bool) error {
	defer w.use()()
	var err error
	w.a.call(func() {
		w.layout = layout
		err = w.open(u)
	})
	if err != nil {
		return err
	}
	_, err = w.page(ctx)
	return err
}

func (w *ttWeb) Cookies() ([]dy.Cookie, error) {
	list, err := mygo.Cookies.List(mygo.CookieFilter{Domain: "tiktok.com"})
	if err != nil {
		return nil, err
	}
	out := make([]dy.Cookie, len(list))
	for i, k := range list {
		out[i] = dy.Cookie{
			Name:     k.Name,
			Value:    k.Value,
			Domain:   k.Domain,
			Path:     k.Path,
			Expires:  k.Expires.Unix(),
			Secure:   k.Secure,
			HTTPOnly: k.HTTPOnly,
		}
		if k.Expires.IsZero() {
			out[i].Expires = 0
		}
	}
	return out, nil
}

// openTikTokLogin opens the TikTok login page inside the WebView and saves
// cookies once a session cookie appears.
func (a *app) openTikTokLogin() {
	a.ensureTikTokClient()
	if err := a.ttWeb.open(tiktokLoginURL); err != nil {
		a.toast = "TikTok 登录页打不开：" + err.Error()
		return
	}
	a.ttWeb.title = "登录 TikTok"
	a.ttWeb.login = true
	a.ttWeb.importOpen = false
	a.ttWeb.importErr = ""
	a.ttWeb.layout = true
	a.toast = "请在 TikTok 登录页完成登录"
	go func() {
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			time.Sleep(800 * time.Millisecond)
			var open bool
			a.call(func() { open = a.ttWeb.layout })
			if !open {
				return
			}
			cs, err := a.ttWeb.Cookies()
			if err != nil {
				continue
			}
			if tiktok.LoggedIn(cs) {
				_ = a.tt.SaveCookies(cs)
				a.post(func() {
					a.ttWeb.layout = false
					a.ttWeb.login = false
					a.ttWeb.title = ""
					if a.isTikTok() {
						a.signedIn = true
						if a.ttFeed != nil {
							a.refresh(a.ttFeed)
						}
					}
					a.loadTikTokAccount()
					a.toast = "TikTok 已登录"
				})
				return
			}
		}
		a.post(func() {
			a.ttWeb.layout = false
			a.ttWeb.login = false
			a.ttWeb.importOpen = false
			a.ttWeb.title = ""
		})
	}()
}

// importTikTokCookies parses browser-exported cookies (JSON, cURL, Cookie header,
// or Netscape format), injects them into the shared WebView2/WebKit cookie store
// (including HttpOnly cookies), saves them to TikTok/cookies.json, and loads the account.
func (a *app) importTikTokCookies(raw string) {
	if a.ttWeb.importing {
		return
	}
	cs, err := tiktok.ParseImportedCookies(raw)
	if err != nil {
		a.ttWeb.importErr = err.Error()
		return
	}
	tt := a.ensureTikTokClient()
	if tt == nil {
		a.ttWeb.importErr = "TikTok 客户端未就绪"
		return
	}
	a.ttWeb.importing = true
	a.ttWeb.importErr = ""
	go func() {
		var firstErr error
		for _, ck := range cs {
			var exp time.Time
			if ck.Expires > 0 {
				exp = time.Unix(ck.Expires, 0)
			}
			if err := mygo.Cookies.Set(mygo.Cookie{
				Name:     ck.Name,
				Value:    ck.Value,
				Domain:   ck.Domain,
				Path:     ck.Path,
				Expires:  exp,
				Secure:   ck.Secure,
				HTTPOnly: ck.HTTPOnly,
			}); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := tt.SaveCookies(cs); err != nil && firstErr == nil {
			firstErr = err
		}
		a.post(func() {
			a.ttWeb.importing = false
			if firstErr != nil {
				a.ttWeb.importErr = "写入 Cookie 失败：" + firstErr.Error()
				return
			}
			a.ttWeb.layout = false
			a.ttWeb.login = false
			a.ttWeb.importOpen = false
			a.ttWeb.cookieDraft = ""
			a.ttWeb.importErr = ""
			a.ttWeb.title = ""
			a.ttWeb.destroy()
			if a.isTikTok() {
				a.signedIn = true
				if a.ttFeed != nil {
					a.refresh(a.ttFeed)
				}
			}
			a.loadTikTokAccount()
			a.toast = fmt.Sprintf("已导入 %d 条 TikTok Cookie，正在加载账号", len(cs))
		})
	}()
}

// clearTikTokWebCookies removes TikTok cookies from the embedded WebView store on logout.
func clearTikTokWebCookies() {
	go func() {
		list, err := mygo.Cookies.List(mygo.CookieFilter{Domain: "tiktok.com"})
		if err != nil {
			return
		}
		for _, ck := range list {
			_ = mygo.Cookies.Delete(ck)
		}
	}()
}

// verifyTikTok surfaces the TikTok WebView on the video page so the user can
// complete TikTok's captcha or login challenge, then retries the pending action.
func (a *app) verifyTikTok(videoID string, retry func()) {
	if a.ttWeb.layout {
		return
	}
	a.ensureTikTokClient()
	targetURL := a.ttWeb.signHomeURL()
	if videoID != "" {
		targetURL = tiktok.BaseURL + "/@/video/" + videoID
	}
	if err := a.ttWeb.open(targetURL); err != nil {
		a.toast = "TikTok 验证页打不开：" + err.Error()
		return
	}
	a.ttWeb.title = "完成 TikTok 验证（通过后自动继续）"
	a.ttWeb.login = false
	a.ttWeb.layout = true
	a.toast = "请在页面中完成 TikTok 验证（按 Esc 可关闭）"
	go func() {
		deadline := time.Now().Add(3 * time.Minute)
		// Wait briefly for the page and captcha widget to mount before probing.
		time.Sleep(2 * time.Second)
		for time.Now().Before(deadline) {
			var open bool
			a.call(func() { open = a.ttWeb.layout })
			if !open {
				return
			}
			if videoID != "" && a.tt != nil {
				probe := a.tt.Comments(videoID)
				if _, err := probe(); err == nil {
					a.tt.SyncCookies()
					a.post(func() {
						a.ttWeb.layout = false
						a.ttWeb.title = ""
						a.toast = "TikTok 验证已通过"
						if retry != nil {
							retry()
						}
					})
					return
				}
			}
			time.Sleep(1500 * time.Millisecond)
		}
		a.post(func() {
			a.ttWeb.layout = false
			a.ttWeb.title = ""
		})
	}()
}

var _ dy.Web = (*ttWeb)(nil)
var _ = json.Marshal
