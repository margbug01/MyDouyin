package app

import (
	"path/filepath"
	"testing"
	"time"

	"douyin/internal/media"
	"github.com/egoist/mygo/ui"
)

func TestStreamPlayerDrawsAndLoops(t *testing.T) {
	path, err := filepath.Abs("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	live, err := media.OpenVideo("file://"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	p, err := newStreamPlayer(live, true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	tt := ui.NewTester(func(c *ui.Context) { video(c, p).Fill() }, 160, 90)
	tt.Frame()
	first := tt.Image().RGBAAt(80, 45)
	bitmap := p.bitmap
	changed, looped := false, false
	previous := live.Position()
	end := time.Now().Add(4 * time.Second)
	for time.Now().Before(end) && (!changed || !looped) {
		time.Sleep(16 * time.Millisecond)
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		at := live.Position()
		changed = changed || tt.Image().RGBAAt(80, 45) != first
		looped = looped || at+500*time.Millisecond < previous
		previous = at
		if p.Err() != nil {
			t.Fatal(p.Err())
		}
		if p.bitmap != bitmap {
			t.Fatal("created a new bitmap while playing")
		}
	}
	if !changed || !looped {
		t.Fatalf("picture changed=%v looped=%v", changed, looped)
	}
	pl := players{vp: p, live: live}
	pl.pause()
	tt.Frame()
	frozen := tt.Image().RGBAAt(80, 45)
	// Looping seeks and their last decoded picture complete asynchronously after Pause.
	stableSince := time.Now()
	end = time.Now().Add(time.Second)
	for time.Now().Before(end) && (live.Seeking() || time.Since(stableSince) < 100*time.Millisecond) {
		time.Sleep(10 * time.Millisecond)
		tt.Advance(10 * time.Millisecond)
		tt.Frame()
		if picture := tt.Image().RGBAAt(80, 45); picture != frozen || live.Seeking() {
			frozen, stableSince = picture, time.Now()
		}
	}
	if live.Seeking() || time.Since(stableSince) < 100*time.Millisecond {
		t.Fatal("paused picture did not settle after the loop")
	}
	time.Sleep(100 * time.Millisecond)
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if p.Playing() || tt.Image().RGBAAt(80, 45) != frozen {
		t.Fatal("paused picture kept moving")
	}
	pl.play(false)
	if !p.Playing() {
		t.Fatal("player did not resume")
	}
}
