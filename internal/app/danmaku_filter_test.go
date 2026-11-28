package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func flyingTexts(a *app, f *dmField, list []dy.Danmaku, at time.Duration) map[string]dmFlying {
	flying, _ := flyDanmaku(a, f, list, sameDanmaku, at, 1280)
	m := map[string]dmFlying{}
	for _, fl := range flying {
		k := fl.d.Text
		if fl.n > 1 {
			k += fmt.Sprintf("×%d", fl.n)
		}
		m[k] = fl
	}
	return m
}

// Twelve 来了 said while the first is on the right half fly as one 来了×12,
// however they are punctuated; a blocked word is not shown; the user's own
// 来了 still shows.
func TestDanmakuMergeAndBlock(t *testing.T) {
	a := &app{}
	a.setDanmakuBlock([]string{"剧透"})
	var list []dy.Danmaku
	for i, text := range []string{"来了", "来了！", "来 了", "来了", "来了~", "来了", "来了", "来了", "来了", "来了", "来了", "来了"} {
		list = append(list, dy.Danmaku{At: time.Second + time.Duration(i)*100*time.Millisecond, Text: text})
	}
	own := dy.Danmaku{At: 1850 * time.Millisecond, Text: "来了"}
	list = append(list, dy.Danmaku{At: 1500 * time.Millisecond, Text: "前方高能"}, dy.Danmaku{At: 1600 * time.Millisecond, Text: "剧透：凶手是他"}, own)
	slices.SortFunc(list, func(x, y dy.Danmaku) int { return int(x.At - y.At) })
	f := &dmField{own: own}
	got := flyingTexts(a, f, list, 3*time.Second)
	if len(got) != 3 {
		t.Fatalf("flying %v", got)
	}
	if _, ok := got["来了×12"]; !ok {
		t.Fatalf("no 来了×12: %v", got)
	}
	if _, ok := got["前方高能"]; !ok {
		t.Fatalf("前方高能 hidden: %v", got)
	}
	if fl, ok := got["来了"]; !ok || fl.d.At != own.At {
		t.Fatalf("own 来了 hidden: %v", got)
	}

	a.dmMergeOff = true
	a.setDanmakuBlock(nil)
	flying, _ := flyDanmaku(a, f, list, sameDanmaku, 3*time.Second, 1280)
	said := 0
	for _, fl := range flying {
		if fl.n > 1 {
			t.Fatalf("merged with merging off: %q", fl.d.Text)
		}
		if strings.HasPrefix(fl.d.Text, "来") {
			said++
		}
	}
	if said < 3 {
		t.Fatalf("with merging off: %v", flying)
	}
	if got := flyingTexts(a, &dmField{}, []dy.Danmaku{{At: time.Second, Text: "剧透：凶手是他"}}, 2*time.Second); len(got) != 1 {
		t.Fatalf("with no blocked words: %v", got)
	}
}

// Merging only takes repeats away: the lane a folded 来了 would have taken
// stays empty, so a sentence the lanes had no room for with merging off is
// not let in with merging on.
func TestDanmakuMergeOnlyRemoves(t *testing.T) {
	list := []dy.Danmaku{{At: 0, Text: "来了"}, {At: 100 * time.Millisecond, Text: "来了"}, {At: 200 * time.Millisecond, Text: "这句话本来就没地方放"}}
	shown := func(mergeOff bool) []string {
		a := &app{dmMergeOff: mergeOff}
		if n := a.dmLaneCount(100); n != 2 {
			t.Fatalf("%d lanes", n)
		}
		flying, _ := flyDanmakuArea(a, &dmField{}, list, sameDanmaku, time.Second, 1280, 100, dmPointer{})
		var out []string
		for _, fl := range flying {
			out = append(out, fmt.Sprintf("%s×%d", fl.d.Text, fl.n))
		}
		return out
	}
	if got := shown(true); !slices.Equal(got, []string{"来了×1", "来了×1"}) {
		t.Fatalf("merging off: %q", got)
	}
	if got := shown(false); !slices.Equal(got, []string{"来了×2"}) {
		t.Fatalf("merging on: %q", got)
	}
}

// In a live room the repeats come after the first sets off: they fold into
// it while it is on the right half, its count growing without it jumping
// back, and one said after that flies by itself. The merged one flies on at
// its speed until its pill, not only its text, has gone.
func TestDanmakuMergeLive(t *testing.T) {
	a := &app{}
	f := &dmField{}
	list := []dy.Danmaku{{At: 0, Text: "来了"}}
	first := flyingTexts(a, f, list, 500*time.Millisecond)["来了"]
	list = append(list, dy.Danmaku{At: time.Second, Text: "来了"}, dy.Danmaku{At: 2 * time.Second, Text: "来了！"})
	later := flyingTexts(a, f, list, 2500*time.Millisecond)
	three, ok := later["来了×3"]
	if !ok || len(later) != 1 {
		t.Fatalf("at 2.5s: %v", later)
	}
	want := float32(2*time.Second) / float32(flight) * (1280 + a.danmakuText("来了").w)
	if d := first.x - three.x - want; three.y != first.y || d < -0.5 || d > 0.5 {
		t.Fatalf("moved from %+v to %+v", first, three)
	}
	list = append(list, dy.Danmaku{At: 5 * time.Second, Text: "来了"})
	if got := flyingTexts(a, f, list, 5500*time.Millisecond); got["来了×3"].d.At != 0 || got["来了"].d.At != 5*time.Second {
		t.Fatalf("at 5.5s: %v", got)
	}
	at := flight + 50*time.Millisecond
	end, ok := flyingTexts(a, f, list, at)["来了×3"]
	wantX := 1280 - float32(at)/float32(flight)*(1280+a.danmakuText("来了").w)
	if !ok || end.x-wantX < -0.5 || end.x-wantX > 0.5 || end.x+a.danmakuWidth("来了", 3) <= 0 {
		t.Fatalf("as its text has gone, its pill still showing: %+v, want x %.1f", end, wantX)
	}
	if got := flyingTexts(a, f, list, flight+time.Second); got["来了×3"].d.Text != "" {
		t.Fatalf("pill gone too: %v", got)
	}
}

// The player's 弹幕设置 opens the settings' 弹幕 page from 屏蔽词, which adds a
// blocked word with Enter, removes it with its chip, turns merging off, and
// the next launch reads both back.
func TestDanmakuFilterSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	p := stub("推荐", 1)
	p.list[0].v = dy.Video{ID: "v", Duration: time.Minute}
	ready(t, p.list[0])
	a := withRoots(&app{pages: []*page{p}})
	a.loadSettings(file)
	if a.dmMergeOff || len(a.dmBlock) != 0 {
		t.Fatalf("defaults: merge off %v, blocked %v", a.dmMergeOff, a.dmBlock)
	}
	tt := newTester(a)
	tt.Frame()
	for _, label := range []string{"弹幕设置", "管理屏蔽词"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if !a.settingsOpen || a.settingsPage != settingsDanmaku || a.dmSettingsOpen {
		t.Fatalf("settings open %v on %v, popover open %v", a.settingsOpen, a.settingsPage, a.dmSettingsOpen)
	}
	for _, word := range []string{"剧透", "广告"} {
		if err := tt.Click("添加屏蔽词"); err != nil {
			t.Fatal(err)
		}
		tt.Type(word)
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
	}
	if !slices.Equal(a.dmBlock, []string{"剧透", "广告"}) || a.dmBlockText != "" {
		t.Fatalf("blocked %q, box %q", a.dmBlock, a.dmBlockText)
	}
	if err := tt.Click("移除屏蔽词 广告"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("合并重复弹幕开关"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), `"danmakuMerge": false`) || !strings.Contains(string(b), `"剧透"`) || strings.Contains(string(b), `"广告"`) {
		t.Fatalf("written: %s", b)
	}
	next := &app{}
	next.loadSettings(file)
	if !next.dmMergeOff || !slices.Equal(next.dmBlock, []string{"剧透"}) {
		t.Fatalf("read back: merge off %v, blocked %q", next.dmMergeOff, next.dmBlock)
	}
}
