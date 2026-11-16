package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"douyin/internal/dy"
	"douyin/internal/media"
)

// Opt-in comparison with the previous implementation: identical local video,
// four feed entries, actual decoding and rendering, and the same navigation.
func TestPlaybackMemory(t *testing.T) {
	path := os.Getenv("DY_MEMORY_MP4")
	if path == "" {
		t.Skip("DY_MEMORY_MP4 not set")
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
	c, err := dy.New()
	if err != nil {
		t.Fatal(err)
	}
	p := &page{title: "推荐", done: true, idx: 1}
	for i := range 4 {
		p.list = append(p.list, &slot{v: dy.Video{ID: strconv.Itoa(i), Author: "性能测试", PlayURL: srv.URL + "/v.mp4", Duration: duration}})
	}
	a := &app{c: c, pages: []*page{p}, feed: p, dir: t.TempDir(), posted: make(chan func(), 100), dmOff: true}
	a.setSpeed(1)
	a.setVolume(0, true)
	defer func() {
		for _, s := range p.list {
			a.release(s)
		}
	}()
	tt := newTester(a)
	pump := func() {
		for {
			select {
			case fn := <-a.posted:
				fn()
			default:
				tt.Advance(16 * time.Millisecond)
				tt.Frame()
				return
			}
		}
	}
	wait := func() {
		t.Helper()
		end := time.Now().Add(20 * time.Second)
		for p.list[1].vp == nil && time.Now().Before(end) {
			pump()
			time.Sleep(10 * time.Millisecond)
		}
		if p.list[1].vp == nil {
			t.Fatalf("load failed: %v", p.list[1].err)
		}
	}
	sample := func(label string) {
		runtime.GC()
		out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			t.Fatal(err)
		}
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		n := 0
		for _, s := range p.list {
			if s.vp != nil {
				n++
			}
		}
		t.Logf("%s: RSS=%s KiB heap=%.1f MiB players=%d goroutines=%d", label, strings.TrimSpace(string(out)), float64(m.HeapAlloc)/(1<<20), n, runtime.NumGoroutine())
	}
	start := time.Now()
	a.fill()
	wait()
	t.Logf("first frame=%v", time.Since(start).Round(time.Millisecond))
	for range 180 {
		pump()
		time.Sleep(16 * time.Millisecond)
	}
	sample("playing")
	s := p.list[1]
	a.seek(s, 42*time.Second)
	for range 30 {
		pump()
		time.Sleep(16 * time.Millisecond)
	}
	a.open(&page{title: "合集", grid: true, done: true})
	pump()
	start = time.Now()
	a.back()
	wait()
	ready := time.Since(start)
	for range 30 {
		pump()
		time.Sleep(16 * time.Millisecond)
	}
	t.Logf("return ready=%v progress=%v", ready.Round(time.Millisecond), s.at().Round(time.Millisecond))
	sample("returned")
	for round := range 3 {
		for range 300 {
			pump()
			time.Sleep(16 * time.Millisecond)
		}
		sample("continuous " + strconv.Itoa(round+1))
	}
}
