package app

import (
	"fmt"
	"image"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
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

// Typing in the search box does not pause, and Space types there; a click
// on the video pauses; Escape and the back button close the page on top,
// and nothing closes the feed.
func TestControls(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 3)}})
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
	own.list[0].v.AuthorID = "x"
	a.pages = append(a.pages, own)
	tt.Frame()
	tt.Click("@作者")
	tt.Frame()
	if len(a.pages) != 3 {
		t.Fatalf("author on their own page opened another: %d pages", len(a.pages))
	}
}

// An account followed, in the sidebar, opens its videos and shows chosen
// while they do; 精选 goes to its covers, with nothing over them. Live: the videos load in the background.
func TestLiveSidebar(t *testing.T) {
	c := live(t)
	a := withRoots(&app{c: c, pages: []*page{stub("推荐", 1)}, follows: []dy.User{{ID: "x", Name: "某人"}}, me: dy.User{Name: "我"}})
	tt := newTester(a)
	if err := tt.Click("某人"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 2 || a.cur().title != "@某人" || a.paused {
		t.Fatalf("after clicking a name: %d pages, paused %v", len(a.pages), a.paused)
	}
	tt.Click("某人") // already shown: nothing more
	tt.Frame()
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
	if a.picks.playing || len(a.pages) != 1 || a.picks.list[1].vp != nil {
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

// The comments open from the rail and close with their button, the rail
// again, Escape and moving to another video, none of which pauses or closes
// the page; a click in them leaves them open.
func TestComments(t *testing.T) {
	feed := stub("推荐", 3)
	ready(t, feed.list[0])
	a := withRoots(&app{pages: []*page{feed, stub("@作者", 3)}})
	a.pages[1].list[0] = feed.list[0]
	tt := newTester(a)
	open := func() {
		a.sheet = &sheet{total: 2, done: true, items: []dy.Comment{{Author: "甲", Text: "第一条"}, {Author: "乙", Text: "第二条"}}}
		tt.Frame()
		if !tt.HasText("第一条") || !tt.HasText("2 条评论") {
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
	check("the next video")
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

// The download button is off until the video is ready, does not pause, and
// shows its progress.
func TestSaveButton(t *testing.T) {
	p := stub("推荐", 1)
	s := p.list[0]
	a := withRoots(&app{pages: []*page{p}, dir: t.TempDir()})
	tt := newTester(a)
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
	if a.paused || s.saved != "保存中…" || !tt.HasText("保存中") {
		t.Fatalf("after click: paused %v, saved %q", a.paused, s.saved)
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
