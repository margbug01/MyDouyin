package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/hg"
)

// hgOrbitSource selects which drama stream feeds the MyHongguo Orbital Home.
// hgOrbitDual is the default: left wing 真人剧, right wing 漫剧.
type hgOrbitSource uint8

const (
	hgOrbitDual hgOrbitSource = iota
	hgOrbitLive
	hgOrbitComic
	hgOrbitTrack
)

type hgOrbitSourceSpec struct {
	id    hgOrbitSource
	label string
}

var hgOrbitSources = []hgOrbitSourceSpec{
	{id: hgOrbitDual, label: "双星"},
	{id: hgOrbitLive, label: "真人剧"},
	{id: hgOrbitComic, label: "漫剧"},
	{id: hgOrbitTrack, label: "追剧"},
}

const (
	hgOrbitRing = 8
	hgOrbitWing = 4 // posters per side in dual mode
)

var (
	hgLiveBadgeColor  = ui.Hex("#ff4d3d")
	hgComicBadgeColor = ui.Hex("#7c6bff")
)

// hgOrbitState holds UI state for MyHongguo's Dual-Wing Orbital Home (红果引力场).
type hgOrbitState struct {
	source         hgOrbitSource
	hidden         bool      // pure-search mode
	hotOpen        bool      // opt-in hot-search popover
	offset         int       // per-wing offset in dual mode; list offset in single-source modes
	pendingAdvance bool      // waiting for fresh page before advancing
	swipeAt        time.Time // last precise trackpad scroll event time
	scrollAt       time.Time // last committed scroll refresh time
}

type hgOrbitCard struct {
	series   hg.Series
	wingTag  string
	wingTint ui.Color
	progress string
}

// hgOrbitScatter places 8 vertical 3:4 drama posters in two balanced wings
// around the centered search hub: indices 0..3 form the left wing (真人剧 in
// dual mode) and 4..7 form the right wing (漫剧 in dual mode), with one larger
// hero poster anchoring each outer flank.
var hgOrbitScatter = [hgOrbitRing]orbitCardRect{
	// Left wing (真人剧)
	{26, 192, 207, 276},  // 0: left outer hero
	{280, 66, 177, 236},  // 1: top left
	{286, 514, 177, 236}, // 2: bottom inner left
	{72, 502, 174, 232},  // 3: bottom outer left
	// Right wing (漫剧) — mirrored X anchors with subtle organic Y stagger
	{623, 76, 177, 236},  // 4: top right
	{847, 204, 207, 276}, // 5: right outer hero
	{834, 514, 174, 232}, // 6: bottom outer right
	{617, 504, 177, 236}, // 7: bottom inner right
}

func hgOrbitHubRect(w, h float32) (x, y, rw, rh float32) {
	rw = min(480, max(340, 480*orbitScale(w, h)))
	rh = 112
	return (w - rw) / 2, (h - rh) / 2, rw, rh
}

func hgOrbitLayout(w, h float32) []orbitCardRect {
	if w < 780 || h < 480 {
		return nil
	}
	s := orbitScale(w, h)
	ox, oy := (w-orbitFrameW*s)/2, (h-orbitFrameH*s)/2
	rects := make([]orbitCardRect, 0, hgOrbitRing)
	for _, r := range hgOrbitScatter {
		rects = append(rects, orbitCardRect{ox + r.x*s, oy + r.y*s, r.w * s, r.h * s})
	}
	return rects
}

func (a *app) isHGOrbit() bool {
	return a.mode == modeHongguo && a.hg != nil && a.hg.root == hgRootOrbit && len(a.hg.stack) == 0
}

func (h *hgApp) orbitStep() int {
	if h.orbit.source == hgOrbitDual {
		return hgOrbitWing
	}
	return hgOrbitRing
}

func (h *hgApp) trackOrbitCards() []hgOrbitCard {
	if h == nil || h.hist == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []hgOrbitCard
	add := func(e hg.WatchEntry, isFav bool) {
		if e.SeriesID == "" || seen[e.SeriesID] {
			return
		}
		seen[e.SeriesID] = true
		prog := ""
		switch {
		case e.EpisodeIndex > 0:
			prog = fmt.Sprintf("看到第 %d 集", e.EpisodeIndex)
		case isFav:
			prog = "已追剧"
		}
		out = append(out, hgOrbitCard{
			series: hg.Series{
				ID:           e.SeriesID,
				Title:        e.Title,
				CoverURL:     e.CoverURL,
				EpisodeCount: e.EpisodeCount,
			},
			progress: prog,
		})
	}
	for _, e := range h.hist.Favorites() {
		add(e, true)
	}
	for _, e := range h.hist.Recent() {
		add(e, false)
	}
	return out
}

// hgEnsureOrbitLoaded kicks off fetching the channels backing the active
// Hongguo orbit source so both wings stay warm and 「换一批」 never stalls.
func (a *app) hgEnsureOrbitLoaded() {
	h := a.hg
	if h == nil || h.svc == nil {
		return
	}
	ensureChannel := func(ch string, need int) {
		l := h.discoverList(ch)
		if l.loading {
			return
		}
		if !l.loaded && l.err.Kind == hg.ErrKindNone {
			a.hgLoadDiscover(ch, true)
			return
		}
		if l.loaded && l.hasMore && l.err.Kind == hg.ErrKindNone && len(l.items)-h.orbit.offset < need {
			a.hgLoadDiscover(ch, false)
		}
	}
	switch h.orbit.source {
	case hgOrbitDual:
		ensureChannel("真人剧", 2*hgOrbitWing)
		ensureChannel("漫剧", 2*hgOrbitWing)
	case hgOrbitLive:
		ensureChannel("真人剧", 2*hgOrbitRing)
	case hgOrbitComic:
		ensureChannel("漫剧", 2*hgOrbitRing)
	case hgOrbitTrack:
		// Local HistoryStore.
	}
}

func (a *app) hgSetOrbitSource(src hgOrbitSource) {
	h := a.ensureHG()
	h.orbit.source = src
	h.orbit.offset = 0
	h.orbit.pendingAdvance = false
	a.hgEnsureOrbitLoaded()
}

func (a *app) hgOrbitHasNext(nextOff int) bool {
	h := a.hg
	if h == nil {
		return false
	}
	switch h.orbit.source {
	case hgOrbitDual:
		live := h.discoverList("真人剧")
		comic := h.discoverList("漫剧")
		liveReady := nextOff+hgOrbitWing <= len(live.items) || (live.loaded && !live.hasMore)
		comicReady := nextOff+hgOrbitWing <= len(comic.items) || (comic.loaded && !comic.hasMore)
		anyFresh := nextOff < len(live.items) || nextOff < len(comic.items)
		return liveReady && comicReady && anyFresh
	case hgOrbitLive:
		l := h.discoverList("真人剧")
		return nextOff+hgOrbitRing <= len(l.items)
	case hgOrbitComic:
		l := h.discoverList("漫剧")
		return nextOff+hgOrbitRing <= len(l.items)
	case hgOrbitTrack:
		return nextOff < len(h.trackOrbitCards())
	}
	return false
}

func (a *app) hgOrbitCanFetchMore() bool {
	h := a.hg
	if h == nil || h.svc == nil {
		return false
	}
	can := func(ch string) bool {
		l := h.discoverList(ch)
		return (!l.loaded || l.hasMore) && l.err.Kind == hg.ErrKindNone
	}
	switch h.orbit.source {
	case hgOrbitDual:
		return can("真人剧") || can("漫剧")
	case hgOrbitLive:
		return can("真人剧")
	case hgOrbitComic:
		return can("漫剧")
	default:
		return false
	}
}

func (a *app) hgCycleOrbit() {
	h := a.hg
	if h == nil {
		return
	}
	if h.orbit.source == hgOrbitTrack {
		cards := h.trackOrbitCards()
		if len(cards) <= hgOrbitRing {
			return
		}
		nextOff := h.orbit.offset + hgOrbitRing
		if nextOff >= len(cards) {
			nextOff = 0
		}
		h.orbit.offset = nextOff
		h.orbit.pendingAdvance = false
		return
	}
	step := h.orbitStep()
	nextOff := h.orbit.offset + step
	if a.hgOrbitHasNext(nextOff) {
		h.orbit.offset = nextOff
		h.orbit.pendingAdvance = false
		a.hgEnsureOrbitLoaded()
		a.hgPrefetchOrbitCovers(h)
		return
	}
	if a.hgOrbitCanFetchMore() {
		h.orbit.pendingAdvance = true
		a.hgEnsureOrbitLoaded()
		return
	}
	h.orbit.offset = 0
	h.orbit.pendingAdvance = false
	a.hgPrefetchOrbitCovers(h)
}

func (a *app) hgPrevOrbit() {
	h := a.hg
	if h == nil || h.orbit.offset <= 0 {
		return
	}
	h.orbit.offset = max(0, h.orbit.offset-h.orbitStep())
	h.orbit.pendingAdvance = false
	a.hgPrefetchOrbitCovers(h)
}

func (a *app) handleHGOrbitScroll(ev ui.InputEvent) bool {
	h := a.hg
	if h == nil || ev.Kind != ui.InputScroll || ev.Momentum || ev.DY == 0 || h.orbit.hidden {
		return false
	}
	if ev.Precise {
		same := !h.orbit.swipeAt.IsZero() && ev.Time.Sub(h.orbit.swipeAt) < 200*time.Millisecond
		h.orbit.swipeAt = ev.Time
		if same {
			return true
		}
	}
	if !h.orbit.scrollAt.IsZero() && ev.Time.Sub(h.orbit.scrollAt) < 400*time.Millisecond {
		return true
	}
	if ev.DY > 0 {
		h.orbit.scrollAt = ev.Time
		a.hgCycleOrbit()
	} else if h.orbit.offset > 0 {
		h.orbit.scrollAt = ev.Time
		a.hgPrevOrbit()
	}
	return true
}

func (a *app) onHGOrbitChannelLoaded(h *hgApp, channel string) {
	if h == nil || h.root != hgRootOrbit {
		return
	}
	if h.orbit.pendingAdvance {
		nextOff := h.orbit.offset + h.orbitStep()
		switch {
		case a.hgOrbitHasNext(nextOff):
			h.orbit.offset = nextOff
			h.orbit.pendingAdvance = false
		case !a.hgOrbitCanFetchMore():
			h.orbit.offset = 0
			h.orbit.pendingAdvance = false
		}
	}
	a.hgPrefetchOrbitCovers(h)
}

func (a *app) hgPrefetchOrbitCovers(h *hgApp) {
	if h == nil || h.img == nil {
		return
	}
	cards := h.currentOrbitCards()
	for _, card := range cards {
		if card != nil && card.series.CoverURL != "" {
			h.img.getSized(card.series.CoverURL, coverEdge)
		}
	}
}

func (h *hgApp) currentOrbitCards() [hgOrbitRing]*hgOrbitCard {
	var slots [hgOrbitRing]*hgOrbitCard
	if h == nil {
		return slots
	}
	fillFromList := func(dstStart, count int, list []hg.Series, off int, tag string, tint ui.Color) {
		n := len(list)
		if n == 0 {
			return
		}
		start := off % n
		limit := min(count, n)
		for i := range limit {
			s := list[(start+i)%n]
			slots[dstStart+i] = &hgOrbitCard{series: s, wingTag: tag, wingTint: tint}
		}
	}
	switch h.orbit.source {
	case hgOrbitDual:
		fillFromList(0, hgOrbitWing, h.discoverList("真人剧").items, h.orbit.offset, "真人剧", hgLiveBadgeColor)
		fillFromList(hgOrbitWing, hgOrbitWing, h.discoverList("漫剧").items, h.orbit.offset, "漫剧", hgComicBadgeColor)
	case hgOrbitLive:
		fillFromList(0, hgOrbitRing, h.discoverList("真人剧").items, h.orbit.offset, "", ui.Color{})
	case hgOrbitComic:
		fillFromList(0, hgOrbitRing, h.discoverList("漫剧").items, h.orbit.offset, "", ui.Color{})
	case hgOrbitTrack:
		cards := h.trackOrbitCards()
		n := len(cards)
		if n == 0 {
			return slots
		}
		start := h.orbit.offset % n
		limit := min(hgOrbitRing, n)
		for i := range limit {
			c := cards[(start+i)%n]
			slots[i] = &c
		}
	}
	return slots
}

var (
	hgOrbitLightVeil = []ui.GradientStop{
		{Color: ui.Hex("#fffdfb"), At: 0},
		{Color: ui.Hex("#faf8f7"), At: 0.48},
		{Color: ui.Hex("#f1f2f7"), At: 1},
	}
	hgOrbitLiveGlowDark = []ui.GradientStop{
		{Color: ui.RGBA(255, 77, 61, 0.13), At: 0},
		{Color: ui.RGBA(255, 77, 61, 0.055), At: 0.42},
		{Color: ui.RGBA(0, 0, 0, 0), At: 1},
	}
	hgOrbitComicGlowDark = []ui.GradientStop{
		{Color: ui.RGBA(124, 107, 255, 0.14), At: 0},
		{Color: ui.RGBA(124, 107, 255, 0.055), At: 0.44},
		{Color: ui.RGBA(0, 0, 0, 0), At: 1},
	}
	hgOrbitLiveGlowLight = []ui.GradientStop{
		{Color: ui.RGBA(255, 92, 74, 0.075), At: 0},
		{Color: ui.RGBA(255, 92, 74, 0.025), At: 0.48},
		{Color: ui.RGBA(255, 92, 74, 0), At: 1},
	}
	hgOrbitComicGlowLight = []ui.GradientStop{
		{Color: ui.RGBA(124, 107, 255, 0.075), At: 0},
		{Color: ui.RGBA(124, 107, 255, 0.025), At: 0.48},
		{Color: ui.RGBA(124, 107, 255, 0), At: 1},
	}
)

// hgOrbitView renders MyHongguo's Dual-Wing Orbital Home (红果引力场).
func (a *app) hgOrbitView(c *ui.Context, h *hgApp) {
	w, hWin := c.Size()
	canvas := ui.Box(c.Key("hg-orbit-canvas")).Fill().DragWindow().HandleInput(a.handleHGOrbitScroll)
	if pal.dark {
		canvas.Background(pal.bg)
	} else {
		canvas.RadialGradient(ui.RadialGradient{Center: ui.Point{X: 0.5, Y: 0.48}, Oklab: true, Stops: hgOrbitLightVeil})
	}
	canvas.Children(func() {
		a.hgOrbitAmbientGlows(c, h, w, hWin)
		if !h.orbit.hidden {
			a.hgOrbitCards(c, h, w, hWin)
		}
		ui.Box(c.Key("hg-orbit-brand")).Absolute().Top(11).Left(brandBarLeft()).ZIndex(30).Children(func() {
			a.brandSwitcher(c)
		})
		a.hgOrbitCenterHub(c, h, w, hWin)
	})
}

func (a *app) hgOrbitAmbientGlows(c *ui.Context, h *hgApp, w, hWin float32) {
	noHit := func(float32, float32, ui.Rect) bool { return false }
	radius := max(w, hWin) * 0.46
	liveStops, comicStops := hgOrbitLiveGlowLight, hgOrbitComicGlowLight
	if pal.dark {
		liveStops, comicStops = hgOrbitLiveGlowDark, hgOrbitComicGlowDark
	}
	switch h.orbit.source {
	case hgOrbitLive:
		ui.Box(c.Key("hg-orbit-glow-live")).Absolute().Top(0).Left(0).Right(0).Bottom(0).
			RadialGradient(ui.RadialGradient{Center: ui.Point{X: 0.5, Y: 0.48}, Radius: radius * 1.15, Oklab: true, Stops: liveStops}).
			HitTest(noHit)
	case hgOrbitComic:
		ui.Box(c.Key("hg-orbit-glow-comic")).Absolute().Top(0).Left(0).Right(0).Bottom(0).
			RadialGradient(ui.RadialGradient{Center: ui.Point{X: 0.5, Y: 0.48}, Radius: radius * 1.15, Oklab: true, Stops: comicStops}).
			HitTest(noHit)
	default:
		// Dual-hemisphere constellation glow: warm coral behind left 真人剧 wing,
		// soft indigo-violet behind right 漫剧 wing.
		ui.Box(c.Key("hg-orbit-glow-left")).Absolute().Top(0).Left(0).Right(0).Bottom(0).
			RadialGradient(ui.RadialGradient{Center: ui.Point{X: 0.25, Y: 0.48}, Radius: radius, Oklab: true, Stops: liveStops}).
			HitTest(noHit)
		ui.Box(c.Key("hg-orbit-glow-right")).Absolute().Top(0).Left(0).Right(0).Bottom(0).
			RadialGradient(ui.RadialGradient{Center: ui.Point{X: 0.75, Y: 0.50}, Radius: radius, Oklab: true, Stops: comicStops}).
			HitTest(noHit)
	}
}

func (a *app) hgOrbitCenterHub(c *ui.Context, h *hgApp, w, hWin float32) {
	hx, hy, hw, _ := hgOrbitHubRect(w, hWin)
	q := strings.TrimSpace(h.query)
	hasCards := false
	for _, slot := range h.currentOrbitCards() {
		if slot != nil {
			hasCards = true
			break
		}
	}

	ui.Column(c.Key("hg-orbit-hub")).Absolute().Left(hx).Top(hy).Width(hw).AlignItems(ui.Center).ZIndex(25).Children(func() {
		focused := c.Focused(h.searchBox)
		field := ui.Row(c.Key("hg-orbit-search-bar")).Width(hw).Height(52).Radius(20).Padding(0, 10, 0, 18).Gap(8).
			AlignItems(ui.Center).
			Background(onOff(focused, pal.searchFocus, pal.searchTint)).
			Border(1, onOff(focused, pal.focusBorder, pal.searchBorder)).
			InsetShadow(0, 1, 0, 0, pal.searchInset)
		if pal.dark {
			field.Shadow(0, 0, 64, 4, ui.RGBA(255, 86, 62, 0.14))
		} else {
			field.Shadow(0, 14, 38, -6, pal.searchShadow)
		}
		field.Children(func() {
			ui.Icon(c, iconSearch).Size(16, 16).TextColor(onOff(focused, pal.text, pal.muted))
			in := ui.TextInputBase(c.Key("hg-orbit-search"), &h.query).Bind(&h.searchBox).Grow(1).
				Placeholder("搜索短剧、漫剧、演员…").FontSize(15).Label("搜索")
			if in.Submitted() {
				h.orbit.hotOpen = false
				a.hgSearch(h.query)
			} else if in.Changed() {
				a.hgSuggest()
			}
			if h.query != "" {
				if iconButton(c.Key("hg-orbit-clear"), iconClose, 13, pal.muted, "清空搜索").Clicked() {
					h.query = ""
					h.sugs, h.sugFor = nil, ""
				}
				return
			}
			if hasCards && !h.orbit.hidden {
				btn := iconButton(c.Key("hg-orbit-cycle"), iconRefresh, 14, onOff(h.orbit.pendingAdvance, pal.accent, pal.faint), "换一批")
				if h.orbit.pendingAdvance {
					btn.Opacity(0.6)
				} else if btn.Clicked() {
					a.hgCycleOrbit()
				}
			}
			eyeIcon := onOff(h.orbit.hidden, iconEye, iconEyeOff)
			eyeLabel := onOff(h.orbit.hidden, "显示周围海报", "收起周围海报")
			if iconButton(c.Key("hg-orbit-hide"), eyeIcon, 15, onOff(h.orbit.hidden, pal.text, pal.faint), eyeLabel).Clicked() {
				h.orbit.hidden = !h.orbit.hidden
			}
			if len(h.hot.Guesses) > 0 || len(h.hot.Boards) > 0 {
				if iconButton(c.Key("hg-orbit-hot-btn"), iconTrend, 15, onOff(h.orbit.hotOpen, pal.text, pal.faint), "短剧热搜").Clicked() {
					h.orbit.hotOpen = !h.orbit.hotOpen
				}
			}
		})

		ui.Row(c.Key("hg-orbit-sources")).Gap(6).Margin(14, 0, 0, 0).Justify(ui.Center).Children(func() {
			for _, s := range hgOrbitSources {
				on := h.orbit.source == s.id
				b := ui.ButtonBase(c.Key("hg-src:" + s.label)).Height(27).PaddingX(12).Radius(14).Gap(5).
					Row().Center().FocusRing(false).Label(s.label)
				bg, tc := ui.Transparent, pal.muted
				switch {
				case on:
					bg, tc = pal.tabActiveBg, pal.text
					b.Border(1, pal.tabActiveBd)
				case b.Hovered():
					bg, tc = pal.tabHoverBg, pal.text
				}
				b.Background(bg).Children(func() {
					switch s.id {
					case hgOrbitLive:
						ui.Box(c).Size(6, 6).Radius(3).Background(hgLiveBadgeColor.Alpha(onOff(on, float32(1), 0.65)))
					case hgOrbitComic:
						ui.Box(c).Size(6, 6).Radius(3).Background(hgComicBadgeColor.Alpha(onOff(on, float32(1), 0.65)))
					}
					ui.Text(c, s.label).FontSize(12.5).FontWeight(onOff(on, 600, 500)).TextColor(tc).SingleLine()
				})
				if b.Clicked() {
					a.hgSetOrbitSource(s.id)
				}
			}
		})

		if h.orbit.source == hgOrbitTrack && !hasCards {
			b := ui.ButtonBase(c.Key("hg-orbit-track-empty")).Margin(12, 0, 0, 0).Height(30).PaddingX(14).Radius(15).
				Center().Background(pal.pillBg).Border(1, pal.pillBorder).FocusRing(false).Label("去双星引力场看看")
			b.Children(func() {
				ui.Text(c, "暂无追剧记录 · 切回双星").FontSize(12).FontWeight(500).TextColor(pal.muted)
			})
			if b.Clicked() {
				a.hgSetOrbitSource(hgOrbitDual)
			}
		}

		// Show compact error retry banner if active channel failed with no cached items.
		if !hasCards && h.orbit.source != hgOrbitTrack {
			if errInfo := a.hgOrbitActiveError(h); errInfo.Kind != hg.ErrKindNone {
				ui.Box(c.Key("hg-orbit-err-wrap")).Margin(12, 0, 0, 0).Width(min(hw, 420)).Children(func() {
					a.hgErrorBanner(c, "hg-orbit", errInfo, func() {
						a.hgRetryOrbit(h)
					})
				})
			}
		}

		a.hgOrbitPopover(c, h, hw, q, focused)
	})
}

func (a *app) hgOrbitActiveError(h *hgApp) hg.ErrorInfo {
	if h == nil {
		return hg.ErrorInfo{}
	}
	switch h.orbit.source {
	case hgOrbitLive:
		return h.discoverList("真人剧").err
	case hgOrbitComic:
		return h.discoverList("漫剧").err
	case hgOrbitDual:
		l1, l2 := h.discoverList("真人剧"), h.discoverList("漫剧")
		if l1.err.Kind != hg.ErrKindNone && l2.err.Kind != hg.ErrKindNone {
			return l1.err
		}
	}
	return hg.ErrorInfo{}
}

func (a *app) hgRetryOrbit(h *hgApp) {
	if h == nil {
		return
	}
	switch h.orbit.source {
	case hgOrbitDual:
		if l := h.discoverList("真人剧"); l.err.Kind != hg.ErrKindNone {
			a.hgLoadDiscover("真人剧", len(l.items) == 0)
		}
		if l := h.discoverList("漫剧"); l.err.Kind != hg.ErrKindNone {
			a.hgLoadDiscover("漫剧", len(l.items) == 0)
		}
	case hgOrbitLive:
		l := h.discoverList("真人剧")
		a.hgLoadDiscover("真人剧", len(l.items) == 0)
	case hgOrbitComic:
		l := h.discoverList("漫剧")
		a.hgLoadDiscover("漫剧", len(l.items) == 0)
	}
}

func (a *app) hgOrbitPopover(c *ui.Context, h *hgApp, hw float32, q string, focused bool) {
	if focused && q != "" && q == h.sugFor && len(h.sugs) > 0 {
		words := h.sugs[:min(6, len(h.sugs))]
		ui.Column(c.Key("hg-orbit-sug")).Width(min(hw, 420)).Margin(12, 0, 0, 0).Padding(8, 6).Radius(14).
			Background(pal.popover).Border(1, pal.popoverBd).
			Shadow(0, 20, 48, -8, pal.searchShadow).Children(func() {
			for _, word := range words {
				row := ui.Row(c.Key("hg-opop:" + word)).Height(32).PaddingX(10).Gap(10).Radius(8).
					AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(word)
				row.HandleInput(func(ev ui.InputEvent) bool {
					if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
						h.query = word
						h.orbit.hotOpen = false
						a.hgSearch(word)
						return true
					}
					return false
				})
				if row.Hovered() {
					row.Background(pal.popoverHover)
				}
				row.Children(func() {
					ui.Icon(c, iconSearch).Size(12, 12).TextColor(pal.faint)
					ui.Text(c, word).FontSize(13).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				})
			}
		})
		return
	}

	if !h.orbit.hotOpen {
		return
	}
	var board *hg.HotBoard
	for i := range h.hot.Boards {
		if len(h.hot.Boards[i].Items) > 0 {
			board = &h.hot.Boards[i]
			break
		}
	}
	if len(h.hot.Guesses) == 0 && board == nil {
		return
	}
	ui.Column(c.Key("hg-orbit-hot")).Width(min(hw, 420)).Margin(12, 0, 0, 0).Padding(10, 8).Gap(6).Radius(14).
		Background(pal.popover).Border(1, pal.popoverBd).
		Shadow(0, 20, 48, -8, pal.searchShadow).Children(func() {
		ui.Row(c).Padding(0, 8, 2, 8).AlignItems(ui.Center).Children(func() {
			title := "红果热搜"
			if board != nil && board.Name != "" {
				title = board.Name
			}
			ui.Text(c, title).FontSize(11).FontWeight(600).TextColor(pal.faint).Grow(1)
			closeBtn := ui.ButtonBase(c.Key("hg-hot-close")).FocusRing(false).Label("收起热搜")
			closeBtn.Children(func() {
				ui.Text(c, "收起").FontSize(11).TextColor(onOff(closeBtn.Hovered(), pal.text, pal.muted))
			})
			if closeBtn.Clicked() {
				h.orbit.hotOpen = false
			}
		})
		if len(h.hot.Guesses) > 0 {
			ui.Row(c.Key("hg-orbit-guesses")).Wrap().Gap(6).Padding(2, 6, 4, 6).Children(func() {
				for _, g := range h.hot.Guesses[:min(6, len(h.hot.Guesses))] {
					chip := ui.Row(c.Key("hg-oguess:"+g)).Height(26).PaddingX(9).Radius(13).AlignItems(ui.Center).
						Background(pal.pillBg).Border(1, pal.pillBorder).Cursor(ui.CursorPointer).Label(g)
					if chip.Hovered() {
						chip.Background(pal.pillHoverBg)
					}
					chip.HandleInput(func(ev ui.InputEvent) bool {
						if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
							h.query = g
							h.orbit.hotOpen = false
							a.hgSearch(g)
							return true
						}
						return false
					})
					chip.Children(func() {
						ui.Text(c, g).FontSize(11.5).TextColor(pal.text).SingleLine()
					})
				}
			})
		}
		if board != nil {
			for i, it := range board.Items[:min(6, len(board.Items))] {
				row := ui.Row(c.Key(fmt.Sprintf("hg-ohot:%d:%s", i, it.Word))).Height(30).PaddingX(8).Gap(10).Radius(8).
					AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(it.Word)
				if row.Hovered() {
					row.Background(pal.popoverHover)
				}
				row.HandleInput(func(ev ui.InputEvent) bool {
					if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
						h.query = it.Word
						h.orbit.hotOpen = false
						a.hgSearch(it.Word)
						return true
					}
					return false
				})
				row.Children(func() {
					col := pal.faint
					if i == 0 {
						col = pal.accent
					} else if i < 3 {
						col = ui.Hex("#ff9f43")
					}
					ui.Text(c, fmt.Sprint(i+1)).FontSize(12).FontWeight(700).TextColor(col).Width(16)
					ui.Text(c, it.Word).FontSize(13).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
					if it.Score != "" {
						ui.Text(c, it.Score).FontSize(11).TextColor(pal.faint).SingleLine()
					}
				})
			}
		}
	})
}

func (a *app) hgOrbitCards(c *ui.Context, h *hgApp, w, hWin float32) {
	rects := hgOrbitLayout(w, hWin)
	if len(rects) == 0 {
		return
	}
	cards := h.currentOrbitCards()
	q := strings.ToLower(strings.TrimSpace(h.query))

	// In Dual-Wing mode, show subtle clickable constellation labels above the left (真人剧)
	// and right (漫剧) hero posters so the split is immediately legible at a glance.
	if h.orbit.source == hgOrbitDual {
		wingLabel := func(key, text string, tint ui.Color, x, y float32, target hgOrbitSource) {
			b := ui.ButtonBase(c.Key(key)).Absolute().Left(x).Top(max(58, y)).Height(25).PaddingX(11).Radius(13).
				Row().Gap(6).AlignItems(ui.Center).FocusRing(false).ZIndex(18).Label("只看" + text)
			bd := tint.Alpha(onOff(b.Hovered(), float32(0.55), 0.24))
			b.Background(onOff(b.Hovered(), pal.pillHoverBg, pal.pillBg)).Border(1, bd)
			b.Children(func() {
				ui.Box(c).Size(6.5, 6.5).Radius(3.5).Background(tint)
				ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(onOff(b.Hovered(), pal.strong, pal.text)).SingleLine()
			})
			if b.Clicked() {
				a.hgSetOrbitSource(target)
			}
		}
		if cards[0] != nil {
			wingLabel("hg-wing-live", "真人剧", hgLiveBadgeColor, rects[0].x, rects[0].y-34, hgOrbitLive)
		}
		if cards[hgOrbitWing] != nil {
			wingLabel("hg-wing-comic", "漫剧", hgComicBadgeColor, rects[5].x, rects[5].y-34, hgOrbitComic)
		}
	}

	for i, r := range rects {
		card := cards[i]
		if card == nil {
			continue
		}
		s := card.series
		matched := q == "" ||
			strings.Contains(strings.ToLower(s.Title), q) ||
			strings.Contains(strings.ToLower(s.Category), q) ||
			strings.Contains(strings.ToLower(s.Abstract), q)

		b := ui.ButtonBase(c.Key(fmt.Sprintf("hg-orbit-card:%d:%s", i, s.ID))).Absolute().Left(r.x).Top(r.y).Size(r.w, r.h).
			Radius(16).Clip().Background(pal.well).FocusRing(false).Label(s.Title).Transition(orbitCardMotion(i))
		hovered := b.Hovered()
		bd, z, pose := pal.cardBorder, 10, ui.Pose{}
		switch {
		case q != "" && !matched:
			pose = ui.Pose{Scale: 0.97, Fade: 0.8}
		case hovered:
			if card.wingTag != "" {
				bd = card.wingTint.Alpha(0.75)
			} else {
				bd = pal.cardHoverBd
			}
			z, pose = 24, ui.Pose{Scale: 1.035, Y: -4}
		case q != "":
			bd, z = pal.accent.Alpha(0.55), 18
		}
		b.Border(1, bd).ZIndex(z).Shadow(0, 18, 40, -12, pal.cardShadow).AnimatePose(pose, ui.Smooth)

		small := r.w < 178
		b.Children(func() {
			if h.img != nil {
				if bmp := h.img.getSized(s.CoverURL, coverEdge); bmp != nil {
					ui.Image(c.Key("cover"), bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover).Transition(coverFade)
				}
			}
			if card.progress != "" {
				ui.Row(c).Absolute().Top(9).Left(9).Height(20).PaddingX(7).Radius(10).
					AlignItems(ui.Center).Background(pal.accent.Alpha(0.88)).Children(func() {
					ui.Text(c, card.progress).FontSize(10.5).FontWeight(600).TextColor(white).SingleLine()
				})
			}
			ui.Box(c.Key("play")).Absolute().Top(9).Right(9).Size(28, 28).Radius(14).
				Background(onOff(card.wingTag != "", card.wingTint, pal.accent)).Center().
				AnimatePose(ui.Pose{Scale: onOff(hovered, float32(1), 0.6), Fade: onOff(hovered, float32(0), 1)}, ui.Snappy).
				Children(func() { ui.Icon(c, iconPlay).Size(11, 11).TextColor(white) })

			ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).
				Padding(onOff(small, float32(42), 52), onOff(small, float32(11), 13), onOff(small, float32(10), 12), onOff(small, float32(11), 13)).Gap(3).
				Gradient(ui.RGBA(6, 8, 14, 0), ui.RGBA(6, 8, 14, 0.92), 180).
				Children(func() {
					ui.Text(c, hgFirstNonEmpty(s.Title, "未命名剧集")).FontSize(onOff(small, float32(12.5), 14)).FontWeight(700).
						TextColor(ui.RGBA(255, 255, 255, 0.96)).SingleLine()
					meta := hgOrbitMeta(s)
					if meta != "" || card.wingTag != "" {
						ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
							if card.wingTag != "" {
								ui.Box(c).Size(5.5, 5.5).Radius(3).Background(card.wingTint)
							}
							if meta != "" {
								ui.Text(c, meta).FontSize(onOff(small, float32(10.5), 11.5)).TextColor(ui.RGBA(255, 255, 255, 0.78)).SingleLine()
							}
						})
					}
				})
		})
		if b.Clicked() {
			a.hgOpenDetail(s)
		}
	}
}

func hgOrbitMeta(s hg.Series) string {
	var parts []string
	if status := s.DisplayStatus(); status != "" {
		parts = append(parts, status)
	}
	if s.Category != "" {
		parts = append(parts, s.Category)
	} else if s.Score != "" {
		parts = append(parts, s.Score)
	}
	return strings.Join(parts, " · ")
}
