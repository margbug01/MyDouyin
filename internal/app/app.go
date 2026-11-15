// Package app is the window: the recommend feed, one video at a time, scrolled
// with the wheel or arrow keys.
package app

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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

// app is read and changed on the main thread: by the view, and by goroutines through post.
type app struct {
	win      *mygo.Window
	c        *dy.Client
	dir      string
	list     []*slot
	idx      int
	fetching bool
	paused   bool
	lastNav  time.Time
	msg      string
}

func (a *app) post(fn func()) {
	a.win.Update(func() { fn(); a.win.Invalidate() })
}

// more fetches the next page of the feed.
func (a *app) more() {
	if a.fetching {
		return
	}
	a.fetching = true
	go func() {
		vs, err := a.c.Feed()
		a.post(func() {
			a.fetching = false
			if err != nil {
				a.msg = err.Error()
				return
			}
			a.msg = ""
			for _, v := range vs {
				a.list = append(a.list, &slot{v: v})
			}
			a.fill()
		})
	}()
}

// fill prepares the videos around idx, frees the ones far from it, and asks for more feed near the end.
func (a *app) fill() {
	for i, s := range a.list {
		switch {
		case i >= a.idx-behind && i <= a.idx+ahead:
			a.prepare(s)
		case i < a.idx-behind || i > a.idx+ahead:
			a.release(s)
		}
	}
	if len(a.list)-a.idx <= ahead+2 {
		a.more()
	}
	a.sync()
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
			a.sync()
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
	for i, s := range a.list {
		if s.vp == nil {
			continue
		}
		if i == a.idx && !a.paused {
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
	i := a.idx + d
	if i < 0 || i >= len(a.list) {
		return
	}
	a.lastNav = now
	a.idx, a.paused = i, false
	a.fill()
}

func (a *app) toggle() {
	a.paused = !a.paused
	a.sync()
}

func (a *app) view(c *ui.Context) {
	c.OnShortcut(0, ui.KeyDown, func() { a.step(1) })
	c.OnShortcut(0, ui.KeyUp, func() { a.step(-1) })
	c.OnShortcut(0, ui.KeySpace, a.toggle)
	white := ui.RGB(255, 255, 255)
	root := ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).HandleInput(func(ev ui.InputEvent) bool {
		switch {
		case ev.Kind == ui.InputScroll && !ev.Momentum && ev.DY != 0:
			if ev.DY > 0 {
				a.step(1)
			} else {
				a.step(-1)
			}
			return true
		case ev.Kind == ui.InputPointerUp && !ev.Canceled && ev.Button == 0:
			a.toggle()
			return true
		}
		return false
	})
	root.Children(func() {
		if a.idx >= len(a.list) {
			text := "载入中…"
			if a.msg != "" {
				text = a.msg
			}
			ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Children(func() {
				ui.Text(c, text).TextColor(white)
			})
			return
		}
		s := a.list[a.idx]
		if s.vp != nil {
			ui.Video(c, s.vp).Fill().Fit(ui.Contain)
		}
		// The fade keeps the white text readable over a bright frame.
		ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(48, 16, 20, 16).Gap(6).
			Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.6), 180).Children(func() {
			ui.Text(c, "@"+s.v.Author).Bold().FontSize(16).TextColor(white)
			ui.Text(c, s.v.Desc).FontSize(14).TextColor(white).MaxLines(3)
			status := fmt.Sprintf("♥ %s   💬 %s   %d/%d", count(s.v.Likes), count(s.v.Comments), a.idx+1, len(a.list))
			switch {
			case s.err != nil:
				status = "无法播放：" + s.err.Error()
			case s.vp == nil:
				status = "缓冲中…"
			}
			ui.Text(c, status).FontSize(12).TextColor(ui.RGB(200, 200, 200))
		})
	})
}

func count(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1f万", float64(n)/10000)
	}
	return fmt.Sprint(n)
}

// selfCheck (DY_SHOT=dir) saves a shot of the first video, of the next one
// after a swipe, and of the first again after swiping back, then quits.
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
			for i, s := range a.list {
				state += fmt.Sprintf(" %d:vp=%v,loading=%v,err=%v", i, s.vp != nil, s.loading, s.err)
			}
			done <- fmt.Sprintf("idx=%d%s", a.idx, state)
		})
		log.Println("shot", name, err, <-done)
	}
	shot("1.png")
	a.post(func() { a.step(1) })
	shot("2.png")
	a.post(func() { a.step(-1) })
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
	a := &app{c: c, dir: filepath.Join(cache, "Douyin", "videos")}
	os.RemoveAll(a.dir) // leftovers of a crashed run
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: "抖音", Width: 405, Height: 720, Content: ui.View(a.view)})
		a.more()
		if dir := os.Getenv("DY_SHOT"); dir != "" {
			go a.selfCheck(dir)
		}
	})
	log.Println("douyin: running")
	return mygo.App.Run()
}
