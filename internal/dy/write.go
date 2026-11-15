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

// writeScript runs in ego lite: it opens a Douyin page without a video
// playing, sends the request from it, retrying while the page's scripts
// load, then saves the cookies if the login still holds, and closes.
const writeScript = `
const [path, body, cookieFile] = ARGS;
const task = await taskSpace("mydouyin write");
const out = { ok: false };
try {
  const page = task.page("p1");
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
  const self = await page.evaluate(async () => (await fetch("/aweme/v1/web/user/profile/self/?device_platform=webapp&aid=6383&channel=channel_pc_web", { credentials: "include" }).then(r => r.text())).match(/"status_code":\d+/)?.[0]);
  const { cookies } = await page.cdp("Network.getCookies", { urls: ["https://www.douyin.com/", "https://douyin.com/", "https://www-hj.douyin.com/"] });
  if (self === '"status_code":0' && cookies.some(c => c.name === "sessionid")) {
    const fs = await import("node:fs/promises");
    await fs.writeFile(cookieFile + ".tmp", JSON.stringify(cookies), { mode: 0o600 });
    await fs.rename(cookieFile + ".tmp", cookieFile);
    out.cookies = cookies.length;
  } else {
    out.loggedOut = true;
  }
} catch (e) {
  out.error = String(e);
} finally {
  await task.finish({ keep: [] });
}
console.log("MYDOUYIN " + JSON.stringify(out));
`

// write sends a form to a write endpoint through ego lite, then reads the
// cookies it renewed.
func (c *Client) write(path, body string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	ego, err := egoBrowser()
	if err != nil {
		return err
	}
	file, err := cookieFile()
	if err != nil {
		return err
	}
	args, _ := json.Marshal([]string{path, body, file})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ego, "nodejs")
	cmd.Stdin = strings.NewReader("const ARGS = " + string(args) + ";\n" + writeScript)
	var outb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outb, &outb
	runErr := cmd.Run()
	var r struct {
		OK        bool
		Reply     string
		Cookies   int
		LoggedOut bool
		Error     string
	}
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
		return fmt.Errorf("ego lite 没跑完（%v）：%s", runErr, tail)
	}
	if r.Cookies > 0 {
		if err := c.ReloadCookies(); err != nil {
			return err
		}
	}
	switch {
	case r.Error != "":
		return errors.New("ego lite：" + r.Error)
	case !r.OK && r.LoggedOut:
		return errors.New("ego lite 里的抖音没登录，请先在 ego lite 里登录抖音")
	case !r.OK:
		return fmt.Errorf("抖音没接受：%.200s", r.Reply)
	}
	return nil
}
