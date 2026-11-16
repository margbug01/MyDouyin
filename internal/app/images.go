package app

import (
	"bytes"
	"fmt"
	"image"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
)

// images loads pictures (covers, avatars) by address for the view, a few at
// a time, and keeps those shown lately: a grid scrolled far down lets the
// ones above go, and loads them again if they come back.
type images struct {
	fetch    func(url string) ([]byte, error)
	post     func(func()) // runs on the main thread, where get and sweep run
	m        map[string]*picture
	slots    chan struct{} // downloads at once
	decoding chan struct{} // full-size source images being decoded at once
	frame    int
	bytes    int
}

type picture struct {
	bmp     *ui.Bitmap
	loading bool
	failed  bool // kept, so a broken address is not asked for every frame
	used    int  // the frame that last showed it
	bytes   int
}

const keepPictures = 96
const pictureBudget = 24 << 20 // decoded pixels; GPU textures may hold another copy
const pictureEdge = 720

func newImages(fetch func(string) ([]byte, error), post func(func())) *images {
	return &images{fetch: fetch, post: post, m: map[string]*picture{}, slots: make(chan struct{}, 6), decoding: make(chan struct{}, 2)}
}

// get returns the picture at url, nil while it loads or if it failed; asking
// starts the loading. A nil images has none, as in a test without a client.
func (im *images) get(url string) *ui.Bitmap {
	if im == nil || url == "" {
		return nil
	}
	p := im.m[url]
	if p == nil {
		p = &picture{}
		im.m[url] = p
	}
	p.used = im.frame
	if p.bmp == nil && !p.loading && !p.failed {
		select {
		case im.slots <- struct{}{}:
		default:
			return nil // the next redraw retries visible pictures; no unbounded queue
		}
		p.loading = true
		go func() {
			b, err := im.fetch(url)
			var bmp *ui.Bitmap
			if err == nil {
				im.decoding <- struct{}{}
				bmp, err = decodePicture(b)
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
		}()
	}
	return p.bmp
}

// sweep ends a frame: past keepPictures, those it did not show go.
func (im *images) sweep() {
	if im == nil {
		return
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
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16<<20 {
		return nil, fmt.Errorf("cover dimensions exceed limit: %dx%d", cfg.Width, cfg.Height)
	}
	if max(cfg.Width, cfg.Height) <= pictureEdge {
		return ui.DecodeBitmap(data)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	w, h := max(1, cfg.Width*pictureEdge/max(cfg.Width, cfg.Height)), max(1, cfg.Height*pictureEdge/max(cfg.Width, cfg.Height))
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
	return ui.NewBitmap(small), nil
}
