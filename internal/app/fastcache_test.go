package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"douyin/internal/dy"
	"douyin/internal/hg"
)

func TestOrbitFastCacheSavesUnseenRingAndRestoresInstantly(t *testing.T) {
	dir := t.TempDir()
	orbitPage := &page{title: "精选", grid: true}
	for i := range 16 {
		orbitPage.list = append(orbitPage.list, &slot{
			v: dy.Video{ID: fmt.Sprintf("v%d", i), Desc: fmt.Sprintf("视频 %d", i)},
		})
	}
	a := &app{
		cacheDir:   dir,
		orbitPicks: orbitPage,
		orbit:      orbitState{source: orbitPicks, offset: 0},
	}
	a.saveOrbitCache()

	// Wait for async atomic write to land.
	cachePath := a.orbitCachePath()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(cachePath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Next launch loads the unseen second ring (v8..v15) immediately.
	next := &app{
		cacheDir:   dir,
		orbitPicks: &page{title: "精选", grid: true},
		orbit:      orbitState{source: orbitPicks},
	}
	next.loadOrbitCache()
	if len(next.orbitPicks.list) != orbitRing {
		t.Fatalf("loaded %d cached videos, want %d", len(next.orbitPicks.list), orbitRing)
	}
	if got := next.orbitPicks.list[0].v.ID; got != "v8" {
		t.Fatalf("first cached video is %s, want v8 (next unseen ring)", got)
	}
	if !next.orbitPicks.list[0].cached {
		t.Fatal("cached slot must be marked cached=true for PlayURL refresh")
	}
}

func TestHGDiscoverStaleWhileRevalidateCache(t *testing.T) {
	dir := t.TempDir()

	// Seed disk cache as a previous session would.
	snap := hgDiscoverSnapshot{
		SavedAt: time.Now(),
		Channel: hgDiscoverChannels[0],
		Items: []hg.Series{
			{ID: "c1", Title: "缓存短剧一"},
			{ID: "c2", Title: "缓存短剧二"},
		},
	}
	writeJSONAtomic(filepath.Join(dir, "hg-discover.json"), snap)

	gate := make(chan struct{})
	svc := &stubHGService{
		discoverFn: func(ctx context.Context, channel, cursor string) (hg.Page[hg.Series], error) {
			<-gate
			return hg.Page[hg.Series]{
				Items: []hg.Series{
					{ID: "f1", Title: "最新短剧一"},
					{ID: "f2", Title: "最新短剧二"},
				},
				Cursor:  "next-cur",
				HasMore: true,
			}, nil
		},
	}
	a := withRoots(&app{
		pages:    []*page{stub("推荐", 1)},
		cacheDir: dir,
		mode:     modeHongguo,
		posted:   make(chan func(), 32),
		newHGService: func(cfg hg.Config) (hg.Service, error) {
			return svc, nil
		},
	})

	h := a.ensureHG()
	l := h.discoverList(hgDiscoverChannels[0])
	if len(l.items) != 2 || l.items[0].ID != "c1" || !l.stale {
		t.Fatalf("expected stale cached items on ensureHG: items=%+v stale=%v", l.items, l.stale)
	}

	// Triggering initial load refreshes in background WITHOUT wiping cached items.
	a.hgEnsureActiveLoaded()
	if !l.loading || len(l.items) != 2 || l.items[0].ID != "c1" {
		t.Fatalf("background refresh cleared cached items while loading: loading=%v items=%+v", l.loading, l.items)
	}

	// Release live Discover response -> replaces cached items cleanly.
	close(gate)
	deadline := time.After(2 * time.Second)
	for l.loading {
		select {
		case fn := <-a.posted:
			fn()
		case <-deadline:
			t.Fatal("timed out waiting for background Discover refresh")
		}
	}
	if l.stale || !l.loaded || len(l.items) != 2 || l.items[0].ID != "f1" {
		t.Fatalf("after background refresh: stale=%v loaded=%v items=%+v", l.stale, l.loaded, l.items)
	}
	// Wait for the async cache update to finish before TempDir cleanup.
	writeDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(writeDeadline) {
		if raw, err := os.ReadFile(a.hgDiscoverCachePath()); err == nil && bytes.Contains(raw, []byte(`"f1"`)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHGDiscoverStaleCacheKeptOnRefreshError(t *testing.T) {
	dir := t.TempDir()
	writeJSONAtomic(filepath.Join(dir, "hg-discover.json"), hgDiscoverSnapshot{
		SavedAt: time.Now(),
		Channel: hgDiscoverChannels[0],
		Items:   []hg.Series{{ID: "c1", Title: "离线兜底短剧"}},
	})

	svc := &stubHGService{
		discoverFn: func(ctx context.Context, channel, cursor string) (hg.Page[hg.Series], error) {
			return hg.Page[hg.Series]{}, errors.New("network down")
		},
	}
	a := withRoots(&app{
		pages:    []*page{stub("推荐", 1)},
		cacheDir: dir,
		mode:     modeHongguo,
		posted:   make(chan func(), 32),
		newHGService: func(cfg hg.Config) (hg.Service, error) {
			return svc, nil
		},
	})
	h := a.ensureHG()
	l := h.discoverList(hgDiscoverChannels[0])
	a.hgEnsureActiveLoaded()
	deadline := time.After(2 * time.Second)
	for l.loading {
		select {
		case fn := <-a.posted:
			fn()
		case <-deadline:
			t.Fatal("timed out waiting for failed Discover refresh")
		}
	}
	if len(l.items) != 1 || l.items[0].ID != "c1" {
		t.Fatalf("failed background refresh dropped cached items: %+v", l.items)
	}
	if l.err.Kind == hg.ErrKindNone {
		t.Fatal("expected error info recorded for retry")
	}
}

func TestImagesDiskCoverCacheAndPrune(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 120, 120)))

	var fetches atomic.Int32
	done := make(chan func(), 16)
	im1 := newImages(func(url string) ([]byte, error) {
		fetches.Add(1)
		return buf.Bytes(), nil
	}, func(fn func()) { done <- fn })
	im1.cacheDir = dir

	im1.getSized("https://example.com/c1.png", coverEdge)
	(<-done)()
	if fetches.Load() != 1 {
		t.Fatalf("first load fetches=%d, want 1", fetches.Load())
	}

	// Second images instance with the same cacheDir serves from disk without fetching.
	im2 := newImages(func(url string) ([]byte, error) {
		fetches.Add(1)
		return buf.Bytes(), nil
	}, func(fn func()) { done <- fn })
	im2.cacheDir = dir

	im2.getSized("https://example.com/c1.png", coverEdge)
	(<-done)()
	if fetches.Load() != 1 {
		t.Fatalf("second load hit network: fetches=%d, want 1", fetches.Load())
	}
	if im2.getSized("https://example.com/c1.png", coverEdge) == nil {
		t.Fatal("disk-cached bitmap not loaded")
	}

	// Prune keeps only the newest keep files.
	for i := range 10 {
		p := filepath.Join(dir, fmt.Sprintf("f%d.img", i))
		_ = os.WriteFile(p, []byte("x"), 0o644)
		mt := time.Now().Add(time.Duration(i) * time.Second)
		_ = os.Chtimes(p, mt, mt)
	}
	pruneCoverCache(dir, 4)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("after prune: %d files, want 4", len(entries))
	}
}
