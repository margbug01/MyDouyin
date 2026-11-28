package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchMainThreadWritesStacksOnlyWhileItStalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	file := filepath.Join(t.TempDir(), "main-thread-stall.txt")
	var stuck atomic.Bool
	post := func(fn func()) {
		if !stuck.Load() {
			fn()
		}
	}
	done := make(chan struct{})
	go func() { watchMainThread(ctx, post, file, 100*time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()

	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("wrote stacks while the main thread answered")
	}
	stuck.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, err := os.ReadFile(file)
		if err == nil && strings.Contains(string(b), "has not answered") && strings.Contains(string(b), "watchMainThread") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no stacks 2 s into a stall: %v %q", err, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
