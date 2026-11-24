package app

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// orbitSource names which video list orbits the centered search hub.
type orbitSource int

const (
	orbitFollow orbitSource = iota
	orbitLater
	orbitHistory
	orbitPicks
)

type orbitSourceSpec struct {
	id         orbitSource
	label      string
	needSignIn bool
}

var orbitSources = []orbitSourceSpec{
	{id: orbitFollow, label: "关注", needSignIn: true},
	{id: orbitLater, label: "稍后", needSignIn: true},
	{id: orbitHistory, label: "历史", needSignIn: true},
	{id: orbitPicks, label: "精选"},
}

// orbitState holds all UI state for the Orbital Home (引力场).
type orbitState struct {
	source     orbitSource
	userPicked bool   // true once the user explicitly chooses a source pill
	hidden     bool   // 「收起周围画面」 pure-search mode
	hotOpen    bool   // 「热搜潜望」 opt-in hot-search popover
	hovered    int    // hovered orbit card index, -1 when none
	offset     int    // starting index for 「换一批」 cycling
	theme      string // "", "light", or "dark"; "" follows system appearance
}

// orbitCardRect is one card's placed rectangle on the orbital canvas.
type orbitCardRect struct {
	x, y, w, h float32
	portrait   bool
}

// orbitPalette groups every surface, ink, border, and glass token for one
// appearance so light and dark modes stay completely coherent.
type orbitPalette struct {
	dark         bool
	opaqueBg     ui.Color
	veilStops    []ui.GradientStop
	text         ui.Color
	muted         ui.Color
	faint         ui.Color
	divider       ui.Color
	searchTint    ui.Color
	searchFocus   ui.Color
	searchBorder  ui.Color
	focusBorder   ui.Color
	searchInset   ui.Color
	searchShadow  ui.Color
	dockTint      ui.Color
	dockBorder    ui.Color
	dockInset     ui.Color
	dockShadow    ui.Color
	dockActiveBg  ui.Color
	dockHoverBg   ui.Color
	pillBg        ui.Color
	pillHoverBg   ui.Color
	pillBorder    ui.Color
	tabActiveBg   ui.Color
	tabActiveBd   ui.Color
	tabHoverBg    ui.Color
	cardBorder    ui.Color
	cardHoverBd   ui.Color
	cardShadow    ui.Color
	popoverBg     ui.Color
	popoverBd     ui.Color
	popoverHover  ui.Color
}

var (
	orbitLightPal = orbitPalette{
		dark:     false,
		opaqueBg: ui.Hex("#f5f7fb"),
		// Pure luminous porcelain white: brightest center glow breathing softly
		// into cool alabaster edges, completely opaque with zero wallpaper bleed.
		veilStops: []ui.GradientStop{
			{Color: ui.Hex("#ffffff"), At: 0},
			{Color: ui.Hex("#fafbfe"), At: 0.34},
			{Color: ui.Hex("#f2f4f9"), At: 0.72},
			{Color: ui.Hex("#eaedf4"), At: 1},
		},
		text:         ui.RGBA(17, 20, 30, 0.92),
		muted:        ui.RGBA(17, 20, 30, 0.56),
		faint:        ui.RGBA(17, 20, 30, 0.38),
		divider:      ui.RGBA(17, 20, 30, 0.08),
		searchTint:   ui.Hex("#ffffff"),
		searchFocus:  ui.Hex("#ffffff"),
		searchBorder: ui.RGBA(15, 23, 42, 0.09),
		focusBorder:  ui.RGBA(15, 23, 42, 0.24),
		searchInset:  ui.RGBA(255, 255, 255, 1),
		searchShadow: ui.RGBA(15, 23, 42, 0.09),
		dockTint:     ui.RGBA(255, 255, 255, 0.92),
		dockBorder:   ui.RGBA(15, 23, 42, 0.08),
		dockInset:    ui.RGBA(255, 255, 255, 1),
		dockShadow:   ui.RGBA(15, 23, 42, 0.08),
		dockActiveBg: ui.RGBA(15, 23, 42, 0.08),
		dockHoverBg:  ui.RGBA(15, 23, 42, 0.05),
		pillBg:       ui.RGBA(255, 255, 255, 0.88),
		pillHoverBg:  ui.Hex("#ffffff"),
		pillBorder:   ui.RGBA(15, 23, 42, 0.08),
		tabActiveBg:  ui.Hex("#ffffff"),
		tabActiveBd:  ui.RGBA(15, 23, 42, 0.09),
		tabHoverBg:   ui.RGBA(15, 23, 42, 0.045),
		cardBorder:   ui.RGBA(15, 23, 42, 0.08),
		cardHoverBd:  ui.RGBA(15, 23, 42, 0.22),
		cardShadow:   ui.RGBA(15, 23, 42, 0.16),
		popoverBg:    ui.Hex("#ffffff"),
		popoverBd:    ui.RGBA(15, 23, 42, 0.10),
		popoverHover: ui.RGBA(15, 23, 42, 0.05),
	}

	orbitDarkPal = orbitPalette{
		dark:     true,
		opaqueBg: ui.Hex("#000000"),
		// Pure #000000 black-hole singularity at center with a whisper-soft
		// event-horizon depth falloff back into absolute void black.
		veilStops: []ui.GradientStop{
			{Color: ui.Hex("#000000"), At: 0},
			{Color: ui.Hex("#000000"), At: 0.24},
			{Color: ui.RGBA(130, 152, 210, 0.075), At: 0.38},
			{Color: ui.RGBA(40, 48, 76, 0.045), At: 0.56},
			{Color: ui.Hex("#000000"), At: 0.86},
			{Color: ui.Hex("#000000"), At: 1},
		},
		text:         ui.RGBA(255, 255, 255, 0.94),
		muted:        ui.RGBA(255, 255, 255, 0.56),
		faint:        ui.RGBA(255, 255, 255, 0.34),
		divider:      ui.RGBA(255, 255, 255, 0.08),
		searchTint:   ui.Hex("#030305"),
		searchFocus:  ui.Hex("#06070c"),
		searchBorder: ui.RGBA(255, 255, 255, 0.20),
		focusBorder:  ui.RGBA(255, 255, 255, 0.44),
		searchInset:  ui.RGBA(255, 255, 255, 0.14),
		searchShadow: ui.RGBA(135, 165, 240, 0.16),
		dockTint:     ui.RGBA(10, 11, 16, 0.92),
		dockBorder:   ui.RGBA(255, 255, 255, 0.09),
		dockInset:    ui.RGBA(255, 255, 255, 0.08),
		dockShadow:   ui.RGBA(0, 0, 0, 0.85),
		dockActiveBg: ui.RGBA(255, 255, 255, 0.12),
		dockHoverBg:  ui.RGBA(255, 255, 255, 0.07),
		pillBg:       ui.RGBA(255, 255, 255, 0.05),
		pillHoverBg:  ui.RGBA(255, 255, 255, 0.11),
		pillBorder:   ui.RGBA(255, 255, 255, 0.10),
		tabActiveBg:  ui.RGBA(255, 255, 255, 0.11),
		tabActiveBd:  ui.RGBA(255, 255, 255, 0.20),
		tabHoverBg:   ui.RGBA(255, 255, 255, 0.06),
		cardBorder:   ui.RGBA(255, 255, 255, 0.11),
		cardHoverBd:  ui.RGBA(255, 255, 255, 0.38),
		cardShadow:   ui.RGBA(0, 0, 0, 0.80),
		popoverBg:    ui.RGBA(10, 11, 16, 0.96),
		popoverBd:    ui.RGBA(255, 255, 255, 0.12),
		popoverHover: ui.RGBA(255, 255, 255, 0.07),
	}

	orbitCardBg = ui.Hex("#171a26")
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

func (a *app) orbitIsDark() bool {
	switch a.orbit.theme {
	case "dark":
		return true
	case "light":
		return false
	default:
		if a.win == nil {
			return false
		}
		return mygo.Theme.IsDark()
	}
}

func (a *app) orbitPal() orbitPalette {
	if a.orbitIsDark() {
		return orbitDarkPal
	}
	return orbitLightPal
}

func (a *app) applyThemeSource() {
	if a.win == nil {
		return
	}
	switch a.orbit.theme {
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func (a *app) toggleOrbitTheme() {
	if a.orbitIsDark() {
		a.orbit.theme = "light"
	} else {
		a.orbit.theme = "dark"
	}
	a.applyThemeSource()
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
		return a.picks
	}
}

// ensureOrbitLoaded kicks off fetching the active orbit source when empty.
func (a *app) ensureOrbitLoaded() {
	src := a.orbitSourcePage(a.orbit.source)
	if src != nil && len(src.list) == 0 && !src.fetching && !src.done && src.next != nil {
		a.more(src)
	}
}

func (a *app) setOrbitSource(src orbitSource) {
	a.orbit.source = src
	a.orbit.userPicked = true
	a.orbit.offset = 0
	a.orbit.hovered = -1
	a.ensureOrbitLoaded()
}

// cycleOrbit advances to the next ring of videos in the active source, or
// fetches another page when nearing the end.
func (a *app) cycleOrbit() {
	src := a.orbitSourcePage(a.orbit.source)
	if src == nil {
		return
	}
	if len(src.list) == 0 {
		a.ensureOrbitLoaded()
		return
	}
	nextOff := a.orbit.offset + 8
	if nextOff >= len(src.list) {
		if !src.done && src.next != nil {
			a.more(src)
		}
		nextOff = 0
	}
	a.orbit.offset = nextOff
	a.orbit.hovered = -1
}

// playFromOrbit opens srcPage playing at idx on top of the Orbital Home so
// Escape or Back returns straight to the orbital canvas.
func (a *app) playFromOrbit(src *page, idx int) {
	if src == nil || idx < 0 || idx >= len(src.list) {
		return
	}
	playPage := &page{
		title: src.title,
		next:  src.next,
		list:  append([]*slot(nil), src.list...),
		idx:   idx,
		done:  src.done,
	}
	a.open(playPage)
}

const (
	dockLeft       = float32(16)
	dockCollapsedW = float32(58)
	dockExpandedW  = float32(124)
	dockSafeLeft   = dockLeft + dockExpandedW + 14 // 154: expanded dock never covers any card
)

// orbitHubRect returns the centered search hub rectangle that surrounding cards
// must never overlap.
func orbitHubRect(w, h float32) (x, y, rw, rh float32) {
	rw = min(float32(480), max(float32(340), w-520))
	rh = 96
	cx := max(w/2, (dockSafeLeft+w-24)/2)
	x = cx - rw/2
	y = (h - rh) / 2
	return x, y, rw, rh
}

// orbitLayout computes up to 8 balanced, non-overlapping video card slots in an
// elliptical halo around the centered search hub. Every returned rectangle is
// guaranteed clear of the center hub, the EXPANDED left dock, and the top bar.
func orbitLayout(w, h float32) []orbitCardRect {
	if w < 780 || h < 540 {
		return nil
	}
	hx, hy, hw, hh := orbitHubRect(w, h)
	const (
		leftMargin   = dockSafeLeft
		rightMargin  = float32(24)
		topMargin    = float32(62)
		bottomMargin = float32(20)
		padX         = float32(20)
		padY         = float32(20)
	)
	cx, cy := hx+hw/2, h/2
	hubL, hubR := hx-padX, hx+hw+padX
	hubT, hubB := hy-padY, hy+hh+padY

	// Scale card sizes smoothly between compact (960x620) and roomy (1440x900+).
	scale := min(max((w-960)/480, 0), 1)*0.20 + 0.86
	landW := float32(math.Round(float64(212 * scale)))
	landH := float32(math.Round(float64(128 * scale)))
	portW := float32(math.Round(float64(168 * scale)))
	portH := float32(math.Round(float64(214 * scale)))

	// Available bands above, below, left, and right of the center hub.
	topBand := hubT - topMargin
	botBand := (h - bottomMargin) - hubB
	if landH > topBand && topBand >= 90 {
		landH = topBand
		landW = float32(math.Round(float64(landH * 1.64)))
	}
	if landH > botBand && botBand >= 90 {
		landH = botBand
		landW = float32(math.Round(float64(landH * 1.64)))
	}

	leftBand := hubL - leftMargin
	rightBand := (w - rightMargin) - hubR
	if portW > leftBand-8 && leftBand >= 118 {
		portW = leftBand - 8
		portH = float32(math.Round(float64(portW * 1.26)))
	}
	if portW > rightBand-8 && rightBand >= 118 {
		portW = rightBand - 8
		portH = float32(math.Round(float64(portW * 1.26)))
	}

	// Harmonic 8-card elliptical ring staying completely clear of the expanded dock:
	//   Top arc:    [0] top-left, [1] top-center-left, [2] top-center-right
	//   Flanks:     [3] right-upper portrait, [4] left-mid portrait
	//   Bottom arc: [5] bottom-right, [6] bottom-center, [7] bottom-left
	topY := max(topMargin+10, hubT-landH-76)
	botY := min(h-bottomMargin-landH-14, hubB+76)
	usableL := leftMargin
	usableR := w - rightMargin - 6
	leftPortY := max(topY+landH+32, cy-portH*0.44)

	candidates := []orbitCardRect{
		// 0: Top-left landscape
		{x: usableL + 4, y: min(hubT-landH-16, topY+14), w: landW, h: landH},
		// 1: Top-center-left landscape
		{x: cx - landW - 22, y: max(topMargin+6, topY-16), w: landW, h: landH},
		// 2: Top-center-right landscape
		{x: cx + 22, y: max(topMargin+8, topY-12), w: landW, h: landH},
		// 3: Right-upper portrait
		{x: min(usableR-portW, hubR+max(20, (rightBand-portW)*0.44)), y: max(topMargin+36, cy-portH*0.72), w: portW, h: portH, portrait: true},
		// 4: Left-mid portrait
		{x: max(usableL+4, hubL-portW-max(18, (leftBand-portW)*0.48)), y: leftPortY, w: portW, h: portH, portrait: true},
		// 5: Bottom-right landscape
		{x: min(usableR-landW-8, max(hubR-14, cx+hw/2-landW*0.18)), y: max(hubB+36, botY-8), w: landW, h: landH},
		// 6: Bottom-center landscape
		{x: cx - landW*0.5 + 8, y: min(h-bottomMargin-landH-6, botY+22), w: landW, h: landH},
		// 7: Bottom-left landscape
		{x: max(usableL+28, cx-hw/2-landW*0.56), y: min(h-bottomMargin-landH-8, max(leftPortY+portH+22, botY+4)), w: landW, h: landH},
	}

	// Keep only slots strictly inside viewport bounds and free of overlap with
	// the hub or already accepted cards.
	var placed []orbitCardRect
	overlaps := func(a, b orbitCardRect, gap float32) bool {
		return a.x < b.x+b.w+gap && a.x+a.w+gap > b.x && a.y < b.y+b.h+gap && a.y+a.h+gap > b.y
	}
	hubBox := orbitCardRect{x: hubL, y: hubT, w: hubR - hubL, h: hubB - hubT}

	for _, r := range candidates {
		if r.x < leftMargin || r.y < topMargin || r.x+r.w > w-rightMargin || r.y+r.h > h-bottomMargin {
			continue
		}
		if overlaps(r, hubBox, 0) {
			continue
		}
		clash := false
		for _, prev := range placed {
			if overlaps(r, prev, 14) {
				clash = true
				break
			}
		}
		if !clash {
			placed = append(placed, r)
		}
	}
	return placed
}

// orbitView renders the full-canvas Orbital Home (引力场) in either Light or
// Dark translucent glass appearance, with zero decorative lines.
func (a *app) orbitView(c *ui.Context) {
	w, h := c.Size()
	op := a.orbitPal()
	canvas := ui.Box(c.Key("orbit-canvas")).Fill().DragWindow()
	switch {
	case a.web.v != nil && a.web.layout:
		canvas.Background(op.opaqueBg)
	case op.dark:
		// Pure #000000 OLED void so desktop wallpaper never muddies the black hole.
		canvas.Background(op.opaqueBg)
	default:
		// Pure opaque luminous porcelain gradient with zero wallpaper bleed.
		canvas.RadialGradient(ui.RadialGradient{
			Center: ui.Point{X: 0.5, Y: 0.46},
			Oklab:  true,
			Stops:  op.veilStops,
		})
	}
	canvas.Children(func() {
		// In pure-black dark mode, paint an ultra-soft event-horizon radial depth
		// over #000000: absolute black singularity at the center, whisper-soft
		// accretion sheen around it, dissolving seamlessly back into pure #000000.
		if op.dark && !(a.web.v != nil && a.web.layout) {
			ui.Box(c.Key("orbit-singularity")).Absolute().Top(0).Left(0).Right(0).Bottom(0).
				RadialGradient(ui.RadialGradient{
					Center: ui.Point{X: 0.5, Y: 0.5},
					Radius: min(w, h) * 0.62,
					Oklab:  true,
					Stops:  op.veilStops,
				}).
				HitTest(func(float32, float32, ui.Rect) bool { return false })
		}
		// 1. Surrounding video cards (hidden cleanly in pure-search mode).
		if !a.orbit.hidden {
			a.orbitCards(c, w, h, op)
		}

		// 2. Top-left brand mark beside macOS traffic lights.
		brandLeft := float32(94)
		if runtime.GOOS == "windows" {
			brandLeft = 20
		}
		ui.Row(c.Key("orbit-brand")).Absolute().Top(15).Left(brandLeft).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Image(c, logo).Size(22, 22)
			ui.Text(c, "MyDouyin").FontSize(16).FontWeight(700).TextColor(op.text)
		})

		// 3. Top-right quiet corner: theme toggle, 推荐流, inbox, notifications, account, settings.
		a.orbitTopRight(c, op)

		// 4. Whisper-thin left floating navigation dock.
		a.orbitDock(c, h, op)

		// 5. Centered Liquid Glass Search Capsule + minimal 4-word source switcher.
		a.orbitCenterHub(c, w, h, op)

		// 6. Optional floating side panels (通知 / 私信).
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

func (a *app) orbitTopRight(c *ui.Context, op orbitPalette) {
	right := float32(18)
	if runtime.GOOS == "windows" {
		right += 138
	}
	ui.Row(c.Key("orbit-top-right")).Absolute().Top(11).Right(right).Gap(6).AlignItems(ui.Center).ZIndex(30).Children(func() {
		// Light / Dark theme toggle
		themeIcon := onOff(op.dark, iconSun, iconMoon)
		themeLabel := onOff(op.dark, "切换浅色主题", "切换深色主题")
		if iconButton(c.Key("orbit-theme"), themeIcon, 16, op.muted, themeLabel).Clicked() {
			a.toggleOrbitTheme()
		}

		if a.c != nil {
			dmColor := onOff(a.dms != nil, op.text, op.muted)
			if iconButton(c.Key("orbit-dm"), iconChat, 16, dmColor, "私信").Clicked() {
				if a.dms != nil {
					a.dms = nil
				} else {
					a.openDMs()
				}
			}
			bell := ui.Box(c.Key("orbit-bell-wrap"))
			bell.Children(func() {
				bellColor := onOff(a.notes != nil, op.text, op.muted)
				if iconButton(c.Key("orbit-bell"), iconBell, 16, bellColor, "通知").Clicked() {
					if a.notes != nil {
						a.notes = nil
					} else {
						a.openNotes()
					}
				}
				if a.unread > 0 {
					ui.Box(c).Absolute().Top(3).Right(2).MinWidth(15).Height(15).PaddingX(4).Radius(8).
						Background(pal.accent).Center().
						HitTest(func(float32, float32, ui.Rect) bool { return false }).
						Children(func() {
							ui.Text(c, fmt.Sprint(min(a.unread, 99))).FontSize(9.5).FontWeight(700).TextColor(white)
						})
				}
			})
		}

		// Account pill or Login button
		switch {
		case a.signedIn || a.me.Name != "":
			mine := a.mine[a.mineTab]
			b := ui.ButtonBase(c.Key("orbit-account")).Height(32).Padding(0, 11, 0, 5).Gap(7).Radius(16).
				AlignItems(ui.Center).FocusRing(false).Label("我的")
			b.Background(onOff(b.Hovered(), op.pillHoverBg, op.pillBg)).Border(1, op.pillBorder).Children(func() {
				a.avatar(c, a.me.Avatar, a.me.Name, 22)
				ui.Text(c, a.me.Name).FontSize(12.5).FontWeight(500).TextColor(op.text).SingleLine().MaxWidth(100)
			})
			if b.Clicked() && mine != nil {
				a.showMine(a.mineTab)
			}
		case a.c != nil:
			b := ui.ButtonBase(c.Key("orbit-login")).Height(32).PaddingX(12).Gap(6).Radius(16).
				AlignItems(ui.Center).FocusRing(false).Label("登录")
			b.Background(onOff(b.Hovered(), op.pillHoverBg, op.pillBg)).Border(1, op.pillBorder).Children(func() {
				ui.Icon(c, iconUser).Size(14, 14).TextColor(op.text)
				ui.Text(c, "登录").FontSize(12.5).FontWeight(500).TextColor(op.text)
			})
			if b.Clicked() {
				a.openLogin()
			}
		}

		if iconButton(c.Key("orbit-settings"), iconSettings, 16, onOff(a.settingsOpen, op.text, op.muted), "设置").Clicked() {
			a.settingsOpen = true
		}
	})
}

func (a *app) orbitDock(c *ui.Context, h float32, op orbitPalette) {
	type dockItem struct {
		key   string
		icon  *ui.SVG
		label string
		page  *page
		on    bool
		div   bool
	}
	items := []dockItem{
		{key: "home", icon: iconOrbit, label: "引力场", page: a.home, on: true},
	}
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

	const (
		dockPadX  = float32(7)
		dockItemH = float32(42)
	)

	estH := float32(16)
	for _, it := range items {
		if it.div {
			estH += 9
		} else {
			estH += dockItemH + 5
		}
	}
	dockTop := max(float32(66), (h-estH)/2)

	dock := ui.Column(c.Key("orbit-dock")).Absolute().Left(dockLeft).Top(dockTop).
		Padding(8, dockPadX).Gap(5).Radius(22).Clip().AlignItems(ui.Stretch).
		Background(op.dockTint).
		Border(1, op.dockBorder).
		InsetShadow(0, 1, 0, 0, op.dockInset).
		Shadow(0, 16, 38, -8, op.dockShadow).ZIndex(35)

	expanded := dock.Hovered()
	progress := dock.AnimateTo("expand", onOff(expanded, float32(1), float32(0)), ui.Snappy)
	if progress < 0.005 {
		progress = 0
	} else if progress > 0.995 {
		progress = 1
	}
	dockW := dockCollapsedW + (dockExpandedW-dockCollapsedW)*progress
	labelAlpha := min(max((progress-0.2)/0.8, 0), 1)
	dock.Width(dockW)

	dock.Children(func() {
		for _, it := range items {
			if it.div {
				ui.Box(c.Key(it.key)).Height(1).Margin(2, 10).Background(op.divider)
				continue
			}
			b := ui.ButtonBase(c.Key("dock:" + it.key)).Row().Height(dockItemH).Padding(0, 11, 0, 11).
				Gap(10).Radius(14).AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(it.label)
			bg := ui.Transparent
			color := op.muted
			switch {
			case it.on:
				bg, color = op.dockActiveBg, op.text
			case b.Hovered():
				bg, color = op.dockHoverBg, op.text
			}
			b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 140 * time.Millisecond, Oklab: true}))

			// Subtle tactile scale on icon hover / press.
			var iconScale float32 = 1.0
			switch {
			case b.Pressed():
				iconScale = 0.90
			case b.Hovered():
				iconScale = 1.10
			}

			b.Children(func() {
				ui.Icon(c, it.icon).Size(22, 22).TextColor(color).
					AnimatePose(ui.Pose{Scale: iconScale}, ui.Snappy)
				if labelAlpha > 0.01 {
					ui.Text(c, it.label).FontSize(13.5).FontWeight(onOff(it.on, 600, 500)).
						TextColor(color).SingleLine().Opacity(labelAlpha).
						AnimatePose(ui.Pose{X: (1 - labelAlpha) * -6}, ui.Snappy)
				}
			})
			if b.Clicked() && it.page != nil {
				if it.page == a.home {
					a.cycleOrbit()
				} else {
					a.root(it.page)
				}
			}
		}
	})
}

func (a *app) orbitCenterHub(c *ui.Context, w, h float32, op orbitPalette) {
	hx, hy, hw, _ := orbitHubRect(w, h)
	q := strings.TrimSpace(a.query)
	srcPage := a.orbitSourcePage(a.orbit.source)
	spec := a.orbitSpec(a.orbit.source)

	ui.Column(c.Key("orbit-hub")).Absolute().Left(hx).Top(hy).Width(hw).AlignItems(ui.Center).ZIndex(25).Children(func() {
		// Minimal Liquid Glass Search Capsule (matches light or dark appearance seamlessly).
		focused := c.Focused(a.searchBox)
		borderColor := op.searchBorder
		tint := op.searchTint
		if focused {
			borderColor = op.focusBorder
			tint = op.searchFocus
		}
		field := ui.Row(c.Key("orbit-search-bar")).Width(hw).Height(52).Radius(20).Padding(0, 10, 0, 18).Gap(8).
			AlignItems(ui.Center).
			Background(tint).
			Border(1, borderColor).
			InsetShadow(0, 1, 0, 0, op.searchInset)
		if op.dark {
			// Pitch-black singularity capsule with a subtle cosmic event-horizon halo.
			field.Shadow(0, 0, 64, 4, op.searchShadow)
		} else {
			field.Shadow(0, 14, 38, -6, op.searchShadow)
		}
		field.Children(func() {
			ui.Icon(c, iconSearch).Size(16, 16).TextColor(onOff(focused, op.text, op.muted))
			in := ui.TextInputBase(c.Key("orbit-search"), &a.query).Bind(&a.searchBox).Grow(1).
				Placeholder("搜索…").FontSize(15).Label("搜索")
			if in.Submitted() {
				a.orbit.hotOpen = false
				a.search(a.query)
			} else if in.Changed() {
				a.suggest()
			}
			if a.query != "" {
				if iconButton(c.Key("orbit-clear"), iconClose, 13, op.muted, "清空搜索").Clicked() {
					a.query = ""
				}
			} else {
				if srcPage != nil && len(srcPage.list) > 0 && !a.orbit.hidden {
					if iconButton(c.Key("orbit-cycle"), iconRefresh, 14, op.faint, "换一批").Clicked() {
						a.cycleOrbit()
					}
				}
				eyeIcon := onOff(a.orbit.hidden, iconEye, iconEyeOff)
				eyeLabel := onOff(a.orbit.hidden, "显示周围画面", "收起周围画面")
				if iconButton(c.Key("orbit-hide"), eyeIcon, 15, onOff(a.orbit.hidden, op.text, op.faint), eyeLabel).Clicked() {
					a.orbit.hidden = !a.orbit.hidden
				}
				if len(a.hot) > 0 {
					if iconButton(c.Key("orbit-hot-btn"), iconTrend, 15, onOff(a.orbit.hotOpen, op.text, op.faint), "热搜潜望").Clicked() {
						a.orbit.hotOpen = !a.orbit.hotOpen
					}
				}
			}
		})

		// Ultra-quiet 4-word source switcher matched to current theme.
		ui.Row(c.Key("orbit-sources")).Gap(6).Margin(14, 0, 0, 0).Justify(ui.Center).Children(func() {
			for _, s := range orbitSources {
				on := a.orbit.source == s.id
				b := ui.ButtonBase(c.Key("src:" + s.label)).Height(27).PaddingX(12).Radius(14).
					Center().FocusRing(false).Label(s.label)
				bg := ui.Transparent
				tc := op.muted
				switch {
				case on:
					bg, tc = op.tabActiveBg, op.text
					b.Border(1, op.tabActiveBd)
				case b.Hovered():
					bg, tc = op.tabHoverBg, op.text
				}
				b.Background(bg).Children(func() {
					ui.Text(c, s.label).FontSize(12.5).FontWeight(onOff(on, 600, 500)).TextColor(tc).SingleLine()
				})
				if b.Clicked() {
					a.setOrbitSource(s.id)
				}
			}
		})

		// Subtle login action only when user picks a signed-in source while logged out.
		if srcPage == nil && spec.needSignIn && a.c != nil {
			b := ui.ButtonBase(c.Key("orbit-signin-cta")).Margin(12, 0, 0, 0).Height(30).PaddingX(14).Radius(15).
				Center().Background(op.pillBg).Border(1, op.pillBorder).
				FocusRing(false).Label("扫码登录查看")
			b.Children(func() {
				ui.Text(c, "登录查看"+spec.label).FontSize(12).FontWeight(500).TextColor(op.text)
			})
			if b.Clicked() {
				a.openLogin()
			}
		}

		// Centered live suggestions (when typing) or opt-in hot search popover.
		a.orbitPopover(c, hw, q, focused, op)
	})
}

func (a *app) orbitPopover(c *ui.Context, hw float32, q string, focused bool, op orbitPalette) {
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

	popW := min(hw, float32(400))
	ui.Column(c.Key("orbit-popover")).Width(popW).Margin(12, 0, 0, 0).Padding(8, 6).Radius(14).
		Background(op.popoverBg).Border(1, op.popoverBd).
		Shadow(0, 20, 48, -8, op.searchShadow).Children(func() {
		if isHot {
			ui.Row(c).Padding(2, 10, 6, 10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "抖音热搜").FontSize(11).FontWeight(600).TextColor(op.faint).Grow(1)
				closeBtn := ui.ButtonBase(c.Key("hot-close")).FocusRing(false).Label("收起热搜")
				closeBtn.Children(func() {
					ui.Text(c, "收起").FontSize(11).TextColor(onOff(closeBtn.Hovered(), op.text, op.muted))
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
				row.Background(op.popoverHover)
			}
			row.Children(func() {
				if !isHot {
					ui.Icon(c, iconSearch).Size(12, 12).TextColor(op.faint)
					ui.Text(c, word).FontSize(13).TextColor(op.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
					return
				}
				rank, col := fmt.Sprint(i+1), op.faint
				if i == 0 {
					col = pal.accent
				} else if i < 3 {
					col = ui.Hex("#ff9f43")
				}
				ui.Text(c, rank).FontSize(12).FontWeight(700).TextColor(col).Width(16)
				ui.Text(c, word).FontSize(13).TextColor(op.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if v := a.hot[i].Value; v > 0 {
					ui.Text(c, count(v)).FontSize(11).TextColor(op.faint)
				}
			})
		}
	})
}

func (a *app) orbitCards(c *ui.Context, w, h float32, op orbitPalette) {
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
	nextHover := -1

	for i, r := range rects {
		if i >= n {
			break
		}
		slotIdx := (start + i) % n
		s := src.list[slotIdx]
		v := s.v

		matched := q == "" ||
			strings.Contains(strings.ToLower(v.Desc), q) ||
			strings.Contains(strings.ToLower(v.Author), q)

		key := fmt.Sprintf("orbit-card-%d-%s", i, v.ID)
		b := ui.ButtonBase(c.Key(key)).Absolute().Left(r.x).Top(r.y).Size(r.w, r.h).
			Radius(16).Clip().Background(orbitCardBg).FocusRing(false).Label(v.Desc)

		hovered := b.Hovered()
		if hovered {
			nextHover = i
		}

		// Border, depth, hover lift, and sibling dimming.
		bd := op.cardBorder
		z := 10 + i
		var scale float32 = 1.0
		var opAlpha float32 = 1.0
		switch {
		case q != "" && !matched:
			opAlpha = 0.16
			scale = 0.96
		case hovered:
			bd = op.cardHoverBd
			z = 24
			scale = 1.045
		case q != "" && matched:
			bd = ui.RGBA(254, 44, 85, 0.55)
			z = 18
		case a.orbit.hovered >= 0 && a.orbit.hovered != i:
			opAlpha = 0.52
		}

		b.Border(1, bd).ZIndex(z).Opacity(opAlpha).
			Shadow(0, 18, 40, -10, op.cardShadow).
			AnimatePose(ui.Pose{Scale: scale}, ui.Snappy)

		b.Children(func() {
			if bmp := a.img.get(v.Cover); bmp != nil {
				ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover)
			}

			// Top-left duration or live badge
			if v.Live != nil {
				liveBadge(c)
			} else if v.Duration > 0 {
				ui.Row(c).Absolute().Top(9).Left(9).Height(20).PaddingX(7).Radius(6).
					AlignItems(ui.Center).Background(ui.RGBA(0, 0, 0, 0.52)).Children(func() {
					ui.Text(c, clock(v.Duration)).FontSize(10.5).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.92))
				})
			}

			// Top-right play resonance dot on hover
			if hovered {
				ui.Box(c).Absolute().Top(9).Right(9).Size(26, 26).Radius(13).
					Background(pal.accent).Center().Children(func() {
					ui.Icon(c, iconPlay).Size(10, 10).TextColor(white)
				})
			}

			// Bottom scrim gradient + crisp 2-line title & byline
			ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).
				Padding(34, 11, 10, 11).Gap(3).
				Gradient(ui.RGBA(8, 10, 16, 0), ui.RGBA(8, 10, 16, 0.88), 180).
				Children(func() {
					title := strings.TrimSpace(v.Desc)
					if title == "" {
						title = "@" + v.Author
					}
					ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.95)).MaxLines(2)
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "@"+v.Author).FontSize(10.5).TextColor(ui.RGBA(255, 255, 255, 0.72)).SingleLine().Grow(1).Shrink(1).MinWidth(0)
						if when := ago(v.Created, time.Now()); when != "" {
							ui.Text(c, when).FontSize(10.5).TextColor(ui.RGBA(255, 255, 255, 0.56)).SingleLine()
						}
					})
				})
		})

		if b.Clicked() {
			a.playFromOrbit(src, slotIdx)
		}
	}
	a.orbit.hovered = nextHover
}
