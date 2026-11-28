package app

import (
	"image"
	"slices"
	"testing"
	"time"

	"douyin/internal/dy"
)

// hoverFly flies the comments of *list over a 1280×720 video at at with the
// pointer at pt, by their texts.
func hoverFly(a *app, f *dmField, list *[]dy.Danmaku) func(at time.Duration, pt dmPointer) map[string]dmFlying {
	return func(at time.Duration, pt dmPointer) map[string]dmFlying {
		flying, _ := flyDanmakuIn(a, f, *list, sameDanmaku, at, 1280, 720, flight, pt)
		m := map[string]dmFlying{}
		for _, fl := range flying {
			m[fl.d.Text] = fl
		}
		return m
	}
}

func pointerOn(fl dmFlying) dmPointer { return dmPointer{x: fl.x + 10, y: fl.y + 5, over: true} }

// The comment under the pointer stands where it is however long the pointer
// stays, past its own flight and the live room dropping comments that have
// flown; let go with nothing behind it in its lane, it flies on from where it
// stood, not from where its time would have it, and once it has crossed
// nothing of it is kept.
func TestDanmakuHoverHolds(t *testing.T) {
	a := &app{}
	s := &slot{danmaku: []dy.Danmaku{{At: 0, Text: "旁边飞过"}, {At: 100 * time.Millisecond, Text: "停一下看看"}}}
	fly := hoverFly(a, &s.dm, &s.danmaku)
	first := fly(2*time.Second, dmPointer{})["停一下看看"]
	if first.y == fly(2*time.Second, dmPointer{})["旁边飞过"].y {
		t.Fatal("in one lane")
	}
	on := pointerOn(first)
	if got := fly(2*time.Second, on)["停一下看看"]; !got.held || got.x != first.x || got.y != first.y {
		t.Fatalf("pointer on it: %+v, was %+v", got, first)
	}
	s.pruneDanmaku(20*time.Second, flight)
	held := fly(20*time.Second, on)
	if got := held["停一下看看"]; !got.held || got.x != first.x || got.y != first.y || len(held) != 1 {
		t.Fatalf("held past its flight: %v, stood at %+v", held, first)
	}
	if got := fly(21*time.Second, dmPointer{})["停一下看看"]; got.held || got.x != first.x || got.y != first.y {
		t.Fatalf("let go: %+v, stood at %+v", got, first)
	}
	want := first.x - float32(time.Second)/float32(flight)*(1280+a.danmakuText("停一下看看").w)
	if got := fly(22*time.Second, dmPointer{})["停一下看看"]; got.x-want < -0.5 || got.x-want > 0.5 {
		t.Fatalf("a second after letting go at x %v, want %v", got.x, want)
	}
	if got := fly(21*time.Second+flight, dmPointer{}); len(got) != 0 || len(s.dm.held) != 0 || len(s.dm.lane) != 0 {
		t.Fatalf("crossed: %v, held %v, lanes %v", got, s.dm.held, s.dm.lane)
	}
}

// Let go, a comment flies on while the one behind it in its lane is still
// clear of it, and goes rather than fly on over one that came up on it, or
// flew past it, as it stood; one setting off after it was let go keeps clear
// of where it is.
func TestDanmakuHoverLetGoKeepsClear(t *testing.T) {
	a := &app{}
	list := []dy.Danmaku{{At: 0, Text: "第一条弹幕"}, {At: 2 * time.Second, Text: "第二条弹幕"}}
	fly := hoverFly(a, &dmField{}, &list)
	at := fly(2500*time.Millisecond, dmPointer{})
	if at["第一条弹幕"].y != at["第二条弹幕"].y {
		t.Fatalf("not one lane: %v", at)
	}
	stood := fly(2500*time.Millisecond, pointerOn(at["第一条弹幕"]))["第一条弹幕"]
	if got := fly(2800*time.Millisecond, dmPointer{})["第一条弹幕"]; got.held || got.x != stood.x {
		t.Fatalf("let go well ahead of the next: %+v, stood at %+v", got, stood)
	}
	at = fly(3*time.Second, dmPointer{})
	fly(3*time.Second, pointerOn(at["第一条弹幕"]))
	if got := fly(5*time.Second, dmPointer{}); len(got) != 1 || got["第二条弹幕"].d.Text == "" {
		t.Fatalf("let go as the next flew past it: %v", got)
	}

	list = []dy.Danmaku{{At: 0, Text: "第一条弹幕"}}
	fly = hoverFly(a, &dmField{}, &list)
	held := fly(300*time.Millisecond, dmPointer{})["第一条弹幕"]
	fly(300*time.Millisecond, pointerOn(held))
	fly(time.Second, dmPointer{})
	list = append(list, dy.Danmaku{At: 1100 * time.Millisecond, Text: "第三条弹幕"})
	if got := fly(1200*time.Millisecond, dmPointer{}); got["第三条弹幕"].y == got["第一条弹幕"].y {
		t.Fatalf("set off over the one let go: %v", got)
	}
}

// A merged comment let go where it would run into the next takes its count
// with it: the same said after shows by itself rather than fold into one no
// longer shown.
func TestDanmakuHoverLeftMergedLetsRepeatsShow(t *testing.T) {
	a := &app{}
	list := []dy.Danmaku{{At: 0, Text: "来了"}, {At: 100 * time.Millisecond, Text: "来了"}, {At: time.Second, Text: "别的弹幕"}}
	fly := hoverFly(a, &dmField{}, &list)
	at := fly(1200*time.Millisecond, dmPointer{})
	if at["来了"].n != 2 || at["来了"].y != at["别的弹幕"].y {
		t.Fatalf("not merged ahead of the next: %v", at)
	}
	fly(1200*time.Millisecond, pointerOn(at["来了"]))
	if got := fly(2200*time.Millisecond, dmPointer{}); got["来了"].d.Text != "" {
		t.Fatalf("let go as the next flew past it: %v", got)
	}
	list = append(list, dy.Danmaku{At: 2300 * time.Millisecond, Text: "来了"})
	if got := fly(2500*time.Millisecond, dmPointer{})["来了"]; got.d.At != 2300*time.Millisecond || got.n != 1 {
		t.Fatalf("said again after: %+v", got)
	}
}

// In the player, resting the pointer on a flying comment holds it there on a
// pill as the video plays on; moving off, it flies on from there.
func TestDanmakuHoverInPlayer(t *testing.T) {
	p := stub("推荐", 1)
	s := p.list[0]
	s.v = dy.Video{ID: "v", Author: "作者", Duration: time.Minute}
	ready(t, s)
	s.resume = 5 * time.Second
	s.danmaku = []dy.Danmaku{{At: 0, Text: "停下来让我看清楚这一条"}}
	a := withRoots(&app{pages: []*page{p}})
	tt := newTester(a)
	tt.Frame()
	scale := float32(tt.Image().Rect.Dx()) / 1100
	// shown is where the layer paints, in DIPs: what differs with comments off
	// in the top half, where they fly, the bar's 弹幕 switch being below.
	shown := func() (r image.Rectangle) {
		on := slices.Clone(tt.Image().Pix)
		a.dmOff = true
		tt.Frame()
		off := tt.Image()
		for y := off.Rect.Min.Y; y < off.Rect.Dy()/2; y++ {
			for x := off.Rect.Min.X; x < off.Rect.Max.X; x++ {
				if i := off.PixOffset(x, y); !slices.Equal(on[i:i+4], off.Pix[i:i+4]) {
					r = r.Union(image.Rect(x, y, x+1, y+1))
				}
			}
		}
		a.dmOff = false
		tt.Frame()
		return image.Rect(int(float32(r.Min.X)/scale), int(float32(r.Min.Y)/scale), int(float32(r.Max.X)/scale), int(float32(r.Max.Y)/scale))
	}
	mid := func(r image.Rectangle) int { return (r.Min.X + r.Max.X) / 2 }
	flying := shown()
	if flying.Empty() {
		t.Fatal("no comment painted")
	}
	ptr := image.Pt(mid(flying), flying.Min.Y+flying.Dy()/2)
	tt.Move(float32(ptr.X), float32(ptr.Y))
	tt.Frame()
	held := shown()
	s.resume += 1500 * time.Millisecond
	tt.Advance(time.Second / 60)
	if still := shown(); still != held || !ptr.In(held) || held.Dx() < flying.Dx()+10 || len(s.dm.held) != 1 {
		t.Fatalf("held under the pointer at %v: painted at %v, then %v as the video played on; flying it was %v", ptr, held, still, flying)
	}
	tt.Move(5, 5)
	tt.Frame()
	s.resume += 500 * time.Millisecond
	tt.Advance(time.Second / 60)
	if after := shown(); mid(held)-mid(after) < 20 || mid(held)-mid(after) > 120 || after.Dx() >= held.Dx() {
		t.Fatalf("let go: painted at %v, held at %v", after, held)
	}
}
