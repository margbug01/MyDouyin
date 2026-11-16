package media

import (
	"errors"
	"image"
	"io"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

// UsePixels restores the regular decoder when native presentation is unavailable.
func (l *Live) UsePixels() error {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return io.EOF
	}
	if l.out != 0 {
		return nil
	}
	l.out = newPixelOutput()
	if l.out == 0 {
		return errors.New("media: cannot create pixel output")
	}
	l.item.Send(sel("addOutput:"), l.out)
	l.last = 0
	return nil
}

// UntilEnd is wall-clock time at the current rate, for scheduling loop service.
func (l *Live) UntilEnd() time.Duration {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || !l.vod {
		return time.Hour
	}
	at := objc.Send[cmTime](l.player, sel("currentTime"))
	end := objc.Send[cmTime](l.item, sel("duration"))
	if !at.valid() || !end.valid() {
		return time.Hour
	}
	return time.Duration(float64(end.dur()-at.dur()) / float64(l.speed))
}

func newPixelOutput() objc.ID {
	return objc.ID(clsItemOutput).Send(sel("alloc")).Send(sel("initWithPixelBufferAttributes:"), nsDict("PixelFormatType", nsInt(0x42475241)))
}

// Snapshot attaches a pixel output only for this capture. Normal native playback
// keeps no Go image and no continuously converted BGRA output.
func (l *Live) Snapshot() (*image.RGBA, error) {
	var output, item objc.ID
	var paused bool
	func() {
		defer autorelease()()
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.closed {
			return
		}
		output = newPixelOutput()
		if output == 0 {
			return
		}
		item = l.item.Send(sel("retain"))
		item.Send(sel("addOutput:"), output)
		paused = l.paused
	}()
	if output == 0 {
		return nil, errors.New("media: stream unavailable for capture")
	}
	defer func() {
		defer autorelease()()
		l.mu.Lock()
		defer l.mu.Unlock()
		item.Send(sel("removeOutput:"), output)
		output.Send(sel("release"))
		item.Send(sel("release"))
	}()
	if paused {
		l.Seek(l.Position())
	}
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		var picture *image.RGBA
		var closed bool
		func() {
			defer autorelease()()
			l.mu.Lock()
			defer l.mu.Unlock()
			closed = l.closed
			if closed {
				return
			}
			at := objc.Send[cmTime](l.player, sel("currentTime"))
			if pb := uintptr(output.Send(sel("copyPixelBufferForItemTime:itemTimeForDisplay:"), at, unsafe.Pointer(nil))); pb != 0 {
				picture = bgra(pb, nil)
				cfRelease(pb)
			}
		}()
		if closed {
			return nil, io.EOF
		}
		if picture != nil {
			return picture, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("media: capture timed out waiting for a picture")
}
