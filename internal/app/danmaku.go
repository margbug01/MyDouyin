package app

import (
	"sort"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

// flight is how long a bullet comment takes to cross the video.
const flight = 8 * time.Second

const dmLanes, dmGap = 7, 24 // lanes over the video; px kept between two in a lane

const (
	dmTop, dmLaneH = 14, 30 // where the first lane is, and each lane's height
	dmSize         = 17
	dmWeight       = 600
	// dmFPS is how often flying comments repaint: smooth for text crossing
	// the video, where each of the display's refreshes (up to 144 a second)
	// cost as much again.
	dmFPS = 60
)

var (
	dmColor  = ui.RGBA(255, 255, 255, 0.92)
	dmShadow = ui.NewTextShadow(0, 1, 3, ui.RGBA(0, 0, 0, 0.7))
)

// laneTail is the last comment given a lane: when it set off, how wide.
type laneTail struct {
	at   time.Duration
	w    float32
	used bool
}

// dmField is where the bullet comments over a video fly: the lane each took
// as it set off, kept as it flies.
type dmField struct {
	lane map[dy.Danmaku]int // -1: no room, not shown
	tail [dmLanes]laneTail
	own  dy.Danmaku // the user's last sent: shown even when the lanes are full
}

// place gives a comment setting off at at, tw wide, the topmost lane where it
// neither starts on the last one's tail nor, being faster, catches it up
// before that one is gone: -1 when none, as Douyin's drops a crowd's excess.
func (f *dmField) place(d dy.Danmaku, tw, w float32) int {
	fits := func(t laneTail) bool {
		if !t.used {
			return true
		}
		sec := float32(flight.Seconds())
		v0, v := (w+t.w)/sec, (w+tw)/sec // px a second: wider ones fly faster
		if float32((d.At-t.at).Seconds())*v0 < t.w+dmGap {
			return false
		}
		return v <= v0 || float32((t.at+flight-d.At).Seconds())*v <= w-dmGap
	}
	lane := -1
	for i, t := range f.tail {
		if fits(t) {
			lane = i
			break
		}
	}
	if lane < 0 && d == f.own { // the user's own shows anyway, where the last one set off longest ago
		lane = 0
		for i, t := range f.tail {
			if t.at < f.tail[lane].at {
				lane = i
			}
		}
	}
	if lane >= 0 {
		f.tail[lane] = laneTail{d.At, tw, true}
	}
	if f.lane == nil {
		f.lane = map[dy.Danmaku]int{}
	}
	f.lane[d] = lane
	return lane
}

// dmFlying is a bullet comment where it is at a moment, in the layer.
type dmFlying struct {
	d    dy.Danmaku
	x, y float32
}

// flyDanmaku returns the comments of list, in time order, flying at at over a
// video w wide: each from its time on for flight, in the lane of f it took as
// it set off. next is how long until the next one sets off, -1 when none is
// to come.
func flyDanmaku[D any](a *app, f *dmField, list []D, key func(D) dy.Danmaku, at time.Duration, w float32) (out []dmFlying, next time.Duration) {
	next = -1
	if w <= 0 {
		return nil, next
	}
	// list is time-ordered: jump straight to the first comment still in flight.
	start := sort.Search(len(list), func(i int) bool { return key(list[i]).At > at-flight })
	for _, it := range list[start:] {
		d := key(it)
		age := at - d.At
		if age < 0 {
			next = -age
			break
		}
		l, ok := f.lane[d]
		if ok && l < 0 {
			continue
		}
		tw := a.danmakuText(d.Text).w
		if !ok {
			l = f.place(d, tw, w)
			if l < 0 {
				continue
			}
		}
		out = append(out, dmFlying{d, w - float32(age)/float32(flight)*(w+tw), float32(dmTop + l*dmLaneH)})
	}
	return out, next
}

// dmShaped is a bullet comment's text shaped once to paint in every frame
// that it flies, and the pictures of its emoji, in the order of its objects.
type dmShaped struct {
	l    *ui.TextLayout
	w    float32
	pics []string
}

// danmakuText is text shaped as a bullet comment, kept with its emoji spans,
// so that loading the emoji shapes it again.
func (a *app) danmakuText(text string) dmShaped {
	key := emojiKey{text: text, size: dmSize, weight: dmWeight}
	if e, ok := a.emojiCache[key]; ok && e.dm != nil {
		return *e.dm
	}
	a.emojiSpans(text, dmSize, dmWeight)
	e := a.emojiCache[key]
	spans := make([]ui.Span, len(e.spans))
	for i, sp := range e.spans {
		sp.Color, sp.Shadow = dmColor, dmShadow
		spans[i] = sp
	}
	l := ui.ShapeRichText(spans, ui.Font{Size: dmSize, Weight: dmWeight}, 0)
	sh := &dmShaped{l: l, pics: make([]string, len(e.refs))}
	sh.w, _ = l.Size()
	for i, r := range e.refs {
		sh.pics[i] = r.url
	}
	e.dm = sh
	a.emojiCache[key] = e
	return *sh
}

// danmakuLayer paints over the video the bullet comments fly gives for the
// layer's width. They move in frames that only paint, without building the
// view, dmFPS a second while moving.
func (a *app) danmakuLayer(c *ui.Context, key string, moving bool, fly func(w float32) ([]dmFlying, time.Duration)) {
	if a.dmOff {
		return
	}
	ui.Box(c.Key(key)).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		flying, next := fly(r.W)
		for _, f := range flying {
			sh := a.danmakuText(f.d.Text)
			x, y := r.X+f.x, r.Y+f.y
			p.TextLayout(sh.l, x, y, dmColor)
			for i, o := range sh.l.Objects() {
				if i < len(sh.pics) {
					if bmp := a.img.getSized(sh.pics[i], emojiEdge); bmp != nil {
						p.Image(bmp, ui.Rect{X: x + o.Rect.X, Y: y + o.Rect.Y, W: o.Rect.W, H: o.Rect.H}, ui.Contain)
					}
				}
			}
		}
		switch {
		case !moving:
		case len(flying) > 0:
			p.AnimationFrameRate(dmFPS)
		case next >= 0:
			p.After(next)
		}
	})
}
