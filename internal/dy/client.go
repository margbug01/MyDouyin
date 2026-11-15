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
	"sync/atomic"
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
	uifid   string       // the UIFID cookie, sent again as a header
	imSeq   atomic.Int32 // the IM requests' sequence
	refresh atomic.Int32 // the feed's page, which two lists of it share
}

// Video is one entry of the recommend feed that has a playable video.
type Video struct {
	ID, Desc, Author string
	AuthorID         string // sec_uid, which Posts takes
	Size             int64
	PlayURL          string // carries audio; the bit_rate gears named adapt_* are video-only
	W, H             int
	Duration         time.Duration
	Likes, Comments  int
	Collects, Shares int
	Cover, Avatar    string // image addresses: the frame the video shows first, the author's picture
	Created          time.Time
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
		if ck.Name == "UIFID" {
			c.uifid = ck.Value
		}
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
	return c.call("GET", path, extra, "")
}

// call is Get with any method and a form body, which a_bogus signs too.
func (c *Client) call(method, path, extra, body string) ([]byte, error) {
	fp := "verify_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "_" + randString(8, alnum) + "_" + randString(4, alnum) + "_" + randString(4, alnum) + "_" + randString(4, alnum) + "_" + randString(12, alnum)
	q := baseQuery + "&verifyFp=" + fp + "&fp=" + fp
	if extra != "" {
		q += "&" + extra
	}
	q += "&msToken=" + randString(107, token)
	q += "&a_bogus=" + url.QueryEscape(c.signer.Sign(q, body))
	req, err := http.NewRequest(method, "https://www.douyin.com"+path+"?"+q, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.douyin.com/")
	req.Header.Set("Cookie", c.cookie)
	if c.uifid != "" {
		// Douyin's gateway (ArgusSecurityPlugin) refuses a user's posts with 403
		// "Uifid Not Found" without uifid, then "Signature Not Found" without x-tt-argus.
		// shortcut: the gateway does not check x-tt-argus's value yet (as MediaCrawler
		// found); once it does, compute x-secsdk-web-signature instead.
		req.Header.Set("uifid", c.uifid)
		req.Header.Set("x-tt-argus", "1")
	}
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

type aweme struct {
	ID      string `json:"aweme_id"`
	Desc    string
	Created int64 `json:"create_time"`
	Author  struct {
		Nickname string
		SecUID   string `json:"sec_uid"`
		Avatar   addr   `json:"avatar_thumb"`
	}
	Video struct {
		Duration int  `json:"duration"`
		Cover    addr `json:"cover"`
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
		Collects int `json:"collect_count"`
		Shares   int `json:"share_count"`
	} `json:"statistics"`
}

// first is an address's first URL, or "".
func (a addr) first() string {
	if len(a.URLs) == 0 {
		return ""
	}
	return a.URLs[0]
}

// videos keeps the entries with a play address: ads and photo posts have none.
func videos(list []aweme) []Video {
	var out []Video
	for _, a := range list {
		play, pa := playURL(a.Video.PlayAddr, a.Video.Gears)
		if play == "" {
			continue
		}
		out = append(out, Video{ID: a.ID, Desc: a.Desc, Author: a.Author.Nickname, AuthorID: a.Author.SecUID, PlayURL: play,
			W: pa.W, H: pa.H, Size: pa.Size, Duration: time.Duration(a.Video.Duration) * time.Millisecond,
			Likes: a.Stats.Likes, Comments: a.Stats.Comments, Collects: a.Stats.Collects, Shares: a.Stats.Shares,
			Cover: a.Video.Cover.first(), Avatar: a.Author.Avatar.first(), Created: time.Unix(a.Created, 0)})
	}
	return out
}

// getJSON calls Get and decodes the body into v, which must carry status_code.
func (c *Client) getJSON(path, extra string, v interface{ code() int }) error {
	return c.callJSON("GET", path, extra, v)
}

// callJSON is getJSON with another method, an empty body.
func (c *Client) callJSON(method, path, extra string, v interface{ code() int }) error {
	b, err := c.call(method, path, extra, "")
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return fmt.Errorf("dy: %s: empty reply (signature or login refused)", path)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("dy: %s: %w", path, err)
	}
	if s := v.code(); s != 0 {
		return fmt.Errorf("dy: %s: status %d", path, s)
	}
	return nil
}

type status struct {
	Code int `json:"status_code"`
}

func (s status) code() int { return s.Code }

type awemeList struct {
	status
	List []aweme `json:"aweme_list"`
}

// Feed returns the next page of the recommend feed: about 6 videos when
// logged in, 2 on the first call. Ads and photo posts, which have no play
// address, are dropped.
func (c *Client) Feed() ([]Video, error) {
	n := c.refresh.Add(1)
	var r awemeList
	if err := c.getJSON("/aweme/v1/web/tab/feed/", fmt.Sprintf("count=10&refresh_index=%d&video_type_select=1&aweme_pc_rec_raw_data=%%7B%%7D&globalwid=&pull_type=0", n), &r); err != nil {
		return nil, err
	}
	return videos(r.List), nil
}

// A Pager returns the next page of a list each call, and an empty page after
// the last. Pages are fetched until one has an entry, as ads and photo posts
// are dropped and can make up a whole page.
type Pager[T any] func() ([]T, error)

// pager makes a Pager of fetch, which returns a page and whether more follow.
func pager[T any](fetch func() ([]T, bool, error)) Pager[T] {
	done := false
	return func() ([]T, error) {
		for !done {
			page, more, err := fetch()
			if err != nil {
				return nil, err
			}
			done = !more
			if len(page) > 0 {
				return page, nil
			}
		}
		return nil, nil
	}
}

// Search pages through the videos found for keyword.
func (c *Client) Search(keyword string) Pager[Video] {
	offset, id := 0, "" // later pages carry the search_id the first one returned
	return pager(func() ([]Video, bool, error) {
		var r struct {
			status
			Data []struct {
				Aweme *aweme `json:"aweme_info"`
			}
			HasMore int `json:"has_more"`
			Cursor  int `json:"cursor"`
			Extra   struct {
				LogID string `json:"logid"`
			}
		}
		q := fmt.Sprintf("search_channel=aweme_video_web&keyword=%s&search_source=normal_search&query_correct_type=1&is_filter_search=0&offset=%d&count=10", url.QueryEscape(keyword), offset)
		if id != "" {
			q += "&search_id=" + url.QueryEscape(id)
		}
		if err := c.getJSON("/aweme/v1/web/search/item/", q, &r); err != nil {
			return nil, false, err
		}
		var list []aweme
		for _, d := range r.Data {
			if d.Aweme != nil {
				list = append(list, *d.Aweme)
			}
		}
		more := r.HasMore != 0 && r.Cursor > offset
		offset = r.Cursor
		if id == "" {
			id = r.Extra.LogID
		}
		return videos(list), more, nil
	})
}

// Posts pages through a user's videos, newest first.
func (c *Client) Posts(secUID string) Pager[Video] {
	var cursor int64
	return pager(func() ([]Video, bool, error) {
		var r struct {
			awemeList
			HasMore int   `json:"has_more"`
			Cursor  int64 `json:"max_cursor"`
		}
		q := fmt.Sprintf("sec_user_id=%s&max_cursor=%d&count=18&locate_query=false&show_live_replay_strategy=1&need_time_list=1&time_list_query=0&whale_cut_token=&cut_version=1&publish_video_strategy_type=2", url.QueryEscape(secUID), cursor)
		if err := c.getJSON("/aweme/v1/web/aweme/post/", q, &r); err != nil {
			return nil, false, err
		}
		cursor = r.Cursor
		return videos(r.List), r.HasMore != 0, nil
	})
}

// User is an account in a list such as the ones a user follows.
type User struct {
	ID, Name, Signature string // ID is the sec_uid
	Avatar              string // the picture's address
}

// Self returns the logged-in user.
func (c *Client) Self() (User, error) {
	var r struct {
		status
		User struct {
			SecUID    string `json:"sec_uid"`
			Nickname  string
			Signature string
			Avatar    addr `json:"avatar_thumb"`
		}
	}
	if err := c.getJSON("/aweme/v1/web/user/profile/self/", "", &r); err != nil {
		return User{}, err
	}
	return User{r.User.SecUID, r.User.Nickname, r.User.Signature, r.User.Avatar.first()}, nil
}

// Following pages through the accounts secUID follows.
func (c *Client) Following(secUID string) Pager[User] {
	offset := 0
	return pager(func() ([]User, bool, error) {
		var r struct {
			status
			List []struct {
				SecUID    string `json:"sec_uid"`
				Nickname  string
				Signature string
				Avatar    addr `json:"avatar_thumb"`
			} `json:"followings"`
			HasMore bool `json:"has_more"`
		}
		q := fmt.Sprintf("user_id=&sec_user_id=%s&offset=%d&min_time=0&max_time=0&count=20&source_type=4&gps_access=0&address_book_access=0", url.QueryEscape(secUID), offset)
		if err := c.getJSON("/aweme/v1/web/user/following/list/", q, &r); err != nil {
			return nil, false, err
		}
		out := make([]User, len(r.List))
		for i, u := range r.List {
			out[i] = User{u.SecUID, u.Nickname, u.Signature, u.Avatar.first()}
		}
		offset += len(out)
		return out, r.HasMore && len(out) > 0, nil
	})
}

// Comment is a top-level comment on a video.
type Comment struct {
	Author, Text, Place string // Place is the region Douyin shows, as 广东
	Likes, Replies      int
	Time                time.Time
}

// Comments pages through a video's comments, as Douyin orders them.
func (c *Client) Comments(videoID string) Pager[Comment] {
	cursor := 0
	return pager(func() ([]Comment, bool, error) {
		var r struct {
			status
			List []struct {
				Text    string
				User    struct{ Nickname string }
				Likes   int    `json:"digg_count"`
				Replies int    `json:"reply_comment_total"`
				Time    int64  `json:"create_time"`
				Place   string `json:"ip_label"`
			} `json:"comments"`
			Cursor  int `json:"cursor"`
			HasMore int `json:"has_more"`
		}
		q := fmt.Sprintf("aweme_id=%s&cursor=%d&count=20&item_type=0&insert_ids=&whale_cut_token=&cut_version=1&rcFT=", videoID, cursor)
		if err := c.getJSON("/aweme/v1/web/comment/list/", q, &r); err != nil {
			return nil, false, err
		}
		out := make([]Comment, len(r.List))
		for i, x := range r.List {
			out[i] = Comment{x.User.Nickname, x.Text, x.Place, x.Likes, x.Replies, time.Unix(x.Time, 0)}
		}
		more := r.HasMore != 0 && r.Cursor > cursor
		cursor = r.Cursor
		return out, more, nil
	})
}

// Tab is a category of 精选, as its row of tabs names them.
type Tab struct {
	ID   int
	Name string
}

// Tabs are 精选's categories, in the page's order: the page has them in its
// code, with no endpoint to ask.
// shortcut: copied from www.douyin.com/jingxuan on 2026-11-15; read them from the page if they change often.
var Tabs = []Tab{{0, "全部"}, {100000, "公开课"}, {300205, "游戏"}, {300206, "二次元"}, {300209, "音乐"},
	{300215, "影视"}, {300204, "美食"}, {300213, "知识"}, {300214, "小剧场"}, {300216, "生活vlog"},
	{300207, "体育"}, {300221, "旅行"}, {300217, "亲子"}, {300220, "动物"}, {300219, "三农"},
	{300218, "汽车"}, {300222, "美妆穿搭"}}

// Picks pages through 精选's videos of the tab with the given ID, 0 for all.
func (c *Client) Picks(tab int) Pager[Video] {
	n := 0
	return pager(func() ([]Video, bool, error) {
		n++
		q := fmt.Sprintf("module_id=3003101&count=20&refresh_index=%d&refer_type=10&pull_type=%d&awemePcRecRawData=%%7B%%22is_client%%22%%3Afalse%%7D", n, min(n-1, 1)*2)
		if tab != 0 {
			q += "&tag_id=" + strconv.Itoa(tab)
		}
		var r struct {
			awemeList
			HasMore int `json:"has_more"`
		}
		if err := c.callJSON("POST", "/aweme/v2/web/module/feed/", q, &r); err != nil {
			return nil, false, err
		}
		return videos(r.List), r.HasMore != 0, nil
	})
}

// Danmaku is a bullet comment, shown At into its video.
type Danmaku struct {
	At   time.Duration
	Text string
}

// Danmaku returns the bullet comments of the video with the given ID and
// length that show from from to to; Douyin's page asks 32 seconds at a time.
func (c *Client) Danmaku(videoID string, length, from, to time.Duration) ([]Danmaku, error) {
	var r struct {
		status
		List []struct {
			Offset int64 `json:"offset_time"` // ms
			Text   string
		} `json:"danmaku_list"`
	}
	q := fmt.Sprintf("app_name=aweme&format=json&group_id=%s&item_id=%s&start_time=%d&end_time=%d&duration=%d",
		videoID, videoID, from.Milliseconds(), to.Milliseconds(), length.Milliseconds())
	if err := c.getJSON("/aweme/v1/web/danmaku/get_v2/", q, &r); err != nil {
		return nil, err
	}
	var out []Danmaku
	for _, d := range r.List {
		// The reply strays past the window now and then, which the next window brings again.
		if at := time.Duration(d.Offset) * time.Millisecond; at >= from && at < to {
			out = append(out, Danmaku{at, d.Text})
		}
	}
	return out, nil
}

// Notice is something someone did to the logged-in user: liked, commented,
// replied or followed. Text is what they wrote, or what they liked.
type Notice struct {
	User    User
	Action  string // as Douyin words it: 赞了你的作品, 关注了你…
	Text    string
	VideoID string
	Time    time.Time
}

type noticeUser struct {
	SecUID   string `json:"sec_uid"`
	Nickname string
	Avatar   addr `json:"avatar_thumb"`
}

func (u noticeUser) user() User {
	return User{ID: u.SecUID, Name: u.Nickname, Avatar: u.Avatar.first()}
}

// Notices pages through the logged-in user's notifications, newest first,
// leaving them unread.
func (c *Client) Notices() Pager[Notice] {
	var before int64 // 0: from now
	return pager(func() ([]Notice, bool, error) {
		var r struct {
			status
			List []struct {
				Type    int    `json:"type"`
				Time    int64  `json:"create_time"`
				VideoID string `json:"aweme_id"`
				Digg    struct {
					Type    int          `json:"digg_type"` // 1 a video, 3 a comment
					From    []noticeUser `json:"from_user"`
					Comment struct{ Text string }
					Aweme   struct{ Desc string }
				}
				Comment struct {
					Type    int `json:"comment_type"` // 2 a reply
					Comment struct {
						Text string
						User noticeUser
					}
				}
				Follow struct {
					From noticeUser `json:"from_user"`
				}
			} `json:"notice_list_v2"`
			HasMore int   `json:"has_more"`
			MaxTime int64 `json:"max_time"` // the oldest here: the next page's start
		}
		q := fmt.Sprintf("is_new_notice=1&is_mark_read=0&notice_group=960&count=20&min_time=0&max_time=%d", before)
		if err := c.getJSON("/aweme/v1/web/notice/", q, &r); err != nil {
			return nil, false, err
		}
		var out []Notice
		for _, x := range r.List {
			n := Notice{VideoID: x.VideoID, Time: time.Unix(x.Time, 0)}
			switch x.Type {
			case 41:
				if len(x.Digg.From) > 0 {
					n.User = x.Digg.From[0].user()
				}
				n.Action, n.Text = "赞了你的作品", x.Digg.Aweme.Desc
				if x.Digg.Type == 3 {
					n.Action, n.Text = "赞了你的评论", x.Digg.Comment.Text
				}
			case 31:
				n.User, n.Text = x.Comment.Comment.User.user(), x.Comment.Comment.Text
				n.Action = "评论了你的作品"
				if x.Comment.Type == 2 {
					n.Action = "回复了你的评论"
				}
			case 33:
				n.User, n.Action = x.Follow.From.user(), "关注了你"
			default: // others (mentions, system notices) wait until a client shows them
				continue
			}
			out = append(out, n)
		}
		more := r.HasMore != 0 && r.MaxTime != 0 && (before == 0 || r.MaxTime < before)
		before = r.MaxTime
		return out, more, nil
	})
}

// Unread counts the notifications not yet seen.
func (c *Client) Unread() (int, error) {
	var r struct {
		status
		Groups []struct{ Count int } `json:"notice_count"`
	}
	if err := c.getJSON("/aweme/v1/web/notice/count/", "is_new_notice=1&need_social_count=1", &r); err != nil {
		return 0, err
	}
	n := 0
	for _, g := range r.Groups {
		n += g.Count
	}
	return n, nil
}

// Image fetches a picture Douyin serves (a cover, an avatar), whose CDN wants
// the page's Referer as its videos' does.
func (c *Client) Image(u string) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.douyin.com/")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("dy: image %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
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
