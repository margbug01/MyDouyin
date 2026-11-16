package app

import (
	"os"
	"runtime"
	"testing"

	"github.com/ebitengine/purego"
)

func init() { runtime.LockOSThread() }

// AVPlayer needs the main thread's run loop even when a Tester draws the UI.
func TestMain(m *testing.M) {
	h, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	var run func()
	purego.RegisterLibFunc(&run, h, "CFRunLoopRun")
	go func() { os.Exit(m.Run()) }()
	run()
}
