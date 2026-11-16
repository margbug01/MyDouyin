package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
