// Package tiktok talks to TikTok's web API. Read and write API calls route
// through the lightweight same-origin TikTok WebView (dy.Web) so TikTok's own
// security SDK supplies X-Bogus / X-Gnarly / msToken and browser TLS identity,
// while media and cover downloads flow through a configurable HTTP/SOCKS5
// proxy carrying the session's tt_chain_token cookie.
package tiktok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"douyin/internal/dy"
	"douyin/internal/media"
	"douyin/internal/platform"
	ttsign "douyin/internal/sign/tiktok"
)

const (
	BaseURL = "https://www.tiktok.com"
	ua      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// ErrVerify signals that TikTok held back a response pending captcha verification.
var ErrVerify = errors.New("tiktok: verification required")

// ErrNeedLogin signals that TikTok refused an action because the user is logged out.
var ErrNeedLogin = errors.New("TikTok 需要先登录账号")

type login struct {
	cookie      string
	mediaCookie string
	msToken     string
	csrfToken   string
	chainToken  string
	sessionID   string
}

func loginFromCookies(cs []dy.Cookie) login {
	var l login
	seenAll := make(map[string]int, len(cs))
	parts := make([]string, 0, len(cs))
	seenMedia := make(map[string]int, len(cs))
	mediaParts := make([]string, 0, len(cs))

	for _, ck := range cs {
		if ck.Name == "" || ck.Value == "" {
			continue
		}
		pair := ck.Name + "=" + ck.Value
		dom := strings.ToLower(strings.TrimSpace(ck.Domain))
		isRootDomain := dom == "" || dom == ".tiktok.com" || dom == "tiktok.com"

		if idx, ok := seenAll[ck.Name]; ok {
			if isRootDomain {
				parts[idx] = pair
			}
		} else {
			seenAll[ck.Name] = len(parts)
			parts = append(parts, pair)
		}
		if isRootDomain {
			if idx, ok := seenMedia[ck.Name]; ok {
				mediaParts[idx] = pair
			} else {
				seenMedia[ck.Name] = len(mediaParts)
				mediaParts = append(mediaParts, pair)
			}
		}

		switch ck.Name {
		case "msToken":
			if l.msToken == "" || isRootDomain {
				l.msToken = ck.Value
			}
		case "tt_csrf_token":
			l.csrfToken = ck.Value
		case "tt_chain_token":
			if l.chainToken == "" || isRootDomain {
				l.chainToken = ck.Value
			}
		case "sessionid", "sessionid_ss", "sid_tt":
			if l.sessionID == "" && ck.Value != "" {
				l.sessionID = ck.Value
			}
		}
	}
	l.cookie = strings.Join(parts, "; ")
	if len(mediaParts) > 0 {
		l.mediaCookie = strings.Join(mediaParts, "; ")
	} else {
		l.mediaCookie = l.cookie
	}
	return l
}

// LoggedIn reports whether cs carries an active TikTok login session cookie.
func LoggedIn(cs []dy.Cookie) bool {
	return loginFromCookies(cs).sessionID != ""
}

// HasSession reports whether the client currently holds a TikTok login session cookie.
func (c *Client) HasSession() bool {
	return c.cookies().sessionID != ""
}

// Client talks to TikTok's web API and media CDN.
type Client struct {
	httpMu            sync.RWMutex
	http              *http.Client
	proxyURL          string
	baseURL           string // defaults to BaseURL; overridable in tests
	translateURL      string // custom single-endpoint override in tests
	translateMu       sync.Mutex
	translateCache    map[string]string
	translateCooldown map[string]time.Time
	translateChain    []translateProvider // optional test override for fallback chain
	login             atomic.Pointer[login]
	cookieSaveMu      sync.Mutex
	savedCookies      []byte
	webMu             sync.RWMutex
	web               dy.Web
	webUA             atomic.Value // string captured from WebView navigator.userAgent
	region            atomic.Value // string, e.g. "US"
	lang              atomic.Value // string, e.g. "en"
}

// New creates a TikTok client, loading saved cookies if present.
func New() (*Client, error) {
	tr, err := media.NewHTTPTransport("")
	if err != nil {
		return nil, err
	}
	c := &Client{
		http:    &http.Client{Timeout: 45 * time.Second, Transport: tr},
		baseURL: BaseURL,
	}
	c.region.Store("US")
	c.lang.Store("en")
	if err := c.ReloadCookies(); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
		return c, err
	}
	return c, nil
}

// Platform identifies this client as TikTok.
func (c *Client) Platform() platform.ID { return platform.TikTok }

// SetBaseURLForTest overrides the API base URL in tests.
func (c *Client) SetBaseURLForTest(raw string) { c.baseURL = raw }

// SetWeb attaches the TikTok WebView host used for signed same-origin requests.
func (c *Client) SetWeb(w dy.Web) {
	c.webMu.Lock()
	c.web = w
	c.webMu.Unlock()
}

func (c *Client) getWeb() dy.Web {
	c.webMu.RLock()
	defer c.webMu.RUnlock()
	return c.web
}

// SetRegion updates the TikTok region and language parameters.
func (c *Client) SetRegion(region, lang string) {
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" {
		region = "US"
	}
	lang = strings.TrimSpace(lang)
	if lang == "" {
		lang = "en"
	}
	c.region.Store(region)
	c.lang.Store(lang)
}

func (c *Client) currentRegion() (string, string) {
	r, _ := c.region.Load().(string)
	l, _ := c.lang.Load().(string)
	if r == "" {
		r = "US"
	}
	if l == "" {
		l = "en"
	}
	return r, l
}

// SetProxy updates the HTTP/SOCKS5 proxy used for TikTok API fallback and CDN requests.
func (c *Client) SetProxy(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	c.httpMu.RLock()
	same := c.proxyURL == trimmed
	c.httpMu.RUnlock()
	if same {
		return nil
	}
	tr, err := media.NewHTTPTransport(trimmed)
	if err != nil {
		return err
	}
	c.httpMu.Lock()
	old, _ := c.http.Transport.(*http.Transport)
	c.http.Transport = tr
	c.proxyURL = trimmed
	c.httpMu.Unlock()
	if old != nil {
		old.CloseIdleConnections()
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	c.httpMu.RLock()
	defer c.httpMu.RUnlock()
	return c.http
}

func cookieFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "TikTok", "cookies.json"), nil
}

// ReloadCookies reads TikTok/cookies.json into memory.
func (c *Client) ReloadCookies() error {
	path, err := cookieFile()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cs []dy.Cookie
	if err := json.Unmarshal(raw, &cs); err != nil {
		return err
	}
	l := loginFromCookies(cs)
	c.login.Store(&l)
	return nil
}

// SaveCookies updates in-memory credentials immediately and persists cs to
// TikTok/cookies.json so playback headers see fresh tokens even if disk write fails.
func (c *Client) SaveCookies(cs []dy.Cookie) error {
	l := loginFromCookies(cs)
	b, _ := json.Marshal(cs)
	c.cookieSaveMu.Lock()
	defer c.cookieSaveMu.Unlock()
	c.login.Store(&l)
	if bytes.Equal(b, c.savedCookies) {
		return nil
	}
	file, err := cookieFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(file+".tmp", b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(file+".tmp", file); err != nil {
		return err
	}
	c.savedCookies = b
	return nil
}

// Logout clears saved TikTok cookies and resets the session to anonymous.
func (c *Client) Logout() error {
	c.cookieSaveMu.Lock()
	c.savedCookies = nil
	c.cookieSaveMu.Unlock()
	c.login.Store(&login{})
	if p, err := cookieFile(); err == nil {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (c *Client) cookies() login {
	if l := c.login.Load(); l != nil {
		return *l
	}
	return login{}
}

// SyncCookies pulls fresh cookies (including tt_chain_token) from the attached WebView.
func (c *Client) SyncCookies() {
	w := c.getWeb()
	if w == nil {
		return
	}
	if cs, err := w.Cookies(); err == nil && len(cs) > 0 {
		_ = c.SaveCookies(cs)
	}
}

func regionTimezone(region string) string {
	switch strings.ToUpper(strings.TrimSpace(region)) {
	case "TW", "HK":
		return "Asia/Taipei"
	case "JP":
		return "Asia/Tokyo"
	case "KR":
		return "Asia/Seoul"
	case "SG":
		return "Asia/Singapore"
	case "GB":
		return "Europe/London"
	default:
		return "America/New_York"
	}
}

func regionBrowserLang(region, fallback string) string {
	switch strings.ToUpper(strings.TrimSpace(region)) {
	case "TW":
		return "zh-TW"
	case "HK":
		return "zh-HK"
	case "JP":
		return "ja-JP"
	case "KR":
		return "ko-KR"
	case "SG":
		return "zh-SG"
	case "GB":
		return "en-GB"
	case "US":
		return "en-US"
	default:
		if fallback != "" {
			return fallback
		}
		return "en-US"
	}
}

func (c *Client) commonQuery(extra string) string {
	region, lang := c.currentRegion()
	browserLang := regionBrowserLang(region, lang)
	tz := regionTimezone(region)
	q := "aid=1988&app_language=" + url.QueryEscape(lang) +
		"&app_name=tiktok_web&browser_language=" + url.QueryEscape(browserLang) +
		"&browser_name=Mozilla&browser_online=true&browser_platform=Win32" +
		"&cookie_enabled=true&device_platform=web_pc&os=windows" +
		"&priority_region=" + url.QueryEscape(region) +
		"&region=" + url.QueryEscape(region) +
		"&tz_name=" + url.QueryEscape(tz)
	if extra != "" {
		q += "&" + extra
	}
	return q
}

// call executes a TikTok API request. When a WebView is attached, the call runs
// inside the same-origin page so TikTok's own security scripts sign it.
func (c *Client) call(method, path, extra, body string) ([]byte, error) {
	relURL := path + "?" + c.commonQuery(extra)
	if w := c.getWeb(); w != nil {
		return c.callViaWeb(w, method, relURL, body)
	}
	fullURL := strings.TrimRight(c.baseURL, "/") + relURL
	return c.callDirect(method, fullURL, body)
}

func extractHTMLTitle(html string) string {
	low := strings.ToLower(html)
	i := strings.Index(low, "<title")
	if i < 0 {
		return ""
	}
	gt := strings.IndexByte(low[i:], '>')
	if gt < 0 {
		return ""
	}
	start := i + gt + 1
	end := strings.Index(low[start:], "</title>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(html[start : start+end])
}

func classifyNonJSONReply(text, finalURL string, hasSDK bool) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ErrVerify
	}
	low := strings.ToLower(trimmed)
	if strings.Contains(low, "captcha") || strings.Contains(low, "verifycenter") ||
		strings.Contains(low, "sec_sdk") || strings.Contains(low, "verify-bar") {
		return ErrVerify
	}
	if strings.Contains(strings.ToLower(finalURL), "/hk/about") {
		return errors.New("当前代理节点位于香港（TikTok 已停止香港服务），请切换至日本、新加坡、台湾或美国等海外节点后重试")
	}
	title := extractHTMLTitle(trimmed)
	extra := ""
	if finalURL != "" {
		extra = " url=" + finalURL
	}
	if !hasSDK {
		extra += " sdk=none"
	}
	if title != "" {
		return fmt.Errorf("TikTok 返回网页而非数据（%s%s），请确认海外代理可用或在设置中登录 TikTok", title, extra)
	}
	return fmt.Errorf("TikTok 返回网页而非数据（%s），请确认海外代理可用或在设置中登录 TikTok", strings.TrimSpace(extra))
}

func (c *Client) callViaWeb(w dy.Web, method, relURL, body string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	loggedIn := "false"
	if c.cookies().sessionID != "" {
		loggedIn = "true"
	}
	args, _ := json.Marshal([]string{method, relURL, body, loggedIn})
	js := "const [method, rawUrl, body, goLoggedIn] = " + string(args) + `;
let rel = rawUrl.startsWith("http") ? new URL(rawUrl).pathname + new URL(rawUrl).search : rawUrl;
const getCookie = (name) => {
  const m = document.cookie.match(new RegExp("(?:^|;\\s*)" + name + "=([^;]+)"));
  return m ? decodeURIComponent(m[1]) : "";
};
const addParam = (k, v) => {
  if (!v || rel.includes(k + "=")) return;
  rel += (rel.includes("?") ? "&" : "?") + k + "=" + encodeURIComponent(v);
};
let wid = "", odinId = "", pageRegion = "", webIdTime = "", pageLoggedIn = false;
try {
  const el = document.getElementById("__UNIVERSAL_DATA_FOR_REHYDRATION__");
  if (el && el.textContent) {
    const d = JSON.parse(el.textContent);
    const ctx = d && d.__DEFAULT_SCOPE__ && d.__DEFAULT_SCOPE__["webapp.app-context"];
    if (ctx) {
      if (ctx.wid) wid = String(ctx.wid);
      if (ctx.odinId) odinId = String(ctx.odinId);
      if (ctx.region) pageRegion = String(ctx.region);
      if (ctx.webIdCreatedTime) webIdTime = String(ctx.webIdCreatedTime);
      if (ctx.user && (ctx.user.uid || ctx.user.secUid)) pageLoggedIn = true;
    }
  }
} catch {}
const isLogin = (goLoggedIn === "true" || pageLoggedIn || !!getCookie("sessionid"));
addParam("WebIdLastTime", webIdTime || String(Math.floor(Date.now() / 1000)));
addParam("channel", "tiktok_web");
addParam("data_collection_enabled", "true");
addParam("device_id", wid);
addParam("odinId", odinId || wid);
addParam("verifyFp", getCookie("s_v_web_id"));
addParam("browser_version", navigator.appVersion || "5.0");
addParam("focus_state", "true");
addParam("history_len", String(Math.max(2, history.length || 2)));
addParam("is_fullscreen", "false");
addParam("is_page_visible", "true");
addParam("tz_name", Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Tokyo");
addParam("screen_width", String(screen.width || 1920));
addParam("screen_height", String(screen.height || 1080));
addParam("user_is_login", isLogin ? "true" : "false");
const full = location.origin + rel;
const pathOnly = rel.split("?")[0];
const hasSDK = typeof window.byted_acrawler === "object" || typeof window.webmssdk === "object" || typeof window._SdkGlueInit === "function";
const isHooked = (fn) => typeof fn === "function" && !Function.prototype.toString.call(fn).includes("[native code]");
const inspectPerf = () => {
  let state = "none";
  let matchedUrl = "";
  try {
    const entries = performance.getEntriesByType("resource") || [];
    for (let i = entries.length - 1; i >= 0; i--) {
      const n = entries[i].name || "";
      if (!matchedUrl && n.includes(pathOnly)) matchedUrl = n;
      if (n.includes("/api/")) {
        if (/x-bogus=|x-gnarly=/i.test(n)) { state = "signed"; break; }
        if (state === "none") state = "api";
      }
    }
  } catch {}
  return { state, matchedUrl };
};
const doFetch = async (targetUrl) => {
  try {
    const headers = { "accept": "application/json, text/plain, */*" };
    const opts = { method, credentials: "include", headers };
    if (method === "POST") {
      headers["content-type"] = "application/x-www-form-urlencoded; charset=UTF-8";
      opts.body = body;
    }
    const r = await fetch(targetUrl, opts);
    const text = await r.text();
    const fu = r.url || targetUrl;
    const perf = inspectPerf();
    const wireUrl = perf.matchedUrl || fu;
    const checkU = fu + " " + perf.matchedUrl;
    const msN = (wireUrl.match(/(?:^|[?&])msToken=/gi) || []).length;
    const hdrFlags = [];
    if (r.headers.get("x-ms-token")) hdrFlags.push("xms");
    if (r.headers.get("x-tt-logid")) hdrFlags.push("logid");
    if (r.headers.get("bdturing-verify") || r.headers.get("x-tt-verify")) hdrFlags.push("verify");
    return {
      status: r.status,
      contentType: r.headers.get("content-type") || "",
      finalUrl: fu,
      hasSDK,
      hasBogus: /x-bogus=/i.test(checkU),
      hasGnarly: /x-gnarly=/i.test(checkU),
      msN,
      hdr: hdrFlags.join(",") || "none",
      region: pageRegion,
      text
    };
  } catch {
    return { status: 0, contentType: "", finalUrl: "", hasSDK, hasBogus: false, hasGnarly: false, msN: 0, hdr: "err", region: pageRegion, text: "" };
  }
};
const doXHR = (targetUrl) => new Promise((resolve) => {
  const xhr = new XMLHttpRequest();
  xhr.open(method, targetUrl, true);
  xhr.withCredentials = true;
  xhr.setRequestHeader("Accept", "application/json, text/plain, */*");
  if (method === "POST") {
    xhr.setRequestHeader("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8");
  }
  xhr.onload = () => {
    const fu = xhr.responseURL || targetUrl;
    const perf = inspectPerf();
    const wireUrl = perf.matchedUrl || fu;
    const checkU = fu + " " + perf.matchedUrl;
    const msN = (wireUrl.match(/(?:^|[?&])msToken=/gi) || []).length;
    resolve({
      status: xhr.status,
      contentType: xhr.getResponseHeader("content-type") || "",
      finalUrl: fu,
      hasSDK,
      hasBogus: /x-bogus=/i.test(checkU),
      hasGnarly: /x-gnarly=/i.test(checkU),
      msN,
      hdr: "xhr",
      region: pageRegion,
      text: xhr.responseText || ""
    });
  };
  xhr.onerror = () => resolve({ status: 0, contentType: "", finalUrl: "", hasSDK, hasBogus: false, hasGnarly: false, msN: 0, hdr: "xhrerr", region: pageRegion, text: "" });
  xhr.send(method === "POST" ? body : null);
});
let anyBogus = false, anyGnarly = false, didInit = false;
const track = (r) => {
  if (r.hasBogus) anyBogus = true;
  if (r.hasGnarly) anyGnarly = true;
  return r;
};
// Let webmssdk inject its own authoritative msToken + X-Bogus + X-Gnarly first.
let res = track(await doFetch(full));
if ((!res.text || res.status === 0) && res.msN === 0) {
  const ms = getCookie("msToken");
  if (ms) {
    res = track(await doFetch(full + "&msToken=" + encodeURIComponent(ms)));
  }
}
if (!res.text || res.status === 0) {
  res = track(await doXHR(full));
}
if ((!res.text || res.status === 0) && !anyBogus && !anyGnarly) {
  res = track(await doFetch(rel));
  if ((!res.text || res.status === 0) && !anyBogus && !anyGnarly) {
    res = track(await doXHR(rel));
  }
}
if ((!res.text || res.status === 0) && !anyBogus && !anyGnarly &&
    window.byted_acrawler && typeof window.byted_acrawler.init === "function" && !window.__ttAcrawlerInited) {
  window.__ttAcrawlerInited = true;
  didInit = true;
  try {
    window.byted_acrawler.init({
      aid: 1988,
      dfp: true,
      boe: false,
      intercept: true,
      enablePathList: ["/api/"],
      paths: ["/api/"]
    });
  } catch {}
  await new Promise((r) => setTimeout(r, 150));
  res = track(await doFetch(full));
  if (!res.text || res.status === 0) {
    res = track(await doXHR(full));
  }
}
const perfFinal = inspectPerf();
res.hasBogus = anyBogus;
res.hasGnarly = anyGnarly;
res.fHook = isHooked(window.fetch);
res.xHook = isHooked(XMLHttpRequest.prototype.open) || isHooked(XMLHttpRequest.prototype.send);
res.didInit = didInit;
res.perf = perfFinal.state;
res.loggedIn = isLogin;
res.ua = navigator.userAgent || "";
return res;`
	raw, err := w.Eval(ctx, js)
	if err != nil {
		return nil, fmt.Errorf("tiktok: web %s: %w", method, err)
	}
	var res struct {
		Status      int    `json:"status"`
		ContentType string `json:"contentType"`
		FinalURL    string `json:"finalUrl"`
		HasSDK      bool   `json:"hasSDK"`
		HasBogus    bool   `json:"hasBogus"`
		HasGnarly   bool   `json:"hasGnarly"`
		FHook       bool   `json:"fHook"`
		XHook       bool   `json:"xHook"`
		DidInit     bool   `json:"didInit"`
		MsN         int    `json:"msN"`
		Hdr         string `json:"hdr"`
		Perf        string `json:"perf"`
		LoggedIn    bool   `json:"loggedIn"`
		UA          string `json:"ua"`
		Region      string `json:"region"`
		Text        string `json:"text"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("tiktok: decode web reply: %w", err)
	}
	if s := strings.TrimSpace(res.UA); s != "" {
		c.webUA.Store(s)
	}
	// Keep tt_chain_token & msToken fresh for CDN video playback.
	c.SyncCookies()
	if res.Status != http.StatusOK {
		return nil, fmt.Errorf("tiktok: HTTP %d", res.Status)
	}
	trimmed := strings.TrimSpace(res.Text)
	if trimmed == "" {
		return nil, fmt.Errorf("%w (empty, http=%d msN=%d bogus=%v gnarly=%v)",
			ErrVerify, res.Status, res.MsN, res.HasBogus, res.HasGnarly)
	}
	if trimmed[0] == '<' || strings.Contains(strings.ToLower(res.ContentType), "text/html") {
		return nil, classifyNonJSONReply(trimmed, res.FinalURL, res.HasSDK)
	}
	return []byte(trimmed), nil
}

func (c *Client) callDirect(method, fullURL, body string) ([]byte, error) {
	if idx := strings.IndexByte(fullURL, '?'); idx >= 0 && !strings.Contains(fullURL, "X-Bogus=") {
		xb := ttsign.SignXBogus(fullURL[idx+1:], ua, 0)
		fullURL += "&X-Bogus=" + url.QueryEscape(xb)
	}
	req, err := http.NewRequest(method, fullURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", BaseURL+"/")
	if ck := c.cookies().cookie; ck != "" {
		req.Header.Set("Cookie", ck)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tiktok: HTTP %d", resp.StatusCode)
	}
	if len(b) == 0 {
		return nil, ErrVerify
	}
	return b, nil
}

func checkTikTokStatus(b []byte) error {
	var st struct {
		StatusCode  int    `json:"statusCode"`
		StatusCode2 int    `json:"status_code"`
		StatusMsg   string `json:"statusMsg"`
		StatusMsg2  string `json:"status_msg"`
		VerifyEvent string `json:"verify_event"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	code := st.StatusCode
	if code == 0 {
		code = st.StatusCode2
	}
	if code == 10000 || st.VerifyEvent != "" {
		return ErrVerify
	}
	if code == 8 || code == 2483 || code == 2154 || code == 10004 {
		return ErrNeedLogin
	}
	if code != 0 {
		msg := firstNonEmpty(st.StatusMsg, st.StatusMsg2)
		low := strings.ToLower(msg)
		if strings.Contains(low, "login") || strings.Contains(low, "logged out") {
			return ErrNeedLogin
		}
		if msg != "" {
			return fmt.Errorf("tiktok: status %d (%s)", code, msg)
		}
		return fmt.Errorf("tiktok: status %d", code)
	}
	return nil
}

func pager[T any](fetch func() ([]T, bool, error)) dy.Pager[T] {
	done := false
	return func() ([]T, error) {
		for !done {
			items, more, err := fetch()
			if err != nil {
				return nil, err
			}
			done = !more
			if len(items) > 0 {
				return items, nil
			}
		}
		return nil, nil
	}
}

// Feed returns the next batch of For You recommended videos, falling back to
// TikTok's explore feed and finally the page's hydrated SSR item list.
func (c *Client) Feed() ([]dy.Video, error) {
	vs, err := c.fetchItemList("/api/recommend/item_list/", "count=15&from_page=fyp")
	if err == nil && len(vs) > 0 {
		return vs, nil
	}
	// Fallback 1: Explore feed which is more permissive for anonymous sessions.
	if fallback, ferr := c.fetchItemList("/api/explore/item_list/", "count=16&categoryType=120"); ferr == nil && len(fallback) > 0 {
		return fallback, nil
	}
	// Fallback 2: Extract hydrated items embedded in the loaded TikTok page.
	if ssr, serr := c.extractPageItems(); serr == nil && len(ssr) > 0 {
		return ssr, nil
	}
	return nil, err
}

func (c *Client) extractPageItems() ([]dy.Video, error) {
	w := c.getWeb()
	if w == nil {
		return nil, errors.New("tiktok: no web page")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const js = `
const el = document.getElementById("__UNIVERSAL_DATA_FOR_REHYDRATION__");
if (!el || !el.textContent) return [];
let data;
try { data = JSON.parse(el.textContent); } catch { return []; }
const found = [];
const seen = new Set();
const walk = (node, depth) => {
  if (!node || typeof node !== "object" || depth > 8) return;
  if (Array.isArray(node)) {
    for (const x of node) walk(x, depth + 1);
    return;
  }
  if (typeof node.id === "string" && node.video && (node.video.playAddr || (Array.isArray(node.video.bitrateInfo) && node.video.bitrateInfo.length > 0))) {
    if (!seen.has(node.id)) {
      seen.add(node.id);
      found.push(node);
    }
    return;
  }
  for (const k of Object.keys(node)) walk(node[k], depth + 1);
};
walk(data, 0);
return found.slice(0, 30);`
	raw, err := w.Eval(ctx, js)
	if err != nil {
		return nil, err
	}
	var items []ttItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	c.SyncCookies()
	return parseItems(items), nil
}

func (c *Client) fetchItemList(path, extra string) ([]dy.Video, error) {
	b, err := c.call(http.MethodGet, path, extra, "")
	if err != nil {
		return nil, err
	}
	if err := checkTikTokStatus(b); err != nil {
		return nil, err
	}
	var r struct {
		ItemList []ttItem `json:"itemList"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	vs := parseItems(r.ItemList)
	if len(vs) == 0 {
		snippet := strings.TrimSpace(string(b))
		if len(snippet) > 180 {
			snippet = snippet[:180] + "..."
		}
		return nil, fmt.Errorf("TikTok 接口返回空列表（%s，items=%d）：%s", path, len(r.ItemList), snippet)
	}
	return vs, nil
}

// FollowFeed pages through videos from followed creators.
func (c *Client) FollowFeed() dy.Pager[dy.Video] {
	cursor := int64(0)
	return pager(func() ([]dy.Video, bool, error) {
		b, err := c.call(http.MethodGet, "/api/following/item_list/", fmt.Sprintf("count=15&cursor=%d", cursor), "")
		if err != nil {
			return nil, false, err
		}
		if err := checkTikTokStatus(b); err != nil {
			return nil, false, err
		}
		var r struct {
			ItemList []ttItem `json:"itemList"`
			Cursor   flexInt  `json:"cursor"`
			HasMore  bool     `json:"hasMore"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, false, err
		}
		next := int64(r.Cursor)
		more := r.HasMore && next != cursor
		cursor = next
		return parseItems(r.ItemList), more, nil
	})
}

// Search pages through TikTok videos matching keyword.
func (c *Client) Search(keyword string) dy.Pager[dy.Video] {
	offset := 0
	return pager(func() ([]dy.Video, bool, error) {
		q := fmt.Sprintf("keyword=%s&offset=%d&count=12", url.QueryEscape(keyword), offset)
		b, err := c.call(http.MethodGet, "/api/search/general/full/", q, "")
		if err != nil {
			return nil, false, err
		}
		if err := checkTikTokStatus(b); err != nil {
			return nil, false, err
		}
		var r struct {
			Data []struct {
				Type int     `json:"type"`
				Item *ttItem `json:"item"`
			} `json:"data"`
			Cursor  int `json:"cursor"`
			HasMore int `json:"has_more"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, false, err
		}
		items := make([]ttItem, 0, len(r.Data))
		for _, d := range r.Data {
			if d.Item != nil && d.Item.ID != "" {
				items = append(items, *d.Item)
			}
		}
		more := r.HasMore != 0 && r.Cursor > offset
		if r.Cursor > offset {
			offset = r.Cursor
		} else {
			offset += len(r.Data)
		}
		return parseItems(items), more, nil
	})
}

// SearchUsers searches TikTok accounts matching keyword.
func (c *Client) SearchUsers(keyword string) ([]dy.User, error) {
	q := fmt.Sprintf("keyword=%s&cursor=0&count=12", url.QueryEscape(keyword))
	b, err := c.call(http.MethodGet, "/api/search/user/full/", q, "")
	if err != nil {
		return nil, err
	}
	if err := checkTikTokStatus(b); err != nil {
		return nil, err
	}
	var r struct {
		UserList []struct {
			UserInfo struct {
				UID       string  `json:"uid"`
				SecUID    string  `json:"sec_uid"`
				UniqueID  string  `json:"unique_id"`
				Nickname  string  `json:"nickname"`
				Avatar    string  `json:"avatar_thumb"`
				Signature string  `json:"signature"`
				Followers flexInt `json:"follower_count"`
			} `json:"user_info"`
		} `json:"user_list"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	out := make([]dy.User, 0, len(r.UserList))
	for _, u := range r.UserList {
		info := u.UserInfo
		out = append(out, dy.User{
			ID:        firstNonEmpty(info.SecUID, info.UniqueID),
			UID:       info.UID,
			Name:      firstNonEmpty(info.Nickname, info.UniqueID),
			Handle:    info.UniqueID,
			Avatar:    info.Avatar,
			Signature: info.Signature,
			Followers: int(info.Followers),
		})
	}
	return out, nil
}

// Profile fetches a creator's profile by secUid or uniqueId.
func (c *Client) Profile(secUID string) (dy.User, error) {
	param := "secUid=" + url.QueryEscape(secUID)
	if !strings.HasPrefix(secUID, "MS4w") {
		param = "uniqueId=" + url.QueryEscape(secUID)
	}
	b, err := c.call(http.MethodGet, "/api/user/detail/", param, "")
	if err != nil {
		return dy.User{}, err
	}
	if err := checkTikTokStatus(b); err != nil {
		return dy.User{}, err
	}
	var r struct {
		UserInfo ttUserDetail `json:"userInfo"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return dy.User{}, err
	}
	return r.UserInfo.toUser(), nil
}

// Posts pages through a creator's published videos.
func (c *Client) Posts(secUID string) dy.Pager[dy.Video] {
	cursor := int64(0)
	return pager(func() ([]dy.Video, bool, error) {
		q := fmt.Sprintf("secUid=%s&count=20&cursor=%d", url.QueryEscape(secUID), cursor)
		b, err := c.call(http.MethodGet, "/api/post/item_list/", q, "")
		if err != nil {
			return nil, false, err
		}
		if err := checkTikTokStatus(b); err != nil {
			return nil, false, err
		}
		var r struct {
			ItemList []ttItem `json:"itemList"`
			Cursor   flexInt  `json:"cursor"`
			HasMore  bool     `json:"hasMore"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, false, err
		}
		next := int64(r.Cursor)
		more := r.HasMore && next != cursor
		cursor = next
		return parseItems(r.ItemList), more, nil
	})
}

// Liked pages through videos liked by secUID.
func (c *Client) Liked(secUID string) dy.Pager[dy.Video] {
	cursor := int64(0)
	return pager(func() ([]dy.Video, bool, error) {
		q := fmt.Sprintf("secUid=%s&count=20&cursor=%d", url.QueryEscape(secUID), cursor)
		b, err := c.call(http.MethodGet, "/api/favorite/item_list/", q, "")
		if err != nil {
			return nil, false, err
		}
		if err := checkTikTokStatus(b); err != nil {
			return nil, false, err
		}
		var r struct {
			ItemList []ttItem `json:"itemList"`
			Cursor   flexInt  `json:"cursor"`
			HasMore  bool     `json:"hasMore"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, false, err
		}
		next := int64(r.Cursor)
		more := r.HasMore && next != cursor
		cursor = next
		return parseItems(r.ItemList), more, nil
	})
}

// Collected pages through the signed-in user's bookmarked videos.
func (c *Client) Collected() dy.Pager[dy.Video] {
	cursor := int64(0)
	return pager(func() ([]dy.Video, bool, error) {
		q := fmt.Sprintf("count=20&cursor=%d", cursor)
		b, err := c.call(http.MethodGet, "/api/user/collect/item_list/", q, "")
		if err != nil {
			return nil, false, err
		}
		if err := checkTikTokStatus(b); err != nil {
			return nil, false, err
		}
		var r struct {
			ItemList []ttItem `json:"itemList"`
			Cursor   flexInt  `json:"cursor"`
			HasMore  bool     `json:"hasMore"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, false, err
		}
		next := int64(r.Cursor)
		more := r.HasMore && next != cursor
		cursor = next
		return parseItems(r.ItemList), more, nil
	})
}

// Comments pages through top-level comments on videoID.
func (c *Client) Comments(videoID string) dy.Pager[dy.Comment] {
	cursor := int64(0)
	return pager(func() ([]dy.Comment, bool, error) {
		region, _ := c.currentRegion()
		q := fmt.Sprintf("aweme_id=%s&count=20&cursor=%d&current_region=%s&enter_from=tiktok_web&from_page=video",
			url.QueryEscape(videoID), cursor, url.QueryEscape(region))
		b, err := c.call(http.MethodGet, "/api/comment/list/", q, "")
		if err != nil {
			return nil, false, err
		}
		if err := checkTikTokStatus(b); err != nil {
			return nil, false, err
		}
		var r struct {
			Comments []ttComment `json:"comments"`
			Cursor   flexInt     `json:"cursor"`
			HasMore  int         `json:"has_more"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, false, err
		}
		out := make([]dy.Comment, 0, len(r.Comments))
		for _, cm := range r.Comments {
			out = append(out, cm.toComment())
		}
		next := int64(r.Cursor)
		more := r.HasMore != 0 && next != cursor
		cursor = next
		return out, more, nil
	})
}

// Replies pages through replies under commentID on videoID.
func (c *Client) Replies(videoID, commentID string) dy.Pager[dy.Comment] {
	cursor := int64(0)
	return pager(func() ([]dy.Comment, bool, error) {
		region, _ := c.currentRegion()
		q := fmt.Sprintf("item_id=%s&comment_id=%s&count=20&cursor=%d&current_region=%s&enter_from=tiktok_web&from_page=video",
			url.QueryEscape(videoID), url.QueryEscape(commentID), cursor, url.QueryEscape(region))
		b, err := c.call(http.MethodGet, "/api/comment/list/reply/", q, "")
		if err != nil {
			return nil, false, err
		}
		if err := checkTikTokStatus(b); err != nil {
			return nil, false, err
		}
		var r struct {
			Comments []ttComment `json:"comments"`
			Cursor   flexInt     `json:"cursor"`
			HasMore  int         `json:"has_more"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, false, err
		}
		out := make([]dy.Comment, 0, len(r.Comments))
		for _, cm := range r.Comments {
			out = append(out, cm.toComment())
		}
		next := int64(r.Cursor)
		more := r.HasMore != 0 && next != cursor
		cursor = next
		return out, more, nil
	})
}

// Video fetches a single TikTok video by ID.
func (c *Client) Video(id string) (dy.Video, error) {
	b, err := c.call(http.MethodGet, "/api/item/detail/", "itemId="+url.QueryEscape(id), "")
	if err != nil {
		return dy.Video{}, err
	}
	if err := checkTikTokStatus(b); err != nil {
		return dy.Video{}, err
	}
	var r struct {
		ItemInfo struct {
			ItemStruct ttItem `json:"itemStruct"`
		} `json:"itemInfo"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return dy.Video{}, err
	}
	vs := parseItems([]ttItem{r.ItemInfo.ItemStruct})
	if len(vs) == 0 {
		return dy.Video{}, errors.New("tiktok: video has no playable stream")
	}
	return vs[0], nil
}

// Self fetches the currently logged-in TikTok user info.
// When session cookies are already loaded, it tries pure-Go direct HTTP first
// (~150ms, no WebView startup race) and falls back to the attached WebView.
func (c *Client) Self() (dy.User, error) {
	parseSelf := func(b []byte) (dy.User, error) {
		var r struct {
			Message string `json:"message"`
			Data    struct {
				UserIDStr string `json:"user_id_str"`
				SecUserID string `json:"sec_user_id"`
				Username  string `json:"username"`
				Screen    string `json:"screen_name"`
				Avatar    string `json:"avatar_url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return dy.User{}, err
		}
		if r.Message != "success" || r.Data.UserIDStr == "" {
			return dy.User{}, ErrNeedLogin
		}
		return dy.User{
			ID:     firstNonEmpty(r.Data.SecUserID, r.Data.Username),
			UID:    r.Data.UserIDStr,
			Name:   firstNonEmpty(r.Data.Screen, r.Data.Username),
			Handle: r.Data.Username,
			Avatar: r.Data.Avatar,
		}, nil
	}

	relURL := "/passport/web/account/info/?" + c.commonQuery("")
	fullURL := strings.TrimRight(c.baseURL, "/") + relURL
	if c.HasSession() || c.getWeb() == nil {
		if b, err := c.callDirect(http.MethodGet, fullURL, ""); err == nil {
			if u, perr := parseSelf(b); perr == nil {
				return u, nil
			} else if errors.Is(perr, ErrNeedLogin) && c.getWeb() == nil {
				return dy.User{}, ErrNeedLogin
			}
		}
	}
	if w := c.getWeb(); w != nil {
		b, err := c.callViaWeb(w, http.MethodGet, relURL, "")
		if err != nil {
			return dy.User{}, err
		}
		return parseSelf(b)
	}
	b, err := c.callDirect(http.MethodGet, fullURL, "")
	if err != nil {
		return dy.User{}, err
	}
	return parseSelf(b)
}

// CheckLogin reports whether cs represents a live TikTok session.
func (c *Client) CheckLogin(cs []dy.Cookie) (bool, error) {
	if !LoggedIn(cs) {
		return false, nil
	}
	return true, nil
}

// SetLiked likes or unlikes a TikTok video via the authenticated WebView.
func (c *Client) SetLiked(id string, on bool) error {
	typ := "0"
	if on {
		typ = "1"
	}
	b, err := c.call(http.MethodPost, "/api/commit/item/digg/", fmt.Sprintf("aweme_id=%s&type=%s", url.QueryEscape(id), typ), "")
	if err != nil {
		return err
	}
	return checkTikTokStatus(b)
}

// SetCommentLiked likes or unlikes a TikTok comment or reply.
func (c *Client) SetCommentLiked(videoID, commentID string, _ bool, on bool) error {
	diggType := "2"
	if on {
		diggType = "1"
	}
	q := fmt.Sprintf("aweme_id=%s&cid=%s&digg_type=%s&channel_id=0",
		url.QueryEscape(videoID), url.QueryEscape(commentID), diggType)
	b, err := c.call(http.MethodPost, "/api/comment/digg/", q, "")
	if err != nil {
		return err
	}
	return checkTikTokStatus(b)
}

// SetSaved bookmarks or unbookmarks a TikTok video via the authenticated WebView.
func (c *Client) SetSaved(id string, on bool) error {
	action := "0"
	if on {
		action = "1"
	}
	b, err := c.call(http.MethodPost, "/api/commit/item/collect/", fmt.Sprintf("aweme_id=%s&action=%s", url.QueryEscape(id), action), "")
	if err != nil {
		return err
	}
	return checkTikTokStatus(b)
}

// SetFollowed follows or unfollows a TikTok creator.
func (c *Client) SetFollowed(uid, secUID string, on bool) (int, error) {
	typ := "0"
	if on {
		typ = "1"
	}
	q := fmt.Sprintf("user_id=%s&sec_user_id=%s&type=%s", url.QueryEscape(uid), url.QueryEscape(secUID), typ)
	b, err := c.call(http.MethodPost, "/api/commit/follow/user/", q, "")
	if err != nil {
		return 0, err
	}
	if err := checkTikTokStatus(b); err != nil {
		return 0, err
	}
	if on {
		return 1, nil
	}
	return 0, nil
}

// PublishComment posts a comment or reply on a TikTok video.
func (c *Client) PublishComment(req dy.PublishCommentReq) (dy.Comment, error) {
	q := fmt.Sprintf("aweme_id=%s&text=%s", url.QueryEscape(req.VideoID), url.QueryEscape(req.Text))
	if req.ReplyID != "" {
		q += "&reply_id=" + url.QueryEscape(req.ReplyID)
	}
	b, err := c.call(http.MethodPost, "/api/comment/publish/", q, "")
	if err != nil {
		return dy.Comment{}, err
	}
	if err := checkTikTokStatus(b); err != nil {
		return dy.Comment{}, err
	}
	var r struct {
		Comment ttComment `json:"comment"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return dy.Comment{}, err
	}
	return r.Comment.toComment(), nil
}

// MediaHeaders returns the HTTP headers required by TikTok's video CDN,
// including the session Cookie (tt_chain_token) without which CDN nodes 403.
func (c *Client) MediaHeaders(_ dy.Video) map[string]string {
	userAgent := ua
	if s, _ := c.webUA.Load().(string); s != "" {
		userAgent = s
	}
	h := map[string]string{
		"Origin":     BaseURL,
		"Referer":    BaseURL + "/",
		"Accept":     "*/*",
		"User-Agent": userAgent,
	}
	if ck := c.cookies().mediaCookie; ck != "" {
		h["Cookie"] = ck
	}
	return h
}

// Image fetches a TikTok cover or avatar through the configured proxy.
func (c *Client) Image(u string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", BaseURL+"/")
	if ck := c.cookies().cookie; ck != "" {
		req.Header.Set("Cookie", ck)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tiktok: image %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// FetchSubtitle downloads and parses a WebVTT subtitle track over pure Go HTTP
// (through the configured proxy, without touching the WebView).
func (c *Client) FetchSubtitle(rawURL string) ([]SubtitleCue, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.MediaHeaders(dy.Video{}) {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tiktok: subtitle %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return ParseWebVTT(string(b)), nil
}

var _ platform.Client = (*Client)(nil)
