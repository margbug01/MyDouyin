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
// On Windows, engine holds a mygo/ui/video player backed by Media Foundation
// and D3D11 shared GPU textures (zero CPU pixel copies).
type streamPlayer struct {
	live                 *media.Live
	engine               *ui.VideoPlayer
	bitmap               *ui.Bitmap
	layer                *media.Layer
	native               bool
	width, height        int
	loop, paused, closed bool
	err                  error
	// lastUpdate is when the player last moved on, waitFrom since when it
	// has played with no new picture.
	lastUpdate, waitFrom time.Time
	lastPos              time.Duration
	hasPos               bool
}

// noPictureFor is how long a playing stream may show no new picture.
const noPictureFor = 20 * time.Second

var errNoPicture = errors.New("media: the stream sent no picture for 20 s")

func newStreamPlayer(live *media.Live, loop bool) (*streamPlayer, error) {
	if vp := live.Player(); vp != nil {
		live.SetLoop(loop) // through live, which a cancel may be closing
		return &streamPlayer{live: live, engine: vp, loop: loop}, nil
	}
	f, err := live.Next()
	if err != nil {
		return nil, err
	}
	return &streamPlayer{live: live, bitmap: ui.NewBitmap(f.Image), loop: loop, width: f.Image.Bounds().Dx(), height: f.Image.Bounds().Dy()}, nil
}

// newNativePlayer is for media.OpenNative: AVPlayerLayer draws the first
// picture itself, so only wait until the item can play.
func newNativePlayer(live *media.Live, loop bool) (*streamPlayer, error) {
	w, h, err := live.WaitReady()
	if err != nil {
		return nil, err
	}
	return &streamPlayer{live: live, native: true, loop: loop, width: w, height: h}, nil
}

func (p *streamPlayer) Play() {
	p.paused = false
	p.lastUpdate = time.Time{}
	if p.engine != nil {
		p.live.Play()
	}
}
func (p *streamPlayer) Pause() {
	p.paused = true
	if p.engine != nil {
		p.live.Pause()
	}
}
func (p *streamPlayer) Playing() bool { return !p.paused && !p.closed && p.Err() == nil }
func (p *streamPlayer) Err() error {
	if p.err != nil {
		return p.err
	}
	if p.engine != nil {
		return p.engine.Err()
	}
	return nil
}
func (p *streamPlayer) Size() (int, int) {
	if p.engine != nil {
		return p.engine.Size()
	}
	return p.width, p.height
}
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
	p.waitFrom = time.Now()
}

// update also runs while listening with the picture hidden, so loops still happen.
func (p *streamPlayer) update() {
	if p.closed || (p.err != nil && !errors.Is(p.err, errNoPicture)) {
		return
	}
	if p.engine != nil {
		p.live.Sync()
		return
	}
	now := time.Now()
	if p.paused || now.Sub(p.lastUpdate) > time.Second {
		p.waitFrom = now
	}
	p.lastUpdate = now
	f, err := p.live.Frame()
	if err == io.EOF && p.loop {
		err = p.live.Rewind()
		p.waitFrom = now
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
		p.waitFrom = now
		if errors.Is(p.err, errNoPicture) {
			p.err = nil
		}
	} else if p.native && (p.layer == nil || p.layer.Ready() || p.layer.Hidden()) { // no pixels to wait for before mounting or while hidden
		p.waitFrom = now
		if errors.Is(p.err, errNoPicture) {
			p.err = nil
		}
	} else if now.Sub(p.waitFrom) > noPictureFor {
		p.err = errNoPicture
	}
}

// watch fails an engine's player that has played where it shows with no
// picture for noPictureFor: none yet, or a stream stuck buffering. MyGo
// learns both only as it draws the player, so watch runs as video does.
func (p *streamPlayer) watch(now time.Time) {
	p.watchState(now, p.engine.Ready(), p.engine.Buffering(), p.engine.Position())
}

func (p *streamPlayer) watchState(now time.Time, ready, buffering bool, pos time.Duration) {
	advancing := p.hasPos && pos != p.lastPos
	p.lastPos, p.hasPos = pos, true
	playing := ready && (!buffering || advancing)
	if p.paused || now.Sub(p.lastUpdate) > time.Second || playing {
		p.waitFrom = now
		if errors.Is(p.err, errNoPicture) && playing {
			p.err = nil
		}
	}
	p.lastUpdate = now
	if p.err == nil && now.Sub(p.waitFrom) > noPictureFor {
		p.err = errNoPicture
	}
}

// pictureWait is how long p had played with no new picture when it last
// moved on: it does not grow while nothing shows it.
func pictureWait(p videoPlayer) time.Duration {
	if s, ok := p.(*streamPlayer); ok {
		return s.lastUpdate.Sub(s.waitFrom)
	}
	return 0
}

func video(c *ui.Context, p videoPlayer) ui.Element {
	if s, ok := p.(*streamPlayer); ok {
		if s.engine != nil {
			s.live.Sync()
			s.watch(time.Now())
			return ui.Video(c, s.engine)
		}
		s.update()
		if s.Playing() || s.live.Seeking() {
			c.AnimationFrame()
		}
		if s.bitmap == nil {
			return ui.Box(c)
		}
		return ui.Image(c, s.bitmap)
	}
	if vp, ok := p.(*ui.VideoPlayer); ok {
		return ui.Video(c, vp)
	}
	return ui.Box(c)
}
