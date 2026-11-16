package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A newer version shows at the foot of the sidebar once a check finds it,
// and nothing shows before; a click downloads it, its progress shown, and
// a failed install says why and can be tried again.
func TestUpdateBanner(t *testing.T) {
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
	a.checkUpdate()
	tt.Frame()
	select {
	case <-a.posted:
		t.Fatal("up to date, yet something was posted")
	case <-time.After(100 * time.Millisecond):
	}
	if tt.HasText("有新版本 v9.9.9") {
		t.Fatal("a banner while up to date")
	}
	halfway := make(chan bool)
	a.upd.check = func(context.Context) (*release, error) {
		return &release{Version: "9.9.9", install: func(_ context.Context, progress func(done, total int64)) error {
			progress(50, 100)
			<-halfway
			return errors.New("boom")
		}}, nil
	}
	a.checkUpdate()
	pump("the check")
	if !tt.HasText("有新版本 v9.9.9") || !tt.HasText("点击更新并重启") {
		t.Fatalf("found: %q", tt.Texts())
	}
	if err := tt.Click("有新版本 v9.9.9"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	pump("the progress")
	if !tt.HasText("正在下载更新") || !tt.HasText("50%") {
		t.Fatalf("downloading: %q", tt.Texts())
	}
	a.checkUpdate() // a check while it installs changes nothing
	close(halfway)
	pump("the failure")
	if !tt.HasText("更新没装上：boom，点击重试") || a.upd.busy {
		t.Fatalf("failed: %q", tt.Texts())
	}
}
