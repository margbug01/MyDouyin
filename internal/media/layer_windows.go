package media

import (
	"errors"

	"github.com/egoist/mygo/ui"
)

// Windows frames are painted by MyGo, including clipping, dialogs and PiP.
// The native layer is an optional capability used only by the macOS backend.
type Layer struct{}

func (l *Live) NewLayer() (*Layer, error) {
	return nil, errors.New("media: native video layers are unavailable on Windows")
}
func (l *Layer) PlaceIn(window uintptr, picture, clip ui.Rect, cover bool, radius float32) error {
	return errors.New("media: native video layers are unavailable on Windows")
}
func (l *Layer) Ready() bool                   { return false }
func (l *Layer) Hidden() bool                  { return false }
func (l *Layer) VisibleIn(window uintptr) bool { return false }
func (l *Layer) Hide(window uintptr)           {}
func (l *Layer) Close()                        {}
