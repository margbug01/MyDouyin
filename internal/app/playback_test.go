package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func TestDanmakuWindowAndLoop(t *testing.T) {
	a := &app{}
	s := &slot{danmaku: []dy.Danmaku{{At: time.Second, Text: "past"}, {At: 35 * time.Second, Text: "visible"}, {At: 50 * time.Second, Text: "next"}}, dmTo: 64 * time.Second, lastAt: 60 * time.Second}
	a.loadDanmaku(s, 40*time.Second)
	if len(s.danmaku) != 2 || s.danmaku[0].Text != "visible" {
		t.Fatal("old bullet comments retained or visible ones lost")
	}
	a.watch(s, 0)
	if len(s.danmaku) != 0 || s.dmTo != 0 || s.dmGen != 1 {
		t.Fatal("loop did not restart the bounded bullet-comment window")
	}
}

func TestDanmakuReloadKeepsFlyingComments(t *testing.T) {
	a := &app{}
	for _, at := range []time.Duration{0, 7 * time.Second, 32 * time.Second, 33 * time.Second, 64 * time.Second, 65 * time.Second} {
		for _, reload := range []string{"seek", "resume"} {
			s := &slot{v: dy.Video{Duration: time.Minute * 5}, resume: at}
			if reload == "seek" {
				s.dec = &paced{}
				a.seek(s, at)
			} else {
				a.release(s)
			}
			start := max(0, at-flight)
			if s.dmTo > start || s.dmTo+32*time.Second <= start {
				t.Errorf("%s at %v: starts loading at %v, missing flying comments from %v", reload, at, s.dmTo, start)
			}
		}
	}
}

func TestDanmakuSkipsMissedWindows(t *testing.T) {
	a := &app{dmOff: true}
	s := &slot{dmTo: 32 * time.Second, dmLoading: true, v: dy.Video{Duration: time.Hour}}
	a.loadDanmaku(s, 10*time.Minute)
	if s.dmTo != 32*time.Second {
		t.Fatal("moved the window while its request was still in flight")
	}
	s.dmLoading = false
	a.loadDanmaku(s, 10*time.Minute)
	if s.dmTo != 576*time.Second {
		t.Fatalf("after leaving danmaku off: next window starts at %v, want 9m36s", s.dmTo)
	}
}

func TestSeekingDoesNotAutoAdvance(t *testing.T) {
	a := &app{autoNext: true}
	s := &slot{lastAt: 42 * time.Second, seekTarget: 42 * time.Second, seeking: true}
	if a.watch(s, time.Second) || a.watch(s, 42*time.Second) {
		t.Fatal("async seek mistaken for end of video")
	}
	if !a.watch(s, 0) {
		t.Fatal("real loop no longer advances")
	}
}

func TestPlaybackErrorCanRetry(t *testing.T) {
	p := stub("推荐", 1)
	s := p.list[0]
	s.loading = false
	s.err = errors.New("network error")
	a := &app{pages: []*page{p}, posted: make(chan func(), 1)}
	tt := newTester(a)
	if !tt.HasText("视频暂时无法播放") {
		t.Fatal("playback error invisible")
	}
	if err := tt.Click("重新加载"); err != nil {
		t.Fatal(err)
	}
	if !s.loading || s.err != nil {
		t.Fatal("retry did not clear failed state")
	}
	a.release(s)
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(time.Second):
		t.Fatal("canceled retry did not return")
	}
}

func TestNavigationKeepsPlayer(t *testing.T) {
	p := stub("推荐", 1)
	a := &app{pages: []*page{p}, paused: true}
	s := p.list[0]
	ready(t, s)
	s.dec = &paced{}
	s.dec.done.Store(int64(42 * time.Second))
	// The still decoder is closed by VideoPlayer; dec supplies only the clock.
	before := s.vp
	a.open(&page{title: "合集", grid: true, done: true})
	if s.vp != before || s.vp.Playing() || s.at() != 42*time.Second {
		t.Fatal("opening a collection lost the player or position")
	}
	a.open(&page{title: "作者", grid: true, done: true})
	a.back()
	a.back()
	if s.vp != before || s.loading || !a.paused || s.at() != 42*time.Second {
		t.Fatal("return did not preserve player, pause, and progress")
	}
	a.release(s)
	if s.resume != 42*time.Second {
		t.Fatalf("released progress = %v", s.resume)
	}
}

func TestOnlyOneHeldPlayer(t *testing.T) {
	p := stub("推荐", 3)
	a := &app{pages: []*page{p}}
	for _, s := range p.list {
		ready(t, s)
	}
	a.hold(p.list[0])
	a.hold(p.list[1])
	if p.list[0].vp != nil || a.held != p.list[1] {
		t.Fatal("more than one held player")
	}
	p.idx = 2
	a.fill()
	if p.list[0].vp != nil || p.list[1].vp == nil || p.list[2].vp == nil {
		t.Fatal("wrong playback budget")
	}
	for _, s := range p.list {
		a.release(s)
	}
}

func TestReleaseCancelsLoadingAndAllowsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &slot{loading: true, cancel: cancel, err: errors.New("failed"), loadGen: 4, dmLoading: true, danmaku: nil}
	a := &app{}
	a.release(s)
	if ctx.Err() == nil || s.loading || s.err != nil || s.loadGen != 5 || s.dmLoading {
		t.Fatal("release left stale loading/error state")
	}
	if _, err := a.build(ctx, s.v, "", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build: %v", err)
	}
}

func TestPicturesBoundDecodedSizeAndMemory(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1920, 1080))); err != nil {
		t.Fatal(err)
	}
	bmp, err := decodePicture(data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if w, h := bmp.Size(); w != 720 || h != 405 {
		t.Fatalf("cover size %dx%d", w, h)
	}
	im := newImages(nil, nil)
	im.frame = 1
	im.m["old"] = &picture{bmp: bmp, bytes: pictureBudget, used: 0}
	im.m["shown"] = &picture{bmp: bmp, bytes: 720 * 405 * 4, used: 1}
	im.bytes = pictureBudget + 720*405*4
	im.sweep()
	if im.m["old"] != nil || im.m["shown"] == nil || im.bytes != 720*405*4 {
		t.Fatal("byte budget did not evict offscreen pictures")
	}
}

func TestPicturesDoNotQueueOffscreenDownloads(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	done := make(chan func(), 6)
	im := newImages(func(string) ([]byte, error) { calls.Add(1); <-gate; return nil, errors.New("test") }, func(fn func()) { done <- fn })
	for i := 0; i < 100; i++ {
		im.get(string(rune(i + 1)))
	}
	if len(im.slots) != 6 {
		t.Fatalf("active downloads %d", len(im.slots))
	}
	close(gate)
	for range 6 {
		select {
		case fn := <-done:
			fn()
		case <-time.After(time.Second):
			t.Fatal("image load did not finish")
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("downloads queued for hidden images: %d", calls.Load())
	}
}

// Measure real decoder allocation using the same 1080p frame repeatedly.
func BenchmarkPictureDecode(b *testing.B) {
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1920, 1080)))
	for _, tc := range []struct {
		name   string
		decode func([]byte) (*ui.Bitmap, error)
	}{{"original", ui.DecodeBitmap}, {"bounded", decodePicture}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				bmp, err := tc.decode(data.Bytes())
				if err != nil {
					b.Fatal(err)
				}
				w, h := bmp.Size()
				b.ReportMetric(float64(w*h*4), "retained-pixel-bytes")
			}
		})
	}
}
