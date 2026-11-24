package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A newer version shows at the foot of the dock once a check finds it,
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
	a.checkUpdate(false)
	pump("the check, up to date")
	if tt.HasText("有新版本 v9.9.9") || !a.upd.latest || a.upd.checking {
		t.Fatalf("up to date: latest %v, checking %v", a.upd.latest, a.upd.checking)
	}
	halfway := make(chan bool)
	a.upd.check = func(context.Context) (*release, error) {
		return &release{Version: "9.9.9", install: func(_ context.Context, progress func(done, total int64)) error {
			progress(50, 100)
			<-halfway
			return errors.New("boom")
		}}, nil
	}
	a.checkUpdate(false)
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
	a.checkUpdate(false) // a check while it installs does nothing
	if a.upd.checking {
		t.Fatal("a check while installing")
	}
	close(halfway)
	pump("the failure")
	if !tt.HasText("更新没装上：boom，点击重试") || a.upd.busy {
		t.Fatalf("failed: %q", tt.Texts())
	}
}
