package app

import (
	"fmt"
	"strings"
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
		if len(rects) != orbitRing {
			t.Fatalf("%gx%g: expected %d cards, got %d", w, h, orbitRing, len(rects))
		}
		hx, hy, hw, hh := orbitHubRect(w, h)
		hub := orbitCardRect{x: hx, y: hy, w: hw, h: hh}
		for i, r := range rects {
			if r.x < contentLeft || r.y < 56 || r.x+r.w > w-contentLeft || r.y+r.h > h-16 {
				t.Fatalf("%gx%g card %d out of bounds (must stay clear of the dock at rest): %+v", w, h, i, r)
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
		orbit: orbitState{source: orbitPicks},
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

// A video played from 引力场 goes on into what its source fetches next, and
// what it fetched is the source's too, each page fetched once: 换一批 goes on
// after it rather than skipping it.
func TestOrbitPlayFetchesThroughSource(t *testing.T) {
	calls := 0
	src := &page{title: "关注", grid: true, next: func() ([]dy.Video, error) {
		calls++
		return []dy.Video{{ID: fmt.Sprint("new", calls)}}, nil
	}}
	for i := range 3 {
		src.list = append(src.list, &slot{v: dy.Video{ID: fmt.Sprint("v", i)}, loading: true})
	}
	home := &page{title: "引力场", grid: true}
	a := &app{home: home, follow: src, signedIn: true, pages: []*page{home}, posted: make(chan func(), 64)}
	a.orbit.source = orbitFollow
	a.playFromOrbit(src, 2) // the end near: the next page is asked for
	for src.fetching || a.cur().fetching {
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatal("the next page never came")
		}
	}
	ids := func(p *page) (s []string) {
		for _, x := range p.list {
			s = append(s, x.v.ID)
		}
		return s
	}
	played := fmt.Sprint(ids(a.cur()))
	if !strings.HasPrefix(played, "[v0 v1 v2 new1") {
		t.Fatalf("played: %s", played)
	}
	if got := fmt.Sprint(ids(src)); got != played || calls != len(src.list)-3 {
		t.Fatalf("source: %s after %d fetches, played %s", got, calls, played)
	}
}

func TestOrbitPicksDefaultFirstAndInfiniteNonRepeating(t *testing.T) {
	if orbitSources[0].id != orbitPicks || (orbitState{}).source != orbitPicks {
		t.Fatalf("orbitPicks must be first and default: sources=%+v default=%v", orbitSources, (orbitState{}).source)
	}

	// Sidebar 精选 already holds p1 and p2; orbit stream must never repeat them,
	// nor repeat duplicates returned across category tabs or infinite fallback.
	sidebarIDs := map[string]bool{"p1": true, "p2": true}
	tab1Done := false
	tab1 := func() ([]dy.Video, error) {
		if tab1Done {
			return nil, nil
		}
		tab1Done = true
		return []dy.Video{
			{ID: "p1"}, {ID: "a1"}, {ID: "a2"}, {ID: "a3"}, {ID: "a3"},
			{ID: "a4"}, {ID: "a5"}, {ID: "a6"}, {ID: "a7"}, {ID: "a8"},
		}, nil
	}
	tab2Calls := 0
	tab2 := func() ([]dy.Video, error) {
		tab2Calls++
		if tab2Calls == 1 {
			// Overlaps heavily with tab1 and sidebar 精选; only b1..b3 are new.
			return []dy.Video{{ID: "p2"}, {ID: "a1"}, {ID: "b1"}, {ID: "b2"}, {ID: "b3"}}, nil
		}
		return nil, nil
	}
	infSeq := 0
	infinite := func() ([]dy.Video, error) {
		batch := make([]dy.Video, 0, orbitRing+2)
		// Include an old duplicate on every pull to ensure dedup holds forever.
		batch = append(batch, dy.Video{ID: "a1"}, dy.Video{ID: "p1"})
		for range orbitRing {
			infSeq++
			batch = append(batch, dy.Video{ID: fmt.Sprintf("inf%d", infSeq)})
		}
		return batch, nil
	}

	pager := newOrbitStreamPager([]dy.Pager[dy.Video]{tab1, tab2}, infinite, func(id string) bool { return sidebarIDs[id] })
	seen := map[string]bool{}
	for round := range 12 {
		vs, err := pager()
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if len(vs) < orbitRing {
			t.Fatalf("round %d: got only %d videos, want >= %d", round, len(vs), orbitRing)
		}
		for _, v := range vs {
			if sidebarIDs[v.ID] {
				t.Fatalf("round %d leaked sidebar video %s", round, v.ID)
			}
			if seen[v.ID] {
				t.Fatalf("round %d repeated video %s", round, v.ID)
			}
			seen[v.ID] = true
		}
	}
}

func TestCycleOrbitWaitsForFreshBatchInsteadOfWrapping(t *testing.T) {
	gate := make(chan struct{})
	seq := 0
	orbitPage := &page{
		title: "精选",
		grid:  true,
		next: func() ([]dy.Video, error) {
			<-gate
			out := make([]dy.Video, orbitRing)
			for i := range orbitRing {
				seq++
				out[i] = dy.Video{ID: fmt.Sprintf("fresh%d", seq)}
			}
			return out, nil
		},
	}
	for i := range orbitRing {
		orbitPage.list = append(orbitPage.list, &slot{v: dy.Video{ID: fmt.Sprintf("init%d", i)}})
	}
	home := &page{title: "引力场", grid: true}
	a := &app{
		home:       home,
		orbitPicks: orbitPage,
		pages:      []*page{home},
		orbit:      orbitState{source: orbitPicks},
		posted:     make(chan func(), 16),
	}

	// Clicking 换一批 when only 1 ring is loaded must NOT wrap back to 0; it waits for fresh videos.
	a.cycleOrbit()
	if a.orbit.offset != 0 || !a.orbit.pendingAdvance {
		t.Fatalf("expected pendingAdvance while fetching: offset=%d pending=%v", a.orbit.offset, a.orbit.pendingAdvance)
	}
	close(gate)
	// First batch finishes -> automatically advances offset to 8 (fresh1..fresh8) and prefetches next ring.
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fresh orbit batch")
	}
	if a.orbit.pendingAdvance || a.orbit.offset != orbitRing {
		t.Fatalf("did not advance to fresh ring: offset=%d pending=%v len=%d", a.orbit.offset, a.orbit.pendingAdvance, len(orbitPage.list))
	}
	if got := orbitPage.list[a.orbit.offset].v.ID; got != "fresh1" {
		t.Fatalf("new ring starts at %s, want fresh1", got)
	}
}
