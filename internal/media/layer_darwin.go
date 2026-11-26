package media

import (
	"errors"
	"structs"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo/ui"
)

// Layer displays a Live directly with AVPlayerLayer. All its methods run on
// the main thread, and Close must run before its host window is destroyed.
type Layer struct {
	layer, view, mask, surface objc.ID
	window                     uintptr
	frame                      [8]float64
	cover, hidden              bool
	radius                     float32
	gravities                  [2]objc.ID
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
	mask := objc.ID(objc.GetClass("CALayer")).Send(sel("new"))
	mask.Send(sel("setBackgroundColor:"), objc.ID(objc.GetClass("NSColor")).Send(sel("whiteColor")).Send(sel("CGColor")))
	layer.Send(sel("setMask:"), mask)
	layer.Send(sel("setBackgroundColor:"), objc.ID(objc.GetClass("NSColor")).Send(sel("blackColor")).Send(sel("CGColor")))
	if l.out != 0 {
		l.item.Send(sel("removeOutput:"), l.out)
		l.out.Send(sel("release"))
		l.out = 0
	}
	l.frame = nil
	return &Layer{layer: layer, mask: mask, gravities: gravities}, nil
}

// Place uses the content view's DIPs, as MyGo's Element.Draw does.
func (v *Layer) Place(window uintptr, x, y, w, h float64, cover bool) error {
	r := ui.Rect{X: float32(x), Y: float32(y), W: float32(w), H: float32(h)}
	return v.PlaceIn(window, r, r, cover, 12)
}

// PlaceIn clips a full picture to its scroll viewport without changing its aspect ratio.
func (v *Layer) PlaceIn(window uintptr, picture, clip ui.Rect, cover bool, radius float32) error {
	defer autorelease()()
	x, y, w, h := float64(picture.X), float64(picture.Y), float64(picture.W), float64(picture.H)
	mx, my, mw, mh := float64(clip.X-picture.X), float64(picture.H-(clip.Y-picture.Y)-clip.H), float64(clip.W), float64(clip.H)
	if v.layer == 0 {
		return errors.New("media: video layer closed")
	}
	moved := v.window != window
	if moved {
		surface := findSurface(objc.ID(window).Send(sel("contentView")))
		if surface == 0 {
			return errors.New("media: MyGo native surface not found")
		}
		parent := surface.Send(sel("superview"))
		if parent == 0 {
			return errors.New("media: MyGo surface has no native parent")
		}
		// Give video its own view, as native WebKit embedding does. AVPlayerLayer
		// must not mutate the same backing layer that the Metal UI renderer owns.
		bounds := objc.Send[layerRect](surface, sel("frame"))
		host := objc.ID(objc.GetClass("NSView")).Send(sel("alloc")).Send(sel("initWithFrame:"), bounds)
		if host == 0 {
			return errors.New("media: cannot create native video host")
		}
		host.Send(sel("setWantsLayer:"), true)
		host.Send(sel("setAutoresizingMask:"), uint(18)) // width and height sizeable
		v.layer.Send(sel("removeFromSuperlayer"))
		v.view.Send(sel("removeFromSuperview"))
		v.view.Send(sel("release"))
		v.surface.Send(sel("release"))
		v.surface = surface.Send(sel("retain"))
		v.view = host
		parent.Send(sel("addSubview:positioned:relativeTo:"), host, int(-1), surface)
		host.Send(sel("layer")).Send(sel("addSublayer:"), v.layer)
		v.window = window
	}
	if err := v.orderAbovePages(); err != nil {
		return err
	}
	view := v.view
	parent := view.Send(sel("layer"))
	if !objc.Send[bool](parent, sel("isGeometryFlipped")) {
		bounds := objc.Send[layerRect](view, sel("bounds"))
		y = bounds.H - y - h
	}
	if v.hidden {
		transaction := objc.ID(objc.GetClass("CATransaction"))
		transaction.Send(sel("begin"))
		transaction.Send(sel("setDisableActions:"), true)
		v.layer.Send(sel("setHidden:"), false)
		transaction.Send(sel("commit"))
		v.hidden = false
	}
	frame := [8]float64{x, y, w, h, mx, my, mw, mh}
	if !moved && v.frame == frame && v.cover == cover && v.radius == radius {
		return nil
	}
	transaction := objc.ID(objc.GetClass("CATransaction"))
	transaction.Send(sel("begin"))
	defer transaction.Send(sel("commit"))
	transaction.Send(sel("setDisableActions:"), true)
	v.layer.Send(sel("setFrame:"), layerRect{X: x, Y: y, W: w, H: h})
	v.mask.Send(sel("setFrame:"), layerRect{X: mx, Y: my, W: mw, H: mh})
	v.layer.Send(sel("setCornerRadius:"), float64(radius))
	gravity := v.gravities[0]
	if cover {
		gravity = v.gravities[1]
	}
	v.layer.Send(sel("setVideoGravity:"), gravity)
	v.frame, v.cover, v.radius = frame, cover, radius
	return nil
}

// Keep video above a lazily-created WebKit view but below the native UI.
func (v *Layer) orderAbovePages() error {
	parent := v.surface.Send(sel("superview"))
	if parent == 0 {
		return errors.New("media: video host detached")
	}
	views := parent.Send(sel("subviews"))
	var previous objc.ID
	for i, n := uint(0), objc.Send[uint](views, sel("count")); i < n; i++ {
		view := views.Send(sel("objectAtIndex:"), i)
		if view == v.surface {
			if previous == v.view {
				return nil
			}
			if v.view.Send(sel("superview")) != parent {
				v.view.Send(sel("setFrame:"), objc.Send[layerRect](v.surface, sel("frame")))
			}
			parent.Send(sel("addSubview:positioned:relativeTo:"), v.view, int(-1), v.surface)
			return nil
		}
		previous = view
	}
	return errors.New("media: video surface detached")
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

func (v *Layer) VisibleIn(window uintptr) bool {
	return window != 0 && v.layer != 0 && v.window == window && !v.hidden
}

func (v *Layer) AttachedTo(window uintptr) bool {
	return v.layer != 0 && v.window == window
}

// Hidden reports whether the layer is hidden or not yet mounted in a window.
func (v *Layer) Hidden() bool {
	return v.layer == 0 || v.hidden || v.window == 0
}

// Hide hides only this host's picture; another window may be presenting it.
func (v *Layer) Hide(window uintptr) {
	defer autorelease()()
	if v.layer != 0 && v.window == window && !v.hidden {
		transaction := objc.ID(objc.GetClass("CATransaction"))
		transaction.Send(sel("begin"))
		transaction.Send(sel("setDisableActions:"), true)
		v.layer.Send(sel("setHidden:"), true)
		transaction.Send(sel("commit"))
		v.hidden = true
	}
}

func (v *Layer) Close() {
	defer autorelease()()
	if v.layer == 0 {
		return
	}
	v.layer.Send(sel("setPlayer:"), objc.ID(0))
	v.layer.Send(sel("removeFromSuperlayer"))
	v.layer.Send(sel("setMask:"), objc.ID(0))
	v.mask.Send(sel("release"))
	v.layer.Send(sel("release"))
	v.view.Send(sel("removeFromSuperview"))
	v.view.Send(sel("release"))
	v.surface.Send(sel("release"))
	v.layer, v.view, v.mask, v.surface, v.window = 0, 0, 0, 0, 0
}
