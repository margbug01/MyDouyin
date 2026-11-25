package app

import (
	"image"
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
)

// Stepping to the next video slides the last one's cover up and out as the
// next one's comes in from below; once in, the picture shows as it is.
func TestStepSlides(t *testing.T) {
	im := newImages(func(string) ([]byte, error) { select {} }, func(func()) {})
	solid := func(c color.RGBA) *ui.Bitmap {
		img := image.NewRGBA(image.Rect(0, 0, 90, 160))
		for i := range img.Pix {
			img.Pix[i] = []uint8{c.R, c.G, c.B, 255}[i%4]
		}
		return ui.NewBitmap(img)
	}
	im.m["red"] = &picture{bmp: solid(color.RGBA{255, 0, 0, 255})}
	im.m["blue"] = &picture{bmp: solid(color.RGBA{0, 0, 255, 255})}
	p := &page{title: "推荐", done: true, list: []*slot{
		{v: dy.Video{ID: "a", Poster: "red"}, loading: true},
		{v: dy.Video{ID: "b", Poster: "blue"}, loading: true},
	}}
	a := withRoots(&app{pages: []*page{p}, img: im})
	tt := newTester(a)
	tt.Frame()
	at := func(y float32) color.RGBA {
		return tt.Image().RGBAAt(600, int(y))
	}
	a.step(1)
	tt.Frame()
	tt.Advance(slideTime / 3)
	top, bottom := at(140), at(450)
	if !(top.R > 200 && top.B < 60) || !(bottom.B > 200 && bottom.R < 60) {
		t.Fatalf("mid slide: top %v, bottom %v; want the red cover leaving above the blue one", top, bottom)
	}
	tt.Advance(slideTime)
	tt.Frame()
	if a.slide != nil {
		t.Fatal("the slide did not end")
	}
	if c := at(140); !(c.B > 200 && c.R < 60) {
		t.Fatalf("after the slide the top shows %v, want the blue cover", c)
	}
}
