package app

import (
	"errors"
	"io"
	"log"
	"time"

	"douyin/internal/media"
	"github.com/egoist/mygo/ui"
)

type videoPlayer interface {
	Play()
	Pause()
	Playing() bool
	Close()
	Err() error
	Size() (int, int)
}

// AVPlayer already schedules frames. Drawing its current one on the UI thread
// needs one RGBA buffer, without VideoPlayer's copied first/ahead/free frames.
type streamPlayer struct {
	live                 *media.Live
	bitmap               *ui.Bitmap
	layer                *media.Layer
	native               bool
	width, height        int
	loop, paused, closed bool
	err                  error
	lastUpdate, deadline time.Time
}

func newStreamPlayer(live *media.Live, loop bool) (*streamPlayer, error) {
	f, err := live.Next()
	if err != nil {
		return nil, err
	}
	return &streamPlayer{live: live, bitmap: ui.NewBitmap(f.Image), loop: loop, width: f.Image.Bounds().Dx(), height: f.Image.Bounds().Dy()}, nil
}

func (p *streamPlayer) Play()            { p.paused = false; p.lastUpdate = time.Time{} }
func (p *streamPlayer) Pause()           { p.paused = true }
func (p *streamPlayer) Playing() bool    { return !p.paused && !p.closed && p.err == nil }
func (p *streamPlayer) Err() error       { return p.err }
func (p *streamPlayer) Size() (int, int) { return p.width, p.height }
func (p *streamPlayer) Close() {
	p.closed = true
	if p.layer != nil {
		p.layer.Close()
		p.layer = nil
	}
	p.bitmap = nil
	p.live.Close()
}

func (p *streamPlayer) enableNative() {
	if !p.native || p.layer != nil || p.closed {
		return
	}
	var err error
	p.layer, err = p.live.NewLayer()
	if err != nil {
		p.fallback(err)
	}
}

func (p *streamPlayer) fallback(reason error) {
	log.Printf("native video unavailable, using pixels: %v", reason)
	if p.layer != nil {
		p.layer.Close()
		p.layer = nil
	}
	p.native = false
	if err := p.live.UsePixels(); err != nil {
		p.err = err
		return
	}
	if p.loop {
		p.live.Seek(p.live.Position())
	}
	p.deadline = time.Now().Add(20 * time.Second)
}

// update also runs while listening with the picture hidden, so loops still happen.
func (p *streamPlayer) update() {
	if p.closed || p.err != nil {
		return
	}
	now := time.Now()
	if p.paused || now.Sub(p.lastUpdate) > time.Second {
		p.deadline = now.Add(20 * time.Second)
	}
	p.lastUpdate = now
	f, err := p.live.Frame()
	if err == io.EOF && p.loop {
		err = p.live.Rewind()
		p.deadline = now.Add(20 * time.Second)
	}
	if err != nil {
		p.err = err
		return
	}
	if f.Image != nil {
		if p.bitmap == nil {
			p.bitmap = ui.NewBitmap(f.Image)
		} else {
			p.bitmap.Update(f.Image)
		}
		p.deadline = now.Add(20 * time.Second)
	} else if p.layer != nil && p.layer.Ready() {
		p.deadline = now.Add(20 * time.Second)
	} else if now.After(p.deadline) {
		p.err = errors.New("media: the stream sent no picture for 20 s")
	}
}

func video(c *ui.Context, p videoPlayer) ui.Element {
	if s, ok := p.(*streamPlayer); ok {
		s.update()
		if s.Playing() || s.live.Seeking() {
			c.AnimationFrame()
		}
		if s.bitmap == nil {
			return ui.Box(c)
		}
		return ui.Image(c, s.bitmap)
	}
	return ui.Video(c, p.(*ui.VideoPlayer))
}
