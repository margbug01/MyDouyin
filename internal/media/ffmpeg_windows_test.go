package media

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testClip(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "中文 音画.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd, err := command(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "2", "-c:v", "mpeg4", "-c:a", "aac", "-y", path)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v %s", err, data)
	}
	return path
}

func TestWindowsPlaybackThroughHTTPProxy(t *testing.T) {
	path := testClip(t)
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.ServeFile(w, r, path)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	l, err := OpenVideo("http://mydouyin.test/clip.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if frame, err := l.Next(); err != nil || frame.Image == nil {
		t.Fatalf("proxied picture: %v", err)
	}
	if requests.Load() < 2 {
		t.Fatal("probe and decoder did not use the proxy")
	}
}

func TestWindowsHLS(t *testing.T) {
	path := testClip(t)
	playlist := filepath.Join(filepath.Dir(path), "live.m3u8")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd, err := command(ctx, "ffmpeg", "-nostdin", "-v", "error", "-i", path, "-c:v", "libx264", "-preset", "ultrafast", "-g", "15", "-c:a", "aac", "-hls_time", "0.5", "-hls_playlist_type", "vod", playlist)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("HLS fixture: %v %s", err, data)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(path))))
	defer srv.Close()
	l, err := OpenLive(srv.URL + "/live.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.file.Duration != 0 {
		t.Fatal("live playback was clamped to the playlist window")
	}
	if frame, err := l.Next(); err != nil || frame.Image == nil {
		t.Fatalf("HLS picture: %v", err)
	}
}

func TestWindowsFileDecoders(t *testing.T) {
	f, err := Open(testClip(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.W != 160 || f.H != 90 || f.Duration < 1900*time.Millisecond {
		t.Fatalf("metadata: %+v", f)
	}
	v, _ := f.Video()
	defer v.Close()
	first, err := v.Next()
	if err != nil || first.Image == nil {
		t.Fatalf("first frame: %v", err)
	}
	if err := v.RewindTo(time.Second); err != nil {
		t.Fatal(err)
	}
	second, err := v.Next()
	if err != nil || second.Image == nil {
		t.Fatalf("seek: %v", err)
	}
	a, err := f.Audio()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	buf := make([]float32, 9600)
	n, err := a.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("audio: n=%d err=%v", n, err)
	}
	var energy float64
	for _, x := range buf[:n] {
		energy += math.Abs(float64(x))
	}
	if energy < 1 {
		t.Fatalf("silent decoded audio: %v", energy)
	}
	if err := a.SetPosition(48000); err != nil {
		t.Fatal(err)
	}
	if n, err := a.Read(buf); err != nil || n == 0 {
		t.Fatalf("audio seek: %d %v", n, err)
	}
}

func TestWindowsStreamControlsAndClose(t *testing.T) {
	l, err := OpenVideo(testClip(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.Next(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); l.Position() <= 0; {
		if time.Now().After(deadline) {
			t.Fatal("playback clock did not advance")
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.Pause()
	at := l.Position()
	time.Sleep(80 * time.Millisecond)
	if l.Position() != at {
		t.Fatal("paused clock moved")
	}
	l.Seek(time.Second)
	if _, err := l.Next(); err != nil {
		t.Fatal(err)
	}
	if l.Seeking() || l.Position() != time.Second {
		t.Fatalf("paused seek: %v %v", l.Seeking(), l.Position())
	}
	l.SetSpeed(2)
	l.SetMuted(false)
	l.SetVolume(0.25)
	l.Play()
	for deadline := time.Now().Add(3 * time.Second); ; {
		_, err := l.Frame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reach EOF: %v", l.Position())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := l.Rewind(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Next(); err != nil {
		t.Fatal(err)
	}
	if l.Position() > 200*time.Millisecond {
		t.Fatalf("rewind: %v", l.Position())
	}
	if _, err := l.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Frame(); !errors.Is(err, io.EOF) {
		t.Fatalf("closed frame: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsHTTPHeadersAndCancellation(t *testing.T) {
	path := testClip(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://www.douyin.com/" {
			http.Error(w, "missing referer", http.StatusForbidden)
			return
		}
		requests.Add(1)
		http.ServeFile(w, r, path)
	}))
	defer srv.Close()
	l, err := OpenVideo(srv.URL+"/clip.mp4", map[string]string{"Referer": "https://www.douyin.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Next(); err != nil {
		l.Close()
		t.Fatal(err)
	}
	l.Close()
	if requests.Load() < 2 {
		t.Fatalf("stream did not request video after probing: %d", requests.Load())
	}
	if _, err := OpenVideo(srv.URL, map[string]string{"Referer": "x\r\nInjected: value"}); err == nil {
		t.Fatal("accepted header injection")
	}

	// Close must interrupt a decoder blocked in network I/O, rather than wait
	// for FFmpeg's network timeout or leave a child process behind.
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer blocked.Close()
	f := &File{source: blocked.URL, W: 160, H: 90, fps: 30}
	v, _ := f.Video()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = v.Next() }()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	v.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close left decoder blocked")
	}
	if time.Since(start) > time.Second {
		t.Fatal("Close waited for network timeout")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
