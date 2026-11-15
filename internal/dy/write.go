package dy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Liking and saving go through the user's own browser, ego lite: Douyin's
// write endpoints want a header bound to the browser's device
// (x-tt-session-dtrait), which only its page makes. The page adds it, with
// the ticket guard's and the signature, to a fetch it sends itself
// (checked 2026-11-16). Each write opens Douyin in a task space of its own,
// sends the request there, exports the cookies it leaves renewed, and
// closes: a Douyin page left open renews the session by itself, which would
// make the client's copy of the cookies stale.

// SetLiked likes the video with the given ID, or takes the like back.
func (c *Client) SetLiked(id string, on bool) error {
	return c.write("/aweme/v1/web/commit/item/digg/", fmt.Sprintf("aweme_id=%s&item_type=0&type=%d", id, b2i(on)))
}

// SetSaved saves the video with the given ID (收藏), or takes it out.
func (c *Client) SetSaved(id string, on bool) error {
	return c.write("/aweme/v1/web/aweme/collect/", fmt.Sprintf("action=%d&aweme_id=%s&aweme_type=0", b2i(on), id))
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// writeMu keeps writes one at a time: they share the browser's task space.
var writeMu sync.Mutex

// egoBrowser finds the ego lite command, which an app opened from the
// Finder does not have on its PATH.
func egoBrowser() (string, error) {
	if p, err := exec.LookPath("ego-browser"); err == nil {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, ".local", "bin", "ego-browser")
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("点赞和收藏要用 ego lite，没找到 ego-browser 命令")
	}
	return p, nil
}

// egoLib is what every script run in ego lite starts with: the task space,
// and done, which saves the cookies if the login still holds, closes the
// space and prints the result for the client.
const egoLib = `
const task = await taskSpace("mydouyin write");
const out = { ok: false };
const done = async (page) => {
  try {
    if (page) {
      const self = await page.evaluate(async () => (await fetch("/aweme/v1/web/user/profile/self/?device_platform=webapp&aid=6383&channel=channel_pc_web", { credentials: "include" }).then(r => r.text())).match(/"status_code":\d+/)?.[0]);
      const { cookies } = await page.cdp("Network.getCookies", { urls: ["https://www.douyin.com/", "https://douyin.com/", "https://www-hj.douyin.com/"] });
      if (self === '"status_code":0' && cookies.some(c => c.name === "sessionid")) {
        const fs = await import("node:fs/promises");
        await fs.writeFile(COOKIES + ".tmp", JSON.stringify(cookies), { mode: 0o600 });
        await fs.rename(COOKIES + ".tmp", COOKIES);
        out.cookies = cookies.length;
      } else {
        out.loggedOut = true;
      }
    }
  } catch (e) {
    out.error = out.error || String(e);
  } finally {
    await task.finish({ keep: [] });
  }
  console.log("MYDOUYIN " + JSON.stringify(out));
};
`

// writeScript sends a form to a write endpoint from a Douyin page without a
// video playing, retrying while the page's scripts load.
const writeScript = `
const [path, body] = ARGS;
let page;
try {
  page = task.page("p1");
  await page.goto("https://www.douyin.com/user/self", { timeout: 40000 });
  for (let i = 0; i < 12 && !out.ok; i++) {
    await new Promise(r => setTimeout(r, i == 0 ? 2500 : 1000));
    const r = await page.evaluate(async ([path, body]) => {
      try {
        const r = await fetch("https://www-hj.douyin.com" + path + "?device_platform=webapp&aid=6383&channel=channel_pc_web&pc_client_type=1&version_code=170400&version_name=17.4.0", {
          method: "POST", credentials: "include", headers: { "content-type": "application/x-www-form-urlencoded; charset=UTF-8" }, body });
        return { http: r.status, text: (await r.text()).slice(0, 300) };
      } catch (e) { return { http: 0, text: String(e) }; }
    }, [path, body]);
    out.reply = r.text;
    out.ok = r.http == 200 && /"status_code":0[,}]/.test(r.text);
  }
} catch (e) {
  out.error = String(e);
}
await done(page);
`

// egoResult is what a script reports.
type egoResult struct {
	OK        bool
	Reply     string
	Cookies   int
	LoggedOut bool
	Error     string
	Dry       bool // a share stopped before it left, as asked
}

// write sends a form to a write endpoint through ego lite.
func (c *Client) write(path, body string) error {
	r, err := c.ego(writeScript, path, body)
	if err != nil {
		return err
	}
	return r.err("抖音没接受")
}

func (r egoResult) err(refused string) error {
	switch {
	case r.Error != "":
		return errors.New("ego lite：" + r.Error)
	case !r.OK && r.LoggedOut:
		return errors.New("ego lite 里的抖音没登录，请先在 ego lite 里登录抖音")
	case !r.OK:
		return fmt.Errorf("%s：%.200s", refused, r.Reply)
	}
	return nil
}

// ego runs a script in ego lite, one at a time, with args, and reads the
// cookies it renewed.
func (c *Client) ego(script string, args ...string) (egoResult, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	var r egoResult
	ego, err := egoBrowser()
	if err != nil {
		return r, err
	}
	file, err := cookieFile()
	if err != nil {
		return r, err
	}
	a, _ := json.Marshal(args)
	f, _ := json.Marshal(file)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ego, "nodejs")
	cmd.Stdin = strings.NewReader("const ARGS = " + string(a) + ";\nconst COOKIES = " + string(f) + ";\n" + egoLib + script)
	var outb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outb, &outb
	runErr := cmd.Run()
	found := false
	sc := bufio.NewScanner(&outb)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "MYDOUYIN "); ok {
			found = json.Unmarshal([]byte(line), &r) == nil
		}
	}
	if !found {
		tail := strings.TrimSpace(outb.String())
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return r, fmt.Errorf("ego lite 没跑完（%v）：%s", runErr, tail)
	}
	if r.Cookies > 0 {
		if err := c.ReloadCookies(); err != nil {
			return r, err
		}
	}
	return r, nil
}

// ShareTo sends the video with the given ID to someone in a direct message,
// as the web page's share panel does: the panel lists the user's friends
// and finds others by name. With dry it goes as far as the message leaving
// and stops it there, which tests the way without sending anything.
func (c *Client) ShareTo(videoID, name string, dry bool) error {
	r, err := c.ego(shareScript, videoID, name, map[bool]string{true: "1"}[dry])
	if err != nil {
		return err
	}
	if r.OK && dry && !r.Dry {
		return errors.New("演练没拦住发送")
	}
	return r.err("没分享出去")
}

// shareScript opens the video's page silent, its share panel by hovering
// the share button, finds the friend's row (by the panel's search if it is
// not listed), clicks its 分享 and reads what the message's sending
// (imapi /v1/message/send, protobuf) answered: status is field 3, its
// message field 4.
const shareScript = `
const [id, name, dry] = ARGS;
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const status = (b64) => {
  const b = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
  let i = 0, st = -1, msg = "";
  const varint = () => { let x = 0, s = 0, c; do { c = b[i++]; x += (c & 0x7f) * 2 ** s; s += 7; } while (c & 0x80); return x; };
  while (i < b.length) {
    const k = varint(), f = Math.floor(k / 8), t = k & 7;
    if (t == 0) { const v = varint(); if (f == 3) st = v; }
    else if (t == 2) { const n = varint(); if (f == 4) msg = new TextDecoder().decode(b.slice(i, i + n)); i += n; }
    else if (t == 1) i += 8; else if (t == 5) i += 4; else break;
  }
  return [st, msg];
};
let page;
try {
  page = task.page("p1");
  await page.goto("https://www.douyin.com/video/" + id, { timeout: 40000 });
  for (let i = 0; i < 30; i++) {
    await sleep(500);
    if (await page.evaluate(() => !!document.querySelector('[data-e2e="video-share-icon-container"]'))) break;
  }
  await page.evaluate(() => document.querySelectorAll("video").forEach(v => { v.muted = true; v.pause(); }));
  await page.hover('[data-e2e="video-share-icon-container"]', { label: "open the share panel" });
  // mark finds the panel and the 分享 button of name's row, marking it to click.
  // Names compare without spaces and with full-width bars as plain ones: the
  // page shows "斑斓Lab｜AIGC" for the API's "斑斓Lab | AIGC".
  const mark = () => page.evaluate((name) => {
    const norm = (s) => s.replace(/\s+/g, "").replace(/｜/g, "|");
    const leaves = (root, text) => [...root.querySelectorAll("*")].filter(e => e.children.length === 0 && norm(e.textContent) === norm(text) && e.getBoundingClientRect().width > 0);
    const head = leaves(document, "分享给朋友")[0];
    if (!head) return "no panel";
    let panel = head;
    while (panel && !panel.querySelector("input")) panel = panel.parentElement;
    if (!panel) return "no panel";
    for (const n of leaves(panel, name)) {
      let row = n;
      while (row && row !== panel && leaves(row, "分享").length === 0) row = row.parentElement;
      if (row && row !== panel && leaves(row, "分享").length === 1) {
        leaves(row, "分享")[0].setAttribute("data-agent", "share-to");
        return "found";
      }
    }
    return "not listed";
  }, name);
  let found = "";
  for (let i = 0; i < 10 && found !== "found"; i++) { await sleep(500); found = await mark(); if (found === "not listed") break; }
  if (found === "not listed") {
    await page.evaluate(() => {
      const head = [...document.querySelectorAll("*")].find(e => e.children.length === 0 && e.textContent.trim() === "分享给朋友" && e.getBoundingClientRect().width > 0);
      let panel = head; while (panel && !panel.querySelector("input")) panel = panel.parentElement;
      panel.querySelector("input").setAttribute("data-agent", "share-search");
    });
    await page.fill('[data-agent="share-search"]', name, { clearFirst: true });
    for (let i = 0; i < 10 && found !== "found"; i++) { await sleep(500); found = await mark(); }
  }
  if (found !== "found") {
    out.reply = found === "no panel" ? "分享面板没打开" : "分享面板里找不到「" + name + "」";
  } else {
    if (dry) await page.cdp("Fetch.enable", { patterns: [{ urlPattern: "*imapi.douyin.com/v1/message/send*", requestStage: "Request" }] });
    else await page.cdp("Network.enable", {});
    await page.events();
    await page.click('[data-agent="share-to"]', { label: "share to " + name });
    const until = Date.now() + 20000;
    while (Date.now() < until && out.reply === undefined) {
      for (const e of await page.events()) {
        if (dry && e.method === "Fetch.requestPaused") {
          if (e.params.request.method !== "POST") { await page.cdp("Fetch.continueRequest", { requestId: e.params.requestId }).catch(() => {}); continue; }
          await page.cdp("Fetch.failRequest", { requestId: e.params.requestId, errorReason: "BlockedByClient" }).catch(() => {});
          out.ok = out.dry = true; out.reply = "演练：发送已拦下";
        }
        if (!dry && e.method === "Network.loadingFinished" && e.params.requestId === out.sendId) {
          const b = await page.cdp("Network.getResponseBody", { requestId: e.params.requestId });
          const [st, msg] = status(b.base64Encoded ? b.body : btoa(b.body));
          out.ok = st === 0; out.reply = st === 0 ? "OK" : "status " + st + " " + msg;
        }
        if (!dry && e.method === "Network.requestWillBeSent" && /imapi\.douyin\.com\/v1\/message\/send/.test(e.params.request.url) && e.params.request.method === "POST") out.sendId = e.params.requestId;
      }
      await sleep(100);
    }
    if (out.reply === undefined) out.reply = "等了 20 秒没见到发送";
    if (dry) await page.cdp("Fetch.disable", {}).catch(() => {});
  }
} catch (e) {
  out.error = String(e);
}
await done(page);
`
