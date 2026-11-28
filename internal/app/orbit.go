package app

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

// orbitSource names which video list orbits the centered search hub.
// orbitPicks is the zero value and the fixed first landing source.
type orbitSource int

const (
	orbitPicks orbitSource = iota
	orbitFollow
	orbitLater
	orbitHistory
)

type orbitSourceSpec struct {
	id         orbitSource
	label      string
	needSignIn bool
}

var orbitSources = []orbitSourceSpec{
	{id: orbitPicks, label: "精选"},
	{id: orbitFollow, label: "关注", needSignIn: true},
	{id: orbitLater, label: "稍后", needSignIn: true},
	{id: orbitHistory, label: "历史", needSignIn: true},
}

// orbitState holds all UI state for the Orbital Home (引力场).
type orbitState struct {
	source         orbitSource
	userPicked     bool      // true once the user explicitly chooses a source pill
	hidden         bool      // 「收起周围画面」 pure-search mode
	hotOpen        bool      // 「热搜潜望」 opt-in hot-search popover
	offset         int       // starting index for 「换一批」 cycling
	pendingAdvance bool      // waiting for a freshly fetched ring before advancing
	theme          string    // "", "light", or "dark"; "" follows system appearance
	swipeAt        time.Time // last precise trackpad scroll event time
	scrollAt       time.Time // last committed scroll refresh time
}

// orbitCardRect is one card's placed rectangle on the orbital canvas.
type orbitCardRect struct {
	x, y, w, h float32
}

// orbitRing is how many covers ring the search hub at once.
const orbitRing = 8

var (
	iconOrbit   = strokeIcon(`<circle cx="12" cy="12" r="3"/><circle cx="12" cy="12" r="9" stroke-dasharray="3 3"/>`)
	iconRefresh = strokeIcon(`<path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/>`)
	iconEye     = strokeIcon(`<path d="M2 12s3.5-6.5 10-6.5S22 12 22 12s-3.5 6.5-10 6.5S2 12 2 12z"/><circle cx="12" cy="12" r="2.8"/>`)
	iconEyeOff  = strokeIcon(`<path d="M10.6 5.6A10.4 10.4 0 0 1 12 5.5c6.5 0 10 6.5 10 6.5a17 17 0 0 1-3.3 4.2"/><path d="M6.6 6.6A17.6 17.6 0 0 0 2 12s3.5 6.5 10 6.5c1.5 0 2.9-.3 4.1-.9"/><path d="m3 3 18 18"/>`)
	iconTrend   = strokeIcon(`<path d="m21 7-7.5 7.5-4.5-4.5L3 16"/><path d="M16 7h5v5"/>`)
	iconSun     = strokeIcon(`<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>`)
	iconMoon    = strokeIcon(`<path d="M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401"/>`)
)

func (a *app) isHome(p *page) bool {
	return a.home != nil && p == a.home && !p.plays()
}

func (a *app) orbitSpec(src orbitSource) orbitSourceSpec {
	for _, s := range orbitSources {
		if s.id == src {
			return s
		}
	}
	return orbitSources[len(orbitSources)-1]
}

// orbitSourcePage returns the underlying page supplying videos for src, or nil
// when the source requires login and is not yet available.
func (a *app) orbitSourcePage(src orbitSource) *page {
	switch src {
	case orbitFollow:
		if a.signedIn || (a.follow != nil && len(a.follow.list) > 0) {
			return a.follow
		}
		return nil
	case orbitLater:
		if a.mine != nil {
			return a.mine["稍后再看"]
		}
		return nil
	case orbitHistory:
		if a.mine != nil {
			return a.mine["观看历史"]
		}
		return nil
	default:
		if a.orbitPicks != nil {
			return a.orbitPicks
		}
		return a.picks
	}
}

// orbitPicksPage builds an independent, deduplicated, infinitely refreshable
// 精选 page for 引力场 so refreshing never duplicates videos or mirrors the
// sidebar 精选 list.
func (a *app) orbitPicksPage(c *dy.Client) *page {
	next := func() dy.Pager[dy.Video] {
		var tabs []int
		for _, tab := range dy.Tabs {
			if tab.ID != 0 {
				tabs = append(tabs, tab.ID)
			}
		}
		rand.Shuffle(len(tabs), func(i, j int) { tabs[i], tabs[j] = tabs[j], tabs[i] })
		streams := make([]dy.Pager[dy.Video], 0, len(tabs)+1)
		for _, id := range tabs {
			streams = append(streams, c.Picks(id))
		}
		streams = append(streams, c.Picks(0))
		return newOrbitStreamPager(streams, c.Feed, func(id string) bool {
			return a.picksHasID(id)
		})
	}
	return &page{title: "精选", grid: true, next: next(), restart: next}
}

func (a *app) picksHasID(id string) bool {
	if id == "" {
		return false
	}
	_, ok := a.picksIDs.Load(id)
	return ok
}

// newOrbitStreamPager merges multiple finite category pagers and an infinite
// fallback feed into one deduplicated stream that delivers at least orbitRing
// unseen videos per call and never loops back to already-seen video IDs.
func newOrbitStreamPager(streams []dy.Pager[dy.Video], infinite dy.Pager[dy.Video], skip func(id string) bool) dy.Pager[dy.Video] {
	var mu sync.Mutex
	seen := map[string]bool{}
	idx := 0
	staleInTab := 0

	return func() ([]dy.Video, error) {
		mu.Lock()
		defer mu.Unlock()
		var out []dy.Video
		var lastErr error
		const maxPulls = 6

		for pulls := 0; pulls < maxPulls && len(out) < orbitRing; pulls++ {
			var batch []dy.Video
			var err error
			if idx < len(streams) {
				batch, err = streams[idx]()
				if err != nil {
					lastErr = err
					idx++
					staleInTab = 0
					continue
				}
				if len(batch) == 0 {
					idx++
					staleInTab = 0
					continue
				}
			} else if infinite != nil {
				batch, err = infinite()
				if err != nil {
					lastErr = err
					break
				}
				if len(batch) == 0 {
					break
				}
			} else {
				break
			}

			added := 0
			for _, v := range batch {
				if v.ID != "" {
					if seen[v.ID] || (skip != nil && skip(v.ID)) {
						continue
					}
					seen[v.ID] = true
				}
				out = append(out, v)
				added++
			}
			if idx < len(streams) {
				if added == 0 {
					staleInTab++
					if staleInTab >= 1 {
						idx++
						staleInTab = 0
					}
				} else {
					staleInTab = 0
					// Rotate across categories after a full batch so consecutive rings stay varied.
					if len(out) >= orbitRing {
						idx++
					}
				}
			}
		}
		if len(out) == 0 && lastErr != nil {
			return nil, lastErr
		}
		return out, nil
	}
}

// ensureOrbitLoaded kicks off fetching the active orbit source when empty or
// running low on upcoming rings so 「换一批」 stays instant and non-repeating.
func (a *app) ensureOrbitLoaded() {
	src := a.orbitSourcePage(a.orbit.source)
	if src == nil || src.fetching || src.done || src.next == nil {
		return
	}
	if len(src.list) == 0 || len(src.list)-a.orbit.offset < 2*orbitRing {
		a.more(src)
	}
}

func (a *app) setOrbitSource(src orbitSource) {
	a.orbit.source = src
	a.orbit.userPicked = true
	a.orbit.offset = 0
	a.orbit.pendingAdvance = false
	if src == orbitHistory && a.mine != nil {
		a.freshHistory()
	}
	a.ensureOrbitLoaded()
}

// cycleOrbit advances to the next ring of unseen videos in the active source.
// When an infinite or still-loading source does not have a full next ring yet,
// it waits for the fresh fetch instead of looping back to already-seen videos.
func (a *app) cycleOrbit() {
	src := a.orbitSourcePage(a.orbit.source)
	if src == nil {
		return
	}
	if len(src.list) == 0 {
		a.ensureOrbitLoaded()
		return
	}
	nextOff := a.orbit.offset + orbitRing
	if nextOff+2*orbitRing > len(src.list) && !src.done && src.next != nil {
		a.more(src)
	}
	if nextOff+orbitRing <= len(src.list) {
		a.orbit.offset = nextOff
		a.orbit.pendingAdvance = false
		a.prefetchOrbitCovers()
		if src == a.orbitPicks {
			a.saveOrbitCache()
		}
		return
	}
	if !src.done && src.next != nil {
		a.orbit.pendingAdvance = true
		a.more(src)
		return
	}
	if nextOff < len(src.list) {
		a.orbit.offset = nextOff
	} else {
		a.orbit.offset = 0
	}
	a.orbit.pendingAdvance = false
}

func (a *app) prevOrbit() {
	if a.orbit.offset <= 0 {
		return
	}
	a.orbit.offset = max(0, a.orbit.offset-orbitRing)
	a.orbit.pendingAdvance = false
}

func (a *app) handleOrbitScroll(ev ui.InputEvent) bool {
	if ev.Kind != ui.InputScroll || ev.Momentum || ev.DY == 0 || a.orbit.hidden {
		return false
	}
	if ev.Precise {
		same := !a.orbit.swipeAt.IsZero() && ev.Time.Sub(a.orbit.swipeAt) < 200*time.Millisecond
		a.orbit.swipeAt = ev.Time
		if same {
			return true
		}
	}
	if !a.orbit.scrollAt.IsZero() && ev.Time.Sub(a.orbit.scrollAt) < 400*time.Millisecond {
		return true
	}
	if ev.DY > 0 {
		a.orbit.scrollAt = ev.Time
		a.cycleOrbit()
	} else if a.orbit.offset > 0 {
		a.orbit.scrollAt = ev.Time
		a.prevOrbit()
	}
	return true
}

// prefetchOrbitCovers warms visible (and upcoming) orbital covers as soon as
// videos arrive so covers render without waiting for an extra frame cycle.
func (a *app) prefetchOrbitCovers() {
	if a.img == nil {
		return
	}
	src := a.orbitSourcePage(a.orbit.source)
	if src == nil || len(src.list) == 0 {
		return
	}
	n := len(src.list)
	start := a.orbit.offset % n
	count := min(n, orbitRing)
	for i := range count {
		a.img.getSized(src.list[(start+i)%n].v.Cover, coverEdge)
	}
}

// onOrbitSourceLoaded completes a pending 「换一批」 advance once fresh videos arrive.
func (a *app) onOrbitSourceLoaded(p *page, added int) {
	if p == nil {
		return
	}
	if p == a.orbitPicks && added > 0 {
		a.saveOrbitCache()
	}
	if p != a.orbitSourcePage(a.orbit.source) {
		return
	}
	if a.orbit.pendingAdvance {
		nextOff := a.orbit.offset + orbitRing
		switch {
		case nextOff+orbitRing <= len(p.list):
			a.orbit.offset = nextOff
			a.orbit.pendingAdvance = false
			if p == a.orbitPicks {
				a.saveOrbitCache()
			}
		case p.done || added == 0:
			if nextOff < len(p.list) {
				a.orbit.offset = nextOff
			} else {
				a.orbit.offset = 0
			}
			a.orbit.pendingAdvance = false
		default:
			a.more(p)
		}
	}
	if added > 0 {
		a.prefetchOrbitCovers()
	}
	if a.isHome(a.cur()) && added > 0 && len(p.list)-a.orbit.offset < 2*orbitRing && !p.done && !p.fetching && p.next != nil {
		a.more(p)
	}
}

// playFromOrbit plays src's video idx on top of the Orbital Home so Escape
// or Back returns straight to the orbital canvas. src fetches what comes
// next, for both.
func (a *app) playFromOrbit(src *page, idx int) {
	if src == nil || idx < 0 || idx >= len(src.list) {
		return
	}
	a.open(&page{title: src.title, list: slices.Clone(src.list), idx: idx, from: src, fromGen: src.gen})
}

const (
	dockLeft       = float32(16)
	dockCollapsedW = float32(58)
	dockExpandedW  = float32(124)
	// contentLeft is where a page's content starts, right of the dock at rest;
	// the dock, opened, goes over it.
	contentLeft = dockLeft + dockCollapsedW + 14
)

// orbitFrame is the area 引力场's covers are drawn for: the window's at its
// usual size, less the dock at rest on the left and as much on the right. A
// larger or smaller area scales it.
const orbitFrameW, orbitFrameH = 1080, 820

// orbitScatter is the covers around the search field, in orbitFrame: of
// different sizes, some upright, set off from each other, so they read as
// thrown around it rather than laid out in a grid. Clockwise from the top left.
var orbitScatter = [orbitRing]orbitCardRect{
	{36, 92, 288, 162},   // top left
	{356, 70, 168, 224},  // top, upright
	{556, 120, 252, 142}, // top right
	{842, 210, 200, 266}, // right, upright
	{668, 508, 300, 169}, // bottom right
	{446, 496, 176, 234}, // bottom, upright
	{152, 522, 262, 147}, // bottom left
	{52, 286, 168, 210},  // left, upright
}

// orbitScale is how much orbitFrame grows or shrinks to fit a window w by h.
func orbitScale(w, h float32) float32 {
	return min(max(min((w-2*contentLeft)/orbitFrameW, h/orbitFrameH), 0.6), 1.3)
}

// orbitHubRect returns the centered search hub rectangle that surrounding cards
// must never overlap.
func orbitHubRect(w, h float32) (x, y, rw, rh float32) {
	rw = min(480, max(340, 480*orbitScale(w, h)))
	rh = 96
	return (w - rw) / 2, (h - rh) / 2, rw, rh
}

// orbitLayout is orbitScatter scaled to a window w by h and centered there.
func orbitLayout(w, h float32) []orbitCardRect {
	if w < 780 || h < 480 {
		return nil
	}
	s := orbitScale(w, h)
	ox, oy := (w-orbitFrameW*s)/2, (h-orbitFrameH*s)/2
	rects := make([]orbitCardRect, 0, orbitRing)
	for _, r := range orbitScatter {
		rects = append(rects, orbitCardRect{ox + r.x*s, oy + r.y*s, r.w * s, r.h * s})
	}
	return rects
}

// orbitView renders the full-canvas Orbital Home (引力场). The dock and the
// buttons at the top right are chrome's, over it as over every page.
func (a *app) orbitView(c *ui.Context) {
	w, h := c.Size()
	ui.Box(c.Key("orbit-root")).Fill().Children(func() {
		canvas := ui.Box(c.Key("orbit-canvas")).Fill().DragWindow().HandleInput(a.handleOrbitScroll)
		switch {
		case a.web.v != nil && a.web.layout, pal.dark:
			canvas.Background(pal.bg)
		default:
			canvas.RadialGradient(ui.RadialGradient{Center: ui.Point{X: 0.5, Y: 0.46}, Oklab: true, Stops: pal.veil})
		}
		canvas.Children(func() {
			if pal.dark && !(a.web.v != nil && a.web.layout) {
				// A faint sheen ringing the black center.
				ui.Box(c.Key("orbit-singularity")).Absolute().Top(0).Left(0).Right(0).Bottom(0).
					RadialGradient(ui.RadialGradient{Center: ui.Point{X: 0.5, Y: 0.5}, Radius: min(w, h) * 0.62, Oklab: true, Stops: pal.veil}).
					HitTest(func(float32, float32, ui.Rect) bool { return false })
			}
			if !a.orbit.hidden {
				a.orbitCards(c, w, h)
			}
			ui.Box(c.Key("orbit-brand")).Absolute().Top(11).Left(brandBarLeft()).ZIndex(30).Children(func() {
				a.brandSwitcher(c)
			})
			a.orbitCenterHub(c, w, h)
		})
		if a.notes != nil || a.dms != nil {
			ui.Box(c.Key("orbit-side-panel")).Absolute().Top(60).Right(18).Bottom(18).ZIndex(40).Children(func() {
				if a.notes != nil {
					a.notePanel(c, a.notes)
				} else if a.dms != nil {
					a.dmPanel(c, a.dms)
				}
			})
		}
	})
}

// chrome is what every page shares, over it: the dock down the left and the
// buttons at the top right (including the update pill when a release is found).
func (a *app) chrome(c *ui.Context, p *page) {
	_, h := c.Size()
	a.dock(c, h)
	a.topRight(c)
}

func (a *app) topRight(c *ui.Context) {
	right := float32(18)
	if runtime.GOOS == "windows" {
		right += 138 // three native caption buttons, 46 DIPs each
	}
	ui.Row(c.Key("top-right")).Absolute().Top(11).Right(right).Gap(6).AlignItems(ui.Center).ZIndex(30).Children(func() {
		a.updatePill(c)
		themeIcon := onOff(pal.dark, iconSun, iconMoon)
		themeLabel := onOff(pal.dark, "切换浅色主题", "切换深色主题")
		if iconButton(c.Key("theme"), themeIcon, 16, pal.muted, themeLabel).Clicked() {
			a.toggleOrbitTheme()
		}
		if a.mode != modeHongguo {
			if !a.isTikTok() {
				if iconButton(c.Key("dm"), iconChat, 16, onOff(a.dms != nil, pal.strong, pal.muted), onOff(a.dms != nil, "收起私信", "私信")).Clicked() {
					if a.dms != nil {
						a.dms = nil
					} else {
						a.openDMs()
					}
				}
				ui.Box(c.Key("bell-wrap")).Children(func() {
					if iconButton(c.Key("bell"), iconBell, 16, onOff(a.notes != nil, pal.strong, pal.muted), onOff(a.notes != nil, "收起通知", "通知")).Clicked() {
						if a.notes != nil {
							a.notes = nil
						} else {
							a.openNotes()
						}
					}
					if a.unread > 0 {
						ui.Box(c).Absolute().Top(3).Right(2).MinWidth(15).Height(15).PaddingX(4).Radius(8).
							Background(pal.accent).Center().
							HitTest(func(float32, float32, ui.Rect) bool { return false }). // the bell under it takes the click
							Children(func() {
								ui.Text(c, fmt.Sprint(min(a.unread, 99))).FontSize(9.5).FontWeight(700).TextColor(white)
							})
					}
				})
			}
			switch {
			case a.signedIn || a.me.Name != "":
				mine := a.mine[a.mineTab]
				on := a.isMine(a.pages[0])
				b := ui.ButtonBase(c.Key("account")).Height(32).Padding(0, 11, 0, 5).Gap(7).Radius(16).
					AlignItems(ui.Center).FocusRing(false).Label(onOff(mine != nil, "我的", a.me.Name)) // 我的 once it can open
				b.Background(onOff(on || b.Hovered(), pal.pillHoverBg, pal.pillBg)).Border(1, onOff(on, pal.tabActiveBd, pal.pillBorder)).Children(func() {
					a.avatar(c, pal, a.me.Avatar, a.me.Name, 22)
					ui.Text(c, a.me.Name).FontSize(12.5).FontWeight(onOff(on, 600, 500)).TextColor(pal.text).SingleLine().MaxWidth(100)
				})
				if b.Clicked() && mine != nil {
					a.showMine(a.mineTab)
				}
			case a.c != nil || a.isTikTok():
				b := ui.ButtonBase(c.Key("login")).Height(32).PaddingX(12).Gap(6).Radius(16).
					AlignItems(ui.Center).FocusRing(false).Label("登录")
				b.Background(onOff(b.Hovered(), pal.pillHoverBg, pal.pillBg)).Border(1, pal.pillBorder).Children(func() {
					ui.Icon(c, iconUser).Size(14, 14).TextColor(pal.text)
					ui.Text(c, "登录").FontSize(12.5).FontWeight(500).TextColor(pal.text)
				})
				if b.Clicked() {
					a.openActiveLogin()
				}
			}
		}
		ui.Box(c.Key("settings-wrap")).Children(func() {
			if iconButton(c.Key("settings"), iconSettings, 16, onOff(a.settingsOpen, pal.strong, pal.muted), "设置").Clicked() {
				a.settingsOpen = true
			}
			if a.upd.found != nil && a.upd.dismissed && !a.upd.busy {
				ui.Box(c).Absolute().Top(5).Right(5).Size(7, 7).Radius(4).Background(pal.accent).
					HitTest(func(float32, float32, ui.Rect) bool { return false })
			}
		})
	})
}

// dock is the navigation down the left of every page: icons at rest, their
// names as the pointer comes over it. The root shown is marked; pressed
// again it is refreshed, 引力场 bringing the next ring of covers.
func (a *app) dock(c *ui.Context, h float32) {
	type dockItem struct {
		key   string
		icon  *ui.SVG
		label string
		page  *page
		div   bool
	}
	var items []dockItem
	if a.isTikTok() {
		if a.ttFeed != nil {
			items = append(items, dockItem{key: "tt-feed", icon: iconSpark, label: "For You", page: a.ttFeed})
		}
		if a.ttFollow != nil {
			items = append(items, dockItem{key: "tt-follow", icon: iconFollow, label: "关注", page: a.ttFollow})
		}
		if a.ttLive != nil {
			items = append(items, dockItem{key: "tt-live", icon: iconLive, label: "直播", page: a.ttLive})
		}
	} else {
		items = []dockItem{{key: "home", icon: iconOrbit, label: "引力场", page: a.home}}
		if a.picks != nil {
			items = append(items, dockItem{key: "picks", icon: iconCompass, label: "精选", page: a.picks})
		}
		if a.feed != nil {
			items = append(items, dockItem{key: "feed", icon: iconSpark, label: "推荐", page: a.feed})
		}
		if a.follow != nil || a.friends != nil {
			items = append(items, dockItem{key: "div0", div: true})
		}
		if a.follow != nil {
			items = append(items, dockItem{key: "follow", icon: iconFollow, label: "关注", page: a.follow})
		}
		if a.friends != nil {
			items = append(items, dockItem{key: "friends", icon: iconFriends, label: "朋友", page: a.friends})
		}
		if a.live != nil || a.drama != nil {
			items = append(items, dockItem{key: "div1", div: true})
		}
		if a.live != nil {
			items = append(items, dockItem{key: "live", icon: iconLive, label: "直播", page: a.live})
		}
		if a.drama != nil {
			items = append(items, dockItem{key: "drama", icon: iconDrama, label: "短剧", page: a.drama})
		}
	}

	const dockItemH = float32(42)
	estH := float32(16)
	for _, it := range items {
		estH += onOff(it.div, float32(9), dockItemH+5)
	}
	root := a.pages[0]
	dock := ui.Column(c.Key("dock")).Absolute().Left(dockLeft).Top(max(66, (h-estH)/2)).
		Padding(8, 7).Gap(5).Radius(22).Clip().AlignItems(ui.Stretch).
		Background(pal.dockTint).Border(1, pal.dockBorder).
		InsetShadow(0, 1, 0, 0, pal.dockInset).Shadow(0, 16, 38, -8, pal.dockShadow).ZIndex(35)
	progress := dock.AnimateTo("expand", onOff(dock.Hovered(), float32(1), 0), ui.Snappy)
	progress = min(max(progress, 0), 1)
	labelAlpha := min(max((progress-0.2)/0.8, 0), 1)
	dock.Width(dockCollapsedW + (dockExpandedW-dockCollapsedW)*progress)

	dock.Children(func() {
		for _, it := range items {
			if it.div {
				ui.Box(c.Key(it.key)).Height(1).Margin(2, 10).Background(pal.hairline)
				continue
			}
			on := it.page == root
			b := ui.ButtonBase(c.Key("dock:" + it.key)).Row().Height(dockItemH).PaddingX(11).
				Gap(10).Radius(14).AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(it.label)
			bg, color := ui.Transparent, pal.muted
			switch {
			case on:
				bg, color = pal.dockActiveBg, pal.strong
			case b.Hovered():
				bg, color = pal.dockHoverBg, pal.text
			}
			b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 140 * time.Millisecond, Oklab: true}))
			iconScale := float32(1)
			switch {
			case b.Pressed():
				iconScale = 0.9
			case b.Hovered():
				iconScale = 1.1
			}
			b.Children(func() {
				ui.Icon(c, it.icon).Size(22, 22).TextColor(color).AnimatePose(ui.Pose{Scale: iconScale}, ui.Snappy)
				if labelAlpha > 0.01 {
					ui.Text(c, it.label).FontSize(13.5).FontWeight(onOff(on, 600, 500)).
						TextColor(color).SingleLine().Opacity(labelAlpha).
						AnimatePose(ui.Pose{X: (1 - labelAlpha) * -6}, ui.Snappy)
				}
			})
			if b.Clicked() && it.page != nil {
				if it.page == a.home && a.isHome(a.cur()) {
					a.cycleOrbit()
				} else {
					a.root(it.page)
				}
			}
		}
	})
}

func (a *app) orbitCenterHub(c *ui.Context, w, h float32) {
	hx, hy, hw, _ := orbitHubRect(w, h)
	q := strings.TrimSpace(a.query)
	srcPage := a.orbitSourcePage(a.orbit.source)
	spec := a.orbitSpec(a.orbit.source)

	ui.Column(c.Key("orbit-hub")).Absolute().Left(hx).Top(hy).Width(hw).AlignItems(ui.Center).ZIndex(25).Children(func() {
		focused := c.Focused(a.searchBox)
		field := ui.Row(c.Key("orbit-search-bar")).Width(hw).Height(52).Radius(20).Padding(0, 10, 0, 18).Gap(8).
			AlignItems(ui.Center).
			Background(onOff(focused, pal.searchFocus, pal.searchTint)).
			Border(1, onOff(focused, pal.focusBorder, pal.searchBorder)).
			InsetShadow(0, 1, 0, 0, pal.searchInset)
		if pal.dark {
			field.Shadow(0, 0, 64, 4, pal.searchShadow)
		} else {
			field.Shadow(0, 14, 38, -6, pal.searchShadow)
		}
		field.Children(func() {
			ui.Icon(c, iconSearch).Size(16, 16).TextColor(onOff(focused, pal.text, pal.muted))
			in := ui.TextInputBase(c.Key("orbit-search"), &a.query).Bind(&a.searchBox).Grow(1).
				Placeholder("搜索…").FontSize(15).Label("搜索")
			if in.Submitted() {
				a.orbit.hotOpen = false
				a.search(a.query)
			} else if in.Changed() {
				a.suggest()
			}
			if a.query != "" {
				if iconButton(c.Key("orbit-clear"), iconClose, 13, pal.muted, "清空搜索").Clicked() {
					a.query = ""
				}
				return
			}
			if srcPage != nil && len(srcPage.list) > 0 && !a.orbit.hidden {
				btn := iconButton(c.Key("orbit-cycle"), iconRefresh, 14, onOff(a.orbit.pendingAdvance, pal.accent, pal.faint), "换一批")
				if a.orbit.pendingAdvance {
					btn.Opacity(0.6)
				} else if btn.Clicked() {
					a.cycleOrbit()
				}
			}
			eyeIcon := onOff(a.orbit.hidden, iconEye, iconEyeOff)
			eyeLabel := onOff(a.orbit.hidden, "显示周围画面", "收起周围画面")
			if iconButton(c.Key("orbit-hide"), eyeIcon, 15, onOff(a.orbit.hidden, pal.text, pal.faint), eyeLabel).Clicked() {
				a.orbit.hidden = !a.orbit.hidden
			}
			if len(a.hot) > 0 {
				if iconButton(c.Key("orbit-hot-btn"), iconTrend, 15, onOff(a.orbit.hotOpen, pal.text, pal.faint), "热搜潜望").Clicked() {
					a.orbit.hotOpen = !a.orbit.hotOpen
				}
			}
		})

		ui.Row(c.Key("orbit-sources")).Gap(6).Margin(14, 0, 0, 0).Justify(ui.Center).Children(func() {
			for _, s := range orbitSources {
				on := a.orbit.source == s.id
				b := ui.ButtonBase(c.Key("src:" + s.label)).Height(27).PaddingX(12).Radius(14).
					Center().FocusRing(false).Label(s.label)
				bg, tc := ui.Transparent, pal.muted
				switch {
				case on:
					bg, tc = pal.tabActiveBg, pal.text
					b.Border(1, pal.tabActiveBd)
				case b.Hovered():
					bg, tc = pal.tabHoverBg, pal.text
				}
				b.Background(bg).Children(func() {
					ui.Text(c, s.label).FontSize(12.5).FontWeight(onOff(on, 600, 500)).TextColor(tc).SingleLine()
				})
				if b.Clicked() {
					a.setOrbitSource(s.id)
				}
			}
		})

		if srcPage == nil && spec.needSignIn && a.c != nil {
			b := ui.ButtonBase(c.Key("orbit-signin-cta")).Margin(12, 0, 0, 0).Height(30).PaddingX(14).Radius(15).
				Center().Background(pal.pillBg).Border(1, pal.pillBorder).
				FocusRing(false).Label("扫码登录查看")
			b.Children(func() {
				ui.Text(c, "登录查看"+spec.label).FontSize(12).FontWeight(500).TextColor(pal.text)
			})
			if b.Clicked() {
				a.openLogin()
			}
		}

		a.orbitPopover(c, hw, q, focused)
	})
}

func (a *app) orbitPopover(c *ui.Context, hw float32, q string, focused bool) {
	var words []string
	isHot := false
	switch {
	case focused && q != "" && q == a.sugFor && len(a.sugs) > 0:
		words = a.sugs[:min(6, len(a.sugs))]
	case a.orbit.hotOpen && len(a.hot) > 0:
		isHot = true
		for _, h := range a.hot[:min(6, len(a.hot))] {
			words = append(words, h.Word)
		}
	}
	if len(words) == 0 {
		return
	}

	ui.Column(c.Key("orbit-popover")).Width(min(hw, 400)).Margin(12, 0, 0, 0).Padding(8, 6).Radius(14).
		Background(pal.popover).Border(1, pal.popoverBd).
		Shadow(0, 20, 48, -8, pal.searchShadow).Children(func() {
		if isHot {
			ui.Row(c).Padding(2, 10, 6, 10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "抖音热搜").FontSize(11).FontWeight(600).TextColor(pal.faint).Grow(1)
				closeBtn := ui.ButtonBase(c.Key("hot-close")).FocusRing(false).Label("收起热搜")
				closeBtn.Children(func() {
					ui.Text(c, "收起").FontSize(11).TextColor(onOff(closeBtn.Hovered(), pal.text, pal.muted))
				})
				if closeBtn.Clicked() {
					a.orbit.hotOpen = false
				}
			})
		}
		for i, word := range words {
			row := ui.Row(c.Key("pop:" + word)).Height(32).PaddingX(10).Gap(10).Radius(8).
				AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(word)
			row.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
					a.query = word
					a.orbit.hotOpen = false
					a.search(word)
					return true
				}
				return false
			})
			if row.Hovered() {
				row.Background(pal.popoverHover)
			}
			row.Children(func() {
				if !isHot {
					ui.Icon(c, iconSearch).Size(12, 12).TextColor(pal.faint)
					ui.Text(c, word).FontSize(13).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
					return
				}
				rank, col := fmt.Sprint(i+1), pal.faint
				if i == 0 {
					col = pal.accent
				} else if i < 3 {
					col = ui.Hex("#ff9f43")
				}
				ui.Text(c, rank).FontSize(12).FontWeight(700).TextColor(col).Width(16)
				ui.Text(c, word).FontSize(13).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if v := a.hot[i].Value; v > 0 {
					ui.Text(c, count(v)).FontSize(11).TextColor(pal.faint)
				}
			})
		}
	})
}

// orbitCardMotion brings a ring's covers in one after another, clockwise, as
// the ring before fades: a card is a video's, so 换一批 replaces them.
func orbitCardMotion(i int) ui.ElementTransition {
	return ui.ElementTransition{
		Position:     true,
		Animation:    &ui.Animation{Spring: ui.Smooth.Spring, Delay: time.Duration(i) * 20 * time.Millisecond},
		Enter:        &ui.Motion{Opacity: 0, Scale: 0.94, Y: 10},
		Exit:         &ui.Motion{Opacity: 0, Scale: 0.97},
		ExitDuration: 160 * time.Millisecond,
	}
}

// coverFade fades a cover in once loaded, rather than popping over its card.
var coverFade = ui.ElementTransition{Duration: 120 * time.Millisecond, Enter: &ui.Motion{Opacity: 0}}

func (a *app) orbitCards(c *ui.Context, w, h float32) {
	src := a.orbitSourcePage(a.orbit.source)
	if src == nil || len(src.list) == 0 {
		return
	}
	rects := orbitLayout(w, h)
	if len(rects) == 0 {
		return
	}
	q := strings.ToLower(strings.TrimSpace(a.query))
	n := len(src.list)
	start := a.orbit.offset % n

	for i, r := range rects {
		if i >= n {
			break
		}
		slotIdx := (start + i) % n
		v := src.list[slotIdx].v
		matched := q == "" ||
			strings.Contains(strings.ToLower(v.Desc), q) ||
			strings.Contains(strings.ToLower(v.Author), q)

		b := ui.ButtonBase(c.Key(fmt.Sprintf("orbit-card:%d:%s", slotIdx, v.ID))).Absolute().Left(r.x).Top(r.y).Size(r.w, r.h).
			Radius(18).Clip().Background(pal.well).FocusRing(false).Label(v.Desc).Transition(orbitCardMotion(i))
		hovered := b.Hovered()
		if hovered {
			a.img.get(v.Poster) // what the stage shows until the video does, there as it is clicked
		}
		bd, z, pose := pal.cardBorder, 10, ui.Pose{}
		switch {
		case q != "" && !matched:
			pose = ui.Pose{Scale: 0.97, Fade: 0.8}
		case hovered:
			bd, z, pose = pal.cardHoverBd, 24, ui.Pose{Scale: 1.03, Y: -3}
		case q != "":
			bd, z = pal.accent.Alpha(0.55), 18
		}
		b.Border(1, bd).ZIndex(z).Shadow(0, 18, 40, -12, pal.cardShadow).AnimatePose(pose, ui.Smooth)

		small := r.w < 200 // a little card, its text smaller
		b.Children(func() {
			if bmp := a.img.getSized(v.Cover, coverEdge); bmp != nil {
				ui.Image(c.Key("cover"), bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover).Transition(coverFade)
			}
			if v.Live != nil {
				liveBadge(c)
			} else if v.Duration > 0 {
				ui.Row(c).Absolute().Top(10).Left(10).Height(20).PaddingX(7).Radius(6).
					AlignItems(ui.Center).Background(ui.RGBA(0, 0, 0, 0.52)).Children(func() {
					ui.Text(c, clock(v.Duration)).FontSize(11).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.92))
				})
			}
			ui.Box(c.Key("play")).Absolute().Top(10).Right(10).Size(28, 28).Radius(14).
				Background(pal.accent).Center().
				AnimatePose(ui.Pose{Scale: onOff(hovered, float32(1), 0.6), Fade: onOff(hovered, float32(0), 1)}, ui.Snappy).
				Children(func() { ui.Icon(c, iconPlay).Size(11, 11).TextColor(white) })
			ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).
				Padding(onOff(small, float32(28), 40), onOff(small, float32(10), 14), onOff(small, float32(9), 12), onOff(small, float32(10), 14)).Gap(3).
				Gradient(ui.RGBA(8, 10, 16, 0), ui.RGBA(8, 10, 16, 0.86), 180).
				Children(func() {
					title := strings.TrimSpace(v.Desc)
					if title == "" {
						title = "@" + v.Author
					}
					ui.Text(c, title).FontSize(onOff(small, float32(12), 13.5)).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.96)).MaxLines(2)
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "@"+v.Author).FontSize(onOff(small, float32(10.5), 11.5)).TextColor(ui.RGBA(255, 255, 255, 0.74)).SingleLine().Grow(1).Shrink(1).MinWidth(0)
						if when := ago(v.Created, time.Now()); when != "" {
							ui.Text(c, when).FontSize(onOff(small, float32(10.5), 11.5)).TextColor(ui.RGBA(255, 255, 255, 0.56)).SingleLine()
						}
					})
				})
		})
		if b.Clicked() {
			a.playFromOrbit(src, slotIdx)
		}
	}
	// The next ring's covers load now, so 换一批 shows them at once.
	if n > orbitRing {
		for i := range orbitRing {
			a.img.getSized(src.list[(start+orbitRing+i)%n].v.Cover, coverEdge)
		}
	}
}
