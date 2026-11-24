package app

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
)

// images loads pictures (covers, avatars) by address for the view, a few at
// a time, and keeps those shown lately: a grid scrolled far down lets the
// ones above go, and loads them again if they come back.
type images struct {
	fetch         func(url string) ([]byte, error)
	post          func(func()) // runs on the main thread, where get and sweep run
	m             map[string]*picture
	slots         chan struct{} // downloads at once
	decoding      chan struct{} // full-size source images being decoded at once
	frame         int
	bytes         int
	releasePixels bool // true on GPU windows: drop CPU pixels after GPU upload
}

type picture struct {
	bmp      *ui.Bitmap
	loading  bool
	failed   bool // kept, so a broken address is not asked for every frame
	used     int  // the frame that last showed it
	drawn    int  // first frame (1-based) that returned bmp to the view
	released bool // ReleasePixels called after GPU upload
	bytes    int
}

const (
	keepPictures    = 48
	pictureBudget   = 12 << 20 // decoded pixels; GPU textures may hold another copy
	commentFullEdge = 1600
	pictureEdge     = 720
	coverEdge       = 540
	thumbEdge       = 320
	commentPicEdge  = 256
	avatarEdge      = 160
	emojiEdge       = 64
)

func newImages(fetch func(string) ([]byte, error), post func(func())) *images {
	return &images{fetch: fetch, post: post, m: map[string]*picture{}, slots: make(chan struct{}, 6), decoding: make(chan struct{}, 2)}
}

// get returns the picture at url decoded up to pictureEdge, nil while it loads
// or if it failed; asking starts the loading. A nil images has none, as in a
// test without a client.
func (im *images) get(url string) *ui.Bitmap {
	return im.getSized(url, pictureEdge)
}

// getSized returns the picture at url scaled to fit within maxEdge pixels on
// its longest side, keeping memory proportional to the display size.
func (im *images) getSized(url string, maxEdge int) *ui.Bitmap {
	return im.getKeyed(url, url, maxEdge)
}

// getFull returns the picture at url decoded up to commentFullEdge under a
// distinct cache key so a lightbox never reuses a 256px comment thumbnail.
func (im *images) getFull(url string) *ui.Bitmap {
	if url == "" {
		return nil
	}
	return im.getKeyed("@full:"+url, url, commentFullEdge)
}

func (im *images) getKeyed(key, url string, maxEdge int) *ui.Bitmap {
	if im == nil || url == "" {
		return nil
	}
	if maxEdge <= 0 {
		maxEdge = pictureEdge
	}
	p := im.m[key]
	if p == nil {
		select {
		case im.slots <- struct{}{}:
		default:
			return nil // the next redraw retries visible pictures; no unbounded map or queue
		}
		p = &picture{loading: true, used: im.frame}
		im.m[key] = p
		go im.load(url, p, maxEdge)
		return nil
	}
	p.used = im.frame
	if p.bmp == nil && !p.loading && !p.failed {
		select {
		case im.slots <- struct{}{}:
		default:
			return nil
		}
		p.loading = true
		go im.load(url, p, maxEdge)
		return nil
	}
	if p.bmp != nil && p.drawn == 0 {
		p.drawn = im.frame + 1
	}
	return p.bmp
}

func (im *images) load(url string, p *picture, maxEdge int) {
	b, err := im.fetch(url)
	var bmp *ui.Bitmap
	if err == nil {
		im.decoding <- struct{}{}
		bmp, err = decodePictureEdge(b, maxEdge)
		<-im.decoding
	}
	<-im.slots
	im.post(func() {
		p.loading, p.bmp, p.failed = false, bmp, err != nil
		if bmp != nil {
			w, h := bmp.Size()
			p.bytes = w * h * 4
			im.bytes += p.bytes
		}
	})
}

// sweep ends a frame: releases CPU pixels of bitmaps already uploaded to the
// GPU, and past keepPictures or pictureBudget evicts pictures not shown this frame.
func (im *images) sweep() {
	if im == nil {
		return
	}
	if im.releasePixels {
		for _, p := range im.m {
			if p.bmp != nil && !p.released && p.drawn > 0 && im.frame >= p.drawn {
				p.bmp.ReleasePixels()
				p.released = true
			}
		}
	}
	if len(im.m) > keepPictures || im.bytes > pictureBudget {
		for url, p := range im.m {
			if !p.loading && p.used < im.frame {
				im.bytes -= p.bytes
				delete(im.m, url)
			}
		}
	}
	im.frame++
}

func decodePicture(data []byte) (*ui.Bitmap, error) {
	return decodePictureEdge(data, pictureEdge)
}

func decodePictureEdge(data []byte, maxEdge int) (*ui.Bitmap, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16<<20 {
		return nil, fmt.Errorf("cover dimensions exceed limit: %dx%d", cfg.Width, cfg.Height)
	}
	if max(cfg.Width, cfg.Height) <= maxEdge {
		return ui.DecodeBitmap(data)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	w, h := max(1, cfg.Width*maxEdge/max(cfg.Width, cfg.Height)), max(1, cfg.Height*maxEdge/max(cfg.Width, cfg.Height))
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
	// Re-encode the downscaled image so ui.DecodeBitmap keeps only compact
	// compressed bytes and ReleasePixels can drop 100% of CPU RGBA pixels.
	var buf bytes.Buffer
	if format == "jpeg" || isOpaqueRGBA(small) {
		err = jpeg.Encode(&buf, small, &jpeg.Options{Quality: 82})
	} else {
		err = png.Encode(&buf, small)
	}
	if err == nil {
		if bmp, derr := ui.DecodeBitmap(buf.Bytes()); derr == nil {
			return bmp, nil
		}
	}
	return ui.NewBitmap(small), nil
}

func isOpaqueRGBA(img *image.RGBA) bool {
	pix := img.Pix
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0xff {
			return false
		}
	}
	return true
}
