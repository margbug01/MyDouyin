package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestControlsAutoHideAndHoverReveal(t *testing.T) {
	p := stub("推荐", 2)
	p.list[0].v.Duration = 60 * time.Second
	p.list[0].v.Desc = "第一条测试视频"
	p.list[1].v.Duration = 60 * time.Second
	p.list[1].v.Desc = "第二条测试视频"
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)

	settle := func(d time.Duration) {
		tt.Advance(d)
		tt.Frame()
		tt.Advance(250 * time.Millisecond)
		tt.Frame()
	}
	assertVisible := func(ctx, desc string) {
		t.Helper()
		if _, ok := tt.Find("进度"); !ok {
			t.Fatalf("%s: controls bar should be visible", ctx)
		}
		if _, ok := tt.Find("@作者"); !ok {
			t.Fatalf("%s: author caption should be visible", ctx)
		}
		if !tt.HasText(desc) {
			t.Fatalf("%s: description %q should be visible", ctx, desc)
		}
		if _, ok := tt.Find("听抖音"); !ok {
			t.Fatalf("%s: rail should be visible", ctx)
		}
		if tt.Cursor() == ui.CursorNone {
			t.Fatalf("%s: cursor should be visible", ctx)
		}
	}
	assertHidden := func(ctx, desc string, wantCursorNone bool) {
		t.Helper()
		if _, ok := tt.Find("进度"); ok {
			t.Fatalf("%s: controls bar should be hidden", ctx)
		}
		if _, ok := tt.Find("@作者"); ok {
			t.Fatalf("%s: author caption should be hidden", ctx)
		}
		if tt.HasText(desc) {
			t.Fatalf("%s: description %q should be hidden", ctx, desc)
		}
		if _, ok := tt.Find("听抖音"); ok {
			t.Fatalf("%s: rail should be hidden", ctx)
		}
		if got := tt.Cursor(); wantCursorNone && got != ui.CursorNone {
			t.Fatalf("%s: cursor should be hidden (CursorNone), got %v", ctx, got)
		} else if !wantCursorNone && got == ui.CursorNone {
			t.Fatalf("%s: cursor should be visible outside stage", ctx)
		}
	}

	// Initially on load: controls, caption, description, rail, and cursor are visible.
	tt.Frame()
	bar, ok := tt.Find("进度")
	if !ok {
		t.Fatal("controls should show initially")
	}
	assertVisible("initial load", "第一条测试视频")

	// Idle while playing with pointer stationary over the video hides all overlays and cursor.
	videoX, videoY := bar.X+bar.W/2, bar.Y-200
	tt.Move(videoX, videoY)
	settle(controlsHideAfter + 300*time.Millisecond)
	assertHidden("idle over video", "第一条测试视频", true)

	// Moving pointer over the video reveals all overlays and restores cursor.
	tt.Move(videoX+20, videoY)
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	assertVisible("pointer move over video", "第一条测试视频")

	// Hovering over the bottom controls bar keeps HUD visible past timeout.
	tt.Move(bar.X+bar.W/2, bar.Y+bar.H/2)
	settle(controlsHideAfter + 500*time.Millisecond)
	assertVisible("hovering controls bar", "第一条测试视频")

	// Hovering over the right action rail keeps HUD visible past timeout.
	railBtn, ok := tt.Find("听抖音")
	if !ok {
		t.Fatal("expected 听抖音 rail button")
	}
	tt.Move(railBtn.X+railBtn.W/2, railBtn.Y+railBtn.H/2)
	settle(controlsHideAfter + 500*time.Millisecond)
	assertVisible("hovering rail", "第一条测试视频")

	// Hovering over the author caption button keeps HUD visible past timeout.
	authorBtn, ok := tt.Find("@作者")
	if !ok {
		t.Fatal("expected @作者 caption button")
	}
	tt.Move(authorBtn.X+authorBtn.W/2, authorBtn.Y+authorBtn.H/2)
	settle(controlsHideAfter + 500*time.Millisecond)
	assertVisible("hovering author button", "第一条测试视频")

	// Opening the share panel pins HUD visible past timeout even with pointer over the video.
	if err := tt.Click("分享"); err != nil {
		t.Fatalf("click 分享: %v", err)
	}
	tt.Frame()
	tt.Move(videoX, videoY)
	settle(controlsHideAfter + 500*time.Millisecond)
	assertVisible("share panel open", "第一条测试视频")
	a.shareOpen = false
	tt.Frame()

	// Moving pointer off the stage hides overlays and keeps cursor visible.
	tt.Move(10, 10)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	assertHidden("pointer off stage", "第一条测试视频", false)

	// Pausing pins HUD visible and restores cursor even after idling over the video.
	tt.Move(videoX, videoY)
	settle(controlsHideAfter + 300*time.Millisecond)
	assertHidden("idle before pause", "第一条测试视频", true)
	a.paused = true
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	assertVisible("paused", "第一条测试视频")

	// Switching videos reveals HUD for the timeout window, then auto-hides.
	a.paused = false
	a.step(1)
	tt.Frame()
	assertVisible("after video switch", "第二条测试视频")
	settle(controlsHideAfter + 300*time.Millisecond)
	assertHidden("idle after video switch", "第二条测试视频", true)

	// With 清屏 enabled, pointer movement reveals controls and cursor while caption and rail stay hidden.
	a.clear = true
	tt.Move(videoX+10, videoY)
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("进度"); !ok {
		t.Fatal("清屏: moving pointer should reveal controls")
	}
	if tt.Cursor() == ui.CursorNone {
		t.Fatal("清屏: moving pointer should reveal cursor")
	}
	if _, ok := tt.Find("@作者"); ok {
		t.Fatal("清屏: author caption should stay hidden")
	}
	if tt.HasText("第二条测试视频") {
		t.Fatal("清屏: description should stay hidden")
	}
	if _, ok := tt.Find("听抖音"); ok {
		t.Fatal("清屏: rail should stay hidden")
	}
	settle(controlsHideAfter + 300*time.Millisecond)
	assertHidden("清屏 idle", "第二条测试视频", true)
}
