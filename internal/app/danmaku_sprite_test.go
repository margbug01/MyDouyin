package app

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

// Translucent danmaku (Opacity < 1) renders through per-comment cached canvases
// so it keeps repaint-only frames without rebuilding the view, reuses the same
// canvas across flying frames, and actually dims the rendered pixels.
func TestDanmakuSpriteCacheReuseAndOpacity(t *testing.T) {
	s := &slot{danmaku: []dy.Danmaku{
		{At: 0, Text: "来了"},
		{At: 50 * time.Millisecond, Text: "来了"},
		{At: 100 * time.Millisecond, Text: "透明描边弹幕测试"},
	}}
	a := &app{}
	a.setDanmakuStyle(danmakuStyle{Speed: 1, Size: 18, Weight: 700, Stroke: dmStrokeShadow, Opacity: 0.55, Area: 0.5})
	at, builds := 2*time.Second, 0
	tt := ui.NewTester(func(c *ui.Context) {
		builds++
		ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).Children(func() {
			a.danmakuLayer(c, "dm", true, func(w, h float32, _ time.Duration, pt dmPointer) ([]dmFlying, time.Duration) {
				return flyDanmakuArea(a, &s.dm, s.danmaku, sameDanmaku, at, w, h, pt)
			})
		})
	}, 800, 360)
	frame0 := slices.Clone(tt.Image().Pix)
	maxLuma := func(pix []byte) uint8 {
		var m uint8
		for i := 0; i < len(pix); i += 4 {
			m = max(m, pix[i], pix[i+1], pix[i+2])
		}
		return m
	}
	dimLuma := maxLuma(frame0)
	if dimLuma < 80 || dimLuma > 180 {
		t.Fatalf("opacity 0.55 peak channel = %d, want dimmed range [80, 180]", dimLuma)
	}
	sh := a.danmakuText("透明描边弹幕测试")
	sp0 := a.dmSprites.m[sh]
	if sp0 == nil || sp0.cv == nil {
		t.Fatal("expected cached sprite canvas for flying comment")
	}
	if b := a.danmakuBadge(2); b == nil || b.cv == nil {
		t.Fatal("expected cached canvas for merged count badge")
	}
	beforeBuilds := builds
	at += 400 * time.Millisecond
	tt.Advance(time.Second / 30)
	if builds != beforeBuilds {
		t.Fatalf("opacity < 1 moving rebuilt the view %d times", builds-beforeBuilds)
	}
	if slices.Equal(frame0, tt.Image().Pix) {
		t.Fatal("danmaku did not move on repaint-only frame")
	}
	if sp1 := a.dmSprites.m[sh]; sp1 == nil || sp1.cv != sp0.cv {
		t.Fatal("expected sprite canvas to be reused across flying frames")
	}

	// Changing opacity to 1.0 brightens the output without invalidating the baked canvas.
	a.setDanmakuStyle(danmakuStyle{Speed: 1, Size: 18, Weight: 700, Stroke: dmStrokeShadow, Opacity: 1, Area: 0.5})
	tt.Frame()
	if fullLuma := maxLuma(tt.Image().Pix); fullLuma < 220 {
		t.Fatalf("opacity 1.0 peak channel = %d, want >= 220", fullLuma)
	}
	if sp2 := a.dmSprites.m[sh]; sp2 == nil || sp2.cv != sp0.cv {
		t.Fatal("changing opacity alone should reuse the baked canvas")
	}

	// Changing stroke or display scale invalidates and re-bakes.
	a.setDanmakuStyle(danmakuStyle{Speed: 1, Size: 18, Weight: 700, Stroke: dmStrokeThick, Opacity: 1, Area: 0.5})
	if len(a.dmSprites.m) != 0 {
		t.Fatalf("changing stroke left %d stale sprites", len(a.dmSprites.m))
	}
	tt.Frame()
	shThick := a.danmakuText("透明描边弹幕测试")
	spThick := a.dmSprites.m[shThick]
	if spThick == nil || spThick.scale != 1 {
		t.Fatalf("expected re-baked sprite at scale 1, got %+v", spThick)
	}
	tt.SetScale(2)
	tt.Frame()
	sp2x := a.dmSprites.m[shThick]
	if sp2x == nil || sp2x.scale != 2 || sp2x.pxW <= spThick.pxW {
		t.Fatalf("expected re-baked 2x sprite: 1x=%+v 2x=%+v", spThick, sp2x)
	}

	// Turning danmaku off drops cached GPU canvases.
	a.dmOff = true
	tt.Frame()
	if len(a.dmSprites.m) != 0 {
		t.Fatalf("dmOff left %d sprites retained", len(a.dmSprites.m))
	}
}

// Inline emoji that finishes loading after a comment starts flying re-bakes its
// sprite once so the emoji appears instead of staying blank.
func TestDanmakuSpriteRebakesWhenEmojiLoads(t *testing.T) {
	emojiReady := make(chan struct{})
	done := make(chan func(), 16)
	emojiJPEG := makeJPEG(32, 32)
	a := &app{
		emoji: map[string]string{"[微笑]": "emoji:smile"},
	}
	a.img = newImages(func(u string) ([]byte, error) {
		<-emojiReady
		return emojiJPEG, nil
	}, func(fn func()) { done <- fn })

	s := &slot{danmaku: []dy.Danmaku{{At: 0, Text: "太棒了[微笑]"}}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).Children(func() {
			a.danmakuLayer(c, "dm", false, func(w, h float32, _ time.Duration, pt dmPointer) ([]dmFlying, time.Duration) {
				return flyDanmakuArea(a, &s.dm, s.danmaku, sameDanmaku, 2*time.Second, w, h, pt)
			})
		})
	}, 600, 120)
	before := slices.Clone(tt.Image().Pix)
	sh := a.danmakuText("太棒了[微笑]")
	sp0 := a.dmSprites.m[sh]
	if sp0 == nil || sp0.picsMissing != 1 {
		t.Fatalf("expected 1 missing emoji before load, got %+v", sp0)
	}
	close(emojiReady)
	select {
	case fn := <-done:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for emoji image")
	}
	tt.Advance(50 * time.Millisecond)
	tt.Frame()
	sp1 := a.dmSprites.m[sh]
	if sp1 == nil || sp1.picsMissing != 0 || sp1.cv == sp0.cv {
		t.Fatalf("expected sprite re-bake after emoji load: before=%+v after=%+v", sp0, sp1)
	}
	if slices.Equal(before, tt.Image().Pix) {
		t.Fatal("rendered pixels did not change after emoji loaded")
	}
}

// Many distinct comments streaming through stay within dmMaxSprites.
func TestDanmakuSpriteCacheBounded(t *testing.T) {
	a := &app{}
	var list []dy.Danmaku
	for i := range dmMaxSprites + 40 {
		list = append(list, dy.Danmaku{At: time.Duration(i) * 100 * time.Millisecond, Text: fmt.Sprintf("弹幕 #%d", i)})
	}
	s := &slot{danmaku: list}
	at := time.Second
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Children(func() {
			a.danmakuLayer(c, "dm", false, func(w, h float32, _ time.Duration, pt dmPointer) ([]dmFlying, time.Duration) {
				return flyDanmakuArea(a, &s.dm, s.danmaku, sameDanmaku, at, w, h, pt)
			})
		})
	}, 800, 400)
	for step := range 25 {
		at = time.Duration(step+1) * 700 * time.Millisecond
		tt.Frame()
		if len(a.dmSprites.m) > dmMaxSprites {
			t.Fatalf("sprite cache grew to %d, limit %d", len(a.dmSprites.m), dmMaxSprites)
		}
	}
}

// When the initial 256x256 glyph atlas overflows mid-frame and MyGo runs a
// MakeRoom recovery repaint with the same frame timestamp, sprites and badges
// baked during the aborted pass must re-bake before presentation so they never
// sample stale atlas UVs.
func TestDanmakuSpriteRecoveryRepaintAfterAtlasGrowth(t *testing.T) {
	comments := []dy.Danmaku{
		{At: 0, Text: "前方高能名场面来了"},
		{At: 50 * time.Millisecond, Text: "哈哈哈哈太真实了"},
		{At: 80 * time.Millisecond, Text: "哈哈哈哈太真实了"},
		{At: 110 * time.Millisecond, Text: "哈哈哈哈太真实了"},
		{At: 200 * time.Millisecond, Text: "45°投影 + 92%不透明度 123 ABC"},
	}
	for _, scale := range []float32{1, 2} {
		t.Run(fmt.Sprintf("scale_%gx", scale), func(t *testing.T) {
			a := &app{}
			a.setDanmakuStyle(danmakuStyle{Speed: 1, Size: 18, Weight: 700, Stroke: dmStrokeShadow, Opacity: 0.92, Area: 1})
			s := &slot{danmaku: comments}
			show := scale == 1
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Box(c).Fill().Background(ui.RGB(24, 52, 86)).Children(func() {
					if !show {
						return
					}
					a.danmakuLayer(c, "dm", false, func(w, h float32, _ time.Duration, pt dmPointer) ([]dmFlying, time.Duration) {
						return flyDanmakuArea(a, &s.dm, s.danmaku, sameDanmaku, 3500*time.Millisecond, w, h, pt)
					})
				})
			}, 760, 180)
			if scale != 1 {
				tt.SetScale(scale)
				show = true
				tt.Advance(50 * time.Millisecond)
				tt.Frame()
			}
			if a.dmSprites.framePass == 0 {
				t.Fatal("expected glyph atlas MakeRoom recovery repaint on cold atlas")
			}
			recovered := slices.Clone(tt.Image().Pix)

			// Re-render at the exact same video timestamp against the already-grown
			// atlas with fresh canvases; pixels must match the recovered frame.
			a.clearDanmakuCanvases()
			tt.Advance(50 * time.Millisecond)
			tt.Frame()
			if a.dmSprites.framePass != 0 {
				t.Fatalf("expected warm atlas single pass, got framePass=%d", a.dmSprites.framePass)
			}
			warm := tt.Image().Pix
			if !slices.Equal(recovered, warm) {
				t.Fatal("recovered frame pixels diverge from warm-atlas render (stale glyph atlas UVs)")
			}
		})
	}
}

// After an idle gap approaching Metal's 2s texture trim window, cached sprite
// and badge canvases drop and re-bake against the live glyph atlas.
func TestDanmakuSpriteIdleGapInvalidatesCanvases(t *testing.T) {
	a := &app{}
	s := &slot{danmaku: []dy.Danmaku{
		{At: 0, Text: "空闲回收测试"},
		{At: 50 * time.Millisecond, Text: "空闲回收测试"},
	}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).Children(func() {
			a.danmakuLayer(c, "dm", false, func(w, h float32, _ time.Duration, pt dmPointer) ([]dmFlying, time.Duration) {
				return flyDanmakuArea(a, &s.dm, s.danmaku, sameDanmaku, 2*time.Second, w, h, pt)
			})
		})
	}, 600, 180)
	sh := a.danmakuText("空闲回收测试")
	sp0 := a.dmSprites.m[sh]
	b0 := a.danmakuBadge(2).cv
	if sp0 == nil || sp0.cv == nil || b0 == nil {
		t.Fatal("expected initial sprite and badge canvases")
	}
	tt.Advance(2 * time.Second)
	tt.Frame()
	sp1 := a.dmSprites.m[sh]
	b1 := a.danmakuBadge(2).cv
	if sp1 == nil || sp1.cv == nil || sp1.cv == sp0.cv {
		t.Fatal("expected sprite canvas to re-bake after idle gap")
	}
	if b1 == nil || b1 == b0 {
		t.Fatal("expected badge canvas to re-bake after idle gap")
	}
}
