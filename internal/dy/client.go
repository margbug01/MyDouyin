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
	login   atomic.Pointer[login] // the cookies, which a write through the browser renews
	imSeq   atomic.Int32          // the IM requests' sequence
	refresh atomic.Int32          // the feed's page, which two lists of it share
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
	Mix              Mix    // the series the video is an episode of, if any
	Live             *Live  // a live room in the list, which has no PlayURL
	Created          time.Time

	Liked, Saved bool // whether the user logged in has liked it and saved it (收藏)

	Qualities []Quality // the definitions to choose from, finest first; PlayURL is Douyin's 智能 pick
}

// Quality is a definition a video comes in: 1080P, 720P or 540P, H.264
// with its sound, as AVFoundation plays it.
type Quality struct {
	Name string
	URL  string
	Size int64
}

// login is the cookies a request carries, and the UIFID among them, which a
// request sends again as a header.
type login struct{ cookie, uifid string }

func (c *Client) cookies() login {
	if l := c.login.Load(); l != nil {
		return *l
	}
	return login{}
}

// Live is a room broadcasting now: its stream as HLS, which AVFoundation
// plays, over https, which an app's transport security allows.
type Live struct {
	RoomID, WebRID string // WebRID is the room's address on live.douyin.com
	HLS            string
	Viewers        string // as Douyin says it, 41 or 1.2万
}

// room decodes a live room. A feed's entry may be a room that has ended,
// whose status is not 2; the live list's say 0 and are all on air.
func room(raw []byte, onAir bool) (v Video, ok bool) {
	var r struct {
		ID     string `json:"id_str"`
		Status int
		Title  string
		Cover  addr
		Owner  struct {
			Nickname string
			SecUID   string `json:"sec_uid"`
			WebRID   string `json:"web_rid"`
			Avatar   addr   `json:"avatar_thumb"`
		}
		Stats struct {
			Viewers string `json:"user_count_str"`
		}
		Stream struct {
			HLS    string            `json:"hls_pull_url"`
			HLSMap map[string]string `json:"hls_pull_url_map"`
		} `json:"stream_url"`
	}
	if json.Unmarshal(raw, &r) != nil || !onAir && r.Status != 2 { // 2: on air
		return v, false
	}
	hls := r.Stream.HLS
	for _, q := range []string{"SD2", "HD1"} { // HD1 last: the finest a 1080p screen needs
		if u := r.Stream.HLSMap[q]; u != "" {
			hls = u
		}
	}
	if hls == "" {
		return v, false
	}
	return Video{ID: "live:" + r.ID, Desc: r.Title, Author: r.Owner.Nickname, AuthorID: r.Owner.SecUID,
		Cover: r.Cover.first(), Avatar: r.Owner.Avatar.first(),
		Live: &Live{RoomID: r.ID, WebRID: r.Owner.WebRID, HLS: strings.Replace(hls, "http://", "https://", 1), Viewers: r.Stats.Viewers}}, true
}

// Mix is a series of videos an author groups (合集), and the episode a video is of it.
type Mix struct {
	ID, Name         string
	Episode, Updated int // this video's episode, and how many there are so far
}

// New loads the cookies the login step saved; without them the client is anonymous.
func New() (*Client, error) {
	c := &Client{http: &http.Client{Timeout: 60 * time.Second}, signer: sign.New(ua)}
	err := c.ReloadCookies()
	if errors.Is(err, os.ErrNotExist) {
		return c, c.anonymous()
	}
	return c, err
}

// cookieFile is where the login's cookies are kept, as ego lite exports them.
func cookieFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Douyin", "cookies.json"), nil
}

// ReloadCookies reads the login's cookies again, as a write through the
// browser leaves them renewed.
func (c *Client) ReloadCookies() error {
	path, err := cookieFile()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cookies []struct{ Name, Value string }
	if err := json.Unmarshal(raw, &cookies); err != nil {
		return err
	}
	var l login
	parts := make([]string, len(cookies))
	for i, ck := range cookies {
		parts[i] = ck.Name + "=" + ck.Value
		if ck.Name == "UIFID" {
			l.uifid = ck.Value
		}
	}
	l.cookie = strings.Join(parts, "; ")
	c.login.Store(&l)
	return nil
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
			c.login.Store(&login{cookie: "ttwid=" + ck.Value})
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
	l := c.cookies()
	req.Header.Set("Cookie", l.cookie)
	if l.uifid != "" {
		// Douyin's gateway (ArgusSecurityPlugin) refuses a user's posts with 403
		// "Uifid Not Found" without uifid, then "Signature Not Found" without x-tt-argus.
		// shortcut: the gateway does not check x-tt-argus's value yet (as MediaCrawler
		// found); once it does, compute x-secsdk-web-signature instead.
		req.Header.Set("uifid", l.uifid)
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
	Name   string `json:"gear_name"`
	H265   int    `json:"is_h265"`
	Format string // "dash" for a picture without its sound
	Addr   addr   `json:"play_addr"`
}) (u string, best addr, qs []Quality) {
	find := func(a addr) string {
		for _, u := range a.URLs {
			if strings.Contains(u, "aweme/v1/play") {
				return u
			}
		}
		return ""
	}
	bestSize := int64(0)
	classes := []int{1080, 720, 540}
	byClass := map[int]addr{}
	for _, g := range gears {
		short := min(g.Addr.W, g.Addr.H)
		// A dash gear is the picture alone, an adapt_ one too; both come
		// as small as the H.264 mp4 of the definition, which has the sound.
		if g.H265 != 0 || g.Format == "dash" || strings.HasPrefix(g.Name, "adapt") || find(g.Addr) == "" || short < 540 {
			continue
		}
		if bestSize == 0 || g.Addr.Size < bestSize {
			best, bestSize = g.Addr, g.Addr.Size
		}
		for _, cl := range classes { // the smallest of each definition
			if short >= cl {
				if have, ok := byClass[cl]; !ok || g.Addr.Size < have.Size {
					byClass[cl] = g.Addr
				}
				break
			}
		}
	}
	if bestSize == 0 {
		best = top
	}
	for _, cl := range classes {
		if a, ok := byClass[cl]; ok {
			qs = append(qs, Quality{fmt.Sprintf("%dP", cl), find(a), a.Size})
		}
	}
	return find(best), best, qs
}

type aweme struct {
	ID   string `json:"aweme_id"`
	Room *struct {
		Raw string `json:"rawdata"`
	} `json:"cell_room"` // a live room, in place of a video
	Desc    string
	Created int64 `json:"create_time"`
	Digged  int   `json:"user_digged"`
	Saved   int   `json:"collect_stat"`
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
			Name   string `json:"gear_name"`
			H265   int    `json:"is_h265"`
			Format string
			Addr   addr `json:"play_addr"`
		} `json:"bit_rate"`
	}
	Mix *struct {
		ID     string `json:"mix_id"`
		Name   string `json:"mix_name"`
		Statis struct {
			Episode int `json:"current_episode"`
			Updated int `json:"updated_to_episode"`
		} `json:"statis"`
	} `json:"mix_info"`
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
		if a.Room != nil {
			if v, ok := room([]byte(a.Room.Raw), false); ok {
				out = append(out, v)
			}
			continue
		}
		play, pa, qs := playURL(a.Video.PlayAddr, a.Video.Gears)
		if play == "" {
			continue
		}
		var mix Mix
		if a.Mix != nil {
			mix = Mix{a.Mix.ID, a.Mix.Name, a.Mix.Statis.Episode, a.Mix.Statis.Updated}
		}
		out = append(out, Video{Mix: mix, ID: a.ID, Desc: a.Desc, Author: a.Author.Nickname, AuthorID: a.Author.SecUID, PlayURL: play,
			W: pa.W, H: pa.H, Size: pa.Size, Duration: time.Duration(a.Video.Duration) * time.Millisecond,
			Likes: a.Stats.Likes, Comments: a.Stats.Comments, Collects: a.Stats.Collects, Shares: a.Stats.Shares,
			Cover: a.Video.Cover.first(), Avatar: a.Author.Avatar.first(), Created: time.Unix(a.Created, 0),
			Liked: a.Digged == 1, Saved: a.Saved == 1, Qualities: qs})
	}
	return out
}

// getJSON calls Get and decodes the body into v, which must carry status_code.
func (c *Client) getJSON(path, extra string, v interface{ code() int }) error {
	return c.callJSON("GET", path, extra, "", v)
}

// callJSON is getJSON with another method, an empty body.
// callJSON calls an endpoint with a form body, if any, and decodes the reply into v.
func (c *Client) callJSON(method, path, extra, body string, v interface{ code() int }) error {
	b, err := c.call(method, path, extra, body)
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

// SearchUsers returns the first accounts found for keyword, as 综合's row of
// users atop the videos.
func (c *Client) SearchUsers(keyword string) ([]User, error) {
	var r struct {
		status
		List []struct {
			U profile `json:"user_info"`
		} `json:"user_list"`
	}
	q := "search_channel=aweme_user_web&search_source=normal_search&query_correct_type=1&is_filter_search=0&from_group_id=&offset=0&count=10&keyword=" + url.QueryEscape(keyword)
	if err := c.getJSON("/aweme/v1/web/discover/search/", q, &r); err != nil {
		return nil, err
	}
	out := make([]User, len(r.List))
	for i, x := range r.List {
		out[i] = x.U.user()
	}
	return out, nil
}

// Suggest returns what the search box suggests for what is typed in it.
func (c *Client) Suggest(keyword string) ([]string, error) {
	var r struct {
		status
		List []struct{ Content string } `json:"sug_list"`
	}
	if err := c.getJSON("/aweme/v1/web/search/sug/", "source=aweme_video_web&history_list=&keyword="+url.QueryEscape(keyword), &r); err != nil {
		return nil, err
	}
	var out []string
	for _, s := range r.List {
		if s.Content != "" {
			out = append(out, s.Content)
		}
	}
	return out, nil
}

// Profile reads the account with the given sec_uid: its counts, 抖音号 and region.
func (c *Client) Profile(secUID string) (User, error) {
	var r struct {
		status
		User profile
	}
	q := "publish_video_strategy_type=2&source=channel_pc_web&personal_center_strategy=1&sec_user_id=" + url.QueryEscape(secUID)
	if err := c.getJSON("/aweme/v1/web/user/profile/other/", q, &r); err != nil {
		return User{}, err
	}
	if r.User.SecUID == "" {
		return User{}, errors.New("dy: no such account")
	}
	return r.User.user(), nil
}

// profile is an account as the profile and the user search give it.
type profile struct {
	SecUID    string `json:"sec_uid"`
	Nickname  string
	Signature string
	Avatar    addr `json:"avatar_thumb"`
	Following int  `json:"following_count"`
	Followers int  `json:"follower_count"`
	Liked     int  `json:"total_favorited"`
	UID       string
	UniqueID  string `json:"unique_id"`
	ShortID   string `json:"short_id"`
	Place     string `json:"ip_location"`
}

func (p profile) user() User {
	handle := p.UniqueID
	if handle == "" {
		handle = p.ShortID
	}
	return User{ID: p.SecUID, Name: p.Nickname, Signature: p.Signature, Avatar: p.Avatar.first(),
		Following: p.Following, Followers: p.Followers, Liked: p.Liked, UID: p.UID,
		Handle: handle, Place: strings.TrimPrefix(p.Place, "IP属地：")}
}

// Posts pages through a user's videos, newest first.
func (c *Client) Posts(secUID string) Pager[Video] {
	return c.posts(secUID, "&publish_video_strategy_type=2")
}

// MyPosts pages through the logged-in user's own videos, whose secUID it
// takes: with the strategy the web page asks others' with, they come back
// none (8 against 0 on 2026-11-16).
func (c *Client) MyPosts(secUID string) Pager[Video] { return c.posts(secUID, "") }

func (c *Client) posts(secUID, strategy string) Pager[Video] {
	var cursor int64
	return pager(func() ([]Video, bool, error) {
		var r struct {
			awemeList
			HasMore int   `json:"has_more"`
			Cursor  int64 `json:"max_cursor"`
		}
		q := fmt.Sprintf("sec_user_id=%s&max_cursor=%d&count=18&locate_query=false&show_live_replay_strategy=1&need_time_list=1&time_list_query=0&whale_cut_token=&cut_version=1%s", url.QueryEscape(secUID), cursor, strategy)
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

	Following, Followers, Liked int    // the counts of a profile, which Self and Profile fill in
	UID                         string // the numeric ID, which Self and Profile fill in
	Handle, Place               string // 抖音号 and IP属地, which Profile fills in
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
			Following int  `json:"following_count"`
			Followers int  `json:"follower_count"`
			Liked     int  `json:"total_favorited"`
			UID       string
		}
	}
	if err := c.getJSON("/aweme/v1/web/user/profile/self/", "", &r); err != nil {
		return User{}, err
	}
	u := r.User
	return User{ID: u.SecUID, Name: u.Nickname, Signature: u.Signature, Avatar: u.Avatar.first(),
		Following: u.Following, Followers: u.Followers, Liked: u.Liked, UID: u.UID}, nil
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
			out[i] = User{ID: u.SecUID, Name: u.Nickname, Signature: u.Signature, Avatar: u.Avatar.first()}
		}
		offset += len(out)
		return out, r.HasMore && len(out) > 0, nil
	})
}

// Comment is a top-level comment on a video. The replies to one are not read:
// comment/list/reply answers this client empty, wanting the ticket guard's
// headers that the browser's device key signs, and asks Douyin's own page
// for a captcha now and then (x-vc-bdturing-parameters, type verify); so
// they open on the web (checked 2026-11-16, the captcha passed).
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
		if err := c.callJSON("POST", "/aweme/v2/web/module/feed/", q, "", &r); err != nil {
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

// Hot is a term of Douyin's hot search list, and how hot it is; the list's
// first, pinned, has none.
type Hot struct {
	Word  string
	Value int
}

// HotSearch returns the hot search list, hottest first.
func (c *Client) HotSearch() ([]Hot, error) {
	var r struct {
		status
		Data struct {
			Words []struct {
				Word  string
				Value float64 `json:"hot_value"`
			} `json:"word_list"`
		}
	}
	if err := c.getJSON("/aweme/v1/web/hot/search/list/", "detail_list=1&source=6&main_billboard_count=5", &r); err != nil {
		return nil, err
	}
	out := make([]Hot, len(r.Data.Words))
	for i, w := range r.Data.Words {
		out[i] = Hot{w.Word, int(w.Value)}
	}
	return out, nil
}

// Liked pages through the videos the user with the given sec_uid has liked,
// the latest first: the logged-in user's own, or anyone's who shows them.
func (c *Client) Liked(secUID string) Pager[Video] {
	var cursor int64
	return pager(func() ([]Video, bool, error) {
		var r struct {
			awemeList
			Cursor  int64 `json:"max_cursor"`
			HasMore int   `json:"has_more"`
		}
		q := fmt.Sprintf("sec_user_id=%s&max_cursor=%d&min_cursor=0&whale_cut_token=&cut_version=1&count=18&publish_video_strategy_type=2", url.QueryEscape(secUID), cursor)
		if err := c.getJSON("/aweme/v1/web/aweme/favorite/", q, &r); err != nil {
			return nil, false, err
		}
		more := r.HasMore != 0 && r.Cursor != cursor
		cursor = r.Cursor
		return videos(r.List), more, nil
	})
}

// Related pages through the videos Douyin relates to the one with the given ID.
func (c *Client) Related(videoID string) Pager[Video] {
	var seen []string // asked not to come again
	n := 0
	return pager(func() ([]Video, bool, error) {
		n++
		var r struct {
			awemeList
			HasMore int `json:"has_more"`
		}
		q := fmt.Sprintf("aweme_id=%s&count=20&filterGids=%s&awemePcRecRawData=%%7B%%7D&sub_channel_id=0&refresh_index=%d",
			videoID, url.QueryEscape(strings.Join(seen, ",")), n)
		if err := c.getJSON("/aweme/v1/web/aweme/related/", q, &r); err != nil {
			return nil, false, err
		}
		vs := videos(r.List)
		for _, v := range vs {
			seen = append(seen, v.ID)
		}
		return vs, r.HasMore != 0 && len(r.List) > 0 && n < 10, nil
	})
}

// MixVideos pages through a series' videos, from its first episode.
func (c *Client) MixVideos(mixID string) Pager[Video] {
	var cursor int64
	return pager(func() ([]Video, bool, error) {
		var r struct {
			awemeList
			Cursor  int64 `json:"cursor"`
			HasMore int   `json:"has_more"`
		}
		if err := c.getJSON("/aweme/v1/web/mix/aweme/", fmt.Sprintf("mix_id=%s&cursor=%d&count=20", mixID, cursor), &r); err != nil {
			return nil, false, err
		}
		more := r.HasMore != 0 && r.Cursor != cursor
		cursor = r.Cursor
		return videos(r.List), more, nil
	})
}

// Video reads one video as it is now, the user's like and save of it included.
func (c *Client) Video(id string) (Video, error) {
	var r struct {
		status
		Aweme *aweme `json:"aweme_detail"`
	}
	if err := c.getJSON("/aweme/v1/web/aweme/detail/", "aweme_id="+id, &r); err != nil {
		return Video{}, err
	}
	if r.Aweme == nil {
		return Video{}, fmt.Errorf("dy: video %s not found", id)
	}
	vs := videos([]aweme{*r.Aweme})
	if len(vs) == 0 {
		return Video{}, fmt.Errorf("dy: video %s has nothing to play", id)
	}
	return vs[0], nil
}

// FollowFeed pages through the videos of the accounts the user follows, the
// latest first, as 关注 shows them.
func (c *Client) FollowFeed() Pager[Video] {
	return c.socialFeed("GET", "/aweme/v1/web/follow/feed/",
		"level=1&count=20&pull_type=0&aweme_ids=&room_ids=&webcast_sdk_version=170400&webcast_version_code=170400")
}

// FriendFeed pages through the videos of the user's friends, those who
// follow back, as 朋友 shows them.
// shortcut: its entries are read as 关注's are (data[].aweme); the account it was mapped on had no friends' videos to show the shape.
func (c *Client) FriendFeed() Pager[Video] {
	return c.socialFeed("POST", "/aweme/v1/web/familiar/feed/", "level=1&pull_type=0&address_book_access=2&gps_access=2")
}

// socialFeed pages through a feed of entries that each hold a video, by
// the cursor each page gives the next.
func (c *Client) socialFeed(method, path, query string) Pager[Video] {
	var cursor int64
	empty := 0
	return pager(func() ([]Video, bool, error) {
		var r struct {
			status
			Data []struct {
				Aweme *aweme
			}
			Cursor  int64
			HasMore int `json:"has_more"`
		}
		if err := c.callJSON(method, path, fmt.Sprintf("cursor=%d&%s", cursor, query), "", &r); err != nil {
			return nil, false, err
		}
		var list []aweme
		for _, d := range r.Data {
			if d.Aweme != nil {
				list = append(list, *d.Aweme)
			}
		}
		// A page can come empty with more said to come; a few of those in a row is the end.
		if len(list) == 0 {
			empty++
		} else {
			empty = 0
		}
		more := r.HasMore != 0 && r.Cursor != cursor && empty < 3
		cursor = r.Cursor
		return videos(list), more, nil
	})
}

// WatchLater pages through the videos the user put in 稍后再看, the latest
// first.
func (c *Client) WatchLater() Pager[Video] {
	offset := 0
	return pager(func() ([]Video, bool, error) {
		var r struct {
			status
			Items   []aweme
			HasMore bool `json:"has_more"`
		}
		if err := c.getJSON("/aweme/v1/web/watchlater/list/", fmt.Sprintf("offset=%d&list_type=0&operate_type=0", offset), &r); err != nil {
			return nil, false, err
		}
		offset += len(r.Items)
		return videos(r.Items), r.HasMore && len(r.Items) > 0, nil
	})
}

// Collected pages through the videos the user saved (收藏), the latest first.
func (c *Client) Collected() Pager[Video] {
	var cursor int64
	return pager(func() ([]Video, bool, error) {
		var r struct {
			awemeList
			Cursor  int64
			HasMore int `json:"has_more"`
		}
		if err := c.callJSON("POST", "/aweme/v1/web/aweme/listcollection/", "publish_video_strategy_type=2", fmt.Sprintf("count=10&cursor=%d", cursor), &r); err != nil {
			return nil, false, err
		}
		more := r.HasMore != 0 && r.Cursor != cursor
		cursor = r.Cursor
		return videos(r.List), more, nil
	})
}

// History pages through the videos the user watched (观看历史), the latest first.
func (c *Client) History() Pager[Video] {
	var cursor int64
	return pager(func() ([]Video, bool, error) {
		var r struct {
			awemeList
			Cursor  int64 `json:"max_cursor"`
			HasMore int   `json:"has_more"`
		}
		if err := c.getJSON("/aweme/v1/web/history/read/", fmt.Sprintf("max_cursor=%d&count=20", cursor), &r); err != nil {
			return nil, false, err
		}
		more := r.HasMore != 0 && r.Cursor != cursor
		cursor = r.Cursor
		return videos(r.List), more, nil
	})
}

// LiveRooms pages through the rooms on air that the live list recommends.
// Each page is a fresh pick, which may repeat a room shown before; those
// are left out.
func (c *Client) LiveRooms() Pager[Video] {
	seen := map[string]bool{}
	n := 0
	return pager(func() ([]Video, bool, error) {
		n++
		var r struct {
			status
			Data []struct {
				Type   int
				Pseudo bool            `json:"is_pseudo_living"`
				Room   json.RawMessage `json:"data"`
			}
		}
		q := "app_name=douyin_web&live_id=1&request_tag_from=web&need_map=1&liveid=1&is_draw=1&inner_from_drawer=0&custom_count=30" +
			"&action=load_more&action_type=loadmore&enter_source=web_homepage_hot_web_live_card&source_key=web_homepage_hot_web_live_card&maxtime=0"
		if err := c.getJSON("/webcast/feed/", q, &r); err != nil {
			return nil, false, err
		}
		var out []Video
		for _, d := range r.Data {
			if d.Type != 1 || d.Pseudo {
				continue
			}
			if v, ok := room(d.Room, true); ok && !seen[v.ID] {
				seen[v.ID] = true
				out = append(out, v)
			}
		}
		return out, len(out) > 0 && n < 8, nil
	})
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
