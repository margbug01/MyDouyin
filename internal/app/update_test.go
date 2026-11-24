package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A newer version shows as a compact pill in the top-right bar (never over the
// video controls), opens a release-notes dialog on click, tracks download
// progress in both dialog and pill, allows retry on failure, and can be
// dismissed with 稍后再说.
func TestUpdatePillAndDialog(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}, posted: make(chan func(), 8)})
	tt := newTester(a)
	pump := func(what string) {
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: nothing came back", what)
		}
		tt.Frame()
	}

	a.upd.check = func(context.Context) (*release, error) { return nil, nil }
	a.checkUpdate(false)
	pump("the check, up to date")
	if tt.HasText("新版本 v9.9.9") || !a.upd.latest || a.upd.checking {
		t.Fatalf("up to date: latest %v, checking %v", a.upd.latest, a.upd.checking)
	}

	halfway := make(chan bool)
	a.upd.check = func(context.Context) (*release, error) {
		return &release{
			Version: "9.9.9",
			Notes:   "- 评论图片支持点击查看大图\n- 评论区底部新增发评论、表情和图片",
			install: func(_ context.Context, progress func(done, total int64)) error {
				progress(50, 100)
				<-halfway
				return errors.New("boom")
			},
		}, nil
	}
	a.checkUpdate(false)
	pump("the check")

	pillRect, ok := tt.Find("有新版本 v9.9.9")
	if !ok {
		t.Fatalf("missing top-right update pill: %q", tt.Texts())
	}
	if pillRect.Y+pillRect.H > topBarHeight {
		t.Fatalf("update pill must stay in top bar (<= %d), got %+v", topBarHeight, pillRect)
	}
	if searchRect, ok := tt.Find("搜索"); ok && searchRect.X+searchRect.W > pillRect.X {
		t.Fatalf("update pill overlaps search box: search=%+v pill=%+v", searchRect, pillRect)
	}

	if err := tt.Click("有新版本 v9.9.9"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.upd.dialogOpen || !a.mainVideo.off {
		t.Fatalf("dialog did not open or hide native video: open=%v videoOff=%v", a.upd.dialogOpen, a.mainVideo.off)
	}
	if !tt.HasText("发现新版本 v9.9.9") || !tt.HasText("评论图片支持点击查看大图") {
		t.Fatalf("dialog missing version or notes: %q", tt.Texts())
	}

	if err := tt.Click("更新并重启"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	pump("the progress")
	if !tt.HasText("正在下载更新…") || !tt.HasText("50%") || !tt.HasText("下载 50%") {
		t.Fatalf("downloading progress missing: %q", tt.Texts())
	}
	a.checkUpdate(false) // a check while it installs does nothing
	if a.upd.checking {
		t.Fatal("a check while installing")
	}

	close(halfway)
	pump("the failure")
	if !tt.HasText("更新没装上：boom") || !tt.HasText("重试更新") || a.upd.busy {
		t.Fatalf("failed state wrong: busy=%v texts=%q", a.upd.busy, tt.Texts())
	}

	if err := tt.Click("稍后再说"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.upd.dialogOpen || !a.upd.dismissed {
		t.Fatalf("later did not dismiss dialog: open=%v dismissed=%v", a.upd.dialogOpen, a.upd.dismissed)
	}
	// Clear error to confirm a dismissed clean update hides the top-right pill.
	a.upd.err = ""
	tt.Frame()
	if _, ok := tt.Find("有新版本 v9.9.9"); ok {
		t.Fatal("dismissed update pill should hide until next check")
	}
}
