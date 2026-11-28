package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync/atomic"
	"time"
)

// mainStallAfter is how long the main thread may not answer before its
// stacks are written: as long as Windows waits before it calls a window
// not responding.
const mainStallAfter = 5 * time.Second

// watchMainThread writes every goroutine's stack to file each time the main
// thread has not answered for after, until ctx ends, so a window that
// stopped responding leaves where it stood for its report to bring. post
// runs a function on the main thread.
func watchMainThread(ctx context.Context, post func(func()), file string, after time.Duration) {
	start := time.Now()
	var answered atomic.Int64 // time.Since(start) at the main thread's last answer
	var asking atomic.Bool
	written := false
	tick := time.NewTicker(after / 5)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if asking.CompareAndSwap(false, true) {
			post(func() {
				answered.Store(int64(time.Since(start)))
				asking.Store(false)
			})
		}
		still := time.Since(start) - time.Duration(answered.Load())
		if still < after {
			written = false
			continue
		}
		if written {
			continue
		}
		written = true
		still = still.Round(100 * time.Millisecond)
		buf := make([]byte, 16<<20)
		buf = buf[:runtime.Stack(buf, true)]
		head := fmt.Sprintf("%s: the main thread has not answered for %v\n\n", time.Now().Format(time.RFC3339), still)
		if err := os.WriteFile(file, append([]byte(head), buf...), 0o644); err != nil {
			log.Printf("douyin: main thread stalled %v: %v", still, err)
			continue
		}
		log.Printf("douyin: main thread stalled %v, stacks in %s", still, file)
	}
}
