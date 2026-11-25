package media

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// A stream used from goroutines, as the app's loader and MyGo's decoder use
// it, must free its item and asset on Close: an autoreleased reference left
// on a thread without a draining pool kept each video's HTTP cache (~32 MB).
func TestStreamFreedOnClose(t *testing.T) {
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	var initWeak func(p unsafe.Pointer, o objc.ID) objc.ID
	var loadWeak func(p unsafe.Pointer) objc.ID
	purego.RegisterLibFunc(&initWeak, lib, "objc_initWeak")
	purego.RegisterLibFunc(&loadWeak, lib, "objc_loadWeak")

	path, _ := filepath.Abs("../../testdata/silent.mp4")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, path) }))
	defer srv.Close()
	l, err := OpenVideo(t.Context(), srv.URL+"/v.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Seek(100 * time.Millisecond)
	l.Pause()
	l.Play()
	for range 5 {
		if _, err := l.Next(); err != nil {
			t.Fatal(err)
		}
	}
	l.Position()
	var weak [2]objc.ID
	func() {
		defer autorelease()()
		initWeak(unsafe.Pointer(&weak[0]), l.item)
		initWeak(unsafe.Pointer(&weak[1]), l.item.Send(sel("asset")))
	}()
	l.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		alive := func() bool {
			defer autorelease()()
			return loadWeak(unsafe.Pointer(&weak[0])) != 0 || loadWeak(unsafe.Pointer(&weak[1])) != 0
		}()
		if !alive {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the player item or its asset outlived Close")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
