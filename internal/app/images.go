package app

import "github.com/egoist/mygo/ui"

// images loads pictures (covers, avatars) by address for the view, a few at
// a time, and keeps those shown lately: a grid scrolled far down lets the
// ones above go, and loads them again if they come back.
type images struct {
	fetch func(url string) ([]byte, error)
	post  func(func()) // runs on the main thread, where get and sweep run
	m     map[string]*picture
	slots chan struct{} // downloads at once
	frame int
}

type picture struct {
	bmp     *ui.Bitmap
	loading bool
	failed  bool // kept, so a broken address is not asked for every frame
	used    int  // the frame that last showed it
}

// keepPictures is how many pictures stay before those not on screen go:
// some 300 KB each as covers, so some 60 MB.
const keepPictures = 200

func newImages(fetch func(string) ([]byte, error), post func(func())) *images {
	return &images{fetch: fetch, post: post, m: map[string]*picture{}, slots: make(chan struct{}, 6)}
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
		p.loading = true
		go func() {
			im.slots <- struct{}{}
			b, err := im.fetch(url)
			<-im.slots
			var bmp *ui.Bitmap
			if err == nil {
				bmp, err = ui.DecodeBitmap(b)
			}
			im.post(func() { p.loading, p.bmp, p.failed = false, bmp, err != nil })
		}()
	}
	return p.bmp
}

// sweep ends a frame: past keepPictures, those it did not show go.
func (im *images) sweep() {
	if im == nil {
		return
	}
	if len(im.m) > keepPictures {
		for url, p := range im.m {
			if !p.loading && p.used < im.frame {
				delete(im.m, url)
			}
		}
	}
	im.frame++
}
