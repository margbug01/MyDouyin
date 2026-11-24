package app

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
)

// A picture loads once however often it is asked for, a failed one is not
// asked for again, and past keepPictures those not shown in a frame go.
func TestImages(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	var fetches atomic.Int32
	done := make(chan func(), 1000)
	im := newImages(func(url string) ([]byte, error) {
		fetches.Add(1)
		if url == "bad" {
			return nil, errors.New("404")
		}
		return buf.Bytes(), nil
	}, func(fn func()) { done <- fn })
	settle := func(n int) {
		for range n {
			(<-done)()
		}
	}

	if im.get("a") != nil || im.get("a") != nil || im.get("bad") != nil {
		t.Fatal("a picture before it loaded")
	}
	settle(2)
	if im.get("a") == nil || im.get("bad") != nil {
		t.Fatal("after loading: a missing, or bad there")
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("%d fetches for a, a, bad, a, bad", n)
	}

	im.sweep() // few pictures: all stay
	for i := range keepPictures + 10 {
		im.get(fmt.Sprint("k", i))
		settle(1)
	}
	im.sweep()  // the frame that showed them all
	im.get("a") // the next frame shows only a
	im.sweep()
	if len(im.m) != 1 || im.m["a"] == nil {
		t.Fatalf("after sweeping %d pictures", len(im.m))
	}
	var none *images
	if none.get("a") != nil {
		t.Fatal("a nil images had a picture")
	}
	none.sweep()
}

func TestImagesSizedAndReleasePixels(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 800, 800)))
	done := make(chan func(), 4)
	im := newImages(func(string) ([]byte, error) { return buf.Bytes(), nil }, func(fn func()) { done <- fn })
	im.releasePixels = true

	if im.getSized("av", avatarEdge) != nil {
		t.Fatal("returned picture before load")
	}
	(<-done)()
	im.sweep() // frame 0 -> 1
	bmp := im.getSized("av", avatarEdge)
	if bmp == nil {
		t.Fatal("missing loaded avatar")
	}
	if w, h := bmp.Size(); w != avatarEdge || h != avatarEdge {
		t.Fatalf("avatar scaled to %dx%d, want %dx%d", w, h, avatarEdge, avatarEdge)
	}
	im.sweep() // frame 1 -> 2 (drawn=2; GPU paints frame 1 after sweep)
	if im.m["av"].released {
		t.Fatal("released pixels before GPU upload frame")
	}
	im.getSized("av", avatarEdge)
	im.sweep() // frame 2 -> 3 (frame >= drawn -> ReleasePixels called)
	if !im.m["av"].released {
		t.Fatal("did not release CPU pixels after GPU upload frame")
	}
}
