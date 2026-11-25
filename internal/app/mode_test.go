package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
	"douyin/internal/hg"
)

type fakeHGService struct {
	mu          sync.Mutex
	reconfigs   []hg.Config
	resetCalls  int
	reconfigErr error
	resetErr    error
}

func (f *fakeHGService) Discover(context.Context, string, string) (hg.Page[hg.Series], error) {
	return hg.Page[hg.Series]{}, nil
}
func (f *fakeHGService) Search(context.Context, string, string, []string) (hg.Page[hg.Series], error) {
	return hg.Page[hg.Series]{}, nil
}
func (f *fakeHGService) Suggest(context.Context, string) ([]string, error) { return nil, nil }
func (f *fakeHGService) HotSearch(context.Context) (hg.HotSearch, error)   { return hg.HotSearch{}, nil }
func (f *fakeHGService) ShortRank(context.Context, string, string, string) (hg.ShortRankPage, error) {
	return hg.ShortRankPage{}, nil
}
func (f *fakeHGService) ShortFilterPanel(context.Context, string) ([]hg.FilterRow, error) {
	return nil, nil
}
func (f *fakeHGService) ShortFilter(context.Context, string, map[string]string, string) (hg.Page[hg.Series], error) {
	return hg.Page[hg.Series]{}, nil
}
func (f *fakeHGService) NewReleases(context.Context, string, string) (hg.NewReleasePage, error) {
	return hg.NewReleasePage{}, nil
}
func (f *fakeHGService) GetSeries(context.Context, string) (hg.Series, error) { return hg.Series{}, nil }
func (f *fakeHGService) ListEpisodes(context.Context, string) ([]hg.Episode, error) {
	return nil, nil
}
func (f *fakeHGService) Seasons(context.Context, string) ([]hg.Season, error) { return nil, nil }
func (f *fakeHGService) SeriesRecommend(context.Context, string) ([]hg.Series, error) {
	return nil, nil
}
func (f *fakeHGService) Person(context.Context, string) (hg.Person, error) { return hg.Person{}, nil }
func (f *fakeHGService) PersonWorks(context.Context, string, string) (hg.Page[hg.Series], error) {
	return hg.Page[hg.Series]{}, nil
}
func (f *fakeHGService) ResolvePlayback(context.Context, string) (hg.Playback, error) {
	return hg.Playback{}, nil
}
func (f *fakeHGService) ListDanmaku(context.Context, string, string, int64, string) (hg.DanmakuPage, error) {
	return hg.DanmakuPage{}, nil
}
func (f *fakeHGService) ListEpisodeComments(context.Context, string, string, string) (hg.CommentPage, error) {
	return hg.CommentPage{}, nil
}
func (f *fakeHGService) ListEpisodeReplies(context.Context, string, string, string, string) (hg.CommentPage, error) {
	return hg.CommentPage{}, nil
}
func (f *fakeHGService) ListSeriesComments(context.Context, string, string) (hg.CommentPage, error) {
	return hg.CommentPage{}, nil
}
func (f *fakeHGService) ListSeriesReplies(context.Context, string, string, string) (hg.CommentPage, error) {
	return hg.CommentPage{}, nil
}
func (f *fakeHGService) Image(context.Context, string) ([]byte, error) { return nil, nil }

func (f *fakeHGService) Reconfigure(cfg hg.Config) error {
	f.mu.Lock()
	f.reconfigs = append(f.reconfigs, cfg)
	err := f.reconfigErr
	f.mu.Unlock()
	return err
}

func (f *fakeHGService) ResetDevice(context.Context) error {
	f.mu.Lock()
	f.resetCalls++
	err := f.resetErr
	f.mu.Unlock()
	return err
}

func TestModeSwitchLazyHGAndDouyinRelease(t *testing.T) {
	feed := stub("推荐", 2)
	ready(t, feed.list[0])
	heldSlot := &slot{v: dy.Video{ID: "held"}}
	ready(t, heldSlot)

	initCalls := 0
	var fake *fakeHGService
	a := withRoots(&app{
		pages:    []*page{feed},
		held:     heldSlot,
		hgDirect: true,
		newHGService: func(cfg hg.Config) (hg.Service, error) {
			initCalls++
			fake = &fakeHGService{}
			return fake, nil
		},
	})
	a.sync()
	if !feed.list[0].vp.Playing() {
		t.Fatal("expected Douyin slot to be playing before switching mode")
	}

	tt := newTester(a)
	tt.Frame()
	if a.hg != nil || initCalls != 0 {
		t.Fatalf("expected lazy HG init to stay idle in Douyin mode: hg=%v calls=%d", a.hg, initCalls)
	}

	// Open popover via brand switcher and switch to MyHongguo.
	if err := tt.Click("切换模式"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.brandMenuOpen || !tt.HasText("免费短剧 · 漫剧 · 榜单") {
		t.Fatalf("brand popover did not open: open=%v texts=%q", a.brandMenuOpen, tt.Texts())
	}
	if err := tt.Click("切换到 MyHongguo"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if a.mode != modeHongguo || a.brandMenuOpen {
		t.Fatalf("after switch to Hongguo: mode=%v menuOpen=%v", a.mode, a.brandMenuOpen)
	}
	if initCalls != 1 || a.hg == nil || a.hg.svc != fake {
		t.Fatalf("expected HG initialized once: calls=%d hg=%+v", initCalls, a.hg)
	}
	if feed.list[0].vp != nil || a.held != nil {
		t.Fatalf("expected Douyin playback released on leave: vp=%v held=%v", feed.list[0].vp, a.held)
	}

	// Guard: sync and fill in modeHongguo must never play Douyin slots.
	ready(t, feed.list[0])
	feed.list[0].vp.Pause()
	a.sync()
	a.fill()
	if feed.list[0].vp.Playing() {
		t.Fatal("Douyin slot started playing while in Hongguo mode")
	}

	// Switch back to Douyin via Cmd+1 / Ctrl+1 shortcut.
	tt.Key(ui.Cmd, ui.Key1)
	tt.Frame()
	if a.mode != modeDouyin {
		t.Fatalf("Cmd+1 did not switch back to Douyin: mode=%v", a.mode)
	}
	if !feed.list[0].vp.Playing() {
		t.Fatal("expected Douyin slot to resume playing on returning to Douyin mode")
	}

	// Switch to Hongguo again via Cmd+2 / Ctrl+2: must reuse existing hgApp.
	tt.Key(ui.Cmd, ui.Key2)
	tt.Frame()
	if a.mode != modeHongguo || initCalls != 1 {
		t.Fatalf("second switch to Hongguo: mode=%v initCalls=%d", a.mode, initCalls)
	}
}

func TestBrandMenuEscapeAndOutsideClick(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1), stub("搜索：猫", 1)}})
	tt := newTester(a)
	tt.Frame()

	a.brandMenuOpen = true
	tt.Frame()
	if !tt.HasText("刷抖音 · 直播 · 精选") {
		t.Fatalf("expected brand menu visible: %q", tt.Texts())
	}

	// First Escape closes brandMenuOpen without popping the child page.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.brandMenuOpen || len(a.pages) != 2 {
		t.Fatalf("first Escape: menuOpen=%v pages=%d", a.brandMenuOpen, len(a.pages))
	}

	// Second Escape pops the child page as usual.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(a.pages) != 1 {
		t.Fatalf("second Escape did not pop page: pages=%d", len(a.pages))
	}

	// Clicking outside the popover closes it.
	a.brandMenuOpen = true
	tt.Frame()
	tt.ClickAt(900, 500)
	tt.Frame()
	if a.brandMenuOpen {
		t.Fatal("outside click did not dismiss brand popover")
	}
}

func TestSettingsModeAndHGRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "settings.json")

	// Backward compatibility: legacy settings.json without mode or hgDirect.
	if err := os.WriteFile(file, []byte(`{"autoNext":true,"volume":0.6}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	a.loadSettings(file)
	if a.mode != modeDouyin || !a.hgDirect || !a.autoNext || a.volume != 0.6 {
		t.Fatalf("legacy load mismatch: mode=%v hgDirect=%v autoNext=%v volume=%v", a.mode, a.hgDirect, a.autoNext, a.volume)
	}

	// Mutate mode and Hongguo settings, save, and reload into a fresh app.
	a.mode = modeHongguo
	a.hgDirect = false
	a.hgSignerURL = "https://signer.example/sign"
	a.hgSignerToken = "secret-token"
	a.saveSettings()

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"mode": "hongguo"`, `"hgDirect": false`, `"hgSignerURL": "https://signer.example/sign"`, `"hgSignerToken": "secret-token"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved JSON missing %s:\n%s", want, text)
		}
	}

	next := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	next.loadSettings(file)
	if next.mode != modeHongguo || next.hgDirect || next.hgSignerURL != "https://signer.example/sign" || next.hgSignerToken != "secret-token" {
		t.Fatalf("round-trip mismatch: mode=%v hgDirect=%v url=%q tok=%q", next.mode, next.hgDirect, next.hgSignerURL, next.hgSignerToken)
	}
}

func TestHongguoShellViewAndAccentIsolation(t *testing.T) {
	origLightAccent := lightPal.accent
	origDarkAccent := darkPal.accent

	a := &app{
		mode:     modeHongguo,
		c:        &dy.Client{},
		hgDirect: true,
		newHGService: func(cfg hg.Config) (hg.Service, error) {
			return &fakeHGService{}, nil
		},
	}
	tt := newTester(a)
	tt.Frame()

	for _, want := range []string{"MyHongguo", "双星", "真人剧", "漫剧", "追剧"} {
		if !tt.HasText(want) {
			t.Fatalf("Hongguo view missing %q: %q", want, tt.Texts())
		}
	}
	// Douyin-only top-right controls must be hidden in MyHongguo mode.
	texts := tt.Texts()
	for _, forbidden := range []string{"登录", "私信", "通知"} {
		if slices.Contains(texts, forbidden) {
			t.Fatalf("Hongguo view unexpectedly rendered Douyin button %q: %q", forbidden, texts)
		}
		if err := tt.Click(forbidden); err == nil {
			t.Fatalf("Hongguo view unexpectedly allowed clicking Douyin button %q", forbidden)
		}
	}
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("expected shared settings button in Hongguo view: %v", err)
	}
	tt.Frame()
	if !a.settingsOpen {
		t.Fatal("settings dialog did not open from Hongguo view")
	}
	if pal.accent != hongguoAccent {
		t.Fatalf("expected Hongguo accent %v, got %v", hongguoAccent, pal.accent)
	}
	if lightPal.accent != origLightAccent || darkPal.accent != origDarkAccent {
		t.Fatal("mode palette mutated package-level lightPal or darkPal")
	}
}

func TestHGSettingsReconfigureAndReset(t *testing.T) {
	fake := &fakeHGService{}
	a := withRoots(&app{
		pages:    []*page{stub("推荐", 1)},
		mode:     modeHongguo,
		hgDirect: true,
		posted:   make(chan func(), 16),
		newHGService: func(cfg hg.Config) (hg.Service, error) {
			return fake, nil
		},
	})
	tt := newTester(a)
	tt.Frame()

	// Open settings from Hongguo top-right controls.
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.settingsOpen || !tt.HasText("红果短剧 (MyHongguo)") || !tt.HasText("绕过系统代理直连") {
		t.Fatalf("settings dialog missing Hongguo section: %q", tt.Texts())
	}

	// Toggling direct switch immediately calls Reconfigure.
	if err := tt.Click("红果直连开关"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	fake.mu.Lock()
	if len(fake.reconfigs) != 1 || fake.reconfigs[0].Direct {
		t.Fatalf("expected Reconfigure(Direct=false), got %+v", fake.reconfigs)
	}
	fake.mu.Unlock()

	// Updating signer URL/Token and clicking 应用配置 calls Reconfigure.
	a.hgSignerURL = "https://sign.local/v1"
	a.hgSignerToken = "abc"
	if err := tt.Click("应用配置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	fake.mu.Lock()
	if len(fake.reconfigs) != 2 || fake.reconfigs[1].SignerURL != "https://sign.local/v1" || fake.reconfigs[1].SignerToken != "abc" {
		t.Fatalf("expected Reconfigure with signer settings, got %+v", fake.reconfigs)
	}
	fake.mu.Unlock()

	// Clicking 重置匿名设备 invokes ResetDevice asynchronously and posts toast.
	if err := tt.Click("重置匿名设备"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for a.hgResetBusy {
		select {
		case fn := <-a.posted:
			fn()
		case <-deadline:
			t.Fatal("resetHGDevice did not post completion")
		}
	}
	fake.mu.Lock()
	resets := fake.resetCalls
	fake.mu.Unlock()
	if resets != 1 {
		t.Fatalf("expected 1 ResetDevice call, got %d", resets)
	}
	if a.toast != "已重置红果匿名设备" {
		t.Fatalf("unexpected toast after reset: %q", a.toast)
	}
}

func TestBrandSwitcherPositionUnifiedAcrossModesAndPages(t *testing.T) {
	if got := brandBarLeftFor("windows"); got != 20 {
		t.Fatalf("brandBarLeftFor(windows) = %v, want 20", got)
	}
	if got := brandBarLeftFor("darwin"); got != contentLeft+8 {
		t.Fatalf("brandBarLeftFor(darwin) = %v, want %v", got, contentLeft+8)
	}

	home := &page{title: "引力场", grid: true}
	picks := stub("精选", 2)
	picks.grid = true
	a := &app{
		home:       home,
		picks:      picks,
		orbitPicks: picks,
		feed:       stub("推荐", 1),
		pages:      []*page{home},
		hgDirect:   true,
		newHGService: func(cfg hg.Config) (hg.Service, error) {
			return &fakeHGService{}, nil
		},
	}
	tt := newTester(a)

	measure := func(label string) (float32, float32) {
		tt.Frame()
		box, ok := tt.Find("切换模式")
		if !ok {
			t.Fatalf("%s: brand switcher not found", label)
		}
		if box.X != a.brandMenuLeft() {
			t.Fatalf("%s: brand switcher X=%v != brandMenuLeft=%v", label, box.X, a.brandMenuLeft())
		}
		return box.X, box.Y
	}

	dyOrbitX, dyOrbitY := measure("Douyin 引力场")

	a.root(picks)
	dyPicksX, dyPicksY := measure("Douyin 精选")

	a.switchMode(modeHongguo)
	hgOrbitX, hgOrbitY := measure("Hongguo 引力场")

	a.hgSelectRoot(hgRootDiscover)
	hgDiscX, hgDiscY := measure("Hongguo 发现")

	for _, tc := range []struct {
		name string
		x, y float32
	}{
		{"Douyin 精选", dyPicksX, dyPicksY},
		{"Hongguo 引力场", hgOrbitX, hgOrbitY},
		{"Hongguo 发现", hgDiscX, hgDiscY},
	} {
		if tc.x != dyOrbitX || tc.y != dyOrbitY {
			t.Fatalf("brand switcher jumped on %s: got (%v, %v), want (%v, %v)", tc.name, tc.x, tc.y, dyOrbitX, dyOrbitY)
		}
	}
}
