//go:build windows

package app

import "testing"

func TestWindowsCaptionButtonsKeepNotificationsClear(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	tt := newTester(a)
	for _, name := range []string{"私信", "通知"} {
		box, ok := tt.Find(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if box.X+box.W > 1100-138 {
			t.Fatalf("%s overlaps caption buttons: %+v", name, box)
		}
	}
}
