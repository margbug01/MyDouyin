package media

import (
	"image"
	"io"
	"os"
	"testing"
	"time"
)

// A live stream gives pictures as they come, about as often as its frame
// rate says, with something on them; closed, it gives no more. Skipped
// unless DY_TEST_HLS names a stream on air.
func TestLive(t *testing.T) {
	url := os.Getenv("DY_TEST_HLS")
	if url == "" {
		t.Skip("DY_TEST_HLS not set")
	}
	l, err := OpenLiveWithOptions(t.Context(), url, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now() // it plays, silent, from the start
	var first time.Time
	bright := 0
	const n = 60
	for i := range n {
		f, err := l.Next()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = time.Now()
			t.Logf("first picture after %v: %v", first.Sub(start).Round(time.Millisecond), f.Image.Bounds())
		}
		if i == n/2 {
			for _, p := range f.Image.(*image.RGBA).Pix {
				bright += int(p)
			}
		}
	}
	per := time.Since(first) / (n - 1)
	t.Logf("%d pictures, one every %v", n, per.Round(time.Millisecond))
	if per < 10*time.Millisecond || per > 120*time.Millisecond || bright < 1000000 {
		t.Fatalf("one every %v, brightness %d", per, bright)
	}
	// Paused for longer than the 20 s it waits for a picture, it does not give up.
	l.Pause()
	got := make(chan error, 1)
	go func() { _, err := l.Next(); got <- err }()
	select {
	case err := <-got:
		t.Fatalf("paused, Next returned %v", err)
	case <-time.After(22 * time.Second):
	}
	l.Play()
	if err := <-got; err != nil {
		t.Fatalf("played again: %v", err)
	}
	l.Close()
	if _, err := l.Next(); err != io.EOF {
		t.Fatalf("after Close: %v", err)
	}
}
