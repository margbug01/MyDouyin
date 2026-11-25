//go:build windows

package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsMFOpenAndDecodeSilentMP4(t *testing.T) {
	path, err := filepath.Abs("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.W <= 0 || f.H <= 0 || f.Duration < time.Second {
		t.Fatalf("unexpected metadata: %+v", f)
	}
	if _, err := f.Audio(); err == nil {
		t.Fatal("silent.mp4 should not report an audio track")
	}
	vd, err := f.Video()
	if err != nil {
		t.Fatal(err)
	}
	defer vd.Close()
	first, err := vd.Next()
	if err != nil || first.Image == nil {
		t.Fatalf("first frame: %v", err)
	}
	if err := vd.RewindTo(time.Second); err != nil {
		t.Fatal(err)
	}
	second, err := vd.Next()
	if err != nil || second.Image == nil {
		t.Fatalf("post-seek frame: %v", err)
	}
}

func TestWindowsMFLiveStreamWithHeaders(t *testing.T) {
	path, err := filepath.Abs("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://www.douyin.com/" {
			http.Error(w, "missing referer", http.StatusForbidden)
			return
		}
		http.ServeFile(w, r, path)
	}))
	defer srv.Close()

	if _, err := OpenVideo(t.Context(), srv.URL+"/clip.mp4", map[string]string{"Referer": "x\r\nInjected: 1"}); err == nil {
		t.Fatal("accepted header injection")
	}
	l, err := OpenVideo(t.Context(), srv.URL+"/clip.mp4", map[string]string{"Referer": "https://www.douyin.com/"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.Player() == nil {
		t.Fatal("expected mygo/ui/video engine player on Windows")
	}
	l.SetSpeed(1.5)
	l.SetMuted(true)
	l.SetVolume(0.5)
	l.Play()
	l.Pause()
	l.Seek(500 * time.Millisecond)
	if !l.Seeking() {
		t.Fatal("expected seeking state right after Seek")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Frame(); err != io.EOF {
		t.Fatalf("expected EOF after close, got %v", err)
	}
}

// A cancel closes the stream from a goroutine of its own while the one that
// opened it may still set it up: what it calls after Close does nothing.
func TestWindowsLiveCallsAfterCloseDoNothing(t *testing.T) {
	path, err := filepath.Abs("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	l, err := OpenVideo(t.Context(), "file://"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	l.SetLoop(true)
	l.SetSpeed(1.5)
	l.SetMuted(false)
	l.SetVolume(0.5)
	l.Seek(time.Second)
	l.Play()
	l.Pause()
	l.Sync()
	if l.Player() != nil || l.Position() != 0 || l.UntilEnd() != time.Hour {
		t.Fatal("a closed stream still answers")
	}
}

// Opening waits for the stream's first answer only as long as ctx lets it.
func TestWindowsOpenVideoStopsWithItsContext(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer stalled.Close()
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, err := OpenVideo(ctx, stalled.URL+"/hang.mp4", nil); err == nil {
		t.Fatal("opened a stream that never answered")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("OpenVideo returned %v after its cancel", took)
	}
}
