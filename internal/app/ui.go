package app

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// The window follows MySpotify's: a sidebar of 232 points under the
// system's material with the search field, the pages and the accounts
// followed, and the content beside it on the page's background.

const sidebarWidth = 232

// topBarHeight is the strip along the top of the content, which drags the
// window and holds the way back.
const topBarHeight = 40

// palette holds the app's colors, for the light and the dark appearance:
// MySpotify's.
type palette struct {
	dark                                     bool
	bg, sidebar, text, muted, faint          ui.Color
	hairline, hover, press, accent, onAccent ui.Color
}

func paletteFor(t *ui.Theme) palette {
	if t.Dark {
		return palette{
			dark:     true,
			bg:       ui.Hex("#141416"),
			sidebar:  ui.Hex("#1c1c1f"),
			text:     ui.Hex("#f5f5f7"),
			muted:    ui.RGBA(235, 235, 245, 0.56),
			faint:    ui.RGBA(235, 235, 245, 0.3),
			hairline: ui.RGBA(255, 255, 255, 0.07),
			hover:    ui.RGBA(255, 255, 255, 0.06),
			press:    ui.RGBA(255, 255, 255, 0.1),
			accent:   ui.Oklch(0.66, 0.2, 22),
			onAccent: ui.Hex("#ffffff"),
		}
	}
	return palette{
		bg:       ui.Hex("#ffffff"),
		sidebar:  ui.Hex("#f4f4f6"),
		text:     ui.Hex("#1d1d1f"),
		muted:    ui.RGBA(60, 60, 67, 0.62),
		faint:    ui.RGBA(60, 60, 67, 0.32),
		hairline: ui.RGBA(0, 0, 0, 0.07),
		hover:    ui.RGBA(0, 0, 0, 0.04),
		press:    ui.RGBA(0, 0, 0, 0.08),
		accent:   ui.Oklch(0.6, 0.21, 24),
		onAccent: ui.Hex("#ffffff"),
	}
}

var appFont = func() string {
	if runtime.GOOS == "darwin" {
		return "system-ui"
	}
	return "MiSans, system-ui, Microsoft YaHei UI"
}()

var (
	white = ui.RGB(255, 255, 255)
	// Text over the video, which may be bright: a little shy of white.
	onVideo = ui.RGBA(255, 255, 255, 0.86)
)

func (a *app) view(c *ui.Context) {
	t := *c.Theme()
	a.pal = paletteFor(&t)
	p := a.pal
	t.Accent, t.AccentText = p.accent, p.onAccent
	t.Selection = p.accent.Alpha(0.3)
	t.Font = appFont
	c.SetTheme(&t)

	c.OnShortcut(0, ui.KeyDown, func() { a.step(1) })
	c.OnShortcut(0, ui.KeyUp, func() { a.step(-1) })
	c.OnShortcut(0, ui.KeyEscape, a.escape)
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}
	root := ui.Box(c).Fill()
	if !c.Focused(a.searchBox) {
		// Space pauses wherever the focus is, as in MySpotify: on the box
		// around everything it comes before a button the pointer focused,
		// which would take it as a click; on the window, as nothing has
		// the focus.
		root.OnShortcut(0, ui.KeySpace, a.toggle)
		c.OnShortcut(0, ui.KeySpace, a.toggle)
	}
	root.Children(func() {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			side := ui.Column(c).Width(sidebarWidth)
			if !c.Vibrancy() {
				side.Background(p.sidebar)
			}
			side.Children(func() { a.sidebar(c) })
			ui.Column(c.Key("main")).Grow(1).Background(p.bg).BorderWidth(0, 0, 0, 1).BorderColor(p.hairline).Children(func() {
				a.pageTop(c)
				ui.Row(c).Grow(1).Padding(0, 16, 16, 16).Gap(16).AlignItems(ui.Stretch).Children(func() {
					a.stage(c, a.cur())
					if a.sheet != nil {
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
	p := a.pal
	// Under the window buttons, which sit over the sidebar.
	top := float32(14)
	if c.TitleBar().Left > 0 {
		top = max(c.TitleBar().Height, topBarHeight) + 4
	}
	ui.Column(c).Fill().Padding(top, 14, 14, 14).Gap(4).DragWindow().Children(func() {
		field := ui.Row(c).Height(32).PaddingX(10).Gap(7).Radius(9).AlignItems(ui.Center).Margin(0, 0, 14, 0)
		if c.Focused(a.searchBox) {
			field.Background(p.bg).Shadow(0, 0, 0, 3, p.accent.Alpha(0.25)).Border(1, p.accent.Alpha(0.6))
		} else {
			field.Background(p.hover).Border(1, p.hairline)
		}
		field.Children(func() {
			ui.Icon(c, iconSearch).Size(14, 14).TextColor(p.muted)
			in := ui.TextInputBase(c.Key("search"), &a.query).Bind(&a.searchBox).Grow(1).
				Placeholder("搜索视频").FontSize(13).Label("搜索")
			if in.Submitted() {
				a.search(a.query)
			}
		})

		ui.Scroll(c.Key("nav")).Grow(1).Margin(0, -6).Padding(0, 6).Gap(4).Children(func() {
			if a.navItem(c, iconHome, "推荐", len(a.pages) == 1).Clicked() {
				a.home()
			}
			if len(a.follows) > 0 || a.followMsg != "" {
				ui.Text(c, "关注").FontSize(11.5).FontWeight(600).TextColor(p.faint).Margin(18, 4, 6, 10)
			}
			if a.followMsg != "" {
				ui.Text(c, a.followMsg).FontSize(12).TextColor(p.faint).Margin(0, 10)
			}
			cur := a.cur().title
			for _, u := range a.follows {
				b := a.listItem(c.Key("u:"+u.ID), cur == "@"+u.Name, u.Name)
				b.Children(func() {
					a.initial(c, u.Name, 22)
					ui.Text(c, u.Name).FontSize(13).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0)
				})
				if b.Clicked() {
					a.openAuthor(a.cur(), u.Name, u.ID)
				}
			}
		})

		if a.me.Name != "" {
			ui.Row(c).Height(40).PaddingX(8).Gap(10).AlignItems(ui.Center).Children(func() {
				a.initial(c, a.me.Name, 26)
				ui.Text(c, a.me.Name).FontSize(13).FontWeight(600).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0)
			})
		}
	})
}

// listItem is a row of the sidebar: highlighted under the pointer, and
// marked when it is the page shown.
func (a *app) listItem(c *ui.Context, selected bool, label string) ui.Element {
	p := a.pal
	b := ui.ButtonBase(c).Height(34).PaddingX(10).Gap(10).Radius(8).AlignItems(ui.Center).Justify(ui.Start).FocusRing(false).Label(label)
	bg := ui.Transparent
	switch {
	case selected:
		bg = p.press
	case b.Hovered():
		bg = p.hover
	}
	return b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
}

func (a *app) navItem(c *ui.Context, icon *ui.SVG, label string, selected bool) ui.Element {
	p := a.pal
	b := a.listItem(c.Key(label), selected, label)
	color := p.text
	if selected {
		color = p.accent
	}
	return b.Children(func() {
		ui.Icon(c, icon).Size(17, 17).TextColor(color)
		ui.Text(c, label).FontSize(13.5).FontWeight(500).TextColor(p.text)
	})
}

// initial stands for an account's picture: its name's first character in a circle.
// shortcut: no avatars, which would need an image loader as MySpotify's artwork.go; add one with video covers.
func (a *app) initial(c *ui.Context, name string, size float32) {
	r := []rune(name)
	ch := "?"
	if len(r) > 0 {
		ch = strings.ToUpper(string(r[0]))
	}
	ui.Box(c).Size(size, size).Radius(size / 2).Background(a.pal.press).Center().Children(func() {
		ui.Text(c, ch).FontSize(size * 0.46).FontWeight(600).TextColor(a.pal.muted)
	})
}

// pageTop is the strip over the content: it drags the window, and holds the
// page's name with the way back once there is a page under it.
func (a *app) pageTop(c *ui.Context) {
	p := a.pal
	title := a.cur().title
	if title == "" {
		title = "推荐"
	}
	ui.Row(c).Height(topBarHeight + 6).PaddingX(12).Gap(4).AlignItems(ui.Center).DragWindow().Children(func() {
		if len(a.pages) > 1 && iconButton(c.Key("back"), iconBack, 18, p.muted, "返回").Clicked() {
			a.back()
		}
		ui.Text(c, title).FontSize(15).FontWeight(600).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0).Margin(0, 4)
	})
}

// stage is p's current video on black, rounded, with its author and text
// over its foot and its actions down its right side. All of it takes the
// wheel, which steps through the list; the picture takes clicks, which
// pause. Not the stage: a handler there would take the press from the
// buttons over the picture.
func (a *app) stage(c *ui.Context, p *page) {
	box := ui.Box(c.Key("stage")).Grow(1).Radius(14).Clip().Background(ui.RGB(0, 0, 0))
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
				ui.Text(c, text).FontSize(13).TextColor(onVideo)
			})
			return
		}
		s := p.list[p.idx]
		picture.Children(func() {
			if s.vp != nil {
				ui.Video(c, s.vp).Fill().Fit(ui.Contain)
			}
			if a.paused && s.vp != nil {
				ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Children(func() {
					ui.Icon(c, iconPlay).Size(56, 56).TextColor(white).Opacity(0.8)
				})
			}
		})
		a.caption(c, p, s)
		a.rail(c, s)
	})
}

// caption is the author, the text and where the video is in its list,
// over the foot of the video; the fade keeps it readable over a bright frame.
func (a *app) caption(c *ui.Context, p *page, s *slot) {
	ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(56, 84, 18, 18).Gap(6).
		Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.6), 180).Children(func() {
		name := "@" + s.v.Author
		b := ui.ButtonBase(c.Key("author")).FocusRing(false).Justify(ui.Start).Label(name)
		b.Children(func() {
			t := ui.Text(c, name).Bold().FontSize(16).TextColor(white)
			if b.Hovered() {
				t.Underline()
			}
		})
		if b.Clicked() {
			a.openAuthor(p, s.v.Author, s.v.AuthorID)
		}
		ui.Text(c, s.v.Desc).FontSize(14).TextColor(onVideo).MaxLines(3)
		status := fmt.Sprintf("%d / %d", p.idx+1, len(p.list))
		switch {
		case s.err != nil:
			status = "无法播放：" + s.err.Error()
		case s.vp == nil:
			status = "缓冲中…"
		}
		ui.Text(c, status).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.6))
	})
}

// rail is the column of actions down the right of the video, as Douyin's:
// the author, the likes, the comments and saving.
func (a *app) rail(c *ui.Context, s *slot) {
	ui.Column(c).Absolute().Right(10).Bottom(96).Gap(14).AlignItems(ui.Center).Children(func() {
		action := func(key string, icon *ui.SVG, label, count string, color ui.Color) ui.Element {
			var b ui.Element
			ui.Column(c).Gap(1).AlignItems(ui.Center).Children(func() {
				b = iconButton(c.Key(key), icon, 26, color, label)
				ui.Text(c, count).FontSize(11.5).FontWeight(600).TextColor(onVideo)
			})
			return b
		}
		action("likes", iconHeartFill, "点赞数", count(s.v.Likes), white)
		color := white
		if a.sheet != nil {
			color = a.pal.accent
		}
		if action("comments", iconComment, "评论", count(s.v.Comments), color).Clicked() {
			if a.sheet != nil {
				a.sheet = nil
			} else {
				a.comments(s.v)
			}
		}
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
	p := a.pal
	ui.Column(c.Key("comments")).Width(340).Radius(14).Background(p.sidebar).Border(1, p.hairline).Children(func() {
		ui.Row(c).Padding(10, 8, 6, 16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, count(sh.total)+" 条评论").FontSize(14).FontWeight(600).TextColor(p.text).Grow(1)
			if iconButton(c.Key("close"), iconClose, 16, p.muted, "关闭评论").Clicked() {
				a.sheet = nil
			}
		})
		ui.Scroll(c.Key("comment-list")).Grow(1).Padding(0, 0, 12, 0).Children(func() {
			for _, cm := range sh.items {
				ui.Row(c).Padding(10, 16).Gap(10).AlignItems(ui.Start).Children(func() {
					a.initial(c, cm.Author, 28)
					ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						ui.Text(c, cm.Author).FontSize(12).TextColor(p.muted).SingleLine()
						ui.Text(c, cm.Text).FontSize(14).TextColor(p.text)
						meta := when(cm.Time)
						if cm.Place != "" {
							meta += " · " + cm.Place
						}
						meta += "   ♥ " + count(cm.Likes)
						if cm.Replies > 0 {
							meta += fmt.Sprintf("   %d 条回复", cm.Replies)
						}
						ui.Text(c, meta).FontSize(11).TextColor(p.faint)
					})
				})
			}
			ui.Row(c).Padding(12, 16).Justify(ui.Center).Children(func() {
				switch {
				case sh.fetching:
					ui.Text(c, "载入中…").FontSize(12).TextColor(p.faint)
				case sh.msg != "":
					ui.Text(c, sh.msg).FontSize(12).TextColor(p.faint)
				case !sh.done:
					if ui.Button(c.Key("more"), "更多评论").Clicked() {
						a.moreComments()
					}
				case len(sh.items) == 0:
					ui.Text(c, "还没有评论").FontSize(12).TextColor(p.faint)
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
		bg = ui.RGBA(128, 128, 128, 0.18)
	}
	b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
	pose := ui.Pose{}
	if b.Pressed() {
		pose.Scale = 0.88
	}
	b.AnimatePose(pose, ui.Snappy)
	return b.Children(func() { ui.Icon(c, icon).Size(size, size) })
}

// when is a comment's time as Douyin shows it: the day, and the year if not this one.
func when(t time.Time) string {
	if t.Year() == time.Now().Year() {
		return t.Format("01-02")
	}
	return t.Format("2006-01-02")
}

func count(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1f万", float64(n)/10000)
	}
	return fmt.Sprint(n)
}

// Icons on a 24-point grid in the color of the text, as MySpotify's: the
// play mark and the heart filled, the rest in a 1.75-point stroke.
func strokeIcon(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`))
}

func fillIcon(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor">` + paths + `</svg>`))
}

var (
	iconPlay      = fillIcon(`<path d="M7 4.6v14.8c0 .8.9 1.3 1.6.9l12-7.4c.6-.4.6-1.3 0-1.7l-12-7.4C7.9 3.3 7 3.8 7 4.6z"/>`)
	iconHeartFill = fillIcon(`<path d="M19 14c1.5-1.5 3-3.2 3-5.5A5.5 5.5 0 0 0 16.5 3c-1.8 0-3 .5-4.5 2-1.5-1.5-2.7-2-4.5-2A5.5 5.5 0 0 0 2 8.5c0 2.3 1.5 4 3 5.5l7 7z"/>`)
	iconSearch    = strokeIcon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>`)
	iconHome      = strokeIcon(`<path d="M12 3 4 9v11a1 1 0 0 0 1 1h4v-6h6v6h4a1 1 0 0 0 1-1V9z"/>`)
	iconBack      = strokeIcon(`<path d="m15 18-6-6 6-6"/>`)
	iconComment   = strokeIcon(`<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>`)
	iconDownload  = strokeIcon(`<path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/>`)
	iconCheck     = strokeIcon(`<path d="M20 6 9 17l-5-5"/>`)
	iconClose     = strokeIcon(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`)
)
