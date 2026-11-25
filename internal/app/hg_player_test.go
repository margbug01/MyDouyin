package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/hg"
	"douyin/internal/media"
)

type fakeHGVideoPlayer struct {
	mu      sync.Mutex
	url     string
	opts    media.StreamOptions
	playing bool
	closed  bool
	err     error
	pos     time.Duration
	dur     time.Duration
	seeking bool
	volume  float64
	muted   bool
	speed   float64
	seeks   []time.Duration
}

func (f *fakeHGVideoPlayer) Play() {
	f.mu.Lock()
	f.playing = true
	f.mu.Unlock()
}

func (f *fakeHGVideoPlayer) Pause() {
	f.mu.Lock()
	f.playing = false
	f.mu.Unlock()
}

func (f *fakeHGVideoPlayer) Playing() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.playing && !f.closed && f.err == nil
}

func (f *fakeHGVideoPlayer) Close() {
	f.mu.Lock()
	f.closed = true
	f.playing = false
	f.mu.Unlock()
}

func (f *fakeHGVideoPlayer) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *fakeHGVideoPlayer) Size() (int, int) { return 720, 1280 }

func (f *fakeHGVideoPlayer) Position() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pos
}

func (f *fakeHGVideoPlayer) Duration() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dur
}

func (f *fakeHGVideoPlayer) Seeking() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seeking
}

func (f *fakeHGVideoPlayer) Seek(t time.Duration) {
	f.mu.Lock()
	f.pos = t
	f.seeks = append(f.seeks, t)
	f.mu.Unlock()
}

func (f *fakeHGVideoPlayer) SetVolume(v float64) {
	f.mu.Lock()
	f.volume = v
	f.mu.Unlock()
}

func (f *fakeHGVideoPlayer) SetMuted(m bool) {
	f.mu.Lock()
	f.muted = m
	f.mu.Unlock()
}

func (f *fakeHGVideoPlayer) SetSpeed(s float64) {
	f.mu.Lock()
	f.speed = s
	f.mu.Unlock()
}

func (f *fakeHGVideoPlayer) setPos(d time.Duration) {
	f.mu.Lock()
	f.pos = d
	f.mu.Unlock()
}

func (f *fakeHGVideoPlayer) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

type playerStubHGService struct {
	stubHGService

	resolveFn func(ctx context.Context, epID string) (hg.Playback, error)
	danmakuFn func(ctx context.Context, seriesID, epID string, startMs int64, cursor string) (hg.DanmakuPage, error)

	pmu          sync.Mutex
	resolveCalls []string
	danmakuCalls int
}

func (s *playerStubHGService) ResolvePlayback(ctx context.Context, epID string) (hg.Playback, error) {
	s.pmu.Lock()
	s.resolveCalls = append(s.resolveCalls, epID)
	fn := s.resolveFn
	s.pmu.Unlock()
	if fn != nil {
		return fn(ctx, epID)
	}
	return hg.Playback{}, nil
}

func (s *playerStubHGService) ListDanmaku(ctx context.Context, seriesID, epID string, startMs int64, cursor string) (hg.DanmakuPage, error) {
	s.pmu.Lock()
	s.danmakuCalls++
	fn := s.danmakuFn
	s.pmu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID, epID, startMs, cursor)
	}
	return hg.DanmakuPage{}, nil
}

func (s *playerStubHGService) resolveCount(epID string) int {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	n := 0
	for _, id := range s.resolveCalls {
		if id == epID {
			n++
		}
	}
	return n
}

func (s *playerStubHGService) totalResolves() int {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	return len(s.resolveCalls)
}

type openedStreamRecord struct {
	url    string
	opts   media.StreamOptions
	player *fakeHGVideoPlayer
}

func newHGPlayerTestEnv(t *testing.T, svc *playerStubHGService, dur time.Duration) (*app, *ui.Tester, *[]openedStreamRecord, *sync.Mutex) {
	t.Helper()
	dir := t.TempDir()
	var mu sync.Mutex
	var opened []openedStreamRecord

	a := withRoots(&app{
		pages:        []*page{stub("推荐", 1)},
		posted:       make(chan func(), 128),
		settingsFile: filepath.Join(dir, "settings.json"),
		hgDirect:     true,
		volume:       0.8,
		speed:        1,
		newHGService: func(hg.Config) (hg.Service, error) {
			return svc, nil
		},
		openHGStream: func(_ context.Context, rawURL string, opts media.StreamOptions, _ bool) (players, error) {
			fp := &fakeHGVideoPlayer{url: rawURL, opts: opts, dur: dur}
			mu.Lock()
			opened = append(opened, openedStreamRecord{url: rawURL, opts: opts, player: fp})
			mu.Unlock()
			return players{vp: fp}, nil
		},
	})
	return a, newTester(a), &opened, &mu
}

func TestHGPlayerEncryptedStreamDecryptKeyAndStart(t *testing.T) {
	const keyHex = "00112233445566778899aabbccddeeff"
	wantKey, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatal(err)
	}

	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Protected: true,
				KeyHex:    keyHex,
				Duration:  90 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams: []hg.Stream{
					{Definition: "1080p", Codec: "h264", URL: "https://cdn.example/ep1-1080p.mp4", Height: 1080},
				},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 90*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	series := hg.Series{ID: "ser-1", Title: "加密短剧", EpisodeCount: 2}
	eps := []hg.Episode{
		{ID: "ep-1", Index: 1, Duration: 90 * time.Second},
		{ID: "ep-2", Index: 2, Duration: 90 * time.Second},
	}
	a.hgOpenPlayer(series, eps, 0)
	tt.Frame()

	waitUntil(t, a, tt, "encrypted episode playing", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})

	mu.Lock()
	if len(*opened) != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 openHGStream call, got %d", len(*opened))
	}
	rec := (*opened)[0]
	mu.Unlock()

	if rec.url != "https://cdn.example/ep1-1080p.mp4" {
		t.Fatalf("unexpected stream URL: %q", rec.url)
	}
	if !bytes.Equal(rec.opts.DecryptKey, wantKey) {
		t.Fatalf("DecryptKey = %x, want %x", rec.opts.DecryptKey, wantKey)
	}
	if !rec.player.Playing() {
		t.Fatal("expected encrypted episode player to be playing")
	}
}

func TestHGPlayerDrawerSwitchCheckpointsAndClosesOutgoing(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  120 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams: []hg.Stream{
					{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720},
				},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 120*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	series := hg.Series{ID: "ser-2", Title: "切换选集测试", EpisodeCount: 3}
	eps := []hg.Episode{
		{ID: "ep-1", Index: 1, Duration: 120 * time.Second},
		{ID: "ep-2", Index: 2, Duration: 120 * time.Second},
		{ID: "ep-3", Index: 3, Duration: 120 * time.Second},
	}
	a.hgOpenPlayer(series, eps, 0)
	tt.Frame()

	waitUntil(t, a, tt, "ep 1 started", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil
	})

	mu.Lock()
	vp1 := (*opened)[0].player
	mu.Unlock()

	// Advance playback to 18s then click 第2集 in the episode drawer.
	vp1.setPos(18 * time.Second)
	tt.Frame()

	if err := tt.Click("第2集"); err != nil {
		t.Fatalf("click 第2集 in drawer: %v (texts=%q)", err, tt.Texts())
	}
	tt.Frame()

	waitUntil(t, a, tt, "ep 2 started", func() bool {
		return a.hg.player.epIdx == 1 && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})

	vp1.mu.Lock()
	closed1 := vp1.closed
	vp1.mu.Unlock()
	if !closed1 {
		t.Fatal("outgoing episode player was not closed on drawer switch")
	}

	entry, ok := a.hg.hist.Get("ser-2")
	if !ok {
		t.Fatal("expected history entry for ser-2")
	}
	if entry.Positions["ep-1"] != 18*time.Second {
		t.Fatalf("ep-1 checkpoint position = %v, want 18s", entry.Positions["ep-1"])
	}
	if a.hg.player.epIdx != 1 {
		t.Fatalf("active epIdx = %d, want 1", a.hg.player.epIdx)
	}
}

func TestHGPlayerPrefetchAt75PercentAndCacheReuse(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  100 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams: []hg.Stream{
					{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720},
				},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 100*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	// Browsing Discover must never trigger ResolvePlayback.
	if svc.totalResolves() != 0 {
		t.Fatalf("expected 0 ResolvePlayback calls on browse grid, got %d", svc.totalResolves())
	}

	series := hg.Series{ID: "ser-pref", Title: "预解析剧", EpisodeCount: 2}
	eps := []hg.Episode{
		{ID: "ep-1", Index: 1, Duration: 100 * time.Second},
		{ID: "ep-2", Index: 2, Duration: 100 * time.Second},
	}
	a.hgOpenPlayer(series, eps, 0)
	tt.Frame()
	waitUntil(t, a, tt, "ep 1 loaded", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil
	})

	mu.Lock()
	vp1 := (*opened)[0].player
	mu.Unlock()

	// Below 75%: no prefetch for ep-2.
	vp1.setPos(50 * time.Second)
	tt.Frame()
	if svc.resolveCount("ep-2") != 0 {
		t.Fatalf("prefetched ep-2 too early at 50%%: calls=%d", svc.resolveCount("ep-2"))
	}

	// Cross 75%: triggers exactly ONE background ResolvePlayback for ep-2.
	vp1.setPos(76 * time.Second)
	tt.Frame()
	vp1.setPos(80 * time.Second)
	tt.Frame()

	waitUntil(t, a, tt, "ep-2 prefetch completed", func() bool {
		return a.hg.player.prefetchEpID == "ep-2" && a.hg.player.prefetchPB != nil && !a.hg.player.prefetchBusy
	})

	if svc.resolveCount("ep-2") != 1 {
		t.Fatalf("expected exactly 1 prefetch ResolvePlayback for ep-2, got %d", svc.resolveCount("ep-2"))
	}

	// Advance to ep-2: must reuse cached prefetchPB without calling ResolvePlayback again.
	a.hgPlayEpisode(1)
	tt.Frame()
	waitUntil(t, a, tt, "ep-2 started from prefetch", func() bool {
		return a.hg.player.epIdx == 1 && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})

	if svc.resolveCount("ep-2") != 1 {
		t.Fatalf("advancing to prefetched ep-2 made extra ResolvePlayback call: count=%d", svc.resolveCount("ep-2"))
	}
}

func TestHGDetailPreResolvesTargetEpisodeAndCachesDetail(t *testing.T) {
	svc := &playerStubHGService{
		stubHGService: stubHGService{
			getSeriesFn: func(_ context.Context, id string) (hg.Series, error) {
				return hg.Series{ID: id, Title: "秒开短剧", EpisodeCount: 2}, nil
			},
			episodesFn: func(_ context.Context, id string) ([]hg.Episode, error) {
				return []hg.Episode{
					{ID: "ep-detail-1", Index: 1, Duration: 90 * time.Second},
					{ID: "ep-detail-2", Index: 2, Duration: 90 * time.Second},
				}, nil
			},
		},
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  90 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams: []hg.Stream{
					{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720},
				},
			}, nil
		},
	}

	a, tt, _, _ := newHGPlayerTestEnv(t, svc, 90*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	// Opening detail page loads episodes and pre-resolves episode 1 in background.
	a.hgOpenDetail(hg.Series{ID: "ser-instant", Title: "秒开短剧"})
	tt.Frame()
	waitUntil(t, a, tt, "detail pre-resolved ep-1", func() bool {
		top := a.hg.top()
		return top != nil && top.kind == hgPageDetail && top.prefetchEpID == "ep-detail-1" && top.prefetchPB != nil && !top.prefetchBusy
	})
	if got := svc.resolveCount("ep-detail-1"); got != 1 {
		t.Fatalf("detail pre-resolve count = %d, want 1", got)
	}

	// Clicking Play consumes the detail pre-resolve with zero additional ResolvePlayback calls.
	top := a.hg.top()
	a.hgOpenPlayer(top.series, top.episodes, 0)
	tt.Frame()
	waitUntil(t, a, tt, "ep-1 started from detail prefetch", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})
	if got := svc.resolveCount("ep-detail-1"); got != 1 {
		t.Fatalf("Play after detail pre-resolve triggered extra ResolvePlayback: count=%d, want 1", got)
	}

	// Pop player and detail, then reopen detail -> episodes must be populated synchronously from detailCache.
	a.hgPop()
	a.hgPop()
	a.hgOpenDetail(hg.Series{ID: "ser-instant", Title: "秒开短剧"})
	reopened := a.hg.top()
	if reopened == nil || len(reopened.episodes) != 2 {
		t.Fatalf("reopened detail page did not hydrate episodes synchronously from cache: %+v", reopened)
	}
}

func TestHGPlayerEOFAutoNextAndFinalEpisodeToast(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  60 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams: []hg.Stream{
					{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720},
				},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 60*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	series := hg.Series{ID: "ser-eof", Title: "自动连播剧", EpisodeCount: 2}
	eps := []hg.Episode{
		{ID: "ep-1", Index: 1, Duration: 60 * time.Second},
		{ID: "ep-2", Index: 2, Duration: 60 * time.Second},
	}
	a.hgOpenPlayer(series, eps, 0)
	tt.Frame()
	waitUntil(t, a, tt, "ep-1 ready", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil
	})

	mu.Lock()
	vp1 := (*opened)[0].player
	mu.Unlock()

	// Record progress on ep-1, then trigger EOF.
	vp1.setPos(30 * time.Second)
	a.hgCheckpoint(true)
	if entry, ok := a.hg.hist.Get("ser-eof"); !ok || entry.Positions["ep-1"] != 30*time.Second {
		t.Fatalf("expected ep-1 position saved before EOF: %+v", entry)
	}

	vp1.setErr(io.EOF)
	tt.Frame()

	waitUntil(t, a, tt, "auto-advanced to ep-2", func() bool {
		return a.hg.player.epIdx == 1 && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})

	// Completed ep-1 position must be cleared from HistoryStore.
	entry, ok := a.hg.hist.Get("ser-eof")
	if !ok {
		t.Fatal("expected history entry")
	}
	if _, stillHas := entry.Positions["ep-1"]; stillHas {
		t.Fatalf("expected ep-1 position cleared on EOF, got %v", entry.Positions)
	}

	// Now trigger EOF on final episode (ep-2).
	mu.Lock()
	vp2 := (*opened)[len(*opened)-1].player
	mu.Unlock()
	vp2.setErr(io.EOF)
	tt.Frame()

	if !a.hg.player.paused || !a.hg.player.finished {
		t.Fatalf("expected final episode to pause at end: paused=%v finished=%v", a.hg.player.paused, a.hg.player.finished)
	}
	if !tt.HasText("全剧播放完毕") && a.toast != "全剧播放完毕" {
		t.Fatalf("expected toast 全剧播放完毕, toast=%q texts=%q", a.toast, tt.Texts())
	}
}

func TestHGPlayerExpiredPrefetchAndFailedOpenReResolves(t *testing.T) {
	var rmu sync.Mutex
	ep1Attempt := 0
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			rmu.Lock()
			defer rmu.Unlock()
			if epID == "ep-1" {
				ep1Attempt++
				url := "https://cdn.example/ep1-stale.mp4"
				if ep1Attempt >= 2 {
					url = "https://cdn.example/ep1-fresh.mp4"
				}
				return hg.Playback{
					EpisodeID: epID,
					Duration:  60 * time.Second,
					ExpiresAt: time.Now().Add(10 * time.Minute),
					Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: url, Height: 720}},
				}, nil
			}
			return hg.Playback{
				EpisodeID: epID,
				Duration:  60 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/ep2-fresh.mp4", Height: 720}},
			}, nil
		},
	}

	a, tt, _, _ := newHGPlayerTestEnv(t, svc, 60*time.Second)
	var omu sync.Mutex
	var openURLs []string
	a.openHGStream = func(_ context.Context, rawURL string, opts media.StreamOptions, _ bool) (players, error) {
		omu.Lock()
		openURLs = append(openURLs, rawURL)
		omu.Unlock()
		if rawURL == "https://cdn.example/ep1-stale.mp4" {
			return players{}, errors.New("media: HTTP 403")
		}
		return players{vp: &fakeHGVideoPlayer{url: rawURL, opts: opts, dur: 60 * time.Second}}, nil
	}

	a.switchMode(modeHongguo)
	tt.Frame()

	series := hg.Series{ID: "ser-exp", Title: "过期重解析", EpisodeCount: 2}
	eps := []hg.Episode{
		{ID: "ep-1", Index: 1, Duration: 60 * time.Second},
		{ID: "ep-2", Index: 2, Duration: 60 * time.Second},
	}

	// Case 1: Initial stream URL fails (403) -> automatically re-resolves once and succeeds.
	a.hgOpenPlayer(series, eps, 0)
	tt.Frame()
	waitUntil(t, a, tt, "ep-1 recovered via fresh ResolvePlayback", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})
	if svc.resolveCount("ep-1") != 2 {
		t.Fatalf("expected 2 ResolvePlayback calls for ep-1 (initial + retry), got %d", svc.resolveCount("ep-1"))
	}

	// Case 2: Expired cached prefetch (ExpiresAt in the past) must trigger fresh ResolvePlayback.
	expiredPB := hg.Playback{
		EpisodeID: "ep-2",
		ExpiresAt: time.Now().Add(-5 * time.Minute),
		Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/ep2-expired.mp4", Height: 720}},
	}
	a.hg.player.prefetchEpID = "ep-2"
	a.hg.player.prefetchPB = &expiredPB

	a.hgPlayEpisode(1)
	tt.Frame()
	waitUntil(t, a, tt, "ep-2 re-resolved after expired cache", func() bool {
		return a.hg.player.epIdx == 1 && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})

	if svc.resolveCount("ep-2") != 1 {
		t.Fatalf("expected fresh ResolvePlayback for expired ep-2 prefetch, got %d", svc.resolveCount("ep-2"))
	}
	omu.Lock()
	lastURL := openURLs[len(openURLs)-1]
	omu.Unlock()
	if lastURL != "https://cdn.example/ep2-fresh.mp4" {
		t.Fatalf("expected fresh URL for ep-2, got %q", lastURL)
	}
}

func TestHGPlayerModeSwitchSuspendsReleasesAndResumes(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  120 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720}},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 120*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	series := hg.Series{ID: "ser-mode", Title: "模式切换续播", EpisodeCount: 2}
	eps := []hg.Episode{{ID: "ep-1", Index: 1, Duration: 120 * time.Second}}
	a.hgOpenPlayer(series, eps, 0)
	tt.Frame()
	waitUntil(t, a, tt, "ep-1 playing", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil
	})

	mu.Lock()
	vp1 := (*opened)[0].player
	mu.Unlock()
	vp1.setPos(42 * time.Second)
	tt.Frame()

	// Switch to Douyin -> checkpoints 42s and releases active player.
	a.switchMode(modeDouyin)
	tt.Frame()

	vp1.mu.Lock()
	closed1 := vp1.closed
	vp1.mu.Unlock()
	if !closed1 {
		t.Fatal("expected Hongguo player to close on switching to Douyin")
	}
	if a.hg.player == nil || a.hg.player.pl.vp != nil {
		t.Fatalf("expected player released while in Douyin: player=%+v", a.hg.player)
	}
	entry, ok := a.hg.hist.Get("ser-mode")
	if !ok || entry.Position != 42*time.Second {
		t.Fatalf("expected 42s checkpointed on leave, got %+v", entry)
	}

	// Switch back to Hongguo -> automatically reopens and seeks to 42s.
	a.switchMode(modeHongguo)
	tt.Frame()
	waitUntil(t, a, tt, "ep-1 resumed after returning to Hongguo", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})

	mu.Lock()
	if len(*opened) != 2 {
		mu.Unlock()
		t.Fatalf("expected 2 total openHGStream calls across suspend/resume, got %d", len(*opened))
	}
	vp2 := (*opened)[1].player
	mu.Unlock()

	if vp2.Position() != 42*time.Second || !vp2.Playing() {
		t.Fatalf("expected resumed player at 42s playing, got pos=%v playing=%v", vp2.Position(), vp2.Playing())
	}
}

func TestHGPlayerDashDualTrackUnsupportedNotice(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				AudioURL:  "https://cdn.example/audio.m4a",
				Duration:  3600 * time.Second,
				Streams:   []hg.Stream{{Definition: "1080p", URL: "https://cdn.example/video.m4s", Dash: true}},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 3600*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	a.hgOpenPlayer(hg.Series{ID: "mov-1", Title: "双轨电影"}, []hg.Episode{{ID: "ep-m1", Index: 1}}, 0)
	tt.Frame()

	waitUntil(t, a, tt, "dash unsupported surfaced", func() bool {
		return a.hg.player != nil && !a.hg.player.loading && a.hg.player.dashUnsupported
	})

	mu.Lock()
	nOpened := len(*opened)
	mu.Unlock()
	if nOpened != 0 {
		t.Fatalf("expected openHGStream NOT called for DASH dual-track stream, got %d calls", nOpened)
	}
	if !tt.HasText(hgDashUnsupportedMsg) {
		t.Fatalf("missing DASH unsupported notice in UI: %q", tt.Texts())
	}
}

func TestHGPlayerDanmakuRenderAndToggle(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  90 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720}},
			}, nil
		},
		danmakuFn: func(_ context.Context, seriesID, epID string, startMs int64, cursor string) (hg.DanmakuPage, error) {
			return hg.DanmakuPage{
				Items: []hg.Danmaku{
					{At: 8 * time.Second, Text: "高能预警弹幕"},
					{At: 9 * time.Second, Text: "太爽了吧"},
				},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 90*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	a.hgOpenPlayer(hg.Series{ID: "ser-dm", Title: "弹幕剧"}, []hg.Episode{{ID: "ep-dm1", Index: 1, Duration: 90 * time.Second}}, 0)
	tt.Frame()

	waitUntil(t, a, tt, "stream and danmaku loaded", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil && a.hg.player.dmLoaded
	})

	mu.Lock()
	vp := (*opened)[0].player
	mu.Unlock()
	vp.setPos(10 * time.Second)
	tt.Frame()
	if lanes := a.hg.player.dm.lane; len(lanes) != 2 {
		t.Fatalf("expected both bullets flying at 10s, lanes %v", lanes)
	}
	shown := slices.Clone(tt.Image().Pix)

	// Turning dmOff hides bullets immediately: the picture loses them.
	a.dmOff = true
	tt.Frame()
	hidden := tt.Image().Pix
	changed := 0
	for i := 0; i < len(shown); i += 4 {
		if shown[i] != hidden[i] || shown[i+1] != hidden[i+1] || shown[i+2] != hidden[i+2] {
			changed++
		}
	}
	if changed < 1000 { // the 弹幕 chip alone changes about 130
		t.Fatalf("expected the bullets' pixels gone when a.dmOff=true, %d changed", changed)
	}
}

func TestHGPlayerVolumeSpeedTheaterAndEscape(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  90 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720}},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 90*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	a.hgOpenPlayer(hg.Series{ID: "ser-ctl", Title: "控制测试"}, []hg.Episode{{ID: "ep-1", Index: 1, Duration: 90 * time.Second}}, 0)
	tt.Frame()
	waitUntil(t, a, tt, "player ready", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil
	})

	mu.Lock()
	vp := (*opened)[0].player
	mu.Unlock()

	a.setVolume(0.35, false)
	a.setSpeed(1.5)
	vp.mu.Lock()
	gotVol, gotMuted, gotSpeed := vp.volume, vp.muted, vp.speed
	vp.mu.Unlock()
	if gotVol != 0.35 || gotMuted || gotSpeed != 1.5 {
		t.Fatalf("volume/speed did not propagate: vol=%v muted=%v speed=%v", gotVol, gotMuted, gotSpeed)
	}

	a.setVolume(0.35, true)
	vp.mu.Lock()
	gotVol, gotMuted = vp.volume, vp.muted
	vp.mu.Unlock()
	if gotVol != 0 || !gotMuted {
		t.Fatalf("mute did not propagate: vol=%v muted=%v", gotVol, gotMuted)
	}

	// Space toggles pause/play in Hongguo player.
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if !a.hg.player.paused || vp.Playing() {
		t.Fatalf("Space did not pause Hongguo player: paused=%v playing=%v", a.hg.player.paused, vp.Playing())
	}

	// Enter theater mode: first Escape exits theater without popping player.
	a.theater = true
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.theater || !a.hgPlaying() || a.hg.player == nil {
		t.Fatalf("first Escape should exit theater only: theater=%v playing=%v player=%v", a.theater, a.hgPlaying(), a.hg.player)
	}

	// Second Escape pops hgPagePlayer and closes media.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.hgPlaying() || a.hg.player != nil {
		t.Fatalf("second Escape should pop player and clear controller: playing=%v player=%v", a.hgPlaying(), a.hg.player)
	}
	vp.mu.Lock()
	closed := vp.closed
	vp.mu.Unlock()
	if !closed {
		t.Fatal("Escape did not close active Hongguo video player")
	}
}

func TestHGTrackOneClickResumeFetchesEpisodesAndPlays(t *testing.T) {
	svc := &playerStubHGService{
		stubHGService: stubHGService{
			episodesFn: func(_ context.Context, seriesID string) ([]hg.Episode, error) {
				return []hg.Episode{
					{ID: "ep-1", Index: 1, Duration: 90 * time.Second},
					{ID: "ep-2", Index: 2, Duration: 90 * time.Second},
				}, nil
			},
		},
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  90 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720}},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 90*time.Second)
	a.switchMode(modeHongguo)
	tt.Frame()

	// Seed history on ep-2 at 27s and mark favorited.
	s := hg.Series{ID: "ser-track", Title: "追剧直达", EpisodeCount: 2}
	_, _ = a.hg.hist.ToggleFavorite(s)
	_ = a.hg.hist.RecordProgress(s, hg.Episode{ID: "ep-2", Index: 2}, 27*time.Second)

	a.hgSelectRoot(hgRootTrack)
	tt.Frame()
	if err := tt.Click("继续观看"); err != nil {
		t.Fatalf("click 继续观看: %v (texts=%q)", err, tt.Texts())
	}
	tt.Frame()

	waitUntil(t, a, tt, "track resume loaded ep-2 at 27s", func() bool {
		return a.hg.player != nil && a.hg.player.epIdx == 1 && a.hg.player.pl.vp != nil && !a.hg.player.loading
	})

	mu.Lock()
	vp := (*opened)[0].player
	mu.Unlock()
	if vp.Position() != 27*time.Second {
		t.Fatalf("expected resumed position 27s on ep-2, got %v", vp.Position())
	}
}

func TestHGPlayerDanmakuDrawerTabAndClickToSeek(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  120 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720}},
			}, nil
		},
		danmakuFn: func(_ context.Context, _, _ string, _ int64, _ string) (hg.DanmakuPage, error) {
			return hg.DanmakuPage{
				Total: 42,
				Items: []hg.Danmaku{
					{ID: "d1", At: 5 * time.Second, Text: "高能预警", Likes: 18},
					{ID: "d2", At: 35 * time.Second, Text: "这一段绝了", Likes: 99},
				},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 120*time.Second)
	a.dmOff = true // Screen bullets off: switching to 弹幕 tab must still load list.
	a.switchMode(modeHongguo)
	tt.Frame()

	series := hg.Series{ID: "ser-dm-tab", Title: "弹幕列表测试", EpisodeCount: 1}
	eps := []hg.Episode{{ID: "ep-1", Index: 1, Duration: 120 * time.Second}}
	a.hgOpenPlayer(series, eps, 0)
	tt.Frame()
	waitUntil(t, a, tt, "ep-1 playing", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil
	})
	a.hg.player.drawerOpen = true
	tt.Frame()

	if err := tt.Click("弹幕"); err != nil {
		t.Fatalf("click 弹幕 tab: %v (texts=%q)", err, tt.Texts())
	}
	tt.Frame()
	waitUntil(t, a, tt, "danmaku loaded in drawer", func() bool {
		return len(a.hg.player.danmaku) == 2
	})
	tt.Frame()

	if !slices.Contains(tt.Texts(), "这一段绝了") {
		t.Fatalf("expected danmaku item in drawer, texts=%q", tt.Texts())
	}
	if err := tt.Click("00:35 这一段绝了"); err != nil {
		t.Fatalf("click danmaku row to seek: %v (texts=%q)", err, tt.Texts())
	}
	tt.Frame()

	mu.Lock()
	vp := (*opened)[0].player
	mu.Unlock()
	if vp.Position() != 35*time.Second {
		t.Fatalf("expected player seeked to 35s after clicking danmaku row, got %v", vp.Position())
	}
}

func TestHGPlayerRendersDouyinAndHongguoEmojiAliases(t *testing.T) {
	svc := &playerStubHGService{
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  90 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720}},
			}, nil
		},
		danmakuFn: func(_ context.Context, _, _ string, _ int64, _ string) (hg.DanmakuPage, error) {
			return hg.DanmakuPage{
				Total: 2,
				Items: []hg.Danmaku{
					{ID: "e1", At: 1 * time.Second, Text: "陛下[笑哭][爱慕][爽][哭][吃瓜]", Likes: 25},
				},
			}, nil
		},
	}

	a, tt, opened, mu := newHGPlayerTestEnv(t, svc, 90*time.Second)
	a.emoji = map[string]string{
		"[笑哭]":   "https://emoji.test/xiaoku.png",
		"[色]":    "https://emoji.test/se.png",
		"[好开心]":  "https://emoji.test/kaixin.png",
		"[流泪]":   "https://emoji.test/liulei.png",
		"[吃瓜群众]": "https://emoji.test/chigua.png",
	}
	applyEmojiAliases(a.emoji)

	spans, pics := a.emojiSpans("陛下[笑哭][爱慕][爽][哭][吃瓜]", 17, 600)
	if len(pics) != 5 {
		t.Fatalf("expected 5 resolved emoji pics (1 direct + 4 Hongguo aliases), got %d (spans=%+v)", len(pics), spans)
	}

	a.switchMode(modeHongguo)
	tt.Frame()
	a.hgOpenPlayer(hg.Series{ID: "ser-emoji", Title: "表情测试", EpisodeCount: 1}, []hg.Episode{{ID: "ep-1", Index: 1, Duration: 90 * time.Second}}, 0)
	tt.Frame()
	waitUntil(t, a, tt, "emoji danmaku loaded", func() bool {
		return a.hg.player != nil && a.hg.player.pl.vp != nil && len(a.hg.player.danmaku) == 1
	})
	mu.Lock()
	(*opened)[0].player.setPos(2 * time.Second)
	mu.Unlock()
	a.hg.player.drawerOpen = true
	a.hg.player.drawerTab = hgDrawerTabDanmaku
	tt.Frame()
	if !slices.Contains(tt.Texts(), "陛下[笑哭][爱慕][爽][哭][吃瓜]") {
		t.Fatalf("expected emoji danmaku label rendered in stage/drawer, texts=%q", tt.Texts())
	}
}

func TestHGPlayerCommentsDrawerAndReplyThreads(t *testing.T) {
	svc := &playerStubHGService{
		stubHGService: stubHGService{
			epCommentsFn: func(_ context.Context, seriesID, epID, cursor string) (hg.CommentPage, error) {
				if epID == "ep-2" {
					return hg.CommentPage{
						Total: 1,
						Items: []hg.Comment{{ID: "c-ep2", Author: "二集观众", Text: "第二集反转绝了[爽]", Likes: 9}},
					}, nil
				}
				return hg.CommentPage{
					Total:   120,
					HasMore: false,
					Items: []hg.Comment{
						{ID: "c-1", Author: "热心观众", Text: "开头太炸了[爱慕]", Likes: 88, ReplyCount: 2, CreatedAt: time.Now().Add(-10 * time.Minute)},
					},
				}, nil
			},
			epRepliesFn: func(_ context.Context, seriesID, epID, commentID, cursor string) (hg.CommentPage, error) {
				if commentID != "c-1" {
					return hg.CommentPage{}, nil
				}
				if cursor == "" {
					return hg.CommentPage{
						Total:   2,
						HasMore: true,
						Cursor:  "10",
						Items: []hg.Comment{
							{ID: "r-1", Author: "回复人A", Text: "我也觉得[捂脸]", Likes: 5},
						},
					}, nil
				}
				return hg.CommentPage{
					Total:   2,
					HasMore: false,
					Items: []hg.Comment{
						{ID: "r-2", Author: "回复人B", ReplyTo: "回复人A", Text: "二刷打卡[赞]", Likes: 3},
					},
				}, nil
			},
			seriesCommentsFn: func(_ context.Context, seriesID, cursor string) (hg.CommentPage, error) {
				return hg.CommentPage{
					Total:      35,
					Score:      "9.4",
					ScoreCount: 1500,
					Items: []hg.Comment{
						{ID: "sr-1", Author: "专业剧评", Score: "10", Text: "全剧无尿点，节奏极佳", Likes: 210, ReplyCount: 1},
					},
				}, nil
			},
		},
		resolveFn: func(_ context.Context, epID string) (hg.Playback, error) {
			return hg.Playback{
				EpisodeID: epID,
				Duration:  90 * time.Second,
				ExpiresAt: time.Now().Add(10 * time.Minute),
				Streams:   []hg.Stream{{Definition: "720p", Codec: "h264", URL: "https://cdn.example/" + epID + ".mp4", Height: 720}},
			}, nil
		},
	}

	a, tt, _, _ := newHGPlayerTestEnv(t, svc, 90*time.Second)
	a.emoji = map[string]string{
		"[色]":   "https://emoji.test/se.png",
		"[捂脸]":  "https://emoji.test/wulian.png",
		"[好开心]": "https://emoji.test/kaixin.png",
		"[赞]":   "https://emoji.test/zan.png",
	}
	applyEmojiAliases(a.emoji)

	a.switchMode(modeHongguo)
	tt.Frame()
	a.hgOpenPlayer(
		hg.Series{ID: "7100000000000101", Title: "逆袭之局", EpisodeCount: 2},
		[]hg.Episode{
			{ID: "ep-1", Index: 1, Duration: 90 * time.Second},
			{ID: "ep-2", Index: 2, Duration: 90 * time.Second},
		},
		0,
	)
	tt.Frame()
	waitUntil(t, a, tt, "episode comments preloaded", func() bool {
		return a.hg.player != nil && a.hg.player.epComments != nil && len(a.hg.player.epComments.items) == 1
	})

	// Switch right drawer to 评论 tab.
	a.hg.player.drawerOpen = true
	tt.Frame()
	if err := tt.Click("评论 120"); err != nil {
		t.Fatalf("expected 评论 120 tab button: %v, texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if !slices.Contains(tt.Texts(), "开头太炸了[爱慕]") {
		t.Fatalf("expected episode comment rendered, texts=%q", tt.Texts())
	}

	// Expand reply thread and load next page of replies.
	if err := tt.Click("展开 2 条回复"); err != nil {
		t.Fatalf("expected expand replies button: %v, texts=%q", err, tt.Texts())
	}
	waitUntil(t, a, tt, "first reply loaded", func() bool {
		th := a.hg.player.epComments.threads["c-1"]
		return th != nil && len(th.items) == 1
	})
	tt.Frame()
	if !slices.Contains(tt.Texts(), "我也觉得[捂脸]") {
		t.Fatalf("expected first reply rendered, texts=%q", tt.Texts())
	}
	if err := tt.Click("展开更多回复"); err != nil {
		t.Fatalf("expected load more replies button: %v, texts=%q", err, tt.Texts())
	}
	waitUntil(t, a, tt, "second reply loaded", func() bool {
		th := a.hg.player.epComments.threads["c-1"]
		return th != nil && len(th.items) == 2
	})
	tt.Frame()
	if !slices.Contains(tt.Texts(), "回复人B ▸ 回复人A") || !slices.Contains(tt.Texts(), "二刷打卡[赞]") {
		t.Fatalf("expected nested reply target + text rendered, texts=%q", tt.Texts())
	}

	// Switch to 剧评 sub-tab and verify series average score + review content.
	if err := tt.Click("剧评"); err != nil {
		t.Fatalf("expected 剧评 sub-tab pill: %v, texts=%q", err, tt.Texts())
	}
	waitUntil(t, a, tt, "series reviews loaded", func() bool {
		return a.hg.player.seriesComments != nil && len(a.hg.player.seriesComments.items) == 1
	})
	tt.Frame()
	texts := tt.Texts()
	if !slices.Contains(texts, "9.4 分") || !slices.Contains(texts, "全剧无尿点，节奏极佳") {
		t.Fatalf("expected series score and review rendered, texts=%q", texts)
	}

	// Switching to episode 2 refreshes episode comments.
	a.hg.player.commentTab = hgCommentTabEpisode
	a.hgPlayEpisode(1)
	waitUntil(t, a, tt, "episode 2 comments loaded", func() bool {
		return a.hg.player.epComments != nil && len(a.hg.player.epComments.items) == 1 && a.hg.player.epComments.items[0].ID == "c-ep2"
	})
	tt.Frame()
	if !slices.Contains(tt.Texts(), "第二集反转绝了[爽]") {
		t.Fatalf("expected episode 2 comment rendered, texts=%q", tt.Texts())
	}
}

func TestLiveHongguoCENCPlayback(t *testing.T) {
	if os.Getenv("HG_LIVE_PLAYBACK") == "" {
		t.Skip("set HG_LIVE_PLAYBACK=1 to run live Hongguo CENC playback test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := hg.New(hg.Config{ProfileDir: t.TempDir(), Direct: true})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := c.ResolvePlayback(ctx, "7697465535398677529")
	if err != nil {
		t.Fatalf("ResolvePlayback: %v", err)
	}
	t.Logf("Resolved: protected=%v streams=%d keyLen=%d dur=%v", pb.Protected, len(pb.Streams), len(pb.KeyHex), pb.Duration)
	if len(pb.Streams) == 0 {
		t.Fatal("no streams returned")
	}
	var opts media.StreamOptions
	if pb.Protected {
		key, err := hg.DecodeKeyHex(pb.KeyHex)
		if err != nil {
			t.Fatalf("DecodeKeyHex: %v", err)
		}
		opts.DecryptKey = key
	}
	chosen, cands := hg.SelectStream(pb, "智能", media.PreferH264)
	t.Logf("Selected: def=%s codec=%s %dx%d cands=%d", chosen.Definition, chosen.Codec, chosen.Width, chosen.Height, len(cands))
	live, err := media.OpenNativeWithOptions(ctx, cands[0], opts, true)
	if err != nil {
		t.Fatalf("OpenNativeWithOptions: %v", err)
	}
	defer live.Close()
	w, h, err := live.WaitReady()
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	t.Logf("AVFoundation WaitReady: %dx%d", w, h)
	if w <= 0 || h <= 0 {
		t.Fatalf("invalid frame size %dx%d", w, h)
	}
}
