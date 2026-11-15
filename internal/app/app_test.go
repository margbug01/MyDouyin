package app

import (
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

// The control bar's chips switch bullet comments and 连播 and go round the
// speeds, none of them pausing; with 连播 on, a video going back to its start
// asks for the next, and without it, it does not.
func TestControlBar(t *testing.T) {
	p := stub("推荐", 2)
	ready(t, p.list[0])
	a := withRoots(&app{pages: []*page{p}})
	a.setSpeed(1)
	tt := newTester(a)
	for _, name := range []string{"弹幕", "连播", "倍速"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(name, err)
		}
		tt.Frame()
	}
	if !a.dmOff || !a.autoNext || a.speed != 1.25 || a.paused || !tt.HasText("1.25x") {
		t.Fatalf("after the chips: dmOff %v autoNext %v speed %v paused %v", a.dmOff, a.autoNext, a.speed, a.paused)
	}
	a.setSpeed(0.75)
	tt.Frame()
	if err := tt.Click("0.75x"); err != nil {
		t.Fatal(err)
	}
	if a.speed != 1 {
		t.Fatalf("after 0.75x came %v, not 1", a.speed)
	}

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
