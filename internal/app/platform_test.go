package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"douyin/internal/dy"
	"douyin/internal/media"
	"douyin/internal/platform"
	"douyin/internal/tiktok"
)

func TestSwitchPlatformIsolatesPagesAndCapabilities(t *testing.T) {
	t.Cleanup(func() { _ = media.SetProxy("") })

	home := &page{title: "引力场", grid: true}
	a := &app{
		home:  home,
		pages: []*page{home},
		me:    dy.User{Name: "DouyinUser"},
	}
	a.signedIn = true

	if a.isTikTok() {
		t.Fatal("default platform should be Douyin")
	}
	if !a.caps().Danmaku || !a.caps().Location {
		t.Fatal("Douyin capabilities should enable Danmaku and Location")
	}

	a.switchPlatform(platform.TikTok)
	if !a.isTikTok() {
		t.Fatal("expected active platform to be TikTok")
	}
	if a.caps().Danmaku || a.caps().Location {
		t.Fatal("TikTok capabilities should disable Danmaku and Location")
	}
	if a.cur() != a.ttFeed {
		t.Fatalf("expected current page to be ttFeed, got %v", a.cur())
	}
	if a.signedIn || a.me.Name != "" {
		t.Fatalf("TikTok session should be isolated from Douyin, got signedIn=%v me=%+v", a.signedIn, a.me)
	}
	hdrs := a.activeVideoHeaders(dy.Video{ID: "1"})
	if !strings.Contains(hdrs["Referer"], "tiktok.com") {
		t.Fatalf("expected TikTok Referer header, got %+v", hdrs)
	}

	// Switching back restores Douyin pages and login snapshot.
	a.switchPlatform(platform.Douyin)
	if a.isTikTok() {
		t.Fatal("expected platform back to Douyin")
	}
	if a.cur() != home || !a.signedIn || a.me.Name != "DouyinUser" {
		t.Fatalf("Douyin snapshot not restored: cur=%v signedIn=%v me=%+v", a.cur(), a.signedIn, a.me)
	}
}

func TestSettingsPersistPlatformProxyAndRegion(t *testing.T) {
	t.Cleanup(func() { _ = media.SetProxy("") })

	file := filepath.Join(t.TempDir(), "settings.json")
	a := &app{settingsFile: file}
	a.platform = platform.TikTok
	if err := a.applyProxySetting("socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("applyProxySetting: %v", err)
	}
	a.applyTikTokRegion("JP")
	a.saveSettings()

	b := &app{}
	b.loadSettings(file)
	if b.activePlatform() != platform.TikTok {
		t.Fatalf("reloaded platform = %q, want tiktok", b.activePlatform())
	}
	if b.proxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("reloaded proxyURL = %q", b.proxyURL)
	}
	if b.ttRegion != "JP" {
		t.Fatalf("reloaded ttRegion = %q, want JP", b.ttRegion)
	}
}

func TestTikTokPhase2CapabilitiesAndUI(t *testing.T) {
	t.Cleanup(func() { _ = media.SetProxy("") })

	v := dy.Video{ID: "741234567890", Desc: "hello tiktok", Author: "Alice", AuthorID: "MS4w_alice"}
	p := &page{title: "For You", list: []*slot{{v: v}}, done: true}
	a := withRoots(&app{
		pages:   []*page{p},
		mine:    map[string]*page{"作品": stub("作品", 1), "观看历史": stub("观看历史", 1)},
		mineTab: "作品",
	})
	if got := a.videoURL(v); got != "https://www.douyin.com/video/741234567890" {
		t.Fatalf("Douyin videoURL = %q", got)
	}

	a.switchPlatform(platform.TikTok)
	a.pages = []*page{p}
	if got := a.videoURL(v); got != "https://www.tiktok.com/@/video/741234567890" {
		t.Fatalf("TikTok videoURL = %q", got)
	}

	// Build TikTok "我的" tabs and verify only 作品/喜欢/收藏 are present.
	ttMine := a.buildTikTokMine(dy.User{ID: "MS4w_me", Name: "Me"})
	if len(ttMine) != 3 || ttMine["作品"] == nil || ttMine["喜欢"] == nil || ttMine["收藏"] == nil {
		t.Fatalf("unexpected TikTok mine tabs: %+v", ttMine)
	}
	if ttMine["观看历史"] != nil || ttMine["稍后再看"] != nil {
		t.Fatalf("TikTok mine should not contain Douyin-only tabs: %+v", ttMine)
	}

	// Verify TikTok playback UI trims Douyin-only controls.
	tt := newTester(a)
	tt.Frame()
	if tt.HasText("听抖音") || tt.HasText("输入弹幕") {
		t.Fatalf("TikTok stage should hide Douyin-only labels, got: %q", tt.Texts())
	}
	if !tt.HasText("听音频") || !tt.HasText("登录") {
		t.Fatalf("TikTok stage should show 听音频 and 登录, got: %q", tt.Texts())
	}

	// Verify TikTok comment panel hides 相关推荐 and uses Unicode emoji picker.
	a.signedIn = true
	a.sheet = &sheet{v: v, total: 1, items: []dy.Comment{{ID: "c1", Author: "Bob", Text: "nice 🔥"}}}
	a.sheet.draft.emojiOpen = true
	tt.Frame()
	if tt.HasText("相关推荐") {
		t.Fatalf("TikTok comment panel should hide 相关推荐, got: %q", tt.Texts())
	}
	if !tt.HasText("😂") {
		t.Fatalf("TikTok emoji popover should render Unicode emojis, got: %q", tt.Texts())
	}

	// Switching back to Douyin restores Douyin's mine snapshot.
	a.switchPlatform(platform.Douyin)
	if a.mine == nil || a.mine["观看历史"] == nil || a.mineTab != "作品" {
		t.Fatalf("Douyin mine snapshot not restored: mine=%+v tab=%q", a.mine, a.mineTab)
	}
}

func TestAllowTikTokPopupOAuthHosts(t *testing.T) {
	for _, u := range []string{
		"",
		"about:blank",
		"https://accounts.google.com/gsi/select?client_id=123",
		"https://appleid.apple.com/auth/authorize",
		"https://www.tiktok.com/login",
	} {
		if !allowTikTokPopup(u) {
			t.Fatalf("expected allowTikTokPopup(%q) = true", u)
		}
	}
	for _, u := range []string{
		"http://accounts.google.com/insecure",
		"https://evil-google.com/phish",
		"javascript:alert(1)",
	} {
		if allowTikTokPopup(u) {
			t.Fatalf("expected allowTikTokPopup(%q) = false", u)
		}
	}
}

func TestTikTokPhase3SubtitlesAndLive(t *testing.T) {
	t.Cleanup(func() { _ = media.SetProxy("") })

	v := dy.Video{
		ID:       "741999111222",
		Desc:     "Rules absolute, between you and me. #dubbing",
		Author:   "Carol",
		AuthorID: "MS4w_carol",
		Subtitles: []dy.SubtitleTrack{
			{Lang: "en", Label: "English", URL: "https://v16.tiktokcdn.com/en.vtt", Format: "webvtt"},
		},
	}
	s := &slot{
		v:      v,
		resume: 2 * time.Second,
		subRawCues: []tiktok.SubtitleCue{
			{Start: 1 * time.Second, End: 4 * time.Second, Text: "Hello world"},
		},
		subTransCues: []tiktok.SubtitleCue{
			{Start: 1 * time.Second, End: 4 * time.Second, Text: "你好世界"},
		},
		subLoadedURL: "https://v16.tiktokcdn.com/en.vtt",
		subTrack:     0,
		subPicked:    true,
	}
	p := &page{title: "For You", list: []*slot{s}, done: true}
	a := withRoots(&app{pages: []*page{p}, posted: make(chan func(), 8)})
	a.switchPlatform(platform.TikTok)
	a.pages = []*page{p}
	a.translateText = func(text, target string) (string, error) {
		return "你我之间的绝对规则。", nil
	}

	if !a.caps().Live || !a.caps().Subtitles {
		t.Fatalf("TikTok Phase 3 capabilities should enable Live and Subtitles: %+v", a.caps())
	}
	if a.ttLive == nil {
		t.Fatal("expected ttLive page to be initialized")
	}

	liveVideo := dy.Video{ID: "live:741", Live: &dy.Live{RoomID: "741", WebRID: "piano_host"}}
	if got := a.videoURL(liveVideo); got != "https://www.tiktok.com/@piano_host/live" {
		t.Fatalf("TikTok live URL = %q", got)
	}

	tt := newTester(a)
	tt.Frame()
	if !tt.HasText("你好世界") {
		t.Fatalf("expected translated Chinese subtitle to render on stage, got: %q", tt.Texts())
	}
	if !tt.HasText("查看翻译") {
		t.Fatalf("expected 查看翻译 button for non-Chinese caption, got: %q", tt.Texts())
	}

	// Clicking 查看翻译 switches caption to translated Chinese and button to 查看原文.
	if err := tt.Click("查看翻译"); err != nil {
		t.Fatalf("click 查看翻译: %v", err)
	}
	deadline := time.After(time.Second)
	for s.descTranslating {
		select {
		case fn := <-a.posted:
			fn()
		case <-deadline:
			t.Fatal("caption translation did not settle")
		}
	}
	tt.Frame()
	if !tt.HasText("你我之间的绝对规则。") || !tt.HasText("查看原文") {
		t.Fatalf("expected translated caption and 查看原文, got: %q", tt.Texts())
	}

	// Turning auto-translate off falls back to original English subtitle.
	a.subTranslateOff = true
	tt.Frame()
	if !tt.HasText("Hello world") {
		t.Fatalf("expected original subtitle when subTranslateOff=true, got: %q", tt.Texts())
	}

	// Turning subtitles off hides the caption overlay.
	a.subtitlesOff = true
	tt.Frame()
	if tt.HasText("Hello world") || tt.HasText("你好世界") {
		t.Fatalf("expected subtitle overlay to hide when subtitlesOff=true")
	}
}

func TestTikTokAutoLoginRestoresSession(t *testing.T) {
	t.Cleanup(func() { _ = media.SetProxy("") })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/passport/web/account/info/" {
			_, _ = w.Write([]byte(`{"message":"success","data":{"user_id_str":"998877","sec_user_id":"MS4w_me","username":"ikun","screen_name":"测试账号"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Use an isolated config dir so SaveCookies/ReloadCookies never touch user cookies.
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ttClient, err := tiktok.New()
	if err != nil {
		t.Fatalf("tiktok.New: %v", err)
	}
	if err := ttClient.SaveCookies([]dy.Cookie{{Name: "sessionid", Value: "sess_test_1", Domain: ".tiktok.com", Path: "/"}}); err != nil {
		t.Fatalf("SaveCookies: %v", err)
	}
	ttClient.SetBaseURLForTest(srv.URL)

	p := &page{title: "For You", done: true}
	a := withRoots(&app{pages: []*page{p}, posted: make(chan func(), 8)})
	a.tt = ttClient
	a.platform = platform.TikTok

	a.startTikTokLogin()
	if !a.signedIn {
		t.Fatal("expected startTikTokLogin to mark signedIn immediately when session cookie exists")
	}
	deadline := time.After(2 * time.Second)
	for a.me.ID == "" {
		select {
		case fn := <-a.posted:
			fn()
		case <-deadline:
			t.Fatal("loadTikTokAccount did not populate a.me")
		}
	}
	if a.me.Name != "测试账号" || a.me.ID != "MS4w_me" || a.mine["作品"] == nil {
		t.Fatalf("unexpected TikTok auto-login account: me=%+v mine=%v", a.me, a.mine)
	}
}
