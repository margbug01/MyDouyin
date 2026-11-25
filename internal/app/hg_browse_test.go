package app

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/sdk"
	"github.com/egoist/mygo/ui"

	"douyin/internal/hg"
)

type stubHGService struct {
	mu sync.Mutex

	discoverFn    func(ctx context.Context, channel, cursor string) (hg.Page[hg.Series], error)
	searchFn      func(ctx context.Context, query, cursor string, filters []string) (hg.Page[hg.Series], error)
	suggestFn     func(ctx context.Context, prefix string) ([]string, error)
	hotSearchFn   func(ctx context.Context) (hg.HotSearch, error)
	shortRankFn   func(ctx context.Context, content, board, cursor string) (hg.ShortRankPage, error)
	filterPanelFn func(ctx context.Context, genre string) ([]hg.FilterRow, error)
	filterFn      func(ctx context.Context, genre string, selected map[string]string, cursor string) (hg.Page[hg.Series], error)
	newReleasesFn func(ctx context.Context, date, cursor string) (hg.NewReleasePage, error)
	getSeriesFn   func(ctx context.Context, seriesID string) (hg.Series, error)
	episodesFn    func(ctx context.Context, seriesID string) ([]hg.Episode, error)
	seasonsFn     func(ctx context.Context, seriesID string) ([]hg.Season, error)
	recommendFn   func(ctx context.Context, seriesID string) ([]hg.Series, error)
	personFn         func(ctx context.Context, userID string) (hg.Person, error)
	personWorksFn    func(ctx context.Context, userID, cursor string) (hg.Page[hg.Series], error)
	epCommentsFn     func(ctx context.Context, seriesID, episodeID, cursor string) (hg.CommentPage, error)
	epRepliesFn      func(ctx context.Context, seriesID, episodeID, commentID, cursor string) (hg.CommentPage, error)
	seriesCommentsFn func(ctx context.Context, seriesID, cursor string) (hg.CommentPage, error)
	seriesRepliesFn  func(ctx context.Context, seriesID, commentID, cursor string) (hg.CommentPage, error)
	resetFn          func(ctx context.Context) error

	discoverCalls []string
	hotCalls      int
	searchCalls   []struct {
		query   string
		filters []string
	}
}

func (s *stubHGService) Discover(ctx context.Context, channel, cursor string) (hg.Page[hg.Series], error) {
	s.mu.Lock()
	s.discoverCalls = append(s.discoverCalls, channel+":"+cursor)
	fn := s.discoverFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, channel, cursor)
	}
	return hg.Page[hg.Series]{}, nil
}

func (s *stubHGService) Search(ctx context.Context, query, cursor string, filters []string) (hg.Page[hg.Series], error) {
	s.mu.Lock()
	s.searchCalls = append(s.searchCalls, struct {
		query   string
		filters []string
	}{query: query, filters: slices.Clone(filters)})
	fn := s.searchFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, query, cursor, filters)
	}
	return hg.Page[hg.Series]{}, nil
}

func (s *stubHGService) Suggest(ctx context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	fn := s.suggestFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, prefix)
	}
	return nil, nil
}

func (s *stubHGService) HotSearch(ctx context.Context) (hg.HotSearch, error) {
	s.mu.Lock()
	s.hotCalls++
	fn := s.hotSearchFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return hg.HotSearch{}, nil
}

func (s *stubHGService) ShortRank(ctx context.Context, content, board, cursor string) (hg.ShortRankPage, error) {
	s.mu.Lock()
	fn := s.shortRankFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, content, board, cursor)
	}
	return hg.ShortRankPage{}, nil
}

func (s *stubHGService) ShortFilterPanel(ctx context.Context, genre string) ([]hg.FilterRow, error) {
	s.mu.Lock()
	fn := s.filterPanelFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, genre)
	}
	return nil, nil
}

func (s *stubHGService) ShortFilter(ctx context.Context, genre string, selected map[string]string, cursor string) (hg.Page[hg.Series], error) {
	s.mu.Lock()
	fn := s.filterFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, genre, selected, cursor)
	}
	return hg.Page[hg.Series]{}, nil
}

func (s *stubHGService) NewReleases(ctx context.Context, date, cursor string) (hg.NewReleasePage, error) {
	s.mu.Lock()
	fn := s.newReleasesFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, date, cursor)
	}
	return hg.NewReleasePage{}, nil
}

func (s *stubHGService) GetSeries(ctx context.Context, seriesID string) (hg.Series, error) {
	s.mu.Lock()
	fn := s.getSeriesFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID)
	}
	return hg.Series{ID: seriesID}, nil
}

func (s *stubHGService) ListEpisodes(ctx context.Context, seriesID string) ([]hg.Episode, error) {
	s.mu.Lock()
	fn := s.episodesFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID)
	}
	return nil, nil
}

func (s *stubHGService) Seasons(ctx context.Context, seriesID string) ([]hg.Season, error) {
	s.mu.Lock()
	fn := s.seasonsFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID)
	}
	return nil, nil
}

func (s *stubHGService) SeriesRecommend(ctx context.Context, seriesID string) ([]hg.Series, error) {
	s.mu.Lock()
	fn := s.recommendFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID)
	}
	return nil, nil
}

func (s *stubHGService) Person(ctx context.Context, userID string) (hg.Person, error) {
	s.mu.Lock()
	fn := s.personFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, userID)
	}
	return hg.Person{UserID: userID}, nil
}

func (s *stubHGService) PersonWorks(ctx context.Context, userID, cursor string) (hg.Page[hg.Series], error) {
	s.mu.Lock()
	fn := s.personWorksFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, userID, cursor)
	}
	return hg.Page[hg.Series]{}, nil
}

func (s *stubHGService) ResolvePlayback(context.Context, string) (hg.Playback, error) {
	return hg.Playback{}, nil
}

func (s *stubHGService) ListDanmaku(context.Context, string, string, int64, string) (hg.DanmakuPage, error) {
	return hg.DanmakuPage{}, nil
}

func (s *stubHGService) ListEpisodeComments(ctx context.Context, seriesID, episodeID, cursor string) (hg.CommentPage, error) {
	s.mu.Lock()
	fn := s.epCommentsFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID, episodeID, cursor)
	}
	return hg.CommentPage{}, nil
}

func (s *stubHGService) ListEpisodeReplies(ctx context.Context, seriesID, episodeID, commentID, cursor string) (hg.CommentPage, error) {
	s.mu.Lock()
	fn := s.epRepliesFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID, episodeID, commentID, cursor)
	}
	return hg.CommentPage{}, nil
}

func (s *stubHGService) ListSeriesComments(ctx context.Context, seriesID, cursor string) (hg.CommentPage, error) {
	s.mu.Lock()
	fn := s.seriesCommentsFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID, cursor)
	}
	return hg.CommentPage{}, nil
}

func (s *stubHGService) ListSeriesReplies(ctx context.Context, seriesID, commentID, cursor string) (hg.CommentPage, error) {
	s.mu.Lock()
	fn := s.seriesRepliesFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, seriesID, commentID, cursor)
	}
	return hg.CommentPage{}, nil
}

func (s *stubHGService) ResetDevice(ctx context.Context) error {
	s.mu.Lock()
	fn := s.resetFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (s *stubHGService) Image(context.Context, string) ([]byte, error) { return nil, nil }
func (s *stubHGService) Reconfigure(hg.Config) error                   { return nil }

func newHGTestApp(t *testing.T, svc hg.Service) (*app, *ui.Tester) {
	t.Helper()
	dir := t.TempDir()
	a := withRoots(&app{
		pages:        []*page{stub("推荐", 1)},
		posted:       make(chan func(), 64),
		settingsFile: filepath.Join(dir, "settings.json"),
		hgDirect:     true,
		newHGService: func(hg.Config) (hg.Service, error) {
			return svc, nil
		},
	})
	return a, newTester(a)
}

func waitUntil(t *testing.T, a *app, tt *ui.Tester, desc string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case fn := <-a.posted:
			fn()
			tt.Frame()
		case <-deadline:
			t.Fatalf("timed out waiting for %s; texts=%q", desc, tt.Texts())
		}
	}
}

func TestHGLazyDiscoverHotSearchAndThemes(t *testing.T) {
	svc := &stubHGService{
		discoverFn: func(_ context.Context, channel, cursor string) (hg.Page[hg.Series], error) {
			return hg.Page[hg.Series]{
				Items: []hg.Series{
					{ID: "s1", Title: "逆袭龙王", Category: "都市", EpisodeCount: 80, Score: "2550万热度"},
				},
			}, nil
		},
		hotSearchFn: func(context.Context) (hg.HotSearch, error) {
			return hg.HotSearch{
				Guesses: []string{"战神归来"},
				Boards: []hg.HotBoard{{
					Name:  "短剧热搜榜",
					Items: []hg.HotItem{{Word: "闪婚总裁", Score: "1800万"}},
				}},
			}, nil
		},
	}

	a, tt := newHGTestApp(t, svc)
	tt.Frame()
	if a.hg != nil {
		t.Fatal("expected lazy HG init to stay nil in Douyin mode")
	}

	a.switchMode(modeHongguo)
	a.hgSelectRoot(hgRootDiscover)
	tt.Frame()
	waitUntil(t, a, tt, "discover and hot search loaded", func() bool {
		return a.hg != nil && a.hg.hotLoaded && a.hg.discoverList("推荐").loaded
	})

	if !tt.HasText("逆袭龙王") || !tt.HasText("全80集") || !tt.HasText("2550万热度") {
		t.Fatalf("discover card missing expected text: %q", tt.Texts())
	}

	// Focus search box to verify HotSearch dropdown renders guesses and board.
	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("猜你想搜") || !tt.HasText("战神归来") || !tt.HasText("闪婚总裁") {
		t.Fatalf("hot search dropdown missing items: %q", tt.Texts())
	}

	// Render cleanly in both light and dark themes.
	a.orbit.theme = "light"
	tt.Frame()
	a.orbit.theme = "dark"
	tt.Frame()
	if !tt.HasText("逆袭龙王") {
		t.Fatalf("dark theme frame lost content: %q", tt.Texts())
	}
}

func TestHGDiscoverChannelCacheAndDeduplication(t *testing.T) {
	svc := &stubHGService{
		discoverFn: func(_ context.Context, channel, cursor string) (hg.Page[hg.Series], error) {
			switch channel {
			case "推荐":
				if cursor == "" {
					return hg.Page[hg.Series]{
						Items: []hg.Series{
							{ID: "r1", Title: "推荐一"},
							{ID: "r2", Title: "推荐二"},
						},
						Cursor:  "c2",
						HasMore: true,
					}, nil
				}
				return hg.Page[hg.Series]{
					Items: []hg.Series{
						{ID: "r2", Title: "推荐二-重复"},
						{ID: "r3", Title: "推荐三"},
					},
					HasMore: false,
				}, nil
			case "漫剧":
				return hg.Page[hg.Series]{
					Items: []hg.Series{{ID: "m1", Title: "漫剧之王"}},
				}, nil
			}
			return hg.Page[hg.Series]{}, nil
		},
	}

	a, tt := newHGTestApp(t, svc)
	a.switchMode(modeHongguo)
	a.hgSelectRoot(hgRootDiscover)
	tt.Frame()
	waitUntil(t, a, tt, "推荐 paginated and deduplicated", func() bool {
		l := a.hg.discoverList("推荐")
		return l.loaded && !l.loading && !l.hasMore
	})

	recList := a.hg.discoverList("推荐")
	if len(recList.items) != 3 {
		t.Fatalf("expected 3 deduplicated items in 推荐, got %d: %+v", len(recList.items), recList.items)
	}
	if recList.items[0].ID != "r1" || recList.items[1].ID != "r2" || recList.items[2].ID != "r3" {
		t.Fatalf("unexpected dedup order: %+v", recList.items)
	}

	// Switch to 漫剧 (already warmed by orbit startup), then back to 推荐:
	// both channels must reuse their in-memory cache without extra Discover calls.
	svc.mu.Lock()
	recCallsBefore := len(svc.discoverCalls)
	svc.mu.Unlock()

	if err := tt.Click("漫剧"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "漫剧 loaded", func() bool {
		return a.hg.discoverList("漫剧").loaded
	})
	if !tt.HasText("漫剧之王") {
		t.Fatalf("expected 漫剧 content, got %q", tt.Texts())
	}

	if err := tt.Click("推荐"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("推荐一") || !tt.HasText("推荐三") {
		t.Fatalf("cached 推荐 items not visible after switch back: %q", tt.Texts())
	}
	svc.mu.Lock()
	totalCalls := len(svc.discoverCalls)
	svc.mu.Unlock()
	if totalCalls != recCallsBefore {
		t.Fatalf("expected cached channel switches to trigger 0 extra Discover calls, got %d -> %d (%v)", recCallsBefore, totalCalls, svc.discoverCalls)
	}
}

func TestHGRankBoardSwitchSeriesAndActorNavigation(t *testing.T) {
	svc := &stubHGService{
		shortRankFn: func(_ context.Context, content, board, cursor string) (hg.ShortRankPage, error) {
			contents := []hg.RankContent{
				{
					ID:       "drama",
					Name:     "短剧",
					Selected: true,
					Boards: []hg.RankBoard{
						{ID: "hot", Name: "热播榜", Selected: true},
						{ID: "new", Name: "新剧榜"},
					},
				},
				{
					ID:   "actor",
					Name: "演员",
					Boards: []hg.RankBoard{
						{ID: "star", Name: "人气榜", Selected: true},
					},
				},
			}
			switch {
			case content == "actor":
				return hg.ShortRankPage{
					Contents: contents,
					Content:  "actor",
					Board:    "star",
					Items: []hg.RankItem{
						{Position: 1, UserID: "u99", Actor: "柯淳", Score: "999万", Intro: "人气男主"},
					},
				}, nil
			case board == "new":
				return hg.ShortRankPage{
					Contents: contents,
					Content:  "drama",
					Board:    "new",
					Items: []hg.RankItem{
						{Position: 1, SeriesID: "s-new", Title: "新晋爆款", Score: "500万", EpisodeCount: 60},
					},
				}, nil
			default:
				return hg.ShortRankPage{
					Contents: contents,
					Content:  "drama",
					Board:    "hot",
					Items: []hg.RankItem{
						{Position: 1, SeriesID: "s-hot", Title: "热播第一", Score: "3000万", EpisodeCount: 100},
					},
				}, nil
			}
		},
		getSeriesFn: func(_ context.Context, id string) (hg.Series, error) {
			return hg.Series{ID: id, Title: "新晋爆款", Abstract: "详情简介", EpisodeCount: 60}, nil
		},
		personFn: func(_ context.Context, uid string) (hg.Person, error) {
			return hg.Person{UserID: uid, Name: "柯淳", WorksCount: 12, Intro: "代表作多多"}, nil
		},
		personWorksFn: func(_ context.Context, uid, cursor string) (hg.Page[hg.Series], error) {
			return hg.Page[hg.Series]{
				Items: []hg.Series{{ID: "w1", Title: "好一个乖乖女"}},
			}, nil
		},
	}

	a, tt := newHGTestApp(t, svc)
	a.switchMode(modeHongguo)
	tt.Frame()
	if err := tt.Click("榜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "rank hot loaded", func() bool {
		return a.hg.rankLoaded && !a.hg.rankLoading && len(a.hg.rankItems) == 1
	})
	if !tt.HasText("热播第一") {
		t.Fatalf("expected 热播第一 in rank view: %q", tt.Texts())
	}

	// Switch to 新剧榜 and click series -> hgPageDetail.
	if err := tt.Click("新剧榜"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "rank new board loaded", func() bool {
		return a.hg.rankBoard == "new" && a.hg.rankLoaded && !a.hg.rankLoading
	})
	if err := tt.Click("新晋爆款"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if top := a.hg.top(); top == nil || top.kind != hgPageDetail || top.seriesID != "s-new" {
		t.Fatalf("expected hgPageDetail for s-new, got %+v", top)
	}

	// Pop back to rank, switch to 演员 tab, click actor -> hgPagePerson.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if err := tt.Click("演员"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "actor rank loaded", func() bool {
		return a.hg.rankContent == "actor" && a.hg.rankLoaded && !a.hg.rankLoading
	})
	if err := tt.Click("柯淳"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "person works loaded", func() bool {
		top := a.hg.top()
		return top != nil && top.kind == hgPagePerson && top.works.loaded && !top.personLoading
	})
	if !tt.HasText("好一个乖乖女") || !tt.HasText("12 部作品") {
		t.Fatalf("person page missing expected profile/works: %q", tt.Texts())
	}
}

func TestHGFilterPanelReloadAndDropStaleGen(t *testing.T) {
	releaseFirst := make(chan struct{})
	firstEntered := make(chan struct{})

	svc := &stubHGService{
		filterPanelFn: func(context.Context, string) ([]hg.FilterRow, error) {
			return []hg.FilterRow{
				{
					Key:  "theme",
					Name: "题材",
					Options: []hg.FilterOption{
						{Name: "全部题材", Value: "", Selected: true},
						{Name: "古装", Value: "ancient"},
						{Name: "悬疑", Value: "mystery"},
					},
				},
			}, nil
		},
		filterFn: func(_ context.Context, genre string, selected map[string]string, cursor string) (hg.Page[hg.Series], error) {
			switch selected["theme"] {
			case "ancient":
				close(firstEntered)
				<-releaseFirst
				return hg.Page[hg.Series]{
					Items: []hg.Series{{ID: "old", Title: "过期古装剧"}},
				}, nil
			case "mystery":
				return hg.Page[hg.Series]{
					Items: []hg.Series{{ID: "new", Title: "最新悬疑剧"}},
				}, nil
			default:
				return hg.Page[hg.Series]{
					Items: []hg.Series{{ID: "all", Title: "默认精选剧"}},
				}, nil
			}
		},
	}

	a, tt := newHGTestApp(t, svc)
	a.switchMode(modeHongguo)
	tt.Frame()
	if err := tt.Click("分类"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "filter panel and initial results loaded", func() bool {
		return a.hg.filterPanelLoaded && a.hg.filterResult.loaded
	})
	if !tt.HasText("默认精选剧") {
		t.Fatalf("expected initial filter item: %q", tt.Texts())
	}

	// Click 古装 (slow request), wait until it is in flight, then click 悬疑 (fast request).
	if err := tt.Click("古装"); err != nil {
		t.Fatal(err)
	}
	<-firstEntered
	if err := tt.Click("悬疑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "mystery filter loaded", func() bool {
		return a.hg.filterResult.loaded && !a.hg.filterResult.loading && len(a.hg.filterResult.items) == 1
	})
	if a.hg.filterResult.items[0].ID != "new" {
		t.Fatalf("expected mystery result, got %+v", a.hg.filterResult.items)
	}

	// Now unblock stale 古装 request and drain its posted callback: must be dropped!
	close(releaseFirst)
	deadline := time.After(2 * time.Second)
	select {
	case fn := <-a.posted:
		fn()
		tt.Frame()
	case <-deadline:
		t.Fatal("stale filter response never posted")
	}
	if len(a.hg.filterResult.items) != 1 || a.hg.filterResult.items[0].ID != "new" || tt.HasText("过期古装剧") {
		t.Fatalf("stale filter response overwrote newer results: %+v", a.hg.filterResult.items)
	}
}

func TestHGSearchSuggestFilterAndEscape(t *testing.T) {
	svc := &stubHGService{
		suggestFn: func(_ context.Context, prefix string) ([]string, error) {
			return []string{prefix + "总裁", prefix + "战神"}, nil
		},
		searchFn: func(_ context.Context, query, cursor string, filters []string) (hg.Page[hg.Series], error) {
			if len(filters) == 1 && filters[0] == "漫剧" {
				return hg.Page[hg.Series]{
					Items: []hg.Series{{ID: "sm1", Title: "霸总漫剧版"}},
				}, nil
			}
			return hg.Page[hg.Series]{
				Items: []hg.Series{{ID: "sa1", Title: "霸总真人版"}},
			}, nil
		},
	}

	a, tt := newHGTestApp(t, svc)
	a.switchMode(modeHongguo)
	a.hg.suggestDelay = 10 * time.Millisecond
	tt.Frame()

	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Type("霸总")
	tt.Frame()

	waitUntil(t, a, tt, "suggest loaded", func() bool {
		return a.hg.sugFor == "霸总" && len(a.hg.sugs) == 2
	})
	if !tt.HasText("霸总总裁") || !tt.HasText("霸总战神") {
		t.Fatalf("expected suggest dropdown items: %q", tt.Texts())
	}

	// Submit search via Enter -> pushes hgPageSearch.
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	waitUntil(t, a, tt, "search results loaded", func() bool {
		top := a.hg.top()
		return top != nil && top.kind == hgPageSearch && top.results.loaded
	})
	if !tt.HasText("霸总真人版") {
		t.Fatalf("expected search result 霸总真人版: %q", tt.Texts())
	}

	// Toggle search filter chip to 漫剧 -> reloads with filters=["漫剧"].
	if err := tt.Click("漫剧"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "filtered search loaded", func() bool {
		top := a.hg.top()
		return top != nil && top.searchFilter == "漫剧" && top.results.loaded && !top.results.loading
	})
	if !tt.HasText("霸总漫剧版") {
		t.Fatalf("expected filtered search result 霸总漫剧版: %q", tt.Texts())
	}

	// Escape pops back to root.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(a.hg.stack) != 0 {
		t.Fatalf("Escape did not pop hgPageSearch: stack=%d", len(a.hg.stack))
	}
}

func TestHGDetailFavoritePersistencePlayerAndTrackView(t *testing.T) {
	svc := &stubHGService{
		discoverFn: func(context.Context, string, string) (hg.Page[hg.Series], error) {
			return hg.Page[hg.Series]{
				Items: []hg.Series{{ID: "d1", Title: "太奶奶驾到", EpisodeCount: 45}},
			}, nil
		},
		getSeriesFn: func(_ context.Context, id string) (hg.Series, error) {
			return hg.Series{
				ID:           id,
				Title:        "太奶奶驾到",
				Score:        "9.6分",
				StatusText:   "全45集",
				Category:     "喜剧",
				Abstract:     "穿越喜剧爆款",
				Actors:       []string{"李柯以"},
				EpisodeCount: 45,
			}, nil
		},
		episodesFn: func(_ context.Context, id string) ([]hg.Episode, error) {
			eps := make([]hg.Episode, 45)
			for i := range eps {
				eps[i] = hg.Episode{ID: "ep-" + string(rune('a'+i%26)), Index: i + 1}
			}
			return eps, nil
		},
		seasonsFn: func(_ context.Context, id string) ([]hg.Season, error) {
			return []hg.Season{
				{SeriesID: "d1", Label: "第一季", Title: "太奶奶驾到", Current: true},
				{SeriesID: "d2", Label: "第二季", Title: "太奶奶驾到2"},
			}, nil
		},
	}

	a, tt := newHGTestApp(t, svc)
	a.switchMode(modeHongguo)
	a.hgSelectRoot(hgRootDiscover)
	tt.Frame()
	waitUntil(t, a, tt, "discover loaded", func() bool {
		return a.hg.discoverList("推荐").loaded
	})

	if err := tt.Click("太奶奶驾到"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "detail loaded", func() bool {
		top := a.hg.top()
		return top != nil && top.kind == hgPageDetail && !top.loading && !top.epLoading && !top.seasonsLoading
	})

	for _, want := range []string{"太奶奶驾到", "9.6分", "全45集", "第一季", "第二季", "1-30", "31-45"} {
		if !tt.HasText(want) {
			t.Fatalf("detail view missing %q: %q", want, tt.Texts())
		}
	}

	// Click 追剧 -> persists to HistoryStore.
	if err := tt.Click("追剧"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.hg.hist.IsFavorited("d1") || !tt.HasText("已追剧") {
		t.Fatalf("expected series d1 favorited: fav=%v texts=%q", a.hg.hist.IsFavorited("d1"), tt.Texts())
	}

	// Verify persistence on disk by reopening HistoryStore.
	reopened, err := hg.NewHistoryStore(filepath.Join(a.hgDir(), "hongguo-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.IsFavorited("d1") {
		t.Fatal("favorite was not persisted to hongguo-history.json")
	}

	// Clicking 开始播放 navigates cleanly to hgPagePlayer (PR-5 hook).
	if err := tt.Click("开始播放"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if top := a.hg.top(); top == nil || top.kind != hgPagePlayer || top.series.ID != "d1" || len(top.episodes) != 45 {
		t.Fatalf("expected hgPagePlayer, got %+v", top)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	// Navigate to 追剧 root view and confirm favorited drama shows up.
	a.hgSelectRoot(hgRootTrack)
	tt.Frame()
	if !tt.HasText("太奶奶驾到") || !tt.HasText("我的追剧 (1)") {
		t.Fatalf("追剧 root view missing favorited series: %q", tt.Texts())
	}
}

func TestHGErrorBannerAnonymousLimitedAndResetRetry(t *testing.T) {
	fail := true
	var mu sync.Mutex
	svc := &stubHGService{
		discoverFn: func(context.Context, string, string) (hg.Page[hg.Series], error) {
			mu.Lock()
			shouldFail := fail
			mu.Unlock()
			if shouldFail {
				return hg.Page[hg.Series]{}, sdk.ErrAnonymousLimited
			}
			return hg.Page[hg.Series]{
				Items: []hg.Series{{ID: "ok1", Title: "恢复后的剧集"}},
			}, nil
		},
		resetFn: func(context.Context) error {
			mu.Lock()
			fail = false
			mu.Unlock()
			return nil
		},
	}

	a, tt := newHGTestApp(t, svc)
	a.switchMode(modeHongguo)
	a.hgSelectRoot(hgRootDiscover)
	tt.Frame()
	waitUntil(t, a, tt, "discover error classified", func() bool {
		return a.hg.discoverList("推荐").err.Kind == hg.ErrKindAnonymousLimited
	})

	errInfo := a.hg.discoverList("推荐").err
	if !errInfo.CanResetDevice || !errInfo.CanOpenSettings {
		t.Fatalf("expected CanResetDevice & CanOpenSettings true, got %+v", errInfo)
	}
	for _, want := range []string{"重试", "重置匿名设备", "红果设置"} {
		if !tt.HasText(want) {
			t.Fatalf("error banner missing action %q: %q", want, tt.Texts())
		}
	}

	// Clicking 重置匿名设备 resets device and automatically retries the failing view.
	if err := tt.Click("重置匿名设备"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitUntil(t, a, tt, "discover recovered after reset", func() bool {
		l := a.hg.discoverList("推荐")
		return l.loaded && l.err.Kind == hg.ErrKindNone && len(l.items) == 1
	})
	if !tt.HasText("恢复后的剧集") {
		t.Fatalf("expected recovered series after reset: %q", tt.Texts())
	}
}

func TestHGDetailPageRendersSeriesReviewsAndReplies(t *testing.T) {
	svc := &stubHGService{
		getSeriesFn: func(_ context.Context, id string) (hg.Series, error) {
			return hg.Series{ID: id, Title: "测试剧评剧", EpisodeCount: 1}, nil
		},
		episodesFn: func(_ context.Context, _ string) ([]hg.Episode, error) {
			return []hg.Episode{{ID: "ep-1", Index: 1}}, nil
		},
		seriesCommentsFn: func(_ context.Context, _ string, _ string) (hg.CommentPage, error) {
			return hg.CommentPage{
				Total:      18,
				Score:      "9.5",
				ScoreCount: 2300,
				Items: []hg.Comment{
					{ID: "rev-1", Author: "资深剧迷", Score: "10", Text: "制作精良值得追", Likes: 66, ReplyCount: 1},
				},
			}, nil
		},
		seriesRepliesFn: func(_ context.Context, _, commentID, _ string) (hg.CommentPage, error) {
			if commentID != "rev-1" {
				return hg.CommentPage{}, nil
			}
			return hg.CommentPage{
				Total: 1,
				Items: []hg.Comment{{ID: "srep-1", Author: "附议者", Text: "确实上头", Likes: 4}},
			}, nil
		},
	}

	a, tt := newHGTestApp(t, svc)
	a.switchMode(modeHongguo)
	tt.Frame()
	a.hgOpenDetail(hg.Series{ID: "7100000000000999", Title: "测试剧评剧"})
	waitUntil(t, a, tt, "detail reviews loaded", func() bool {
		top := a.hg.top()
		return top != nil && top.reviews != nil && len(top.reviews.items) == 1
	})
	tt.Frame()
	if !tt.HasText("9.5 分") || !tt.HasText("制作精良值得追") {
		t.Fatalf("expected detail page review & score rendered, texts=%q", tt.Texts())
	}
	if err := tt.Click("展开 1 条回复"); err != nil {
		t.Fatalf("expected expand button on detail review: %v, texts=%q", err, tt.Texts())
	}
	waitUntil(t, a, tt, "detail review reply loaded", func() bool {
		th := a.hg.top().reviews.threads["rev-1"]
		return th != nil && len(th.items) == 1
	})
	tt.Frame()
	if !tt.HasText("确实上头") {
		t.Fatalf("expected detail review reply rendered, texts=%q", tt.Texts())
	}
}
