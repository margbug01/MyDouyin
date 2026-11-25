package app

import (
	"strings"

	"douyin/internal/media"
)

var videoHeaders = map[string]string{
	"Referer":    "https://www.douyin.com/",
	"User-Agent": "Mozilla/5.0",
}

var liveHeaders = map[string]string{
	"Referer":    "https://live.douyin.com/",
	"User-Agent": "Mozilla/5.0",
}

type nextVideo struct {
	slot  *slot
	url   string
	asset *media.WarmVideo
}

func (n *nextVideo) close() {
	if n.asset != nil {
		n.asset.Close()
	}
	*n = nextVideo{}
}

func (a *app) preloadNextVideo(p *page) {
	if !media.NativeAvailable || a.win == nil || a.paused || !p.plays() || p.idx+1 >= len(p.list) {
		a.preload.close()
		return
	}
	current, next := p.list[p.idx], p.list[p.idx+1]
	if current.v.Live != nil || current.vp == nil || current.vp.Err() != nil || next.v.Live != nil || next.vp != nil {
		a.preload.close()
		return
	}
	url := videoSource(next.v, a.quality)
	if a.preload.slot == next && a.preload.url == url {
		return
	}
	a.preload.close()
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		a.preload = nextVideo{slot: next, url: url, asset: media.PreloadVideo(url, videoHeaders)}
	}
}
