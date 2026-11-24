package app

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"runtime"
	"testing"
	"time"

	"douyin/internal/dy"
)

func makeJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

func makePerfComments(n, repliesPer int) *sheet {
	sh := &sheet{
		total:   n * 10,
		done:    true,
		v:       dy.Video{ID: "v1", Author: "作者", Comments: n * 10},
		threads: map[string]*thread{},
	}
	now := time.Now().Add(-30 * time.Minute)
	for i := range n {
		cid := fmt.Sprintf("c%d", i)
		cm := dy.Comment{
			ID:      cid,
			Author:  fmt.Sprintf("用户%d", i),
			Avatar:  fmt.Sprintf("avatar:%d", i),
			Text:    fmt.Sprintf("这是一条测试评论 %d [微笑] 说得太好了 [捂脸] 哈哈哈哈", i),
			Place:   "广东",
			Likes:   100 + i,
			Replies: repliesPer,
			Time:    now.Add(-time.Duration(i) * time.Minute),
		}
		sh.items = append(sh.items, cm)
		if repliesPer > 0 && i < 10 {
			th := &thread{video: "v1", cid: cid, open: true, done: true}
			for j := range repliesPer {
				th.items = append(th.items, dy.Comment{
					ID:      fmt.Sprintf("r%d-%d", i, j),
					Author:  fmt.Sprintf("回复者%d", j),
					ReplyTo: cm.Author,
					Avatar:  fmt.Sprintf("ravatar:%d:%d", i, j),
					Text:    fmt.Sprintf("回复一下 %d [笑哭] 确实如此", j),
					Place:   "北京",
					Likes:   j,
					Time:    now.Add(-time.Duration(j) * time.Second),
				})
			}
			sh.threads[cid] = th
		}
	}
	return sh
}

func makePerfDanmaku(n int) []dy.Danmaku {
	out := make([]dy.Danmaku, n)
	for i := range n {
		out[i] = dy.Danmaku{
			At:   time.Duration(i*150) * time.Millisecond,
			Text: fmt.Sprintf("前方高能弹幕第 %d 条 [捂脸] 哈哈哈", i),
		}
	}
	return out
}

func BenchmarkViewFrames(b *testing.B) {
	avatarJPEG := makeJPEG(100, 100)
	coverJPEG := makeJPEG(1280, 720)
	emojiJPEG := makeJPEG(48, 48)
	emojiMap := map[string]string{"[微笑]": "emoji:smile", "[捂脸]": "emoji:facepalm", "[笑哭]": "emoji:cry"}

	setupImages := func(a *app) {
		done := make(chan func(), 4096)
		a.img = newImages(func(u string) ([]byte, error) {
			switch {
			case len(u) >= 6 && u[:6] == "cover:":
				return coverJPEG, nil
			case len(u) >= 6 && u[:6] == "emoji:":
				return emojiJPEG, nil
			default:
				return avatarJPEG, nil
			}
		}, func(fn func()) { done <- fn })
		a.emoji = emojiMap
	}

	b.Run("StagePlain", func(b *testing.B) {
		p := stub("推荐", 1)
		p.list[0].v = dy.Video{ID: "v1", Author: "作者", Desc: "测试视频", Duration: time.Minute, Likes: 100, Comments: 200}
		a := withRoots(&app{pages: []*page{p}, dmOff: true})
		setupImages(a)
		tt := newTester(a)
		tt.Frame()
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
	})

	b.Run("StageDanmaku30", func(b *testing.B) {
		p := stub("推荐", 1)
		p.list[0].v = dy.Video{ID: "v1", Author: "作者", Desc: "测试视频", Duration: time.Minute, Likes: 100, Comments: 200}
		p.list[0].danmaku = makePerfDanmaku(40)
		p.list[0].resume = 5 * time.Second
		a := withRoots(&app{pages: []*page{p}})
		setupImages(a)
		tt := newTester(a)
		tt.Frame()
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			p.list[0].resume += 16 * time.Millisecond
			if p.list[0].resume > 7*time.Second {
				p.list[0].resume = 4 * time.Second
			}
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
	})

	b.Run("StageDanmaku30_Comments60", func(b *testing.B) {
		p := stub("推荐", 1)
		p.list[0].v = dy.Video{ID: "v1", Author: "作者", Desc: "测试视频", Duration: time.Minute, Likes: 100, Comments: 600}
		p.list[0].danmaku = makePerfDanmaku(40)
		p.list[0].resume = 5 * time.Second
		sh := makePerfComments(60, 5)
		p.list[0].comments = sh
		a := withRoots(&app{pages: []*page{p}, sheet: sh})
		setupImages(a)
		tt := newTester(a)
		tt.Frame()
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			p.list[0].resume += 16 * time.Millisecond
			if p.list[0].resume > 7*time.Second {
				p.list[0].resume = 4 * time.Second
			}
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
	})

	b.Run("StageComments200", func(b *testing.B) {
		p := stub("推荐", 1)
		p.list[0].v = dy.Video{ID: "v1", Author: "作者", Desc: "测试视频", Duration: time.Minute, Likes: 100, Comments: 2000}
		sh := makePerfComments(200, 10)
		p.list[0].comments = sh
		a := withRoots(&app{pages: []*page{p}, sheet: sh, dmOff: true})
		setupImages(a)
		tt := newTester(a)
		tt.Frame()
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
	})

	b.Run("Grid120", func(b *testing.B) {
		p := &page{title: "精选", grid: true, done: true}
		for i := range 120 {
			p.list = append(p.list, &slot{v: dy.Video{
				ID:       fmt.Sprintf("g%d", i),
				Author:   fmt.Sprintf("作者%d", i),
				Desc:     fmt.Sprintf("精选视频标题 %d 测试内容长度", i),
				Cover:    fmt.Sprintf("cover:%d", i),
				Likes:    i * 10,
				Duration: time.Minute,
			}})
		}
		a := &app{pages: []*page{p}, picks: p, feed: stub("推荐", 1)}
		setupImages(a)
		tt := newTester(a)
		tt.Frame()
		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
	})
}

func TestImageRetentionInGridAndComments(t *testing.T) {
	coverJPEG := makeJPEG(1280, 720)
	avatarJPEG := makeJPEG(100, 100)
	done := make(chan func(), 4096)
	p := &page{title: "精选", grid: true, done: true}
	for i := range 120 {
		p.list = append(p.list, &slot{v: dy.Video{
			ID:       fmt.Sprintf("g%d", i),
			Author:   fmt.Sprintf("作者%d", i),
			Desc:     fmt.Sprintf("精选视频 %d", i),
			Cover:    fmt.Sprintf("cover:%d", i),
			Duration: time.Minute,
		}})
	}
	a := &app{pages: []*page{p}, picks: p, feed: stub("推荐", 1)}
	a.img = newImages(func(u string) ([]byte, error) {
		if len(u) >= 6 && u[:6] == "cover:" {
			return coverJPEG, nil
		}
		return avatarJPEG, nil
	}, func(fn func()) { done <- fn })
	tt := newTester(a)
	for range 30 {
		tt.Frame()
		for {
			select {
			case fn := <-done:
				fn()
			default:
				goto drained
			}
		}
	drained:
		time.Sleep(5 * time.Millisecond)
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("Grid120 retained: pictures=%d tracked_bytes=%.1f MiB heap=%.1f MiB",
		len(a.img.m), float64(a.img.bytes)/(1<<20), float64(ms.HeapAlloc)/(1<<20))
	if len(a.img.m) > keepPictures {
		t.Fatalf("grid retained %d pictures, exceeds keepPictures=%d", len(a.img.m), keepPictures)
	}
	if a.img.bytes > pictureBudget {
		t.Fatalf("grid retained %.1f MiB pixels, exceeds budget %.1f MiB", float64(a.img.bytes)/(1<<20), float64(pictureBudget)/(1<<20))
	}
}

