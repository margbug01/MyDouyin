package app

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"douyin/internal/dy"
	"douyin/internal/media"
)

func TestStreamPlaybackRoundTrip(t *testing.T) {
	path := os.Getenv("DY_TEST_MP4")
	if path == "" {
		path = "../../testdata/silent.mp4"
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := media.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	duration := f.Duration
	f.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, path) }))
	defer srv.Close()
	s := &slot{v: dy.Video{ID: "test", Author: "播放回归测试", PlayURL: srv.URL + "/video.mp4", Duration: duration}}
	p := &page{title: "推荐", done: true, list: []*slot{s}}
	a := &app{pages: []*page{p}, feed: p, posted: make(chan func(), 100), speed: 1, dmOff: true}
	a.setSpeed(1)
	a.setVolume(0, true)
	defer func() { a.release(s) }()
	tt := newTester(a)
	pump := func() {
		for {
			select {
			case fn := <-a.posted:
				fn()
			default:
				tt.Advance(10 * time.Millisecond)
				tt.Frame()
				return
			}
		}
	}
	wait := func(what string, done func() bool) {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for !done() && time.Now().Before(end) {
			pump()
			time.Sleep(10 * time.Millisecond)
		}
		if !done() {
			t.Fatalf("%s: error=%v position=%v loading=%v", what, s.err, s.at(), s.loading)
		}
	}
	a.fill()
	wait("first frame", func() bool { return s.vp != nil })
	target := min(duration/3, 42*time.Second)
	a.seek(s, target)
	wait("seek", func() bool { return s.at() >= target-50*time.Millisecond })
	for range 15 {
		pump()
		time.Sleep(10 * time.Millisecond)
	}
	a.paused = true
	a.sync()
	before, at := s.vp, s.at()
	for range 20 {
		a.open(&page{title: "合集", grid: true, done: true})
		pump()
		if s.vp != before || s.vp.Playing() {
			t.Fatal("collection discarded or played hidden video")
		}
		a.back()
		pump()
		if s.vp != before || !a.paused || s.loading {
			t.Fatal("return reloaded or lost pause")
		}
	}
	if !tt.HasText(clock(at) + " / " + clock(duration)) {
		t.Fatalf("rendered position missing: %v", tt.Texts())
	}
	if d := s.at() - at; d > 100*time.Millisecond || d < -100*time.Millisecond {
		t.Fatalf("round trip moved progress by %v", d)
	}
	if dir := os.Getenv("DY_SHOT"); dir != "" {
		out, err := os.Create(filepath.Join(dir, "playback-roundtrip.png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(out, tt.Image())
		out.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	// The short fixture can loop before pause under -race; retain the actual paused position.
	other := &page{title: "收藏", grid: true, done: true}
	a.root(other)
	if s.vp != nil || s.resume < at-100*time.Millisecond || s.resume > at+100*time.Millisecond {
		t.Fatalf("released progress %v", s.resume)
	}
	a.root(p)
	wait("reopen at saved position", func() bool {
		return s.vp != nil && s.at() >= at-100*time.Millisecond && s.at() <= at+100*time.Millisecond
	})
	if !a.paused {
		t.Fatal("root lost pause state")
	}
	// A hidden/paused Next must also close promptly during replacement.
	a.setQuality("高清")
	wait("quality switch", func() bool {
		return s.vp != nil && s.at() >= at-100*time.Millisecond && s.at() <= at+100*time.Millisecond
	})
	t.Logf("20 collection round trips retained the player at %v; root and quality changes resumed at %v", at.Round(time.Millisecond), s.at().Round(time.Millisecond))
	if os.Getenv("DY_STRESS") != "" {
		for i := 0; i < 30; i++ {
			a.root(other)
			a.root(p)
			wait("stress reopen", func() bool { return s.vp != nil })
			a.paused = false
			a.sync()
			for range 10 {
				pump()
				time.Sleep(10 * time.Millisecond)
			}
			if i%10 == 9 {
				runtime.GC()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				t.Logf("reopen %d: heap=%.1f MiB goroutines=%d", i+1, float64(m.HeapAlloc)/(1<<20), runtime.NumGoroutine())
			}
		}
	}
}
