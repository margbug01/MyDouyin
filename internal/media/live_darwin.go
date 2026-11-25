package media

import (
	"errors"
	"image"
	"io"
	"structs"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo/ui"
)

// Live plays HLS or an on-demand video with AVPlayer, which plays its
// sound itself, and hands its pictures to ui.VideoPlayer as they come due:
// both follow AVPlayer's clock, so they keep together.
type Live struct {
	player, item, out objc.ID

	mu       sync.Mutex // Close against a Next on the player's goroutine
	closed   bool
	last     float64 // the host time of the last picture handed out
	vod      bool
	paused   bool
	speed    float32
	seekDone *atomic.Bool
	frame    *image.RGBA
}

var (
	liveOnce         sync.Once
	caCurrentTime    func() float64
	clsPlayer        objc.Class
	clsPlayerItem    objc.Class
	clsItemOutput    objc.Class
	errLiveNoPicture = errors.New("media: the stream sent no picture for 20 s")
)

func liveInit() {
	h, err := purego.Dlopen("/System/Library/Frameworks/QuartzCore.framework/QuartzCore", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	purego.RegisterLibFunc(&caCurrentTime, h, "CACurrentMediaTime")
	clsPlayer, clsPlayerItem = objc.GetClass("AVPlayer"), objc.GetClass("AVPlayerItem")
	clsItemOutput = objc.GetClass("AVPlayerItemVideoOutput")
}

// OpenLive starts the stream at url, playing silent: a player asks for its
// first picture as it is made, which a paused stream never sends.
func OpenLive(url string) (*Live, error) {
	return openStream(url, nil, false, true)
}

// OpenLiveHeaders starts the live stream at url with custom HTTP headers.
func OpenLiveHeaders(url string, headers map[string]string) (*Live, error) {
	return openStream(url, headers, false, true)
}

// OpenVideo streams an MP4 using AVPlayer's range loading and audio clock.
func OpenVideo(url string, headers map[string]string) (*Live, error) {
	return openStream(url, headers, true, true)
}

// OpenNative opens a stream for NewLayer without any pixel output: one attached
// even briefly makes the decoder keep a BGRA pool (~120 MB at 1080p) for the
// item's life. Wait with WaitReady instead of Next.
func OpenNative(url string, headers map[string]string, vod bool) (*Live, error) {
	return openStream(url, headers, vod, false)
}

func openStream(url string, headers map[string]string, vod, pixels bool) (*Live, error) {
	defer autorelease()()
	asset, err := videoAsset(url, headers)
	if err != nil {
		return nil, err
	}
	return openAsset(asset, vod, pixels)
}

func videoAsset(url string, headers map[string]string) (objc.ID, error) {
	if url == "" {
		return 0, errors.New("media: empty stream address")
	}
	u := objc.ID(clsURL).Send(sel("URLWithString:"), nsString(url))
	if u == 0 {
		return 0, errors.New("media: bad stream address")
	}
	h := nsDict()
	for k, v := range headers {
		h.Send(sel("setObject:forKey:"), nsString(v), nsString(k))
	}
	// Without AVURLAssetUsesNoPersistentCacheKey, which WebKit sets too, the
	// player downloads the whole MP4 at once into $TMPDIR/MediaCache: 466 MB
	// fetched and 424 MB written for two 225 MB videos watched 8 s each. With
	// it, it reads ahead about 40 MB in memory and nothing goes to disk.
	yes := objc.ID(objc.GetClass("NSNumber")).Send(sel("numberWithBool:"), true)
	return objc.ID(clsAsset).Send(sel("URLAssetWithURL:options:"), u,
		nsDict("AVURLAssetHTTPHeaderFieldsKey", h, "AVURLAssetUsesNoPersistentCacheKey", yes)), nil
}

func openAsset(asset objc.ID, vod, pixels bool) (*Live, error) {
	liveOnce.Do(liveInit)
	l := &Live{vod: vod, speed: 1}
	l.item = objc.ID(clsPlayerItem).Send(sel("playerItemWithAsset:"), asset)
	if pixels {
		l.out = newPixelOutput()
	}
	if l.item == 0 || pixels && l.out == 0 {
		l.out.Send(sel("release"))
		return nil, errors.New("media: cannot open the stream")
	}
	l.item.Send(sel("retain"))
	if pixels {
		l.item.Send(sel("addOutput:"), l.out)
	}
	l.player = objc.ID(clsPlayer).Send(sel("playerWithPlayerItem:"), l.item)
	l.player.Send(sel("retain"))
	l.player.Send(sel("setMuted:"), true)
	if vod {
		l.player.Send(sel("setAutomaticallyWaitsToMinimizeStalling:"), false)
		l.item.Send(sel("setPreferredForwardBufferDuration:"), float64(3))
		l.player.Send(sel("play"))
	} else {
		// Start with the first decodable segment. AVPlayer still handles rebuffering.
		l.player.Send(sel("playImmediatelyAtRate:"), l.speed)
	}
	return l, nil
}

// Play and Pause the stream, its sound with it; SetMuted silences it.
// Each method drains its own pool: callers include goroutines whose threads
// never drain one, and a single autoreleased item would outlive Close.
func (l *Live) Play() {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed && (l.vod || l.paused) {
		l.paused = false
		if l.seekDone == nil {
			l.player.Send(sel("setRate:"), l.speed)
		}
	}
}
func (l *Live) Pause() {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.paused = true
		l.player.Send(sel("pause"))
	}
}

func (l *Live) SetSpeed(speed float64) {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.speed = float32(speed)
	if !l.closed && !l.paused && l.seekDone == nil {
		l.player.Send(sel("setRate:"), l.speed)
	}
}

func (l *Live) Position() time.Duration {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		if t := objc.Send[cmTime](l.player, sel("currentTime")); t.valid() {
			return t.dur()
		}
	}
	return 0
}

func (l *Live) Seeking() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.closed && l.seekDone != nil && !l.seekDone.Load()
}

func (l *Live) Seek(at time.Duration) {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed && l.vod {
		t := cmTime{Value: int64(at), Timescale: int32(time.Second), Flags: 1}
		zero := cmTime{Timescale: 1, Flags: 1}
		done := &atomic.Bool{}
		l.seekDone = done
		completed := objc.NewBlock(func(_ objc.Block, _ bool) { done.Store(true) })
		defer completed.Release()
		l.player.Send(sel("seekToTime:toleranceBefore:toleranceAfter:completionHandler:"), t, zero, zero, completed)
		l.last = 0
	}
}
func (l *Live) SetMuted(m bool) {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.player.Send(sel("setMuted:"), m)
	}
}

// SetVolume sets the stream's sound, from 0 to 1.
func (l *Live) SetVolume(v float64) {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.player.Send(sel("setVolume:"), float32(v))
	}
}

// Frame returns the picture due now, or no image when none is due yet.
// Like Next, it reuses its image; the caller must finish drawing before polling again.
func (l *Live) Frame() (ui.VideoFrame, error) {
	deadline := time.Now().Add(20 * time.Second)
	f, err, _ := l.poll(&deadline)
	return f, err
}

// Next waits for the stream's next picture to come due and returns it,
// lasting as long as the one before it did, so the player shows each as it
// comes. It gives up after 20 s without one, not counting while paused.
func (l *Live) Next() (ui.VideoFrame, error) {
	deadline := time.Now().Add(20 * time.Second)
	for {
		if f, err, ok := l.poll(&deadline); ok {
			return f, err
		}
		if time.Now().After(deadline) {
			return ui.VideoFrame{}, errLiveNoPicture
		}
		time.Sleep(4 * time.Millisecond)
	}
}

// poll is one look for a due picture. It drains its own autorelease pool:
// AVPlayer's getters autorelease the item, and this goroutine's OS thread has
// no pool that ever drains, so without it the item, its asset and its HTTP
// cache (tens of MB a video) were never freed.
func (l *Live) poll(deadline *time.Time) (ui.VideoFrame, error, bool) {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ui.VideoFrame{}, io.EOF, true
	}
	if objc.Send[int](l.item, sel("status")) == 2 { // failed
		return ui.VideoFrame{}, itemError(l.item), true
	}
	if l.seekDone != nil && l.seekDone.Load() {
		l.seekDone = nil
		if !l.paused {
			l.player.Send(sel("setRate:"), l.speed)
		}
	}
	at := objc.Send[cmTime](l.player, sel("currentTime"))
	if l.vod && !l.paused && l.seekDone == nil {
		end := objc.Send[cmTime](l.item, sel("duration"))
		if at.valid() && end.valid() && at.dur() >= end.dur() {
			return ui.VideoFrame{}, io.EOF, true
		}
	}
	if l.paused {
		*deadline = time.Now().Add(20 * time.Second)
	}
	if l.out == 0 { // AVPlayerLayer owns presentation; only service the clock and seeks.
		return ui.VideoFrame{}, nil, false
	}
	now := caCurrentTime()
	t := objc.Send[cmTime](l.out, sel("itemTimeForHostTime:"), now)
	var img *image.RGBA
	if l.seekDone == nil && objc.Send[bool](l.out, sel("hasNewPixelBufferForItemTime:"), t) {
		if pb := uintptr(l.out.Send(sel("copyPixelBufferForItemTime:itemTimeForDisplay:"), t, unsafe.Pointer(nil))); pb != 0 {
			l.frame = bgra(pb, l.frame)
			img = l.frame
			cfRelease(pb)
		}
	}
	if img != nil {
		dur := time.Second / 30
		if l.last > 0 {
			// A stalled/hidden view must not display its next frame for the whole gap.
			dur = min(max(time.Duration((now-l.last)*float64(time.Second)), 5*time.Millisecond), 100*time.Millisecond)
		}
		l.last = now
		return ui.VideoFrame{Image: img, Duration: dur}, nil, true
	}
	return ui.VideoFrame{}, nil, false
}

// WaitReady waits until the item can play and returns its picture size, for
// streams opened without pixels.
func (l *Live) WaitReady() (int, int, error) {
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if w, h, err := l.ready(); err != nil || w > 0 && h > 0 {
			return w, h, err
		}
	}
	return 0, 0, errLiveNoPicture
}

func (l *Live) ready() (int, int, error) {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, 0, io.EOF
	}
	switch objc.Send[int](l.item, sel("status")) {
	case 2:
		return 0, 0, itemError(l.item)
	case 1:
		sz := objc.Send[struct {
			_    structs.HostLayout
			W, H float64
		}](l.item, sel("presentationSize"))
		return int(sz.W), int(sz.H), nil
	}
	return 0, 0, nil
}

func itemError(item objc.ID) error {
	if item != 0 {
		if nserr := item.Send(sel("error")); nserr != 0 {
			if desc := nserr.Send(sel("localizedDescription")); desc != 0 {
				if msg := objc.Send[string](desc, sel("UTF8String")); msg != "" {
					return errors.New("media: " + msg)
				}
			}
		}
	}
	return errors.New("media: the stream failed to play")
}

// Rewind loops a video; live rooms have no start to go back to.
func (l *Live) Rewind() error {
	if l.vod {
		l.Seek(0)
		l.Play()
	}
	return nil
}

// Close stops the stream and its sound, and lets them go.
func (l *Live) Close() error {
	defer autorelease()()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.player.Send(sel("pause"))
	l.item.Send(sel("cancelPendingSeeks"))
	l.item.Send(sel("asset")).Send(sel("cancelLoading"))
	l.item.Send(sel("removeOutput:"), l.out)
	l.player.Send(sel("replaceCurrentItemWithPlayerItem:"), objc.ID(0))
	l.player.Send(sel("release"))
	l.out.Send(sel("release"))
	l.item.Send(sel("release"))
	l.frame = nil
	return nil
}

// bgra copies a BGRA pixel buffer into an RGBA image.
func bgra(pb uintptr, img *image.RGBA) *image.RGBA {
	cvPixelBufferLock(pb, 1)
	defer cvPixelBufferUnlock(pb, 1)
	w, h, stride := int(cvPixelBufferWidth(pb)), int(cvPixelBufferHeight(pb)), int(cvPixelBufferStride(pb))
	src := unsafe.Slice((*byte)(cvPixelBufferBase(pb)), stride*h)
	if img == nil || img.Rect.Dx() != w || img.Rect.Dy() != h {
		img = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < h; y++ {
		s, d := src[y*stride:y*stride+w*4], img.Pix[y*img.Stride:]
		for x := 0; x < w*4; x += 4 {
			d[x], d[x+1], d[x+2], d[x+3] = s[x+2], s[x+1], s[x], 255
		}
	}
	return img
}
