package drama

import (
	"context"
	"douyin/internal/media"
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"io"
	"log"
	"time"
)

type Page struct {
	s      Series
	path   string
	p      Progress
	index  int
	player *media.Live
	bitmap *ui.Bitmap
	paused bool
	resume time.Duration
	saved  time.Time
	status string
}

func NewPage(c Catalog, path string) (*Page, error) {
	s := c.Series()
	if len(s.Episodes) == 0 {
		return nil, errors.New("剧集为空")
	}
	p, e := Load(path)
	if e != nil {
		return nil, e
	}
	a := &Page{s: s, path: path, p: p}
	i := 0
	for k, ep := range s.Episodes {
		if ep.ID == p.Episode {
			i = k
			break
		}
	}
	a.open(i)
	return a, nil
}
func (a *Page) persist() {
	if e := a.p.Save(a.path); e != nil {
		a.status = "保存进度失败：" + e.Error()
		log.Print(a.status)
	}
}
func (a *Page) checkpoint() {
	if a.player != nil && a.resume == 0 && !a.player.Seeking() {
		if pos := a.player.Position(); pos > 0 {
			a.p.Positions[a.p.Episode] = pos
		}
	}
	a.persist()
	a.saved = time.Now()
}
func (a *Page) release() {
	if a.player != nil {
		a.player.Close()
		a.player = nil
	}
	a.bitmap = nil
}
func (a *Page) Close() { a.checkpoint(); a.release() }
func (a *Page) open(i int) {
	if a.player != nil {
		a.checkpoint()
	}
	a.release()
	ep := a.s.Episodes[i]
	a.index = i
	a.paused = false
	a.status = ""
	a.resume = a.p.Positions[ep.ID]
	a.p.Episode = ep.ID
	a.persist()
	p, e := media.OpenVideo(context.Background(), ep.URL, nil)
	if e != nil {
		a.status = e.Error()
		return
	}
	a.player = p
	p.SetMuted(false)
	p.Play() // Windows opens it paused
}
func (a *Page) tick() {
	if a.player == nil {
		return
	}
	a.player.Sync()
	f, e := a.player.Frame()
	if errors.Is(e, io.EOF) {
		delete(a.p.Positions, a.p.Episode)
		a.release()
		if a.index+1 < len(a.s.Episodes) {
			a.open(a.index + 1)
		} else {
			a.status = "全剧播放完毕"
			a.persist()
		}
		return
	}
	if e != nil {
		a.status = e.Error()
		a.release()
		return
	}
	// Windows draws the picture itself, from the player, not from frames.
	ready := f.Image != nil
	if vp := a.player.Player(); vp != nil {
		ready = vp.Ready()
	}
	if ready && a.resume > 0 {
		a.player.Seek(a.resume)
		a.resume = 0
	}
	if f.Image != nil {
		if a.bitmap == nil {
			a.bitmap = ui.NewBitmap(f.Image)
		} else {
			a.bitmap.Update(f.Image)
		}
	}
	if time.Since(a.saved) > time.Second {
		a.checkpoint()
	}
}
func (a *Page) View(c *ui.Context) {
	a.tick()
	ui.Column(c).Fill().Padding(20).Gap(12).Children(func() {
		ui.Text(c, a.s.Title).FontSize(23).FontWeight(700)
		ui.Text(c, fmt.Sprintf("共 %d 集 · 正在查看：%s", len(a.s.Episodes), a.s.Episodes[a.index].Title))
		if a.player != nil && a.player.Player() != nil {
			ui.Video(c, a.player.Player()).Grow(1).Fit(ui.Contain)
		} else if a.bitmap != nil {
			ui.Image(c, a.bitmap).Grow(1).Fit(ui.Contain)
		} else {
			ui.Box(c).Grow(1)
		}
		ui.Text(c, a.status)
		ui.Row(c).Gap(8).Children(func() {
			for i, ep := range a.s.Episodes {
				if ui.Button(c.Key(ep.ID), ep.Title).Clicked() {
					a.open(i)
				}
			}
			label := "暂停"
			if a.paused {
				label = "继续"
			}
			if ui.Button(c.Key("pause"), label).Disabled(a.player == nil).Clicked() {
				a.paused = !a.paused
				if a.paused {
					a.player.Pause()
					a.checkpoint()
				} else {
					a.player.Play()
				}
			}
			if a.player != nil {
				ui.Text(c, fmt.Sprintf("%d 秒", int(a.player.Position().Seconds())))
			}
		})
	})
	if a.player != nil {
		c.After(time.Second / 30)
	}
}
