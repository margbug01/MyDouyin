package app

import (
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
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

// The sidebar is Douyin's: 关注 shows the accounts followed along its top,
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
			t.Fatalf("sidebar lacks %q: %q", want, tt.Texts())
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

// A bullet comment shows from its time for flight, and not before or after;
// hidden, none shows.
func TestDanmakuLayer(t *testing.T) {
	s := &slot{danmaku: []dy.Danmaku{{At: time.Second, Text: "早了"}, {At: 3 * time.Second, Text: "正在飞"}, {At: 9 * time.Second, Text: "还没到"}}}
	a := &app{}
	at := 5 * time.Second
	var shown bool
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Children(func() { shown = a.danmakuLayer(c, s, 800, at) })
	}, 800, 400)
	if !shown || !tt.HasText("早了") || !tt.HasText("正在飞") || tt.HasText("还没到") {
		t.Fatalf("at 5s: %q", tt.Texts())
	}
	at = 10 * time.Second
	tt.Frame()
	if tt.HasText("早了") || !tt.HasText("正在飞") || !tt.HasText("还没到") {
		t.Fatalf("at 10s: %q", tt.Texts())
	}
	// Those flown are let go; the one still flying keeps its lane.
	before, _ := tt.Find("正在飞")
	a.loadDanmaku(s, at)
	tt.Frame()
	if after, ok := tt.Find("正在飞"); len(s.danmaku) != 2 || !ok || after.Y != before.Y {
		t.Fatalf("after letting go of the first: %d left, y %v -> %v", len(s.danmaku), before.Y, after.Y)
	}
	a.dmOff = true
	tt.Frame()
	if shown || tt.HasText("正在飞") {
		t.Fatal("hidden bullet comments showed")
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
	for _, how := range []string{"关闭通知", "通知", "Escape"} {
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

// 我的 shows in the sidebar once the account is known, with the user's
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
		video, cid  string
		reply, on bool
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
