package app

import (
	"testing"
	"time"
)

func TestControlsAutoHideAndHoverReveal(t *testing.T) {
	p := stub("推荐", 2)
	p.list[0].v.Duration = 60 * time.Second
	p.list[1].v.Duration = 60 * time.Second
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)

	// Visible on initial video load.
	tt.Frame()
	bar, ok := tt.Find("进度")
	if !ok || a.controlsAlpha < 0.99 {
		t.Fatalf("controls should show initially: ok=%v alpha=%v", ok, a.controlsAlpha)
	}

	// After idle timeout while playing, controls fade out completely.
	tt.Advance(controlsHideAfter + 300*time.Millisecond)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("进度"); ok || a.controlsAlpha > 0.01 {
		t.Fatalf("controls did not auto-hide when idle: ok=%v alpha=%v", ok, a.controlsAlpha)
	}

	// Moving the pointer onto the video reveals controls immediately.
	tt.Move(bar.X+bar.W/2, bar.Y-120)
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("进度"); !ok || a.controlsAlpha < 0.99 {
		t.Fatalf("pointer move over video did not reveal controls: ok=%v alpha=%v", ok, a.controlsAlpha)
	}

	// Hovering over the bottom controls bar keeps them visible even past timeout.
	tt.Move(bar.X+bar.W/2, bar.Y+bar.H/2)
	tt.Advance(controlsHideAfter + 500*time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("进度"); !ok || a.controlsAlpha < 0.99 {
		t.Fatalf("hovering controls bar should keep it visible: ok=%v alpha=%v", ok, a.controlsAlpha)
	}

	// Moving pointer off the video stage hides controls smoothly.
	tt.Move(10, 10)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("进度"); ok || a.controlsAlpha > 0.01 {
		t.Fatalf("leaving video stage did not hide controls: ok=%v alpha=%v", ok, a.controlsAlpha)
	}

	// Pausing pins controls visible even with pointer outside the stage.
	a.paused = true
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("进度"); !ok || a.controlsAlpha < 0.99 {
		t.Fatalf("pausing should keep controls visible: ok=%v alpha=%v", ok, a.controlsAlpha)
	}

	// Resuming and stepping to the next video briefly reveals controls, then auto-hides.
	a.paused = false
	a.step(1)
	tt.Frame()
	if _, ok := tt.Find("进度"); !ok {
		t.Fatal("switching videos should reveal controls")
	}
	tt.Advance(controlsHideAfter + 300*time.Millisecond)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("进度"); ok {
		t.Fatal("controls did not auto-hide after switching videos")
	}
}
