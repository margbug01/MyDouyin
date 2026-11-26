package app

import (
	"context"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
	"douyin/internal/hg"
)

// solidCover is a portrait cover all in c.
func solidCover(c color.RGBA) *picture {
	img := image.NewRGBA(image.Rect(0, 0, 90, 160))
	for i := range img.Pix {
		img.Pix[i] = []uint8{c.R, c.G, c.B, 255}[i%4]
	}
	return &picture{bmp: ui.NewBitmap(img)}
}

var (
	red  = color.RGBA{255, 0, 0, 255}
	blue = color.RGBA{0, 0, 255, 255}
)

func isRed(c color.RGBA) bool  { return c.R > 200 && c.B < 60 }
func isBlue(c color.RGBA) bool { return c.B > 200 && c.R < 60 }

// Stepping to the next video slides the last one's cover up and out as the
// next one's comes in from below; once in, the picture shows as it is.
func TestStepSlides(t *testing.T) {
	im := newImages(func(string) ([]byte, error) { select {} }, func(func()) {})
	im.m["red"] = solidCover(red)
	im.m["blue"] = solidCover(blue)
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
	if !isRed(top) || !isBlue(bottom) {
		t.Fatalf("mid slide: top %v, bottom %v; want the red cover leaving above the blue one", top, bottom)
	}
	tt.Advance(slideTime)
	tt.Frame()
	if a.slide != nil {
		t.Fatal("the slide did not end")
	}
	if c := at(140); !isBlue(c) {
		t.Fatalf("after the slide the top shows %v, want the blue cover", c)
	}
}

// Hongguo's next episode slides in as the feed's next video does.
func TestHGStepEpisodeSlides(t *testing.T) {
	svc := &playerStubHGService{resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
		return hg.Playback{EpisodeID: epID, Duration: time.Minute, ExpiresAt: time.Now().Add(time.Hour),
			Streams: []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720}}}, nil
	}}
	a, tt, _, _ := newHGPlayerTestEnv(t, svc, time.Minute)
	a.switchMode(modeHongguo)
	tt.Frame()
	a.hg.img.m["red"] = solidCover(red)
	a.hg.img.m["blue"] = solidCover(blue)
	a.hgOpenPlayer(hg.Series{ID: "s", Title: "滑动剧", EpisodeCount: 2},
		[]hg.Episode{{ID: "e1", Index: 1, CoverURL: "red"}, {ID: "e2", Index: 2, CoverURL: "blue"}}, 0)
	tt.Frame()
	at := func(y int) color.RGBA { return tt.Image().RGBAAt(300, y) } // the stage's left, over the cover

	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Advance(slideTime / 3)
	if top, bottom := at(150), at(500); !isRed(top) || !isBlue(bottom) {
		t.Fatalf("mid slide: top %v, bottom %v; want episode 1's red cover leaving above episode 2's blue one", top, bottom)
	}
	if a.hg.player.epIdx != 1 {
		t.Fatalf("stepped to episode index %d, want 1", a.hg.player.epIdx)
	}
	tt.Advance(slideTime)
	tt.Frame()
	if a.slide != nil {
		t.Fatal("the slide did not end")
	}
}
