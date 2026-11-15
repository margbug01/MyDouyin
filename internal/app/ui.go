package app

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
)

// The window is laid out as Douyin's own, with less in it: a narrow sidebar
// (精选, 推荐, the accounts followed), the search field centered along the
// top, and under it a grid of covers or the video playing. The components
// are MySpotify's: its buttons, its rows, its type sizes.

const (
	sidebarWidth = 200
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
	if !c.Focused(a.searchBox) {
		// Space pauses wherever the focus is, as in MySpotify: on the box
		// around everything it comes before a button the pointer focused,
		// which would take it as a click; on the window, as nothing has
		// the focus.
		root.OnShortcut(0, ui.KeySpace, a.toggle)
		c.OnShortcut(0, ui.KeySpace, a.toggle)
	}
	p := a.cur()
	root.Children(func() {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(sidebarWidth).Children(func() { a.sidebar(c) })
			ui.Column(c.Key("main")).Grow(1).Children(func() {
				a.topBar(c, p)
				if p == a.picks && !p.plays() {
					a.tabsRow(c)
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
					case a.sheet != nil && p.plays():
						a.commentPanel(c, a.sheet)
					}
				})
			})
		})
	})
	if a.toast != "" {
		c.Toast(a.toast)
		a.toast = ""
	}
}

func (a *app) sidebar(c *ui.Context) {
	// Below the window buttons, which sit over it.
	ui.Column(c).Fill().Padding(52, 12, 14, 12).Gap(4).DragWindow().Children(func() {
		ui.Text(c, "抖音").FontSize(20).FontWeight(800).TextColor(white).Margin(0, 10, 14, 10)
		if a.navItem(c, iconGrid, "精选", a.pages[0] == a.picks).Clicked() {
			a.root(a.picks)
		}
		if a.navItem(c, iconSpark, "推荐", a.pages[0] == a.feed).Clicked() {
			a.root(a.feed)
		}
		ui.Box(c).Height(1).Background(pal.hairline).Margin(10, 10)
		ui.Scroll(c.Key("follows")).Grow(1).Margin(0, -6).Padding(0, 6).Gap(2).Children(func() {
			if len(a.follows) > 0 || a.followMsg != "" {
				ui.Text(c, "关注").FontSize(11.5).FontWeight(600).TextColor(pal.faint).Margin(4, 10, 6, 10)
			}
			if a.followMsg != "" {
				ui.Text(c, a.followMsg).FontSize(12).TextColor(pal.faint).Margin(0, 10)
			}
			cur := a.cur().title
			for _, u := range a.follows {
				b := a.listItem(c.Key("u:"+u.ID), cur == "@"+u.Name, u.Name)
				b.Children(func() {
					a.avatar(c, u.Avatar, u.Name, 24)
					ui.Text(c, u.Name).FontSize(13).TextColor(pal.text).SingleLine().Shrink(1).MinWidth(0)
				})
				if b.Clicked() {
					a.openAuthor(a.cur(), u.Name, u.ID)
				}
			}
		})
		if a.me.Name != "" {
			ui.Row(c).Height(40).PaddingX(8).Gap(10).AlignItems(ui.Center).Children(func() {
				a.avatar(c, a.me.Avatar, a.me.Name, 28)
				ui.Text(c, a.me.Name).FontSize(13).FontWeight(600).TextColor(pal.text).SingleLine().Shrink(1).MinWidth(0)
			})
		}
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
		w, _ := c.Size()
		fieldW := min(460, w-sidebarWidth-2*260)
		left := (w - sidebarWidth - fieldW) / 2
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
	ui.ScrollHorizontal(c.Key("tabs")).Height(40).PaddingX(24).Gap(22).AlignItems(ui.Center).Children(func() {
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
	inner := w - sidebarWidth - 24 - 16 // the grid's padding, the row's
	if a.notes != nil {
		inner -= 360 + 16
	}
	cols := max(2, int((inner+16)/(260+16)))
	colW := (inner - 16*float32(cols-1)) / float32(cols)
	ui.Scroll(c.Key("grid:"+p.title)).TrackScroll(&p.scroll).Grow(1).Padding(4, 0, 32, 24).Children(func() {
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
			ui.Row(c).Absolute().Left(0).Right(0).Bottom(0).Padding(18, 10, 7, 10).AlignItems(ui.Center).
				Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.55), 180).Children(func() {
				ui.Icon(c, iconHeart).Size(13, 13).TextColor(white)
				ui.Text(c, count(v.Likes)).FontSize(12).FontWeight(500).TextColor(white).Margin(0, 0, 0, 4).Grow(1)
				ui.Text(c, clock(v.Duration)).FontSize(12).FontWeight(500).TextColor(white)
			})
		})
		title := ui.Text(c, v.Desc).FontSize(14).TextColor(pal.text).MaxLines(2)
		if b.Hovered() {
			title.TextColor(white)
		}
		ui.Text(c, "@"+v.Author+" · "+ago(v.Created, time.Now())).FontSize(12).TextColor(pal.faint).SingleLine()
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
		ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(60, 18, 16, 18).Gap(6).
			Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.6), 180).Children(func() {
			ui.Text(c, "@"+v.Author+" · "+ago(v.Created, time.Now())).FontSize(15).FontWeight(700).TextColor(white)
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
			if s.vp != nil {
				ui.Video(c, s.vp).Fill().Fit(ui.Contain)
			} else if bmp := a.img.get(s.v.Cover); bmp != nil {
				// The cover stands in while the video downloads.
				ui.Image(c, bmp).Fill().Fit(ui.Contain).Opacity(0.5)
			}
			if a.paused && s.vp != nil {
				ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Children(func() {
					ui.Icon(c, iconPlay).Size(60, 60).TextColor(white).Opacity(0.85)
				})
			}
		})
		at := time.Duration(0)
		if s.ap != nil {
			at = s.ap.Position()
			if a.watch(s, at) { // after this frame, which is building the stage
				a.post(func() { a.lastNav = time.Time{}; a.step(1) })
			}
			a.loadDanmaku(s, at)
		}
		if a.danmakuLayer(c, s, box.Bounds().W, at) && !a.paused {
			c.AnimationFrame() // they fly
		} else if s.ap != nil && !a.paused {
			c.After(500 * time.Millisecond) // the time moves on
		}
		a.caption(c, p, s)
		a.rail(c, p, s)
		a.controls(c, s, at)
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
			ui.Text(c, "· "+ago(s.v.Created, time.Now())).FontSize(12.5).TextColor(ui.RGBA(255, 255, 255, 0.6))
		})
		ui.Text(c, s.v.Desc).FontSize(14).TextColor(ui.RGBA(255, 255, 255, 0.88)).MaxLines(2).TextShadow(0, 1, 2, ui.RGBA(0, 0, 0, 0.4))
	})
}

// controls is the bar along the bottom of the video: play and pause, the
// time, and how far it has played, a line over it. Not seekable: the video
// decoder can only start over.
func (a *app) controls(c *ui.Context, s *slot, at time.Duration) {
	var of time.Duration
	if s.ap != nil {
		of = s.ap.Duration()
	}
	if of <= 0 {
		of = s.v.Duration
	}
	ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Children(func() {
		ui.Box(c).Height(2).Background(ui.RGBA(255, 255, 255, 0.18)).Children(func() {
			if of > 0 {
				f := min(1, float32(at)/float32(of))
				ui.Box(c).Absolute().Top(0).Left(0).Bottom(0).WidthPercent(f * 100).Background(white)
			}
		})
		ui.Row(c).Height(40).PaddingX(8).Gap(4).AlignItems(ui.Center).Children(func() {
			icon, label := iconPause, "暂停"
			if a.paused {
				icon, label = iconPlay, "播放"
			}
			if iconButton(c.Key("toggle"), icon, 16, white, label).Clicked() {
				a.toggle()
			}
			ui.Text(c, clock(at)+" / "+clock(of)).FontSize(12).FontWeight(500).TextColor(ui.RGBA(255, 255, 255, 0.8)).Grow(1)
			if chip(c.Key("danmaku"), "弹幕", !a.dmOff).Clicked() {
				a.dmOff = !a.dmOff
			}
			if chip(c.Key("next"), "连播", a.autoNext).Clicked() {
				a.autoNext = !a.autoNext
			}
			sl := "倍速"
			if a.speed != 0 && a.speed != 1 {
				sl = strconv.FormatFloat(a.speed, 'f', -1, 64) + "x"
			}
			if chip(c.Key("speed"), sl, a.speed != 0 && a.speed != 1).Clicked() {
				i := 0
				for j, sp := range speeds {
					if sp == a.speed {
						i = j + 1
					}
				}
				a.setSpeed(speeds[i%len(speeds)])
			}
		})
	})
}

// rail is the column of actions down the right of the video, as Douyin's:
// the author, the likes, the comments, the saves and the download.
func (a *app) rail(c *ui.Context, p *page, s *slot) {
	ui.Column(c).Absolute().Right(12).Bottom(64).Gap(12).AlignItems(ui.Center).Children(func() {
		ab := ui.ButtonBase(c.Key("avatar")).FocusRing(false).Label("作者主页").Margin(0, 0, 8, 0).
			Radius(999).Border(2, white)
		ab.Children(func() { a.avatar(c, s.v.Avatar, s.v.Author, 44) })
		if ab.Clicked() {
			a.openAuthor(p, s.v.Author, s.v.AuthorID)
		}
		action := func(key string, icon *ui.SVG, label, count string, color ui.Color) ui.Element {
			var b ui.Element
			ui.Column(c).Gap(0).AlignItems(ui.Center).Children(func() {
				b = iconButton(c.Key(key), icon, 28, color, label)
				ui.Text(c, count).FontSize(12).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.86)).TextShadow(0, 1, 2, ui.RGBA(0, 0, 0, 0.4))
			})
			return b
		}
		action("likes", iconHeartFill, "点赞数", count(s.v.Likes), white)
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
		action("collects", iconStar, "收藏数", count(s.v.Collects), white)
		label, icon := "下载", iconDownload
		switch {
		case s.saved == "保存中…":
			label = "保存中"
		case strings.HasPrefix(s.saved, "已存到"):
			label, icon = "已下载", iconCheck
		}
		b := action("save", icon, "下载到「下载」文件夹", label, white)
		if s.vp == nil || s.saved == "保存中…" {
			b.Disabled(true).Opacity(0.4)
		} else if b.Clicked() {
			a.save(s)
		}
	})
}

// commentPanel is the comments of the video beside it, loaded a page at a time.
func (a *app) commentPanel(c *ui.Context, sh *sheet) {
	ui.Column(c.Key("comments")).Width(360).Radius(12).Background(pal.raised).Children(func() {
		ui.Row(c).Padding(10, 8, 6, 16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, count(sh.total)+" 条评论").FontSize(14).FontWeight(600).TextColor(pal.text).Grow(1)
			if iconButton(c.Key("close"), iconClose, 16, pal.muted, "关闭评论").Clicked() {
				a.sheet = nil
			}
		})
		ui.Scroll(c.Key("comment-list")).Grow(1).Padding(0, 0, 12, 0).Children(func() {
			for _, cm := range sh.items {
				ui.Row(c).Padding(10, 16).Gap(10).AlignItems(ui.Start).Children(func() {
					a.avatar(c, "", cm.Author, 30)
					ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						ui.Text(c, cm.Author).FontSize(12).TextColor(pal.muted).SingleLine()
						ui.Text(c, cm.Text).FontSize(14).TextColor(pal.text)
						meta := ago(cm.Time, time.Now())
						if cm.Place != "" {
							meta += " · " + cm.Place
						}
						meta += "   ♥ " + count(cm.Likes)
						if cm.Replies > 0 {
							meta += fmt.Sprintf("   %d 条回复", cm.Replies)
						}
						ui.Text(c, meta).FontSize(11).TextColor(pal.faint)
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
	iconPlay      = fillIcon(`<path d="M7 4.6v14.8c0 .8.9 1.3 1.6.9l12-7.4c.6-.4.6-1.3 0-1.7l-12-7.4C7.9 3.3 7 3.8 7 4.6z"/>`)
	iconPause     = fillIcon(`<rect x="5.5" y="4" width="4.5" height="16" rx="1.4"/><rect x="14" y="4" width="4.5" height="16" rx="1.4"/>`)
	iconHeartFill = fillIcon(heartPath)
	iconHeart     = strokeIcon(heartPath)
	iconStar      = fillIcon(`<path d="M12 2.8l2.8 5.7 6.3.9-4.6 4.4 1.1 6.3L12 17.1l-5.6 3 1.1-6.3L2.9 9.4l6.3-.9z"/>`)
	iconSearch    = strokeIcon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>`)
	iconGrid      = strokeIcon(`<rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/>`)
	iconSpark     = strokeIcon(`<path d="M12 3v4"/><path d="M12 17v4"/><path d="M3 12h4"/><path d="M17 12h4"/><path d="m6 6 2.5 2.5"/><path d="m15.5 15.5 2.5 2.5"/><path d="m6 18 2.5-2.5"/><path d="m15.5 8.5 2.5-2.5"/>`)
	iconBack      = strokeIcon(`<path d="m15 18-6-6 6-6"/>`)
	iconComment   = fillIcon(`<path d="M12 3C6.5 3 2 6.8 2 11.5c0 2.6 1.4 4.9 3.6 6.5L5 21.5l4-2c.9.2 1.9.3 3 .3 5.5 0 10-3.8 10-8.5S17.5 3 12 3z"/>`)
	iconDownload  = strokeIcon(`<path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/>`)
	iconCheck     = strokeIcon(`<path d="M20 6 9 17l-5-5"/>`)
	iconBell      = strokeIcon(`<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>`)
	iconClose     = strokeIcon(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`)
)
