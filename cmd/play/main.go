// play: open one local mp4 in a MyGo window with sound. usage: play file.mp4
package main

import (
	"log"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"douyin/internal/media"
)

func main() {
	f, err := media.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	vd, err := f.Video()
	if err != nil {
		log.Fatal(err)
	}
	vp, err := ui.NewVideoPlayer(vd)
	if err != nil {
		log.Fatal(err)
	}
	vp.SetLoop(true)
	ad, err := f.Audio()
	if err != nil {
		log.Fatal(err)
	}
	ap := audio.NewPlayer(ad)
	ap.SetLoop(true).Play()
	log.Printf("%dx%d %v", f.W, f.H, f.Duration)

	mygo.App.WhenReady(func() {
		w := mygo.NewWindow(mygo.WindowOptions{Title: "play", Width: 405, Height: 720,
			Content: ui.View(func(c *ui.Context) {
				ui.Box(c).Fill().Children(func() { ui.Video(c, vp).Fill().Fit(ui.Contain) })
			})})
		// DY_SHOT=file.png saves what the window shows after 3 s, to check the picture.
		if shot := os.Getenv("DY_SHOT"); shot != "" {
			time.AfterFunc(3*time.Second, func() {
				b, err := w.CapturePage()
				if err == nil {
					err = os.WriteFile(shot, b, 0o644)
				}
				log.Println("shot:", err)
				os.Exit(0)
			})
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
