package main

import (
	"log"
	"runtime/debug"

	"douyin/internal/app"
)

func main() {
	// The heap is mostly reused video frames, so GC is rare (0.02% CPU measured
	// at 25); the default 100 let idle garbage add ~50 MB to the footprint.
	debug.SetGCPercent(25)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
