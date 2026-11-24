package dy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writes (likes, saves, 稍后再看, bullet comments, shares) go through Web,
// a Douyin page of the app's own, logged in there by the user's scan: its
// scripts add what a write needs, the signature, the ticket guard's headers
// and x-tt-session-dtrait, which binds the session to the device it was
// made on. Never send a write from Go with the session's cookies: Douyin
// answers 用户未登录 and logs the session out everywhere (2026-11-16, a
// login was lost so). Nor touch the page's natives before a write: with
// Headers.prototype and XMLHttpRequest.prototype hooked to read what the
// SDK adds, the page's own fetch got 403 and the session died with it
// (2026-11-16 20:54, a second login lost).
type Web interface {
	// Eval runs js, the body of an async function, in the page once its
	// scripts are up, and returns the JSON of what it returns.
	Eval(ctx context.Context, js string) ([]byte, error)
	// Load goes to url, and lays the page out, hidden under the app, while
	// layout: what a script finds by its place needs it.
	Load(ctx context.Context, url string, layout bool) error
	// Cookies are the page's douyin.com cookies.
	Cookies() ([]Cookie, error)
}

// Cookie is a cookie as cookies.json keeps it.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Expires  int64  `json:"expires"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"httpOnly"`
}

// LoggedIn tells whether cookies hold a login.
func LoggedIn(cs []Cookie) bool {
	for _, ck := range cs {
		if ck.Name == "sessionid" && ck.Value != "" {
			return true
		}
	}
	return false
}

// SetWeb gives the client the page its writes go through.
func (c *Client) SetWeb(w Web) { c.web = w }

// SaveCookies keeps cs as the client's login, in cookies.json too.
func (c *Client) SaveCookies(cs []Cookie) error {
	file, err := cookieFile()
	if err != nil {
		return err
	}
	b, _ := json.Marshal(cs)
	if err := writePrivate(file, b); err != nil {
		return err
	}
	return c.ReloadCookies()
}

// Logout forgets the login, its cookies and ticket guard material; the
// client goes on anonymous.
func (c *Client) Logout() error {
	for _, f := range []func() (string, error){cookieFile, guardFile} {
		if p, err := f(); err == nil {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return c.anonymous()
}

// writePrivate writes a file only the user reads, whole or not at all.
func writePrivate(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// writeMu keeps writes one at a time: they share the page.
var writeMu sync.Mutex

var (
	errNoWeb = errors.New("要先扫码登录抖音")
	// ErrNeedLogin is a write Douyin refused for want of a login.
	ErrNeedLogin = errors.New("抖音说没登录，请重新扫码登录")
)

const form = "application/x-www-form-urlencoded; charset=UTF-8"

// write sends a form to a write endpoint.
func (c *Client) write(path, body string) error {
	return c.post(path, "", body, form)
}

// post sends a POST to a write endpoint, with query and a body of type,
// from the page, and saves the cookies it leaves renewed.
func (c *Client) post(path, query, body, typ string) error {
	return c.send(path, query, body, typ, 3)
}

// send is post, tried up to tries times.
func (c *Client) send(path, query, body, typ string, tries int) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	if c.web == nil {
		return errNoWeb
	}
	u := "https://www-hj.douyin.com" + path + "?device_platform=webapp&aid=6383&channel=channel_pc_web&pc_client_type=1&version_code=170400&version_name=17.4.0"
	if query != "" {
		u += "&" + query
	}
	args, _ := json.Marshal([]string{u, typ, body})
	js := "const [u, type, body] = " + string(args) + `;
const r = await fetch(u, { method: "POST", credentials: "include", headers: { "content-type": type }, body });
const t = await r.text();
let code = null;
try { code = JSON.parse(t).status_code; } catch {}
return { http: r.status, code, text: t.slice(0, 300) };`
	// Only the reply's own status_code counts: it may nest others, as user_collect_count's.
	var r struct {
		HTTP int
		Code *int
		Text string
	}
	var err error
	for try := range tries {
		if try > 0 {
			time.Sleep(time.Second) // a page just loaded may sign only once its scripts settle
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var b []byte
		b, err = c.web.Eval(ctx, js)
		cancel()
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		switch {
		case err != nil:
		case r.HTTP == 200 && r.Code != nil && *r.Code == 0:
			c.syncCookies()
			return nil
		case r.Code != nil && *r.Code == 8:
			return ErrNeedLogin
		}
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("抖音没接受（HTTP %d）：%.200s", r.HTTP, r.Text)
}

// pageGet reads path with query from the page, whose scripts sign it as
// theirs: a read Douyin holds back from the client for its verification
// (ErrVerify) it gives the page once the page has passed it (2026-11-19,
// replies: passed in the page, the client's next ask was held back again).
func (c *Client) pageGet(path, query string) ([]byte, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	if c.web == nil {
		return nil, ErrVerify
	}
	args, _ := json.Marshal("https://www.douyin.com" + path + "?device_platform=webapp&aid=6383&channel=channel_pc_web&" + query)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := c.web.Eval(ctx, "const r = await fetch("+string(args)+`, { credentials: "include" });
return { http: r.status, text: await r.text() };`)
	if err != nil {
		return nil, err
	}
	var r struct {
		HTTP int
		Text string
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	switch {
	case r.HTTP != 200:
		return nil, fmt.Errorf("dy: %s HTTP %d", path, r.HTTP)
	case r.Text == "":
		return nil, ErrVerify // the page is held back too: it has to pass first
	}
	c.syncCookies()
	return []byte(r.Text), nil
}

// SyncCookies saves the page's current douyin.com cookies when they hold a login.
func (c *Client) SyncCookies() {
	if c.web != nil {
		c.syncCookies()
	}
}

// syncCookies saves the page's cookies, which it renews as it goes, while
// they hold the login.
func (c *Client) syncCookies() {
	if cs, err := c.web.Cookies(); err == nil && LoggedIn(cs) {
		c.SaveCookies(cs)
	}
}

// RefreshGuard saves the ticket guard's material (guard.go), which the page
// keeps in its localStorage.
func (c *Client) RefreshGuard() error {
	writeMu.Lock()
	defer writeMu.Unlock()
	if c.web == nil {
		return errNoWeb
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := c.web.Eval(ctx, `const j = (k) => JSON.parse(localStorage.getItem(k) || "null");
const cr = j("security-sdk/s_sdk_crypt_sdk"), s = j("security-sdk/s_sdk_sign_data_key/web_protect"), v = j("security-sdk/s_sdk_server_cert_key");
if (!cr || !s || !v) return null;
const cd = JSON.parse(cr.data), sd = JSON.parse(s.data);
return { ticket: sd.ticket, ts_sign: sd.ts_sign, private_key: cd.ec_privateKey, server_cert: v.cert };`)
	if err != nil {
		return err
	}
	var g guard
	if err := json.Unmarshal(b, &g); err != nil || g.Ticket == "" || g.PrivateKey == "" || g.ServerCert == "" {
		return errors.New("抖音页面里没有签名素材，可能没登录")
	}
	file, err := guardFile()
	if err != nil {
		return err
	}
	return writePrivate(file, b)
}

// SetLiked likes the video with the given ID, or takes the like back.
func (c *Client) SetLiked(id string, on bool) error {
	return c.write("/aweme/v1/web/commit/item/digg/", fmt.Sprintf("aweme_id=%s&item_type=0&type=%d", id, b2i(on)))
}

// SetSaved saves the video with the given ID (收藏), or takes it out.
func (c *Client) SetSaved(id string, on bool) error {
	return c.write("/aweme/v1/web/aweme/collect/", fmt.Sprintf("action=%d&aweme_id=%s&aweme_type=0", b2i(on), id))
}

// SetCommentLiked likes a comment (cid) of the video with the given ID, or
// takes the like back, as the page's heart does: digg_type 1 likes, 2 takes
// back; level 1 is a comment, 3 a reply (read from Douyin's page, 2026-11-16).
func (c *Client) SetCommentLiked(videoID, cid string, reply, on bool) error {
	digg, level := "2", "1"
	if on {
		digg = "1"
	}
	if reply {
		level = "3"
	}
	q := url.Values{"cid": {cid}, "aweme_id": {videoID}, "digg_type": {digg}, "channel_id": {"0"}, "app_name": {"aweme"},
		"item_type": {"0"}, "level": {level}, "enter_from": {"video_detail"}, "previous_page": {"video_detail"}}
	return c.post("/aweme/v1/web/comment/digg", q.Encode(), "", form)
}

// Watched tells Douyin the user watched the video with the given ID, as its
// page does as one plays: into 观看历史, and a play counted. Without it the
// recommend feed brings back what was watched here (2026-11-19).
// authorUID is the author's numeric ID (Video.AuthorUID).
func (c *Client) Watched(id, authorUID string) error {
	q := url.Values{"author_id": {authorUID}, "aweme_id": {id}}
	if err := c.write("/aweme/v1/web/history/write/", q.Encode()); err != nil {
		return err
	}
	// Best effort, once: Douyin answers most videos' play with nothing, its
	// own page's too (2026-11-19, 3 of 4 from www.douyin.com, fetch and XHR).
	q = url.Values{"aweme_type": {"0"}, "item_id": {id}, "play_delta": {"1"}, "source": {"0"}}
	c.send("/aweme/v2/web/aweme/stats/", "", q.Encode(), form, 1)
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetWatchLater puts the video with the given ID in 稍后再看, or takes it out.
func (c *Client) SetWatchLater(id string, on bool) error {
	if on {
		body, _ := json.Marshal(map[string]any{"item_to_playinfo": map[string]any{id: map[string]int{"play_duration": 0, "loop_status": 0}}, "action_type": 1})
		return c.post("/aweme/v1/web/watchlater/upsert/", "", string(body), "application/json")
	}
	body, _ := json.Marshal(map[string]any{"remove_item_ids": []string{id}, "remove_type": 0})
	return c.post("/aweme/v1/web/watchlater/remove/", "", string(body), "application/json")
}

// SendDanmaku posts a bullet comment on the video with the given ID, at
// where it plays; everyone watching it sees it. uid is the user's own
// numeric ID (User.UID).
func (c *Client) SendDanmaku(id, uid string, at time.Duration, text string) error {
	q := url.Values{"item_id": {id}, "offset_time": {fmt.Sprint(at.Milliseconds())}, "text": {text}, "user_id": {uid},
		"enter_from": {"video_detail"}, "previous_page": {"video_detail"}}
	return c.post("/aweme/v1/web/danmaku/post_v2/", q.Encode(), "", form)
}

// ShareTo sends the video with the given ID to someone in a direct message,
// as the web page's share panel does: the panel lists the user's friends
// and finds others by name. With dry it goes as far as the message leaving
// and stops it there, which tests the way without sending anything.
func (c *Client) ShareTo(videoID, name string, dry bool) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	if c.web == nil {
		return errNoWeb
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.web.Load(ctx, "https://www.douyin.com/video/"+videoID, true); err != nil {
		return err
	}
	defer c.web.Load(context.Background(), "", false) // hidden again, where it is
	args, _ := json.Marshal([]any{name, dry})
	b, err := c.web.Eval(ctx, "const [name, dry] = "+string(args)+";\n"+shareJS)
	if err != nil {
		return err
	}
	var r struct {
		OK, Dry bool
		Reply   string
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	switch {
	case r.OK && dry && !r.Dry:
		return errors.New("演练没拦住发送")
	case !r.OK:
		return fmt.Errorf("没分享出去：%.200s", r.Reply)
	}
	c.syncCookies()
	return nil
}

// shareJS, in the video's page, opens its share panel as hovering the share
// button does, finds the friend's row (by the panel's search if it is not
// listed), clicks its 分享 and reads what the message's sending (imapi
// /v1/message/send, protobuf) answered: status is field 3, its message
// field 4. Names compare without spaces and with full-width bars as plain
// ones: the page shows "斑斓Lab｜AIGC" for the API's "斑斓Lab | AIGC".
const shareJS = `
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const decode = (b) => {
  let i = 0, st = -1, msg = "";
  const varint = () => { let x = 0, s = 0, c; do { c = b[i++]; x += (c & 0x7f) * 2 ** s; s += 7; } while (c & 0x80); return x; };
  while (i < b.length) {
    const k = varint(), f = Math.floor(k / 8), t = k & 7;
    if (t == 0) { const v = varint(); if (f == 3) st = v; }
    else if (t == 2) { const n = varint(); if (f == 4) msg = new TextDecoder().decode(b.slice(i, i + n)); i += n; }
    else if (t == 1) i += 8; else if (t == 5) i += 4; else break;
  }
  return { ok: st === 0, reply: st === 0 ? "OK" : "status " + st + " " + msg };
};
const norm = (s) => s.replace(/\s+/g, "").replace(/｜/g, "|");
const leaves = (root, text) => [...root.querySelectorAll("*")].filter(e => e.children.length === 0 && norm(e.textContent) === norm(text) && e.getBoundingClientRect().width > 0);
const panelOf = () => { let p = leaves(document, "分享给朋友")[0]; while (p && !p.querySelector("input")) p = p.parentElement; return p; };
const rowButton = () => {
  const panel = panelOf();
  if (!panel) return "no panel";
  for (const n of leaves(panel, name)) {
    let row = n;
    while (row && row !== panel && leaves(row, "分享").length === 0) row = row.parentElement;
    if (row && row !== panel && leaves(row, "分享").length === 1) return leaves(row, "分享")[0];
  }
  return "not listed";
};
// The message leaves by fetch or XMLHttpRequest, which the page's own
// scripts wrapped as it loaded: these wrap theirs, to see it, or stop it.
const isSend = (u) => /imapi\.douyin\.com\/v1\/message\/send/.test(String(u));
window.__mdShare = undefined;
window.__mdDry = dry;
if (!window.__mdHooked) {
  window.__mdHooked = true;
  const of = window.fetch;
  window.fetch = function (input, init) {
    if (!isSend(typeof input === "string" ? input : input && input.url)) return of.apply(this, arguments);
    if (window.__mdDry) { window.__mdShare = { ok: true, dry: true, reply: "演练：发送已拦下" }; return Promise.reject(new TypeError("blocked")); }
    return of.apply(this, arguments).then(r => { r.clone().arrayBuffer().then(b => { window.__mdShare = window.__mdDecode(new Uint8Array(b)); }); return r; });
  };
  const open = XMLHttpRequest.prototype.open, send = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function (m, u) { this.__mdSend = isSend(u); return open.apply(this, arguments); };
  XMLHttpRequest.prototype.send = function () {
    if (!this.__mdSend) return send.apply(this, arguments);
    if (window.__mdDry) { window.__mdShare = { ok: true, dry: true, reply: "演练：发送已拦下" }; return; }
    this.addEventListener("loadend", () => {
      const r = this.response;
      window.__mdShare = r instanceof ArrayBuffer ? window.__mdDecode(new Uint8Array(r)) : { ok: this.status === 200, reply: "HTTP " + this.status };
    });
    return send.apply(this, arguments);
  };
}
window.__mdDecode = decode;
let icon;
for (let i = 0; i < 40 && !icon; i++) { await sleep(500); icon = document.querySelector('[data-e2e="video-share-icon-container"]'); }
if (!icon) return { reply: "视频页没载入分享按钮" };
document.querySelectorAll("video").forEach(v => { v.muted = true; v.pause(); });
const hover = (e) => { for (const t of ["pointerover", "pointerenter", "mouseover", "mouseenter", "mousemove"]) e.dispatchEvent(new MouseEvent(t, { bubbles: t !== "mouseenter" && t !== "pointerenter", cancelable: true, view: window })); };
hover(icon);
let btn = "";
for (let i = 0; i < 10 && !(btn instanceof Element); i++) { await sleep(500); btn = rowButton(); if (btn === "no panel" && i == 3) icon.click(); if (btn === "not listed") break; }
if (btn === "not listed") {
  const input = panelOf().querySelector("input");
  input.focus();
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, name);
  input.dispatchEvent(new Event("input", { bubbles: true }));
  for (let i = 0; i < 10 && !(btn instanceof Element); i++) { await sleep(500); btn = rowButton(); }
}
if (!(btn instanceof Element)) return { reply: btn === "no panel" ? "分享面板没打开" : "分享面板里找不到「" + name + "」" };
btn.click();
for (let i = 0; i < 200 && window.__mdShare === undefined; i++) await sleep(100);
return window.__mdShare || { reply: "等了 20 秒没见到发送" };
`

// DeleteDanmaku takes back a bullet comment of the user's own, by its ID,
// as the web page's menu on it does (user_type 1: its sender).
func (c *Client) DeleteDanmaku(danmakuID string) error {
	q := url.Values{"app_name": {"aweme"}, "format": {"json"}, "user_type": {"1"}, "danmaku_id": {danmakuID}}
	return c.post("/aweme/v1/web/danmaku/delete_v2/", q.Encode(), "", form)
}

// DanmakuRef is a bullet comment as its sender may take it back.
type DanmakuRef struct {
	ID, Text string
	At       time.Duration
}

// MyDanmaku finds the bullet comments the user (uid, User.UID) left on the
// video with the given ID, of length dur, reading it 32 seconds at a time.
func (c *Client) MyDanmaku(id, uid string, dur time.Duration) ([]DanmakuRef, error) {
	var out []DanmakuRef
	for from := time.Duration(0); from < dur; from += 32 * time.Second {
		var r struct {
			status
			List []struct {
				ID     string `json:"danmaku_id"`
				User   string `json:"user_id"`
				Text   string
				Offset int64 `json:"offset_time"`
			} `json:"danmaku_list"`
		}
		q := fmt.Sprintf("format=json&group_id=%s&item_id=%s&start_time=%d&end_time=%d", id, id, from.Milliseconds(), (from + 32*time.Second).Milliseconds())
		if err := c.getJSON("/aweme/v1/web/danmaku/get_v2/", q, &r); err != nil {
			return out, err
		}
		for _, d := range r.List {
			if d.User == uid {
				out = append(out, DanmakuRef{d.ID, d.Text, time.Duration(d.Offset) * time.Millisecond})
			}
		}
	}
	return out, nil
}
