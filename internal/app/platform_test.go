package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"douyin/internal/dy"
	"douyin/internal/hg"
	"douyin/internal/media"
	"douyin/internal/tiktok"

	"github.com/egoist/mygo/ui"
)

// MyTikTok is the brand menu's third mode: its state is swapped in on entry
// and Douyin's put back on leaving, MyHongguo included.
func TestMyTikTokModeIsolatesState(t *testing.T) {
	t.Cleanup(func() { _ = media.SetProxy("") })
	t.Setenv("HOME", t.TempDir()) // tiktok.New reads TikTok/cookies.json under it

	home := &page{title: "引力场", grid: true}
	a := withRoots(&app{
		home:     home,
		pages:    []*page{home},
		me:       dy.User{Name: "DouyinUser"},
		hgDirect: true,
		newHGService: func(hg.Config) (hg.Service, error) {
			return &fakeHGService{}, nil
		},
	})
	a.signedIn = true
	tt := newTester(a)
	tt.Frame()

	if err := tt.Click("切换模式"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("切换到 MyTikTok"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.mode != modeTikTok || !a.isTikTok() || a.windowTitle() != "MyTikTok" {
		t.Fatalf("brand menu did not enter MyTikTok: mode=%v title=%q", a.mode, a.windowTitle())
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

	// MyHongguo from MyTikTok puts Douyin's state back live, where Douyin's
	// answers arriving meanwhile are written.
	tt.Key(ui.Cmd, ui.Key2)
	tt.Frame()
	if a.mode != modeHongguo || a.isTikTok() || !a.signedIn || a.me.Name != "DouyinUser" {
		t.Fatalf("Douyin state not live in MyHongguo: mode=%v signedIn=%v me=%+v", a.mode, a.signedIn, a.me)
	}
	tt.Key(ui.Cmd, ui.Key1)
	tt.Frame()
	if a.mode != modeDouyin || a.cur() != home || !a.signedIn || a.me.Name != "DouyinUser" {
		t.Fatalf("Douyin snapshot not restored: mode=%v cur=%v signedIn=%v me=%+v", a.mode, a.cur(), a.signedIn, a.me)
	}
	tt.Key(ui.Cmd, ui.Key3)
	tt.Frame()
	if a.mode != modeTikTok || a.cur() != a.ttFeed || a.signedIn {
		t.Fatalf("Cmd+3 did not bring TikTok's state back: mode=%v cur=%v signedIn=%v", a.mode, a.cur(), a.signedIn)
	}
}

func TestSettingsPersistPlatformProxyAndRegion(t *testing.T) {
	t.Cleanup(func() { _ = media.SetProxy("") })

	file := filepath.Join(t.TempDir(), "settings.json")
	a := &app{settingsFile: file, mode: modeTikTok}
	if err := a.applyProxySetting("socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("applyProxySetting: %v", err)
	}
	a.applyTikTokRegion("JP")
	a.saveSettings()

	b := &app{}
	b.loadSettings(file)
	if b.mode != modeTikTok {
		t.Fatalf("reloaded mode = %v, want MyTikTok", b.mode)
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
	t.Setenv("HOME", t.TempDir()) // tiktok.New reads TikTok/cookies.json under it

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

	a.switchMode(modeTikTok)
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
	a.switchMode(modeDouyin)
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
	t.Setenv("HOME", t.TempDir()) // tiktok.New reads TikTok/cookies.json under it

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
	a.switchMode(modeTikTok)
	a.pages = []*page{p}
	a.translateText = func(text, target string) (string, error) {
		return "你我之间的绝对规则。", nil
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

	// Use an isolated config dir so SaveCookies/ReloadCookies never touch user
	// cookies: HOME is macOS's, APPDATA Windows', XDG_CONFIG_HOME Linux's.
	t.Setenv("HOME", t.TempDir())
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
	a.mode = modeTikTok

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
