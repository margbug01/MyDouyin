package media

import (
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func TestLayerReleasesPixelsAndNativeObjects(t *testing.T) {
	path, err := filepath.Abs("../../testdata/silent.mp4")
	if err != nil {
		t.Fatal(err)
	}
	live, err := OpenVideo("file://"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if _, err = live.Next(); err != nil {
		t.Fatal(err)
	}
	onMain := func(fn func()) {
		t.Helper()
		done := make(chan struct{})
		block := objc.NewBlock(func(_ objc.Block) { fn(); close(done) })
		defer block.Release()
		func() {
			defer autorelease()()
			objc.ID(objc.GetClass("NSOperationQueue")).Send(sel("mainQueue")).Send(sel("addOperationWithBlock:"), block)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("main queue did not execute video-layer operation")
		}
	}
	var layer *Layer
	onMain(func() { layer, err = live.NewLayer() })
	if err != nil {
		t.Fatal(err)
	}
	defer onMain(layer.Close)
	if live.out != 0 || live.frame != nil {
		t.Fatal("native layer retained the RGBA output or pixels")
	}
	live.Seek(250 * time.Millisecond)
	live.Play()
	deadline := time.Now().Add(2 * time.Second)
	for live.Seeking() && time.Now().Before(deadline) {
		f, err := live.Frame()
		if err != nil {
			t.Fatal(err)
		}
		if f.Image != nil {
			t.Fatal("native presentation allocated an RGBA frame")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if live.Seeking() || live.Position() < 200*time.Millisecond {
		t.Fatal("seek without pixel output did not resume")
	}
	for _, paused := range []bool{false, true} {
		if paused {
			live.Pause()
		} else {
			live.Play()
		}
		image, err := live.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if image.Bounds().Dx() != 160 || image.Bounds().Dy() != 90 {
			t.Fatalf("snapshot dimensions: %v", image.Bounds())
		}
		if live.out != 0 || live.frame != nil {
			t.Fatal("capture left a permanent pixel output")
		}
	}
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	var initWeak func(unsafe.Pointer, objc.ID) objc.ID
	var loadWeak func(unsafe.Pointer) objc.ID
	var destroyWeak func(unsafe.Pointer)
	purego.RegisterLibFunc(&initWeak, lib, "objc_initWeak")
	purego.RegisterLibFunc(&loadWeak, lib, "objc_loadWeak")
	purego.RegisterLibFunc(&destroyWeak, lib, "objc_destroyWeak")
	var weak [2]objc.ID
	func() {
		defer autorelease()()
		initWeak(unsafe.Pointer(&weak[0]), layer.layer)
		initWeak(unsafe.Pointer(&weak[1]), live.item)
	}()
	defer func() { destroyWeak(unsafe.Pointer(&weak[0])); destroyWeak(unsafe.Pointer(&weak[1])) }()
	onMain(layer.Close)
	if err := live.UsePixels(); err != nil {
		t.Fatal(err)
	}
	live.Seek(100 * time.Millisecond)
	live.Play()
	if f, err := live.Next(); err != nil || f.Image == nil {
		t.Fatalf("pixel fallback failed: %v", err)
	}
	live.Close()
	deadline = time.Now().Add(3 * time.Second)
	for {
		alive := func() bool {
			defer autorelease()()
			return loadWeak(unsafe.Pointer(&weak[0])) != 0 || loadWeak(unsafe.Pointer(&weak[1])) != 0
		}()
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native layer or player item outlived Close")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
