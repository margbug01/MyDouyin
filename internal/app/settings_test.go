package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"douyin/internal/dy"
)

// The settings start from the defaults, open from the sidebar, take a
// switch, and are written once changed, as the bar's changes are; the next
// launch reads them back.
func TestSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	a.loadSettings(file)
	if a.autoNext || a.dmOff || a.volume != 1 || !a.upd.auto {
		t.Fatalf("defaults: %+v", a.current())
	}
	tt := newTester(a)
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.settingsOpen || !tt.HasText("连播") || !tt.HasText("清晰度") || !tt.HasText("当前版本") || !tt.HasText("这个版本不是正式打包的，不能自动更新") {
		t.Fatalf("dialog: %q", tt.Texts())
	}
	if err := tt.Click("连播开关"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.setVolume(0.4, false) // as the bar's volume
	tt.Frame()
	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), `"autoNext": true`) || !strings.Contains(string(b), `"volume": 0.4`) {
		t.Fatalf("written: %s, %v", b, err)
	}
	next := &app{}
	next.loadSettings(file)
	if !next.autoNext || next.volume != 0.4 || next.dmOff {
		t.Fatalf("read back: %+v", next.current())
	}
}

// Logged out, the sidebar and the settings offer the login; logged in, the
// settings name the account and offer to log out, and the sidebar does not.
func TestLoginEntries(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}, c: &dy.Client{}})
	tt := newTester(a)
	tt.Frame()
	if !tt.HasText("登录") {
		t.Fatalf("logged out sidebar: %q", tt.Texts())
	}
	a.settingsOpen = true
	tt.Frame()
	if !tt.HasText("没登录") || !tt.HasText("扫码登录") {
		t.Fatalf("logged out settings: %q", tt.Texts())
	}
	a.signedIn, a.me, a.settingsOpen = true, dy.User{Name: "小明"}, false
	tt.Frame()
	if tt.HasText("登录") {
		t.Fatalf("logged in sidebar: %q", tt.Texts())
	}
	a.settingsOpen = true
	tt.Frame()
	if !tt.HasText("小明") || !tt.HasText("退出登录") || tt.HasText("扫码登录") {
		t.Fatalf("logged in settings: %q", tt.Texts())
	}
}

// The login shows natively: the code's place and how the scan goes, a way
// out, and no page of Douyin's.
func TestLoginPanel(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}, c: &dy.Client{}})
	a.loginOpen, a.login = true, login{status: "已扫码，请在手机上确认登录"}
	tt := newTester(a)
	tt.Frame()
	if !tt.HasText("扫码登录抖音") || !tt.HasText("已扫码，请在手机上确认登录") {
		t.Fatalf("panel: %q", tt.Texts())
	}
	if err := tt.Click("以后再说"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.loginOpen || tt.HasText("扫码登录抖音") {
		t.Fatal("以后再说 left it open")
	}
}
