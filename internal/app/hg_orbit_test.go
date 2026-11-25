package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/hg"
)

func TestHGOrbitLayoutNoOverlapAndPortraitAspect(t *testing.T) {
	sizes := [][2]float32{
		{960, 620},
		{1100, 760},
		{1280, 820},
		{1440, 900},
		{1680, 1050},
	}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		rects := hgOrbitLayout(w, h)
		if len(rects) != hgOrbitRing {
			t.Fatalf("%gx%g: expected %d cards, got %d", w, h, hgOrbitRing, len(rects))
		}
		hx, hy, hw, hh := hgOrbitHubRect(w, h)
		hub := orbitCardRect{x: hx, y: hy, w: hw, h: hh}
		for i, r := range rects {
			if r.x < contentLeft || r.y < 56 || r.x+r.w > w-contentLeft || r.y+r.h > h-16 {
				t.Fatalf("%gx%g card %d out of bounds: %+v", w, h, i, r)
			}
			// Every Hongguo orbit poster is 3:4 portrait.
			if ratio := r.w / r.h; ratio < 0.72 || ratio > 0.78 {
				t.Fatalf("%gx%g card %d aspect ratio %.3f not 3:4", w, h, i, ratio)
			}
			if r.x < hub.x+hub.w && r.x+r.w > hub.x && r.y < hub.y+hub.h && r.y+r.h > hub.y {
				t.Fatalf("%gx%g card %d overlaps hub: card=%+v hub=%+v", w, h, i, r, hub)
			}
			for j := i + 1; j < len(rects); j++ {
				o := rects[j]
				if r.x < o.x+o.w && r.x+r.w > o.x && r.y < o.y+o.h && r.y+r.h > o.y {
					t.Fatalf("%gx%g card %d overlaps card %d: %+v vs %+v", w, h, i, j, r, o)
				}
			}
		}
		// Left wing (0..3) sits strictly left of right wing (4..7).
		for i := range hgOrbitWing {
			for j := hgOrbitWing; j < hgOrbitRing; j++ {
				if rects[i].x+rects[i].w > rects[j].x {
					t.Fatalf("%gx%g left wing card %d crosses right wing card %d", w, h, i, j)
				}
			}
		}
	}
}

func TestHGOrbitDualWingSplitCycleSwitchAndDetailReturn(t *testing.T) {
	makeSeries := func(prefix string, start, count int) []hg.Series {
		out := make([]hg.Series, count)
		for i := range count {
			n := start + i
			out[i] = hg.Series{
				ID:           fmt.Sprintf("%s-%d", prefix, n),
				Title:        fmt.Sprintf("%s剧%d号", prefix, n),
				Category:     prefix,
				EpisodeCount: 60 + n,
			}
		}
		return out
	}

	svc := &stubHGService{
		discoverFn: func(_ context.Context, channel, cursor string) (hg.Page[hg.Series], error) {
			switch channel {
			case "真人剧":
				return hg.Page[hg.Series]{Items: makeSeries("真人", 0, 12)}, nil
			case "漫剧":
				return hg.Page[hg.Series]{Items: makeSeries("漫剧", 0, 12)}, nil
			}
			return hg.Page[hg.Series]{}, nil
		},
		hotSearchFn: func(context.Context) (hg.HotSearch, error) {
			return hg.HotSearch{
				Guesses: []string{"逆袭短剧"},
				Boards: []hg.HotBoard{{
					Name:  "短剧热搜榜",
					Items: []hg.HotItem{{Word: "热搜第一短剧", Score: "2000万"}},
				}},
			}, nil
		},
		getSeriesFn: func(_ context.Context, id string) (hg.Series, error) {
			return hg.Series{ID: id, Title: "真人剧0号", EpisodeCount: 60}, nil
		},
	}

	a, tt := newHGTestApp(t, svc)
	a.switchMode(modeHongguo)
	tt.Frame()
	waitUntil(t, a, tt, "both wings loaded", func() bool {
		return a.hg.discoverList("真人剧").loaded && a.hg.discoverList("漫剧").loaded && a.hg.hotLoaded
	})

	// Default dual-wing view displays 4 真人剧 (0..3) and 4 漫剧 (0..3).
	if !tt.HasText("真人剧0号") || !tt.HasText("真人剧3号") || tt.HasText("真人剧4号") {
		t.Fatalf("left wing mismatch: %q", tt.Texts())
	}
	if !tt.HasText("漫剧剧0号") || !tt.HasText("漫剧剧3号") || tt.HasText("漫剧剧4号") {
		t.Fatalf("right wing mismatch: %q", tt.Texts())
	}

	// Wheel down advances BOTH wings by 4 simultaneously.
	tt.Wheel(400, 300, 0, 40)
	tt.Frame()
	if a.hg.orbit.offset != hgOrbitWing || !tt.HasText("真人剧4号") || !tt.HasText("漫剧剧4号") {
		t.Fatalf("dual wheel down did not advance both wings: offset=%d texts=%q", a.hg.orbit.offset, tt.Texts())
	}

	// Wheel up after cooldown returns to offset 0.
	tt.Advance(450 * time.Millisecond)
	tt.Wheel(400, 300, 0, -40)
	tt.Frame()
	if a.hg.orbit.offset != 0 || !tt.HasText("真人剧0号") {
		t.Fatalf("wheel up did not return to offset 0: offset=%d", a.hg.orbit.offset)
	}

	// Clicking wing label 「只看漫剧」 switches to pure 漫剧 8-poster orbit.
	if err := tt.Click("只看漫剧"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.hg.orbit.source != hgOrbitComic || !tt.HasText("漫剧剧7号") || tt.HasText("真人剧0号") {
		t.Fatalf("switching to pure 漫剧 failed: src=%v texts=%q", a.hg.orbit.source, tt.Texts())
	}

	// Switch back to 双星, open hot search popover, then close with Escape.
	if err := tt.Click("双星"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("短剧热搜"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.hg.orbit.hotOpen || !tt.HasText("热搜第一短剧") {
		t.Fatalf("hot search popover did not open: open=%v texts=%q", a.hg.orbit.hotOpen, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.hg.orbit.hotOpen {
		t.Fatal("Escape did not close Hongguo orbit hot search popover")
	}

	// Hide/show surrounding posters.
	if err := tt.Click("收起周围海报"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.hg.orbit.hidden || tt.HasText("真人剧0号") {
		t.Fatal("expected posters hidden")
	}
	if err := tt.Click("显示周围海报"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.hg.orbit.hidden || !tt.HasText("真人剧0号") {
		t.Fatal("expected posters restored")
	}

	// Clicking a poster opens detail page; Escape returns to Hongguo orbit.
	if err := tt.Click("真人剧0号"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if top := a.hg.top(); top == nil || top.kind != hgPageDetail || top.seriesID != "真人-0" {
		t.Fatalf("expected detail page for 真人-0, got %+v", top)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(a.hg.stack) != 0 || a.hg.root != hgRootOrbit {
		t.Fatalf("Escape did not return to Hongguo orbit: root=%v stack=%d", a.hg.root, len(a.hg.stack))
	}
}
