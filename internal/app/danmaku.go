package app

import (
	"math"
	"sort"
	"strconv"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

// flight is how long a bullet comment takes to cross the video at 1x speed.
const flight = 8 * time.Second

const dmLanes, dmGap = 7, 24 // lanes over the video; px kept between two in a lane

const (
	dmTop, dmLaneH = 14, 30 // default first lane offset and lane height
	dmSize         = 17     // default font size in DIPs
	dmWeight       = 600
	// dmFPS is how often flying comments repaint: smooth for text crossing
	// the video, where each of the display's refreshes (up to 144 a second)
	// cost as much again.
	dmFPS = 60
)

const (
	dmStrokeShadow = "shadow"
	dmStrokeThin   = "thin"
	dmStrokeThick  = "thick"
	dmStrokeNone   = "none"
)

// danmakuStyle is the user's bullet-comment appearance and crossing speed.
type danmakuStyle struct {
	Speed   float64 // crossing speed multiplier relative to 1x (0.5..1.5)
	Size    float32 // font size in DIPs (14..25)
	Stroke  string  // dmStrokeShadow, dmStrokeThin, dmStrokeThick, dmStrokeNone
	Opacity float32 // text alpha (0.5..1.0)
}

var defaultDanmakuStyle = danmakuStyle{
	Speed:   1,
	Size:    dmSize,
	Stroke:  dmStrokeShadow,
	Opacity: 0.92,
}

type dmChoice[T comparable] struct {
	val   T
	label string
}

var (
	dmSpeedChoices = []dmChoice[float64]{
		{0.5, "0.5x"},
		{0.75, "0.75x"},
		{1, "1x"},
		{1.25, "1.25x"},
		{1.5, "1.5x"},
	}
	dmSizeChoices = []dmChoice[float32]{
		{14, "小"},
		{17, "标准"},
		{21, "大"},
		{25, "超大"},
	}
	dmStrokeChoices = []dmChoice[string]{
		{dmStrokeShadow, "阴影"},
		{dmStrokeThin, "细描边"},
		{dmStrokeThick, "粗描边"},
		{dmStrokeNone, "无"},
	}
	dmOpacityChoices = []dmChoice[float32]{
		{1, "100%"},
		{0.92, "90%"},
		{0.75, "75%"},
		{0.55, "55%"},
	}
)

func normalizeDanmakuStyle(s danmakuStyle) danmakuStyle {
	if math.IsNaN(s.Speed) || math.IsInf(s.Speed, 0) || s.Speed <= 0 {
		s.Speed = defaultDanmakuStyle.Speed
	} else {
		s.Speed = max(0.5, min(2, s.Speed))
	}
	if s.Size <= 0 {
		s.Size = defaultDanmakuStyle.Size
	} else {
		s.Size = max(12, min(32, s.Size))
	}
	switch s.Stroke {
	case dmStrokeShadow, dmStrokeThin, dmStrokeThick, dmStrokeNone:
	default:
		s.Stroke = defaultDanmakuStyle.Stroke
	}
	if s.Opacity <= 0 {
		s.Opacity = defaultDanmakuStyle.Opacity
	} else {
		s.Opacity = max(0.3, min(1, s.Opacity))
	}
	return s
}

func (a *app) danmakuStyle() danmakuStyle {
	return normalizeDanmakuStyle(a.dmStyle)
}

func (a *app) setDanmakuStyle(s danmakuStyle) {
	a.dmStyle = normalizeDanmakuStyle(s)
}

func (a *app) playSpeed() float64 {
	if a.speed <= 0 {
		return 1
	}
	return a.speed
}

// dmRealFlight is how long a bullet comment takes to cross the video in wall-clock time.
func (a *app) dmRealFlight() time.Duration {
	return time.Duration(float64(flight) / a.danmakuStyle().Speed)
}

// dmVideoFlight is the video-time span of one crossing, compensated by playback
// speed so 2x playback keeps bullet comments moving at the same readable speed.
func (a *app) dmVideoFlight() time.Duration {
	return time.Duration(float64(a.dmRealFlight()) * a.playSpeed())
}

func (a *app) dmLaneHeight() int {
	return int(math.Round(float64(a.danmakuStyle().Size))) + (dmLaneH - dmSize)
}

func dmChoiceLabels[T comparable](choices []dmChoice[T]) []string {
	out := make([]string, len(choices))
	for i, ch := range choices {
		out[i] = ch.label
	}
	return out
}

func dmLabelFor[T comparable](choices []dmChoice[T], val T, fallback string) string {
	for _, ch := range choices {
		if ch.val == val {
			return ch.label
		}
	}
	return fallback
}

func dmValueFor[T comparable](choices []dmChoice[T], label string, fallback T) T {
	for _, ch := range choices {
		if ch.label == label {
			return ch.val
		}
	}
	return fallback
}

// laneTail is the last comment given a lane: when it set off, how wide.
type laneTail struct {
	at   time.Duration
	w    float32
	used bool
}

// dmField is where the bullet comments over a video fly: the lane each took
// as it set off, kept as it flies.
type dmField struct {
	lane   map[dy.Danmaku]int // -1: no room, not shown
	tail   [dmLanes]laneTail
	own    dy.Danmaku    // the user's last sent: shown even when the lanes are full
	flight time.Duration // flight duration used when these lanes were computed
	size   float32       // font size used when these lanes were computed
}

// place assigns d a lane using the default flight duration.
func (f *dmField) place(d dy.Danmaku, tw, w float32) int {
	return f.placeIn(d, tw, w, flight)
}

// placeIn gives a comment setting off at at, tw wide, the topmost lane where it
// neither starts on the last one's tail nor, being faster, catches it up
// before that one is gone: -1 when none, as Douyin's drops a crowd's excess.
func (f *dmField) placeIn(d dy.Danmaku, tw, w float32, flightDur time.Duration) int {
	if flightDur <= 0 {
		flightDur = flight
	}
	fits := func(t laneTail) bool {
		if !t.used {
			return true
		}
		sec := float32(flightDur.Seconds())
		v0, v := (w+t.w)/sec, (w+tw)/sec // px a second: wider ones fly faster
		if float32((d.At-t.at).Seconds())*v0 < t.w+dmGap {
			return false
		}
		return v <= v0 || float32((t.at+flightDur-d.At).Seconds())*v <= w-dmGap
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

// flyDanmaku returns the VOD comments of list, in time order, flying at at over
// a video w wide, using the playback-speed-compensated flight duration.
func flyDanmaku[D any](a *app, f *dmField, list []D, key func(D) dy.Danmaku, at time.Duration, w float32) (out []dmFlying, next time.Duration) {
	return flyDanmakuWith(a, f, list, key, at, w, a.dmVideoFlight())
}

// flyDanmakuWith returns the comments of list flying at at over a video w wide
// for flightDur: each from its time on for flightDur, in the lane of f it took
// as it set off. next is how long until the next one sets off, -1 when none is
// to come.
func flyDanmakuWith[D any](a *app, f *dmField, list []D, key func(D) dy.Danmaku, at time.Duration, w float32, flightDur time.Duration) (out []dmFlying, next time.Duration) {
	next = -1
	if w <= 0 {
		return nil, next
	}
	if flightDur <= 0 {
		flightDur = flight
	}
	st := a.danmakuStyle()
	if f.flight != flightDur || f.size != st.Size {
		f.lane = nil
		f.tail = [dmLanes]laneTail{}
		f.flight, f.size = flightDur, st.Size
	}
	laneH := a.dmLaneHeight()
	// list is time-ordered: jump straight to the first comment still in flight.
	start := sort.Search(len(list), func(i int) bool { return key(list[i]).At > at-flightDur })
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
			l = f.placeIn(d, tw, w, flightDur)
			if l < 0 {
				continue
			}
		}
		out = append(out, dmFlying{d, w - float32(age)/float32(flightDur)*(w+tw), float32(dmTop + l*laneH)})
	}
	return out, next
}

// dmShaped is a bullet comment's text shaped once to paint in every frame
// that it flies, and the pictures of its emoji, in the order of its objects.
type dmShaped struct {
	l       *ui.TextLayout
	outline *ui.TextLayout // non-nil for thin/thick stroke modes
	w       float32
	pics    []string
	stroke  string
	opacity float32
}

// danmakuText is text shaped as a bullet comment, kept with its emoji spans,
// so that loading the emoji or changing style shapes it again.
func (a *app) danmakuText(text string) dmShaped {
	st := a.danmakuStyle()
	key := emojiKey{text: text, size: st.Size, weight: dmWeight}
	if e, ok := a.emojiCache[key]; ok && e.dm != nil && e.dm.stroke == st.Stroke && e.dm.opacity == st.Opacity {
		return *e.dm
	}
	a.emojiSpans(text, st.Size, dmWeight)
	e := a.emojiCache[key]
	fillColor := ui.RGBA(255, 255, 255, st.Opacity)
	var shadow ui.TextShadow
	switch st.Stroke {
	case dmStrokeShadow:
		shadow = ui.NewTextShadow(0, 1, 3, ui.RGBA(0, 0, 0, min(1, 0.76*st.Opacity)))
	case dmStrokeThin:
		shadow = ui.NewTextShadow(0, 1, 2, ui.RGBA(0, 0, 0, min(1, 0.45*st.Opacity)))
	}
	spans := make([]ui.Span, len(e.spans))
	for i, sp := range e.spans {
		sp.Color, sp.Shadow = fillColor, shadow
		spans[i] = sp
	}
	font := ui.Font{Size: st.Size, Weight: dmWeight}
	l := ui.ShapeRichText(spans, font, 0)
	var outline *ui.TextLayout
	if st.Stroke == dmStrokeThin || st.Stroke == dmStrokeThick {
		outColor := ui.RGBA(0, 0, 0, min(1, st.Opacity))
		outSpans := make([]ui.Span, len(e.spans))
		for i, sp := range e.spans {
			sp.Color, sp.Shadow = outColor, ui.TextShadow{}
			outSpans[i] = sp
		}
		outline = ui.ShapeRichText(outSpans, font, 0)
	}
	sh := &dmShaped{
		l:       l,
		outline: outline,
		pics:    make([]string, len(e.refs)),
		stroke:  st.Stroke,
		opacity: st.Opacity,
	}
	sh.w, _ = l.Size()
	for i, r := range e.refs {
		sh.pics[i] = r.url
	}
	e.dm = sh
	a.emojiCache[key] = e
	return *sh
}

var (
	dmThinOffsets = [][2]float32{
		{-1, 0}, {1, 0}, {0, -1}, {0, 1},
	}
	dmThickOffsets = [][2]float32{
		{-1.6, 0}, {1.6, 0}, {0, -1.6}, {0, 1.6},
		{-1.2, -1.2}, {1.2, -1.2}, {-1.2, 1.2}, {1.2, 1.2},
	}
)

// danmakuLayer paints over the video the bullet comments fly gives for the
// layer's width. They move in frames that only paint, without building the
// view, dmFPS a second while moving.
func (a *app) danmakuLayer(c *ui.Context, key string, moving bool, fly func(w float32) ([]dmFlying, time.Duration)) {
	if a.dmOff {
		return
	}
	ui.Box(c.Key(key)).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		st := a.danmakuStyle()
		fillColor := ui.RGBA(255, 255, 255, st.Opacity)
		outColor := ui.RGBA(0, 0, 0, min(1, st.Opacity))
		flying, next := fly(r.W)
		for _, f := range flying {
			sh := a.danmakuText(f.d.Text)
			x, y := r.X+f.x, r.Y+f.y
			if sh.outline != nil {
				offs := dmThinOffsets
				if st.Stroke == dmStrokeThick {
					offs = dmThickOffsets
				}
				for _, off := range offs {
					p.TextLayout(sh.outline, x+off[0], y+off[1], outColor)
				}
			}
			p.TextLayout(sh.l, x, y, fillColor)
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

// danmakuSettingsButton draws the compact gear trigger beside the 弹幕 chip
// and rises a popover to tune speed, size, stroke, and opacity live.
func (a *app) danmakuSettingsButton(c *ui.Context, key string, open *bool, onOpen func()) {
	b := iconButton(c.Key(key), iconSettings, 14, onOff(*open, vid.accent, ui.RGBA(255, 255, 255, 0.78)), "弹幕设置")
	if b.Clicked() {
		*open = !*open
		if *open && onOpen != nil {
			onOpen()
		}
	}
	barMenu(c, key+"-menu", b, open, 256, func() {
		st := a.danmakuStyle()
		ui.Column(c).Padding(6, 8).Gap(10).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "弹幕设置").FontSize(13).FontWeight(700).TextColor(white).Grow(1)
				if st != defaultDanmakuStyle {
					r := ui.ButtonBase(c.Key(key+"-reset")).Height(22).PaddingX(7).Radius(6).Center().FocusRing(false).Label("重置弹幕设置")
					if r.Hovered() {
						r.Background(vid.hover)
					}
					r.Children(func() {
						ui.Text(c, "重置").FontSize(11.5).TextColor(vid.muted)
					})
					if r.Clicked() {
						a.setDanmakuStyle(defaultDanmakuStyle)
					}
				}
			})
			dmOptionRow(c, key+"-speed", "速度", dmSpeedChoices, st.Speed, func(v float64) {
				st.Speed = v
				a.setDanmakuStyle(st)
			})
			dmOptionRow(c, key+"-size", "字号", dmSizeChoices, st.Size, func(v float32) {
				st.Size = v
				a.setDanmakuStyle(st)
			})
			dmOptionRow(c, key+"-stroke", "描边", dmStrokeChoices, st.Stroke, func(v string) {
				st.Stroke = v
				a.setDanmakuStyle(st)
			})
			dmOptionRow(c, key+"-opacity", "透明度", dmOpacityChoices, st.Opacity, func(v float32) {
				st.Opacity = v
				a.setDanmakuStyle(st)
			})
			ui.Text(c, "倍速播放时自动保持弹幕阅读速度").FontSize(11).TextColor(vid.faint)
		})
	})
}

func dmOptionRow[T comparable](c *ui.Context, key, title string, choices []dmChoice[T], cur T, onPick func(T)) {
	ui.Column(c.Key(key)).Gap(5).Children(func() {
		ui.Text(c, title).FontSize(11.5).FontWeight(600).TextColor(vid.muted)
		ui.Row(c).Gap(4).Children(func() {
			for i, ch := range choices {
				on := ch.val == cur
				b := ui.ButtonBase(c.Key(key+":"+strconv.Itoa(i))).Grow(1).Height(26).Radius(6).Center().FocusRing(false).Label(title + "：" + ch.label)
				switch {
				case on:
					b.Background(vid.accent)
				case b.Hovered():
					b.Background(vid.press)
				default:
					b.Background(ui.RGBA(255, 255, 255, 0.08))
				}
				b.Children(func() {
					ui.Text(c, ch.label).FontSize(11.5).FontWeight(onOff(on, 700, 500)).TextColor(onOff(on, white, vid.text)).SingleLine()
				})
				if b.Clicked() && !on {
					onPick(ch.val)
				}
			}
		})
	})
}
