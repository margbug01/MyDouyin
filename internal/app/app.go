// Package app is the window: the recommend feed, one video at a time, scrolled
// with the wheel or arrow keys, and searches opened over it.
package app

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
	"douyin/internal/media"
)

// slot is one feed entry and, once downloaded and opened, its players. Players
// are made for the videos around the current one so a swipe starts at once.
type slot struct {
	v       dy.Video
	file    *media.File
	vp      *ui.VideoPlayer
	ap      *audio.Player // nil when the clip has no audio
	loading bool
	gone    bool // dropped while loading: close what arrives
	err     error
}

const (
	ahead  = 2 // videos prepared after the current one
	behind = 1
)

// page is a list of videos the window steps through: the recommend feed at
// the bottom, and search results or a user's videos opened over it. A page
// with people lists accounts instead, as the ones the user follows.
type page struct {
	title    string // "" for the recommend feed
	next     dy.Pager[dy.Video]
	list     []*slot
	people   dy.Pager[dy.User]
	users    []dy.User
	idx      int
	fetching bool
	done     bool // next returned its last page
	msg      string
}

// app is read and changed on the main thread: by the view, and by goroutines through post.
type app struct {
	win     *mygo.Window
	c       *dy.Client
	dir     string
	pages   []*page // the window shows the last
	query   string  // the search box
	paused  bool
	lastNav time.Time
	sheet   *sheet // the comments open over the video, or nil
}

// sheet is the comments of the current video, loaded a page at a time.
type sheet struct {
	total    int
	items    []dy.Comment
	next     dy.Pager[dy.Comment]
	fetching bool
	done     bool
	msg      string
}

// comments opens v's comments over it; the video goes on playing.
func (a *app) comments(v dy.Video) {
	a.sheet = &sheet{total: v.Comments, next: a.c.Comments(v.ID)}
	a.moreComments()
}

func (a *app) moreComments() {
	sh := a.sheet
	if sh == nil || sh.fetching || sh.done {
		return
	}
	sh.fetching = true
	go func() {
		cs, err := sh.next()
		a.post(func() {
			sh.fetching = false
			if err != nil {
				sh.msg = err.Error()
				return
			}
			sh.msg, sh.done = "", len(cs) == 0
			sh.items = append(sh.items, cs...)
		})
	}()
}

// escape closes the comments, or else the page on top.
func (a *app) escape() {
	if a.sheet != nil {
		a.sheet = nil
		return
	}
	a.back()
}

func (a *app) cur() *page { return a.pages[len(a.pages)-1] }

func (a *app) post(fn func()) {
	if a.win == nil { // a test's view without a window
		return
	}
	a.win.Update(func() { fn(); a.win.Invalidate() })
}

// more fetches the next page of p's list. A list of people loads to its end
// while it shows, as fill asks again each time.
// shortcut: loads all of a list of people; page it on scroll if lists of thousands matter.
func (a *app) more(p *page) {
	if p.fetching || p.done {
		return
	}
	p.fetching = true
	go func() {
		var vs []dy.Video
		var us []dy.User
		var err error
		if p.people != nil {
			us, err = p.people()
		} else {
			vs, err = p.next()
		}
		a.post(func() {
			p.fetching = false
			if err != nil {
				p.msg = err.Error()
				return
			}
			p.msg, p.done = "", len(vs)+len(us) == 0
			p.users = append(p.users, us...)
			for _, v := range vs {
				p.list = append(p.list, &slot{v: v})
			}
			if p == a.cur() { // a page opened over p while it loaded prepares nothing of p
				a.fill()
			}
		})
	}()
}

// fill prepares the videos around the current one, frees the ones far from
// it, and asks for more of the list near its end.
// The current video comes first: the ones around it start once it plays (or
// failed), so they do not share its bandwidth.
func (a *app) fill() {
	p := a.cur()
	ready := false
	if p.idx < len(p.list) {
		s := p.list[p.idx]
		a.prepare(s)
		ready = s.vp != nil || s.err != nil
	}
	for i, s := range p.list {
		switch {
		case i < p.idx-behind || i > p.idx+ahead:
			a.release(s)
		case ready:
			a.prepare(s)
		}
	}
	if len(p.list)-p.idx <= ahead+2 {
		a.more(p)
	}
	a.sync()
}

// open shows p over the current page, whose players it frees: only one page
// plays, and memory stays that of one page.
func (a *app) open(p *page) {
	for _, s := range a.cur().list {
		a.release(s)
	}
	a.pages = append(a.pages, p)
	a.sheet = nil
	a.paused = false
	a.fill()
}

// back closes the page on top and goes on with the one under it.
func (a *app) back() {
	if len(a.pages) == 1 {
		return
	}
	for _, s := range a.cur().list {
		a.release(s)
	}
	a.pages = a.pages[:len(a.pages)-1]
	a.sheet = nil
	a.paused = false
	a.fill()
}

func (a *app) prepare(s *slot) {
	s.gone = false // back in range while still loading: keep what arrives
	if s.loading || s.vp != nil || s.err != nil {
		return
	}
	s.loading = true
	go func() {
		f, vp, ap, err := a.build(s.v)
		a.post(func() {
			s.loading = false
			if s.gone {
				closeAll(f, vp, ap)
				return
			}
			s.file, s.vp, s.ap, s.err = f, vp, ap, err
			a.fill() // plays it if current, and starts the ones around it
		})
	}()
}

func (a *app) build(v dy.Video) (f *media.File, vp *ui.VideoPlayer, ap *audio.Player, err error) {
	path := filepath.Join(a.dir, v.ID+".mp4")
	if _, err = os.Stat(path); err != nil {
		if err = a.c.Download(v.PlayURL, path); err != nil {
			return
		}
	}
	if f, err = media.Open(path); err != nil {
		return
	}
	vd, err := f.Video()
	if err == nil {
		vp, err = ui.NewVideoPlayer(vd)
	}
	if err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	vp.SetLoop(true).Pause()
	if ad, aerr := f.Audio(); aerr == nil {
		ap = audio.NewPlayer(ad)
		ap.SetLoop(true)
	}
	return
}

func closeAll(f *media.File, vp *ui.VideoPlayer, ap *audio.Player) {
	if vp != nil {
		vp.Close()
	}
	if ap != nil {
		ap.Close()
	}
	if f != nil {
		f.Close()
	}
}

// release frees a slot's players and its cached file.
func (a *app) release(s *slot) {
	s.gone = s.loading
	if s.vp == nil {
		return
	}
	closeAll(s.file, s.vp, s.ap)
	os.Remove(filepath.Join(a.dir, s.v.ID+".mp4"))
	s.file, s.vp, s.ap = nil, nil, nil
	s.err = nil
}

// sync plays the current video and holds the others.
func (a *app) sync() {
	p := a.cur()
	for i, s := range p.list {
		if s.vp == nil {
			continue
		}
		if i == p.idx && !a.paused {
			s.vp.Play()
			if s.ap != nil {
				s.ap.Play()
			}
			continue
		}
		s.vp.Pause()
		if s.ap != nil {
			s.ap.Pause()
		}
	}
}

// step moves to the next (+1) or previous (-1) video. A wheel's momentum sends
// events for a second after the finger lifts, so one step per 400 ms.
func (a *app) step(d int) {
	now := time.Now()
	if now.Sub(a.lastNav) < 400*time.Millisecond {
		return
	}
	p := a.cur()
	i := p.idx + d
	if i < 0 || i >= len(p.list) {
		return
	}
	a.lastNav = now
	p.idx, a.paused = i, false
	a.sheet = nil // the comments were the last video's
	a.fill()
}

func (a *app) toggle() {
	a.paused = !a.paused
	a.sync()
}

// search opens the videos found for q.
func (a *app) search(q string) {
	if q = strings.TrimSpace(q); q != "" {
		a.open(&page{title: "搜索：" + q, next: a.c.Search(q)})
	}
}

// openAuthor opens a user's videos, unless p is that page already.
func (a *app) openAuthor(p *page, name, id string) {
	if title := "@" + name; p.title != title && id != "" {
		a.open(&page{title: title, next: a.c.Posts(id)})
	}
}

// tap is a box that runs fn when clicked. Not a Button: one keeps the focus
// after the click, and Space would then click it again instead of pausing.
func tap(c *ui.Context, fn func()) ui.Element {
	return ui.Box(c).Cursor(ui.CursorPointer).HandleInput(func(ev ui.InputEvent) bool {
		switch {
		case ev.Kind == ui.InputPointerDown && ev.Button == 0:
			return true // taking the press is what brings its release here
		case ev.Kind == ui.InputPointerUp && !ev.Canceled && ev.Button == 0:
			fn()
			return true
		}
		return false
	})
}

func (a *app) view(c *ui.Context) {
	c.OnShortcut(0, ui.KeyDown, func() { a.step(1) })
	c.OnShortcut(0, ui.KeyUp, func() { a.step(-1) })
	c.OnShortcut(0, ui.KeySpace, a.toggle)
	c.OnShortcut(0, ui.KeyEscape, a.escape)
	p := a.cur()
	ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).Children(func() {
		// The video area takes the wheel and clicks. The top bar lies over it,
		// not in it, so a click in the search box does not pause.
		ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).HandleInput(func(ev ui.InputEvent) bool {
			switch {
			case p.people != nil:
				return false // a list of people scrolls itself
			case ev.Kind == ui.InputScroll && !ev.Momentum && ev.DY != 0:
				if ev.DY > 0 {
					a.step(1)
				} else {
					a.step(-1)
				}
				return true
			case ev.Kind == ui.InputPointerDown && ev.Button == 0:
				return true
			case ev.Kind == ui.InputPointerUp && !ev.Canceled && ev.Button == 0:
				if a.sheet != nil { // a click beside the comments closes them
					a.sheet = nil
				} else {
					a.toggle()
				}
				return true
			}
			return false
		}).Children(func() { a.content(c, p) })
		a.topBar(c, p)
		if a.sheet != nil {
			a.commentSheet(c, a.sheet)
		}
	})
}

var white = ui.RGB(255, 255, 255)

// content is p's current video with its author and text, or what stands for it.
func (a *app) content(c *ui.Context, p *page) {
	if len(p.users) > 0 {
		a.people(c, p)
		return
	}
	if p.idx >= len(p.list) {
		text := "载入中…"
		switch {
		case p.msg != "":
			text = p.msg
		case p.done:
			text = "没有结果"
		}
		ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Children(func() {
			ui.Text(c, text).TextColor(white)
		})
		return
	}
	s := p.list[p.idx]
	if s.vp != nil {
		ui.Video(c, s.vp).Fill().Fit(ui.Contain)
	}
	// The fade keeps the white text readable over a bright frame.
	ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(48, 16, 20, 16).Gap(6).
		Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.6), 180).Children(func() {
		tap(c, func() { a.openAuthor(p, s.v.Author, s.v.AuthorID) }).Children(func() {
			ui.Text(c, "@"+s.v.Author).Bold().FontSize(16).TextColor(white)
		})
		ui.Text(c, s.v.Desc).FontSize(14).TextColor(white).MaxLines(3)
		grey := ui.RGB(200, 200, 200)
		switch {
		case s.err != nil:
			ui.Text(c, "无法播放："+s.err.Error()).FontSize(12).TextColor(grey)
		case s.vp == nil:
			ui.Text(c, "缓冲中…").FontSize(12).TextColor(grey)
		default:
			ui.Row(c).Gap(14).Children(func() {
				ui.Text(c, "♥ "+count(s.v.Likes)).FontSize(12).TextColor(grey)
				tap(c, func() { a.comments(s.v) }).Children(func() {
					ui.Text(c, "💬 "+count(s.v.Comments)).FontSize(12).TextColor(grey)
				})
				ui.Text(c, fmt.Sprintf("%d/%d", p.idx+1, len(p.list))).FontSize(12).TextColor(grey)
			})
		}
	})
}

// commentSheet is the comments over the lower part of the video.
func (a *app) commentSheet(c *ui.Context, sh *sheet) {
	grey := ui.RGB(150, 150, 150)
	ui.Column(c).Absolute().Top(160).Left(0).Right(0).Bottom(0).Background(ui.RGB(24, 24, 24)).Radius(12, 12, 0, 0).Children(func() {
		ui.Row(c).Padding(14, 16).Children(func() {
			ui.Text(c, count(sh.total)+" 条评论").Bold().FontSize(14).TextColor(white).Grow(1)
			tap(c, func() { a.sheet = nil }).Children(func() {
				ui.Text(c, "✕").FontSize(16).TextColor(grey)
			})
		})
		ui.Scroll(c).Grow(1).Children(func() {
			for _, cm := range sh.items {
				ui.Column(c).Padding(8, 16).Gap(4).Children(func() {
					ui.Text(c, cm.Author).FontSize(12).TextColor(grey)
					ui.Text(c, cm.Text).FontSize(14).TextColor(white)
					meta := when(cm.Time)
					if cm.Place != "" {
						meta += " · " + cm.Place
					}
					meta += "   ♥ " + count(cm.Likes)
					if cm.Replies > 0 {
						meta += fmt.Sprintf("   %d 条回复", cm.Replies)
					}
					ui.Text(c, meta).FontSize(11).TextColor(grey)
				})
			}
			ui.Row(c).Padding(12, 16).Children(func() {
				switch {
				case sh.fetching:
					ui.Text(c, "载入中…").FontSize(12).TextColor(grey)
				case sh.msg != "":
					ui.Text(c, sh.msg).FontSize(12).TextColor(grey)
				case !sh.done:
					tap(c, a.moreComments).Children(func() {
						ui.Text(c, "更多评论").FontSize(12).TextColor(white)
					})
				case len(sh.items) == 0:
					ui.Text(c, "还没有评论").FontSize(12).TextColor(grey)
				}
			})
		})
	})
}

// when is a comment's time as Douyin shows it: the day, and the year if not this one.
func when(t time.Time) string {
	if t.Year() == time.Now().Year() {
		return t.Format("01-02")
	}
	return t.Format("2006-01-02")
}

// people lists p's accounts; a click opens one's videos.
func (a *app) people(c *ui.Context, p *page) {
	ui.Scroll(c).Absolute().Top(56).Left(0).Right(0).Bottom(0).Children(func() {
		for _, u := range p.users {
			tap(c, func() { a.openAuthor(p, u.Name, u.ID) }).Column().Padding(10, 16).Gap(3).Children(func() {
				ui.Text(c, u.Name).Bold().FontSize(15).TextColor(white)
				if u.Signature != "" {
					ui.Text(c, u.Signature).FontSize(12).TextColor(ui.RGB(160, 160, 160)).MaxLines(1)
				}
			})
		}
	})
}

// topBar is the search box over the feed, and the way back over anything else.
func (a *app) topBar(c *ui.Context, p *page) {
	ui.Row(c).Absolute().Top(0).Left(0).Right(0).Padding(12, 12, 32, 12).Gap(8).
		Gradient(ui.RGBA(0, 0, 0, 0.6), ui.RGBA(0, 0, 0, 0), 180).Children(func() {
		if p.title == "" {
			ui.TextInput(c, &a.query).Placeholder("搜索").Grow(1).OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter || k.Mods != 0 {
					return false
				}
				a.search(k.Text())
				return true
			})
			tap(c, func() { a.open(&page{title: "关注", people: a.c.MyFollowing()}) }).
				Background(white).Radius(6).Padding(5, 10).Children(func() {
				ui.Text(c, "关注").FontSize(13).TextColor(ui.RGB(0, 0, 0))
			})
			return
		}
		if ui.Button(c, "‹ 返回").Clicked() {
			a.back()
		}
		ui.Text(c, p.title).Bold().FontSize(15).TextColor(white).MaxLines(1).Shrink(1)
	})
}

func count(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1f万", float64(n)/10000)
	}
	return fmt.Sprint(n)
}

// selfCheck (DY_SHOT=dir) walks through what the last change touched, as
// clicks would, saves a shot of each step and quits: now a search for 猫, the
// first result's comments, and their second page.
func (a *app) selfCheck(dir string) {
	shot := func(name string) {
		time.Sleep(12 * time.Second)
		b, err := a.win.CapturePage()
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, name), b, 0o644)
		}
		done := make(chan string)
		a.post(func() {
			state := ""
			for _, p := range a.pages {
				state += fmt.Sprintf(" | %q idx=%d users=%d", p.title, p.idx, len(p.users))
				if a.sheet != nil && p == a.cur() {
					state += fmt.Sprintf(" comments=%d done=%v msg=%q", len(a.sheet.items), a.sheet.done, a.sheet.msg)
				}
				for i, s := range p.list {
					if s.vp != nil || s.loading || s.err != nil {
						state += fmt.Sprintf(" %d:vp=%v,loading=%v,err=%v", i, s.vp != nil, s.loading, s.err)
					}
				}
			}
			done <- state
		})
		log.Println("shot", name, err, <-done)
	}
	a.post(func() { a.search("猫") })
	shot("1.png")
	a.post(func() { a.comments(a.cur().list[0].v) })
	shot("2.png")
	a.post(a.moreComments)
	shot("3.png")
	os.Exit(0)
}

// Run opens the window and blocks until it closes.
func Run() error {
	c, err := dy.New()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	a := &app{c: c, dir: filepath.Join(cache, "Douyin", "videos"), pages: []*page{{next: c.Feed}}}
	os.RemoveAll(a.dir) // leftovers of a crashed run
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: "抖音", Width: 405, Height: 720, Content: ui.View(a.view)})
		a.fill()
		if dir := os.Getenv("DY_SHOT"); dir != "" {
			go a.selfCheck(dir)
		}
	})
	log.Println("douyin: running")
	return mygo.App.Run()
}
