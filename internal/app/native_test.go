package app

import (
	"testing"

	"douyin/internal/media"
	"github.com/egoist/mygo/ui"
)

func TestNativeBackgroundMasksWebPageAndScrollCorners(t *testing.T) {
	surface := nativeSurface{player: &streamPlayer{layer: &media.Layer{}}, radius: 12}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Children(func() {
			ui.Box(c).Absolute().Top(0).Bottom(0).Left(0).Right(0).Background(ui.Hex("#ff0000")).ZIndex(-2) // page under the app
			surface.background(c, pal.bg)
			ui.Box(c).Size(12, 12).Background(ui.Hex("#00ff00")) // ordinary foreground UI, above the mask
			surface.viewport = ui.Box(c).Absolute().Left(70).Top(20).Size(100, 100)
			surface.element = ui.Box(c).Absolute().Left(50).Top(40).Size(100, 100)
		})
	}, 200, 160)
	tt.Frame()
	image := tt.Image()
	if foreground := image.RGBAAt(5, 5); foreground.G < 250 || foreground.R > 5 {
		t.Fatalf("background mask covered foreground UI: %v", foreground)
	}
	hidden := image.RGBAAt(180, 70)
	for _, point := range [][2]int{{60, 70}, {80, 130}, {149, 41}} {
		if got := image.RGBAAt(point[0], point[1]); got != hidden {
			t.Fatalf("page escaped video viewport/corner at %v: %v", point, got)
		}
	}
	if hole := image.RGBAAt(80, 70); hole.R < 250 || hole.G > 5 {
		t.Fatalf("video viewport was painted over: %v", hole)
	}
	surface.player = nil
	tt.Frame()
	if got := tt.Image().RGBAAt(80, 70); got != hidden {
		t.Fatalf("hidden video left the page exposed: %v", got)
	}
}
