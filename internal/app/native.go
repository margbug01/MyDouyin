package app

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"time"
)

// Each window owns one presentation slot. A frame hides its old picture first;
// only an element actually painted in this frame can show it again.
type nativeSurface struct {
	window            *mygo.Window
	player            *streamPlayer
	element, viewport ui.Element
	cover             bool
	radius            float32
	root, frame, clip ui.Rect // last painted geometry, valid outside the build pass
}

func (s *nativeSurface) hide() {
	if s.window != nil && s.player != nil && s.player.layer != nil {
		s.player.layer.Hide(s.window.NativeHandle())
	}
}

func (s *nativeSurface) begin(w *mygo.Window) {
	s.hide()
	s.window, s.player, s.element, s.viewport = w, nil, ui.Element{}, ui.Element{}
}

func nativePlayer(p videoPlayer) bool {
	s, ok := p.(*streamPlayer)
	return ok && s.native && !s.closed
}

func (s *nativeSurface) video(c *ui.Context, p videoPlayer, cover bool, viewport ui.Element, radius float32) ui.Element {
	stream, ok := p.(*streamPlayer)
	if !ok || !stream.native || s.window == nil {
		return video(c, p)
	}
	stream.enableNative()
	if stream.layer == nil {
		return video(c, p)
	}
	stream.update()
	s.player, s.cover, s.viewport, s.radius = stream, cover, viewport, radius
	box := ui.Box(c)
	s.element = box
	box.Draw(func(painter *ui.Painter, r ui.Rect) {
		clip := videoIntersection(r, painter.Visible())
		if clip.W <= 0 || clip.H <= 0 {
			return
		}
		if err := stream.layer.PlaceIn(s.window.NativeHandle(), r, clip, cover, radius); err != nil {
			stream.fallback(err)
			s.player = nil
			s.window.Invalidate()
			return
		}
		s.frame, s.clip = r, clip
		stream.bitmap = nil
	})
	// AVPlayerLayer advances independently. UI wakes for seeking/readiness, the
	// progress clock, or real danmaku; it no longer redraws every decoded frame.
	delay := 500 * time.Millisecond
	if stream.live.Seeking() || !stream.layer.Ready() || stream.Playing() && stream.live.UntilEnd() < time.Second {
		delay = 30 * time.Millisecond
	}
	c.After(delay)
	return box
}

func videoIntersection(a, b ui.Rect) ui.Rect {
	x, y := max(a.X, b.X), max(a.Y, b.Y)
	return ui.Rect{X: x, Y: y, W: max(0, min(a.X+a.W, b.X+b.W)-x), H: max(0, min(a.Y+a.H, b.Y+b.H)-y)}
}

func (s *nativeSurface) bounds() (ui.Rect, ui.Rect) {
	frame := s.element.Bounds()
	clip := videoIntersection(frame, s.root)
	if s.viewport.Valid() {
		clip = videoIntersection(clip, s.viewport.Bounds())
	}
	return frame, clip
}

// Paint after the hidden web view's hole, before controls. Everything outside
// the video stays opaque, including rounded corners: login/write pages cannot
// show through a transparent parent, and scroll clipping stays aligned on resize.
func (s *nativeSurface) background(c *ui.Context, color ui.Color) {
	ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).ZIndex(-1).HandleInput(func(ev ui.InputEvent) bool {
		return ev.Kind == ui.InputPointerDown || ev.Kind == ui.InputPointerUp || ev.Kind == ui.InputScroll
	}).Draw(func(p *ui.Painter, r ui.Rect) {
		s.root = r
		if s.player == nil || s.player.layer == nil {
			p.Fill(r, color, 0)
			return
		}
		frame, clip := s.bounds()
		s.frame, s.clip = frame, clip
		if clip.W <= 0 || clip.H <= 0 {
			p.Fill(r, color, 0)
			return
		}
		p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: clip.Y - r.Y}, color, 0)
		p.Fill(ui.Rect{X: r.X, Y: clip.Y + clip.H, W: r.W, H: r.Y + r.H - clip.Y - clip.H}, color, 0)
		p.Fill(ui.Rect{X: r.X, Y: clip.Y, W: clip.X - r.X, H: clip.H}, color, 0)
		p.Fill(ui.Rect{X: clip.X + clip.W, Y: clip.Y, W: r.X + r.W - clip.X - clip.W, H: clip.H}, color, 0)
		radius := min(s.radius, frame.W/2, frame.H/2)
		if radius <= 0 {
			return
		}
		p.Clip(clip, 0, func() {
			for _, corner := range [][4]float32{{frame.X, frame.Y, 1, 1}, {frame.X + frame.W, frame.Y, -1, 1}, {frame.X, frame.Y + frame.H, 1, -1}, {frame.X + frame.W, frame.Y + frame.H, -1, -1}} {
				x, y, sx, sy := corner[0], corner[1], corner[2], corner[3]
				var path ui.Path
				path.MoveTo(x, y).LineTo(x+sx*radius, y).CubeTo(x+sx*radius*0.447715, y, x, y+sy*radius*0.447715, x, y+sy*radius).Close()
				p.FillPath(&path, color)
			}
		})
	})
}
