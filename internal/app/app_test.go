package app

import (
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
	"douyin/internal/media"
)

// stub is a page that loads nothing: its list is done and its slots count as loading.
func stub(title string, n int) *page {
	p := &page{title: title, done: true}
	for range n {
		p.list = append(p.list, &slot{v: dy.Video{Author: "作者"}, loading: true})
	}
	return p
}

// withRoots gives a the two roots, its first page standing for 推荐, and
// 精选 a grid of n videos.
func withRoots(a *app) *app {
	a.feed = a.pages[0]
	a.picks = stub("精选", 3)
	a.picks.grid = true
	return a
}

// newTester shows a's window at its usual size.
func newTester(a *app) *ui.Tester { return ui.NewTester(a.view, 1100, 760) }

// ready gives s a player, as once its video has loaded.
func ready(t *testing.T, s *slot) {
	vp, err := ui.NewVideoPlayer(still{})
	if err != nil {
		t.Fatal(err)
	}
	s.loading, s.vp = false, vp
}

// While the video shown plays the display stays on, 听抖音 keeps only the
// computer awake, and a pause lets both sleep.
func TestPlayingKeepsAwake(t *testing.T) {
	a := &app{pages: []*page{stub("推荐", 2)}}
	held := map[bool]int{} // holds not released, by whether they keep the display on
	a.keepAwake = func(_ string, display bool) func() {
		held[display]++
		return func() { held[display]-- }
	}
	tt := newTester(a)
	tt.Frame()
	if held[true]+held[false] != 0 {
		t.Fatalf("kept %v awake before the video loaded", held)
	}
	ready(t, a.pages[0].list[0])
	a.fill() // as once the video has loaded
	tt.Frame()
	if held[true] != 1 || held[false] != 0 {
		t.Fatalf("playing keeps %v awake, want the display", held)
	}
	if err := tt.Click("听抖音"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if held[true] != 0 || held[false] != 1 {
		t.Fatalf("听抖音 keeps %v awake, want the computer alone", held)
	}
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if held[true]+held[false] != 0 {
		t.Fatalf("paused, %v still kept awake", held)
	}
}

// Typing in the search box does not pause, and Space types there; a click
// on the video pauses; Escape and the back button close the page on top,
// and nothing closes the feed.
func TestControls(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 3)}})
	a.feed.next = func() ([]dy.Video, error) { return nil, nil } // 推荐 pressed below refreshes it
	tt := newTester(a)
	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Type("a b")
	tt.Frame()
	if a.paused || a.query != "a b" {
		t.Fatalf("after typing: paused %v, query %q", a.paused, a.query)
	}
	tt.ClickAt(600, 400) // the video
	tt.Frame()
	if !a.paused {
		t.Fatal("clicking the video did not pause")
	}
	tt.Key(0, ui.KeySpace) // the focus left the search box: Space plays again
	if a.paused {
		t.Fatal("Space after the search box did not play")
	}
	if err := tt.Click("推荐"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeySpace) // 推荐 has the focus, and would take Space as a click
	if !tt.Focused("推荐") || !a.paused {
		t.Fatalf("Space with a button focused: focused %v, paused %v", tt.Focused("推荐"), a.paused)
	}

	a.pages = append(a.pages, stub("搜索：猫", 2), stub("@作者", 2))
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(a.pages) != 2 || !tt.HasText("搜索：猫") {
		t.Fatalf("after Escape: %d pages", len(a.pages))
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 1 || a.paused || !tt.HasText("推荐") {
		t.Fatalf("after back: %d pages, paused %v", len(a.pages), a.paused)
	}
	tt.Key(0, ui.KeyEscape)
	if len(a.pages) != 1 {
		t.Fatal("Escape closed the feed")
	}
}

// Enter in the search box opens the search. Live: the search runs in the background.
func TestLiveSearchEnter(t *testing.T) {
	c := live(t)
	a := withRoots(&app{c: c, pages: []*page{stub("推荐", 1)}})
	tt := newTester(a)
	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Type("猫")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(a.pages) != 2 || a.cur().title != "搜索：猫" || !tt.HasText("搜索：猫") {
		t.Fatalf("after Enter: %d pages", len(a.pages))
	}
}

// Clicking the author opens their videos without pausing, Space still pauses
// after it though the button has the focus, and the author on their own page
// opens nothing. Live: their videos load in the background.
func TestLiveAuthor(t *testing.T) {
	c := live(t)
	feed := stub("推荐", 1)
	feed.list[0].v.AuthorID = "x"
	a := withRoots(&app{c: c, pages: []*page{feed}})
	tt := newTester(a)
	if err := tt.Click("@作者"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 2 || a.cur().title != "@作者" || a.paused {
		t.Fatalf("after click: %d pages, paused %v", len(a.pages), a.paused)
	}
	tt.Key(0, ui.KeySpace)
	if len(a.pages) != 2 || !a.paused {
		t.Fatalf("after Space: %d pages, paused %v", len(a.pages), a.paused)
	}

	own := stub("@作者", 1)
	own.authorID = "x"
	own.list[0].v.AuthorID = "x"
	a.pages = append(a.pages, own)
	tt.Frame()
	tt.Click("@作者")
	tt.Frame()
	if len(a.pages) != 3 {
		t.Fatalf("author on their own page opened another: %d pages", len(a.pages))
	}
}

// The dock is Douyin's: 关注 shows the accounts followed along its top,
// a name of which opens its videos; 精选 goes back to the root with every
// page on it let go. Live: the author's videos load in the background.
func TestLiveSidebar(t *testing.T) {
	c := live(t)
	a := withRoots(&app{c: c, pages: []*page{stub("推荐", 1)}, follows: []dy.User{{ID: "x", Name: "某人"}}, me: dy.User{Name: "我"}})
	a.follow = stub("关注", 2)
	a.follow.grid = true
	a.live, a.drama, a.friends = stub("直播", 1), stub("短剧", 1), stub("朋友", 1)
	tt := newTester(a)
	for _, want := range []string{"精选", "推荐", "关注", "朋友", "直播", "LIVE", "短剧"} {
		if !tt.HasText(want) {
			t.Fatalf("dock lacks %q: %q", want, tt.Texts())
		}
	}
	if tt.HasText("某人") || tt.HasText("我的") {
		t.Fatalf("follows or 我的 before 关注 or the account: %q", tt.Texts())
	}
	if err := tt.Click("关注"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.cur() != a.follow || !tt.HasText("某人") {
		t.Fatalf("关注 shows %q", tt.Texts())
	}
	if err := tt.Click("某人"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 2 || a.cur().title != "@某人" || a.paused {
		t.Fatalf("after clicking a name: %d pages, paused %v", len(a.pages), a.paused)
	}
	a.pages = append(a.pages, stub("搜索：猫", 1))
	tt.Frame()
	if err := tt.Click("精选"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 1 || a.cur() != a.picks {
		t.Fatalf("精选 left %d pages", len(a.pages))
	}
}

// 精选 shows covers, which play nothing and do not step; a cover plays its
// video, which then steps and pauses; Escape and the back button bring the
// covers back, its videos let go.
func TestGrid(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	a.root(a.picks)
	for i, s := range a.picks.list {
		s.v.ID, s.v.Desc = fmt.Sprint(i), fmt.Sprint("视频", i)
		s.loading = false
	}
	tt := newTester(a)
	if !tt.HasText("视频1") || tt.HasText("00:00 / 00:00") {
		t.Fatalf("covers show %q", tt.Texts())
	}
	tt.Key(0, ui.KeyDown)
	if a.picks.idx != 0 {
		t.Fatal("the covers stepped")
	}
	if err := tt.Click("视频1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.picks.playing || a.picks.idx != 1 || a.paused {
		t.Fatalf("after a click: playing %v idx %d paused %v", a.picks.playing, a.picks.idx, a.paused)
	}
	ready(t, a.picks.list[1])
	tt.Frame()
	if !tt.HasText("@作者") || !tt.HasText("00:00 / 00:00") {
		t.Fatalf("playing shows %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.picks.playing || len(a.pages) != 1 || a.picks.list[1].vp == nil || a.picks.list[1].vp.Playing() {
		t.Fatalf("after Escape: playing %v, %d pages, player kept %v", a.picks.playing, len(a.pages), a.picks.list[1].vp != nil)
	}
	tt.Click("视频2")
	tt.Frame()
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.picks.playing {
		t.Fatal("the back button left it playing")
	}
}

func TestComments(t *testing.T) {
	feed := stub("推荐", 3)
	ready(t, feed.list[0])
	a := withRoots(&app{pages: []*page{feed, stub("@作者", 3)}})
	a.pages[1].list[0] = feed.list[0]
	a.pages[1].list[1].v = dy.Video{ID: "v2", Author: "作者", Comments: 5}
	a.pages[1].list[1].comments = &sheet{v: a.pages[1].list[1].v, total: 5, done: true, items: []dy.Comment{{Author: "丙", Text: "下个视频的评论"}}}
	tt := newTester(a)
	open := func() {
		a.sheet = &sheet{total: 2, done: true, items: []dy.Comment{{Author: "甲", Text: "第一条", Replies: 3}, {Author: "乙", Text: "第二条"}}}
		tt.Frame()
		if !tt.HasText("第一条") || !tt.HasText("评论 2") || !tt.HasText("展开 3 条回复") || tt.HasText("展开 0 条回复") {
			t.Fatalf("panel shows %q", tt.Texts())
		}
	}
	check := func(how string) {
		tt.Frame()
		if a.sheet != nil || a.paused || len(a.pages) != 2 {
			t.Fatalf("after %s: open %v, paused %v, %d pages", how, a.sheet != nil, a.paused, len(a.pages))
		}
	}
	open()
	if err := tt.Click("关闭评论"); err != nil {
		t.Fatal(err)
	}
	check("关闭评论")
	open()
	if err := tt.Click("评论"); err != nil {
		t.Fatal(err)
	}
	check("the rail")
	open()
	tt.Key(0, ui.KeyEscape)
	check("Escape")
	open()
	if err := tt.Click("第二条"); err != nil {
		t.Fatal(err)
	}
	if a.sheet == nil || a.paused {
		t.Fatal("a click in the comments closed them or paused")
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.pages[1].idx != 1 || a.sheet != a.pages[1].list[1].comments || !tt.HasText("下个视频的评论") || tt.HasText("第一条") || a.paused || len(a.pages) != 2 {
		t.Fatalf("after the next video: idx=%d sheet=%v paused=%v pages=%d texts=%q", a.pages[1].idx, a.sheet != nil, a.paused, len(a.pages), tt.Texts())
	}
}

func TestStepFollowsCommentsDrawer(t *testing.T) {
	p := &page{
		title: "推荐",
		done:  true,
		list: []*slot{
			{v: dy.Video{ID: "v0", Author: "甲", Comments: 1}, loading: true},
			{v: dy.Video{ID: "v1", Author: "乙", Comments: 2}, loading: true},
			{v: dy.Video{ID: "v2", Author: "丙", Comments: 3}, loading: true},
			{v: dy.Video{ID: "live1", Author: "丁", Live: &dy.Live{RoomID: "r1"}}, loading: true},
		},
	}
	p.list[0].comments = &sheet{v: p.list[0].v, total: 1, done: true, items: []dy.Comment{{ID: "c0", Text: "评论0"}}}
	prefetched := &sheet{v: p.list[1].v, total: 2, done: true, items: []dy.Comment{{ID: "c1", Text: "预拉评论1"}}}
	p.list[1].comments = prefetched

	a := withRoots(&app{pages: []*page{p}, sheet: p.list[0].comments})

	a.step(1)
	if p.idx != 1 || a.sheet != prefetched || p.list[0].comments != nil {
		t.Fatalf("step to v1: idx=%d reused=%v prevReleased=%v", p.idx, a.sheet == prefetched, p.list[0].comments == nil)
	}

	a.sheet.related = true
	a.lastNav = time.Time{}
	a.step(1)
	if p.idx != 2 || a.sheet == nil || a.sheet.v.ID != "v2" || !a.sheet.related || p.list[1].comments != nil {
		t.Fatalf("step to v2 with related tab: idx=%d sheet=%+v prevReleased=%v", p.idx, a.sheet, p.list[1].comments == nil)
	}

	a.lastNav = time.Time{}
	a.step(-1)
	if p.idx != 1 || a.sheet == nil || a.sheet.v.ID != "v1" || !a.sheet.related || p.list[2].comments != nil {
		t.Fatalf("step back to v1: idx=%d sheet=%+v prevReleased=%v", p.idx, a.sheet, p.list[2].comments == nil)
	}

	p.idx = 2
	a.lastNav = time.Time{}
	a.step(1)
	if p.idx != 3 || a.sheet != nil {
		t.Fatalf("step onto live room should close comments: idx=%d sheet=%v", p.idx, a.sheet != nil)
	}
}

// A video saves under a clean name with its bytes intact and no .part left;
// a missing source fails without leaving anything.
func TestSave(t *testing.T) {
	if got := fileName(dy.Video{ID: "7", Author: "a/b:c*"}); got != "抖音-a_b_c_-7.mp4" {
		t.Fatalf("name %q", got)
	}
	src, dir := t.TempDir(), t.TempDir()
	os.WriteFile(src+"/v.mp4", []byte("video bytes"), 0o644)
	dst, err := copyTo(src+"/v.mp4", dir, "x.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "video bytes" {
		t.Fatalf("copied %q", b)
	}
	if _, err := copyTo(src+"/none.mp4", dir, "y.mp4"); err == nil {
		t.Fatal("a missing source saved")
	}
	if ls, _ := os.ReadDir(dir); len(ls) != 1 {
		t.Fatalf("left %d files", len(ls))
	}
}

// 下载, in the rail's 更多, is off until the video is ready, does not pause,
// and shows its progress.
func TestSaveButton(t *testing.T) {
	p := stub("推荐", 1)
	s := p.list[0]
	a := withRoots(&app{pages: []*page{p}, dir: t.TempDir()})
	tt := newTester(a)
	if err := tt.Click("更多"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Click("下载到「下载」文件夹")
	if s.saved != "" {
		t.Fatal("saving before the video is ready")
	}
	ready(t, s)
	tt.Frame()
	if err := tt.Click("下载到「下载」文件夹"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.paused || s.saved != "保存中…" || a.moreOpen {
		t.Fatalf("after click: paused %v, saved %q, menu open %v", a.paused, s.saved, a.moreOpen)
	}
}

// The rail has Douyin's column: the counts, sharing, whose panel copies the
// video's link, 听抖音, which puts the cover in the picture's place and plays
// on, and 更多's menu, whose 复制链接 copies it too.
func TestRail(t *testing.T) {
	var copied []string
	copyText = func(s string) error { copied = append(copied, s); return nil }
	p := stub("推荐", 1)
	p.list[0].v = dy.Video{ID: "42", Author: "作者", Likes: 12000, Comments: 3, Collects: 4, Shares: 5}
	ready(t, p.list[0])
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)
	for _, want := range []string{"1.2万", "3", "4", "5", "听抖音"} {
		if !tt.HasText(want) {
			t.Fatalf("rail lacks %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("分享"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("复制链接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.shareOpen {
		t.Fatal("the share panel stayed open after copying")
	}
	if err := tt.Click("听抖音"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.listen || a.paused || !tt.HasText("听抖音中") {
		t.Fatalf("听抖音: listen %v paused %v", a.listen, a.paused)
	}
	tt.Click("更多")
	tt.Frame()
	if err := tt.Click("复制链接"); err != nil {
		t.Fatal(err)
	}
	want := "https://www.douyin.com/video/42"
	if len(copied) != 2 || copied[0] != want || copied[1] != want || a.paused {
		t.Fatalf("copied %q, paused %v", copied, a.paused)
	}
}

// live is the logged-in client, for a test that reaches Douyin: skipped unless LIVE is set.
func live(t *testing.T) *dy.Client {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := dy.New()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// still is a video of one black frame.
type still struct{}

func (still) Next() (ui.VideoFrame, error) {
	return ui.VideoFrame{Image: image.NewRGBA(image.Rect(0, 0, 4, 4))}, nil
}
func (still) Rewind() error { return nil }

// The control bar's switches turn bullet comments, 连播 and 清屏 on and
// off, 清屏 taking the text and the rail away; its menus choose the speed
// and the definition, which opens the video again in it from where it was;
// 网页全屏 leaves the video alone in the window till Escape; none of them
// pauses. With 连播 on, a video going back to its start asks for the next,
// and without it, it does not.
func TestControlBar(t *testing.T) {
	p := stub("推荐", 2)
	ready(t, p.list[0])
	p.list[0].v.Qualities = []dy.Quality{{Name: "1080P"}, {Name: "720P"}}
	a := withRoots(&app{pages: []*page{p}})
	a.setSpeed(1)
	tt := newTester(a)
	for _, name := range []string{"弹幕", "连播", "清屏"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(name, err)
		}
		tt.Frame()
	}
	if !a.dmOff || !a.autoNext || !a.clear || a.paused || tt.HasText("听抖音") || tt.HasText("@作者") {
		t.Fatalf("after the switches: dmOff %v autoNext %v clear %v paused %v: %q", a.dmOff, a.autoNext, a.clear, a.paused, tt.Texts())
	}
	tt.Click("清屏")
	tt.Frame()
	if a.clear || !tt.HasText("听抖音") {
		t.Fatal("清屏 again did not bring the rail back")
	}
	if err := tt.Click("倍速：倍速"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("1.5x"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.speed != 1.5 || a.speedOpen || !tt.HasText("1.5x") {
		t.Fatalf("speed %v, menu open %v", a.speed, a.speedOpen)
	}
	if err := tt.Click("清晰度：智能"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("1080P") || !tt.HasText("720P") {
		t.Fatalf("definitions: %q", tt.Texts())
	}
	if err := tt.Click("720P"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.quality != "720P" || p.list[0].vp != nil {
		t.Fatalf("after 720P: quality %q, the old player kept %v", a.quality, p.list[0].vp != nil)
	}
	ready(t, p.list[0])
	if err := tt.Click("网页全屏"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.theater || tt.HasText("精选") || a.paused {
		t.Fatalf("网页全屏: theater %v, %q", a.theater, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.theater || !tt.HasText("精选") || len(a.pages) != 1 {
		t.Fatal("Escape did not leave 网页全屏, or left the page")
	}
	a.setSpeed(1)
	s := p.list[0]
	if a.watch(s, 10*time.Second) || a.watch(s, 30*time.Second) {
		t.Fatal("next while playing on")
	}
	if !a.watch(s, 200*time.Millisecond) {
		t.Fatal("no next after looping with 连播 on")
	}
	a.autoNext = false
	a.watch(s, 30*time.Second)
	if a.watch(s, 0) {
		t.Fatal("next after looping with 连播 off")
	}
}

// A burst like a real video's (22 a second) must never overlap within a lane
// at any moment of its flight; the excess is dropped, the user's own is not.
func TestDanmakuLanesNeverOverlap(t *testing.T) {
	const w = 800
	f := &dmField{}
	type flying struct {
		d    dy.Danmaku
		tw   float32
		lane int
	}
	var all []flying
	for i := range 200 {
		d := dy.Danmaku{At: time.Duration(i) * 45 * time.Millisecond, Text: fmt.Sprint(i)}
		tw := float32(40 + i*37%300) // mixed widths: wide ones fly faster
		if i == 150 {
			f.own = d
		}
		all = append(all, flying{d, tw, f.place(d, tw, w)})
	}
	placed := 0
	for _, f := range all {
		if f.lane >= 0 {
			placed++
		}
	}
	if placed < 20 || placed == len(all) || all[150].lane < 0 {
		t.Fatalf("placed %d of %d, own lane %d", placed, len(all), all[150].lane)
	}
	for at := time.Duration(0); at < 18*time.Second; at += 20 * time.Millisecond {
		for i, a := range all {
			for _, b := range all[i+1:] {
				if a.lane < 0 || a.lane != b.lane || a.d == f.own || b.d == f.own {
					continue
				}
				ageA, ageB := at-a.d.At, at-b.d.At
				if ageA < 0 || ageA >= flight || ageB < 0 || ageB >= flight {
					continue
				}
				xa := w - float32(ageA)/float32(flight)*(w+a.tw)
				xb := w - float32(ageB)/float32(flight)*(w+b.tw)
				if xb < xa+a.tw { // b set off later: its head must stay behind a's tail
					t.Fatalf("lane %d at %v: %q [%v,%v] overlaps %q from %v", a.lane, at, a.d.Text, xa, xa+a.tw, b.d.Text, xb)
				}
			}
		}
	}
}

func sameDanmaku(d dy.Danmaku) dy.Danmaku { return d }

// Dense traffic must keep entering at the right, even with over 40 older
// comments still crossing the video. Lane spacing already bounds the crowd.
func TestDanmakuCrowdFillsWidth(t *testing.T) {
	for _, width := range []float32{800, 1280} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s := &slot{}
			for i := range 300 {
				s.danmaku = append(s.danmaku, dy.Danmaku{
					At:   time.Duration(i) * 45 * time.Millisecond,
					Text: fmt.Sprintf("弹幕%d", i),
				})
			}
			a := &app{}
			for at := 250 * time.Millisecond; at <= 12*time.Second; at += 250 * time.Millisecond {
				flying, _ := flyDanmaku(a, &s.dm, s.danmaku, sameDanmaku, at, width)
				if at < flight {
					continue
				}
				if !slices.ContainsFunc(flying, func(f dmFlying) bool { return f.x >= width*0.9 && f.x < width }) {
					t.Fatalf("at %v: no comments entering the rightmost 10%% of width %v", at, width)
				}
			}
		})
	}
}

// A bullet comment flies from its time for flight, and not before or after;
// those flown are let go, and the one still flying keeps its lane.
func TestDanmakuFlight(t *testing.T) {
	s := &slot{danmaku: []dy.Danmaku{{At: time.Second, Text: "早了"}, {At: 3 * time.Second, Text: "正在飞"}, {At: 9 * time.Second, Text: "还没到"}}}
	a := &app{}
	fly := func(at time.Duration) (map[string]dmFlying, time.Duration) {
		flying, next := flyDanmaku(a, &s.dm, s.danmaku, sameDanmaku, at, 800)
		m := map[string]dmFlying{}
		for _, f := range flying {
			m[f.d.Text] = f
		}
		return m, next
	}
	at5, next := fly(5 * time.Second)
	if _, ok := at5["早了"]; !ok || len(at5) != 2 || next != 4*time.Second {
		t.Fatalf("at 5s: %v, next in %v", at5, next)
	}
	at10, next := fly(10 * time.Second)
	if _, ok := at10["早了"]; ok || len(at10) != 2 || next != -1 {
		t.Fatalf("at 10s: %v, next in %v", at10, next)
	}
	if at10["正在飞"].x >= at5["正在飞"].x {
		t.Fatalf("did not fly left: %v -> %v", at5["正在飞"].x, at10["正在飞"].x)
	}
	a.loadDanmaku(s, 10*time.Second)
	after, _ := fly(10 * time.Second)
	if len(s.danmaku) != 2 || after["正在飞"].y != at10["正在飞"].y {
		t.Fatalf("after letting go of the first: %d left, y %v -> %v", len(s.danmaku), at10["正在飞"].y, after["正在飞"].y)
	}
}

// Flying comments repaint the layer on their own, without building the view
// again, and stop asking for frames once paused or turned off; then the
// layer paints nothing.
func TestDanmakuLayerPaintsWithoutBuildingTheView(t *testing.T) {
	s := &slot{danmaku: []dy.Danmaku{{At: 0, Text: "前方高能弹幕"}}}
	a := &app{}
	at, moving, builds := 2*time.Second, true, 0
	tt := ui.NewTester(func(c *ui.Context) {
		builds++
		ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).Children(func() {
			a.danmakuLayer(c, "dm", moving, func(w, h float32, _ time.Duration) ([]dmFlying, time.Duration) {
				return flyDanmakuArea(a, &s.dm, s.danmaku, sameDanmaku, at, w, h)
			})
		})
	}, 800, 200)
	ink := func() (n int) {
		img := tt.Image()
		for i := 0; i < len(img.Pix); i += 4 {
			if img.Pix[i] > 200 && img.Pix[i+1] > 200 && img.Pix[i+2] > 200 {
				n++
			}
		}
		return n
	}
	before, first := builds, tt.Image()
	if ink() == 0 || !tt.FramePending() {
		t.Fatalf("flying: %d white pixels, frame pending %v", ink(), tt.FramePending())
	}
	frame0 := slices.Clone(first.Pix)
	at += 500 * time.Millisecond
	tt.Advance(time.Second / 60)
	if builds != before || slices.Equal(frame0, tt.Image().Pix) {
		t.Fatalf("moving took %d builds of the view, picture changed %v", builds-before, !slices.Equal(frame0, tt.Image().Pix))
	}
	moving = false
	tt.Frame()
	if ink() == 0 || tt.FramePending() {
		t.Fatalf("paused: %d white pixels, frame pending %v", ink(), tt.FramePending())
	}
	a.dmOff = true
	tt.Frame()
	if ink() != 0 {
		t.Fatalf("turned off: %d white pixels", ink())
	}
}

// The notifications show beside the page, who and what, and close with
// their button, the bell and Escape, without closing the page.
func TestNotes(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}, unread: 3})
	tt := newTester(a)
	if !tt.HasText("3") {
		t.Fatal("no unread badge")
	}
	open := func() {
		a.notes = &notes{done: true, items: []dy.Notice{{User: dy.User{Name: "某人"}, Action: "赞了你的评论", Text: "说得好"}}}
		tt.Frame()
		if !tt.HasText("某人") || !tt.HasText("赞了你的评论") || !tt.HasText("说得好") {
			t.Fatalf("panel shows %q", tt.Texts())
		}
	}
	for _, how := range []string{"关闭通知", "收起通知", "Escape"} {
		open()
		if how == "Escape" {
			tt.Key(0, ui.KeyEscape)
		} else if err := tt.Click(how); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if a.notes != nil || len(a.pages) != 1 {
			t.Fatalf("after %s: open %v, %d pages", how, a.notes != nil, len(a.pages))
		}
	}
}

// 精选 shows its tabs; another one opens its own covers, and going back to a
// tab finds its page as it was. Live: the pages load in the background.
func TestLiveTabs(t *testing.T) {
	c := live(t)
	a := withRoots(&app{c: c, pages: []*page{stub("推荐", 1)}, tabPages: map[int]*page{}})
	a.tabPages[0] = a.picks
	a.root(a.picks)
	tt := newTester(a)
	if err := tt.Click("游戏"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	game := a.picks
	if a.tab != 300205 || game == a.tabPages[0] || a.cur() != game {
		t.Fatalf("after 游戏: tab %d", a.tab)
	}
	tt.Click("全部")
	tt.Click("游戏")
	if a.picks != game {
		t.Fatal("游戏 opened a new page the second time")
	}
	a.root(a.feed)
	tt.Frame()
	if tt.HasText("游戏") {
		t.Fatal("tabs over 推荐")
	}
}

// 精选's first video is a big card, which plays it; a search's first is a
// card like the others.
func TestBigCard(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	for i, s := range a.picks.list {
		s.v.Desc = fmt.Sprint("视频", i)
	}
	a.root(a.picks)
	tt := newTester(a)
	big, _ := tt.Find("视频0")
	small, _ := tt.Find("视频1")
	if big.W < 1.8*small.W || big.H < 1.5*small.H {
		t.Fatalf("first card %v beside %v", big, small)
	}
	if err := tt.Click("视频0"); err != nil {
		t.Fatal(err)
	}
	if !a.picks.playing || a.picks.idx != 0 {
		t.Fatal("the big card did not play its video")
	}
	search := stub("搜索：猫", 3)
	search.grid = true
	for i, s := range search.list {
		s.v.Desc = fmt.Sprint("结果", i)
	}
	a.open(search)
	tt.Frame()
	b, _ := tt.Find("结果0")
	if s, _ := tt.Find("结果1"); b.W > 1.2*s.W || b.W == 0 {
		t.Fatalf("a search's first card is big: %v beside %v", b, s)
	}
}

// On a wide window 精选's big card stays near 16:9 and lines up with the
// columns under it, rather than stretching across all of them but one.
func TestBigCardOnWideWindow(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	a.picks = stub("精选", 20)
	a.picks.grid = true
	a.root(a.picks)
	for i, s := range a.picks.list {
		s.v.Desc, s.loading = fmt.Sprint("视频", i), false
	}
	tt := ui.NewTester(a.view, 1885, 1104)
	tt.Frame()
	big, ok := tt.Find("视频0")
	under, ok2 := tt.Find("视频9") // 6 columns: the big card spans 3, 1 to 6 beside it, 7 to 9 under it
	if !ok || !ok2 {
		t.Fatalf("cards: %q", tt.Texts())
	}
	if r := big.W / big.H; r <= 4.0/3 || r > 16.0/9 {
		t.Fatalf("big card %.0f×%.0f", big.W, big.H)
	}
	if d := big.X + big.W - under.X - under.W; d > 0.5 || d < -0.5 {
		t.Fatalf("big card ends at %.1f, the column under it at %.1f", big.X+big.W, under.X+under.W)
	}
}

// At 2x a silent 2-second clip's frames last a second in all, while the
// clock it gives the view, having no sound, counts its own 2 seconds; a
// rewind, as a loop does, sets that clock back to its start.
func TestPacedSilent(t *testing.T) {
	f, err := media.Open("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Audio(); err == nil {
		t.Fatal("the silent clip has sound")
	}
	vd, err := f.Video()
	if err != nil {
		t.Fatal(err)
	}
	var speed atomic.Uint64
	speed.Store(math.Float64bits(2))
	dec := &paced{VideoDecoder: vd, speed: &speed}
	defer dec.Close()
	s := &slot{dec: dec}
	var shown time.Duration
	for {
		fr, err := dec.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		shown += fr.Duration
	}
	near := func(d, want time.Duration) bool { return d > want-50*time.Millisecond && d < want+50*time.Millisecond }
	if !near(shown, time.Second) || !near(s.at(), 2*time.Second) {
		t.Fatalf("shown for %v, clock at %v", shown, s.at())
	}
	if err := dec.Rewind(); err != nil || s.at() != 0 {
		t.Fatalf("after a rewind the clock is at %v (%v)", s.at(), err)
	}
}

// The inbox lists chats beside the page; a chat shows its messages, tips
// included, and Escape or its back button go back to the list, Escape again
// closes it; the comments and the notifications give way to it.
func TestDMs(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	a.notes = &notes{done: true}
	a.dms = &dms{open: -1, chats: []dy.Chat{
		{ID: "a", Peer: dy.User{Name: "甲"}, Msgs: []dy.Message{{Tip: true, Text: "提示"}, {Text: "你好"}, {Mine: true, Text: "在的"}}},
		{ID: "b", Peer: dy.User{Name: "乙"}, Msgs: []dy.Message{{Text: "早"}}},
	}}
	a.notes = nil
	tt := newTester(a)
	if !tt.HasText("甲") || !tt.HasText("在的") || !tt.HasText("乙") || tt.HasText("提示") {
		t.Fatalf("list shows %q", tt.Texts())
	}
	if err := tt.Click("甲"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.dms.open != 0 || !tt.HasText("提示") || !tt.HasText("你好") || tt.HasText("乙") {
		t.Fatalf("chat shows %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.dms == nil || a.dms.open != -1 {
		t.Fatal("Escape in a chat did not go back to the list")
	}
	tt.Click("甲")
	tt.Frame()
	if err := tt.Click("返回私信列表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.dms.open != -1 {
		t.Fatal("the back button left the chat open")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.dms != nil || len(a.pages) != 1 {
		t.Fatal("Escape on the list did not close it, or closed the page")
	}
}

// The comments' 相关推荐 tab lists the related videos; one clicked plays in
// a page of them, at its place, the list loaded with it.
func TestRelatedTab(t *testing.T) {
	p := stub("推荐", 1)
	ready(t, p.list[0])
	a := withRoots(&app{pages: []*page{p}})
	a.sheet = &sheet{done: true, relDone: true, rel: []dy.Video{{ID: "1", Desc: "相关一", Author: "甲"}, {ID: "2", Desc: "相关二", Author: "乙"}}}
	tt := newTester(a)
	if tt.HasText("相关二") {
		t.Fatal("related shown before its tab")
	}
	if err := tt.Click("相关推荐"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("相关一") || !tt.HasText("相关二") {
		t.Fatalf("tab shows %q", tt.Texts())
	}
	if err := tt.Click("相关二"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	q := a.cur()
	if len(a.pages) != 2 || q.title != "相关推荐" || q.idx != 1 || len(q.list) != 2 || !q.done || a.sheet != nil {
		t.Fatalf("after a click: %d pages, %q idx %d", len(a.pages), q.title, q.idx)
	}
}

// A video of a series shows it under its text, with its episode; a click
// opens the series' grid. Live: the series loads in the background.
func TestLiveMixChip(t *testing.T) {
	c := live(t)
	p := stub("推荐", 1)
	p.list[0].v.Mix = dy.Mix{ID: "1", Name: "某合集", Episode: 2, Updated: 9}
	a := withRoots(&app{c: c, pages: []*page{p}})
	tt := newTester(a)
	if !tt.HasText("合集 · 某合集 · 第2集，更新至第9集") {
		t.Fatalf("no series: %q", tt.Texts())
	}
	if err := tt.Click("合集：某合集"); err != nil {
		t.Fatal(err)
	}
	if len(a.pages) != 2 || a.cur().title != "合集：某合集" || !a.cur().grid {
		t.Fatalf("after a click: %d pages %q", len(a.pages), a.cur().title)
	}
}

// The empty search field drops the hot search list, which a press searches
// for; typed into, the field hides it. Live: the search runs in the background.
func TestLiveHotList(t *testing.T) {
	c := live(t)
	a := withRoots(&app{c: c, pages: []*page{stub("推荐", 1)}, hot: []dy.Hot{{Word: "置顶词"}, {Word: "第一热", Value: 120000}}})
	tt := newTester(a)
	if tt.HasText("第一热") {
		t.Fatal("hot list before the field has the focus")
	}
	tt.Click("搜索")
	tt.Frame()
	if !tt.HasText("第一热") || !tt.HasText("12.0万") {
		t.Fatalf("focused: %q", tt.Texts())
	}
	tt.Type("x")
	tt.Frame()
	if tt.HasText("第一热") {
		t.Fatal("hot list over what is typed")
	}
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if err := tt.Click("第一热"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.cur().title != "搜索：第一热" {
		t.Fatalf("after a press: %q", a.cur().title)
	}
}

// 我的 shows in the dock once the account is known, with the user's
// counts and four tabs, each its own grid, which 我的 comes back to.
func TestMine(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}, me: dy.User{Name: "我自己", Following: 13, Followers: 2, Liked: 45}})
	tt := newTester(a)
	if tt.HasText("我的") {
		t.Fatal("我的 before the account is known")
	}
	a.mine = map[string]*page{}
	for _, n := range mineTabs {
		a.mine[n] = stub(n, 1)
		a.mine[n].grid = true
	}
	a.mineTab = "作品"
	tt.Frame()
	if err := tt.Click("我的"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.cur() != a.mine["作品"] || !tt.HasText("我自己") || !tt.HasText("45") || !tt.HasText("观看历史") {
		t.Fatalf("我的 shows %q", tt.Texts())
	}
	if err := tt.Click("收藏"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.cur() != a.mine["收藏"] || len(a.pages) != 1 {
		t.Fatal("收藏 did not show its grid")
	}
	tt.Click("精选")
	tt.Frame()
	tt.Click("我的")
	tt.Frame()
	if a.cur() != a.mine["收藏"] {
		t.Fatal("我的 did not come back to the tab left")
	}
}

// Each root comes back as it was left: a video played from 引力场, a page
// opened over 精选. Pressed with pages over it, a root shows itself.
func TestRootsKeepWhatWasOpen(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}, me: dy.User{ID: "me", Name: "我自己"}})
	for i, s := range a.picks.list {
		s.v.ID, s.v.Desc = fmt.Sprint("p", i), fmt.Sprint("精选视频", i)
	}
	a.home = &page{title: "引力场", grid: true}
	a.orbit.source = orbitPicks
	a.pages = []*page{a.home}
	a.mine = map[string]*page{}
	for _, n := range mineTabs {
		a.mine[n] = stub(n, 1)
		a.mine[n].grid = true
	}
	a.mineTab = "作品"
	tt := newTester(a)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	playing := func() string {
		if p := a.cur(); p.plays() && p.idx < len(p.list) {
			return p.list[p.idx].v.ID
		}
		return ""
	}

	click("精选视频2")
	click("我的")
	click("引力场")
	if playing() != "p2" {
		t.Fatalf("引力场 again: %q playing, %d pages", playing(), len(a.pages))
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.cur() != a.home {
		t.Fatal("Escape did not go back to 引力场")
	}

	author := stub("作者", 1)
	a.root(a.picks) // 引力场's own 精选 pill would take the click
	a.open(author)
	click("我的")
	click("精选")
	if a.cur() != author {
		t.Fatalf("精选 again: %q on top", a.cur().title)
	}
	click("精选")
	if a.cur() != a.picks || len(a.pages) != 1 {
		t.Fatalf("精选 pressed over its pages: %q on top of %d", a.cur().title, len(a.pages))
	}
}

// A live room shows as one: its badge and viewers on its card, and playing,
// "直播中" with no time, speed or next, no counts or comments in the rail, a
// share that copies the room's link, and nothing to download.
func TestLiveRoomUI(t *testing.T) {
	var copied []string
	copyText = func(s string) error { copied = append(copied, s); return nil }
	room := dy.Video{ID: "live:1", Desc: "房间标题", Author: "主播", Live: &dy.Live{RoomID: "1", WebRID: "777", Viewers: "41"}}
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	grid := stub("搜索：x", 2)
	grid.grid = true
	grid.list[1].v = room
	a.pages = append(a.pages, grid)
	tt := newTester(a)
	if !tt.HasText("直播中") || !tt.HasText("41 人在看") || !tt.HasText("@主播") {
		t.Fatalf("card: %q", tt.Texts())
	}
	p := stub("推荐", 1)
	p.list[0].v = room
	ready(t, p.list[0])
	a.pages = []*page{p}
	tt.Frame()
	if !tt.HasText("直播中 · 41 人在看") || tt.HasText("倍速") || tt.HasText("连播") || tt.HasText("00:00 / 00:00") || tt.HasText("评论") {
		t.Fatalf("playing: %q", tt.Texts())
	}
	if err := tt.Click("分享：复制链接"); err != nil {
		t.Fatal(err)
	}
	tt.Click("更多")
	tt.Frame()
	if tt.HasText("下载到「下载」文件夹") || !tt.HasText("在浏览器中打开") {
		t.Fatalf("more: %q", tt.Texts())
	}
	if len(copied) != 1 || copied[0] != "https://live.douyin.com/777" {
		t.Fatalf("copied %q", copied)
	}
}

// The heart and the star like and save at once on screen, without pausing,
// and stay dimmed till Douyin answers; one Douyin refuses goes back as it
// was, saying why.
func TestLikeSave(t *testing.T) {
	p := stub("推荐", 1)
	p.list[0].v = dy.Video{ID: "42", Author: "作者", Likes: 10, Collects: 5}
	ready(t, p.list[0])
	s := p.list[0]
	a := withRoots(&app{pages: []*page{p}, posted: make(chan func(), 8)})
	answer := make(chan error)
	var sent []string
	a.setLiked = func(id string, on bool) error { sent = append(sent, fmt.Sprint("like ", id, " ", on)); return <-answer }
	a.setSaved = func(id string, on bool) error { sent = append(sent, fmt.Sprint("save ", id, " ", on)); return <-answer }
	tt := newTester(a)
	if err := tt.Click("点赞"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !s.v.Liked || s.v.Likes != 11 || !s.liking || a.paused || !tt.HasText("11") {
		t.Fatalf("after a click: %+v liking %v paused %v", s.v, s.liking, a.paused)
	}
	tt.Click("取消点赞") // still on its way: nothing more
	answer <- nil
	waitFor(t, a, tt, func() bool { return !s.liking })
	if !s.v.Liked || s.v.Likes != 11 {
		t.Fatalf("after Douyin took it: %+v", s.v)
	}
	if err := tt.Click("收藏"); err != nil {
		t.Fatal(err)
	}
	answer <- errors.New("不行")
	waitFor(t, a, tt, func() bool { return !s.saving })
	if s.v.Saved || s.v.Collects != 5 || !tt.HasText("收藏没成功：不行") {
		t.Fatalf("a refused save stayed: %+v, %q", s.v, tt.Texts())
	}
	if len(sent) != 2 || sent[0] != "like 42 true" || sent[1] != "save 42 true" {
		t.Fatalf("sent %q", sent)
	}
}

// waitFor runs what a posts and frames, as the window would, until ok or a
// second has passed.
func waitFor(t *testing.T, a *app, tt *ui.Tester, ok func() bool) {
	t.Helper()
	for range 100 {
		select {
		case fn := <-a.posted:
			fn()
		default:
		}
		tt.Frame()
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// 分享 opens a panel of the accounts followed, which its search narrows,
// typing a space without pausing; a 分享 sends the video to one, showing
// 发送中… then 已分享; one that fails goes back to 分享 and says why.
func TestSharePanel(t *testing.T) {
	p := stub("推荐", 1)
	p.list[0].v = dy.Video{ID: "42", Author: "作者"}
	ready(t, p.list[0])
	a := withRoots(&app{pages: []*page{p}, posted: make(chan func(), 8),
		follows: []dy.User{{ID: "a", Name: "甲"}, {ID: "b", Name: "乙 丙"}}})
	answer := make(chan error)
	var sent []string
	a.sendShare = func(id, name string) error { sent = append(sent, id+" "+name); return <-answer }
	tt := newTester(a)
	if err := tt.Click("分享"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("甲") || !tt.HasText("乙 丙") || !tt.HasText("分享给朋友") {
		t.Fatalf("panel shows %q", tt.Texts())
	}
	tt.Click("搜索朋友")
	tt.Frame()
	tt.Type("乙 ")
	tt.Frame()
	if tt.HasText("甲") || !tt.HasText("乙 丙") || a.paused {
		t.Fatalf("searching 乙: %q, paused %v", tt.Texts(), a.paused)
	}
	if err := tt.Click("分享给乙 丙"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("发送中…") {
		t.Fatalf("sending shows %q", tt.Texts())
	}
	answer <- nil
	waitFor(t, a, tt, func() bool { return tt.HasText("已分享") })
	a.shareQuery = ""
	tt.Frame()
	if err := tt.Click("分享给甲"); err != nil {
		t.Fatal(err)
	}
	answer <- errors.New("对方没回复，只能发一条")
	waitFor(t, a, tt, func() bool { return a.shared["42|a"] == "" })
	tt.Frame()
	if !tt.HasText("没分享给甲：对方没回复，只能发一条") || len(sent) != 2 || sent[0] != "42 乙 丙" || sent[1] != "42 甲" {
		t.Fatalf("after a refusal: %q, sent %q", tt.Texts(), sent)
	}
}

// Seeking a video's decoder ends what it decodes, as a loop's end does, and
// its Rewind then goes on from there, its clock with it.
func TestPacedSeek(t *testing.T) {
	f, err := media.Open("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	vd, err := f.Video()
	if err != nil {
		t.Fatal(err)
	}
	var speed atomic.Uint64
	dec := &paced{VideoDecoder: vd, speed: &speed}
	defer dec.Close()
	dec.Next()
	dec.seek(1500 * time.Millisecond)
	if _, err := dec.Next(); err != io.EOF {
		t.Fatalf("seeking, Next gave %v", err)
	}
	if err := dec.Rewind(); err != nil {
		t.Fatal(err)
	}
	if got := time.Duration(dec.done.Load()); got != 1500*time.Millisecond {
		t.Fatalf("the clock after the seek: %v", got)
	}
	n := 0
	for {
		if _, err := dec.Next(); err != nil {
			break
		}
		n++
	}
	if n < 12 || n > 18 { // half a second of 30 fps, give or take where frames start
		t.Fatalf("%d frames after 1.5 s of a 2 s clip", n)
	}
	if err := dec.Rewind(); err != nil || dec.done.Load() != 0 {
		t.Fatalf("a plain rewind: clock %v, %v", dec.done.Load(), err)
	}
}

// A press on the progress and a drag show where the video would go, which
// it goes to on the release.
func TestProgressDrag(t *testing.T) {
	f, err := media.Open("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	vd, _ := f.Video()
	var speed atomic.Uint64
	p := stub("推荐", 1)
	s := p.list[0]
	ready(t, s)
	s.dec = &paced{VideoDecoder: vd, speed: &speed}
	defer s.dec.Close()
	s.v.Duration = 10 * time.Second
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)
	r, ok := tt.Find("进度")
	if !ok {
		t.Fatalf("no progress: %q", tt.Texts())
	}
	y := r.Y + r.H/2
	tt.Press(r.X+r.W*0.25, y)
	tt.Frame()
	tt.Move(r.X+r.W*0.75, y)
	tt.Frame()
	if !a.dragging || !tt.HasText("00:08 / 00:10") || s.dec.to.Load() != 0 {
		t.Fatalf("dragging %v, %q, seeking %v", a.dragging, tt.Texts(), s.dec.to.Load())
	}
	tt.Release(r.X+r.W*0.75, y)
	tt.Frame()
	if got := time.Duration(s.dec.to.Load() - 1); a.dragging || got < 7400*time.Millisecond || got > 7600*time.Millisecond {
		t.Fatalf("after the release: dragging %v, going to %v", a.dragging, got)
	}
}

// However narrow the video, as with the comments open beside it in a small
// window, the bar's controls stay within it: those it has no room for give
// way, 全屏 and 网页全屏 never, and the time keeps to one line.
func TestBarFitsNarrowVideo(t *testing.T) {
	p := stub("推荐", 1)
	p.list[0].v.Duration = 260 * time.Second
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)
	for _, size := range [][2]int{{960, 620}, {1100, 760}, {1280, 820}, {1600, 900}} {
		for _, comments := range []bool{false, true} {
			a.sheet = nil
			if comments {
				a.sheet = &sheet{done: true}
			}
			tt.SetSize(size[0], size[1])
			tt.Frame()
			tt.Frame() // the bar sized to the video laid out
			bar, ok := tt.Find("进度")
			if !ok {
				t.Fatalf("%v comments %v: no progress", size, comments)
			}
			left, right := bar.X-12, bar.X+bar.W+12 // the progress is inset 12 from the video's edges
			for _, l := range []string{"暂停", "00:00 / 04:20", "弹幕", "弹幕内容", "发送弹幕", "连播", "清屏", "清晰度：智能", "倍速：倍速", "稍后再看", "小窗", "音量", "网页全屏", "全屏"} {
				r, ok := tt.Find(l)
				switch {
				case !ok && (l == "全屏" || l == "网页全屏" || l == "暂停" || l == "00:00 / 04:20"):
					t.Fatalf("%v comments %v: %s gone", size, comments, l)
				case ok && (r.X < left || r.X+r.W > right):
					t.Fatalf("%v comments %v: %s at %.0f–%.0f, out of the video %.0f–%.0f", size, comments, l, r.X, r.X+r.W, left, right)
				case ok && l == "00:00 / 04:20" && r.H > 20:
					t.Fatalf("%v comments %v: the time wraps, %.0f tall", size, comments, r.H)
				}
			}
		}
	}
}

// The time over the progress follows the pointer as it slides along, each
// move, not only when the playing clock next redraws.
func TestProgressHoverFollows(t *testing.T) {
	p := stub("推荐", 1)
	p.list[0].v.Duration = 100 * time.Second
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)
	r, ok := tt.Find("进度")
	if !ok {
		t.Fatalf("no progress: %q", tt.Texts())
	}
	y := r.Y + r.H/2
	for _, at := range []struct {
		f    float32
		want string
	}{{0.2, "00:20"}, {0.6, "01:00"}, {0.61, "01:01"}, {0.35, "00:35"}} {
		tt.Move(r.X+r.W*at.f+0.5, y) // no Frame: the move alone must bring one
		if !tt.HasText(at.want) {
			t.Fatalf("pointer at %.0f%%: the time shows %q, want %s", at.f*100, tt.Texts(), at.want)
		}
	}
}

// The bar puts the video in 稍后再看 and takes it out, dimmed while it goes;
// a bullet comment typed, spaces and all without pausing, flies at once on
// Enter and goes back off if Douyin refuses it; 小窗 takes the video out of
// the window, which says so, and its own view plays it and closes.
func TestBarExtras(t *testing.T) {
	p := stub("推荐", 1)
	p.list[0].v = dy.Video{ID: "42", Author: "作者", Duration: time.Minute}
	ready(t, p.list[0])
	s := p.list[0]
	a := withRoots(&app{pages: []*page{p}, posted: make(chan func(), 8)})
	answer := make(chan error)
	var sent []string
	a.setLater = func(id string, on bool) error {
		sent = append(sent, fmt.Sprint("later ", id, " ", on))
		return <-answer
	}
	a.sendDanmaku = func(id string, at time.Duration, text string) error {
		sent = append(sent, "dm "+id+" "+text)
		return <-answer
	}
	tt := ui.NewTester(a.view, 1400, 820)
	if err := tt.Click("稍后再看"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.later["42"] || !a.laterBusy["42"] {
		t.Fatal("稍后再看 did not show at once")
	}
	answer <- nil
	waitFor(t, a, tt, func() bool { return !a.laterBusy["42"] })
	if !a.later["42"] || !tt.HasText("已加入稍后再看") {
		t.Fatalf("after Douyin took it: %v %q", a.later["42"], tt.Texts())
	}
	if err := tt.Click("弹幕内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type("好 看")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.paused || a.dmText != "" || len(s.danmaku) != 1 || s.danmaku[0].Text != "好 看" {
		t.Fatalf("after Enter: paused %v, text %q, danmaku %+v", a.paused, a.dmText, s.danmaku)
	}
	answer <- errors.New("太快了")
	waitFor(t, a, tt, func() bool { return len(s.danmaku) == 0 })
	if !tt.HasText("弹幕没发出去：太快了") {
		t.Fatalf("a refused bullet comment: %q", tt.Texts())
	}
	if len(sent) != 2 || sent[0] != "later 42 true" || sent[1] != "dm 42 好 看" {
		t.Fatalf("sent %q", sent)
	}
	if err := tt.Click("小窗"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.pipOn || !tt.HasText("小窗播放中") {
		t.Fatalf("小窗: %v %q", a.pipOn, tt.Texts())
	}
	pt := ui.NewTester(a.pipView, 480, 270)
	pt.Move(240, 135)
	pt.Frame()
	if !pt.HasText("@作者") {
		t.Fatalf("小窗 shows %q", pt.Texts())
	}
	if err := pt.Click("关闭小窗"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.pipOn || tt.HasText("小窗播放中") {
		t.Fatal("closing 小窗 left the window without its video")
	}
}

// A search's accounts show atop its videos, an author's profile atop theirs,
// and the search field drops what Douyin suggests for what is typed, until
// more is typed.
func TestProfileUsersSuggest(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	s := stub("搜索：美食", 1)
	s.grid, s.users = true, []dy.User{{ID: "u1", Name: "美食家"}}
	a.pages = append(a.pages, s)
	tt := newTester(a)
	tt.Frame()
	if !tt.HasText("美食家") {
		t.Fatalf("search: %q", tt.Texts())
	}
	au := stub("@美食家", 1)
	au.grid, au.user = true, &dy.User{Name: "美食家本人", Handle: "zz123", Place: "江苏", Followers: 120000}
	a.pages = append(a.pages, au)
	tt.Frame()
	if !tt.HasText("美食家本人") || !tt.HasText("抖音号：zz123   IP属地：江苏") || !tt.HasText("12.0万") {
		t.Fatalf("author: %q", tt.Texts())
	}
	a.sugs, a.sugFor = []string{"美食做法家常菜"}, "美食"
	tt.Click("搜索")
	tt.Type("美食")
	tt.Frame()
	if !tt.HasText("美食做法家常菜") {
		t.Fatalf("suggestions: %q", tt.Texts())
	}
	tt.Type("x")
	tt.Frame()
	if tt.HasText("美食做法家常菜") {
		t.Fatal("suggestions for 美食 over 美食x")
	}
}

// A comment's replies open under it a page at a time, a reply to a reply
// saying whom it answers, and close; open again, they show without loading.
func TestReplies(t *testing.T) {
	feed := stub("推荐", 1)
	ready(t, feed.list[0])
	a := withRoots(&app{pages: []*page{feed}, posted: make(chan func(), 8)})
	pages := [][]dy.Comment{{{Author: "丙", Text: "回复一"}}, {{Author: "丁", Text: "回复二", ReplyTo: "丙"}}}
	asked := 0
	a.replies = func(_, id string) dy.Pager[dy.Comment] {
		if id != "c1" {
			t.Fatalf("replies of %q", id)
		}
		return func() ([]dy.Comment, error) {
			asked++
			return pages[asked-1], nil
		}
	}
	a.sheet = &sheet{total: 1, done: true, items: []dy.Comment{{ID: "c1", Author: "甲", Text: "第一条", Replies: 2}}}
	tt := newTester(a)
	pump := func() {
		tt.Frame()
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatal("the replies did not come")
		}
		tt.Frame()
	}
	if err := tt.Click("展开 2 条回复"); err != nil {
		t.Fatal(err)
	}
	pump()
	if !tt.HasText("回复一") || !tt.HasText("展开更多") || !tt.HasText("收起") {
		t.Fatalf("opened: %q", tt.Texts())
	}
	tt.Click("展开更多")
	pump()
	if !tt.HasText("丁 ▸ 丙") || !tt.HasText("回复二") || tt.HasText("展开更多") {
		t.Fatalf("all of them: %q", tt.Texts())
	}
	tt.Click("收起")
	tt.Frame()
	if tt.HasText("回复一") || !tt.HasText("展开 2 条回复") {
		t.Fatalf("closed: %q", tt.Texts())
	}
	tt.Click("展开 2 条回复")
	tt.Frame()
	if !tt.HasText("回复二") || asked != 2 {
		t.Fatalf("opened again: asked %d, %q", asked, tt.Texts())
	}
}

// A slow trackpad swipe sends its scrolls for longer than step's 400 ms;
// it is one swipe and moves one video. A mouse wheel moves one a notch.
func TestSwipeStepsOnce(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 5)}})
	tt := newTester(a)
	tt.Frame()
	swipe := func() {
		for range 40 {
			tt.Scroll(650, 300, 0, 6)
			tt.Advance(16 * time.Millisecond)
			time.Sleep(16 * time.Millisecond) // step's own pause is by the wall clock
		}
	}
	swipe()
	if a.feed.idx != 1 {
		t.Fatalf("one swipe moved to %d", a.feed.idx)
	}
	tt.Advance(300 * time.Millisecond)
	swipe()
	if a.feed.idx != 2 {
		t.Fatalf("a second swipe moved to %d", a.feed.idx)
	}
	time.Sleep(400 * time.Millisecond)
	tt.Wheel(650, 300, 0, 40)
	if a.feed.idx != 3 {
		t.Fatalf("a wheel notch moved to %d", a.feed.idx)
	}
}

// A comment's heart likes it at once, and goes to Douyin as a comment's (a
// reply as a reply's); one Douyin refuses is put back, and says why.
func TestCommentLike(t *testing.T) {
	feed := stub("推荐", 1)
	ready(t, feed.list[0])
	a := withRoots(&app{pages: []*page{feed}, posted: make(chan func(), 8)})
	type call struct {
		video, cid string
		reply, on  bool
	}
	var calls []call
	var refuse error
	a.likeComment = func(video, cid string, reply, on bool) error {
		calls = append(calls, call{video, cid, reply, on})
		return refuse
	}
	a.sheet = &sheet{total: 1, done: true, v: dy.Video{ID: "v1"}, items: []dy.Comment{{ID: "c1", Author: "甲", Text: "第一条", Likes: 9}}}
	tt := newTester(a)
	tt.Frame()
	press := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if a.sheet.liking["c1"] != true {
			t.Fatal("not dimmed while it goes")
		}
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatal("the like did not come back")
		}
		tt.Frame()
	}
	press("赞评论")
	if cm := a.sheet.items[0]; !cm.Liked || cm.Likes != 10 || !tt.HasText("10") || len(calls) != 1 || calls[0] != (call{"v1", "c1", false, true}) {
		t.Fatalf("liked: %+v, calls %+v", cm, calls)
	}
	refuse = errors.New("不行")
	press("取消赞评论")
	if cm := a.sheet.items[0]; !cm.Liked || cm.Likes != 10 || calls[1].on {
		t.Fatalf("refused: %+v, calls %+v", cm, calls)
	}
	if !tt.HasText("取消赞评论没成功：不行") {
		t.Fatalf("no reason shown: %q", tt.Texts())
	}
}

// A comment's emoji Douyin names are asked for and drawn in its text, a name
// it does not know stays text, and its pictures are asked for under it.
func TestCommentEmojiAndPics(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 3)}})
	asked := make(chan string, 8)
	a.img = newImages(func(u string) ([]byte, error) { asked <- u; return nil, errors.New("offline") }, func(func()) {})
	a.emoji = map[string]string{"[赞]": "emoji:赞"}
	a.sheet = &sheet{total: 1, done: true, items: []dy.Comment{{ID: "c1", Author: "甲", Text: "好[赞]看[不认识]", Pics: []dy.CommentPic{{Thumb: "pic:1", Origin: "pic:1:full"}}}}}
	tt := newTester(a)
	tt.Frame()
	got := map[string]bool{}
	for range 2 {
		select {
		case u := <-asked:
			got[u] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("asked for %v", got)
		}
	}
	select {
	case u := <-asked:
		got[u] = true
	case <-time.After(200 * time.Millisecond):
	}
	if len(got) != 2 || !got["emoji:赞"] || !got["pic:1"] {
		t.Fatalf("asked for %v", got)
	}
	if !tt.HasText("好[赞]看[不认识]") { // what is read out keeps the names
		t.Fatalf("text shows %q", tt.Texts())
	}
}

// emojiSpans makes each emoji Douyin names an object at its rune, as for
// bullet comments, in the text's size and weight; other names stay text.
func TestEmojiSpans(t *testing.T) {
	a := &app{emoji: map[string]string{"[赞]": "e"}}
	spans, pics := a.emojiSpans("好看[赞][不认识]啊", 17, 600)
	if len(spans) != 3 || spans[0].Text != "好看" || spans[1].ObjectWidth == 0 || spans[2].Text != "[不认识]啊" || spans[2].Weight != 600 {
		t.Fatalf("spans %+v", spans)
	}
	if _, ok := pics[2]; !ok || len(pics) != 1 {
		t.Fatalf("pics %v", pics)
	}
	if spans, pics := a.emojiSpans("没有表情", 17, 600); len(spans) != 1 || pics != nil {
		t.Fatalf("plain: %+v %v", spans, pics)
	}
}

// A toast's text shows on its fill: the theme's Background is clear, which
// toasts took for their text, leaving a blank pill (2026-11-17).
func TestToastReadable(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 3)}})
	tt := newTester(a)
	a.toast = "点赞没成功：抖音没接受"
	tt.Frame()
	tt.Advance(time.Second) // faded in
	tt.Frame()
	img := tt.Image()
	b := img.Bounds()
	bright := func(x, y int) bool { p := img.RGBAAt(x, y); return p.R > 200 && p.G > 200 && p.B > 200 }
	strokes := 0 // dark pixels between the toast's light fill, at its foot
	for y := b.Max.Y - 80; y < b.Max.Y; y++ {
		for x := b.Max.X/2 - 120; x < b.Max.X/2+80; x++ {
			if p := img.RGBAAt(x, y); int(p.R)+int(p.G)+int(p.B) < 300 && bright(x-4, y) && bright(x+4, y) {
				strokes++
			}
		}
	}
	if strokes < 30 {
		t.Fatalf("the toast shows %d pixels of its text", strokes)
	}
}

// A root pressed again while shown loads anew, from the top where it has a
// restart; a grid playing goes back to its covers first; the tab shown
// pressed again refreshes it too.
func TestRefresh(t *testing.T) {
	batch := 0
	from := func() dy.Pager[dy.Video] {
		batch++
		n := batch
		return func() ([]dy.Video, error) {
			return []dy.Video{{ID: fmt.Sprint("b", n), Desc: fmt.Sprint("第", n, "批"), Author: "作者"}}, nil
		}
	}
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}, tabPages: map[int]*page{}, posted: make(chan func(), 64)})
	a.picks = &page{title: "精选", grid: true, next: from(), restart: from}
	a.tabPages[0] = a.picks
	settle := func() {
		for a.picks.fetching {
			select {
			case fn := <-a.posted:
				fn()
			case <-time.After(2 * time.Second):
				t.Fatal("the page never loaded")
			}
		}
	}
	first := func() string {
		if len(a.picks.list) == 0 {
			return ""
		}
		return a.picks.list[0].v.Desc
	}
	a.root(a.picks)
	settle()
	if first() != "第1批" {
		t.Fatalf("first load: %q", first())
	}
	a.root(a.picks)
	settle()
	if first() != "第2批" || len(a.pages) != 1 {
		t.Fatalf("pressed again: %q", first())
	}
	a.play(a.picks, 0)
	a.root(a.picks)
	if a.picks.playing || first() != "第2批" {
		t.Fatalf("playing, pressed again: playing %v, %q", a.picks.playing, first())
	}
	tt := newTester(a)
	if err := tt.Click("全部"); err != nil {
		t.Fatal(err)
	}
	settle()
	if first() != "第3批" {
		t.Fatalf("the tab shown pressed again: %q", first())
	}
}

// A video played past watchedAfter is queued once to tell Douyin, and a
// batch goes once watchBatch wait; nothing is queued logged out.
func TestWatchedBatches(t *testing.T) {
	var told []string
	a := &app{signedIn: true, posted: make(chan func(), 64)}
	a.sendWatched = func(id, uid string) error { told = append(told, id+"/"+uid); return nil }
	for i := range watchBatch {
		s := &slot{v: dy.Video{ID: fmt.Sprint(i), AuthorUID: "u"}}
		a.watch(s, time.Second)
		a.watch(s, watchedAfter+time.Second)
		a.watch(s, watchedAfter+2*time.Second)
	}
	if !a.watchSending {
		t.Fatalf("a full batch not sent: %d waiting", len(a.watched))
	}
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("the batch never finished")
	}
	if len(told) != watchBatch || told[0] != "0/u" || a.watchSending || len(a.watched) != 0 {
		t.Fatalf("told %q, sending %v, waiting %d", told, a.watchSending, len(a.watched))
	}
	a.signedIn = false
	a.watch(&slot{v: dy.Video{ID: "x"}}, time.Minute)
	if len(a.watched) != 0 {
		t.Fatal("queued while logged out")
	}
}

// 观看历史 shown from elsewhere loads anew from Douyin, after what was
// watched here and not yet told is told, so it is there.
func TestHistoryLoadsAnew(t *testing.T) {
	var did []string
	loads := 0
	from := func() dy.Pager[dy.Video] {
		loads++
		n := loads
		return func() ([]dy.Video, error) {
			did = append(did, fmt.Sprint("load ", n))
			return []dy.Video{{ID: fmt.Sprint("h", n)}}, nil
		}
	}
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}, me: dy.User{ID: "me"}, signedIn: true, posted: make(chan func(), 64)})
	a.sendWatched = func(id, uid string) error { did = append(did, "told "+id); return nil }
	a.mine = map[string]*page{}
	for _, n := range mineTabs {
		a.mine[n] = stub(n, 1)
		a.mine[n].grid = true
	}
	h := &page{title: "观看历史", grid: true, next: from(), restart: from}
	a.mine["观看历史"] = h
	settle := func() {
		t.Helper()
		for h.fetching || a.watchSending {
			select {
			case fn := <-a.posted:
				fn()
			case <-time.After(2 * time.Second):
				t.Fatal("观看历史 never loaded")
			}
		}
	}
	first := func() string { return h.list[0].v.ID }

	a.showMine("观看历史")
	settle()
	shown := first()
	a.showMine("作品")
	a.markWatched(dy.Video{ID: "v1", AuthorUID: "u"})
	did = nil
	a.showMine("观看历史")
	settle()
	if fmt.Sprint(did) != fmt.Sprintf("[told v1 load %d]", loads) || first() == shown {
		t.Fatalf("shown again: %q, %s first (was %s)", did, first(), shown)
	}
}

// What waits to be told when the app quits is told after the next launch,
// once the account it was watched on is back; not to another account.
func TestWatchedKeptOverQuit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	launch := func(account string) *app {
		a := &app{signedIn: true, me: dy.User{ID: account}, settingsFile: file, posted: make(chan func(), 64)}
		a.sendWatched = func(id, uid string) error { return nil }
		return a
	}
	a := launch("me")
	a.markWatched(dy.Video{ID: "v1", AuthorUID: "u1"})
	a.markWatched(dy.Video{ID: "v2", AuthorUID: "u2"})
	a.watchTimer.Stop()
	a.keepWatched()

	b := launch("me")
	b.resumeWatched("me")
	if b.watchTimer != nil {
		b.watchTimer.Stop()
	}
	if got := fmt.Sprint(b.watched); !strings.Contains(got, "v1") || !strings.Contains(got, "u2") || len(b.watched) != 2 {
		t.Fatalf("queued after relaunch: %d", len(b.watched))
	}

	a.keepWatched()
	c := launch("other")
	c.resumeWatched("other")
	if len(c.watched) != 0 {
		t.Fatal("told to another account")
	}
	c.resumeWatched("me")
	if len(c.watched) != 0 {
		t.Fatal("kept after another account came")
	}
}

// Comments fetched ahead show at once, nothing asked again; when fetching
// ahead failed, opening asks again.
func TestCommentsAhead(t *testing.T) {
	asked := 0
	s := &slot{v: dy.Video{ID: "v"}}
	s.comments = &sheet{v: s.v, items: []dy.Comment{{Author: "甲", Text: "早到"}},
		next: func() ([]dy.Comment, error) { asked++; return []dy.Comment{{Author: "乙", Text: "又拉"}}, nil }}
	a := withRoots(&app{pages: []*page{{title: "推荐", done: true, list: []*slot{s}}}, posted: make(chan func(), 4)})
	a.comments(s)
	if a.sheet != s.comments || asked != 0 || a.sheet.fetching {
		t.Fatalf("fetched ahead: asked %d, fetching %v", asked, a.sheet.fetching)
	}
	a.sheet = nil
	s.comments.items, s.comments.msg = nil, "失败"
	a.comments(s)
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("never asked again")
	}
	if asked != 1 || len(a.sheet.items) != 1 || a.sheet.msg != "" {
		t.Fatalf("after a failed fetch ahead: asked %d, %d items, msg %q", asked, len(a.sheet.items), a.sheet.msg)
	}
}

// The rail's 评论 opens the comments from anywhere in it, its count too,
// with a press held as a hand holds it; in 网页全屏 the comments show beside
// the video.
func TestRailHitArea(t *testing.T) {
	p := stub("推荐", 1)
	p.list[0].v = dy.Video{ID: "42", Author: "作者", Comments: 758}
	p.list[0].comments = &sheet{total: 758, done: true, items: []dy.Comment{{Author: "甲", Text: "第一条"}}}
	ready(t, p.list[0])
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)
	r, _ := tt.Find("评论")
	n, _ := tt.Find("758")
	for _, at := range [][2]float32{{r.X + r.W - 2, r.Y + 25}, {r.X + r.W/2, r.Y + 2}, {n.X + n.W/2, n.Y + n.H/2}} {
		a.sheet = nil
		tt.Frame()
		tt.Press(at[0], at[1])
		for range 8 { // some 130 ms
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
		tt.Release(at[0], at[1])
		tt.Frame()
		if a.sheet == nil || a.paused {
			t.Fatalf("a press at %v in %+v: comments %v, paused %v", at, r, a.sheet != nil, a.paused)
		}
	}
	a.sheet, a.theater = nil, true
	tt.Frame()
	if err := tt.Click("评论"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("第一条") {
		t.Fatal("no comments in 网页全屏")
	}
}

// Replies held back for Douyin's verification while it shows already wait
// for it with the first; once passed, held back still, they say so rather
// than show the verification again, which flashed it (2026-11-19).
func TestRepliesVerifyOnce(t *testing.T) {
	a := &app{posted: make(chan func(), 8), loginOpen: true, login: login{verify: true, stop: func() {}}}
	held := func() ([]dy.Comment, error) { return nil, dy.ErrVerify }
	run := func(th *thread) {
		a.moreReplies(th)
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatal("no answer")
		}
	}
	waiting := &thread{next: held}
	run(waiting)
	if len(a.login.retry) != 1 {
		t.Fatalf("a second held back did not wait for the verification open: %d", len(a.login.retry))
	}
	passed := &thread{next: held, verified: true}
	run(passed)
	if len(a.login.retry) != 1 || !strings.Contains(passed.msg, "验证过了") {
		t.Fatalf("verified, held back still: %d waiting, %q", len(a.login.retry), passed.msg)
	}
}

// The progress line follows the playing video between the view's builds, as
// the bullet comments do, rather than in its half-second steps.
func TestProgressGlidesBetweenBuilds(t *testing.T) {
	p := stub("推荐", 1)
	s := p.list[0]
	s.v.Duration, s.resume = time.Minute, 10*time.Second
	ready(t, s)
	a := withRoots(&app{pages: []*page{p}, dmOff: true})
	tt := newTester(a)
	tt.Advance(300 * time.Millisecond) // the controls faded in
	r, ok := tt.Find("进度")
	if !ok {
		t.Fatalf("no progress line: %q", tt.Texts())
	}
	line := func() []byte {
		img := tt.Image()
		var out []byte
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			out = append(out, img.Pix[y*img.Stride+int(r.X)*4:y*img.Stride+int(r.X+r.W)*4]...)
		}
		return out
	}
	before := line()
	s.resume += 6 * time.Second
	tt.Advance(40 * time.Millisecond)
	if slices.Equal(before, line()) {
		t.Fatal("the progress line stood still while the video played on")
	}
}

// A danmaku entering or leaving mid-press must never shift rail button IDs or
// drop clicks on 评论, 点赞, or 收藏.
func TestDanmakuArrivalDoesNotDropRailClick(t *testing.T) {
	p := stub("推荐", 1)
	p.list[0].v = dy.Video{ID: "42", Author: "作者", Likes: 10, Collects: 5, Comments: 758, Duration: time.Minute}
	p.list[0].comments = &sheet{total: 758, done: true, items: []dy.Comment{{Author: "甲", Text: "第一条"}}}
	p.list[0].danmaku = []dy.Danmaku{
		{At: 1 * time.Second, Text: "第一条弹幕"},
		{At: 2 * time.Second, Text: "第二条弹幕"},
	}
	p.list[0].resume = 1500 * time.Millisecond
	ready(t, p.list[0])
	a := withRoots(&app{
		pages:    []*page{p},
		posted:   make(chan func(), 16),
		setLiked: func(string, bool) error { return nil },
		setSaved: func(string, bool) error { return nil },
	})
	tt := newTester(a)
	tt.Frame()

	clickAcrossDanmaku := func(label string) {
		t.Helper()
		p.list[0].resume = 1500 * time.Millisecond
		tt.Frame()
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("%s button not found", label)
		}
		x, y := r.X+r.W/2, r.Y+r.H/2
		tt.Press(x, y)
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		p.list[0].resume = 2100 * time.Millisecond // second danmaku enters mid-press
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		tt.Release(x, y)
		tt.Frame()
	}

	clickAcrossDanmaku("评论")
	if a.sheet == nil {
		t.Fatal("评论 click dropped when danmaku arrived mid-press")
	}
	a.sheet = nil
	clickAcrossDanmaku("点赞")
	if !p.list[0].v.Liked {
		t.Fatal("点赞 click dropped when danmaku arrived mid-press")
	}
	clickAcrossDanmaku("收藏")
	if !p.list[0].v.Saved {
		t.Fatal("收藏 click dropped when danmaku arrived mid-press")
	}
}

// Releasing an off-screen slot drops its cached comment sheet so scrolled-past
// videos do not accumulate comments in memory.
func TestReleaseClearsOffscreenComments(t *testing.T) {
	s1 := &slot{v: dy.Video{ID: "1"}, comments: &sheet{total: 10, items: []dy.Comment{{ID: "c1", Text: "a"}}}}
	s2 := &slot{v: dy.Video{ID: "2"}, comments: &sheet{total: 10, items: []dy.Comment{{ID: "c2", Text: "b"}}}}
	a := &app{sheet: s2.comments}
	a.release(s1)
	if s1.comments != nil {
		t.Fatal("release retained off-screen slot comments")
	}
	a.release(s2)
	if s2.comments == nil {
		t.Fatal("release dropped currently open comment sheet")
	}
}

// Failing to load comments or replies keeps a clickable 重试 button so the
// user is never stranded on an error message, and background comment prefetch
// does not pop up verification until the comment panel is actually open.
func TestCommentsAndRepliesRetryAndPrefetchVerify(t *testing.T) {
	feed := stub("推荐", 1)
	ready(t, feed.list[0])
	a := withRoots(&app{pages: []*page{feed}, posted: make(chan func(), 16)})

	commentAttempt := 0
	sh := &sheet{
		v:     dy.Video{ID: "v1"},
		total: 5,
		next: func() ([]dy.Comment, error) {
			commentAttempt++
			if commentAttempt == 1 {
				return nil, dy.ErrVerify
			}
			return []dy.Comment{{ID: "c1", Author: "甲", Text: "一级评论", Replies: 2}}, nil
		},
	}
	// Background prefetch (a.sheet != sh) must not trigger verify overlay.
	a.moreComments(sh)
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("prefetch did not settle")
	}
	if a.loginOpen || sh.msg == "" {
		t.Fatalf("prefetch verify: loginOpen=%v msg=%q", a.loginOpen, sh.msg)
	}

	// Opening the panel with the error shows a 重试 button that reloads comments.
	a.sheet = sh
	tt := newTester(a)
	tt.Frame()
	if !tt.HasText("重试") {
		t.Fatalf("missing retry button on comment error: %q", tt.Texts())
	}
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("comment retry did not settle")
	}
	tt.Frame()
	if !tt.HasText("一级评论") || sh.msg != "" {
		t.Fatalf("after comment retry: msg=%q texts=%q", sh.msg, tt.Texts())
	}

	// Reply expand failure shows 重试 and recovers on click.
	replyAttempt := 0
	a.replies = func(_, _ string) dy.Pager[dy.Comment] {
		return func() ([]dy.Comment, error) {
			replyAttempt++
			if replyAttempt == 1 {
				return nil, errors.New("网络抖动")
			}
			return []dy.Comment{{ID: "r1", Author: "乙", Text: "重试后的回复"}}, nil
		}
	}
	if err := tt.Click("展开 2 条回复"); err != nil {
		t.Fatal(err)
	}
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("first reply fetch did not settle")
	}
	tt.Frame()
	if !tt.HasText("网络抖动") || !tt.HasText("重试") {
		t.Fatalf("reply failure UI: %q", tt.Texts())
	}
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("reply retry did not settle")
	}
	tt.Frame()
	if !tt.HasText("重试后的回复") {
		t.Fatalf("after reply retry: %q", tt.Texts())
	}
}

// Opening an empty author page (where only the loading footer renders first)
// must stay scrolled at the top when its first page of videos arrives, rather
// than chasing the footer row to the bottom.
func TestAuthorGridStaysAtTopAfterLoad(t *testing.T) {
	ch := make(chan []dy.Video, 1)
	author := &page{
		title:    "@博主",
		authorID: "sec-author",
		grid:     true,
		covers:   portraitCovers,
		next: func() ([]dy.Video, error) {
			return <-ch, nil
		},
	}
	a := withRoots(&app{pages: []*page{stub("推荐", 1), author}, posted: make(chan func(), 8)})
	tt := newTester(a)
	tt.Frame() // empty page: row 0 is the loading footer ("foot")
	if !tt.HasText("载入中…") {
		t.Fatalf("expected loading footer, got %q", tt.Texts())
	}

	batch := make([]dy.Video, 18)
	for i := range batch {
		batch[i] = dy.Video{ID: fmt.Sprintf("v%d", i), Desc: fmt.Sprintf("最新视频%d", i), Author: "博主"}
	}
	ch <- batch
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("author posts did not arrive")
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("最新视频0") {
		t.Fatalf("first video scrolled out of view after load; visible texts=%q", tt.Texts())
	}
}

// Playing at 2x speed keeps bullet comments crossing at the same wall-clock
// speed as 1x playback; adjusting danmaku speed, size, stroke, and opacity
// from the control bar updates rendering immediately.
func TestDanmakuPlaybackSpeedCompensationAndStyleControls(t *testing.T) {
	// 1. Speed compensation: after 4 real seconds (4s video at 1x, 8s video at 2x),
	// a bullet starting at 0s must be at the exact same x position.
	dm := []dy.Danmaku{{At: 0, Text: "倍速弹幕测试"}}
	a1 := &app{speed: 1}
	f1 := &dmField{}
	at1x, _ := flyDanmaku(a1, f1, dm, sameDanmaku, 4*time.Second, 800)

	a2 := &app{speed: 2}
	f2 := &dmField{}
	at2x, _ := flyDanmaku(a2, f2, dm, sameDanmaku, 8*time.Second, 800)

	if len(at1x) != 1 || len(at2x) != 1 || math.Abs(float64(at1x[0].x-at2x[0].x)) > 0.01 {
		t.Fatalf("2x playback speed did not preserve real-time danmaku speed: 1x=%+v 2x=%+v", at1x, at2x)
	}

	// Slowing danmaku speed to 0.5x doubles crossing duration: at 4s it has only
	// crossed half as far as at 1x.
	aSlow := &app{speed: 1, dmStyle: danmakuStyle{Speed: 0.5}}
	fSlow := &dmField{}
	atSlow, _ := flyDanmaku(aSlow, fSlow, dm, sameDanmaku, 4*time.Second, 800)
	if len(atSlow) != 1 || atSlow[0].x <= at1x[0].x {
		t.Fatalf("0.5x danmaku speed should lag behind 1x: slow=%+v 1x=%+v", atSlow, at1x)
	}

	// 2. Control bar popover adjusts speed, size, stroke, and opacity live, and
	// thick stroke draws dark outline pixels around white text over a bright bg.
	p := stub("推荐", 1)
	p.list[0].v = dy.Video{ID: "v4k", Author: "影视飓风", Duration: time.Minute}
	p.list[0].danmaku = []dy.Danmaku{{At: 0, Text: "清晰描边"}}
	p.list[0].resume = 2 * time.Second
	ready(t, p.list[0])
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)
	tt.Frame()

	if err := tt.Click("弹幕设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.dmSettingsOpen {
		t.Fatal("弹幕设置 popover did not open")
	}
	for _, label := range []string{"速度：0.75x", "字号：大", "字重：粗体", "描边：45°投影", "不透明度：75%", "显示区域：满屏"} {
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %s: %v (texts=%q)", label, err, tt.Texts())
		}
		tt.Frame()
	}
	got := a.danmakuStyle()
	if got.Speed != 0.75 || got.Size != 21 || got.Weight != dmWeightNormal || got.Stroke != dmStrokeShadow || got.Opacity != 0.75 || got.Area != 1 {
		t.Fatalf("style after clicks: %+v", got)
	}

	// Verify stroke paints dark outline pixels on a bright background and
	// semi-transparent group opacity keeps white text bright instead of graying
	// out over its own black outline.
	renderOver := func(st danmakuStyle, bg ui.Color) *image.RGBA {
		a.setDanmakuStyle(st)
		pt := ui.NewTester(func(c *ui.Context) {
			ui.Box(c).Fill().Background(bg).Children(func() {
				a.danmakuLayer(c, "dm-stroke", false, func(w, h float32, _ time.Duration) ([]dmFlying, time.Duration) {
					return flyDanmakuArea(a, &dmField{}, dm, sameDanmaku, 2*time.Second, w, h)
				})
			})
		}, 400, 120)
		return pt.Image()
	}
	countDark := func(img *image.RGBA) (dark int) {
		for i := 0; i < len(img.Pix); i += 4 {
			if img.Pix[i] < 80 && img.Pix[i+1] < 80 && img.Pix[i+2] < 80 {
				dark++
			}
		}
		return dark
	}
	noneDark := countDark(renderOver(danmakuStyle{Stroke: dmStrokeNone, Opacity: 1, Size: 21}, ui.RGB(240, 240, 240)))
	heavyDark := countDark(renderOver(danmakuStyle{Stroke: dmStrokeThick, Opacity: 1, Size: 21}, ui.RGB(240, 240, 240)))
	if noneDark != 0 || heavyDark < 50 {
		t.Fatalf("outline dark pixels: none=%d heavy=%d", noneDark, heavyDark)
	}

	// At 55% opacity over a mid-light bg (200), group compositing blends pure
	// white (255) with 200 -> ~230 (brighter than background), whereas the old
	// non-grouped path blended white over stacked black -> ~140 (dark gray).
	semiImg := renderOver(danmakuStyle{Stroke: dmStrokeThick, Opacity: 0.55, Size: 21, Weight: dmWeightBold}, ui.RGB(200, 200, 200))
	brightFill := 0
	for i := 0; i < len(semiImg.Pix); i += 4 {
		if semiImg.Pix[i] >= 220 && semiImg.Pix[i+1] >= 220 && semiImg.Pix[i+2] >= 220 {
			brightFill++
		}
	}
	if brightFill < 50 {
		t.Fatalf("55%% opacity danmaku text grayed out over outline: brightFill=%d, want >=50", brightFill)
	}

	// Display area scales usable lane count proportionally to video height.
	aQuarter := &app{dmStyle: danmakuStyle{Area: 0.25, Size: 18}}
	aFull := &app{dmStyle: danmakuStyle{Area: 1, Size: 18}}
	if q, full := aQuarter.dmLaneCount(720), aFull.dmLaneCount(720); q >= full || q < 2 || full <= dmLanes {
		t.Fatalf("area lane scaling broken: quarter=%d full=%d", q, full)
	}
}

func TestVideoSourceFallbackFor4K(t *testing.T) {
	v4k := dy.Video{
		PlayURL: "https://x/540",
		Qualities: []dy.Quality{
			{Name: "4K", URL: "https://x/4k"},
			{Name: "2K", URL: "https://x/2k"},
			{Name: "1080P", URL: "https://x/1080"},
			{Name: "540P", URL: "https://x/540"},
		},
	}
	v1080 := dy.Video{
		PlayURL: "https://x/540",
		Qualities: []dy.Quality{
			{Name: "1080P", URL: "https://x/1080"},
			{Name: "720P", URL: "https://x/720"},
			{Name: "540P", URL: "https://x/540"},
		},
	}
	if got := videoQuality(v4k, "4K").URL; got != "https://x/4k" {
		t.Fatalf("4K video with 4K pref = %s", got)
	}
	if got := videoQuality(v1080, "4K").URL; got != "https://x/1080" {
		t.Fatalf("1080P video with 4K pref fell back to %s, want 1080P", got)
	}
	if got := videoQuality(v4k, "智能").URL; got != "https://x/540" {
		t.Fatalf("智能 pref = %s", got)
	}

	// The bar shows what each video plays in, and the next 4K one is 4K again.
	a := &app{pages: []*page{stub("推荐", 3)}, quality: "4K"}
	for i, v := range []dy.Video{v4k, v1080, v4k} {
		s := a.pages[0].list[i]
		v.Author = "作者"
		s.v = v
		ready(t, s)
	}
	tt := newTester(a)
	for i, want := range []string{"4K", "1080P", "4K"} {
		if i > 0 {
			a.lastNav = time.Time{} // a step per swipe, not two in 400 ms
			a.step(1)
		}
		tt.Frame()
		if !tt.HasText(want) || want != "4K" && tt.HasText("4K") {
			t.Fatalf("video %d with 4K chosen shows %q, want %s", i, tt.Texts(), want)
		}
	}
	if a.quality != "4K" {
		t.Fatalf("stepping changed the choice to %q", a.quality)
	}
}
