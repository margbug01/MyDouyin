package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/hg"
)

// hgView renders the full MyHongguo experience: left floating glass dock,
// top bar with brand switcher / back button + search pill, shared top-right
// controls, and the active root or pushed sub-page.
func (a *app) hgView(c *ui.Context) {
	h := a.ensureHG()
	if h.img != nil {
		defer h.img.sweep()
	}
	a.hgEnsureActiveLoaded()

	_, winH := c.Size()
	ui.Box(c.Key("hg-root")).Fill().Children(func() {
		a.mainVideo.background(c, pal.bg)
		if a.theater && a.hgPlaying() {
			ui.Row(c.Key("hg-theater-row")).Fill().AlignItems(ui.Stretch).Children(func() {
				a.hgPlayerView(c, h, h.top())
			})
			return
		}
		a.hgDock(c, h, winH)
		a.topRight(c)
		if h.root == hgRootOrbit && h.top() == nil && !(h.svc == nil && h.err != "") {
			a.hgOrbitView(c, h)
			return
		}
		ui.Row(c.Key("hg-main-row")).Fill().AlignItems(ui.Stretch).Children(func() {
			ui.Column(c.Key("hg-main")).Grow(1).Children(func() {
				a.hgTopBar(c, h)
				ui.Column(c.Key("hg-main-body")).Grow(1).PaddingStart(contentLeft).Children(func() {
					if h.svc == nil && h.err != "" {
						a.hgInitErrorView(c, h)
						return
					}
					if p := h.top(); p != nil {
						switch p.kind {
						case hgPageSearch:
							a.hgSearchView(c, h, p)
						case hgPageDetail:
							a.hgDetailView(c, h, p)
						case hgPagePerson:
							a.hgPersonView(c, h, p)
						case hgPagePlayer:
							a.hgPlayerView(c, h, p)
						}
						return
					}
					switch h.root {
					case hgRootDiscover:
						a.hgDiscoverView(c, h)
					case hgRootRank:
						a.hgRankView(c, h)
					case hgRootFilter:
						a.hgFilterView(c, h)
					case hgRootNew:
						a.hgNewView(c, h)
					case hgRootTrack:
						a.hgTrackView(c, h)
					}
				})
			})
		})
	})
}

func (a *app) hgInitErrorView(c *ui.Context, h *hgApp) {
	ui.Column(c.Key("hg-init-err")).Grow(1).Center().Padding(24).Children(func() {
		a.hgErrorBanner(c, "hg-init", hg.ErrorInfo{
			Kind:            hg.ErrKindOther,
			Message:         "红果服务初始化失败：" + h.err,
			Retryable:       true,
			CanOpenSettings: true,
		}, func() {
			a.reconfigureHG()
			a.hgEnsureActiveLoaded()
		})
	})
}

// hgDock mirrors MyDouyin's floating glass left dock with 5 Hongguo roots.
func (a *app) hgDock(c *ui.Context, h *hgApp, winH float32) {
	type hgDockItem struct {
		key   string
		icon  *ui.SVG
		label string
		root  hgRootKind
	}
	items := []hgDockItem{
		{key: "orbit", icon: iconOrbit, label: "引力场", root: hgRootOrbit},
		{key: "discover", icon: iconCompass, label: "发现", root: hgRootDiscover},
		{key: "rank", icon: iconTrend, label: "榜单", root: hgRootRank},
		{key: "filter", icon: iconGrid, label: "分类", root: hgRootFilter},
		{key: "new", icon: iconSpark, label: "上新", root: hgRootNew},
		{key: "track", icon: iconDrama, label: "追剧", root: hgRootTrack},
	}

	const dockItemH = float32(42)
	estH := float32(16) + float32(len(items))*(dockItemH+5)
	dock := ui.Column(c.Key("hg-dock")).Absolute().Left(dockLeft).Top(max(66, (winH-estH)/2)).
		Padding(8, 7).Gap(5).Radius(22).Clip().AlignItems(ui.Stretch).
		Background(pal.dockTint).Border(1, pal.dockBorder).
		InsetShadow(0, 1, 0, 0, pal.dockInset).Shadow(0, 16, 38, -8, pal.dockShadow).ZIndex(35)
	progress := dock.AnimateTo("expand", onOff(dock.Hovered(), float32(1), 0), ui.Snappy)
	progress = min(max(progress, 0), 1)
	labelAlpha := min(max((progress-0.2)/0.8, 0), 1)
	dock.Width(dockCollapsedW + (dockExpandedW-dockCollapsedW)*progress)

	dock.Children(func() {
		for _, it := range items {
			on := h.root == it.root
			b := ui.ButtonBase(c.Key("hg-dock:" + it.key)).Row().Height(dockItemH).PaddingX(11).
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
			if b.Clicked() {
				a.hgSelectRoot(it.root)
			}
		}
	})
}

// hgTopBar matches MyDouyin's topBar geometry: brand switcher or back+title on
// the left, centered search pill in the middle, DragWindow on empty space.
func (a *app) hgTopBar(c *ui.Context, h *hgApp) {
	ui.Row(c.Key("hg-topbar")).Height(topBarHeight).Padding(0, 8, 0, brandBarLeft()).Gap(4).AlignItems(ui.Center).DragWindow().Children(func() {
		if top := h.top(); top != nil {
			if iconButton(c.Key("hg-back"), iconBack, 18, pal.muted, "返回").Clicked() {
				a.hgPop()
			}
			ui.Text(c, top.title).FontSize(15).FontWeight(600).TextColor(pal.text).SingleLine().Shrink(1).MinWidth(0).MaxWidth(220)
		} else {
			a.brandSwitcher(c)
		}

		left, fieldW := a.searchField(c)
		field := ui.Row(c.Key("hg-search-field")).Absolute().Top(10).Left(left).Width(fieldW).Height(36).Radius(12).PaddingStart(14).
			AlignItems(ui.Center).Background(pal.searchTint).Shadow(0, 6, 18, -8, pal.searchShadow)
		if c.Focused(h.searchBox) {
			field.Border(1, pal.focusBorder)
		} else {
			field.Border(1, pal.searchBorder)
		}
		field.Children(func() {
			in := ui.TextInputBase(c.Key("hg-search"), &h.query).Bind(&h.searchBox).Grow(1).
				Placeholder("搜索短剧、漫剧、演员…").FontSize(13.5).Label("搜索")
			if in.Submitted() {
				a.hgSearch(h.query)
			} else if in.Changed() {
				a.hgSuggest()
			}
			if h.query != "" {
				if iconButton(c.Key("hg-search-clear"), iconClose, 12, pal.muted, "清空搜索").Clicked() {
					h.query = ""
					h.sugs, h.sugFor = nil, ""
				}
			}
			ui.Box(c).Width(1).Height(16).Background(pal.hairline)
			b := ui.ButtonBase(c.Key("hg-go")).Height(34).PaddingX(14).Gap(6).AlignItems(ui.Center).FocusRing(false).Label("搜索按钮").Radius(0, 10, 10, 0)
			if b.Hovered() {
				b.Background(pal.hover)
			}
			b.Children(func() {
				ui.Icon(c, iconSearch).Size(15, 15).TextColor(pal.text)
				ui.Text(c, "搜索").FontSize(13.5).FontWeight(600).TextColor(pal.text)
			})
			if b.Clicked() {
				a.hgSearch(h.query)
			}
		})
	})
}

// hgSearchList renders the focused search dropdown: Suggest results while
// typing, or Hongguo HotSearch ("猜你想搜" chips + 短剧热搜榜 rows) when empty.
func (a *app) hgSearchList(c *ui.Context) {
	h := a.hg
	if h == nil {
		return
	}
	q := strings.TrimSpace(h.query)
	left, width := a.searchField(c)

	if q != "" {
		if q != h.sugFor || len(h.sugs) == 0 {
			return
		}
		words := h.sugs[:min(10, len(h.sugs))]
		ui.Column(c.Key("hg-sug-list")).Absolute().Top(52).Left(left).Width(width).Padding(8, 6).Radius(12).
			Background(pal.popover).Border(1, pal.popoverBd).Shadow(0, 10, 30, -6, pal.cardShadow).ZIndex(60).Children(func() {
			for _, w := range words {
				row := ui.Row(c.Key("sug:"+w)).Height(34).PaddingX(10).Gap(10).Radius(8).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(w)
				row.HandleInput(func(ev ui.InputEvent) bool {
					if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
						h.query = w
						a.hgSearch(w)
						return true
					}
					return false
				})
				if row.Hovered() {
					row.Background(pal.hover)
				}
				row.Children(func() {
					ui.Icon(c, iconSearch).Size(13, 13).TextColor(pal.faint)
					ui.Text(c, w).FontSize(13.5).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				})
			}
		})
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

	ui.Column(c.Key("hg-hot-list")).Absolute().Top(52).Left(left).Width(width).Padding(10, 8).Gap(6).Radius(12).
		Background(pal.popover).Border(1, pal.popoverBd).Shadow(0, 10, 30, -6, pal.cardShadow).ZIndex(60).Children(func() {
		if len(h.hot.Guesses) > 0 {
			ui.Text(c, "猜你想搜").FontSize(12).FontWeight(600).TextColor(pal.faint).Margin(0, 8, 2, 8)
			ui.Row(c.Key("hg-hot-guesses")).Wrap().Gap(6).Padding(0, 8, 4, 8).Children(func() {
				for _, g := range h.hot.Guesses[:min(8, len(h.hot.Guesses))] {
					chip := ui.Row(c.Key("guess:"+g)).Height(28).PaddingX(10).Radius(14).AlignItems(ui.Center).
						Background(pal.pillBg).Border(1, pal.pillBorder).Cursor(ui.CursorPointer).Label(g)
					if chip.Hovered() {
						chip.Background(pal.pillHoverBg)
					}
					chip.HandleInput(func(ev ui.InputEvent) bool {
						if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
							h.query = g
							a.hgSearch(g)
							return true
						}
						return false
					})
					chip.Children(func() {
						ui.Text(c, g).FontSize(12).TextColor(pal.text).SingleLine()
					})
				}
			})
		}
		if board != nil {
			title := hgFirstNonEmpty(board.Name, "短剧热搜榜")
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(pal.faint).Margin(2, 8, 2, 8)
			items := board.Items[:min(10, len(board.Items))]
			for i, it := range items {
				row := ui.Row(c.Key(fmt.Sprintf("hot:%d:%s", i, it.Word))).Height(32).PaddingX(8).Gap(10).Radius(8).
					AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(it.Word)
				if row.Hovered() {
					row.Background(pal.hover)
				}
				row.HandleInput(func(ev ui.InputEvent) bool {
					if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
						h.query = it.Word
						a.hgSearch(it.Word)
						return true
					}
					return false
				})
				row.Children(func() {
					color := pal.faint
					switch i {
					case 0:
						color = pal.accent
					case 1, 2:
						color = ui.Hex("#ff9f43")
					}
					ui.Text(c, fmt.Sprint(i+1)).FontSize(12.5).FontWeight(700).TextColor(color).Width(18)
					ui.Text(c, it.Word).FontSize(13).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
					if it.Score != "" {
						ui.Text(c, it.Score).FontSize(11.5).TextColor(pal.faint).SingleLine()
					}
				})
			}
		}
	})
}

func (a *app) hgUnderlineTab(c *ui.Context, key, label string, active bool) bool {
	b := ui.ButtonBase(c.Key(key)).Column().Gap(5).AlignItems(ui.Center).FocusRing(false).Label(label)
	b.Children(func() {
		color, weight := pal.muted, 500
		switch {
		case active:
			color, weight = pal.strong, 700
		case b.Hovered():
			color = pal.text
		}
		ui.Text(c, label).FontSize(15).FontWeight(weight).TextColor(color).SingleLine()
		bar := ui.Box(c).Width(18).Height(3).Radius(2)
		if active {
			bar.Background(pal.accent)
		}
	})
	return b.Clicked()
}

func (a *app) hgPill(c *ui.Context, key, label string, active bool) bool {
	b := ui.ButtonBase(c.Key(key)).Height(28).PaddingX(12).Radius(14).Center().FocusRing(false).Label(label)
	bg, tc := ui.Transparent, pal.muted
	switch {
	case active:
		bg, tc = pal.accent.Alpha(0.14), pal.accent
		b.Border(1, pal.accent.Alpha(0.4))
	case b.Hovered():
		bg, tc = pal.tabHoverBg, pal.text
		b.Border(1, pal.hairline)
	default:
		b.Border(1, pal.hairline)
	}
	b.Background(bg).Children(func() {
		ui.Text(c, label).FontSize(12.5).FontWeight(onOff(active, 600, 500)).TextColor(tc).SingleLine()
	})
	return b.Clicked()
}

func (a *app) hgErrorBanner(c *ui.Context, key string, info hg.ErrorInfo, onRetry func()) {
	if info.Kind == hg.ErrKindNone {
		return
	}
	ui.Row(c.Key("hg-err:"+key)).Padding(14, 16).Gap(12).Radius(12).AlignItems(ui.Center).
		Background(pal.raised).Border(1, pal.accent.Alpha(0.32)).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, info.Message).FontSize(13).TextColor(pal.text).MaxLines(2)
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("retry:"+key), "重试").Clicked() && onRetry != nil {
				onRetry()
			}
			if info.CanResetDevice {
				b := ui.Button(c.Key("reset:"+key), onOff(a.hgResetBusy, "重置中…", "重置匿名设备"))
				if a.hgResetBusy {
					b.Disabled(true)
				} else if b.Clicked() {
					a.resetHGDevice()
				}
			}
			if info.CanOpenSettings {
				if ui.Button(c.Key("settings:"+key), "红果设置").Clicked() {
					a.settingsOpen = true
				}
			}
		})
	})
}

func (a *app) hgDiscoverView(c *ui.Context, h *hgApp) {
	ui.Row(c.Key("hg-disc-tabs")).Height(42).PaddingX(24).Gap(24).AlignItems(ui.Center).Children(func() {
		for _, ch := range hgDiscoverChannels {
			if a.hgUnderlineTab(c, "disc:"+ch, ch, h.discChannel == ch) {
				a.hgSelectDiscoverChannel(ch)
			}
		}
	})
	l := h.discoverList(h.discChannel)
	a.hgSeriesGrid(c, h, "disc:"+h.discChannel, l, "暂无推荐内容", func() {
		a.hgLoadDiscover(h.discChannel, false)
	}, func() {
		a.hgLoadDiscover(h.discChannel, len(l.items) == 0)
	})
}

func (a *app) hgRankView(c *ui.Context, h *hgApp) {
	if len(h.rankContents) > 0 {
		ui.ScrollHorizontal(c.Key("hg-rank-contents")).Height(40+barGutter).Padding(0, 24, barGutter, 24).Gap(22).AlignItems(ui.Center).Children(func() {
			for _, rc := range h.rankContents {
				if a.hgUnderlineTab(c, "rc:"+rc.ID, rc.Name, rc.ID == h.rankContent) {
					a.hgSelectRankContent(rc)
				}
			}
		})
	}
	if boards := h.activeRankBoards(); len(boards) > 0 {
		ui.ScrollHorizontal(c.Key("hg-rank-boards")).Height(34+barGutter).Padding(0, 24, barGutter, 24).Gap(8).AlignItems(ui.Center).Children(func() {
			for _, rb := range boards {
				if a.hgPill(c, "rb:"+rb.ID, rb.Name, rb.ID == h.rankBoard) {
					a.hgSelectRankBoard(rb.ID)
				}
			}
		})
	}

	nItems := len(h.rankItems)
	totalRows := nItems + 1
	h.rankList.Key = func(row int) any {
		if row == nItems {
			return "foot"
		}
		if k := rankItemKey(h.rankItems[row]); k != "" {
			return k
		}
		return row
	}
	ui.List(c.Key("hg-rank-list:"+h.rankContent+":"+h.rankBoard), &h.rankList, totalRows, func(row int) {
		if row >= nItems-3 && h.rankHasMore && !h.rankLoading && h.rankErr.Kind == hg.ErrKindNone {
			a.hgLoadRank(false)
		}
		if row == nItems {
			ui.Column(c.Key("rank-foot")).Padding(16, 24, 32, 24).AlignItems(ui.Center).Children(func() {
				if h.rankErr.Kind != hg.ErrKindNone {
					a.hgErrorBanner(c, "rank", h.rankErr, func() {
						a.hgLoadRank(len(h.rankItems) == 0)
					})
					return
				}
				text := ""
				switch {
				case h.rankLoading:
					text = "载入中…"
				case h.rankLoaded && nItems == 0:
					text = "暂无榜单数据"
				case h.rankLoaded && !h.rankHasMore:
					text = "没有更多了"
				}
				if text != "" {
					ui.Text(c, text).FontSize(12.5).TextColor(pal.faint)
				}
			})
			return
		}
		it := h.rankItems[row]
		a.hgRankRow(c, h, row, it)
	}).Grow(1).Padding(4, 0, 0, 0)
}

func (a *app) hgRankRow(c *ui.Context, h *hgApp, idx int, it hg.RankItem) {
	isActor := it.UserID != "" && it.SeriesID == ""
	pos := it.Position
	if pos <= 0 {
		pos = idx + 1
	}
	title := hgFirstNonEmpty(it.Title, it.Actor)
	ui.Box(c).Padding(4, 24+barGutter, 4, 24).Children(func() {
		b := ui.ButtonBase(c.Key(fmt.Sprintf("rank-row:%d", idx))).Row().FillWidth().Padding(10, 14).Gap(14).Radius(12).
			AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(title)
		bg := pal.raised
		if b.Hovered() {
			bg = pal.hover
		}
		b.Background(bg).Border(1, onOff(b.Hovered(), pal.cardHoverBd, pal.hairline))
		b.Children(func() {
			rankColor := pal.muted
			switch pos {
			case 1:
				rankColor = pal.accent
			case 2, 3:
				rankColor = ui.Hex("#ff9f43")
			}
			ui.Box(c).Width(32).Center().Children(func() {
				ui.Text(c, fmt.Sprintf("#%d", pos)).FontSize(16).FontWeight(800).TextColor(rankColor).SingleLine()
			})
			if isActor {
				a.hgAvatar(c, h, hgFirstNonEmpty(it.AvatarURL, it.CoverURL), title, 52)
			} else {
				ui.Box(c).Size(54, 72).Radius(8).Clip().Background(pal.well).Children(func() {
					if bmp := h.img.getSized(it.CoverURL, thumbEdge); bmp != nil {
						ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover).Transition(coverFade)
					}
				})
			}
			ui.Column(c).Gap(5).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, title).FontSize(14.5).FontWeight(600).TextColor(onOff(b.Hovered(), pal.strong, pal.text)).SingleLine().Shrink(1).MinWidth(0)
					if isActor {
						ui.Row(c).Height(20).PaddingX(7).Radius(6).AlignItems(ui.Center).Background(pal.pillBg).Border(1, pal.pillBorder).Children(func() {
							ui.Text(c, "演员").FontSize(11).TextColor(pal.muted).SingleLine()
						})
					} else if it.EpisodeCount > 0 {
						ui.Text(c, fmt.Sprintf("全%d集", it.EpisodeCount)).FontSize(12).TextColor(pal.muted).SingleLine()
					}
				})
				if it.Actor != "" && !isActor {
					ui.Text(c, "主演："+it.Actor).FontSize(12).TextColor(pal.muted).SingleLine()
				}
				if it.Intro != "" {
					ui.Text(c, it.Intro).FontSize(12).TextColor(pal.faint).MaxLines(2)
				}
			})
			if it.Score != "" {
				ui.Row(c).Height(26).PaddingX(10).Radius(13).AlignItems(ui.Center).Background(pal.accent.Alpha(0.12)).Children(func() {
					ui.Text(c, it.Score).FontSize(12).FontWeight(600).TextColor(pal.accent).SingleLine()
				})
			}
		})
		if b.Clicked() {
			if isActor {
				a.hgOpenPerson(it.UserID, title, hgFirstNonEmpty(it.AvatarURL, it.CoverURL))
			} else if it.SeriesID != "" {
				a.hgOpenDetail(hg.Series{
					ID:           it.SeriesID,
					Title:        it.Title,
					CoverURL:     it.CoverURL,
					Score:        it.Score,
					Abstract:     it.Intro,
					EpisodeCount: it.EpisodeCount,
				})
			}
		}
	})
}

func (a *app) hgFilterView(c *ui.Context, h *hgApp) {
	if h.filterPanelErr.Kind != hg.ErrKindNone {
		ui.Column(c.Key("hg-filter-panel-err")).Padding(12, 24).Children(func() {
			a.hgErrorBanner(c, "filter-panel", h.filterPanelErr, func() {
				a.hgLoadFilterPanel(h.filterGenre)
			})
		})
	} else if len(h.filterRows) > 0 {
		ui.Column(c.Key("hg-filter-panel")).Padding(4, 24, 6, 24).Gap(6).Children(func() {
			for _, row := range h.filterRows {
				if len(row.Options) == 0 {
					continue
				}
				selectedVal := h.filterSelected[row.Key]
				ui.Row(c.Key("frow:"+row.Key)).Gap(10).AlignItems(ui.Center).Children(func() {
					if row.Name != "" {
						ui.Text(c, row.Name).FontSize(12.5).FontWeight(600).TextColor(pal.faint).Width(46).SingleLine()
					}
					ui.ScrollHorizontal(c.Key("fopts:"+row.Key)).Grow(1).Height(30+barGutter).Padding(0, 0, barGutter, 0).Gap(6).AlignItems(ui.Center).Children(func() {
						for _, opt := range row.Options {
							on := opt.Value == selectedVal
							if a.hgPill(c, "fopt:"+row.Key+":"+opt.Value+":"+opt.Name, opt.Name, on) {
								a.hgSelectFilterOption(row.Key, opt.Value)
							}
						}
					})
				})
			}
		})
	}
	l := h.filterResult
	a.hgSeriesGrid(c, h, "filter", l, "当前筛选条件下暂无剧集", func() {
		a.hgLoadFilterResults(false)
	}, func() {
		a.hgLoadFilterResults(len(l.items) == 0)
	})
}

func (a *app) hgNewView(c *ui.Context, h *hgApp) {
	if len(h.newDays) > 0 {
		ui.ScrollHorizontal(c.Key("hg-new-days")).Height(36+barGutter).Padding(4, 24, barGutter, 24).Gap(8).AlignItems(ui.Center).Children(func() {
			for _, d := range h.newDays {
				label := hgFirstNonEmpty(d.Label, d.Date)
				on := d.Date == h.newDate
				if a.hgPill(c, "day:"+d.Date, label, on) {
					a.hgSelectNewDate(d.Date)
				}
			}
		})
	}
	l := h.newResult
	a.hgSeriesGrid(c, h, "new:"+h.newDate, l, "该日期暂无上新剧集", func() {
		a.hgLoadNewReleases(false)
	}, func() {
		a.hgLoadNewReleases(len(l.items) == 0)
	})
}

func (a *app) hgTrackView(c *ui.Context, h *hgApp) {
	var favs, recent []hg.WatchEntry
	if h.hist != nil {
		favs = h.hist.Favorites()
		recent = h.hist.Recent()
	}
	favLabel := "我的追剧"
	if len(favs) > 0 {
		favLabel = fmt.Sprintf("我的追剧 (%d)", len(favs))
	}
	recentLabel := "最近观看"
	if len(recent) > 0 {
		recentLabel = fmt.Sprintf("最近观看 (%d)", len(recent))
	}

	ui.Row(c.Key("hg-track-tabs")).Height(42).PaddingX(24).Gap(24).AlignItems(ui.Center).Children(func() {
		if a.hgUnderlineTab(c, "track-fav", favLabel, h.trackTab == 0) {
			h.trackTab = 0
			h.trackList = ui.ListState{}
		}
		if a.hgUnderlineTab(c, "track-recent", recentLabel, h.trackTab == 1) {
			h.trackTab = 1
			h.trackList = ui.ListState{}
		}
	})

	entries := favs
	emptyText := "还没有追剧收藏，在剧集详情页点击「追剧」即可加入"
	if h.trackTab == 1 {
		entries = recent
		emptyText = "暂无观看历史"
	}
	if len(entries) == 0 {
		ui.Column(c.Key("hg-track-empty")).Grow(1).Center().Padding(24).Gap(8).Children(func() {
			ui.Icon(c, iconDrama).Size(36, 36).TextColor(pal.faint)
			ui.Text(c, emptyText).FontSize(13.5).TextColor(pal.muted)
		})
		return
	}

	h.trackList.Key = func(row int) any {
		if row < len(entries) {
			return fmt.Sprintf("%d:%s", h.trackTab, entries[row].SeriesID)
		}
		return row
	}
	ui.List(c.Key(fmt.Sprintf("hg-track-list:%d", h.trackTab)), &h.trackList, len(entries), func(row int) {
		e := entries[row]
		ui.Box(c).Padding(5, 24+barGutter, 5, 24).Children(func() {
			card := ui.Row(c.Key("track-item:"+e.SeriesID)).FillWidth().Padding(12, 14).Gap(14).Radius(12).
				AlignItems(ui.Center).Background(pal.raised).Border(1, pal.hairline)
			card.Children(func() {
				openBtn := ui.ButtonBase(c.Key("track-open:"+e.SeriesID)).Row().Grow(1).Shrink(1).MinWidth(0).Gap(14).
					AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(e.Title)
				openBtn.Children(func() {
					ui.Box(c).Size(56, 76).Radius(8).Clip().Background(pal.well).Children(func() {
						if bmp := h.img.getSized(e.CoverURL, thumbEdge); bmp != nil {
							ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover).Transition(coverFade)
						}
					})
					ui.Column(c).Gap(6).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						ui.Text(c, hgFirstNonEmpty(e.Title, "未命名剧集")).FontSize(14.5).FontWeight(600).
							TextColor(onOff(openBtn.Hovered(), pal.strong, pal.text)).SingleLine()
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							pill := "已追剧"
							if e.EpisodeIndex > 0 {
								if e.Position > 0 {
									pill = fmt.Sprintf("看到第 %d 集 · %s", e.EpisodeIndex, clock(e.Position))
								} else {
									pill = fmt.Sprintf("看到第 %d 集", e.EpisodeIndex)
								}
							}
							ui.Row(c).Height(22).PaddingX(8).Radius(11).AlignItems(ui.Center).Background(pal.accent.Alpha(0.12)).Children(func() {
								ui.Text(c, pill).FontSize(11.5).FontWeight(600).TextColor(pal.accent).SingleLine()
							})
							if e.EpisodeCount > 0 {
								ui.Text(c, fmt.Sprintf("共%d集", e.EpisodeCount)).FontSize(12).TextColor(pal.muted).SingleLine()
							}
						})
						if when := ago(e.UpdatedAt, time.Now()); when != "" {
							ui.Text(c, when).FontSize(11.5).TextColor(pal.faint).SingleLine()
						}
					})
				})
				if openBtn.Clicked() {
					a.hgOpenDetail(hg.Series{
						ID:           e.SeriesID,
						Title:        e.Title,
						CoverURL:     e.CoverURL,
						EpisodeCount: e.EpisodeCount,
					})
				}

				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ctaLabel := onOff(e.EpisodeIndex > 0, "继续观看", "查看剧集")
					if ui.Button(c.Key("track-watch:"+e.SeriesID), ctaLabel).Clicked() {
						s := hg.Series{
							ID:           e.SeriesID,
							Title:        e.Title,
							CoverURL:     e.CoverURL,
							EpisodeCount: e.EpisodeCount,
						}
						if e.EpisodeIndex > 0 {
							a.hgOpenPlayer(s, nil, 0)
						} else {
							a.hgOpenDetail(s)
						}
					}
					if h.trackTab == 0 {
						if ui.Button(c.Key("track-unfav:"+e.SeriesID), "取消追剧").Clicked() {
							a.hgToggleFavorite(hg.Series{ID: e.SeriesID, Title: e.Title, CoverURL: e.CoverURL, EpisodeCount: e.EpisodeCount})
						}
					} else {
						if ui.Button(c.Key("track-del:"+e.SeriesID), "删除记录").Clicked() {
							a.hgRemoveHistory(e.SeriesID)
						}
					}
				})
			})
		})
	}).Grow(1).Padding(4, 0, 12, 0)
}

func (a *app) hgSearchView(c *ui.Context, h *hgApp, p *hgPage) {
	ui.Row(c.Key("hg-search-filters")).Height(40).PaddingX(24).Gap(8).AlignItems(ui.Center).Children(func() {
		for _, f := range hgSearchFilters {
			if a.hgPill(c, "sf:"+f.label, f.label, p.searchFilter == f.value) {
				a.hgSetSearchFilter(p, f.value)
			}
		}
	})
	a.hgSeriesGrid(c, h, "search:"+p.query+":"+p.searchFilter, p.results, "没有找到相关短剧或漫剧", func() {
		a.hgLoadSearch(p, false)
	}, func() {
		a.hgLoadSearch(p, len(p.results.items) == 0)
	})
}

func (a *app) hgDetailView(c *ui.Context, h *hgApp, p *hgPage) {
	s := p.series
	var (
		histEntry hg.WatchEntry
		hasHist   bool
		fav       bool
	)
	if h.hist != nil && p.seriesID != "" {
		histEntry, hasHist = h.hist.Get(p.seriesID)
		fav = h.hist.IsFavorited(p.seriesID)
	}

	ui.Scroll(c.Key("hg-detail-scroll:"+p.seriesID)).Grow(1).Padding(8, 24+barGutter, 28, 24).Gap(20).Children(func() {
		if p.err.Kind != hg.ErrKindNone {
			a.hgErrorBanner(c, "detail:"+p.seriesID, p.err, func() {
				a.hgLoadDetail(p)
			})
		}

		// Header banner: portrait cover + metadata + actions
		ui.Row(c.Key("hg-detail-header")).FillWidth().Padding(20).Gap(22).Radius(16).AlignItems(ui.Start).
			Background(pal.raised).Border(1, pal.cardBorder).Children(func() {
			ui.Box(c).Size(172, 230).Radius(12).Clip().Background(pal.well).Children(func() {
				if bmp := h.img.getSized(s.CoverURL, coverEdge); bmp != nil {
					ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover).Transition(coverFade)
				}
			})

			ui.Column(c).Gap(10).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, hgFirstNonEmpty(s.Title, "载入中…")).FontSize(22).FontWeight(700).TextColor(pal.strong).MaxLines(2).Shrink(1).MinWidth(0)
				})

				// Status, score, category & tags
				ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
					if s.Score != "" {
						ui.Row(c).Height(24).PaddingX(9).Radius(12).AlignItems(ui.Center).Background(pal.accent.Alpha(0.14)).Children(func() {
							ui.Text(c, s.Score).FontSize(12).FontWeight(700).TextColor(pal.accent).SingleLine()
						})
					}
					status := s.StatusText
					if status == "" && s.EpisodeCount > 0 {
						status = fmt.Sprintf("全%d集", s.EpisodeCount)
					}
					if status != "" {
						ui.Row(c).Height(24).PaddingX(9).Radius(12).AlignItems(ui.Center).Background(pal.pillBg).Border(1, pal.pillBorder).Children(func() {
							ui.Text(c, status).FontSize(12).FontWeight(500).TextColor(pal.text).SingleLine()
						})
					}
					seenTag := map[string]bool{}
					addTag := func(tag string) {
						tag = strings.TrimSpace(tag)
						if tag == "" || seenTag[tag] {
							return
						}
						seenTag[tag] = true
						ui.Row(c).Height(24).PaddingX(9).Radius(12).AlignItems(ui.Center).Background(pal.hover).Children(func() {
							ui.Text(c, tag).FontSize(11.5).TextColor(pal.muted).SingleLine()
						})
					}
					addTag(s.Category)
					for _, tag := range s.Tags {
						addTag(tag)
					}
				})

				// Clickable actor pills -> hgPagePerson
				if len(s.Actors) > 0 {
					ui.Row(c.Key("hg-detail-actors")).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "主演").FontSize(12).FontWeight(600).TextColor(pal.faint)
						for _, actor := range s.Actors {
							ab := ui.ButtonBase(c.Key("actor:"+actor)).Height(26).PaddingX(10).Radius(13).Gap(4).
								AlignItems(ui.Center).FocusRing(false).Label("演员 " + actor)
							ab.Background(onOff(ab.Hovered(), pal.pillHoverBg, pal.pillBg)).Border(1, pal.pillBorder)
							ab.Children(func() {
								ui.Icon(c, iconUser).Size(12, 12).TextColor(pal.muted)
								ui.Text(c, actor).FontSize(12).TextColor(pal.text).SingleLine()
							})
							if ab.Clicked() {
								a.hgOpenPerson("", actor, "")
							}
						}
					})
				}

				if s.Abstract != "" {
					ui.Text(c, s.Abstract).FontSize(13).TextColor(pal.muted).MaxLines(4)
				}

				// Primary CTA + Favorite toggle
				ui.Row(c.Key("hg-detail-actions")).Gap(10).Margin(6, 0, 0, 0).AlignItems(ui.Center).Children(func() {
					resumeIdx := 0
					playLabel := "开始播放"
					if hasHist && histEntry.EpisodeIndex > 0 {
						resumeIdx = hgResumeEpisodeIndex(p.episodes, histEntry)
						playLabel = fmt.Sprintf("继续观看第 %d 集", histEntry.EpisodeIndex)
					}
					playBtn := ui.ButtonBase(c.Key("hg-detail-play")).Height(38).PaddingX(20).Gap(8).Radius(10).
						AlignItems(ui.Center).FocusRing(false).Label(playLabel).
						Background(pal.accent)
					if playBtn.Hovered() {
						playBtn.Background(pal.accent.Alpha(0.9))
					}
					playBtn.Children(func() {
						ui.Icon(c, iconPlay).Size(14, 14).TextColor(white)
						ui.Text(c, playLabel).FontSize(13.5).FontWeight(700).TextColor(white).SingleLine()
					})
					if playBtn.Clicked() {
						a.hgOpenPlayer(p.series, p.episodes, resumeIdx)
					}

					favLabel := onOff(fav, "已追剧", "追剧")
					favBtn := ui.ButtonBase(c.Key("hg-detail-fav")).Height(38).PaddingX(16).Gap(6).Radius(10).
						AlignItems(ui.Center).FocusRing(false).Label(favLabel)
					if fav {
						favBtn.Background(pal.accent.Alpha(0.12)).Border(1, pal.accent.Alpha(0.4))
					} else {
						favBtn.Background(onOff(favBtn.Hovered(), pal.pillHoverBg, pal.pillBg)).Border(1, pal.pillBorder)
					}
					favBtn.Children(func() {
						ui.Icon(c, onOff(fav, iconHeartFill, iconHeart)).Size(15, 15).TextColor(onOff(fav, pal.accent, pal.text))
						ui.Text(c, favLabel).FontSize(13.5).FontWeight(600).TextColor(onOff(fav, pal.accent, pal.text)).SingleLine()
					})
					if favBtn.Clicked() {
						a.hgToggleFavorite(p.series)
					}
				})
			})
		})

		// Multi-season strip
		if len(p.seasons) > 1 {
			ui.Column(c.Key("hg-seasons")).Gap(10).Children(func() {
				ui.Text(c, "系列季数").FontSize(15).FontWeight(700).TextColor(pal.strong)
				ui.ScrollHorizontal(c.Key("hg-seasons-row")).Height(34+barGutter).Padding(0, 0, barGutter, 0).Gap(8).AlignItems(ui.Center).Children(func() {
					for i, sn := range p.seasons {
						label := hgFirstNonEmpty(sn.Label, sn.Title, fmt.Sprintf("第%d季", i+1))
						on := sn.Current || sn.SeriesID == p.seriesID
						if a.hgPill(c, fmt.Sprintf("season:%s:%d", sn.SeriesID, i), label, on) {
							a.hgSwitchSeason(p, sn)
						}
					}
				})
			})
		}

		// Episode list with grouped pagination
		ui.Column(c.Key("hg-episodes")).Gap(12).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "选集").FontSize(15).FontWeight(700).TextColor(pal.strong)
				if len(p.episodes) > 0 {
					ui.Text(c, fmt.Sprintf("共 %d 集", len(p.episodes))).FontSize(12.5).TextColor(pal.muted)
				}
			})
			if p.epErr.Kind != hg.ErrKindNone {
				a.hgErrorBanner(c, "ep:"+p.seriesID, p.epErr, func() {
					a.hgLoadDetail(p)
				})
			} else if p.epLoading && len(p.episodes) == 0 {
				ui.Text(c, "正在加载剧集列表…").FontSize(12.5).TextColor(pal.faint)
			} else if len(p.episodes) == 0 {
				ui.Text(c, "暂无可播放剧集").FontSize(12.5).TextColor(pal.faint)
			} else {
				groups := (len(p.episodes) + hgEpisodeGroupSize - 1) / hgEpisodeGroupSize
				if p.epGroup < 0 || p.epGroup >= groups {
					p.epGroup = 0
				}
				if groups > 1 {
					ui.ScrollHorizontal(c.Key("hg-ep-groups")).Height(32+barGutter).Padding(0, 0, barGutter, 0).Gap(8).AlignItems(ui.Center).Children(func() {
						for g := range groups {
							start := g * hgEpisodeGroupSize
							end := min(len(p.episodes), start+hgEpisodeGroupSize)
							lo := p.episodes[start].Index
							hi := p.episodes[end-1].Index
							if lo <= 0 {
								lo = start + 1
							}
							if hi <= 0 {
								hi = end
							}
							label := fmt.Sprintf("%d-%d", lo, hi)
							if a.hgPill(c, fmt.Sprintf("epg:%d", g), label, p.epGroup == g) {
								p.epGroup = g
							}
						}
					})
				}

				start := p.epGroup * hgEpisodeGroupSize
				end := min(len(p.episodes), start+hgEpisodeGroupSize)
				ui.Row(c.Key(fmt.Sprintf("hg-ep-grid:%d", p.epGroup))).Wrap().Gap(8).Children(func() {
					for i := start; i < end; i++ {
						ep := p.episodes[i]
						num := ep.Index
						if num <= 0 {
							num = i + 1
						}
						watched := hasHist && ((histEntry.EpisodeID != "" && histEntry.EpisodeID == ep.ID) || (histEntry.EpisodeIndex > 0 && histEntry.EpisodeIndex == num))
						epLabel := fmt.Sprintf("第%d集", num)
						eb := ui.ButtonBase(c.Key(fmt.Sprintf("ep:%s:%d", ep.ID, i))).MinWidth(74).Height(38).PaddingX(12).Radius(10).
							Center().FocusRing(false).Label(epLabel)
						switch {
						case watched:
							eb.Background(pal.accent.Alpha(0.14)).Border(1, pal.accent.Alpha(0.5))
						case eb.Hovered():
							eb.Background(pal.hover).Border(1, pal.cardHoverBd)
						default:
							eb.Background(pal.raised).Border(1, pal.hairline)
						}
						eb.Children(func() {
							ui.Text(c, epLabel).FontSize(13).FontWeight(onOff(watched, 700, 500)).
								TextColor(onOff(watched, pal.accent, pal.text)).SingleLine()
						})
						if eb.Clicked() {
							a.hgOpenPlayer(p.series, p.episodes, i)
						}
					}
				})
			}
		})

		// Series reviews section
		a.hgDetailReviewsSection(c, h, p)

		// Related recommendations
		if len(p.recommends) > 0 {
			ui.Column(c.Key("hg-recs")).Gap(12).Children(func() {
				ui.Text(c, "相关推荐").FontSize(15).FontWeight(700).TextColor(pal.strong)
				ui.ScrollHorizontal(c.Key("hg-recs-row")).Height(268+barGutter).Padding(0, 0, barGutter, 0).Gap(14).AlignItems(ui.Start).Children(func() {
					for i, rec := range p.recommends {
						a.hgSeriesCard(c.Key(fmt.Sprintf("rec:%s:%d", rec.ID, i)), h, rec).Width(154)
					}
				})
			})
		}
	})
}

func (a *app) hgPersonView(c *ui.Context, h *hgApp, p *hgPage) {
	ui.Column(c.Key("hg-person-head")).Padding(6, 24, 12, 24).Gap(10).Children(func() {
		if p.personErr.Kind != hg.ErrKindNone {
			a.hgErrorBanner(c, "person:"+p.userID, p.personErr, func() {
				a.hgLoadPerson(p)
			})
		}
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			a.hgAvatar(c, h, p.person.AvatarURL, p.person.Name, 68)
			ui.Column(c).Gap(5).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, hgFirstNonEmpty(p.person.Name, "演员主页")).FontSize(20).FontWeight(700).TextColor(pal.strong).SingleLine()
					if p.person.WorksCount > 0 {
						ui.Text(c, fmt.Sprintf("%d 部作品", p.person.WorksCount)).FontSize(12.5).TextColor(pal.muted).SingleLine()
					}
				})
				if p.person.Intro != "" {
					ui.Text(c, p.person.Intro).FontSize(12.5).TextColor(pal.faint).MaxLines(2)
				}
			})
		})
	})

	a.hgSeriesGrid(c, h, "person:"+p.userID+":"+p.person.Name, p.works, "暂无作品信息", func() {
		a.hgLoadPersonWorks(p, false)
	}, func() {
		a.hgLoadPersonWorks(p, len(p.works.items) == 0)
	})
}

func (a *app) hgSeriesGrid(c *ui.Context, h *hgApp, key string, l *hgSeriesList, emptyText string, onMore, onRetry func()) {
	if l == nil {
		return
	}
	w, _ := c.Size()
	inner := max(320, w-contentLeft-24-barGutter-16)
	cols := max(3, int((inner+16)/(180+16)))
	nCardRows := 0
	if len(l.items) > 0 {
		nCardRows = (len(l.items) + cols - 1) / cols
	}
	totalRows := nCardRows + 1
	l.list.Key = func(row int) any {
		if row == nCardRows {
			return "foot"
		}
		return row
	}
	ui.List(c.Key("hg-grid:"+key), &l.list, totalRows, func(row int) {
		if row >= nCardRows-2 && l.hasMore && !l.loading && l.err.Kind == hg.ErrKindNone && onMore != nil {
			onMore()
		}
		if row == nCardRows {
			ui.Column(c.Key("hg-grid-foot")).Padding(16, 24, 32, 24).AlignItems(ui.Center).Children(func() {
				if l.err.Kind != hg.ErrKindNone {
					a.hgErrorBanner(c, key, l.err, onRetry)
					return
				}
				text := ""
				switch {
				case l.loading:
					text = "载入中…"
				case l.loaded && len(l.items) == 0:
					text = emptyText
				case l.loaded && !l.hasMore:
					text = "没有更多了"
				}
				if text != "" {
					ui.Text(c, text).FontSize(12.5).TextColor(pal.faint)
				}
			})
			return
		}
		start := row * cols
		ui.Row(c).Padding(0, barGutter+16, 22, 24).Gap(16).AlignItems(ui.Start).Children(func() {
			for j := range cols {
				idx := start + j
				if idx < len(l.items) {
					a.hgSeriesCard(c.Key(fmt.Sprintf("%d:%s", idx, l.items[idx].ID)), h, l.items[idx]).Grow(1).Basis(0).MinWidth(0)
				} else {
					ui.Box(c).Grow(1).Basis(0).MinWidth(0)
				}
			}
		})
	}).Grow(1).Padding(4, 0, 0, 0)
}

func (a *app) hgSeriesCard(c *ui.Context, h *hgApp, s hg.Series) ui.Element {
	b := ui.ButtonBase(c).Column().AlignItems(ui.Stretch).Justify(ui.Start).Gap(7).FocusRing(false).Label(s.Title)
	b.Children(func() {
		cover := ui.Box(c).AspectRatio(3.0 / 4.0).Radius(10).Clip().Background(pal.well).
			Border(1, onOff(b.Hovered(), pal.cardHoverBd, pal.cardBorder))
		cover.Children(func() {
			if bmp := h.img.getSized(s.CoverURL, coverEdge); bmp != nil {
				ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover).Transition(coverFade)
			}
			if b.Hovered() {
				ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(ui.RGBA(255, 255, 255, 0.06))
			}
			badge := s.StatusText
			if badge == "" && s.EpisodeCount > 0 {
				badge = fmt.Sprintf("全%d集", s.EpisodeCount)
			}
			if badge != "" || s.Score != "" {
				ui.Row(c).Absolute().Left(0).Right(0).Bottom(0).Padding(22, 8, 7, 8).Gap(6).AlignItems(ui.Center).
					Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.72), 180).Children(func() {
					if badge != "" {
						ui.Text(c, badge).FontSize(11).FontWeight(600).TextColor(white).SingleLine().Grow(1).Shrink(1).MinWidth(0)
					} else {
						ui.Spacer(c)
					}
					if s.Score != "" {
						ui.Text(c, s.Score).FontSize(11).FontWeight(600).TextColor(ui.Hex("#ffd166")).SingleLine()
					}
				})
			}
		})
		title := ui.Text(c, s.Title).FontSize(13.5).FontWeight(600).TextColor(pal.text).MaxLines(2)
		if b.Hovered() {
			title.TextColor(pal.strong)
		}
		sub := hgSeriesSubtitle(s)
		if sub != "" {
			ui.Text(c, sub).FontSize(11.5).TextColor(pal.faint).SingleLine()
		}
	})
	if b.Clicked() {
		a.hgOpenDetail(s)
	}
	return b
}

func hgSeriesSubtitle(s hg.Series) string {
	if s.Category != "" {
		return s.Category
	}
	if len(s.Tags) > 0 {
		return strings.Join(s.Tags[:min(2, len(s.Tags))], " · ")
	}
	if s.Abstract != "" {
		return strings.Join(strings.Fields(s.Abstract), " ")
	}
	return ""
}

func (a *app) hgAvatar(c *ui.Context, h *hgApp, url, name string, size float32) {
	box := ui.Box(c).Size(size, size).Radius(size / 2).Clip().Background(pal.press).Center()
	box.Children(func() {
		if h != nil && h.img != nil {
			if bmp := h.img.getSized(url, avatarEdge); bmp != nil {
				ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover)
				return
			}
		}
		ch := "?"
		if r := []rune(strings.TrimSpace(name)); len(r) > 0 {
			ch = strings.ToUpper(string(r[0]))
		}
		ui.Text(c, ch).FontSize(size * 0.42).FontWeight(600).TextColor(pal.muted)
	})
}
