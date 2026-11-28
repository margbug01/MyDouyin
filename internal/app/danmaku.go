package app

import (
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

// flight is how long a bullet comment takes to cross the video at 1x speed.
const flight = 8 * time.Second

const dmLanes, dmLanesMax, dmGap = 7, 24, 24 // default/max lanes over the video; px kept between two in a lane

const (
	dmTop, dmLaneH = 14, 30 // default first lane offset and lane height
	dmSize         = 18     // default font size in DIPs
	dmWeightBold   = 700    // Bilibili-style bold default
	dmWeightNormal = 500
	dmAreaDefault  = 0.5 // half screen
)

const (
	dmStrokeThick  = "thick"  // 重墨 (Bilibili default: crisp 8-dir 1px outline + subtle shadow)
	dmStrokeThin   = "thin"   // 描边 (tight 8-dir crisp outline)
	dmStrokeShadow = "shadow" // 45°投影 (crisp rim + 45° drop shadow)
	dmStrokeNone   = "none"   // 无
)

// danmakuStyle is the user's bullet-comment appearance and crossing speed.
type danmakuStyle struct {
	Speed   float64 // crossing speed multiplier relative to 1x (0.5..1.5)
	Size    float32 // font size in DIPs (12..32)
	Weight  int     // font weight: dmWeightBold (700) or dmWeightNormal (500)
	Stroke  string  // dmStrokeThick, dmStrokeThin, dmStrokeShadow, dmStrokeNone
	Opacity float32 // group opacity (0.3..1.0)
	Area    float32 // display height fraction (0.25..1.0)
}

var defaultDanmakuStyle = danmakuStyle{
	Speed:   1,
	Size:    dmSize,
	Weight:  dmWeightBold,
	Stroke:  dmStrokeThick,
	Opacity: 1,
	Area:    dmAreaDefault,
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
		{15, "小"},
		{18, "标准"},
		{21, "大"},
		{25, "超大"},
	}
	dmWeightChoices = []dmChoice[int]{
		{dmWeightBold, "粗体"},
		{dmWeightNormal, "常规"},
	}
	dmStrokeChoices = []dmChoice[string]{
		{dmStrokeThick, "重墨"},
		{dmStrokeThin, "描边"},
		{dmStrokeShadow, "45°投影"},
		{dmStrokeNone, "无"},
	}
	dmOpacityChoices = []dmChoice[float32]{
		{1, "100%"},
		{0.85, "85%"},
		{0.75, "75%"},
		{0.55, "55%"},
	}
	dmAreaChoices = []dmChoice[float32]{
		{0.25, "1/4屏"},
		{0.5, "半屏"},
		{0.75, "3/4屏"},
		{1, "满屏"},
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
	switch {
	case s.Weight <= 0, s.Weight >= 600:
		s.Weight = dmWeightBold
	default:
		s.Weight = dmWeightNormal
	}
	switch s.Stroke {
	case dmStrokeThick, dmStrokeThin, dmStrokeShadow, dmStrokeNone:
	default:
		s.Stroke = defaultDanmakuStyle.Stroke
	}
	if s.Opacity <= 0 {
		s.Opacity = defaultDanmakuStyle.Opacity
	} else {
		s.Opacity = max(0.3, min(1, s.Opacity))
	}
	if s.Area <= 0 {
		s.Area = defaultDanmakuStyle.Area
	} else {
		s.Area = max(0.25, min(1, s.Area))
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

func (a *app) dmLaneCount(h float32) int {
	st := a.danmakuStyle()
	if h <= 0 {
		return max(2, min(dmLanesMax, int(math.Round(float64(dmLanes*2)*float64(st.Area)))))
	}
	usable := h*st.Area - dmTop
	if usable <= 0 {
		return 2
	}
	return max(2, min(dmLanesMax, int(usable)/max(1, a.dmLaneHeight())))
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

// laneTail is the last comment given a lane: when it set off, how wide it was
// then, which sets how fast it flies, and the merged comment it is, if it is
// one, whose count's pill can widen it as it flies.
type laneTail struct {
	at   time.Duration
	w    float32
	g    *dmGroup
	used bool
}

// room is how wide t is now.
func (t laneTail) room() float32 {
	if t.g != nil {
		return t.g.w
	}
	return t.w
}

// keepsClear reports whether a comment setting off at at, tw wide, stays dmGap
// behind t from from on while t is room wide: neither on t's tail nor, being
// faster, catching it up before all of t has gone, every comment crossing w px
// in flightDur.
func (t laneTail) keepsClear(room float32, from, at time.Duration, tw, w float32, flightDur time.Duration) bool {
	if !t.used {
		return true
	}
	sec := float32(flightDur.Seconds())
	v0, v := (w+t.w)/sec, (w+tw)/sec // px a second: wider ones fly faster
	if float32((from-t.at).Seconds())*v0-float32((from-at).Seconds())*v < room+dmGap {
		return false
	}
	gone := (w + room) / v0 // seconds after t set off until all of it has left
	return v <= v0 || (gone-float32((at-t.at).Seconds()))*v <= w-dmGap
}

// dmGroup is a shown bullet comment and the identical ones folded into it:
// how many it stands for, how wide it flies with its count's pill, until when
// repeats fold in, and what set off next behind it in its lane, which a wider
// pill must not run into.
type dmGroup struct {
	d      dy.Danmaku
	n      int
	w      float32
	until  time.Duration
	behind laneTail
}

// dmField is where the bullet comments over a video fly: the lane each took
// as it set off, kept as it flies.
type dmField struct {
	lane   map[dy.Danmaku]int // -1: blocked, folded into a repeat, or no room: not shown
	tail   [dmLanesMax]laneTail
	own    dy.Danmaku    // the user's last sent: shown even when the lanes are full
	flight time.Duration // flight duration used when these lanes were computed
	size   float32       // font size used when these lanes were computed
	lanes  int           // active lane count used when these lanes were computed
	merge  bool          // repeats merging when these lanes were computed
	block  []string      // blocked words when these lanes were computed
	group  map[dy.Danmaku]*dmGroup
	repeat map[string]*dmGroup // by dmFoldKey: the latest shown, which repeats fold into
	linger time.Duration       // how long past its flight a merged comment's pill can be on screen
}

func (f *dmField) hide(d dy.Danmaku) {
	if f.lane == nil {
		f.lane = map[dy.Danmaku]int{}
	}
	f.lane[d] = -1
}

// dmFoldKey is what two bullet comments share when they say the same: their
// letters, digits and emoji, any case, so 来了, 来了！ and 来 了~ are one.
func dmFoldKey(text string) string {
	k := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || unicode.Is(unicode.So, r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
	if k == "" { // ？？？ is said by itself
		return text
	}
	return k
}

const dmBadgeGap = 5 // px between a merged comment's text and its count

// dmBadge is the pill a merged comment's count is drawn in after its text,
// hotter as more say it: gold, then orange from 10, then Douyin's red from 50.
type dmBadge struct {
	l             *ui.TextLayout // ×12, the × smaller, on one baseline
	lw, lh, w, h  float32        // the text's size; the pill's
	from, to, ink ui.Color
}

type dmBadgeKey struct {
	n    int
	size float32
}

// danmakuBadge is the count n's pill at the style's size, shaped once.
func (a *app) danmakuBadge(n int) *dmBadge {
	size := a.danmakuStyle().Size
	k := dmBadgeKey{n, size}
	if b := a.dmBadges[k]; b != nil {
		return b
	}
	b := &dmBadge{from: ui.Hex("#ffe58a"), to: ui.Hex("#ffc23d"), ink: ui.Hex("#5a3300")}
	switch {
	case n >= 50:
		b.from, b.to, b.ink = ui.Hex("#ff6b8b"), ui.Hex("#fe2c55"), white
	case n >= 10:
		b.from, b.to, b.ink = ui.Hex("#ffb547"), ui.Hex("#ff7a1a"), white
	}
	b.l = ui.ShapeRichText([]ui.Span{
		{Text: "×", Size: size * 0.62, Weight: 800, Italic: true, Color: b.ink},
		{Text: strconv.Itoa(n), Size: size * 0.84, Weight: 900, Italic: true, Color: b.ink},
	}, ui.Font{Size: size * 0.84, Weight: 900}, 0)
	b.lw, b.lh = b.l.Size()
	b.h = size * 1.02
	b.w = b.lw + b.h*0.62
	if a.dmBadges == nil {
		a.dmBadges = map[dmBadgeKey]*dmBadge{}
	}
	a.dmBadges[k] = b
	return b
}

// paint draws the pill at x, centered on a line of text textH tall.
func (b *dmBadge) paint(p *ui.Painter, x, textH float32) {
	r := ui.Rect{X: x + dmBadgeGap, Y: (textH - b.h) / 2, W: b.w, H: b.h}
	p.Shadow(r, b.h/2, 0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.5))
	p.FillGradient(r, ui.LinearGradient{Angle: 90, From: b.from, To: b.to}, b.h/2)
	p.Stroke(r, ui.RGBA(255, 255, 255, 0.28), b.h/2, 1)
	p.TextLayout(b.l, r.X+(b.w-b.lw)/2, r.Y+(b.h-b.lh)/2, b.ink)
}

// danmakuWidth is how wide a comment standing for n is, its count's pill with it.
func (a *app) danmakuWidth(text string, n int) float32 {
	w := a.danmakuText(text).w
	if n > 1 {
		w += dmBadgeGap + a.danmakuBadge(n).w
	}
	return w
}

// danmakuRoom is how wide a comment standing for n flies in its lane: from 2
// on, its pill kept as wide as for 99 (or 999, …), so that its count grows
// digit by digit without the pill running into what follows.
func (a *app) danmakuRoom(text string, n int) float32 {
	if n < 2 {
		return a.danmakuText(text).w
	}
	nines := 99
	for nines < n {
		nines = nines*10 + 9
	}
	return max(a.danmakuWidth(text, n), a.danmakuWidth(text, nines))
}

// dmBlocked reports whether text has one of the user's blocked words.
func (a *app) dmBlocked(text string) bool {
	if len(a.dmBlock) == 0 {
		return false
	}
	text = strings.ToLower(text)
	for _, w := range a.dmBlock {
		if strings.Contains(text, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// setDanmakuBlock keeps words, trimmed, once each, as the blocked words. It
// always makes a new slice: a dmField compares the one it was laid out with.
func (a *app) setDanmakuBlock(words []string) {
	var out []string
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, w) }) {
			out = append(out, w)
		}
	}
	a.dmBlock = out
}

// dmSetOff decides where d flies as it sets off: -1 when it is not shown, for
// a blocked word, for folding into an identical one still on the right half,
// or for the lanes being full. A folded one keeps its room in a lane empty, so
// merging only takes comments away and never lets in one the lanes had no
// room for.
func (a *app) dmSetOff(f *dmField, d dy.Danmaku, w float32, flightDur time.Duration, lanes int) int {
	own := d == f.own
	if !own && a.dmBlocked(d.Text) {
		f.hide(d)
		return -1
	}
	tw := a.danmakuText(d.Text).w
	if !f.merge || own {
		return f.placeIn(d, tw, w, flightDur, lanes)
	}
	k := dmFoldKey(d.Text)
	if g := f.repeat[k]; g != nil && d.At <= g.until && a.dmFold(f, g, d.At, w, flightDur) {
		f.placeIn(d, tw, w, flightDur, lanes)
		f.hide(d)
		return -1
	}
	l := f.placeIn(d, tw, w, flightDur, lanes)
	if l < 0 {
		return -1
	}
	g := &dmGroup{d: d, n: 1, w: tw, until: d.At + time.Duration(float64(flightDur)*float64(w/2)/float64(w+tw))}
	f.tail[l].g = g
	if f.group == nil {
		f.group, f.repeat = map[dy.Danmaku]*dmGroup{}, map[string]*dmGroup{}
	}
	f.group[d], f.repeat[k] = g, g
	return l
}

// dmFold counts one more, said at at, into g, when g's pill can widen as the
// count needs without running into what set off behind it.
func (a *app) dmFold(f *dmField, g *dmGroup, at time.Duration, w float32, flightDur time.Duration) bool {
	room := a.danmakuRoom(g.d.Text, g.n+1)
	if room > g.w {
		tw := a.danmakuText(g.d.Text).w
		if b := g.behind; b.used && !(laneTail{at: g.d.At, w: tw, used: true}).keepsClear(room, at, b.at, b.w, w, flightDur) {
			return false
		}
		g.w = room
		f.linger = max(f.linger, time.Duration(float64(flightDur)*float64(room-tw)/float64(w+tw)))
	}
	g.n++
	return true
}

// place assigns d a lane using the default flight duration.
func (f *dmField) place(d dy.Danmaku, tw, w float32) int {
	return f.placeIn(d, tw, w, flight, dmLanes)
}

// placeIn gives a comment setting off at at, tw wide, the topmost lane where it
// keeps clear of the last one: -1 when none, as Douyin's drops a crowd's excess.
func (f *dmField) placeIn(d dy.Danmaku, tw, w float32, flightDur time.Duration, maxLanes int) int {
	if flightDur <= 0 {
		flightDur = flight
	}
	if maxLanes <= 0 {
		maxLanes = dmLanes
	}
	maxLanes = min(maxLanes, dmLanesMax)
	lane := -1
	for i := range maxLanes {
		if t := f.tail[i]; t.keepsClear(t.room(), d.At, d.At, tw, w, flightDur) {
			lane = i
			break
		}
	}
	if lane < 0 && d == f.own { // the user's own shows anyway, where the last one set off longest ago
		lane = 0
		for i := range maxLanes {
			if f.tail[i].at < f.tail[lane].at {
				lane = i
			}
		}
	}
	if lane >= 0 {
		t := laneTail{at: d.At, w: tw, used: true}
		if g := f.tail[lane].g; g != nil {
			g.behind = t
		}
		f.tail[lane] = t
	}
	if f.lane == nil {
		f.lane = map[dy.Danmaku]int{}
	}
	f.lane[d] = lane
	return lane
}

// dmFlying is a bullet comment where it is at a moment, in the layer, and how
// many identical ones it stands for.
type dmFlying struct {
	d    dy.Danmaku
	x, y float32
	n    int
}

// flyDanmaku returns the VOD comments of list, in time order, flying at at over
// a video w wide, using the playback-speed-compensated flight duration.
func flyDanmaku[D any](a *app, f *dmField, list []D, key func(D) dy.Danmaku, at time.Duration, w float32) (out []dmFlying, next time.Duration) {
	return flyDanmakuIn(a, f, list, key, at, w, 0, a.dmVideoFlight())
}

// flyDanmakuArea returns the VOD comments flying at at over a video w×h,
// sizing the number of lanes to the user's configured display area.
func flyDanmakuArea[D any](a *app, f *dmField, list []D, key func(D) dy.Danmaku, at time.Duration, w, h float32) (out []dmFlying, next time.Duration) {
	return flyDanmakuIn(a, f, list, key, at, w, h, a.dmVideoFlight())
}

// flyDanmakuWith returns the comments of list flying at at over a video w wide
// for flightDur.
func flyDanmakuWith[D any](a *app, f *dmField, list []D, key func(D) dy.Danmaku, at time.Duration, w float32, flightDur time.Duration) (out []dmFlying, next time.Duration) {
	return flyDanmakuIn(a, f, list, key, at, w, 0, flightDur)
}

// flyDanmakuIn returns the comments of list flying at at over a video w×h,
// each from its time on, crossing in flightDur at the speed it set off with
// until all of it, its count's pill too, has gone, in the lane of f it took as
// it set off. next is how long until the next one sets off, -1 when none is to
// come.
func flyDanmakuIn[D any](a *app, f *dmField, list []D, key func(D) dy.Danmaku, at time.Duration, w, h float32, flightDur time.Duration) (out []dmFlying, next time.Duration) {
	next = -1
	if w <= 0 {
		return nil, next
	}
	if flightDur <= 0 {
		flightDur = flight
	}
	st := a.danmakuStyle()
	lanes := a.dmLaneCount(h)
	merge := !a.dmMergeOff
	if f.flight != flightDur || f.size != st.Size || f.lanes != lanes || f.merge != merge || !slices.Equal(f.block, a.dmBlock) {
		f.lane, f.group, f.repeat = nil, nil, nil
		f.tail = [dmLanesMax]laneTail{}
		f.flight, f.size, f.lanes, f.merge, f.block = flightDur, st.Size, lanes, merge, a.dmBlock
	}
	laneH := a.dmLaneHeight()
	// list is time-ordered: jump straight to the first comment still in flight.
	start := sort.Search(len(list), func(i int) bool { return key(list[i]).At > at-flightDur-f.linger })
	for i := start; i < len(list); i++ {
		d := key(list[i])
		age := at - d.At
		if age < 0 {
			next = -age
			break
		}
		l, ok := f.lane[d]
		if !ok {
			if age >= flightDur { // crossed before it was seen: skipped past
				continue
			}
			l = a.dmSetOff(f, d, w, flightDur, lanes)
		}
		if l < 0 {
			continue
		}
		tw := a.danmakuText(d.Text).w
		x, room := w-float32(age)/float32(flightDur)*(w+tw), tw
		if g := f.group[d]; g != nil {
			room = g.w
		}
		if x+room <= 0 {
			continue
		}
		out = append(out, dmFlying{d, x, float32(dmTop + l*laneH), 1})
	}
	for i := range out { // counted now, with the repeats that set off after it this frame
		if g := f.group[out[i].d]; g != nil {
			out[i].n = g.n
		}
	}
	return out, next
}

// dmShaped is a bullet comment's text shaped once to paint in every frame
// that it flies, and the pictures of its emoji, in the order of its objects.
type dmShaped struct {
	l        *ui.TextLayout
	outline  *ui.TextLayout // non-nil when stroke mode draws an outline pass
	offsets  [][2]float32
	outColor ui.Color
	w        float32
	pics     []string
	stroke   string
}

var (
	dmHeavyOffsets = [][2]float32{
		{-1, 0}, {1, 0}, {0, -1}, {0, 1},
		{-0.75, -0.75}, {0.75, -0.75}, {-0.75, 0.75}, {0.75, 0.75},
	}
	dmThinOffsets = [][2]float32{
		{-0.85, 0}, {0.85, 0}, {0, -0.85}, {0, 0.85},
		{-0.6, -0.6}, {0.6, -0.6}, {-0.6, 0.6}, {0.6, 0.6},
	}
	dmShadowOffsets = [][2]float32{
		{-0.7, 0}, {0.7, 0}, {0, -0.7}, {0, 0.7},
		{1.0, 1.0}, {1.4, 1.4},
	}
)

// danmakuText is text shaped as a bullet comment, kept with its emoji spans,
// so that loading the emoji or changing style shapes it again.
// Text and outline are always shaped at full opacity; st.Opacity is applied as
// a composited group in danmakuLayer so semi-transparent white never grays out
// over its own black outline.
func (a *app) danmakuText(text string) dmShaped {
	st := a.danmakuStyle()
	key := emojiKey{text: text, size: st.Size, weight: st.Weight}
	if e, ok := a.emojiCache[key]; ok && e.dm != nil && e.dm.stroke == st.Stroke {
		return *e.dm
	}
	a.emojiSpans(text, st.Size, st.Weight)
	e := a.emojiCache[key]
	var (
		shadow   ui.TextShadow
		offsets  [][2]float32
		outColor ui.Color
	)
	switch st.Stroke {
	case dmStrokeThick:
		offsets = dmHeavyOffsets
		outColor = ui.RGBA(0, 0, 0, 0.92)
		shadow = ui.NewTextShadow(0, 1, 1.5, ui.RGBA(0, 0, 0, 0.5))
	case dmStrokeThin:
		offsets = dmThinOffsets
		outColor = ui.RGBA(0, 0, 0, 0.88)
	case dmStrokeShadow:
		offsets = dmShadowOffsets
		outColor = ui.RGBA(0, 0, 0, 0.78)
		shadow = ui.NewTextShadow(1.2, 1.4, 1.8, ui.RGBA(0, 0, 0, 0.75))
	}
	spans := make([]ui.Span, len(e.spans))
	for i, sp := range e.spans {
		sp.Color, sp.Shadow = white, shadow
		spans[i] = sp
	}
	font := ui.Font{Size: st.Size, Weight: st.Weight}
	l := ui.ShapeRichText(spans, font, 0)
	var outline *ui.TextLayout
	if len(offsets) > 0 {
		outSpans := make([]ui.Span, len(e.spans))
		for i, sp := range e.spans {
			sp.Color, sp.Shadow = outColor, ui.TextShadow{}
			outSpans[i] = sp
		}
		outline = ui.ShapeRichText(outSpans, font, 0)
	}
	sh := &dmShaped{
		l:        l,
		outline:  outline,
		offsets:  offsets,
		outColor: outColor,
		pics:     make([]string, len(e.refs)),
		stroke:   st.Stroke,
	}
	sh.w, _ = l.Size()
	for i, r := range e.refs {
		sh.pics[i] = r.url
	}
	e.dm = sh
	a.emojiCache[key] = e
	return *sh
}

// danmakuLayer paints over the video the bullet comments fly gives for the
// layer's size. They move in repaint-only frames at the display's native
// refresh rate, without building the view, using GPU subpixel translation so
// glyphs and their outlines glide smoothly without pixel-snap jitter.
//
// fly gets late, how long ago the frame began while they move: the layer
// paints up to a frame's length after that (9 ms measured at 144 Hz), so a
// clock read in fly, set back by late, places each frame's comments at the
// frame's own time and they move evenly, not by how late their layer painted.
func (a *app) danmakuLayer(c *ui.Context, key string, moving bool, fly func(w, h float32, late time.Duration) ([]dmFlying, time.Duration)) {
	if a.dmOff {
		return
	}
	st := a.danmakuStyle()
	draw := func(p *ui.Painter, r ui.Rect) {
		var late time.Duration
		if moving {
			late = time.Since(p.Now())
		}
		flying, next := fly(r.W, r.H, late)
		for _, f := range flying {
			sh := a.danmakuText(f.d.Text)
			if f.x+a.danmakuWidth(f.d.Text, f.n) <= 0 || f.x >= r.W {
				continue
			}
			x := r.X + f.x
			y := float32(math.Round(float64(r.Y + f.y)))
			if !moving {
				x = float32(math.Round(float64(x)))
			}
			p.Transform(ui.Translation(x, y), 0, 0, func() {
				if sh.outline != nil {
					for _, off := range sh.offsets {
						p.TextLayout(sh.outline, off[0], off[1], sh.outColor)
					}
				}
				p.TextLayout(sh.l, 0, 0, white)
				for i, o := range sh.l.Objects() {
					if i < len(sh.pics) {
						if bmp := a.img.getSized(sh.pics[i], emojiEdge); bmp != nil {
							p.Image(bmp, ui.Rect{X: o.Rect.X, Y: o.Rect.Y, W: o.Rect.W, H: o.Rect.H}, ui.Contain)
						}
					}
				}
				if f.n > 1 {
					_, h := sh.l.Size()
					a.danmakuBadge(f.n).paint(p, sh.w, h)
				}
			})
		}
		switch {
		case !moving:
		case len(flying) > 0:
			p.AnimationFrame()
		case next >= 0:
			p.After(next)
		}
	}
	if st.Opacity < 0.999 {
		ui.Box(c.Key(key)).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Opacity(st.Opacity).Children(func() {
			ui.Box(c.Key(key + ":canvas")).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(draw)
		})
		return
	}
	ui.Box(c.Key(key)).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(draw)
}

// danmakuSettingsButton draws the compact gear trigger beside the 弹幕 chip
// and rises a popover to tune speed, size, weight, stroke, opacity, and area live.
func (a *app) danmakuSettingsButton(c *ui.Context, key string, open *bool, onOpen func()) {
	b := iconButton(c.Key(key), iconSettings, 14, onOff(*open, vid.accent, ui.RGBA(255, 255, 255, 0.78)), "弹幕设置")
	if b.Clicked() {
		*open = !*open
		if *open && onOpen != nil {
			onOpen()
		}
	}
	barMenu(c, key+"-menu", b, open, 268, func() {
		st := a.danmakuStyle()
		ui.Column(c).Padding(6, 8).Gap(9).Children(func() {
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
			dmOptionRow(c, key+"-stroke", "描边", dmStrokeChoices, st.Stroke, func(v string) {
				st.Stroke = v
				a.setDanmakuStyle(st)
			})
			ui.Column(c.Key(key + "-size")).Gap(5).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "字号").FontSize(11.5).FontWeight(600).TextColor(vid.muted).Grow(1)
					bold := st.Weight >= dmWeightBold
					if dmToggle(c, key+"-bold", "粗体", "字重：粗体", bold).Clicked() {
						if bold {
							st.Weight = dmWeightNormal
						} else {
							st.Weight = dmWeightBold
						}
						a.setDanmakuStyle(st)
					}
				})
				dmOptionButtons(c, key+"-size", "字号", dmSizeChoices, st.Size, func(v float32) {
					st.Size = v
					a.setDanmakuStyle(st)
				})
			})
			dmOptionRow(c, key+"-opacity", "不透明度", dmOpacityChoices, st.Opacity, func(v float32) {
				st.Opacity = v
				a.setDanmakuStyle(st)
			})
			dmOptionRow(c, key+"-area", "显示区域", dmAreaChoices, st.Area, func(v float32) {
				st.Area = v
				a.setDanmakuStyle(st)
			})
			dmOptionRow(c, key+"-speed", "速度", dmSpeedChoices, st.Speed, func(v float64) {
				st.Speed = v
				a.setDanmakuStyle(st)
			})
			ui.Text(c, "倍速播放时自动保持弹幕阅读速度").FontSize(11).TextColor(vid.faint)
			ui.Row(c.Key(key+"-filter")).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "过滤").FontSize(11.5).FontWeight(600).TextColor(vid.muted).Grow(1)
				if dmToggle(c, key+"-merge", "合并重复", "合并重复弹幕", !a.dmMergeOff).Clicked() {
					a.dmMergeOff = !a.dmMergeOff
				}
				block := "屏蔽词"
				if n := len(a.dmBlock); n > 0 {
					block += " " + strconv.Itoa(n)
				}
				if dmToggle(c, key+"-block", block, "管理屏蔽词", len(a.dmBlock) > 0).Clicked() {
					*open = false
					a.openSettings(settingsDanmaku)
				}
			})
		})
	})
}

// dmToggle is a small pill of the danmaku popover, lit while on.
func dmToggle(c *ui.Context, key, text, label string, on bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Height(20).PaddingX(8).Radius(5).Center().FocusRing(false).Label(label)
	switch {
	case on:
		b.Background(vid.accent.Alpha(0.22)).Border(1, vid.accent.Alpha(0.55))
	case b.Hovered():
		b.Background(vid.press)
	default:
		b.Background(ui.RGBA(255, 255, 255, 0.08))
	}
	return b.Children(func() {
		ui.Text(c, text).FontSize(11).FontWeight(onOff(on, 700, 500)).TextColor(onOff(on, white, vid.muted)).SingleLine()
	})
}

func dmOptionRow[T comparable](c *ui.Context, key, title string, choices []dmChoice[T], cur T, onPick func(T)) {
	ui.Column(c.Key(key)).Gap(5).Children(func() {
		ui.Text(c, title).FontSize(11.5).FontWeight(600).TextColor(vid.muted)
		dmOptionButtons(c, key, title, choices, cur, onPick)
	})
}

func dmOptionButtons[T comparable](c *ui.Context, key, title string, choices []dmChoice[T], cur T, onPick func(T)) {
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
}
