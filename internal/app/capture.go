package app

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	scaledraw "golang.org/x/image/draw"
)

// capture combines the real UI scene with an on-demand video frame. The window
// renderer cannot read AVPlayerLayer, which belongs to the system compositor.
// Call from a worker: Snapshot may wait for a decoded picture.
func (a *app) capture(w *mygo.Window) ([]byte, error) {
	var player *streamPlayer
	var frame, clip, root ui.Rect
	var cover bool
	surface := &a.mainVideo
	mygo.RunOnMain(func() {
		if w == a.pip {
			surface = &a.pipVideo
		}
		if surface.player == nil || surface.player.layer == nil || !surface.player.layer.VisibleIn(w.NativeHandle()) {
			return
		}
		player = surface.player
	})
	if player == nil {
		return w.CapturePage()
	}
	picture, err := player.live.Snapshot()
	if err != nil {
		return nil, err
	}
	raw, err := w.CapturePage()
	if err != nil {
		return nil, err
	}
	changed := false
	mygo.RunOnMain(func() {
		changed = surface.player != player || player.layer == nil
		frame, clip = surface.frame, surface.clip
		root, cover = surface.root, surface.cover
	})
	if changed {
		return nil, errors.New("video changed during capture")
	}
	overlay, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if root.W <= 0 || root.H <= 0 {
		return nil, errors.New("video window has no layout")
	}
	canvas := image.NewRGBA(overlay.Bounds())
	// Keep exactly the displayed fit and scroll clip; opaque UI paint then covers
	// the rounded corners and all controls, including half-transparent dialogs.
	sx, sy := float64(canvas.Bounds().Dx())/float64(root.W), float64(canvas.Bounds().Dy())/float64(root.H)
	pixels := func(r ui.Rect) image.Rectangle {
		return image.Rect(int(math.Round(float64(r.X-root.X)*sx)), int(math.Round(float64(r.Y-root.Y)*sy)), int(math.Round(float64(r.X+r.W-root.X)*sx)), int(math.Round(float64(r.Y+r.H-root.Y)*sy)))
	}
	target, visible := pixels(frame), pixels(clip).Intersect(canvas.Bounds())
	if !visible.Empty() {
		dst := canvas.SubImage(visible).(*image.RGBA)
		draw.Draw(dst, visible, &image.Uniform{C: color.Black}, image.Point{}, draw.Src)
		fitX, fitY := float64(target.Dx())/float64(picture.Bounds().Dx()), float64(target.Dy())/float64(picture.Bounds().Dy())
		factor := math.Min(fitX, fitY)
		if cover {
			factor = math.Max(fitX, fitY)
		}
		width, height := int(math.Round(float64(picture.Bounds().Dx())*factor)), int(math.Round(float64(picture.Bounds().Dy())*factor))
		left, top := target.Min.X+(target.Dx()-width)/2, target.Min.Y+(target.Dy()-height)/2
		scaledraw.ApproxBiLinear.Scale(dst, image.Rect(left, top, left+width, top+height), picture, picture.Bounds(), draw.Src, nil)
	}
	draw.Draw(canvas, canvas.Bounds(), overlay, overlay.Bounds().Min, draw.Over)
	var out bytes.Buffer
	err = png.Encode(&out, canvas)
	return out.Bytes(), err
}
