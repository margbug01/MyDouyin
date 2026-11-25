package main

import (
	"os"
	"runtime"
	"testing"

	"github.com/ebitengine/purego"
)

func init() { runtime.LockOSThread() } // the main goroutine stays on the main thread

// TestMain runs the main thread's run loop, as an app does, while the tests
// run beside it: AVPlayer loads a stream through it.
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
