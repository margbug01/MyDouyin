// Package app is the window: the recommend feed, one video at a time, scrolled
// with the wheel or arrow keys, and searches opened over it.
package app

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
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
	saved   string // what saving to Downloads came to: "" before the click
}

const (
	ahead  = 2 // videos prepared after the current one
	behind = 1
)

// page is a list of videos the window steps through: the recommend feed at
// the bottom, and search results or a user's videos opened over it.
type page struct {
	title    string // "" for the recommend feed
	next     dy.Pager[dy.Video]
	list     []*slot
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
	sheet   *sheet // the comments open beside the video, or nil

	pal       palette
	searchBox ui.Handle
	toast     string    // a message to show once
	me        dy.User   // the account logged in, once known
	follows   []dy.User // the accounts it follows, in the sidebar
	followMsg string    // why they did not load
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

// more fetches the next page of p's list.
func (a *app) more(p *page) {
	if p.fetching || p.done {
		return
	}
	p.fetching = true
	go func() {
		vs, err := p.next()
		a.post(func() {
			p.fetching = false
			if err != nil {
				p.msg = err.Error()
				return
			}
			p.msg, p.done = "", len(vs) == 0
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

// home closes every page over the recommend feed.
func (a *app) home() {
	for len(a.pages) > 1 {
		a.back()
	}
}

// loadAccount fetches the account logged in and all it follows, for the sidebar.
// shortcut: loads every account followed at once; page it on scroll if lists of thousands matter.
func (a *app) loadAccount() {
	go func() {
		me, err := a.c.Self()
		var us []dy.User
		if err == nil {
			next := a.c.Following(me.ID)
			for {
				page, perr := next()
				if perr != nil {
					err = perr
				}
				if perr != nil || len(page) == 0 {
					break
				}
				us = append(us, page...)
			}
		}
		a.post(func() {
			a.me, a.follows = me, us
			if err != nil {
				a.followMsg = "关注列表没载入：" + err.Error()
			}
		})
	}()
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

// save copies s's video, already downloaded to play, to the Downloads folder.
func (a *app) save(s *slot) {
	home, err := os.UserHomeDir()
	if err != nil {
		s.saved = "下载失败：" + err.Error()
		return
	}
	s.saved = "保存中…"
	src := filepath.Join(a.dir, s.v.ID+".mp4")
	go func() {
		dst, err := copyTo(src, filepath.Join(home, "Downloads"), fileName(s.v))
		a.post(func() {
			if err != nil {
				s.saved = "下载失败：" + err.Error()
			} else {
				s.saved = "已存到 下载/" + filepath.Base(dst)
			}
			a.toast = s.saved
		})
	}()
}

// fileName is a video's name in Downloads: its author and id, without the
// characters a file name cannot have.
func fileName(v dy.Video) string {
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < ' ' {
			return '_'
		}
		return r
	}, v.Author)
	return "抖音-" + name + "-" + v.ID + ".mp4"
}

// copyTo copies src into dir as name, through a .part file so a failed copy
// leaves nothing half-written, and returns the path.
func copyTo(src, dir, name string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	dst := filepath.Join(dir, name)
	out, err := os.Create(dst + ".part")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(dst+".part", dst)
	}
	if err != nil {
		os.Remove(dst + ".part")
		return "", err
	}
	return dst, nil
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

// selfCheck (DY_SHOT=dir) walks through what the last change touched, as
// clicks would, saves a shot of each step and quits: now the whole window,
// with the feed, its comments, a search for 猫, the first result's author,
// and the feed again from the sidebar.
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
				state += fmt.Sprintf(" | %q idx=%d", p.title, p.idx)
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
	shot("1.png")
	a.post(func() { p := a.cur(); a.comments(p.list[p.idx].v) })
	shot("2.png")
	a.post(func() { a.search("猫") })
	shot("3.png")
	a.post(func() { p := a.cur(); a.openAuthor(p, p.list[0].v.Author, p.list[0].v.AuthorID) })
	shot("4.png")
	a.post(a.home)
	shot("5.png")
	follows := make(chan string)
	a.post(func() {
		follows <- fmt.Sprintf("me=%v follows=%d msg=%q", a.me.Name != "", len(a.follows), a.followMsg)
	})
	log.Println(<-follows)
	os.Exit(0)
}

// sidebarVibrancy is the material behind the sidebar: macOS's. Windows'
// stays light under a dark theme, so the sidebar is painted there.
func sidebarVibrancy() mygo.Vibrancy {
	if runtime.GOOS == "darwin" {
		return mygo.VibrancySidebar
	}
	return mygo.VibrancyNone
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
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "抖音",
			Width:     1100,
			Height:    760,
			MinWidth:  820,
			MinHeight: 560,
			StateKey:  "main",
			// As MySpotify's: no title bar, the window buttons inset within the sidebar.
			TitleBarStyle:        mygo.TitleBarHidden,
			TitleBarHeight:       topBarHeight + 6,
			TrafficLightPosition: &mygo.Point{X: 20, Y: 20},
			Vibrancy:             sidebarVibrancy(),
			BackgroundColor:      "light-dark(#ffffff, #141416)",
			Content:              ui.View(a.view),
		})
		a.fill()
		a.loadAccount()
		if dir := os.Getenv("DY_SHOT"); dir != "" {
			go a.selfCheck(dir)
		}
	})
	log.Println("douyin: running")
	return mygo.App.Run()
}
