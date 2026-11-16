package app

import (
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
)

// The window is laid out as Douyin's own, with less in it: a narrow sidebar
// (精选, 推荐, the accounts followed), the search field centered along the
// top, and under it a grid of covers or the video playing. The components
// are MySpotify's: its buttons, its rows, its type sizes.

const (
	sidebarWidth = 200
	// barGutter is the room a scroll keeps clear for its bar, which MyGo
	// draws over the content 3 to 9 DIPs in from the edge: a list whose rows
	// keep this much clear themselves needs none.
	barGutter    = 12
	topBarHeight = 56
	bgHex        = "#161823" // Douyin's dark, which the window always is
)

// palette holds the app's colors, on Douyin's dark.
type palette struct {
	bg, raised, text, muted, faint ui.Color
	hairline, hover, press, accent ui.Color
}

var pal = palette{
	bg:       ui.Hex(bgHex),
	raised:   ui.Hex("#21232e"),
	text:     ui.RGBA(255, 255, 255, 0.92),
	muted:    ui.RGBA(255, 255, 255, 0.6),
	faint:    ui.RGBA(255, 255, 255, 0.36),
	hairline: ui.RGBA(255, 255, 255, 0.08),
	hover:    ui.RGBA(255, 255, 255, 0.06),
	press:    ui.RGBA(255, 255, 255, 0.1),
	accent:   ui.Hex("#fe2c55"),
}

var appFont = func() string {
	if runtime.GOOS == "darwin" {
		return "system-ui"
	}
	return "MiSans, system-ui, Microsoft YaHei UI"
}()

var white = ui.RGB(255, 255, 255)

func (a *app) view(c *ui.Context) {
	t := *ui.DarkTheme()
	t.Background, t.Text, t.TextMuted = pal.bg, pal.text, pal.muted
	t.Accent, t.AccentText = pal.accent, white
	t.Selection = pal.accent.Alpha(0.35)
	t.Font = appFont
	c.SetTheme(&t)
	defer a.img.sweep()

	c.OnShortcut(0, ui.KeyDown, func() { a.step(1) })
	c.OnShortcut(0, ui.KeyUp, func() { a.step(-1) })
	c.OnShortcut(0, ui.KeyEscape, a.escape)
	root := ui.Box(c).Fill().Background(pal.bg)
	if !c.Focused(a.searchBox) && !c.Focused(a.shareBox) && !c.Focused(a.dmBox) {
		// Space pauses wherever the focus is, as in MySpotify: on the box
		// around everything it comes before a button the pointer focused,
		// which would take it as a click; on the window, as nothing has
		// the focus.
		root.OnShortcut(0, ui.KeySpace, a.toggle)
		c.OnShortcut(0, ui.KeySpace, a.toggle)
	}
	p := a.cur()
	root.Children(func() {
		if a.theater && p.plays() { // 网页全屏: the video alone, filling the window
			ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() { a.stage(c, p) })
			return
		}
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(sidebarWidth).Children(func() { a.sidebar(c) })
			ui.Column(c.Key("main")).Grow(1).Children(func() {
				a.topBar(c, p)
				if !p.plays() {
					switch {
					case p == a.picks:
						a.tabsRow(c)
					case p == a.follow:
						a.followsRow(c)
					case a.isMine(p):
						a.mineHeader(c)
					case p.user != nil:
						a.profileHeader(c, *p.user)
					case len(p.users) > 0:
						a.usersRow(c.Key("users"), p.users)
					}
				}
				ui.Row(c).Grow(1).Padding(0, 16, 16, 0).Gap(16).AlignItems(ui.Stretch).Children(func() {
					if p.plays() {
						a.stage(c, p)
					} else {
						a.grid(c, p)
					}
					switch {
					case a.notes != nil:
						a.notePanel(c, a.notes)
					case a.dms != nil:
						a.dmPanel(c, a.dms)
					case a.sheet != nil && p.plays():
						a.commentPanel(c, a.sheet)
					}
				})
			})
		})
	})
	if c.Focused(a.searchBox) {
		a.searchList(c)
	}
	if a.toast != "" {
		c.Toast(a.toast)
		a.toast = ""
	}
	if a.pip != nil { // 小窗 follows what this window plays
		a.pip.Invalidate()
	}
}

// searchField is where the search field sits in the window: its left edge
// and width, centered over the content.
func searchField(c *ui.Context) (left, width float32) {
	w, _ := c.Size()
	width = min(460, w-sidebarWidth-2*260)
	return sidebarWidth + (w-sidebarWidth-width)/2, width
}

// searchList drops from the focused search field: the hot search list's
// first ten while it is empty, what Douyin suggests for what is typed once
// something is; a press searches for one. A press, not a click: the field
// loses the focus as it is pressed, which takes the list away before a release.
func (a *app) searchList(c *ui.Context) {
	q := strings.TrimSpace(a.query)
	var words []string
	switch {
	case q == "":
		for _, h := range a.hot {
			words = append(words, h.Word)
		}
	case q == a.sugFor:
		words = a.sugs
	}
	words = words[:min(10, len(words))]
	if len(words) == 0 {
		return
	}
	left, width := searchField(c)
	ui.Column(c.Key("hot")).Absolute().Top(52).Left(left).Width(width).Padding(8, 6).Radius(12).
		Background(ui.Hex("#252733")).Border(0.5, pal.hairline).Shadow(0, 10, 30, -6, ui.RGBA(0, 0, 0, 0.5)).Children(func() {
		if q == "" {
			ui.Text(c, "抖音热搜").FontSize(12).FontWeight(600).TextColor(pal.faint).Margin(2, 10, 6, 10)
		}
		for i, w := range words {
			row := ui.Row(c.Key(w)).Height(34).PaddingX(10).Gap(10).Radius(8).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(w)
			row.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
					a.query = w
					a.search(w)
					return true
				}
				return false
			})
			if row.Hovered() {
				row.Background(pal.hover)
			}
			row.Children(func() {
				if q != "" {
					ui.Icon(c, iconSearch).Size(13, 13).TextColor(pal.faint)
					ui.Text(c, w).FontSize(13.5).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
					return
				}
				rank, color := fmt.Sprint(i), pal.faint
				switch {
				case i == 0:
					rank, color = "顶", pal.accent
				case i <= 3:
					color = ui.Hex("#ff9f43")
				}
				ui.Text(c, rank).FontSize(13).FontWeight(700).TextColor(color).Width(20)
				ui.Text(c, w).FontSize(13.5).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if v := a.hot[i].Value; v > 0 {
					ui.Text(c, count(v)).FontSize(11.5).TextColor(pal.faint)
				}
			})
		}
	})
}

// sidebar is Douyin's: 精选 and 推荐; the user's 关注, 朋友 and 我的; then
// 直播 and 短剧. A root not there (no client, or the account not yet known)
// leaves its row out.
func (a *app) sidebar(c *ui.Context) {
	// Below the window buttons, which sit over it.
	ui.Column(c).Fill().Padding(48, 12, 14, 12).Gap(4).DragWindow().Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(0, 10, 14, 10).Children(func() {
			ui.Image(c, logo).Size(26, 26)
			ui.Text(c, "MyDouyin").FontSize(20).FontWeight(800).TextColor(white)
		})
		root := a.pages[0]
		nav := func(icon *ui.SVG, label string, r *page, on bool) bool {
			return r != nil && a.navItem(c, icon, label, on).Clicked()
		}
		if nav(iconCompass, "精选", a.picks, root == a.picks) {
			a.root(a.picks)
		}
		if nav(iconSpark, "推荐", a.feed, root == a.feed) {
			a.root(a.feed)
		}
		divider := func() { ui.Box(c).Height(1).Background(pal.hairline).Margin(8, 10) }
		divider()
		if nav(iconFollow, "关注", a.follow, root == a.follow) {
			a.root(a.follow)
		}
		if nav(iconFriends, "朋友", a.friends, root == a.friends) {
			a.root(a.friends)
		}
		if nav(iconUser, "我的", a.mine[a.mineTab], a.isMine(root)) {
			a.showMine(a.mineTab)
		}
		divider()
		if a.live != nil {
			b := a.listItem(c.Key("直播"), root == a.live, "直播")
			color := pal.muted
			if root == a.live {
				color = white
			}
			b.Children(func() {
				ui.Icon(c, iconLive).Size(18, 18).TextColor(color)
				ui.Text(c, "直播").FontSize(14.5).FontWeight(600).TextColor(color).Grow(1)
				ui.Text(c, "LIVE").FontSize(9.5).FontWeight(800).TextColor(pal.accent).PaddingX(5).Height(16).Radius(4).
					Background(ui.RGBA(254, 44, 85, 0.16)).Center()
			})
			if b.Clicked() {
				a.root(a.live)
			}
		}
		if nav(iconDrama, "短剧", a.drama, root == a.drama) {
			a.root(a.drama)
		}
		a.updateBanner(c)
	})
}

// followsRow runs along the top of 关注: the accounts followed, each a
// picture over its name, which opens its videos.
func (a *app) followsRow(c *ui.Context) {
	if a.followMsg != "" {
		ui.Text(c, a.followMsg).FontSize(12).TextColor(pal.faint).Margin(4, 24, 8, 24)
		return
	}
	a.usersRow(c.Key("follows"), a.follows)
}

// usersRow is accounts in a row, each a picture over its name, which opens
// its videos: the ones followed atop 关注, the ones found atop a search.
func (a *app) usersRow(c *ui.Context, us []dy.User) {
	if len(us) == 0 {
		return
	}
	ui.ScrollHorizontal(c).Height(84+barGutter).Padding(0, 18, barGutter, 18).Gap(4).AlignItems(ui.Center).Children(func() {
		for _, u := range us {
			b := ui.ButtonBase(c.Key("u:"+u.ID)).Column().Width(68).Padding(6, 2).Gap(6).Radius(10).AlignItems(ui.Center).FocusRing(false).Label(u.Name)
			if b.Hovered() {
				b.Background(pal.hover)
			}
			b.Children(func() {
				a.avatar(c, u.Avatar, u.Name, 44)
				ui.Text(c, u.Name).FontSize(11.5).TextColor(pal.muted).SingleLine().MaxWidth(64)
			})
			if b.Clicked() {
				a.openAuthor(a.cur(), u.Name, u.ID)
			}
		}
	})
}

// mineHeader tops 我的: the user's profile, and the tabs of their videos,
// likes, saves and watch history.
func (a *app) mineHeader(c *ui.Context) {
	a.profileHeader(c, a.me)
	ui.Row(c).Height(40).PaddingX(24).Gap(26).AlignItems(ui.Center).Children(func() {
		for _, name := range mineTabs {
			on := name == a.mineTab
			b := ui.ButtonBase(c.Key("mine:" + name)).Column().Gap(5).AlignItems(ui.Center).FocusRing(false).Label(name)
			b.Children(func() {
				color, weight := pal.muted, 500
				switch {
				case on:
					color, weight = white, 700
				case b.Hovered():
					color = pal.text
				}
				ui.Text(c, name).FontSize(15).FontWeight(weight).TextColor(color).SingleLine()
				bar := ui.Box(c).Width(16).Height(3).Radius(2)
				if on {
					bar.Background(pal.accent)
				}
			})
			if b.Clicked() && !on {
				a.showMine(name)
			}
		}
	})
}

// profileHeader is an account's picture, name, counts, 抖音号 and region
// where known, and signature: atop 我的 and an author's videos.
func (a *app) profileHeader(c *ui.Context, me dy.User) {
	ui.Row(c).Padding(6, 24, 14, 24).Gap(18).AlignItems(ui.Center).Children(func() {
		a.avatar(c, me.Avatar, me.Name, 72)
		ui.Column(c).Gap(8).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, me.Name).FontSize(20).FontWeight(700).TextColor(white).SingleLine()
			ui.Row(c).Gap(18).Children(func() {
				for _, kv := range [][2]string{{"关注", count(me.Following)}, {"粉丝", count(me.Followers)}, {"获赞", count(me.Liked)}} {
					ui.Row(c).Gap(5).AlignItems(ui.Baseline).Children(func() {
						ui.Text(c, kv[0]).FontSize(13).TextColor(pal.muted)
						ui.Text(c, kv[1]).FontSize(15).FontWeight(700).TextColor(pal.text)
					})
				}
			})
			var ids []string
			if me.Handle != "" {
				ids = append(ids, "抖音号："+me.Handle)
			}
			if me.Place != "" {
				ids = append(ids, "IP属地："+me.Place)
			}
			if len(ids) > 0 {
				ui.Text(c, strings.Join(ids, "   ")).FontSize(12).TextColor(pal.faint).SingleLine()
			}
			if me.Signature != "" {
				// On one line: a signature's blank lines would leave the second empty but for its ellipsis.
				ui.Text(c, strings.Join(strings.Fields(me.Signature), " ")).FontSize(12.5).TextColor(pal.faint).MaxLines(2)
			}
		})
	})
}

// listItem is a row of the sidebar: highlighted under the pointer, and
// marked when it is the page shown.
func (a *app) listItem(c *ui.Context, selected bool, label string) ui.Element {
	b := ui.ButtonBase(c).Height(38).PaddingX(10).Gap(10).Radius(10).AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(label)
	bg := ui.Transparent
	switch {
	case selected:
		bg = pal.press
	case b.Hovered():
		bg = pal.hover
	}
	return b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
}

func (a *app) navItem(c *ui.Context, icon *ui.SVG, label string, selected bool) ui.Element {
	color := pal.muted
	if selected {
		color = white
	}
	return a.listItem(c.Key(label), selected, label).Children(func() {
		ui.Icon(c, icon).Size(18, 18).TextColor(color)
		ui.Text(c, label).FontSize(14.5).FontWeight(600).TextColor(color)
	})
}

// avatar is an account's picture in a circle, or its name's first character while it loads.
func (a *app) avatar(c *ui.Context, url, name string, size float32) {
	box := ui.Box(c).Size(size, size).Radius(size / 2).Clip().Background(pal.press).Center()
	box.Children(func() {
		if bmp := a.img.get(url); bmp != nil {
			ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover)
			return
		}
		ch := "?"
		if r := []rune(name); len(r) > 0 {
			ch = strings.ToUpper(string(r[0]))
		}
		ui.Text(c, ch).FontSize(size * 0.44).FontWeight(600).TextColor(pal.muted)
	})
}

// topBar holds the way back with the page's name, and the search field in
// the middle; it drags the window.
func (a *app) topBar(c *ui.Context, p *page) {
	ui.Row(c).Height(topBarHeight).PaddingX(16).Gap(4).AlignItems(ui.Center).DragWindow().Children(func() {
		if len(a.pages) > 1 || p.grid && p.playing {
			if iconButton(c.Key("back"), iconBack, 18, pal.muted, "返回").Clicked() {
				a.back()
			}
			if a.pages[0] != p { // the roots are named in the sidebar
				ui.Text(c, p.title).FontSize(15).FontWeight(600).TextColor(pal.text).SingleLine().Shrink(1).MinWidth(0).MaxWidth(220)
			}
		}
		dm := pal.muted
		if a.dms != nil {
			dm = white
		}
		if iconButton(c.Key("dm"), iconChat, 20, dm, "私信").Absolute().Top(6).Right(16 + 36 + 4).Clicked() {
			if a.dms != nil {
				a.dms = nil
			} else {
				a.openDMs()
			}
		}
		bell := ui.Box(c).Absolute().Top(6).Right(16)
		bell.Children(func() {
			color := pal.muted
			if a.notes != nil {
				color = white
			}
			if iconButton(c.Key("bell"), iconBell, 20, color, "通知").Clicked() {
				if a.notes != nil {
					a.notes = nil
				} else {
					a.openNotes()
				}
			}
			if a.unread > 0 {
				ui.Box(c).Absolute().Top(4).Right(2).MinWidth(16).Height(16).PaddingX(4).Radius(8).Background(pal.accent).Center().
					HitTest(func(x, y float32, r ui.Rect) bool { return false }). // the bell under it takes the click
					Children(func() {
						ui.Text(c, fmt.Sprint(min(a.unread, 99))).FontSize(10).FontWeight(700).TextColor(white)
					})
			}
		})
		left, fieldW := searchField(c)
		left -= sidebarWidth // the bar starts beside the sidebar
		field := ui.Row(c).Absolute().Top(10).Left(left).Width(fieldW).Height(36).Radius(10).PaddingStart(14).
			AlignItems(ui.Center).Background(pal.hover)
		if c.Focused(a.searchBox) {
			field.Border(1, pal.muted)
		} else {
			field.Border(1, pal.hairline)
		}
		field.Children(func() {
			in := ui.TextInputBase(c.Key("search"), &a.query).Bind(&a.searchBox).Grow(1).
				Placeholder("搜索你感兴趣的内容").FontSize(13.5).Label("搜索")
			if in.Submitted() {
				a.search(a.query)
			} else if in.Changed() {
				a.suggest()
			}
			ui.Box(c).Width(1).Height(16).Background(pal.hairline)
			b := ui.ButtonBase(c.Key("go")).Height(34).PaddingX(14).Gap(6).AlignItems(ui.Center).FocusRing(false).Label("搜索按钮").Radius(0, 10, 10, 0)
			if b.Hovered() {
				b.Background(pal.hover)
			}
			b.Children(func() {
				ui.Icon(c, iconSearch).Size(15, 15).TextColor(pal.text)
				ui.Text(c, "搜索").FontSize(13.5).FontWeight(600).TextColor(pal.text)
			})
			if b.Clicked() {
				a.search(a.query)
			}
		})
	})
}

// tabsRow is 精选's categories, the one shown marked, scrolling sideways
// where the window is too narrow for them.
func (a *app) tabsRow(c *ui.Context) {
	ui.ScrollHorizontal(c.Key("tabs")).Height(40+barGutter).Padding(0, 24, barGutter, 24).Gap(22).AlignItems(ui.Center).Children(func() {
		for _, t := range dy.Tabs {
			b := ui.ButtonBase(c.Key(t.Name)).Column().Gap(5).AlignItems(ui.Center).FocusRing(false).Label(t.Name)
			on := t.ID == a.tab
			b.Children(func() {
				color := pal.muted
				switch {
				case on:
					color = white
				case b.Hovered():
					color = pal.text
				}
				weight := 500
				if on {
					weight = 700
				}
				ui.Text(c, t.Name).FontSize(15).FontWeight(weight).TextColor(color).SingleLine()
				bar := ui.Box(c).Width(16).Height(3).Radius(2)
				if on {
					bar.Background(pal.accent)
				}
			})
			if b.Clicked() && !on {
				a.showTab(t.ID)
			}
		}
	})
}

// grid is p's covers, as many columns as fit, loading more near its end. 精选's
// first video is a big card that plays, silent, as Douyin's does.
func (a *app) grid(c *ui.Context, p *page) {
	w, h := c.Size()
	inner := w - sidebarWidth - 24 - barGutter - 16 // the grid's padding, the row's
	if a.notes != nil || a.dms != nil {
		inner -= 360 + 16
	}
	cols := max(2, int((inner+16)/(260+16)))
	colW := (inner - 16*float32(cols-1)) / float32(cols)
	ui.Scroll(c.Key("grid:"+p.title)).TrackScroll(&p.scroll).Grow(1).Padding(4, barGutter, 32, 24).Children(func() {
		ui.Grid(c).Columns(cols).GapX(16).GapY(26).Children(func() {
			for i, s := range p.list {
				if i == 0 && p == a.picks && cols >= 3 {
					// As tall as two cards beside it: their covers and some 63 points of text each (measured).
					a.bigCard(c.Key("0"), p, s, cols-1, 2*(colW*9/16+63)+26)
					continue
				}
				a.card(c.Key(fmt.Sprint(i)), p, i, s) // by place: a feed can bring a video twice
			}
		})
		// Near the end once it has scrolled into view: the next page.
		if end := ui.Box(c.Key("end")).Height(1).Bounds(); end.W > 0 && end.Y < h+800 {
			a.more(p)
		}
		text := ""
		switch {
		case p.msg != "":
			text = p.msg
		case p.fetching:
			text = "载入中…"
		case p.done && len(p.list) == 0:
			text = "没有结果"
		case p.done:
			text = "没有更多了"
		}
		ui.Row(c).Padding(24, 0).Justify(ui.Center).Children(func() {
			ui.Text(c, text).FontSize(12.5).TextColor(pal.faint)
		})
	})
}

// card is a video in the grid: its cover with its likes and length, its text
// in two lines, and its author and day.
func (a *app) card(c *ui.Context, p *page, i int, s *slot) {
	v := s.v
	b := ui.ButtonBase(c).Column().AlignItems(ui.Stretch).Justify(ui.Start).Gap(8).FocusRing(false).Label(v.Desc)
	b.Children(func() {
		cover := ui.Box(c).AspectRatio(16.0 / 9).Radius(10).Clip().Background(pal.raised)
		cover.Children(func() {
			if bmp := a.img.get(v.Cover); bmp != nil {
				ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover)
			}
			if b.Hovered() {
				ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(ui.RGBA(255, 255, 255, 0.06))
			}
			if v.Live != nil {
				liveBadge(c)
			}
			ui.Row(c).Absolute().Left(0).Right(0).Bottom(0).Padding(18, 10, 7, 10).AlignItems(ui.Center).
				Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.55), 180).Children(func() {
				if v.Live != nil {
					ui.Text(c, v.Live.Viewers+" 人在看").FontSize(12).FontWeight(500).TextColor(white)
					return
				}
				ui.Icon(c, iconHeart).Size(13, 13).TextColor(white)
				ui.Text(c, count(v.Likes)).FontSize(12).FontWeight(500).TextColor(white).Margin(0, 0, 0, 4).Grow(1)
				ui.Text(c, clock(v.Duration)).FontSize(12).FontWeight(500).TextColor(white)
			})
		})
		title := ui.Text(c, v.Desc).FontSize(14).TextColor(pal.text).MaxLines(2)
		if b.Hovered() {
			title.TextColor(white)
		}
		ui.Text(c, byline(v)).FontSize(12).TextColor(pal.faint).SingleLine()
	})
	if b.Clicked() {
		a.play(p, i)
	}
}

// bigCard is 精选's first video across span columns and two rows, playing
// silent once loaded, its cover until then, with its author and text over it.
func (a *app) bigCard(c *ui.Context, p *page, s *slot, span int, height float32) {
	v := s.v
	b := ui.ButtonBase(c).ColumnSpan(span).RowSpan(2).Height(height).Radius(12).Clip().Background(ui.RGB(0, 0, 0)).FocusRing(false).Label(v.Desc)
	b.Children(func() {
		if s.vp != nil {
			ui.Video(c, s.vp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover)
		} else if bmp := a.img.get(v.Cover); bmp != nil {
			ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover)
		}
		if v.Live != nil {
			liveBadge(c)
		}
		ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(60, 18, 16, 18).Gap(6).
			Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.6), 180).Children(func() {
			ui.Text(c, byline(v)).FontSize(15).FontWeight(700).TextColor(white)
			ui.Text(c, v.Desc).FontSize(13.5).TextColor(ui.RGBA(255, 255, 255, 0.85)).MaxLines(2)
		})
	})
	if b.Clicked() {
		a.play(p, 0)
	}
}

// stage is p's current video, with its author and text over its foot, the
// play controls along its bottom and its actions down its right side. All of
// it takes the wheel, which steps through the list; the picture takes
// clicks, which pause. Not the stage: a handler there would take the press
// from the buttons over the picture.
func (a *app) stage(c *ui.Context, p *page) {
	box := ui.Box(c.Key("stage")).Grow(1).Radius(12).Clip().Background(ui.RGB(0, 0, 0))
	box.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputScroll || ev.Momentum || ev.DY == 0 {
			return false
		}
		if ev.DY > 0 {
			a.step(1)
		} else {
			a.step(-1)
		}
		return true
	})
	box.Children(func() {
		picture := ui.Box(c.Key("picture")).Absolute().Top(0).Left(0).Right(0).Bottom(0).HandleInput(func(ev ui.InputEvent) bool {
			switch {
			case ev.Kind == ui.InputPointerDown && ev.Button == 0:
				return true // taking the press is what brings its release here
			case ev.Kind == ui.InputPointerUp && !ev.Canceled && ev.Button == 0:
				a.toggle()
				return true
			}
			return false
		})
		if p.idx >= len(p.list) {
			text := "载入中…"
			switch {
			case p.msg != "":
				text = p.msg
			case p.done:
				text = "没有结果"
			}
			picture.Center().Children(func() {
				ui.Text(c, text).FontSize(13).TextColor(pal.muted)
			})
			return
		}
		s := p.list[p.idx]
		picture.Children(func() {
			if s.vp != nil && a.listen { // 听抖音: the sound goes on, the picture gives way to the cover
				if bmp := a.img.get(s.v.Cover); bmp != nil {
					ui.Image(c, bmp).Fill().Fit(ui.Contain).Opacity(0.35)
				}
				ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(10).Children(func() {
					ui.Icon(c, iconHeadphones).Size(48, 48).TextColor(white)
					ui.Text(c, "听抖音中").FontSize(15).FontWeight(600).TextColor(white)
				})
			} else if s.vp != nil && a.pipOn { // playing in 小窗: a player shows in one place at a time
				if bmp := a.img.get(s.v.Cover); bmp != nil {
					ui.Image(c, bmp).Fill().Fit(ui.Contain).Opacity(0.3)
				}
				ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(14).Children(func() {
					ui.Icon(c, iconPip).Size(44, 44).TextColor(white)
					ui.Text(c, "小窗播放中").FontSize(15).FontWeight(600).TextColor(white)
					if ui.Button(c.Key("pip-back"), "回到这里播放").Clicked() {
						a.setPip(false)
					}
				})
			} else if s.vp != nil {
				ui.Video(c, s.vp).Fill().Fit(ui.Contain)
			} else if bmp := a.img.get(s.v.Cover); bmp != nil {
				// The cover stands in until the first streamed frame arrives.
				ui.Image(c, bmp).Fill().Fit(ui.Contain).Opacity(0.5)
			}
			if a.paused && s.vp != nil {
				ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Children(func() {
					ui.Icon(c, iconPlay).Size(60, 60).TextColor(white).Opacity(0.85)
				})
			}
		})
		a.playbackStatus(c, s)
		at := time.Duration(0)
		if s.vp != nil { // the sound's clock, or the picture's for a video without
			at = s.at()
			if a.watch(s, at) { // after this frame, which is building the stage
				a.post(func() { a.lastNav = time.Time{}; a.step(1) })
			}
			a.loadDanmaku(s, at)
		}
		if !a.clear && a.danmakuLayer(c, s, box.Bounds().W, at) && !a.paused {
			c.AnimationFrame() // they fly
		} else if s.vp != nil && !a.paused {
			c.After(500 * time.Millisecond) // the time moves on
		}
		if !a.clear { // 清屏: the video alone, the bar left to bring the rest back
			a.caption(c, p, s)
			a.rail(c, p, s, box.Bounds().H)
		}
		a.controls(c, s, at)
	})
}

func (a *app) playbackStatus(c *ui.Context, s *slot) {
	err := s.err
	if s.vp != nil && s.vp.Err() != nil {
		err = s.vp.Err()
	}
	if err == nil && !s.loading {
		return
	}
	ui.Column(c.Key("playback-status")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(12).PassThrough().Children(func() {
		if err == nil {
			ui.Text(c, "视频载入中…").FontSize(14).TextColor(white)
			return
		}
		ui.Text(c, "视频暂时无法播放").FontSize(14).TextColor(white)
		if ui.Button(c.Key("retry-video"), "重新加载").Clicked() {
			a.release(s)
			a.prepare(s)
		}
	})
}

// flight is how long a bullet comment takes to cross the video.
const flight = 8 * time.Second

// danmakuLayer flies the bullet comments of s across the top of the video,
// w wide, each from its time on, in lanes by its place in the list. It
// reports whether any shows.
func (a *app) danmakuLayer(c *ui.Context, s *slot, w float32, at time.Duration) bool {
	if a.dmOff || w <= 0 {
		return false
	}
	const lanes, lane, size = 7, 30, 17
	shown := 0
	for i, d := range s.danmaku {
		age := at - d.At
		if age < 0 || age >= flight {
			continue
		}
		tw, _ := c.MeasureText(0, ui.Span{Text: d.Text, Size: size, Weight: 600})
		x := w - float32(age)/float32(flight)*(w+tw)
		ui.Text(c, d.Text).Absolute().Top(float32(14+i%lanes*lane)).Left(x).FontSize(size).FontWeight(600).
			TextColor(ui.RGBA(255, 255, 255, 0.92)).TextShadow(0, 1, 3, ui.RGBA(0, 0, 0, 0.7)).SingleLine()
		if shown++; shown == 40 { // a crowd past this reads as noise
			break
		}
	}
	return shown > 0
}

// caption is the author, the day and the text, over the foot of the video;
// the fade keeps it readable over a bright frame.
func (a *app) caption(c *ui.Context, p *page, s *slot) {
	ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(64, 96, 50, 18).Gap(6).
		Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.55), 180).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			name := "@" + s.v.Author
			b := ui.ButtonBase(c.Key("author")).FocusRing(false).Label(name)
			b.Children(func() {
				t := ui.Text(c, name).FontSize(16).FontWeight(700).TextColor(white).TextShadow(0, 1, 2, ui.RGBA(0, 0, 0, 0.4))
				if b.Hovered() {
					t.Underline()
				}
			})
			if b.Clicked() {
				a.openAuthor(p, s.v.Author, s.v.AuthorID)
			}
			when := ago(s.v.Created, time.Now())
			if s.v.Live != nil {
				when = "直播中"
			}
			ui.Text(c, "· "+when).FontSize(12.5).TextColor(ui.RGBA(255, 255, 255, 0.6))
		})
		ui.Text(c, s.v.Desc).FontSize(14).TextColor(ui.RGBA(255, 255, 255, 0.88)).MaxLines(2).TextShadow(0, 1, 2, ui.RGBA(0, 0, 0, 0.4))
		if m := s.v.Mix; m.ID != "" {
			text := "合集 · " + m.Name
			if m.Episode > 0 {
				text += fmt.Sprintf(" · 第%d集", m.Episode)
			}
			if m.Updated > 0 {
				text += fmt.Sprintf("，更新至第%d集", m.Updated)
			}
			b := ui.ButtonBase(c.Key("mix")).AlignSelf(ui.Start).Height(30).PaddingX(12).Gap(6).Radius(8).AlignItems(ui.Center).FocusRing(false).Label("合集："+m.Name).Margin(4, 0, 0, 0)
			bg := ui.RGBA(255, 255, 255, 0.14)
			if b.Hovered() {
				bg = ui.RGBA(255, 255, 255, 0.22)
			}
			b.Background(bg).Children(func() {
				ui.Icon(c, iconLayers).Size(14, 14).TextColor(white)
				ui.Text(c, text).FontSize(12.5).FontWeight(500).TextColor(white).SingleLine()
			})
			if b.Clicked() {
				a.openMix(p, s.v)
			}
		}
	})
}

// controls is the bar under the video, as Douyin's: play, the time, the
// bullet comments on the left; 连播 and 清屏, the definition, the speed, the
// volume, filling the window and the screen on the right; above it the
// progress, which a press or a drag moves the video to.
func (a *app) controls(c *ui.Context, s *slot, at time.Duration) {
	live := s.v.Live != nil
	var of time.Duration
	if s.ap != nil {
		of = s.ap.Duration()
	}
	if of <= 0 {
		of = s.v.Duration
	}
	ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.5), 180).Children(func() {
		if !live {
			a.progress(c, s, at, of)
		}
		ui.Row(c).Height(44).PaddingX(10).Gap(6).AlignItems(ui.Center).Children(func() {
			icon, label := iconPause, "暂停"
			if a.paused {
				icon, label = iconPlay, "播放"
			}
			if iconButton(c.Key("toggle"), icon, 16, white, label).Clicked() {
				a.toggle()
			}
			if live {
				ui.Box(c).Size(7, 7).Radius(4).Background(pal.accent)
				ui.Text(c, "直播中 · "+s.v.Live.Viewers+" 人在看").FontSize(12).FontWeight(500).TextColor(ui.RGBA(255, 255, 255, 0.8))
			} else {
				shown := at
				if a.dragging {
					shown = time.Duration(float64(of) * float64(a.dragAt))
				}
				ui.Text(c, clock(shown)+" / "+clock(of)).FontSize(12).FontWeight(500).TextColor(ui.RGBA(255, 255, 255, 0.85)).Margin(0, 6, 0, 2)
				if chip(c.Key("danmaku"), "弹幕", !a.dmOff).Clicked() {
					a.dmOff = !a.dmOff
				}
				a.danmakuInput(c, s)
			}
			ui.Box(c).Grow(1)
			if !live {
				if toggleChip(c.Key("next"), "连播", a.autoNext).Clicked() {
					a.autoNext = !a.autoNext
				}
			}
			if toggleChip(c.Key("clear"), "清屏", a.clear).Clicked() {
				a.clear = !a.clear
			}
			if !live {
				a.qualityMenu(c, s)
				a.speedMenu(c)
			}
			if !live {
				on := a.later[s.v.ID]
				b := iconButton(c.Key("later"), onOff(on, iconLaterOn, iconLater), 17, onOff(on, pal.accent, white), onOff(on, "移出稍后再看", "稍后再看"))
				if a.laterBusy[s.v.ID] {
					b.Opacity(0.5)
				} else if b.Clicked() {
					a.toggleLater(s.v)
				}
			}
			if iconButton(c.Key("pip"), iconPip, 17, onOff(a.pipOn, pal.accent, white), onOff(a.pipOn, "关闭小窗", "小窗")).Clicked() {
				a.setPip(!a.pipOn)
			}
			a.volumeButton(c)
			if iconButton(c.Key("theater"), onOff(a.theater && !a.fullScreen(), iconUnfill, iconFillWindow), 16, white, onOff(a.theater && !a.fullScreen(), "退出网页全屏", "网页全屏")).Clicked() {
				if a.fullScreen() {
					a.win.SetFullScreen(false)
				}
				a.theater = !a.theater || a.fullScreen()
			}
			full := a.fullScreen()
			if iconButton(c.Key("full"), onOff(full, iconShrink, iconExpand), 16, white, onOff(full, "退出全屏", "全屏")).Clicked() {
				a.setFullScreen(!full)
			}
		})
	})
}

// danmakuInput is where a bullet comment is written, as Douyin's: Enter or
// 发送 posts it where the video plays, for everyone watching to see.
func (a *app) danmakuInput(c *ui.Context, s *slot) {
	w, _ := c.Size()
	if w < 1180 { // a narrow window keeps the bar's other buttons
		return
	}
	field := ui.Row(c).Width(260).Height(30).Radius(8).PaddingStart(10).Gap(4).AlignItems(ui.Center).Background(ui.RGBA(255, 255, 255, 0.12)).Margin(0, 0, 0, 4)
	field.Children(func() {
		in := ui.TextInputBase(c.Key("dm-input"), &a.dmText).Bind(&a.dmBox).Grow(1).Placeholder("发一条友好的弹幕吧").FontSize(12.5).Label("弹幕内容")
		if in.Submitted() {
			a.sendBullet(s)
		}
		b := ui.ButtonBase(c.Key("dm-send")).Height(30).PaddingX(12).Radius(0, 8, 8, 0).Center().FocusRing(false).Label("发送弹幕")
		ready := strings.TrimSpace(a.dmText) != ""
		b.Background(onOff(ready, pal.accent, ui.RGBA(255, 255, 255, 0.08))).Children(func() {
			ui.Text(c, "发送").FontSize(12.5).FontWeight(600).TextColor(onOff(ready, white, ui.RGBA(255, 255, 255, 0.5)))
		})
		if b.Clicked() {
			a.sendBullet(s)
		}
	})
}

// pipView is 小窗: the current video alone, which drags the window; under
// the pointer a play button and a close one. Escape closes it.
func (a *app) pipView(c *ui.Context) {
	c.OnShortcut(0, ui.KeyEscape, func() { a.setPip(false); a.redraw() })
	c.OnShortcut(0, ui.KeySpace, func() { a.toggle(); a.redraw() })
	box := ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).DragWindow()
	box.Children(func() {
		var s *slot
		if p := a.cur(); p.plays() && p.idx < len(p.list) {
			s = p.list[p.idx]
		}
		if s == nil || s.vp == nil {
			box.Center().Children(func() {
				ui.Text(c, "在主窗口里选一个视频播放").FontSize(13).TextColor(pal.muted)
			})
			return
		}
		ui.Video(c, s.vp).Fill().Fit(ui.Contain)
		if box.Hovered() || a.paused {
			ui.Row(c).Absolute().Left(0).Right(0).Bottom(0).Height(40).PaddingX(8).Gap(4).AlignItems(ui.Center).
				Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.6), 180).Children(func() {
				if iconButton(c.Key("pip-toggle"), onOff(a.paused, iconPlay, iconPause), 16, white, onOff(a.paused, "播放", "暂停")).Clicked() {
					a.toggle()
					a.redraw()
				}
				ui.Text(c, "@"+s.v.Author).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.85)).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if iconButton(c.Key("pip-close"), iconClose, 15, white, "关闭小窗").Clicked() {
					a.setPip(false)
					a.redraw()
				}
			})
		}
	})
}

// redraw has both windows build again, after a change in the other one.
func (a *app) redraw() {
	if a.win != nil {
		a.win.Invalidate()
	}
	if a.pip != nil {
		a.pip.Invalidate()
	}
}

// fullScreen tells whether the window fills the screen.
func (a *app) fullScreen() bool { return a.win != nil && a.win.IsFullScreen() }

// setFullScreen has the video fill the screen, or not: the window full
// screen, and the video filling it.
func (a *app) setFullScreen(on bool) {
	if a.win != nil {
		a.win.SetFullScreen(on)
	}
	a.theater = on
}

// progress is the line of how far the video played, over the bar: thicker
// under the pointer, with a knob, which a press or a drag moves the video
// to, at its release.
func (a *app) progress(c *ui.Context, s *slot, at, of time.Duration) {
	bar := ui.Box(c.Key("progress")).Height(14).Cursor(ui.CursorPointer).Label("进度")
	w := bar.Bounds().W
	frac := func(x float32) float32 {
		if w <= 0 {
			return 0
		}
		return max(0, min(1, x/w))
	}
	bar.HandleInput(func(ev ui.InputEvent) bool {
		switch {
		case ev.Kind == ui.InputPointerDown && ev.Button == 0 && of > 0 && (s.dec != nil || s.live != nil && s.v.Live == nil):
			a.dragging, a.dragAt = true, frac(ev.X)
			return true
		case ev.Kind == ui.InputPointerMove && a.dragging:
			a.dragAt = frac(ev.X)
			return true
		case ev.Kind == ui.InputPointerUp && a.dragging:
			a.dragging = false
			if !ev.Canceled {
				a.seek(s, time.Duration(float64(of)*float64(frac(ev.X))))
			}
			return true
		}
		return false
	})
	f := float32(0)
	if of > 0 {
		f = min(1, float32(at)/float32(of))
	}
	if a.dragging {
		f = a.dragAt
	}
	hot := bar.Hovered() || a.dragging
	bar.Draw(func(p *ui.Painter, r ui.Rect) {
		h := float32(2)
		if hot {
			h = 4
		}
		y := r.Y + r.H - 6 - h/2
		p.Fill(ui.Rect{X: r.X, Y: y, W: r.W, H: h}, ui.RGBA(255, 255, 255, 0.22), h/2)
		p.Fill(ui.Rect{X: r.X, Y: y, W: r.W * f, H: h}, white, h/2)
		if hot {
			const k = 12
			p.Fill(ui.Rect{X: r.X + r.W*f - k/2, Y: y + h/2 - k/2, W: k, H: k}, white, k/2)
		}
	})
}

// toggleChip is a switch of the bar with its name, as Douyin's 连播 and 清屏.
func toggleChip(c *ui.Context, label string, on bool) ui.Element {
	b := ui.ButtonBase(c).Height(28).PaddingX(6).Gap(6).Radius(8).AlignItems(ui.Center).FocusRing(false).Label(label).Role(ui.RoleSwitch).Checked(on)
	if b.Hovered() {
		b.Background(ui.RGBA(255, 255, 255, 0.1))
	}
	return b.Children(func() {
		ui.Box(c).Width(26).Height(15).Radius(8).Background(onOff(on, pal.accent, ui.RGBA(255, 255, 255, 0.3))).Children(func() {
			ui.Box(c).Absolute().Top(2).Left(onOff[float32](on, 13, 2)).Size(11, 11).Radius(6).Background(white)
		})
		ui.Text(c, label).FontSize(12.5).FontWeight(500).TextColor(onOff(on, white, ui.RGBA(255, 255, 255, 0.75)))
	})
}

// barMenu is a menu that rises from a chip of the bar.
func barMenu(c *ui.Context, key string, anchor ui.Element, open *bool, width float32, items func()) {
	ui.PopoverBase(c.Key(key), anchor, open, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom).Margin(0, 0, 8, 0).Padding(5).Gap(1).Width(width).
			Radius(10).Background(ui.Hex("#2b2b2e")).Border(0.5, ui.RGBA(255, 255, 255, 0.14)).Shadow(0, 8, 24, -4, ui.RGBA(0, 0, 0, 0.4)).
			Role(ui.RoleMenu).FocusGroup(ui.Vertical).
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.96, Y: 4}, Exit: &ui.Motion{Scale: 0.98}})
		panel.Children(items)
	})
}

// choice is an item of a bar's menu, marked while chosen.
func choice(c *ui.Context, label string, on bool) ui.Element {
	b := ui.ButtonBase(c.Key(label)).Role(ui.RoleMenuItemRadio).Checked(on).Label(label).Height(30).PaddingX(10).Radius(7).
		AlignItems(ui.Center).Justify(ui.Center).FocusRing(false)
	if b.Hovered() || b.FocusVisible() {
		b.Background(pal.press)
	}
	return b.Children(func() {
		ui.Text(c, label).FontSize(13).FontWeight(onOff(on, 700, 500)).TextColor(onOff(on, pal.accent, pal.text))
	})
}

// qualityMenu chooses the definition: Douyin's pick (智能), or one the
// video comes in.
func (a *app) qualityMenu(c *ui.Context, s *slot) {
	cur := a.quality
	if cur == "" {
		cur = "智能"
	}
	b := chip(c.Key("quality"), cur, cur != "智能")
	b.Label("清晰度：" + cur)
	if b.Clicked() {
		a.qualityOpen = !a.qualityOpen
	}
	barMenu(c, "quality-menu", b, &a.qualityOpen, 96, func() {
		names := []string{"智能"}
		for _, q := range s.v.Qualities {
			names = append(names, q.Name)
		}
		for _, n := range names {
			if choice(c, n, n == cur).Clicked() {
				a.qualityOpen = false
				if n != cur {
					a.setQuality(n)
					a.toast = "清晰度：" + n
				}
			}
		}
	})
}

// speedMenu chooses the speed, as Douyin's: 2x down to 0.75x.
func (a *app) speedMenu(c *ui.Context) {
	label := "倍速"
	if a.speed != 0 && a.speed != 1 {
		label = strconv.FormatFloat(a.speed, 'f', -1, 64) + "x"
	}
	b := chip(c.Key("speed"), label, a.speed != 0 && a.speed != 1)
	b.Label("倍速：" + label)
	if b.Clicked() {
		a.speedOpen = !a.speedOpen
	}
	barMenu(c, "speed-menu", b, &a.speedOpen, 84, func() {
		for _, sp := range speeds {
			name := strconv.FormatFloat(sp, 'f', -1, 64) + "x"
			if choice(c, name, sp == a.speed || a.speed == 0 && sp == 1).Clicked() {
				a.speedOpen = false
				a.setSpeed(sp)
			}
		}
	})
}

// volumeButton shows the volume, muted or not; a click opens a slider, with
// a button that mutes and unmutes under it.
func (a *app) volumeButton(c *ui.Context) {
	icon := iconVolume
	if a.muted || a.volume == 0 {
		icon = iconMuted
	}
	b := iconButton(c.Key("volume"), icon, 17, white, "音量")
	if b.Clicked() {
		a.volumeOpen = !a.volumeOpen
	}
	barMenu(c, "volume-menu", b, &a.volumeOpen, 48, func() {
		ui.Text(c, fmt.Sprint(int(math.Round(a.loudness()*100)))).FontSize(12).TextColor(pal.text).AlignSelf(ui.Center).Margin(4, 0)
		const knob, track = 14, 4
		v := a.volume
		if a.muted {
			v = 0
		}
		sl := ui.SliderBase(c.Key("volume-slider"), &v, 0, 1).Vertical().Size(knob+12, 110).PaddingY(knob / 2).Step(0.01).
			AlignSelf(ui.Center).FocusRing(false).Label("音量大小")
		if sl.Changed() {
			a.setVolume(v, false)
		}
		sl.Draw(func(p *ui.Painter, r ui.Rect) {
			top, foot := r.Y+knob/2, r.Y+r.H-knob/2
			x := r.X + (r.W-track)/2
			y := foot - (foot-top)*float32(v)
			p.Fill(ui.Rect{X: x, Y: top - track/2, W: track, H: foot - top + track}, ui.RGBA(255, 255, 255, 0.2), track/2)
			p.Fill(ui.Rect{X: x, Y: y, W: track, H: foot - y + track/2}, pal.accent, track/2)
			p.Fill(ui.Rect{X: r.X + (r.W-knob)/2, Y: y - knob/2, W: knob, H: knob}, white, knob/2)
		})
		m := iconButton(c.Key("mute"), onOff(a.muted, iconMuted, iconVolume), 15, pal.text, onOff(a.muted, "取消静音", "静音")).AlignSelf(ui.Center)
		if m.Clicked() {
			if a.volume == 0 {
				a.volume = 0.5
			}
			a.setVolume(a.volume, !a.muted)
		}
	})
}

// rail is the column down the right of the video, as Douyin's: the author,
// the likes, the comments, the saves, sharing, listening, and more. A like
// or a save shows at once and goes to Douyin through ego lite, some seconds
// on; the button is dimmed till then. On a short stage the column shrinks
// to fit.
func (a *app) rail(c *ui.Context, p *page, s *slot, height float32) {
	icon, gap := float32(34), float32(14)
	if height > 0 && height < 680 {
		icon, gap = 28, 8
	}
	shadow := ui.RGBA(0, 0, 0, 0.45)
	ui.Column(c).Absolute().Right(14).Bottom(64).Gap(gap).AlignItems(ui.Center).Children(func() {
		ab := ui.ButtonBase(c.Key("avatar")).FocusRing(false).Label("作者主页").Margin(0, 0, 4, 0).
			Radius(999).Border(2, white)
		ab.Children(func() { a.avatar(c, s.v.Avatar, s.v.Author, icon+16) })
		if ab.Clicked() {
			a.openAuthor(p, s.v.Author, s.v.AuthorID)
		}
		under := func(text string) {
			ui.Text(c, text).FontSize(14).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.9)).TextShadow(0, 1, 3, shadow).SingleLine()
		}
		// shown is a count with its icon, which does nothing.
		action := func(key string, ic *ui.SVG, label, text string, color ui.Color) ui.Element {
			var b ui.Element
			ui.Column(c).Gap(2).AlignItems(ui.Center).Children(func() {
				b = iconButton(c.Key(key), ic, icon, color, label)
				if text != "" {
					under(text)
				}
			})
			return b
		}
		shares := count(s.v.Shares)
		if s.v.Live == nil { // a room has no counts, nor comments the client reads
			if b := action("like", iconHeartFill, onOff(s.v.Liked, "取消点赞", "点赞"), count(s.v.Likes), onOff(s.v.Liked, pal.accent, white)); s.liking {
				b.Opacity(0.55)
			} else if b.Clicked() {
				a.like(s)
			}
			color := white
			if a.sheet != nil {
				color = pal.accent
			}
			if action("comments", iconComment, "评论", count(s.v.Comments), color).Clicked() {
				if a.sheet != nil {
					a.sheet = nil
				} else {
					a.comments(s.v)
				}
			}
			if b := action("collects", iconStar, onOff(s.v.Saved, "取消收藏", "收藏"), count(s.v.Collects), onOff(s.v.Saved, starColor, white)); s.saving {
				b.Opacity(0.55)
			} else if b.Clicked() {
				a.collect(s)
			}
		} else {
			shares = "分享"
		}
		if s.v.Live != nil { // a room's link is all there is to share
			if action("share", iconShare, "分享：复制链接", shares, white).Clicked() {
				a.copyLink(s.v)
			}
		} else {
			share := action("share", iconShare, "分享", shares, onOff(a.shareOpen, pal.accent, white))
			if share.Clicked() {
				a.shareOpen, a.shareQuery = !a.shareOpen, ""
			}
			a.sharePanel(c, share, s)
		}
		color := white
		if a.listen {
			color = pal.accent
		}
		if action("listen", iconHeadphones, "听抖音", "听抖音", color).Clicked() {
			a.listen = !a.listen
		}
		more := action("more", iconMore, "更多", "", white)
		if more.Clicked() {
			a.moreOpen = !a.moreOpen
		}
		a.moreMenu(c, more, s)
	})
}

// sharePanel opens beside the rail's 分享, as Douyin's: a search, the
// accounts followed, each with a button that sends them the video in a
// direct message (through ego lite, some seconds on), and below, copying
// its link, saving it, and opening it on the web.
func (a *app) sharePanel(c *ui.Context, anchor ui.Element, s *slot) {
	ui.PopoverBase(c.Key("share-panel"), anchor, &a.shareOpen, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorStart, ui.AnchorEnd).Margin(0, 10, 0, 0).Width(340).Height(440).Padding(12, 0, 0, 0).
			Radius(14).Background(ui.Hex("#252632")).Border(0.5, ui.RGBA(255, 255, 255, 0.12)).Shadow(0, 10, 30, -6, ui.RGBA(0, 0, 0, 0.5)).
			Role(ui.RoleDialog).Label("分享").
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.96}, Exit: &ui.Motion{Scale: 0.98}})
		panel.Children(func() {
			ui.Row(c).Height(38).Margin(0, 12, 8, 12).PaddingX(12).Gap(8).Radius(10).AlignItems(ui.Center).Background(pal.hover).Children(func() {
				ui.Icon(c, iconSearch).Size(15, 15).TextColor(pal.faint)
				ui.TextInputBase(c.Key("share-search"), &a.shareQuery).Bind(&a.shareBox).Grow(1).Placeholder("搜索").FontSize(13.5).Label("搜索朋友")
			})
			ui.Text(c, "分享给朋友").FontSize(12).TextColor(pal.faint).Margin(4, 16, 6, 16)
			ui.Scroll(c.Key("share-list")).Grow(1).Children(func() {
				q := strings.ToLower(strings.TrimSpace(a.shareQuery))
				shown := 0
				for _, u := range a.follows {
					if q != "" && !strings.Contains(strings.ToLower(u.Name), q) {
						continue
					}
					shown++
					state := a.shared[s.v.ID+"|"+u.ID]
					ui.Row(c.Key("to:" + u.ID)).Height(56).PaddingX(16).Gap(12).AlignItems(ui.Center).Children(func() {
						a.avatar(c, u.Avatar, u.Name, 40)
						ui.Text(c, u.Name).FontSize(14).TextColor(pal.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
						label := map[string]string{"": "分享", "sending": "发送中…", "done": "已分享"}[state]
						b := ui.ButtonBase(c.Key("send")).Height(32).Width(72).Radius(8).Center().FocusRing(false).Label("分享给" + u.Name)
						bg := pal.accent
						switch {
						case state != "":
							bg = pal.press
						case b.Hovered():
							bg = ui.Hex("#ff4d6d")
						}
						b.Background(bg).Children(func() {
							ui.Text(c, label).FontSize(13).FontWeight(600).TextColor(white)
						})
						if state != "" {
							b.Disabled(true)
						} else if b.Clicked() {
							a.shareTo(s.v, u)
						}
					})
				}
				if shown == 0 {
					text := "没有搜索到朋友"
					if len(a.follows) == 0 {
						text = "还没有关注的人"
					}
					ui.Text(c, text).FontSize(12.5).TextColor(pal.faint).Margin(16, 16)
				}
			})
			ui.Row(c).Padding(10, 12, 12, 12).Gap(8).AlignItems(ui.Center).BorderWidth(0.5, 0, 0, 0).BorderColor(pal.hairline).Children(func() {
				link := ui.ButtonBase(c.Key("copy")).Grow(1).Height(38).Radius(10).Gap(6).Center().FocusRing(false).Label("复制链接")
				link.Background(onOff(link.Hovered(), pal.press, pal.hover)).Children(func() {
					ui.Icon(c, iconLink).Size(15, 15).TextColor(pal.text)
					ui.Text(c, "复制链接").FontSize(13.5).TextColor(pal.text)
				})
				if link.Clicked() {
					a.shareOpen = false
					a.copyLink(s.v)
				}
				square := func(key string, ic *ui.SVG, label string) ui.Element {
					b := ui.ButtonBase(c.Key(key)).Size(38, 38).Radius(10).Center().FocusRing(false).Label(label)
					return b.Background(onOff(b.Hovered(), pal.press, pal.hover)).Children(func() {
						ui.Icon(c, ic).Size(16, 16).TextColor(pal.text)
					})
				}
				if b := square("download", iconDownload, "下载"); s.vp == nil || s.saved == "保存中…" {
					b.Disabled(true).Opacity(0.4)
				} else if b.Clicked() {
					a.shareOpen = false
					a.save(s)
				}
				if square("web", iconGlobe, "在浏览器中打开").Clicked() {
					a.shareOpen = false
					if err := mygo.Shell.OpenExternal(videoURL(s.v)); err != nil {
						a.toast = "打不开浏览器：" + err.Error()
					}
				}
			})
		})
	})
}

// moreMenu drops from the rail's 更多: saving the video to Downloads,
// copying its link, and opening it on the web.
func (a *app) moreMenu(c *ui.Context, anchor ui.Element, s *slot) {
	ui.PopoverBase(c.Key("more-menu"), anchor, &a.moreOpen, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorStart, ui.AnchorEnd).Margin(0, 8, 0, 0).Padding(5).Gap(1).Width(200).
			Radius(12).Background(ui.Hex("#2b2b2e")).Border(0.5, ui.RGBA(255, 255, 255, 0.14)).Shadow(0, 8, 24, -4, ui.RGBA(0, 0, 0, 0.35)).
			Role(ui.RoleMenu).FocusGroup(ui.Vertical).Label("更多").
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.96}, Exit: &ui.Motion{Scale: 0.98}})
		label, ic := "下载到「下载」文件夹", iconDownload
		if s.v.Live != nil { // a stream has no file to keep
			label = ""
		}
		switch {
		case s.saved == "保存中…":
			label = "保存中…"
		case strings.HasPrefix(s.saved, "已存到"):
			label, ic = "已下载", iconCheck
		}
		if label != "" {
			if b := menuItem(c, ic, label); s.vp == nil || s.saved == "保存中…" {
				b.Disabled(true).Opacity(0.4)
			} else if b.Clicked() {
				a.moreOpen = false
				a.save(s)
			}
		}
		if menuItem(c, iconLink, "复制链接").Clicked() {
			a.moreOpen = false
			a.copyLink(s.v)
		}
		if menuItem(c, iconGlobe, "在浏览器中打开").Clicked() {
			a.moreOpen = false
			if err := mygo.Shell.OpenExternal(videoURL(s.v)); err != nil {
				a.toast = "打不开浏览器：" + err.Error()
			}
		}
	})
}

// menuItem is an item of a dropdown: an icon and its label, MySpotify's.
func menuItem(c *ui.Context, ic *ui.SVG, label string) ui.Element {
	b := ui.ButtonBase(c.Key(label)).Role(ui.RoleMenuItem).Label(label).Height(32).PaddingX(9).Gap(9).Radius(7).
		AlignItems(ui.Center).Justify(ui.Start).FocusRing(false)
	if b.Hovered() || b.FocusVisible() {
		b.Background(pal.press)
	}
	return b.Children(func() {
		ui.Icon(c, ic).Size(15, 15).TextColor(pal.muted)
		ui.Text(c, label).FontSize(13).TextColor(pal.text)
	})
}

// videoURL is a video's page on the web.
func videoURL(v dy.Video) string {
	if v.Live != nil {
		return "https://live.douyin.com/" + v.Live.WebRID
	}
	return "https://www.douyin.com/video/" + v.ID
}

// copyLink puts v's link on the clipboard, as Douyin's share does with no app
// to share to.
func (a *app) copyLink(v dy.Video) {
	if err := copyText(videoURL(v)); err != nil {
		a.toast = "复制失败：" + err.Error()
		return
	}
	a.toast = "已复制视频链接"
}

// copyText writes text to the clipboard; a test puts its own.
var copyText = func(text string) error { return mygo.Clipboard.Write(transfer.TextData(text)) }

// commentPanel is the comments of the video beside it, loaded a page at a time.
func (a *app) commentPanel(c *ui.Context, sh *sheet) {
	ui.Column(c.Key("comments")).Width(360).Radius(12).Background(pal.raised).Children(func() {
		ui.Row(c).Padding(10, 8, 6, 16).Gap(18).AlignItems(ui.Center).Children(func() {
			tab := func(label string, on bool) bool {
				b := ui.ButtonBase(c.Key(label)).Column().Gap(4).AlignItems(ui.Center).FocusRing(false).Label(label)
				b.Children(func() {
					color, weight := pal.muted, 500
					if on {
						color, weight = white, 700
					}
					ui.Text(c, label).FontSize(14).FontWeight(weight).TextColor(color)
					bar := ui.Box(c).Width(14).Height(2).Radius(1)
					if on {
						bar.Background(pal.accent)
					}
				})
				return b.Clicked()
			}
			if tab("评论 "+count(sh.total), !sh.related) {
				sh.related = false
			}
			if tab("相关推荐", sh.related) {
				sh.related = true
				if len(sh.rel) == 0 {
					a.moreRelated()
				}
			}
			ui.Box(c).Grow(1)
			if iconButton(c.Key("close"), iconClose, 16, pal.muted, "关闭评论").Clicked() {
				a.sheet = nil
			}
		})
		if sh.related {
			a.relatedList(c, sh)
			return
		}
		ui.Scroll(c.Key("comment-list")).Grow(1).Padding(0, 0, 12, 0).Children(func() {
			for i, cm := range sh.items {
				ui.Row(c.Key(fmt.Sprint(i))).Padding(10, 16).Gap(10).AlignItems(ui.Start).Children(func() {
					a.avatar(c, "", cm.Author, 30)
					ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						commentText(c, cm, 14)
						if cm.Replies > 0 {
							a.replyThread(c, sh, cm)
						}
					})
				})
			}
			ui.Row(c).Padding(12, 16).Justify(ui.Center).Children(func() {
				switch {
				case sh.fetching:
					ui.Text(c, "载入中…").FontSize(12).TextColor(pal.faint)
				case sh.msg != "":
					ui.Text(c, sh.msg).FontSize(12).TextColor(pal.faint)
				case !sh.done:
					if ui.Button(c.Key("more"), "更多评论").Clicked() {
						a.moreComments()
					}
				case len(sh.items) == 0:
					ui.Text(c, "还没有评论").FontSize(12).TextColor(pal.faint)
				}
			})
		})
	})
}

// commentText is a comment's author (and whom a reply answers), its text,
// and when, where from and its likes, in its text size.
func commentText(c *ui.Context, cm dy.Comment, size float32) {
	who := cm.Author
	if cm.ReplyTo != "" {
		who += " ▸ " + cm.ReplyTo
	}
	ui.Text(c, who).FontSize(size - 2).TextColor(pal.muted).SingleLine()
	ui.Text(c, cm.Text).FontSize(size).TextColor(pal.text)
	meta := ago(cm.Time, time.Now())
	if cm.Place != "" {
		meta += " · " + cm.Place
	}
	meta += "   ♥ " + count(cm.Likes)
	ui.Text(c, meta).FontSize(11).TextColor(pal.faint)
}

// replyThread is under a comment with replies, as Douyin's: the replies once
// opened, smaller, then 展开 N 条回复 to open them, 展开更多 for more, 收起.
func (a *app) replyThread(c *ui.Context, sh *sheet, cm dy.Comment) {
	th := sh.threads[cm.ID]
	open := th != nil && th.open
	if open {
		for j, r := range th.items {
			ui.Row(c.Key("r"+fmt.Sprint(j))).Gap(8).Margin(6, 0, 0, 0).AlignItems(ui.Start).Children(func() {
				a.avatar(c, "", r.Author, 22)
				ui.Column(c).Gap(3).Grow(1).Shrink(1).MinWidth(0).Children(func() { commentText(c, r, 13) })
			})
		}
		if th.msg != "" {
			ui.Text(c, th.msg).FontSize(11.5).TextColor(pal.faint)
		}
	}
	link := func(key, label string) bool {
		b := ui.ButtonBase(c.Key(key)).FocusRing(false).Label(label)
		b.Children(func() {
			ui.Text(c, label).FontSize(11.5).FontWeight(600).TextColor(onOff(b.Hovered(), pal.text, pal.muted))
		})
		return b.Clicked()
	}
	ui.Row(c).Gap(16).Margin(2, 0, 0, 0).Children(func() {
		switch {
		case !open:
			if link("open", fmt.Sprintf("展开 %d 条回复", cm.Replies)) {
				a.toggleReplies(cm)
			}
			return
		case th.fetching:
			ui.Text(c, "载入中…").FontSize(11.5).TextColor(pal.faint)
			return
		case !th.done && len(th.items) < cm.Replies:
			if link("more", "展开更多") {
				a.moreReplies(th)
			}
		}
		if link("close", "收起") {
			a.toggleReplies(cm)
		}
	})
}

// relatedList is the 相关推荐 tab: the videos Douyin relates to the one
// playing, each a cover beside its text, which plays it.
func (a *app) relatedList(c *ui.Context, sh *sheet) {
	ui.Scroll(c.Key("related-list")).Grow(1).Padding(4, 12, 12, 12).Gap(6).Children(func() {
		for i, v := range sh.rel {
			b := ui.ButtonBase(c.Key(fmt.Sprint(i))).Row().Padding(6).Gap(10).Radius(10).AlignItems(ui.Start).Justify(ui.Start).FocusRing(false).Label(v.Desc)
			if b.Hovered() {
				b.Background(pal.hover)
			}
			b.Children(func() {
				ui.Box(c).Width(128).AspectRatio(16.0 / 9).Radius(8).Clip().Background(pal.press).Children(func() {
					if bmp := a.img.get(v.Cover); bmp != nil {
						ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Cover)
					}
					ui.Text(c, clock(v.Duration)).Absolute().Right(5).Bottom(4).FontSize(10.5).FontWeight(600).TextColor(white).TextShadow(0, 1, 2, ui.RGBA(0, 0, 0, 0.6))
				})
				ui.Column(c).Gap(5).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					ui.Text(c, v.Desc).FontSize(13).TextColor(pal.text).MaxLines(2)
					ui.Text(c, "@"+v.Author+" · ♥ "+count(v.Likes)).FontSize(11.5).TextColor(pal.faint).SingleLine()
				})
			})
			if b.Clicked() {
				a.playRelated(i)
			}
		}
		ui.Row(c).Padding(12, 0).Justify(ui.Center).Children(func() {
			switch {
			case sh.relBusy:
				ui.Text(c, "载入中…").FontSize(12).TextColor(pal.faint)
			case sh.relMsg != "":
				ui.Text(c, sh.relMsg).FontSize(12).TextColor(pal.faint)
			case !sh.relDone && len(sh.rel) > 0:
				if ui.Button(c.Key("more-related"), "更多").Clicked() {
					a.moreRelated()
				}
			case len(sh.rel) == 0:
				ui.Text(c, "没有相关推荐").FontSize(12).TextColor(pal.faint)
			}
		})
	})
}

// byline is a card's author and when the video came out; a live room has
// no date.
func byline(v dy.Video) string {
	if v.Live != nil {
		return "@" + v.Author
	}
	return "@" + v.Author + " · " + ago(v.Created, time.Now())
}

// starColor is a saved video's star, as Douyin's.
var starColor = ui.Hex("#ffb802")

// onOff is yes when on, else no.
func onOff[T any](on bool, yes, no T) T {
	if on {
		return yes
	}
	return no
}

// liveBadge marks a live room's cover, at its top left.
func liveBadge(c *ui.Context) {
	ui.Row(c).Absolute().Top(8).Left(8).Height(20).PaddingX(7).Gap(4).Radius(5).AlignItems(ui.Center).Background(pal.accent).Children(func() {
		ui.Box(c).Size(5, 5).Radius(3).Background(white)
		ui.Text(c, "直播中").FontSize(11).FontWeight(700).TextColor(white)
	})
}

// chip is a small text button of the control bar, bright while on.
func chip(c *ui.Context, label string, on bool) ui.Element {
	b := ui.ButtonBase(c).Height(28).PaddingX(10).Radius(8).Center().FocusRing(false).Label(label)
	if b.Hovered() {
		b.Background(ui.RGBA(255, 255, 255, 0.12))
	}
	color := ui.RGBA(255, 255, 255, 0.55)
	if on {
		color = white
	}
	return b.Children(func() { ui.Text(c, label).FontSize(12.5).FontWeight(600).TextColor(color) })
}

// notePanel is the notifications beside the page, loaded a page at a time:
// who, what they did, and what it was about.
func (a *app) notePanel(c *ui.Context, n *notes) {
	ui.Column(c.Key("notes")).Width(360).Radius(12).Background(pal.raised).Children(func() {
		ui.Row(c).Padding(10, 8, 6, 16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "通知").FontSize(14).FontWeight(600).TextColor(pal.text).Grow(1)
			if iconButton(c.Key("close"), iconClose, 16, pal.muted, "关闭通知").Clicked() {
				a.notes = nil
			}
		})
		ui.Scroll(c.Key("note-list")).Grow(1).Padding(0, 0, 12, 0).Children(func() {
			for i, it := range n.items {
				ui.Row(c.Key(fmt.Sprint(i))).Padding(10, 16).Gap(10).AlignItems(ui.Start).Children(func() {
					a.avatar(c, it.User.Avatar, it.User.Name, 34)
					ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							b := ui.ButtonBase(c.Key("who")).FocusRing(false).Label(it.User.Name).Shrink(1).MinWidth(0)
							b.Children(func() {
								t := ui.Text(c, it.User.Name).FontSize(13).FontWeight(600).TextColor(pal.text).SingleLine()
								if b.Hovered() {
									t.Underline()
								}
							})
							if b.Clicked() {
								a.openAuthor(a.cur(), it.User.Name, it.User.ID)
							}
							ui.Text(c, it.Action).FontSize(12.5).TextColor(pal.muted).SingleLine()
						})
						if it.Text != "" {
							ui.Text(c, it.Text).FontSize(13).TextColor(pal.text).MaxLines(2)
						}
						ui.Text(c, ago(it.Time, time.Now())).FontSize(11).TextColor(pal.faint)
					})
				})
			}
			ui.Row(c).Padding(12, 16).Justify(ui.Center).Children(func() {
				switch {
				case n.fetching:
					ui.Text(c, "载入中…").FontSize(12).TextColor(pal.faint)
				case n.msg != "":
					ui.Text(c, n.msg).FontSize(12).TextColor(pal.faint)
				case !n.done:
					if ui.Button(c.Key("more"), "更多通知").Clicked() {
						a.moreNotes()
					}
				case len(n.items) == 0:
					ui.Text(c, "没有通知").FontSize(12).TextColor(pal.faint)
				}
			})
		})
	})
}

// dmPanel is the inbox beside the page: the chats, each its peer and last
// message, and a chat's messages once one is clicked, the user's on the
// right. It only reads: replies are for the Douyin app.
func (a *app) dmPanel(c *ui.Context, d *dms) {
	ui.Column(c.Key("dms")).Width(360).Radius(12).Background(pal.raised).Children(func() {
		var open *dy.Chat
		if d.open >= 0 && d.open < len(d.chats) {
			open = &d.chats[d.open]
		}
		ui.Row(c).Padding(10, 8, 6, 8).Gap(2).AlignItems(ui.Center).Children(func() {
			title := "私信"
			if open != nil {
				if iconButton(c.Key("inbox"), iconBack, 16, pal.muted, "返回私信列表").Clicked() {
					d.open = -1
				}
				title = open.Peer.Name
			}
			ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(pal.text).Grow(1).SingleLine().Shrink(1).MinWidth(0).Margin(0, 8)
			if iconButton(c.Key("close"), iconClose, 16, pal.muted, "关闭私信").Clicked() {
				a.dms = nil
			}
		})
		if open != nil {
			a.chatView(c, open)
		} else {
			ui.Scroll(c.Key("chat-list")).Grow(1).Padding(0, 0, 8, 0).Children(func() {
				for i, ch := range d.chats {
					b := ui.ButtonBase(c.Key(ch.ID)).Row().Padding(10, 16).Gap(10).AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(ch.Peer.Name)
					if b.Hovered() {
						b.Background(pal.hover)
					}
					b.Children(func() {
						a.avatar(c, ch.Peer.Avatar, ch.Peer.Name, 40)
						ui.Column(c).Gap(3).Grow(1).Shrink(1).MinWidth(0).Children(func() {
							var m dy.Message
							if len(ch.Msgs) > 0 {
								m = ch.Msgs[len(ch.Msgs)-1]
							}
							ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Text(c, ch.Peer.Name).FontSize(13.5).FontWeight(600).TextColor(pal.text).SingleLine().Shrink(1).MinWidth(0).Grow(1)
								ui.Text(c, ago(m.Time, time.Now())).FontSize(11).TextColor(pal.faint)
							})
							ui.Text(c, m.Text).FontSize(12.5).TextColor(pal.muted).SingleLine()
						})
					})
					if b.Clicked() {
						d.open = i
					}
				}
				ui.Row(c).Padding(12, 16).Justify(ui.Center).Children(func() {
					switch {
					case d.loading:
						ui.Text(c, "载入中…").FontSize(12).TextColor(pal.faint)
					case d.msg != "":
						ui.Text(c, d.msg).FontSize(12).TextColor(pal.faint)
					case len(d.chats) == 0:
						ui.Text(c, "没有私信").FontSize(12).TextColor(pal.faint)
					}
				})
			})
		}
		ui.Text(c, "只读，不会标为已读；回复请用抖音 App").FontSize(11).TextColor(pal.faint).Margin(6, 16, 12, 16)
	})
}

// chatView is a chat's messages as bubbles, the user's on the right in the
// accent, the peer's on the left, Douyin's tips small in the middle.
func (a *app) chatView(c *ui.Context, ch *dy.Chat) {
	ui.Scroll(c.Key("chat:"+ch.ID)).Grow(1).Padding(8, 14).Gap(10).Children(func() {
		for i, m := range ch.Msgs {
			if m.Tip {
				ui.Text(c.Key(fmt.Sprint(i)), m.Text).FontSize(11).TextColor(pal.faint).TextAlign(ui.Center).Margin(4, 16)
				continue
			}
			row := ui.Row(c.Key(fmt.Sprint(i))).Justify(ui.Start)
			bg := pal.hover
			if m.Mine {
				row.Justify(ui.End)
				bg = pal.accent
			}
			row.Children(func() {
				ui.Column(c).MaxWidth(250).Padding(8, 12).Radius(12).Background(bg).Gap(3).Children(func() {
					ui.Text(c, m.Text).FontSize(13.5).TextColor(white).Selectable()
					ui.Text(c, ago(m.Time, time.Now())).FontSize(10).TextColor(ui.RGBA(255, 255, 255, 0.55))
				})
			})
		}
	})
}

// iconButton is a borderless round button showing an icon, which a soft
// circle marks under the pointer and which gives a little as it is
// pressed: MySpotify's.
func iconButton(c *ui.Context, icon *ui.SVG, size float32, color ui.Color, label string) ui.Element {
	b := ui.ButtonBase(c).Size(size+16, size+16).Center().Radius(999).Label(label).TextColor(color).FocusRing(false)
	bg := ui.Transparent
	if b.Hovered() {
		bg = ui.RGBA(128, 128, 128, 0.2)
	}
	b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
	pose := ui.Pose{}
	if b.Pressed() {
		pose.Scale = 0.88
	}
	b.AnimatePose(pose, ui.Snappy)
	return b.Children(func() { ui.Icon(c, icon).Size(size, size) })
}

// ago is when something was posted, as Douyin says it: minutes, hours or
// days ago within a week, then the day, with the year if not this one.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero() || t.Unix() <= 0:
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%d分钟前", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时前", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d天前", int(d.Hours()/24))
	case t.Year() == now.Year():
		return fmt.Sprintf("%d月%d日", t.Month(), t.Day())
	default:
		return fmt.Sprintf("%d年%d月%d日", t.Year(), t.Month(), t.Day())
	}
}

// clock is a length as 07:24, or 1:02:03 past an hour.
func clock(d time.Duration) string {
	s := int(max(0, d).Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func count(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1f万", float64(n)/10000)
	}
	return fmt.Sprint(n)
}

// Icons on a 24-point grid in the color of the text, as MySpotify's: the
// play marks, the heart and the star filled, the rest in a 1.75-point stroke.
func strokeIcon(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`))
}

func fillIcon(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor">` + paths + `</svg>`))
}

const heartPath = `<path d="M19 14c1.5-1.5 3-3.2 3-5.5A5.5 5.5 0 0 0 16.5 3c-1.8 0-3 .5-4.5 2-1.5-1.5-2.7-2-4.5-2A5.5 5.5 0 0 0 2 8.5c0 2.3 1.5 4 3 5.5l7 7z"/>`

var (
	iconPlay       = fillIcon(`<path d="M7 4.6v14.8c0 .8.9 1.3 1.6.9l12-7.4c.6-.4.6-1.3 0-1.7l-12-7.4C7.9 3.3 7 3.8 7 4.6z"/>`)
	iconPause      = fillIcon(`<rect x="5.5" y="4" width="4.5" height="16" rx="1.4"/><rect x="14" y="4" width="4.5" height="16" rx="1.4"/>`)
	iconHeartFill  = fillIcon(heartPath)
	iconHeart      = strokeIcon(heartPath)
	iconStar       = fillIcon(`<path d="M12 2.8l2.8 5.7 6.3.9-4.6 4.4 1.1 6.3L12 17.1l-5.6 3 1.1-6.3L2.9 9.4l6.3-.9z"/>`)
	iconSearch     = strokeIcon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>`)
	iconGrid       = strokeIcon(`<rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/>`)
	iconSpark      = strokeIcon(`<path d="M12 3v4"/><path d="M12 17v4"/><path d="M3 12h4"/><path d="M17 12h4"/><path d="m6 6 2.5 2.5"/><path d="m15.5 15.5 2.5 2.5"/><path d="m6 18 2.5-2.5"/><path d="m15.5 8.5 2.5-2.5"/>`)
	iconBack       = strokeIcon(`<path d="m15 18-6-6 6-6"/>`)
	iconComment    = fillIcon(`<path d="M12 3C6.5 3 2 6.8 2 11.5c0 2.6 1.4 4.9 3.6 6.5L5 21.5l4-2c.9.2 1.9.3 3 .3 5.5 0 10-3.8 10-8.5S17.5 3 12 3z"/>`)
	iconDownload   = strokeIcon(`<path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/>`)
	iconCheck      = strokeIcon(`<path d="M20 6 9 17l-5-5"/>`)
	iconCompass    = strokeIcon(`<circle cx="12" cy="12" r="9"/><path d="m15.5 8.5-2 5-5 2 2-5z"/>`)
	iconFollow     = strokeIcon(`<circle cx="9" cy="8" r="4"/><path d="M2 20c0-3.5 3-6 7-6 1.3 0 2.5.3 3.5.8"/><path d="m15 17 2 2 4-4"/>`)
	iconFriends    = strokeIcon(`<circle cx="9" cy="8" r="4"/><path d="M2 20c0-3.5 3-6 7-6s7 2.5 7 6"/><path d="M16 4.2a4 4 0 0 1 0 7.6"/><path d="M19 14.5c1.8.9 3 2.9 3 5.5"/>`)
	iconUser       = strokeIcon(`<circle cx="12" cy="8" r="4"/><path d="M4 21c0-4 3.6-7 8-7s8 3 8 7"/>`)
	iconLive       = strokeIcon(`<circle cx="12" cy="12" r="2"/><path d="M8.5 8.5a5 5 0 0 0 0 7"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M5.6 5.6a9 9 0 0 0 0 12.8"/><path d="M18.4 5.6a9 9 0 0 1 0 12.8"/>`)
	iconDrama      = strokeIcon(`<rect x="4" y="3" width="16" height="18" rx="2"/><path d="m10 9 5 3-5 3z"/>`)
	iconLater      = strokeIcon(`<path d="M4 6h12"/><path d="M4 12h8"/><path d="M4 18h6"/><circle cx="17" cy="16" r="4"/><path d="M17 14.5V16l1 1"/>`)
	iconLaterOn    = strokeIcon(`<path d="M4 6h12"/><path d="M4 12h8"/><path d="M4 18h6"/><path d="m14 16 2 2 4-4"/>`)
	iconPip        = strokeIcon(`<rect x="3" y="5" width="18" height="14" rx="2"/><rect x="12" y="12" width="7" height="5" rx="1"/>`)
	iconVolume     = strokeIcon(`<path d="M11 5 6 9H3v6h3l5 4z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M18.5 5.5a9 9 0 0 1 0 13"/>`)
	iconMuted      = strokeIcon(`<path d="M11 5 6 9H3v6h3l5 4z"/><path d="m22 9-6 6"/><path d="m16 9 6 6"/>`)
	iconFillWindow = strokeIcon(`<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M8 10V8h2"/><path d="M16 14v2h-2"/>`)
	iconUnfill     = strokeIcon(`<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M10 8v2H8"/><path d="M14 16v-2h2"/>`)
	iconExpand     = strokeIcon(`<path d="M4 9V4h5"/><path d="M20 9V4h-5"/><path d="M4 15v5h5"/><path d="M20 15v5h-5"/>`)
	iconShrink     = strokeIcon(`<path d="M9 4v5H4"/><path d="M15 4v5h5"/><path d="M9 20v-5H4"/><path d="M15 20v-5h5"/>`)
	iconLayers     = strokeIcon(`<path d="m12 3 9 5-9 5-9-5z"/><path d="m3 13 9 5 9-5"/>`)
	iconShare      = fillIcon(`<path d="M13.5 4.5v3.6C7.2 8.8 3.6 12.7 3 19.5c2.3-3.2 5.4-4.9 10.5-5v3.9L21 11z"/>`)
	iconHeadphones = strokeIcon(`<path d="M3 18v-6a9 9 0 0 1 18 0v6"/><path d="M21 19a2 2 0 0 1-2 2h-1a2 2 0 0 1-2-2v-3a2 2 0 0 1 2-2h3zM3 19a2 2 0 0 0 2 2h1a2 2 0 0 0 2-2v-3a2 2 0 0 0-2-2H3z"/>`)
	iconMore       = fillIcon(`<circle cx="5" cy="12" r="2"/><circle cx="12" cy="12" r="2"/><circle cx="19" cy="12" r="2"/>`)
	iconLink       = strokeIcon(`<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>`)
	iconGlobe      = strokeIcon(`<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3a14 14 0 0 1 0 18 14 14 0 0 1 0-18z"/>`)
	iconChat       = strokeIcon(`<path d="M22 2 11 13"/><path d="M22 2 15 22l-4-9-9-4z"/>`)
	iconBell       = strokeIcon(`<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>`)
	iconClose      = strokeIcon(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`)
)
