package media

import (
	"errors"
	"structs"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// Layer displays a Live directly with AVPlayerLayer. All its methods run on
// the main thread, and Close must run before its host window is destroyed.
type Layer struct {
	layer, view objc.ID
	window      uintptr
	frame       [4]float64
	cover       bool
	gravities   [2]objc.ID
}

type layerRect struct {
	_          structs.HostLayout
	X, Y, W, H float64
}

// NewLayer switches the stream from pixel output to native presentation.
func (l *Live) NewLayer() (*Layer, error) {
	defer autorelease()()
	if !objc.Send[bool](objc.ID(objc.GetClass("NSThread")), sel("isMainThread")) {
		return nil, errors.New("media: create the video layer on the main thread")
	}
	handle, err := purego.Dlopen("/System/Library/Frameworks/AVFoundation.framework/AVFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, err
	}
	defer purego.Dlclose(handle)
	var gravities [2]objc.ID
	var copyPointer func(*objc.ID, uintptr, uintptr) uintptr
	purego.RegisterLibFunc(&copyPointer, purego.RTLD_DEFAULT, "memcpy")
	for i, name := range []string{"AVLayerVideoGravityResizeAspect", "AVLayerVideoGravityResizeAspectFill"} {
		address, err := purego.Dlsym(handle, name)
		if err != nil {
			return nil, err
		}
		copyPointer(&gravities[i], address, unsafe.Sizeof(gravities[i]))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errors.New("media: stream already closed")
	}
	layer := objc.ID(objc.GetClass("AVPlayerLayer")).Send(sel("playerLayerWithPlayer:"), l.player)
	if layer == 0 {
		return nil, errors.New("media: cannot create AVPlayerLayer")
	}
	layer.Send(sel("retain"))
	layer.Send(sel("setZPosition:"), float64(-1))
	layer.Send(sel("setMasksToBounds:"), true)
	layer.Send(sel("setCornerRadius:"), float64(12))
	layer.Send(sel("setBackgroundColor:"), objc.ID(objc.GetClass("NSColor")).Send(sel("blackColor")).Send(sel("CGColor")))
	if l.out != 0 {
		l.item.Send(sel("removeOutput:"), l.out)
		l.out.Send(sel("release"))
		l.out = 0
	}
	l.frame = nil
	return &Layer{layer: layer, gravities: gravities}, nil
}

// Place uses the content view's DIPs, as MyGo's Element.Draw does.
func (v *Layer) Place(window uintptr, x, y, w, h float64, cover bool) error {
	defer autorelease()()
	if v.layer == 0 {
		return errors.New("media: video layer closed")
	}
	view := v.view
	if v.window != window {
		view = findSurface(objc.ID(window).Send(sel("contentView")))
		if view == 0 {
			return errors.New("media: MyGo native surface not found")
		}
	}
	parent := view.Send(sel("layer"))
	if !objc.Send[bool](parent, sel("isGeometryFlipped")) {
		bounds := objc.Send[layerRect](view, sel("bounds"))
		y = bounds.H - y - h
	}
	frame := [4]float64{x, y, w, h}
	if v.window == window && v.frame == frame && v.cover == cover {
		return nil
	}
	transaction := objc.ID(objc.GetClass("CATransaction"))
	transaction.Send(sel("begin"))
	defer transaction.Send(sel("commit"))
	transaction.Send(sel("setDisableActions:"), true)
	if v.window != window {
		v.layer.Send(sel("removeFromSuperlayer"))
		v.view.Send(sel("release"))
		v.view = view.Send(sel("retain"))
		v.window = window
		parent.Send(sel("addSublayer:"), v.layer)
	}
	v.layer.Send(sel("setFrame:"), layerRect{X: x, Y: y, W: w, H: h})
	gravity := v.gravities[0]
	if cover {
		gravity = v.gravities[1]
	}
	v.layer.Send(sel("setVideoGravity:"), gravity)
	v.frame, v.cover = frame, cover
	return nil
}

// shortcut: MyGo 0.8.1 has no native-view handle API; fail if its surface class changes.
func findSurface(view objc.ID) objc.ID {
	if view == 0 {
		return 0
	}
	if cls := objc.GetClass("MyGoSurfaceView"); cls != 0 && objc.Send[bool](view, sel("isKindOfClass:"), cls) {
		return view
	}
	children := view.Send(sel("subviews"))
	for i, n := uint(0), objc.Send[uint](children, sel("count")); i < n; i++ {
		if found := findSurface(children.Send(sel("objectAtIndex:"), i)); found != 0 {
			return found
		}
	}
	return 0
}

func (v *Layer) Ready() bool {
	defer autorelease()()
	return v.layer != 0 && objc.Send[bool](v.layer, sel("isReadyForDisplay"))
}

func (v *Layer) AttachedTo(window uintptr) bool {
	return v.layer != 0 && v.window == window
}

func (v *Layer) Close() {
	defer autorelease()()
	if v.layer == 0 {
		return
	}
	v.layer.Send(sel("setPlayer:"), objc.ID(0))
	v.layer.Send(sel("removeFromSuperlayer"))
	v.layer.Send(sel("release"))
	v.view.Send(sel("release"))
	v.layer, v.view, v.window = 0, 0, 0
}
