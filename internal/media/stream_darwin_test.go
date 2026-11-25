package media

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func TestTimeConversion(t *testing.T) {
	for _, want := range []time.Duration{0, 42*time.Second + 123*time.Millisecond, 4 * time.Hour} {
		got := (cmTime{Value: int64(want), Timescale: int32(time.Second), Flags: 1}).dur()
		if got != want {
			t.Fatalf("time conversion: %v != %v", got, want)
		}
	}
	if (cmTime{}).dur() != 0 {
		t.Fatal("invalid time")
	}
}

func TestVideoStream(t *testing.T) {
	path, err := filepath.Abs("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Referer") != "https://www.douyin.com/" {
			t.Error("missing referer")
		}
		http.ServeFile(w, r, path)
	}))
	defer srv.Close()
	l, err := OpenVideo(t.Context(), srv.URL+"/v.mp4", map[string]string{"Referer": "https://www.douyin.com/"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	first, err := l.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.Image.Bounds().Empty() || requests.Load() == 0 {
		t.Fatal("no streamed picture")
	}
	l.Pause()
	at := l.Position()
	time.Sleep(150 * time.Millisecond)
	if d := l.Position() - at; d > 50*time.Millisecond {
		t.Fatalf("paused moved by %v", d)
	}
	l.Seek(500 * time.Millisecond)
	l.SetSpeed(1.5)
	l.Play()
	if _, err := l.Next(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for l.Position() < 450*time.Millisecond && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if l.Position() < 450*time.Millisecond {
		t.Fatal("seek lost")
	}
	if err := l.Rewind(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Next(); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if _, err := l.Next(); err != io.EOF {
		t.Fatalf("closed Next: %v", err)
	}
}

// A streamed video is kept in memory, not in CoreMedia's disk cache, which
// lists each URL it holds in $TMPDIR/MediaCache/diskcacherepository.plist.
func TestVideoStreamSkipsDiskCache(t *testing.T) {
	path, err := filepath.Abs("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, path) }))
	defer srv.Close()
	l, err := OpenVideo(t.Context(), srv.URL+"/disk.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Next(); err != nil {
		t.Fatal(err)
	}
	l.Close()
	repo, _ := os.ReadFile(filepath.Join(os.TempDir(), "MediaCache", "diskcacherepository.plist"))
	if bytes.Contains(repo, []byte(srv.URL)) {
		t.Fatal("the video went into CoreMedia's disk cache")
	}
}

func TestPreloadedAssetOpensWithoutPixelOutput(t *testing.T) {
	path, err := filepath.Abs("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	requested := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requested <- struct{}{}:
		default:
		}
		if r.Header.Get("Referer") != "https://www.douyin.com/" {
			t.Error("preload lost its playback headers")
		}
		http.ServeFile(w, r, path)
	}))
	defer srv.Close()
	warm := PreloadVideo(srv.URL+"/clip.mp4", map[string]string{"Referer": "https://www.douyin.com/"})
	if warm == nil {
		t.Fatal("could not preload the video")
	}
	defer warm.Close()
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("preload did not start before opening the player")
	}
	live, err := warm.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	warm.Close() // promotion transfers ownership to the player
	if w, h, err := live.WaitReady(); err != nil || w != 160 || h != 90 || live.out != 0 {
		t.Fatalf("preloaded player: %dx%d, pixel output %v, error %v", w, h, live.out != 0, err)
	}
}

func TestDiscardedPreloadCancelsRequest(t *testing.T) {
	entered, canceled, finish := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			select {
			case canceled <- struct{}{}:
			default:
			}
		case <-finish:
		}
	}))
	defer srv.Close()
	defer close(finish)
	warm := PreloadVideo(srv.URL+"/clip.mp4", nil)
	if warm == nil {
		t.Fatal("could not preload the video")
	}
	defer warm.Close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preload did not make a request")
	}
	warm.Close()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("discarded preload left an active request")
	}
	if live, err := warm.Open(); err == nil || live != nil {
		t.Fatal("discarded preload could still open a player")
	}
}

func TestVideoLoopsAndRecoversAfterHiddenGap(t *testing.T) {
	path, _ := filepath.Abs("../../testdata/silent.mp4")
	l, err := OpenVideo(t.Context(), "file://"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for loop := range 2 {
		n := 0
		for {
			_, err := l.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("loop=%d frames=%d at=%v pendingSeek=%v: %v", loop, n, l.Position(), l.seekDone != nil, err)
			}
			n++
		}
		if n < 2 {
			t.Fatalf("loop %d only produced %d frames", loop, n)
		}
		if err := l.Rewind(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Next(); err != nil {
		t.Fatal(err)
	}
	l.Pause()
	time.Sleep(300 * time.Millisecond)
	l.Play()
	frame, err := l.Next()
	if err != nil {
		t.Fatal(err)
	}
	if frame.Duration > 100*time.Millisecond {
		t.Fatalf("hidden gap became a frozen frame: %v", frame.Duration)
	}
}

func TestCloseWaitingStream(t *testing.T) {
	entered := make(chan struct{}, 1)
	finish := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-finish:
		}
	}))
	defer srv.Close()
	defer close(finish)
	l, err := OpenVideo(t.Context(), srv.URL+"/v.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := l.Next(); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no request")
	}
	l.Close()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt Next")
	}
}

type meteredResponse struct {
	http.ResponseWriter
	bytes *atomic.Int64
}

func (w meteredResponse) Write(p []byte) (int, error) {
	time.Sleep(10 * time.Millisecond) // about 3 MiB/s with ServeFile's 32 KiB writes
	n, err := w.ResponseWriter.Write(p)
	w.bytes.Add(int64(n))
	return n, err
}

func TestVideoStartsBeforeFullTransfer(t *testing.T) {
	path := os.Getenv("DY_TEST_MP4")
	if path == "" {
		t.Skip("DY_TEST_MP4 not set")
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() < 1<<20 {
		t.Skip("needs a video larger than 1 MiB")
	}
	var transferred atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(meteredResponse{w, &transferred}, r, path)
	}))
	defer srv.Close()
	start := time.Now()
	l, err := OpenVideo(t.Context(), srv.URL+"/v.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.Next(); err != nil {
		t.Fatal(err)
	}
	n := transferred.Load()
	t.Logf("first frame=%v, transferred=%d/%d bytes (%.1f%%)", time.Since(start).Round(time.Millisecond), n, stat.Size(), float64(n)*100/float64(stat.Size()))
	if n >= stat.Size()/2 {
		t.Fatal("first frame waited for too much of the video")
	}
	l.Close()
	srv.CloseClientConnections()
}

// Opt-in: reads a public recommendation, compares full download with AVPlayer
// time to first picture, and leaves a reusable fixture in DY_PERF_DIR.
func TestCDNStart(t *testing.T) {
	dir := os.Getenv("DY_PERF_DIR")
	if dir == "" {
		t.Skip("DY_PERF_DIR not set")
	}
	c, err := dy.New()
	if err != nil {
		t.Fatal(err)
	}
	vs, err := c.Picks(0)()
	if err != nil {
		t.Fatal(err)
	}
	var v dy.Video
	for _, candidate := range vs {
		if candidate.Live == nil && candidate.PlayURL != "" {
			v = candidate
			break
		}
	}
	if v.PlayURL == "" {
		t.Fatal("no video")
	}
	start := time.Now()
	l, err := OpenVideo(t.Context(), v.PlayURL, map[string]string{"Referer": "https://www.douyin.com/", "User-Agent": "Mozilla/5.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	vp, err := ui.NewVideoPlayer(l)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)
	w, h := vp.Size()
	l.Close()
	vp.Close()
	start = time.Now()
	dst := filepath.Join(dir, "cdn.mp4")
	if err := c.Download(v.PlayURL, dst); err != nil {
		t.Fatal(err)
	}
	full := time.Since(start)
	stat, _ := os.Stat(dst)
	t.Logf("%dx%d, duration=%v, bytes=%d, streamed first frame=%v, full download=%v", w, h, v.Duration, stat.Size(), first.Round(time.Millisecond), full.Round(time.Millisecond))
}
