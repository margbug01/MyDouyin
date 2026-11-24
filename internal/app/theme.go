package app

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// palette is one appearance's colors, for the whole window: its surfaces and
// ink, and the glass of 引力场's search field, the dock, pills and cards.
type palette struct {
	dark                bool
	bgHex               string // the window's, shown as it resizes
	bg, raised, popover ui.Color
	well                ui.Color // where a cover shows once loaded
	text, muted, faint  ui.Color
	strong              ui.Color // ink marking what is chosen
	hairline            ui.Color
	hover, press        ui.Color
	accent              ui.Color
	veil                []ui.GradientStop // 引力场's backdrop

	searchTint, searchFocus, searchBorder, focusBorder, searchInset, searchShadow ui.Color
	dockTint, dockBorder, dockInset, dockShadow, dockActiveBg, dockHoverBg        ui.Color
	pillBg, pillHoverBg, pillBorder                                               ui.Color
	tabActiveBg, tabActiveBd, tabHoverBg                                          ui.Color
	cardBorder, cardHoverBd, cardShadow                                           ui.Color
	popoverBd, popoverHover                                                       ui.Color
}

var (
	lightPal = palette{
		bgHex:    "#f5f7fb",
		bg:       ui.Hex("#f5f7fb"),
		raised:   ui.Hex("#ffffff"),
		popover:  ui.Hex("#ffffff"),
		well:     ui.Hex("#e4e7ee"),
		text:     ui.RGBA(17, 20, 30, 0.92),
		muted:    ui.RGBA(17, 20, 30, 0.56),
		faint:    ui.RGBA(17, 20, 30, 0.38),
		strong:   ui.Hex("#11141e"),
		hairline: ui.RGBA(17, 20, 30, 0.08),
		hover:    ui.RGBA(15, 23, 42, 0.05),
		press:    ui.RGBA(15, 23, 42, 0.08),
		accent:   ui.Hex("#fe2c55"),
		// Porcelain: brightest at the center, cooling toward the edges.
		veil: []ui.GradientStop{
			{Color: ui.Hex("#ffffff"), At: 0},
			{Color: ui.Hex("#fafbfe"), At: 0.34},
			{Color: ui.Hex("#f2f4f9"), At: 0.72},
			{Color: ui.Hex("#eaedf4"), At: 1},
		},
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
		popoverBd:    ui.RGBA(15, 23, 42, 0.10),
		popoverHover: ui.RGBA(15, 23, 42, 0.05),
	}

	darkPal = palette{
		dark:     true,
		bgHex:    "#000000",
		bg:       ui.Hex("#000000"),
		raised:   ui.Hex("#15161c"),
		popover:  ui.Hex("#1b1c23"),
		well:     ui.Hex("#15161c"),
		text:     ui.RGBA(255, 255, 255, 0.94),
		muted:    ui.RGBA(255, 255, 255, 0.56),
		faint:    ui.RGBA(255, 255, 255, 0.34),
		strong:   white,
		hairline: ui.RGBA(255, 255, 255, 0.08),
		hover:    ui.RGBA(255, 255, 255, 0.06),
		press:    ui.RGBA(255, 255, 255, 0.1),
		accent:   ui.Hex("#fe2c55"),
		// Black, with a faint sheen ringing the center.
		veil: []ui.GradientStop{
			{Color: ui.Hex("#000000"), At: 0},
			{Color: ui.Hex("#000000"), At: 0.24},
			{Color: ui.RGBA(130, 152, 210, 0.075), At: 0.38},
			{Color: ui.RGBA(40, 48, 76, 0.045), At: 0.56},
			{Color: ui.Hex("#000000"), At: 0.86},
			{Color: ui.Hex("#000000"), At: 1},
		},
		searchTint:   ui.Hex("#08090d"),
		searchFocus:  ui.Hex("#0c0d13"),
		searchBorder: ui.RGBA(255, 255, 255, 0.16),
		focusBorder:  ui.RGBA(255, 255, 255, 0.40),
		searchInset:  ui.RGBA(255, 255, 255, 0.10),
		searchShadow: ui.RGBA(135, 165, 240, 0.14),
		dockTint:     ui.RGBA(18, 19, 25, 0.92),
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
		cardBorder:   ui.RGBA(255, 255, 255, 0.10),
		cardHoverBd:  ui.RGBA(255, 255, 255, 0.36),
		cardShadow:   ui.RGBA(0, 0, 0, 0.70),
		popoverBd:    ui.RGBA(255, 255, 255, 0.12),
		popoverHover: ui.RGBA(255, 255, 255, 0.07),
	}
)

// orbitIsDark reports whether the window is dark: as chosen, or as the system is.
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

// applyThemeSource has the native parts (menus, scroll bars) follow the choice.
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
	a.orbit.theme = onOff(a.orbitIsDark(), "light", "dark")
	a.applyThemeSource()
}
