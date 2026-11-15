// Package dy talks to Douyin's web API without a browser: requests are signed
// with a_bogus (internal/sign) and carry the cookies saved by the login step.
package dy

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"douyin/internal/sign"
)

// ua and the browser_version in baseQuery must agree; the signature carries the UA.
const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

const baseQuery = "device_platform=webapp&aid=6383&channel=channel_pc_web&pc_client_type=1&version_code=190500&version_name=19.5.0&cookie_enabled=true&screen_width=1920&screen_height=1080&browser_language=zh-CN&browser_platform=Win32&browser_name=Chrome&browser_version=130.0.0.0&browser_online=true&engine_name=Blink&engine_version=130.0.0.0&os_name=Windows&os_version=10&cpu_core_num=8&device_memory=8&platform=PC"

type Client struct {
	http    *http.Client
	signer  *sign.Signer
	cookie  string
	refresh int
}

// Video is one entry of the recommend feed that has a playable video.
type Video struct {
	ID, Desc, Author string
	Size             int64
	PlayURL          string // carries audio; the bit_rate gears named adapt_* are video-only
	W, H             int
	Duration         time.Duration
	Likes, Comments  int
}

// New loads the cookies the login step saved; without them the client is anonymous.
func New() (*Client, error) {
	c := &Client{http: &http.Client{Timeout: 60 * time.Second}, signer: sign.New(ua)}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Douyin", "cookies.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, c.anonymous()
	}
	if err != nil {
		return nil, err
	}
	var cookies []struct{ Name, Value string }
	if err := json.Unmarshal(raw, &cookies); err != nil {
		return nil, err
	}
	parts := make([]string, len(cookies))
	for i, ck := range cookies {
		parts[i] = ck.Name + "=" + ck.Value
	}
	c.cookie = strings.Join(parts, "; ")
	return c, nil
}

// anonymous registers a device cookie (ttwid), which is all a logged-out feed needs.
func (c *Client) anonymous() error {
	body := `{"region":"cn","aid":1768,"needFid":false,"service":"www.ixigua.com","migrate_info":{"ticket":"","source":"node"},"cbUrlProtocol":"https","union":true}`
	resp, err := c.http.Post("https://ttwid.bytedance.com/ttwid/union/register/", "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Name == "ttwid" {
			c.cookie = "ttwid=" + ck.Value
			return nil
		}
	}
	return errors.New("dy: no ttwid in response")
}

func randString(n int, set string) string {
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		b[i] = set[x.Int64()]
	}
	return string(b)
}

const (
	alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	token = alnum + "-_"
)

// Get calls a GET endpoint of www.douyin.com with extra (already encoded) query
// parameters, and returns the body.
func (c *Client) Get(path, extra string) ([]byte, error) {
	fp := "verify_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "_" + randString(8, alnum) + "_" + randString(4, alnum) + "_" + randString(4, alnum) + "_" + randString(4, alnum) + "_" + randString(12, alnum)
	q := baseQuery + "&verifyFp=" + fp + "&fp=" + fp
	if extra != "" {
		q += "&" + extra
	}
	q += "&msToken=" + randString(107, token)
	q += "&a_bogus=" + url.QueryEscape(c.signer.Sign(q, ""))
	req, err := http.NewRequest("GET", "https://www.douyin.com"+path+"?"+q, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.douyin.com/")
	req.Header.Set("Cookie", c.cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err == nil && resp.StatusCode != 200 {
		err = fmt.Errorf("dy: %s %s", path, resp.Status)
	}
	return b, err
}

type addr struct {
	URLs []string `json:"url_list"`
	W    int      `json:"width"`
	H    int      `json:"height"`
	Size int64    `json:"data_size"`
}

// playURL is the address of an H.264 stream with audio. The top play_addr can
// be 1080p and 100 MB+ for a long video, and the window shows 540p fine, so
// take the smallest file of at least 540p; the adapt_* gears have no audio.
func playURL(top addr, gears []struct {
	Name string `json:"gear_name"`
	H265 int    `json:"is_h265"`
	Addr addr   `json:"play_addr"`
}) (u string, best addr) {
	find := func(a addr) string {
		for _, u := range a.URLs {
			if strings.Contains(u, "aweme/v1/play") {
				return u
			}
		}
		return ""
	}
	bestSize := int64(0)
	for _, g := range gears {
		if g.H265 != 0 || strings.HasPrefix(g.Name, "adapt") || find(g.Addr) == "" || min(g.Addr.W, g.Addr.H) < 540 {
			continue
		}
		if bestSize == 0 || g.Addr.Size < bestSize {
			best, bestSize = g.Addr, g.Addr.Size
		}
	}
	if bestSize == 0 {
		best = top
	}
	return find(best), best
}

type awemeList struct {
	StatusCode int `json:"status_code"`
	List       []struct {
		ID     string `json:"aweme_id"`
		Desc   string
		Author struct{ Nickname string }
		Video  struct {
			Duration int  `json:"duration"`
			PlayAddr addr `json:"play_addr"`
			Gears    []struct {
				Name string `json:"gear_name"`
				H265 int    `json:"is_h265"`
				Addr addr   `json:"play_addr"`
			} `json:"bit_rate"`
		}
		Stats struct {
			Likes    int `json:"digg_count"`
			Comments int `json:"comment_count"`
		} `json:"statistics"`
	} `json:"aweme_list"`
}

// Feed returns the next page of the recommend feed: about 6 videos when
// logged in, 2 on the first call. Ads and photo posts, which have no play
// address, are dropped.
func (c *Client) Feed() ([]Video, error) {
	c.refresh++
	b, err := c.Get("/aweme/v1/web/tab/feed/", fmt.Sprintf("count=10&refresh_index=%d&video_type_select=1&aweme_pc_rec_raw_data=%%7B%%7D&globalwid=&pull_type=0", c.refresh))
	if err != nil {
		return nil, err
	}
	var r awemeList
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.StatusCode != 0 {
		return nil, fmt.Errorf("dy: feed status %d", r.StatusCode)
	}
	var out []Video
	for _, a := range r.List {
		play, pa := playURL(a.Video.PlayAddr, a.Video.Gears)
		if play == "" {
			continue
		}
		out = append(out, Video{ID: a.ID, Desc: a.Desc, Author: a.Author.Nickname, PlayURL: play,
			W: pa.W, H: pa.H, Size: pa.Size, Duration: time.Duration(a.Video.Duration) * time.Millisecond,
			Likes: a.Stats.Likes, Comments: a.Stats.Comments})
	}
	return out, nil
}

// Download saves a video to dst. The CDN refuses a request without the Referer.
func (c *Client) Download(u, dst string) error {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.douyin.com/")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("dy: download %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst + ".part")
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst + ".part")
		return err
	}
	return os.Rename(dst+".part", dst)
}
