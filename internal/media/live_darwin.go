package media

import (
	"errors"
	"image"
	"io"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo/ui"
)

// Live plays a stream (a live room's HLS) with AVPlayer, which plays its
// sound itself, and hands its pictures to ui.VideoPlayer as they come due:
// both follow AVPlayer's clock, so they keep together.
type Live struct {
	player, item, out objc.ID

	mu     sync.Mutex // Close against a Next on the player's goroutine
	closed bool
	last   float64 // the host time of the last picture handed out
}

var (
	liveOnce         sync.Once
	caCurrentTime    func() float64
	clsPlayer        objc.Class
	clsPlayerItem    objc.Class
	clsItemOutput    objc.Class
	errLiveNoPicture = errors.New("media: the stream sent no picture for 20 s")
)

func liveInit() {
	h, err := purego.Dlopen("/System/Library/Frameworks/QuartzCore.framework/QuartzCore", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	purego.RegisterLibFunc(&caCurrentTime, h, "CACurrentMediaTime")
	clsPlayer, clsPlayerItem = objc.GetClass("AVPlayer"), objc.GetClass("AVPlayerItem")
	clsItemOutput = objc.GetClass("AVPlayerItemVideoOutput")
}

// OpenLive starts the stream at url, playing silent: a player asks for its
// first picture as it is made, which a paused stream never sends.
func OpenLive(url string) (*Live, error) {
	liveOnce.Do(liveInit)
	u := objc.ID(clsURL).Send(sel("URLWithString:"), nsString(url))
	if u == 0 {
		return nil, errors.New("media: bad stream address")
	}
	l := &Live{}
	l.item = objc.ID(clsPlayerItem).Send(sel("playerItemWithURL:"), u)
	l.out = objc.ID(clsItemOutput).Send(sel("alloc")).Send(sel("initWithPixelBufferAttributes:"),
		nsDict("PixelFormatType", nsInt(0x42475241))) // 'BGRA'
	if l.item == 0 || l.out == 0 {
		return nil, errors.New("media: cannot open the stream")
	}
	l.item.Send(sel("retain"))
	l.item.Send(sel("addOutput:"), l.out)
	l.player = objc.ID(clsPlayer).Send(sel("playerWithPlayerItem:"), l.item)
	l.player.Send(sel("retain"))
	l.player.Send(sel("setMuted:"), true)
	l.player.Send(sel("play"))
	return l, nil
}

// Play and Pause the stream, its sound with it; SetMuted silences it.
func (l *Live) Play()  { l.send("play") }
func (l *Live) Pause() { l.send("pause") }
func (l *Live) SetMuted(m bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.player.Send(sel("setMuted:"), m)
	}
}

// SetVolume sets the stream's sound, from 0 to 1.
func (l *Live) SetVolume(v float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.player.Send(sel("setVolume:"), float32(v))
	}
}

func (l *Live) send(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.player.Send(sel(s))
	}
}

// Next waits for the stream's next picture to come due and returns it,
// lasting as long as the one before it did, so the player shows each as it
// comes. It gives up after 20 s without one, not counting while paused.
func (l *Live) Next() (ui.VideoFrame, error) {
	deadline := time.Now().Add(20 * time.Second)
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return ui.VideoFrame{}, io.EOF
		}
		if objc.Send[int](l.item, sel("status")) == 2 { // failed
			l.mu.Unlock()
			return ui.VideoFrame{}, errors.New("media: the stream failed to play")
		}
		if objc.Send[int](l.player, sel("timeControlStatus")) == 0 { // paused
			deadline = time.Now().Add(20 * time.Second)
		}
		now := caCurrentTime()
		t := objc.Send[cmTime](l.out, sel("itemTimeForHostTime:"), now)
		var img *image.RGBA
		if objc.Send[bool](l.out, sel("hasNewPixelBufferForItemTime:"), t) {
			if pb := uintptr(l.out.Send(sel("copyPixelBufferForItemTime:itemTimeForDisplay:"), t, unsafe.Pointer(nil))); pb != 0 {
				img = bgra(pb)
				cfRelease(pb)
			}
		}
		l.mu.Unlock()
		if img != nil {
			dur := time.Second / 30
			if l.last > 0 {
				dur = max(time.Duration((now-l.last)*float64(time.Second)), 5*time.Millisecond)
			}
			l.last = now
			return ui.VideoFrame{Image: img, Duration: dur}, nil
		}
		if time.Now().After(deadline) {
			return ui.VideoFrame{}, errLiveNoPicture
		}
		time.Sleep(4 * time.Millisecond)
	}
}

// Rewind does nothing: a stream has no start to go back to.
func (l *Live) Rewind() error { return nil }

// Close stops the stream and its sound, and lets them go.
func (l *Live) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.player.Send(sel("pause"))
	l.player.Send(sel("replaceCurrentItemWithPlayerItem:"), objc.ID(0))
	l.player.Send(sel("release"))
	l.out.Send(sel("release"))
	l.item.Send(sel("release"))
	return nil
}

// bgra copies a BGRA pixel buffer into an RGBA image.
func bgra(pb uintptr) *image.RGBA {
	cvPixelBufferLock(pb, 1)
	defer cvPixelBufferUnlock(pb, 1)
	w, h, stride := int(cvPixelBufferWidth(pb)), int(cvPixelBufferHeight(pb)), int(cvPixelBufferStride(pb))
	src := unsafe.Slice((*byte)(cvPixelBufferBase(pb)), stride*h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		s, d := src[y*stride:y*stride+w*4], img.Pix[y*img.Stride:]
		for x := 0; x < w*4; x += 4 {
			d[x], d[x+1], d[x+2], d[x+3] = s[x+2], s[x+1], s[x], 255
		}
	}
	return img
}
