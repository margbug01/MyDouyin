// nativevideo compares the current RGBA path with AVPlayerLayer, using identical UI.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"douyin/internal/media"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type probe struct {
	mode, out             string
	win, pip, active      *mygo.Window
	live                  *media.Live
	layer                 *media.Layer
	bitmap                *ui.Bitmap
	err                   error
	paused, cover, dialog bool
	frames                int
	mainRect              ui.Rect
	started               time.Time
}

func (p *probe) toggle() {
	p.paused = !p.paused
	if p.paused {
		p.live.Pause()
	} else {
		p.live.Play()
	}
}

func (p *probe) view(c *ui.Context, w *mygo.Window) {
	p.frames++
	t := *ui.DarkTheme()
	t.Background = ui.RGBA(0, 0, 0, 0)
	c.SetTheme(&t)
	if p.live != nil && w == p.active {
		f, err := p.live.Frame()
		if err == io.EOF {
			err = p.live.Rewind()
		}
		if err != nil {
			p.err = err
		}
		if f.Image != nil && p.bitmap != nil {
			p.bitmap.Update(f.Image)
		}
	}
	ui.Column(c).Fill().Padding(16).Gap(10).Children(func() {
		ui.Text(c, "原生视频直出试验 · "+p.mode).FontSize(17).TextColor(ui.Hex("#ffffff"))
		box := ui.Box(c).Grow(1).Clip().Radius(12)
		box.Children(func() {
			if p.live == nil {
				ui.Text(c, "载入中…").TextColor(ui.Hex("#ffffff"))
				return
			}
			if w != p.active {
				ui.Text(c, "视频在另一个窗口播放").TextColor(ui.Hex("#ffffff"))
				return
			}
			if p.layer != nil {
				box.Draw(func(_ *ui.Painter, r ui.Rect) {
					if w == p.win {
						p.mainRect = r
					}
					if err := p.layer.Place(w.NativeHandle(), float64(r.X), float64(r.Y), float64(r.W), float64(r.H), p.cover); err != nil {
						p.err = err
					}
				})
			} else if p.bitmap != nil {
				fit := ui.Contain
				if p.cover {
					fit = ui.Cover
				}
				ui.Image(c, p.bitmap).Fill().Fit(fit)
			}
			if p.started.IsZero() {
				p.started = time.Now()
			}
			offset := float32(math.Mod(time.Since(p.started).Seconds()*90, 600))
			ui.Text(c, "弹幕叠加测试：视频继续播放").Absolute().Top(50).Left(30+offset).FontSize(23).TextColor(ui.Hex("#ffffff")).TextShadow(0, 1, 3, ui.Hex("#000000"))
			ui.Text(c, "原生画面上方的半透明控件").Absolute().Bottom(20).Left(20).Padding(10).Radius(8).Background(ui.RGBA(0, 0, 0, 0.6)).TextColor(ui.Hex("#ffffff"))
		})
		ui.Row(c).Gap(10).Children(func() {
			if p.live == nil {
				return
			}
			label := "暂停"
			if p.paused {
				label = "继续"
			}
			if ui.Button(c, label).Clicked() {
				p.toggle()
			}
			if ui.Button(c, "跳到2秒").Clicked() {
				p.live.Seek(2 * time.Second)
			}
			if ui.Button(c, "填满 / 适应").Clicked() {
				p.cover = !p.cover
			}
			if ui.Button(c, "小窗 / 返回").Clicked() {
				p.move()
			}
			if ui.Button(c, "叠加弹窗").Clicked() {
				p.dialog = true
			}
			ui.Text(c, fmt.Sprintf("%.2fs", p.live.Position().Seconds())).TextColor(ui.Hex("#ffffff"))
		})
		if p.err != nil {
			ui.Text(c, p.err.Error()).TextColor(ui.Hex("#ff6666"))
		}
	})
	ui.DialogBase(c.Key("dialog"), &p.dialog, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(340).Padding(24).Radius(12).Background(ui.Hex("#252632")).Children(func() {
			ui.Text(c, "弹窗在视频上方").TextColor(ui.Hex("#ffffff")).FontSize(20)
			if ui.Button(c, "关闭").Clicked() {
				p.dialog = false
			}
		})
	})
	if p.live != nil && !p.paused {
		c.AnimationFrame()
	} else {
		c.After(100 * time.Millisecond)
	}
}

func (p *probe) move() {
	if p.pip != nil {
		old := p.pip
		p.active, p.pip = p.win, nil
		if p.layer != nil {
			r := p.mainRect
			if err := p.layer.Place(p.win.NativeHandle(), float64(r.X), float64(r.Y), float64(r.W), float64(r.H), p.cover); err != nil {
				p.err = err
			}
		}
		p.win.Invalidate()
		old.Close()
		return
	}
	var pip *mygo.Window
	pip = mygo.NewWindow(mygo.WindowOptions{Title: "视频小窗试验", Width: 640, Height: 440, MinWidth: 640, MinHeight: 440, BackgroundColor: "#161823", Content: ui.View(func(c *ui.Context) { p.view(c, pip) })})
	p.pip, p.active = pip, pip
	pip.OnClosed(func() {
		if p.pip == pip {
			p.pip = nil
			p.active = p.win
			p.win.Invalidate()
		}
	})
	p.win.Invalidate()
}

func (p *probe) call(fn func()) {
	done := make(chan struct{})
	p.win.Update(func() { fn(); close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		panic("nativevideo: main-thread callback stalled")
	}
}

func (p *probe) sample(i int) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	log.Printf("sample=%d pid=%d heap=%.2f MiB", i, os.Getpid(), float64(m.HeapAlloc)/(1<<20))
	p.call(func() {
		ready := p.bitmap != nil
		if p.layer != nil {
			ready = p.layer.Ready() && p.layer.AttachedTo(p.active.NativeHandle())
		}
		frame := objc.Send[captureRect](objc.ID(p.active.NativeHandle()), objc.RegisterName("frame"))
		log.Printf("ready=%v at=%v builds=%d windowFrame=%+v error=%v", ready, p.live.Position(), p.frames, frame, p.err)
		if p.err != nil || !ready {
			log.Fatalf("video not ready or attached to the wrong window: %v", p.err)
		}
		if p.frames < 30 {
			log.Fatal("window did not keep drawing; do not use occluded-window memory numbers")
		}
	})
	b, err := exec.Command("footprint", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.out, fmt.Sprintf("footprint-%d.txt", i)), b, 0600); err != nil {
		log.Fatal(err)
	}
	b, err = exec.Command("ps", "-p", strconv.Itoa(os.Getpid()), "-o", "rss=,%cpu=").Output()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("RSS KiB and CPU: %s", b)
}

func (p *probe) check() {
	for i := 0; i < 5; i++ {
		time.Sleep(5 * time.Second)
		p.sample(i)
		switch i {
		case 0:
			var pausedAt time.Duration
			p.call(func() { p.toggle(); pausedAt = p.live.Position() })
			time.Sleep(300 * time.Millisecond)
			p.call(func() {
				if d := p.live.Position() - pausedAt; d > 80*time.Millisecond || d < -80*time.Millisecond {
					log.Fatalf("pause drift: %v", d)
				}
				p.toggle()
				p.live.Seek(2 * time.Second)
			})
			time.Sleep(400 * time.Millisecond)
			p.call(func() {
				at := p.live.Position()
				if at < 1900*time.Millisecond || at > 3500*time.Millisecond {
					log.Fatalf("seek position: %v", at)
				}
				log.Print("PASS pause/resume and seek")
			})
		case 1:
			p.call(p.move)
		case 2:
			p.call(func() { p.cover = true })
		case 3:
			p.call(p.move)
		}
	}
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	log.Printf("GC heap=%.2f MiB", float64(m.HeapAlloc)/(1<<20))
	p.call(func() { p.dialog = true })
	time.Sleep(200 * time.Millisecond)
	b, err := p.win.CapturePage()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.out, "mygo-dialog.png"), b, 0600); err != nil {
		log.Fatal(err)
	}
	number := 0
	p.call(func() { number = objc.Send[int](objc.ID(p.win.NativeHandle()), objc.RegisterName("windowNumber")) })
	if b, err = captureOwnWindow(number, 1280, 820); err != nil {
		log.Printf("native window capture: %v", err)
	} else {
		if err := os.WriteFile(filepath.Join(p.out, "window-dialog.png"), b, 0600); err != nil {
			log.Fatal(err)
		}
		log.Print("Native window captured for visual review")
	}
	p.call(func() {
		if p.layer != nil {
			p.layer.Close()
		}
		p.live.Close()
	})
	log.Print("PASS native controls, transfer, and close")
	os.Exit(0)
}

func main() {
	mode := flag.String("mode", "layer", "layer or bitmap")
	out := flag.String("out", "", "diagnostic output directory")
	check := flag.Bool("check", false, "check controls, transfer and sample memory, then exit")
	flag.Parse()
	if flag.NArg() != 1 || (*mode != "layer" && *mode != "bitmap") {
		log.Fatal("usage: nativevideo [-mode layer|bitmap] [-check] [-out directory] clip.mp4")
	}
	if *out == "" {
		home, _ := os.UserHomeDir()
		*out = filepath.Join(home, "Library/Application Support/Alma Go/temp", time.Now().Format("2006-01-02"), "native-layer", *mode)
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		log.Fatal(err)
	}
	clip, err := filepath.Abs(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	address := (&url.URL{Scheme: "file", Path: clip}).String()
	debug.SetGCPercent(25)
	p := &probe{mode: *mode, out: *out}
	mygo.App.WhenReady(func() {
		p.win = mygo.NewWindow(mygo.WindowOptions{Title: "MyDouyin 视频显示对照", Width: 1280, Height: 820, MinWidth: 1280, MinHeight: 820, BackgroundColor: "#161823", Content: ui.View(func(c *ui.Context) { p.view(c, p.win) })})
		p.active = p.win
		p.win.OnClosed(func() {
			if p.layer != nil {
				p.layer.Close()
			}
			if p.pip != nil {
				pip := p.pip
				p.pip = nil
				pip.Close()
			}
			if p.live != nil {
				p.live.Close()
			}
		})
		go func() {
			live, err := media.OpenVideo(address, nil)
			if err != nil {
				log.Fatal(err)
			}
			var bitmap *ui.Bitmap
			if *mode == "bitmap" {
				f, err := live.Next()
				if err != nil {
					log.Fatal(err)
				}
				bitmap = ui.NewBitmap(f.Image)
			}
			p.call(func() {
				p.live, p.bitmap = live, bitmap
				if *mode == "layer" {
					p.layer, p.err = live.NewLayer()
					if p.err != nil {
						log.Fatal(p.err)
					}
				}
				p.win.Show()
				p.win.Focus()
				p.win.Invalidate()
			})
			if *check {
				p.check()
			}
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
