package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
)

func TestOrbitLayoutNoOverlap(t *testing.T) {
	sizes := [][2]float32{
		{960, 620},
		{1100, 760},
		{1280, 820},
		{1440, 900},
		{1680, 1050},
	}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		rects := orbitLayout(w, h)
		minCards := 4
		if w >= 1280 && h >= 820 {
			minCards = 8
		}
		if len(rects) < minCards {
			t.Fatalf("%gx%g: expected at least %d cards, got %d", w, h, minCards, len(rects))
		}
		hx, hy, hw, hh := orbitHubRect(w, h)
		hub := orbitCardRect{x: hx, y: hy, w: hw, h: hh}
		for i, r := range rects {
			if r.x < dockSafeLeft || r.y < 62 || r.x+r.w > w-22 || r.y+r.h > h-18 {
				t.Fatalf("%gx%g card %d out of bounds (must stay clear of expanded dock %.0f): %+v", w, h, i, dockSafeLeft, r)
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
	}
}

func TestOrbitHomeInteractions(t *testing.T) {
	picks := &page{title: "精选", grid: true, done: true}
	for i := range 8 {
		picks.list = append(picks.list, &slot{
			v: dy.Video{
				ID:       fmt.Sprintf("v%d", i),
				Desc:     fmt.Sprintf("测试视频 %d 号作品", i),
				Author:   fmt.Sprintf("博主%d", i),
				Duration: time.Duration(90+i*30) * time.Second,
				Created:  time.Now().Add(-time.Duration(i+1) * time.Hour),
			},
			loading: true,
		})
	}
	home := &page{title: "引力场", grid: true}
	a := &app{
		home:  home,
		picks: picks,
		feed:  stub("推荐", 2),
		pages: []*page{home},
		orbit: orbitState{source: orbitPicks, hovered: -1},
		hot:   []dy.Hot{{Word: "热搜第一", Value: 120000}},
	}
	tt := ui.NewTester(a.view, 1280, 820)
	if !tt.HasText("测试视频 0 号作品") {
		t.Fatal("missing surrounding orbit video card")
	}

	// Hide surrounding cards -> pure search mode
	if err := tt.Click("收起周围画面"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.orbit.hidden || tt.HasText("测试视频 0 号作品") {
		t.Fatal("expected surrounding cards hidden")
	}
	if err := tt.Click("显示周围画面"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.orbit.hidden || !tt.HasText("测试视频 0 号作品") {
		t.Fatal("expected surrounding cards restored")
	}

	// Hot search popover opens on demand and closes on Escape
	if tt.HasText("热搜第一") {
		t.Fatal("hot search should stay hidden by default")
	}
	if err := tt.Click("热搜潜望"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.orbit.hotOpen || !tt.HasText("热搜第一") {
		t.Fatal("hot search popover did not open")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.orbit.hotOpen {
		t.Fatal("Escape did not close hot search popover")
	}

	// Theme toggle switches cleanly between light and dark palettes
	if a.orbitIsDark() {
		t.Fatal("expected light default in headless test")
	}
	if err := tt.Click("切换深色主题"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.orbitIsDark() {
		t.Fatal("expected dark mode after toggle")
	}
	if err := tt.Click("切换浅色主题"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.orbitIsDark() {
		t.Fatal("expected light mode after second toggle")
	}

	// Clicking an orbiting card plays it, and Escape returns straight to Orbital Home
	if err := tt.Click("测试视频 0 号作品"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pages) != 2 || !a.cur().plays() {
		t.Fatalf("after clicking orbit card: pages=%d plays=%v", len(a.pages), a.cur().plays())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(a.pages) != 1 || a.cur() != a.home {
		t.Fatalf("after Escape from orbit playback: pages=%d cur=%v", len(a.pages), a.cur().title)
	}
}
