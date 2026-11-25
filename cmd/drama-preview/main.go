package main

import (
	"douyin/internal/drama"
	"embed"
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed clips/*.mp4
var fixtures embed.FS

type catalog struct{ base string }

func (c catalog) Series() drama.Series {
	return drama.Series{ID: "test-patterns", Title: "短剧预览 · 影像测试场", Episodes: []drama.Episode{{ID: "motion", Title: "第 1 集 · 动态图形", URL: c.base + "/1.mp4"}, {ID: "bars", Title: "第 2 集 · 彩条", URL: c.base + "/2.mp4"}}}
}
func main() {
	d, e := os.UserConfigDir()
	if e != nil {
		log.Fatal(e)
	}
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		log.Fatal(e)
	}
	clips, e := fs.Sub(fixtures, "clips")
	if e != nil {
		log.Fatal(e)
	}
	server := &http.Server{Handler: http.FileServerFS(clips)}
	go server.Serve(l)
	defer server.Close()
	mygo.App.WhenReady(func() {
		a, e := drama.NewPage(catalog{fmt.Sprint("http://", l.Addr())}, filepath.Join(d, "MyDouyin-DramaPreview", "test-patterns.json"))
		if e != nil {
			log.Fatal(e)
		}
		w := mygo.NewWindow(mygo.WindowOptions{Title: "MyDouyin · 短剧预览", Width: 800, Height: 600, Content: ui.View(a.View)})
		w.OnClosed(a.Close)
	})
	if e := mygo.App.Run(); e != nil {
		log.Fatal(e)
	}
}
