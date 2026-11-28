package app

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/hg"
)

// appMode selects which top-level product surface is active in the window.
type appMode uint8

// modeTikTok shows TikTok on MyDouyin's short-video surface: the same pages
// and player, with TikTok's state swapped in (swapVideoState).
const (
	modeDouyin appMode = iota
	modeHongguo
	modeTikTok
)

func (m appMode) String() string {
	switch m {
	case modeHongguo:
		return "hongguo"
	case modeTikTok:
		return "tiktok"
	}
	return "douyin"
}

func parseAppMode(s string) appMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "hongguo":
		return modeHongguo
	case "tiktok":
		return modeTikTok
	}
	return modeDouyin
}

func (a *app) windowTitle() string {
	switch a.mode {
	case modeHongguo:
		return "MyHongguo"
	case modeTikTok:
		return "MyTikTok"
	}
	return "MyDouyin"
}

func (a *app) brandLogo() *ui.Bitmap {
	switch a.mode {
	case modeHongguo:
		return hongguoLogo
	case modeTikTok:
		return tiktokLogo
	}
	return logo
}

// hgDir returns the persistent directory for Hongguo profile and history files,
// or "" when running without a settings file (such as in tests).
func (a *app) hgDir() string {
	if a.settingsFile == "" {
		return ""
	}
	return filepath.Dir(a.settingsFile)
}

func (a *app) hgConfig() hg.Config {
	return hg.Config{
		ProfileDir:  a.hgDir(),
		SignerURL:   strings.TrimSpace(a.hgSignerURL),
		SignerToken: strings.TrimSpace(a.hgSignerToken),
		Direct:      a.hgDirect,
	}
}

// ensureHG lazily creates the MyHongguo service and history store on first entry
// into MyHongguo mode. Staying in MyDouyin mode never calls this.
func (a *app) ensureHG() *hgApp {
	if a.hg != nil {
		return a.hg
	}
	cfg := a.hgConfig()
	h := newHGApp(cfg)
	newSvc := a.newHGService
	if newSvc == nil {
		newSvc = func(c hg.Config) (hg.Service, error) {
			return hg.New(c)
		}
	}
	svc, err := newSvc(cfg)
	if err != nil {
		h.err = err.Error()
	} else {
		h.svc = svc
		h.initImages(a)
	}
	histPath := ""
	if dir := a.hgDir(); dir != "" {
		histPath = filepath.Join(dir, "hongguo-history.json")
	}
	hist, herr := hg.NewHistoryStore(histPath)
	if herr != nil {
		if h.err == "" {
			h.err = herr.Error()
		}
		hist, _ = hg.NewHistoryStore("")
	}
	h.hist = hist
	for _, ch := range hgDiscoverChannels {
		a.loadHGDiscoverCache(h, ch)
	}
	a.hgPrefetchOrbitCovers(h)
	a.hgPrefetchDiscoverCovers(h, hgDiscoverChannels[0])
	a.hg = h
	return h
}

// reconfigureHG applies updated Hongguo settings when the service is already
// initialized, leaving pure Douyin sessions untouched.
func (a *app) reconfigureHG() {
	if a.hg == nil {
		return
	}
	cfg := a.hgConfig()
	if a.hg.svc != nil {
		if err := a.hg.svc.Reconfigure(cfg); err != nil {
			a.hg.err = err.Error()
			a.toast = "红果配置应用失败：" + err.Error()
			return
		}
		a.hg.cfg = cfg
		a.hg.err = ""
		a.hg.initImages(a)
		a.hgRetryIfErrored()
		return
	}
	newSvc := a.newHGService
	if newSvc == nil {
		newSvc = func(c hg.Config) (hg.Service, error) {
			return hg.New(c)
		}
	}
	svc, err := newSvc(cfg)
	if err != nil {
		a.hg.err = err.Error()
		a.toast = "红果配置应用失败：" + err.Error()
		return
	}
	a.hg.svc, a.hg.cfg, a.hg.err = svc, cfg, ""
	a.hg.initImages(a)
	a.hgRetryIfErrored()
}

// resetHGDevice resets the anonymous Hongguo device identity asynchronously.
func (a *app) resetHGDevice() {
	if a.hgResetBusy {
		return
	}
	h := a.ensureHG()
	if h.svc == nil {
		a.toast = "红果服务未就绪：" + h.err
		return
	}
	a.hgResetBusy = true
	svc := h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := svc.ResetDevice(ctx)
		cancel()
		a.post(func() {
			a.hgResetBusy = false
			if err != nil {
				a.toast = "重置匿名设备失败：" + err.Error()
				return
			}
			a.toast = "已重置红果匿名设备"
			a.hgRetryIfErrored()
		})
	}()
}

// hgSuspend checkpoints active Hongguo episode progress and releases media
// playback when leaving MyHongguo mode, while keeping hgPagePlayer on the stack
// so returning to MyHongguo resumes at the saved position.
func (a *app) hgSuspend() {
	if a.hg != nil {
		a.hg.closeStackPrefetch()
		if a.hg.player != nil {
			hp := a.hg.player
			a.hgCheckpoint(true)
			hp.releasePlayer()
			hp.closePrefetch()
		}
	}
	a.mainVideo.hide()
	a.mainVideo.player = nil
}

// hgClose cleans up Hongguo resources on window close.
func (a *app) hgClose() {
	a.hgSuspend()
	a.hg = nil
}

// switchMode transitions cleanly between MyDouyin, MyHongguo and MyTikTok,
// releasing active media from the outgoing mode and lazily initializing the
// target mode.
func (a *app) switchMode(next appMode) {
	a.brandMenuOpen = false
	if next == a.mode {
		return
	}
	if next == modeTikTok && a.ensureTikTokClient() == nil {
		return
	}
	a.sheet, a.notes, a.dms = nil, nil, nil
	a.sugSeq++
	a.ttWeb.layout = false
	a.shareOpen, a.moreOpen, a.qualityOpen, a.speedOpen, a.volumeOpen, a.dmSettingsOpen = false, false, false, false, false, false
	a.orbit.hotOpen = false
	a.group.open = false
	a.picViewer.open = false
	if a.liveChat != nil {
		a.liveChat.gifts.open = false
	}
	if a.theater || a.fullScreen() {
		a.setFullScreen(false)
	}

	if a.mode != modeHongguo {
		if len(a.pages) > 0 {
			a.cur().paused = a.paused
		}
		a.setPip(false)
		a.preload.close()
		a.closeLiveRoom()
		if a.held != nil {
			a.release(a.held)
		}
		for _, p := range a.pages {
			for _, s := range p.list {
				a.release(s)
			}
		}
		a.mainVideo.hide()
		a.mainVideo.player = nil
	} else {
		a.hgSuspend()
	}

	if (a.mode == modeTikTok) != (next == modeTikTok) {
		a.swapVideoState(next == modeTikTok)
	}
	a.mode = next
	if a.win != nil {
		a.win.SetTitle(a.windowTitle())
	}
	switch next {
	case modeHongguo:
		a.ensureHG()
		a.hgEnsureActiveLoaded()
	case modeTikTok:
		if a.win != nil && (!a.signedIn || a.me.ID == "") {
			a.startTikTokLogin()
		}
	default:
		a.startDouyinServices()
	}
	if next != modeHongguo && len(a.pages) > 0 {
		a.paused = a.cur().paused
		a.fill()
	}
	a.saveSettings()
	a.redraw()
}

func brandBarLeftFor(goos string) float32 {
	if goos == "windows" {
		return 20
	}
	return contentLeft + 8
}

func brandBarLeft() float32 {
	return brandBarLeftFor(runtime.GOOS)
}

func (a *app) brandMenuLeft() float32 {
	return brandBarLeft()
}

// brandSwitcher renders the unified top-left brand pill button that opens the
// mode-switching popover menu. Using ui.ButtonBase ensures clicks are handled
// instead of being swallowed by a parent DragWindow region.
func (a *app) brandSwitcher(c *ui.Context) {
	b := ui.ButtonBase(c.Key("brand-switcher")).Row().Height(34).Padding(0, 10, 0, 6).Gap(7).Radius(10).
		AlignItems(ui.Center).FocusRing(false).Label("切换模式")
	switch {
	case a.brandMenuOpen:
		b.Background(pal.press).Border(1, pal.hairline)
	case b.Hovered():
		b.Background(pal.hover)
	}
	b.Children(func() {
		ui.Image(c, a.brandLogo()).Size(22, 22)
		ui.Text(c, a.windowTitle()).FontSize(16).FontWeight(700).TextColor(pal.text).SingleLine()
		ui.Icon(c, iconChevronDown).Size(13, 13).TextColor(onOff(a.brandMenuOpen || b.Hovered(), pal.text, pal.muted))
	})
	if b.Clicked() {
		a.brandMenuOpen = !a.brandMenuOpen
	}
}

// brandMenu renders the floating glass mode-switcher popover and an outside
// click-catcher backdrop while a.brandMenuOpen is true.
func (a *app) brandMenu(c *ui.Context, x float32) {
	if !a.brandMenuOpen {
		return
	}
	ui.Box(c.Key("brand-menu-backdrop")).Absolute().Top(0).Left(0).Right(0).Bottom(0).ZIndex(70).
		HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind == ui.InputPointerDown {
				a.brandMenuOpen = false
				return true
			}
			return false
		})

	type modeItem struct {
		mode     appMode
		icon     *ui.Bitmap
		title    string
		subtitle string
		hint     string
	}
	items := []modeItem{
		{
			mode:     modeDouyin,
			icon:     logo,
			title:    "MyDouyin",
			subtitle: "刷抖音 · 直播 · 精选",
			hint:     onOff(runtime.GOOS == "darwin", "⌘1", "Ctrl+1"),
		},
		{
			mode:     modeHongguo,
			icon:     hongguoLogo,
			title:    "MyHongguo",
			subtitle: "免费短剧 · 漫剧 · 榜单",
			hint:     onOff(runtime.GOOS == "darwin", "⌘2", "Ctrl+2"),
		},
		{
			mode:     modeTikTok,
			icon:     tiktokLogo,
			title:    "MyTikTok",
			subtitle: "For You · 字幕翻译 · 直播",
			hint:     onOff(runtime.GOOS == "darwin", "⌘3", "Ctrl+3"),
		},
	}

	ui.Column(c.Key("brand-menu")).Absolute().Top(50).Left(x).Width(272).Padding(6).Gap(4).Radius(14).
		Background(pal.popover).Border(1, pal.popoverBd).Shadow(0, 14, 34, -6, pal.cardShadow).ZIndex(75).Children(func() {
		for _, it := range items {
			active := a.mode == it.mode
			b := ui.ButtonBase(c.Key("mode:" + it.mode.String())).Row().Padding(9, 10).Gap(10).Radius(10).
				AlignItems(ui.Center).FocusRing(false).Label("切换到 " + it.title)
			switch {
			case b.Hovered():
				b.Background(pal.hover)
			case active:
				b.Background(pal.press)
			}
			b.Children(func() {
				ui.Image(c, it.icon).Size(28, 28)
				ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					ui.Text(c, it.title).FontSize(13.5).FontWeight(onOff(active, 700, 600)).
						TextColor(onOff(active, pal.strong, pal.text)).SingleLine()
					ui.Text(c, it.subtitle).FontSize(11.5).TextColor(pal.muted).SingleLine()
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, it.hint).FontSize(11).TextColor(pal.faint).SingleLine()
					ui.Box(c).Size(14, 14).Center().Children(func() {
						if active {
							ui.Icon(c, iconCheck).Size(14, 14).TextColor(pal.accent)
						}
					})
				})
			})
			if b.Clicked() {
				a.switchMode(it.mode)
			}
		}
	})
}
