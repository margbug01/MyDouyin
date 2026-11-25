package app

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/hg"
)

// hgRootKind selects which top-level MyHongguo destination is active in the dock.
type hgRootKind uint8

const (
	hgRootDiscover hgRootKind = iota
	hgRootRank
	hgRootFilter
	hgRootNew
	hgRootTrack
)

func (r hgRootKind) title() string {
	switch r {
	case hgRootDiscover:
		return "发现"
	case hgRootRank:
		return "榜单"
	case hgRootFilter:
		return "分类"
	case hgRootNew:
		return "上新"
	case hgRootTrack:
		return "追剧"
	default:
		return "发现"
	}
}

// hgPageKind discriminates pushed sub-pages over a Hongguo root.
type hgPageKind uint8

const (
	hgPageRoot hgPageKind = iota
	hgPageSearch
	hgPageDetail
	hgPagePerson
	hgPagePlayer
)

var (
	hgDiscoverChannels = []string{"推荐", "真人剧", "漫剧"}
	hgSearchFilters    = []struct {
		label string
		value string
	}{
		{label: "全部", value: ""},
		{label: "真人剧", value: "真人剧"},
		{label: "漫剧", value: "漫剧"},
		{label: "AI剧", value: "AI剧"},
		{label: "影视剧", value: "影视剧"},
	}
)

const (
	hgRequestTimeout   = 20 * time.Second
	hgSuggestDelay     = 300 * time.Millisecond
	hgEpisodeGroupSize = 30
)

// hgSeriesList stores a paginated, deduplicated list of series for a grid view.
type hgSeriesList struct {
	items   []hg.Series
	seen    map[string]bool
	cursor  string
	hasMore bool
	loaded  bool
	loading bool
	stale   bool // seeded from disk cache, awaiting live background refresh
	err     hg.ErrorInfo
	list    ui.ListState
	gen     int
}

func newHGSeriesList() *hgSeriesList {
	return &hgSeriesList{seen: map[string]bool{}}
}

func (l *hgSeriesList) reset() {
	l.gen++
	l.items = nil
	l.seen = map[string]bool{}
	l.cursor = ""
	l.hasMore = false
	l.loaded = false
	l.loading = false
	l.stale = false
	l.err = hg.ErrorInfo{}
	l.list = ui.ListState{}
}

func (l *hgSeriesList) appendUnique(items []hg.Series) int {
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	added := 0
	for _, s := range items {
		if s.ID != "" {
			if l.seen[s.ID] {
				continue
			}
			l.seen[s.ID] = true
		}
		l.items = append(l.items, s)
		added++
	}
	return added
}

// hgPage represents a pushed MyHongguo sub-page on top of the active root.
type hgPage struct {
	kind  hgPageKind
	title string

	// hgPageSearch
	query        string
	searchFilter string
	results      *hgSeriesList

	// hgPageDetail
	seriesID       string
	series         hg.Series
	episodes       []hg.Episode
	seasons        []hg.Season
	recommends     []hg.Series
	reviews        *hgCommentFeed
	epGroup        int
	loading        bool
	epLoading      bool
	seasonsLoading bool
	recLoading     bool
	err            hg.ErrorInfo
	epErr          hg.ErrorInfo

	// hgPagePerson
	userID        string
	person        hg.Person
	personLoading bool
	personErr     hg.ErrorInfo
	works         *hgSeriesList

	// hgPagePlayer (PR-5 hook)
	epIdx int

	gen int
}

// hgApp holds lazily initialized MyHongguo state, caches, and services.
type hgApp struct {
	svc  hg.Service
	hist *hg.HistoryStore
	cfg  hg.Config
	err  string
	img  *images

	root  hgRootKind
	stack []*hgPage

	// Discover
	discChannel string
	discPages   map[string]*hgSeriesList

	// Rank
	rankContent  string
	rankBoard    string
	rankContents []hg.RankContent
	rankItems    []hg.RankItem
	rankSeen     map[string]bool
	rankCursor   string
	rankHasMore  bool
	rankLoaded   bool
	rankLoading  bool
	rankErr      hg.ErrorInfo
	rankList     ui.ListState
	rankGen      int

	// Filter
	filterGenre        string
	filterRows         []hg.FilterRow
	filterSelected     map[string]string
	filterResult       *hgSeriesList
	filterPanelLoaded  bool
	filterPanelLoading bool
	filterPanelErr     hg.ErrorInfo
	filterPanelGen     int

	// New releases
	newDate   string
	newDays   []hg.NewReleaseDay
	newResult *hgSeriesList

	// Track (favorites & watch history)
	trackTab  int // 0 = 我的追剧, 1 = 最近观看
	trackList ui.ListState

	// Search & suggest
	query        string
	searchBox    ui.Handle
	hot          hg.HotSearch
	hotLoaded    bool
	hotLoading   bool
	sugs         []string
	sugFor       string
	sugSeq       int
	suggestDelay time.Duration

	// Cached actor name -> userID mappings discovered from rankings.
	actorIDs map[string]string

	// Active episode player controller (PR-5).
	player *hgPlayer
}

func newHGApp(cfg hg.Config) *hgApp {
	return &hgApp{
		cfg:            cfg,
		discChannel:    hgDiscoverChannels[0],
		discPages:      map[string]*hgSeriesList{},
		rankSeen:       map[string]bool{},
		filterSelected: map[string]string{},
		filterResult:   newHGSeriesList(),
		newResult:      newHGSeriesList(),
		suggestDelay:   hgSuggestDelay,
		actorIDs:       map[string]string{},
	}
}

func (h *hgApp) initImages(a *app) {
	if h == nil || h.svc == nil || h.img != nil {
		return
	}
	svc := h.svc
	h.img = newImages(func(rawURL string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return svc.Image(ctx, rawURL)
	}, a.post)
	h.img.cacheDir = a.coverCacheDir()
	if a.win != nil {
		h.img.releasePixels = true
	}
}

func (h *hgApp) top() *hgPage {
	if h == nil || len(h.stack) == 0 {
		return nil
	}
	return h.stack[len(h.stack)-1]
}

func (h *hgApp) discoverList(channel string) *hgSeriesList {
	if channel == "" {
		channel = hgDiscoverChannels[0]
	}
	if h.discPages == nil {
		h.discPages = map[string]*hgSeriesList{}
	}
	l := h.discPages[channel]
	if l == nil {
		l = newHGSeriesList()
		h.discPages[channel] = l
	}
	return l
}

func (h *hgApp) activeRankBoards() []hg.RankBoard {
	for _, rc := range h.rankContents {
		if rc.ID == h.rankContent {
			return rc.Boards
		}
	}
	if len(h.rankContents) > 0 {
		return h.rankContents[0].Boards
	}
	return nil
}

func (a *app) hgPop() {
	if a.hg == nil || len(a.hg.stack) == 0 {
		return
	}
	last := a.hg.stack[len(a.hg.stack)-1]
	if last.kind == hgPagePlayer {
		a.hgStopPlayer()
	}
	last.gen++
	if last.results != nil {
		last.results.gen++
	}
	if last.works != nil {
		last.works.gen++
	}
	a.hg.stack[len(a.hg.stack)-1] = nil
	a.hg.stack = a.hg.stack[:len(a.hg.stack)-1]
}

func (a *app) hgSelectRoot(r hgRootKind) {
	h := a.ensureHG()
	if h.player != nil {
		a.hgStopPlayer()
	}
	if h.root != r {
		h.root = r
		h.stack = nil
		a.hgEnsureRootLoaded()
		return
	}
	if len(h.stack) > 0 {
		h.stack = nil
		a.hgEnsureRootLoaded()
		return
	}
	a.hgRefreshRoot()
}

func (a *app) hgEnsureActiveLoaded() {
	if a.hg == nil {
		return
	}
	a.hgLoadHotSearch()
	if top := a.hg.top(); top != nil {
		if top.kind == hgPagePlayer {
			a.hgEnsurePlayerResumed()
		}
		return
	}
	a.hgEnsureRootLoaded()
}

func (a *app) hgEnsureRootLoaded() {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	switch h.root {
	case hgRootDiscover:
		l := h.discoverList(h.discChannel)
		if !l.loaded && !l.loading && l.err.Kind == hg.ErrKindNone {
			a.hgLoadDiscover(h.discChannel, true)
		}
	case hgRootRank:
		if !h.rankLoaded && !h.rankLoading && h.rankErr.Kind == hg.ErrKindNone {
			a.hgLoadRank(true)
		}
	case hgRootFilter:
		if !h.filterPanelLoaded && !h.filterPanelLoading && h.filterPanelErr.Kind == hg.ErrKindNone {
			a.hgLoadFilterPanel(h.filterGenre)
		} else if h.filterPanelLoaded && !h.filterResult.loaded && !h.filterResult.loading && h.filterResult.err.Kind == hg.ErrKindNone {
			a.hgLoadFilterResults(true)
		}
	case hgRootNew:
		if !h.newResult.loaded && !h.newResult.loading && h.newResult.err.Kind == hg.ErrKindNone {
			a.hgLoadNewReleases(true)
		}
	case hgRootTrack:
		// Local HistoryStore; read directly on render.
	}
}

func (a *app) hgRefreshRoot() {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	switch h.root {
	case hgRootDiscover:
		a.hgLoadDiscover(h.discChannel, true)
	case hgRootRank:
		a.hgLoadRank(true)
	case hgRootFilter:
		if !h.filterPanelLoaded {
			a.hgLoadFilterPanel(h.filterGenre)
		} else {
			a.hgLoadFilterResults(true)
		}
	case hgRootNew:
		a.hgLoadNewReleases(true)
	case hgRootTrack:
		h.trackList = ui.ListState{}
	}
}

func (a *app) hgRetryIfErrored() {
	if a.mode != modeHongguo || a.hg == nil || a.hg.svc == nil {
		return
	}
	h := a.hg
	if p := h.top(); p != nil {
		switch p.kind {
		case hgPageSearch:
			if p.results != nil && p.results.err.Kind != hg.ErrKindNone {
				a.hgLoadSearch(p, len(p.results.items) == 0)
			}
		case hgPageDetail:
			if p.err.Kind != hg.ErrKindNone || p.epErr.Kind != hg.ErrKindNone {
				a.hgLoadDetail(p)
			}
		case hgPagePerson:
			if p.personErr.Kind != hg.ErrKindNone || (p.works != nil && p.works.err.Kind != hg.ErrKindNone) {
				a.hgLoadPerson(p)
			}
		case hgPagePlayer:
			if h.player != nil && (h.player.errInfo.Kind != hg.ErrKindNone || h.player.listErr.Kind != hg.ErrKindNone) {
				a.hgRetryPlayback()
			}
		}
		return
	}
	switch h.root {
	case hgRootDiscover:
		l := h.discoverList(h.discChannel)
		if l.err.Kind != hg.ErrKindNone {
			a.hgLoadDiscover(h.discChannel, len(l.items) == 0)
		}
	case hgRootRank:
		if h.rankErr.Kind != hg.ErrKindNone {
			a.hgLoadRank(len(h.rankItems) == 0)
		}
	case hgRootFilter:
		if h.filterPanelErr.Kind != hg.ErrKindNone {
			a.hgLoadFilterPanel(h.filterGenre)
		} else if h.filterResult.err.Kind != hg.ErrKindNone {
			a.hgLoadFilterResults(len(h.filterResult.items) == 0)
		}
	case hgRootNew:
		if h.newResult.err.Kind != hg.ErrKindNone {
			a.hgLoadNewReleases(len(h.newResult.items) == 0)
		}
	}
}

func (a *app) hgSelectDiscoverChannel(ch string) {
	h := a.ensureHG()
	if ch == "" {
		ch = hgDiscoverChannels[0]
	}
	if h.discChannel == ch {
		a.hgLoadDiscover(ch, true)
		return
	}
	h.discChannel = ch
	l := h.discoverList(ch)
	if !l.loaded && !l.loading {
		a.hgLoadDiscover(ch, true)
	}
}

func (a *app) hgPrefetchDiscoverCovers(h *hgApp, channel string) {
	if h == nil || h.img == nil {
		return
	}
	l := h.discoverList(channel)
	n := min(len(l.items), 12)
	for i := range n {
		h.img.getSized(l.items[i].CoverURL, coverEdge)
	}
}

func (a *app) hgLoadDiscover(channel string, reset bool) {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	l := h.discoverList(channel)
	bgRefresh := reset && l.stale && len(l.items) > 0
	if bgRefresh {
		if l.loading {
			return
		}
		l.gen++
	} else if reset {
		l.reset()
	} else if l.loading || (!l.hasMore && l.loaded) {
		return
	}
	l.loading = true
	l.err = hg.ErrorInfo{}
	gen, cursor, svc := l.gen, l.cursor, h.svc
	if bgRefresh {
		cursor = ""
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		page, err := svc.Discover(ctx, channel, cursor)
		cancel()
		a.post(func() {
			if l.gen != gen {
				return
			}
			l.loading = false
			if err != nil {
				l.err = hg.ClassifyError(err)
				if bgRefresh {
					l.stale = false
					l.loaded = true
				}
				return
			}
			if bgRefresh {
				l.items = nil
				l.seen = map[string]bool{}
				l.stale = false
			}
			l.loaded = true
			l.err = hg.ErrorInfo{}
			l.appendUnique(page.Items)
			nextCursor := page.Cursor
			l.hasMore = page.HasMore && nextCursor != "" && nextCursor != cursor
			l.cursor = nextCursor
			a.hgPrefetchDiscoverCovers(h, channel)
			a.saveHGDiscoverCache(channel, l.items)
		})
	}()
}

func (a *app) hgLoadHotSearch() {
	h := a.hg
	if h == nil || h.svc == nil || h.hotLoaded || h.hotLoading {
		return
	}
	h.hotLoading = true
	svc := h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		hot, err := svc.HotSearch(ctx)
		cancel()
		a.post(func() {
			if a.hg != h {
				return
			}
			h.hotLoading = false
			if err == nil {
				h.hot = hot
				h.hotLoaded = true
			}
		})
	}()
}

func (a *app) hgSuggest() {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	h.sugSeq++
	seq, q := h.sugSeq, strings.TrimSpace(h.query)
	if q == "" {
		h.sugs, h.sugFor = nil, ""
		return
	}
	if q == h.sugFor {
		return
	}
	delay := h.suggestDelay
	if delay <= 0 {
		delay = hgSuggestDelay
	}
	svc := h.svc
	go func() {
		time.Sleep(delay)
		a.post(func() {
			if a.hg != h || seq != h.sugSeq {
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
				ws, err := svc.Suggest(ctx, q)
				cancel()
				if err != nil {
					return
				}
				a.post(func() {
					if a.hg != h || seq != h.sugSeq {
						return
					}
					h.sugs, h.sugFor = ws, q
				})
			}()
		})
	}()
}

func (a *app) hgSearch(query string) {
	q := strings.TrimSpace(query)
	if q == "" {
		return
	}
	h := a.ensureHG()
	h.query = q
	if top := h.top(); top != nil && top.kind == hgPageSearch {
		if top.query == q {
			a.hgLoadSearch(top, true)
			return
		}
		top.query = q
		top.title = "搜索：" + q
		top.searchFilter = ""
		a.hgLoadSearch(top, true)
		return
	}
	p := &hgPage{
		kind:    hgPageSearch,
		title:   "搜索：" + q,
		query:   q,
		results: newHGSeriesList(),
	}
	h.stack = append(h.stack, p)
	a.hgLoadSearch(p, true)
}

func (a *app) hgSetSearchFilter(p *hgPage, filter string) {
	if p == nil || p.kind != hgPageSearch {
		return
	}
	if p.searchFilter == filter {
		return
	}
	p.searchFilter = filter
	a.hgLoadSearch(p, true)
}

func (a *app) hgLoadSearch(p *hgPage, reset bool) {
	h := a.hg
	if h == nil || h.svc == nil || p == nil || p.results == nil {
		return
	}
	l := p.results
	if reset {
		l.reset()
	} else if l.loading || (!l.hasMore && l.loaded) {
		return
	}
	l.loading = true
	l.err = hg.ErrorInfo{}
	var filters []string
	if p.searchFilter != "" {
		filters = []string{p.searchFilter}
	}
	gen, q, cursor, svc := l.gen, p.query, l.cursor, h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		page, err := svc.Search(ctx, q, cursor, filters)
		cancel()
		a.post(func() {
			if l.gen != gen {
				return
			}
			l.loading = false
			if err != nil {
				l.err = hg.ClassifyError(err)
				return
			}
			l.loaded = true
			l.err = hg.ErrorInfo{}
			l.appendUnique(page.Items)
			nextCursor := page.Cursor
			l.hasMore = page.HasMore && nextCursor != "" && nextCursor != cursor
			l.cursor = nextCursor
		})
	}()
}

func (a *app) hgSelectRankContent(rc hg.RankContent) {
	h := a.hg
	if h == nil {
		return
	}
	h.rankContent = rc.ID
	board := ""
	for _, rb := range rc.Boards {
		if rb.Selected {
			board = rb.ID
			break
		}
	}
	if board == "" && len(rc.Boards) > 0 {
		board = rc.Boards[0].ID
	}
	h.rankBoard = board
	a.hgLoadRank(true)
}

func (a *app) hgSelectRankBoard(boardID string) {
	h := a.hg
	if h == nil {
		return
	}
	h.rankBoard = boardID
	a.hgLoadRank(true)
}

func (a *app) hgLoadRank(reset bool) {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	if reset {
		h.rankGen++
		h.rankItems = nil
		h.rankSeen = map[string]bool{}
		h.rankCursor = ""
		h.rankHasMore = false
		h.rankLoaded = false
		h.rankLoading = false
		h.rankErr = hg.ErrorInfo{}
		h.rankList = ui.ListState{}
	} else if h.rankLoading || (!h.rankHasMore && h.rankLoaded) {
		return
	}
	h.rankLoading = true
	h.rankErr = hg.ErrorInfo{}
	gen, content, board, cursor, svc := h.rankGen, h.rankContent, h.rankBoard, h.rankCursor, h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		page, err := svc.ShortRank(ctx, content, board, cursor)
		cancel()
		a.post(func() {
			if a.hg != h || h.rankGen != gen {
				return
			}
			h.rankLoading = false
			if err != nil {
				h.rankErr = hg.ClassifyError(err)
				return
			}
			h.rankLoaded = true
			h.rankErr = hg.ErrorInfo{}
			if len(page.Contents) > 0 {
				h.rankContents = page.Contents
			}
			if page.Content != "" {
				h.rankContent = page.Content
			} else if h.rankContent == "" {
				for _, rc := range h.rankContents {
					if rc.Selected {
						h.rankContent = rc.ID
						break
					}
				}
				if h.rankContent == "" && len(h.rankContents) > 0 {
					h.rankContent = h.rankContents[0].ID
				}
			}
			if page.Board != "" {
				h.rankBoard = page.Board
			} else if h.rankBoard == "" {
				boards := h.activeRankBoards()
				for _, rb := range boards {
					if rb.Selected {
						h.rankBoard = rb.ID
						break
					}
				}
				if h.rankBoard == "" && len(boards) > 0 {
					h.rankBoard = boards[0].ID
				}
			}
			if h.rankSeen == nil {
				h.rankSeen = map[string]bool{}
			}
			for _, it := range page.Items {
				key := rankItemKey(it)
				if key != "" {
					if h.rankSeen[key] {
						continue
					}
					h.rankSeen[key] = true
				}
				if it.UserID != "" {
					if name := hgFirstNonEmpty(it.Actor, it.Title); name != "" {
						if h.actorIDs == nil {
							h.actorIDs = map[string]string{}
						}
						h.actorIDs[name] = it.UserID
					}
				}
				h.rankItems = append(h.rankItems, it)
			}
			nextCursor := page.Cursor
			h.rankHasMore = page.HasMore && nextCursor != "" && nextCursor != cursor
			h.rankCursor = nextCursor
		})
	}()
}

func rankItemKey(it hg.RankItem) string {
	if it.SeriesID != "" {
		return "s:" + it.SeriesID
	}
	if it.UserID != "" {
		return "u:" + it.UserID
	}
	if it.Title != "" {
		return fmt.Sprintf("t:%d:%s", it.Position, it.Title)
	}
	return ""
}

func (a *app) hgLoadFilterPanel(genre string) {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	h.filterPanelGen++
	h.filterPanelLoading = true
	h.filterPanelErr = hg.ErrorInfo{}
	gen, svc := h.filterPanelGen, h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		rows, err := svc.ShortFilterPanel(ctx, genre)
		cancel()
		a.post(func() {
			if a.hg != h || h.filterPanelGen != gen {
				return
			}
			h.filterPanelLoading = false
			if err != nil {
				h.filterPanelErr = hg.ClassifyError(err)
				return
			}
			h.filterPanelLoaded = true
			h.filterPanelErr = hg.ErrorInfo{}
			h.filterRows = rows
			if h.filterSelected == nil {
				h.filterSelected = map[string]string{}
			}
			for _, row := range rows {
				if _, exists := h.filterSelected[row.Key]; exists {
					continue
				}
				for _, opt := range row.Options {
					if opt.Selected {
						h.filterSelected[row.Key] = opt.Value
						if row.Key == "genre" && h.filterGenre == "" {
							h.filterGenre = opt.Value
						}
						break
					}
				}
			}
			a.hgLoadFilterResults(true)
		})
	}()
}

func (a *app) hgSelectFilterOption(rowKey, value string) {
	h := a.hg
	if h == nil {
		return
	}
	if h.filterSelected == nil {
		h.filterSelected = map[string]string{}
	}
	if h.filterSelected[rowKey] == value {
		return
	}
	h.filterSelected[rowKey] = value
	if rowKey == "genre" {
		h.filterGenre = value
	}
	a.hgLoadFilterResults(true)
}

func (a *app) hgLoadFilterResults(reset bool) {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	l := h.filterResult
	if l == nil {
		l = newHGSeriesList()
		h.filterResult = l
	}
	if reset {
		l.reset()
	} else if l.loading || (!l.hasMore && l.loaded) {
		return
	}
	l.loading = true
	l.err = hg.ErrorInfo{}
	gen, genre, cursor, svc := l.gen, h.filterGenre, l.cursor, h.svc
	sel := maps.Clone(h.filterSelected)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		page, err := svc.ShortFilter(ctx, genre, sel, cursor)
		cancel()
		a.post(func() {
			if l.gen != gen {
				return
			}
			l.loading = false
			if err != nil {
				l.err = hg.ClassifyError(err)
				return
			}
			l.loaded = true
			l.err = hg.ErrorInfo{}
			l.appendUnique(page.Items)
			nextCursor := page.Cursor
			l.hasMore = page.HasMore && nextCursor != "" && nextCursor != cursor
			l.cursor = nextCursor
		})
	}()
}

func (a *app) hgSelectNewDate(date string) {
	h := a.hg
	if h == nil {
		return
	}
	if h.newDate == date && h.newResult.loaded {
		return
	}
	h.newDate = date
	a.hgLoadNewReleases(true)
}

func (a *app) hgLoadNewReleases(reset bool) {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	l := h.newResult
	if l == nil {
		l = newHGSeriesList()
		h.newResult = l
	}
	if reset {
		l.reset()
	} else if l.loading || (!l.hasMore && l.loaded) {
		return
	}
	l.loading = true
	l.err = hg.ErrorInfo{}
	gen, date, cursor, svc := l.gen, h.newDate, l.cursor, h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		page, err := svc.NewReleases(ctx, date, cursor)
		cancel()
		a.post(func() {
			if l.gen != gen {
				return
			}
			l.loading = false
			if err != nil {
				l.err = hg.ClassifyError(err)
				return
			}
			l.loaded = true
			l.err = hg.ErrorInfo{}
			if len(page.Days) > 0 {
				h.newDays = page.Days
				if h.newDate == "" {
					for _, d := range page.Days {
						if d.Selected {
							h.newDate = d.Date
							break
						}
					}
					if h.newDate == "" {
						h.newDate = page.Days[0].Date
					}
				}
			}
			l.appendUnique(page.Items)
			nextCursor := page.Cursor
			l.hasMore = page.HasMore && nextCursor != "" && nextCursor != cursor
			l.cursor = nextCursor
		})
	}()
}

func (a *app) hgOpenDetail(seed hg.Series) {
	if seed.ID == "" {
		return
	}
	h := a.ensureHG()
	title := seed.Title
	if title == "" {
		title = "剧集详情"
	}
	p := &hgPage{
		kind:     hgPageDetail,
		title:    title,
		seriesID: seed.ID,
		series:   seed,
		reviews:  newHGCommentFeed(seed.ID, ""),
	}
	h.stack = append(h.stack, p)
	a.hgLoadDetail(p)
}

func (a *app) hgSwitchSeason(p *hgPage, s hg.Season) {
	if p == nil || p.kind != hgPageDetail || s.SeriesID == "" || s.SeriesID == p.seriesID {
		return
	}
	p.seriesID = s.SeriesID
	p.series = hg.Series{
		ID:           s.SeriesID,
		Title:        hgFirstNonEmpty(s.Title, p.series.Title),
		CoverURL:     hgFirstNonEmpty(s.CoverURL, p.series.CoverURL),
		EpisodeCount: s.EpisodeCount,
	}
	p.title = hgFirstNonEmpty(s.Title, "剧集详情")
	p.episodes = nil
	p.recommends = nil
	p.reviews = newHGCommentFeed(s.SeriesID, "")
	p.epGroup = 0
	a.hgLoadDetail(p)
}

func (a *app) hgLoadDetail(p *hgPage) {
	h := a.hg
	if h == nil || h.svc == nil || p == nil || p.seriesID == "" {
		return
	}
	p.gen++
	p.loading = true
	p.epLoading = true
	p.seasonsLoading = true
	p.recLoading = true
	p.err = hg.ErrorInfo{}
	p.epErr = hg.ErrorInfo{}
	gen, id, svc := p.gen, p.seriesID, h.svc

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		detail, err := svc.GetSeries(ctx, id)
		cancel()
		a.post(func() {
			if p.gen != gen {
				return
			}
			p.loading = false
			if err != nil {
				p.err = hg.ClassifyError(err)
				return
			}
			p.err = hg.ErrorInfo{}
			p.series = mergeHGSeries(p.series, detail)
			if p.series.Title != "" {
				p.title = p.series.Title
			}
		})
	}()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		eps, err := svc.ListEpisodes(ctx, id)
		cancel()
		a.post(func() {
			if p.gen != gen {
				return
			}
			p.epLoading = false
			if err != nil {
				p.epErr = hg.ClassifyError(err)
				return
			}
			p.epErr = hg.ErrorInfo{}
			p.episodes = eps
			if p.series.EpisodeCount == 0 && len(eps) > 0 {
				p.series.EpisodeCount = len(eps)
			}
			if h.hist != nil {
				if entry, ok := h.hist.Get(id); ok {
					idx := hgResumeEpisodeIndex(eps, entry)
					p.epGroup = idx / hgEpisodeGroupSize
				}
			}
		})
	}()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		seasons, err := svc.Seasons(ctx, id)
		cancel()
		a.post(func() {
			if p.gen != gen {
				return
			}
			p.seasonsLoading = false
			if err == nil {
				p.seasons = seasons
			}
		})
	}()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		recs, err := svc.SeriesRecommend(ctx, id)
		cancel()
		a.post(func() {
			if p.gen != gen {
				return
			}
			p.recLoading = false
			if err == nil {
				p.recommends = recs
			}
		})
	}()

	if p.reviews == nil || p.reviews.seriesID != id {
		p.reviews = newHGCommentFeed(id, "")
	}
	a.hgLoadCommentFeed(p.reviews, true)
}

func mergeHGSeries(base, detail hg.Series) hg.Series {
	out := base
	if detail.ID != "" {
		out.ID = detail.ID
	}
	if detail.Title != "" {
		out.Title = detail.Title
	}
	if detail.CoverURL != "" {
		out.CoverURL = detail.CoverURL
	}
	if detail.Abstract != "" {
		out.Abstract = detail.Abstract
	}
	if detail.Score != "" {
		out.Score = detail.Score
	}
	if detail.Category != "" {
		out.Category = detail.Category
	}
	if detail.StatusText != "" {
		out.StatusText = detail.StatusText
	}
	if detail.AlbumID != "" {
		out.AlbumID = detail.AlbumID
	}
	if detail.EpisodeCount > 0 {
		out.EpisodeCount = detail.EpisodeCount
	}
	if detail.Finished {
		out.Finished = true
	}
	if len(detail.Actors) > 0 {
		out.Actors = detail.Actors
	}
	if len(detail.Tags) > 0 {
		out.Tags = detail.Tags
	}
	return out
}

func hgResumeEpisodeIndex(episodes []hg.Episode, entry hg.WatchEntry) int {
	if len(episodes) == 0 {
		return 0
	}
	if entry.EpisodeID != "" {
		for i, ep := range episodes {
			if ep.ID == entry.EpisodeID {
				return i
			}
		}
	}
	if entry.EpisodeIndex > 0 {
		for i, ep := range episodes {
			if ep.Index == entry.EpisodeIndex {
				return i
			}
		}
		if entry.EpisodeIndex-1 < len(episodes) {
			return entry.EpisodeIndex - 1
		}
	}
	return 0
}

func (a *app) hgToggleFavorite(series hg.Series) {
	h := a.ensureHG()
	if h.hist == nil || series.ID == "" {
		return
	}
	fav, err := h.hist.ToggleFavorite(series)
	if err != nil {
		a.toast = "追剧操作失败：" + err.Error()
		return
	}
	if fav {
		a.toast = "已加入追剧"
	} else {
		a.toast = "已取消追剧"
	}
}

func (a *app) hgRemoveHistory(seriesID string) {
	h := a.ensureHG()
	if h.hist == nil || seriesID == "" {
		return
	}
	if err := h.hist.RemoveHistory(seriesID); err != nil {
		a.toast = "删除观看记录失败：" + err.Error()
		return
	}
	a.toast = "已删除观看记录"
}

func (a *app) hgOpenPerson(userID, name, avatarURL string) {
	userID = strings.TrimSpace(userID)
	name = strings.TrimSpace(name)
	if userID == "" && name == "" {
		return
	}
	h := a.ensureHG()
	if userID == "" && name != "" {
		userID = h.actorIDs[name]
	}
	title := hgFirstNonEmpty(name, "演员主页")
	p := &hgPage{
		kind:   hgPagePerson,
		title:  title,
		userID: userID,
		person: hg.Person{
			UserID:    userID,
			Name:      name,
			AvatarURL: avatarURL,
		},
		works: newHGSeriesList(),
	}
	h.stack = append(h.stack, p)
	a.hgLoadPerson(p)
}

func (a *app) hgLoadPerson(p *hgPage) {
	h := a.hg
	if h == nil || h.svc == nil || p == nil || p.kind != hgPagePerson {
		return
	}
	p.gen++
	p.personErr = hg.ErrorInfo{}
	if p.userID != "" {
		p.personLoading = true
		gen, uid, svc := p.gen, p.userID, h.svc
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
			person, err := svc.Person(ctx, uid)
			cancel()
			a.post(func() {
				if p.gen != gen {
					return
				}
				p.personLoading = false
				if err != nil {
					p.personErr = hg.ClassifyError(err)
					return
				}
				p.personErr = hg.ErrorInfo{}
				if person.UserID != "" {
					p.person.UserID = person.UserID
				}
				if person.Name != "" {
					p.person.Name = person.Name
					p.title = person.Name
				}
				if person.AvatarURL != "" {
					p.person.AvatarURL = person.AvatarURL
				}
				if person.Intro != "" {
					p.person.Intro = person.Intro
				}
				if person.WorksCount > 0 {
					p.person.WorksCount = person.WorksCount
				}
			})
		}()
	}
	a.hgLoadPersonWorks(p, true)
}

func (a *app) hgLoadPersonWorks(p *hgPage, reset bool) {
	h := a.hg
	if h == nil || h.svc == nil || p == nil || p.works == nil {
		return
	}
	l := p.works
	if reset {
		l.reset()
	} else if l.loading || (!l.hasMore && l.loaded) {
		return
	}
	l.loading = true
	l.err = hg.ErrorInfo{}
	gen, uid, name, cursor, svc := l.gen, p.userID, p.person.Name, l.cursor, h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		var (
			page hg.Page[hg.Series]
			err  error
		)
		if uid != "" {
			page, err = svc.PersonWorks(ctx, uid, cursor)
		} else {
			page, err = svc.Search(ctx, name, cursor, nil)
		}
		cancel()
		a.post(func() {
			if l.gen != gen {
				return
			}
			l.loading = false
			if err != nil {
				l.err = hg.ClassifyError(err)
				return
			}
			l.loaded = true
			l.err = hg.ErrorInfo{}
			l.appendUnique(page.Items)
			nextCursor := page.Cursor
			l.hasMore = page.HasMore && nextCursor != "" && nextCursor != cursor
			l.cursor = nextCursor
		})
	}()
}

func hgFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
