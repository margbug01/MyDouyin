package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func TestCommentPictureClickOpensLightboxAndEscapeKeepsComments(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := range 600 {
		for x := range 800 {
			img.SetRGBA(x, y, color.RGBA{R: 120, G: 180, B: 220, A: 255})
		}
	}
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	asked := make(chan string, 8)
	done := make(chan func(), 8)
	a := withRoots(&app{pages: []*page{stub("推荐", 2)}})
	a.img = newImages(func(u string) ([]byte, error) {
		asked <- u
		return buf.Bytes(), nil
	}, func(fn func()) { done <- fn })
	a.sheet = &sheet{
		total: 1,
		done:  true,
		items: []dy.Comment{{
			ID:     "c1",
			Author: "甲",
			Text:   "看图",
			Pics: []dy.CommentPic{
				{Thumb: "pic:thumb:1", Origin: "pic:origin:1"},
				{Thumb: "pic:thumb:2", Origin: "pic:origin:2"},
			},
		}},
	}
	tt := newTester(a)
	tt.Frame()
	for range 2 {
		select {
		case fn := <-done:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatal("thumbnails did not load")
		}
	}
	tt.Frame()
	if err := tt.Click("查看评论图片"); err != nil {
		t.Fatalf("click comment pic: %v", err)
	}
	tt.Frame()
	if !a.picViewer.open || a.picViewer.idx != 0 {
		t.Fatalf("pic viewer did not open: %+v", a.picViewer)
	}
	if !a.mainVideo.off {
		t.Fatal("native video layer must pause/hide under pic viewer")
	}
	// Lightbox should request the origin URL, not only the thumbnail URL.
	gotOrigin := false
	for !gotOrigin {
		select {
		case u := <-asked:
			if u == "pic:origin:1" {
				gotOrigin = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("lightbox did not fetch origin image")
		}
	}
	if !tt.HasText("1 / 2") {
		t.Fatalf("missing multi-image counter: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if a.picViewer.idx != 1 || !tt.HasText("2 / 2") {
		t.Fatalf("KeyRight did not advance image: idx=%d", a.picViewer.idx)
	}
	if err := tt.Click("放大"); err != nil {
		t.Fatalf("click zoom in: %v", err)
	}
	tt.Frame()
	if a.picViewer.zoom <= 1 {
		t.Fatalf("zoom did not increase: %v", a.picViewer.zoom)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.picViewer.open || a.sheet == nil {
		t.Fatalf("Escape should close only pic viewer: open=%v sheet=%v", a.picViewer.open, a.sheet != nil)
	}
}
