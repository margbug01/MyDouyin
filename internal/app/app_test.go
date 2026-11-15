package app

import (
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

// Typing in the search box and clicking it does not pause the video; Escape
// and the back button close the page on top.
func TestControls(t *testing.T) {
	a := &app{pages: []*page{stub("", 3)}}
	tt := ui.NewTester(a.view, 405, 720)
	tt.ClickAt(200, 28) // the search box, in the top bar
	tt.Frame()
	if a.paused {
		t.Fatal("clicking the search box paused")
	}
	tt.Type("a b")
	tt.Frame()
	if a.paused || a.query != "a b" {
		t.Fatalf("after typing: paused %v, query %q", a.paused, a.query)
	}
	tt.ClickAt(200, 400) // the video
	tt.Frame()
	if !a.paused {
		t.Fatal("clicking the video did not pause")
	}

	a.pages = append(a.pages, stub("搜索：猫", 2), stub("@作者", 2))
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(a.pages) != 2 || !tt.HasText("搜索：猫") {
		t.Fatalf("after Escape: %d pages", len(a.pages))
	}
	if err := tt.Click("‹ 返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 1 || a.paused {
		t.Fatalf("after back: %d pages, paused %v", len(a.pages), a.paused)
	}
	tt.Key(0, ui.KeyEscape) // nothing under the feed
	if len(a.pages) != 1 {
		t.Fatal("Escape closed the feed")
	}
}

// Enter in the search box opens the search. Live: the search runs in the background.
func TestLiveSearchEnter(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := dy.New()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{c: c, pages: []*page{stub("", 1)}}
	tt := ui.NewTester(a.view, 405, 720)
	tt.ClickAt(200, 28) // the search box, in the top bar
	tt.Type("猫")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(a.pages) != 2 || a.cur().title != "搜索：猫" || !tt.HasText("搜索：猫") {
		t.Fatalf("after Enter: %d pages", len(a.pages))
	}
}

// Clicking the author opens their videos without pausing, Space still pauses
// after it, and the author on their own page opens nothing. Live: their
// videos load in the background.
func TestLiveAuthor(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := dy.New()
	if err != nil {
		t.Fatal(err)
	}
	feed := stub("", 1)
	feed.list[0].v.AuthorID = "x"
	a := &app{c: c, pages: []*page{feed}}
	tt := ui.NewTester(a.view, 405, 720)
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

// The follow button opens the list without pausing and keeps no focus, so
// Space still pauses; a name in the list opens that account's videos.
// Live: the lists load in the background.
func TestLiveFollowing(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := dy.New()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{c: c, pages: []*page{stub("", 1)}}
	tt := ui.NewTester(a.view, 405, 720)
	if err := tt.Click("关注"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 2 || a.cur().people == nil || a.paused {
		t.Fatalf("after click: %d pages, paused %v", len(a.pages), a.paused)
	}
	tt.Key(0, ui.KeySpace)
	if len(a.pages) != 2 || !a.paused {
		t.Fatalf("after Space: %d pages, paused %v", len(a.pages), a.paused)
	}

	a.cur().users = []dy.User{{ID: "x", Name: "某人", Signature: "签名"}}
	tt.Frame()
	if err := tt.Click("某人"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 3 || a.cur().title != "@某人" {
		t.Fatalf("after clicking a name: %d pages", len(a.pages))
	}
}

// The comments close with their ✕, a click beside them and Escape, none of
// which pauses or closes the page; moving to another video closes them too.
func TestCommentSheet(t *testing.T) {
	a := &app{pages: []*page{stub("", 3), stub("@作者", 3)}}
	tt := ui.NewTester(a.view, 405, 720)
	open := func() {
		a.sheet = &sheet{total: 2, done: true, items: []dy.Comment{{Author: "甲", Text: "第一条"}, {Author: "乙", Text: "第二条"}}}
		tt.Frame()
		if !tt.HasText("第一条") || !tt.HasText("2 条评论") {
			t.Fatalf("sheet shows %q", tt.Texts())
		}
	}
	check := func(how string) {
		tt.Frame()
		if a.sheet != nil || a.paused || len(a.pages) != 2 {
			t.Fatalf("after %s: sheet %v, paused %v, %d pages", how, a.sheet != nil, a.paused, len(a.pages))
		}
	}
	open()
	if err := tt.Click("✕"); err != nil {
		t.Fatal(err)
	}
	check("✕")
	open()
	tt.ClickAt(200, 100) // above the sheet
	check("a click beside")
	open()
	tt.Key(0, ui.KeyEscape)
	check("Escape")
	open()
	tt.ClickAt(200, 400) // on the sheet: nothing
	if a.sheet == nil {
		t.Fatal("a click on the sheet closed it")
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

// The download button does not pause and turns into its progress.
func TestSaveButton(t *testing.T) {
	p := stub("", 1)
	s := p.list[0]
	s.loading, s.vp = false, nil
	a := &app{pages: []*page{p}, dir: t.TempDir()}
	tt := ui.NewTester(a.view, 405, 720)
	if tt.HasText("⤓ 下载") {
		t.Fatal("download offered before the video is ready")
	}
	vp, err := ui.NewVideoPlayer(still{})
	if err != nil {
		t.Fatal(err)
	}
	s.vp = vp
	tt.Frame()
	if err := tt.Click("⤓ 下载"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.paused || s.saved != "保存中…" || !tt.HasText("保存中…") {
		t.Fatalf("after click: paused %v, saved %q", a.paused, s.saved)
	}
}

// still is a video of one black frame.
type still struct{}

func (still) Next() (ui.VideoFrame, error) {
	return ui.VideoFrame{Image: image.NewRGBA(image.Rect(0, 0, 4, 4))}, nil
}
func (still) Rewind() error { return nil }
