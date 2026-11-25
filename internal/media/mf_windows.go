//go:build windows

// Windows decodes and renders video natively via Media Foundation:
//   - Live / VOD streams use mygo/ui/video (IMFMediaEngine in D3D11 frame-server
//     mode with DXVA hardware decoding and zero-copy shared GPU textures),
//     paired with an in-memory loopback HTTP/HLS header proxy (proxy.go).
//   - Local File / VideoDecoder / AudioDecoder use Windows' built-in
//     IMFSourceReader (mfreadwrite.dll + mfplat.dll).
// Zero external FFmpeg executables or DLLs are required.
package media

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"
)

// NativeAvailable is false on Windows because ui.Video draws directly inside
// MyGo's D3D11 GPU compositor using shared DXGI textures (no separate OS
// overlay view like macOS's AVPlayerLayer).
const NativeAvailable = false

// ErrNoSystemPlayer reports a Windows without the Media Foundation that
// plays video: an N or KN edition without its Media Feature Pack.
var ErrNoSystemPlayer = video.ErrUnsupported

// PreferH264 prefers H.264 HLS live variants so playback works out of the box
// on every Windows 10/11 machine without optional Store codec extensions.
const PreferH264 = true

const sampleRate = 48000

// Windows playback streams directly through Media Foundation without prewarming.
type WarmVideo struct{}

func PreloadVideo(string, map[string]string) *WarmVideo { return nil }
func (*WarmVideo) Close()                               {}
func (*WarmVideo) Open() (*Live, error)                 { return nil, errors.New("media: video preload unavailable") }

// Live wraps a mygo/ui/video player (Media Foundation + D3D11 GPU textures)
// and owns any loopback proxy route created for custom HTTP headers / HLS.
type Live struct {
	mu          sync.Mutex
	vp          *ui.VideoPlayer
	release     func()
	vod         bool
	paused      bool
	closed      bool
	speed       float64
	volume      float64
	muted       bool
	pendingSeek time.Duration
	hasPending  bool
	seeking     bool
	seekTarget  time.Duration
	seekSince   time.Time
}

// The Open functions wait for a stream's first answer through the loopback
// proxy, as long as ctx lets them.
func OpenLiveHeaders(ctx context.Context, source string, headers map[string]string) (*Live, error) {
	return openStreamOptions(ctx, source, StreamOptions{Headers: headers}, false)
}
func OpenNative(ctx context.Context, source string, headers map[string]string, vod bool) (*Live, error) {
	return openStreamOptions(ctx, source, StreamOptions{Headers: headers}, vod)
}
func OpenVideo(ctx context.Context, source string, headers map[string]string) (*Live, error) {
	return openStreamOptions(ctx, source, StreamOptions{Headers: headers}, true)
}
func OpenVideoWithOptions(ctx context.Context, source string, opts StreamOptions) (*Live, error) {
	return openStreamOptions(ctx, source, opts, true)
}
func OpenNativeWithOptions(ctx context.Context, source string, opts StreamOptions, vod bool) (*Live, error) {
	return openStreamOptions(ctx, source, opts, vod)
}

func openStreamOptions(ctx context.Context, source string, opts StreamOptions, vod bool) (*Live, error) {
	if source == "" {
		return nil, errors.New("media: empty stream address")
	}
	if len(opts.DecryptKey) != 0 && len(opts.DecryptKey) != 16 {
		return nil, errors.New("media: decrypt key must be 16 bytes")
	}
	norm, isLocal, err := normalizeLocalSource(source)
	if err != nil {
		return nil, err
	}
	var (
		vp      *ui.VideoPlayer
		release func()
	)
	if isLocal {
		if len(opts.DecryptKey) != 0 {
			return nil, errors.New("media: encrypted local files are not supported")
		}
		if _, err := validateHTTPHeaders(opts.Headers); err != nil {
			return nil, err
		}
		vp, err = video.Open(norm)
		if err != nil {
			return nil, err
		}
	} else {
		proxyURL, rel, err := localProxy.registerOptions(ctx, norm, opts, vod)
		if err != nil {
			return nil, err
		}
		vp, err = video.OpenURL(proxyURL)
		if err != nil {
			rel()
			return nil, err
		}
		release = rel
	}
	vp.SetMuted(true)
	vp.Pause()
	return &Live{
		vp:      vp,
		release: release,
		vod:     vod,
		paused:  true,
		speed:   1,
		volume:  1,
		muted:   true,
	}, nil
}

// Player returns the underlying mygo/ui/video player so ui.Video presents the
// hardware-decoded D3D11 shared textures directly without CPU pixel copies.
func (l *Live) Player() *ui.VideoPlayer {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.vp
}

// Sync applies deferred seeks once Media Foundation has loaded metadata and
// settles seeking state as the playhead reaches its target.
func (l *Live) Sync() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return
	}
	if l.hasPending && (l.vp.Ready() || l.vp.Duration() > 0) {
		at := l.pendingSeek
		l.hasPending = false
		l.seeking = true
		l.seekTarget = at
		l.seekSince = time.Now()
		l.vp.Seek(at)
	}
	if l.seeking && !l.hasPending {
		pos := l.vp.Position()
		diff := pos - l.seekTarget
		if diff < 0 {
			diff = -diff
		}
		if diff < 1500*time.Millisecond || time.Since(l.seekSince) > 1500*time.Millisecond {
			l.seeking = false
		}
	}
}

func (l *Live) Position() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return 0
	}
	if l.hasPending {
		return l.pendingSeek
	}
	return l.vp.Position()
}

func (l *Live) Play() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return
	}
	l.paused = false
	l.vp.SetMuted(l.muted)
	l.vp.SetVolume(l.volume)
	l.vp.SetRate(l.speed)
	l.vp.Play()
}

func (l *Live) Pause() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return
	}
	l.paused = true
	l.vp.Pause()
}

func (l *Live) SetSpeed(speed float64) {
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return
	}
	l.speed = speed
	l.vp.SetRate(speed)
}

func (l *Live) SetMuted(muted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return
	}
	l.muted = muted
	l.vp.SetMuted(muted)
}

func (l *Live) SetVolume(volume float64) {
	if math.IsNaN(volume) || math.IsInf(volume, 0) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return
	}
	l.volume = max(0, min(1, volume))
	l.vp.SetVolume(l.volume)
}

func (l *Live) Seeking() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hasPending || l.seeking
}

func (l *Live) Seek(at time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return
	}
	at = max(0, at)
	if dur := l.vp.Duration(); dur > 0 {
		at = min(at, max(0, dur-time.Millisecond))
	}
	if !l.vp.Ready() && l.vp.Duration() == 0 {
		l.pendingSeek = at
		l.hasPending = true
		l.seeking = true
		l.seekTarget = at
		l.seekSince = time.Now()
		return
	}
	l.hasPending = false
	l.seeking = true
	l.seekTarget = at
	l.seekSince = time.Now()
	l.vp.Seek(at)
}

func (l *Live) Rewind() error {
	l.Seek(0)
	return nil
}

func (l *Live) Frame() (ui.VideoFrame, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return ui.VideoFrame{}, io.EOF
	}
	if err := l.vp.Err(); err != nil {
		return ui.VideoFrame{}, err
	}
	if l.vod && l.vp.Ended() { // a looping player never ends
		return ui.VideoFrame{}, io.EOF
	}
	return ui.VideoFrame{Duration: time.Second / 30}, nil
}

// SetLoop has the video play again from its start each time it ends.
func (l *Live) SetLoop(loop bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return
	}
	l.vp.SetLoop(loop)
}

func (l *Live) Next() (ui.VideoFrame, error) {
	return l.Frame()
}

func (l *Live) WaitReady() (int, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.vp == nil {
		return 0, 0, io.EOF
	}
	if err := l.vp.Err(); err != nil {
		return 0, 0, err
	}
	w, h := l.vp.Size()
	return w, h, nil
}

func (l *Live) UntilEnd() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.vod || l.closed || l.vp == nil {
		return time.Hour
	}
	dur := l.vp.Duration()
	if dur <= 0 {
		return time.Hour
	}
	return max(0, dur-l.vp.Position())
}

func (l *Live) UsePixels() error { return nil }

func (l *Live) Snapshot() (*image.RGBA, error) {
	return nil, errors.New("media: Windows video renders directly in the window")
}

func (l *Live) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	// Under mu: a cancel closes from a goroutine of its own, while the one
	// opening may still set the player up through l.
	if l.vp != nil {
		l.vp.Close()
		l.vp = nil
	}
	rel := l.release
	l.release = nil
	l.mu.Unlock()

	if rel != nil {
		rel()
	}
	return nil
}

// --- Windows Media Foundation IMFSourceReader for media.Open(path) ---

type mfGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// PROPVARIANT for VT_I8 (signed 64-bit integer) / VT_UI8 (unsigned 64-bit).
type propVariant struct {
	vt   uint16
	r1   uint16
	r2   uint16
	r3   uint16
	val  int64
	pad  int64
}

var (
	mfInitOnce sync.Once
	mfInitErr  error

	dllOle32       = syscall.NewLazyDLL("ole32.dll")
	dllMFPlat      = syscall.NewLazyDLL("mfplat.dll")
	dllMFReadWrite = syscall.NewLazyDLL("mfreadwrite.dll")

	procCoInitializeEx              = dllOle32.NewProc("CoInitializeEx")
	procPropVariantClear            = dllOle32.NewProc("PropVariantClear")
	procMFStartup                   = dllMFPlat.NewProc("MFStartup")
	procMFCreateAttributes          = dllMFPlat.NewProc("MFCreateAttributes")
	procMFCreateMediaType           = dllMFPlat.NewProc("MFCreateMediaType")
	procMFCreateSourceReaderFromURL = dllMFReadWrite.NewProc("MFCreateSourceReaderFromURL")

	guidNull = mfGUID{}

	// Verified against Windows SDK mfapi.h / mfidl.h / mfreadwrite.h.
	mfMTMajorType          = mfGUID{0x48eba18e, 0xf8c9, 0x4687, [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f}}
	mfMTSubtype            = mfGUID{0xf7e34c9a, 0x42e8, 0x4714, [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5}}
	mfMTFrameSize          = mfGUID{0x1652c33d, 0xd6b2, 0x4012, [8]byte{0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d}}
	// MF_MT_MINIMUM_DISPLAY_APERTURE: the picture without the rows a decoder
	// pads it with to whole macroblocks (an MFVideoArea).
	mfMTMinimumDisplayAperture = mfGUID{0xd7388766, 0x18fe, 0x48c6, [8]byte{0xa1, 0x77, 0xee, 0x89, 0x48, 0x67, 0xc8, 0xc4}}
	mfMTFrameRate          = mfGUID{0xc459a2e8, 0x3d2c, 0x4e44, [8]byte{0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0}}
	mfMTDefaultStride      = mfGUID{0x644b4e48, 0x1e02, 0x4516, [8]byte{0xb0, 0xeb, 0xc0, 0x1c, 0xa9, 0xd4, 0x9a, 0xc6}}
	mfMTAudioSamplesPerSec = mfGUID{0x5faeeae7, 0x0290, 0x4c31, [8]byte{0x9e, 0x8a, 0xc5, 0x34, 0xf6, 0x8d, 0x9d, 0xba}}
	mfMTAudioNumChannels   = mfGUID{0x37e48bf5, 0x645e, 0x4c5b, [8]byte{0x89, 0xde, 0xad, 0xa9, 0xe2, 0x9b, 0x69, 0x6a}}
	mfMTAudioBitsPerSample = mfGUID{0xf2deb57f, 0x40fa, 0x4764, [8]byte{0xaa, 0x33, 0xed, 0x4f, 0x2d, 0x1f, 0xf6, 0x69}}
	mfPDDuration           = mfGUID{0x6c990d33, 0xbb8e, 0x477a, [8]byte{0x85, 0x98, 0x0d, 0x5d, 0x96, 0xfc, 0xd8, 0x8a}}

	mfSourceReaderEnableVideoProcessing = mfGUID{0xfb394f3d, 0xccf1, 0x42ee, [8]byte{0xbb, 0xb3, 0xf9, 0xb8, 0x45, 0xd5, 0x68, 0x1d}}

	mfMediaTypeVideo   = mfGUID{0x73646976, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfMediaTypeAudio   = mfGUID{0x73647561, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfVideoFormatRGB32 = mfGUID{0x00000016, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfAudioFormatFloat = mfGUID{0x00000003, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
)

const (
	// COM vtable slots
	vtRelease = 2

	// IMFAttributes (inherits IUnknown: 0..2)
	vtAttrGetUINT32 = 7
	vtAttrGetUINT64 = 8
	vtAttrGetBlob   = 15
	vtAttrSetUINT32 = 21
	vtAttrSetGUID   = 24

	// IMFSourceReader (inherits IUnknown: 0..2)
	vtSRSetStreamSelection         = 4
	vtSRGetNativeMediaType         = 5
	vtSRGetCurrentMediaType        = 6
	vtSRSetCurrentMediaType        = 7
	vtSRSetCurrentPosition         = 8
	vtSRReadSample                 = 9
	vtSRGetPresentationAttribute   = 12

	// IMFSample (inherits IMFAttributes: 0..32)
	vtSampleGetDuration            = 37
	vtSampleConvertToContiguousBuf = 41

	// IMFMediaBuffer (inherits IUnknown: 0..2)
	vtBufLock   = 3
	vtBufUnlock = 4

	// IMFSourceReader stream indices and flags
	mfFirstVideoStream = 0xFFFFFFFC
	mfFirstAudioStream = 0xFFFFFFFD
	mfAllStreams       = 0xFFFFFFFE
	mfMediaSource      = 0xFFFFFFFF

	mfStreamFlagError              = 0x00000001
	mfStreamFlagEOS                = 0x00000002
	mfStreamFlagNativeTypeChanged  = 0x00000010
	mfStreamFlagCurrentTypeChanged = 0x00000020

	vtI8  = 20
	vtUI8 = 21
)

func mfCPtr(addr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr))
}

func mfFailed(hr uintptr) bool {
	return int32(uint32(hr)) < 0
}

//go:uintptrescapes
func mfCall(obj uintptr, slot int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(mfCPtr(obj))
	fn := *(*uintptr)(unsafe.Add(mfCPtr(vtbl), uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	var a [12]uintptr
	a[0] = obj
	n := copy(a[1:], args)
	r, _, _ := syscall.SyscallN(fn, a[:n+1]...)
	return r
}

func mfRelease(p *uintptr) {
	if *p != 0 {
		mfCall(*p, vtRelease)
		*p = 0
	}
}

func ensureMF() error {
	mfInitOnce.Do(func() {
		for _, p := range []*syscall.LazyProc{
			procCoInitializeEx, procPropVariantClear,
			procMFStartup, procMFCreateAttributes, procMFCreateMediaType,
			procMFCreateSourceReaderFromURL,
		} {
			if err := p.Find(); err != nil {
				mfInitErr = fmt.Errorf("media: Media Foundation unavailable: %w", err)
				return
			}
		}
		const mfVersion, mfStartupFull = 0x00020070, 0
		if hr, _, _ := procMFStartup.Call(mfVersion, mfStartupFull); mfFailed(hr) {
			mfInitErr = fmt.Errorf("media: MFStartup failed (%#x)", uint32(hr))
		}
	})
	return mfInitErr
}

func createSourceReader(path string, enableVideoProc bool) (uintptr, error) {
	if err := ensureMF(); err != nil {
		return 0, err
	}
	procCoInitializeEx.Call(0, 0) // COINIT_MULTITHREADED (S_FALSE / RPC_E_CHANGED_MODE are fine)
	var attrs uintptr
	if enableVideoProc {
		if hr, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&attrs)), 1); !mfFailed(hr) {
			mfCall(attrs, vtAttrSetUINT32, uintptr(unsafe.Pointer(&mfSourceReaderEnableVideoProcessing)), 1)
			defer mfRelease(&attrs)
		}
	}
	u16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var reader uintptr
	hr, _, _ := procMFCreateSourceReaderFromURL.Call(
		uintptr(unsafe.Pointer(u16)),
		attrs,
		uintptr(unsafe.Pointer(&reader)),
	)
	runtime.KeepAlive(u16)
	if mfFailed(hr) || reader == 0 {
		return 0, fmt.Errorf("media: cannot open %s (%#x)", path, uint32(hr))
	}
	return reader, nil
}

type File struct {
	W, H     int
	Duration time.Duration
	fps      float64
	source   string
	hasAudio bool
}

func Open(path string) (*File, error) {
	norm, _, err := normalizeLocalSource(path)
	if err != nil {
		return nil, err
	}
	reader, err := createSourceReader(norm, false)
	if err != nil {
		return nil, err
	}
	defer mfRelease(&reader)

	f := &File{source: norm, fps: 30}

	var vmt uintptr
	if hr := mfCall(reader, vtSRGetNativeMediaType, mfFirstVideoStream, 0, uintptr(unsafe.Pointer(&vmt))); !mfFailed(hr) && vmt != 0 {
		var sz uint64
		if !mfFailed(mfCall(vmt, vtAttrGetUINT64, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(unsafe.Pointer(&sz)))) {
			f.W = int(sz >> 32)
			f.H = int(uint32(sz))
		}
		var rate uint64
		if !mfFailed(mfCall(vmt, vtAttrGetUINT64, uintptr(unsafe.Pointer(&mfMTFrameRate)), uintptr(unsafe.Pointer(&rate)))) {
			num, den := float64(rate>>32), float64(uint32(rate))
			if den > 0 && num/den >= 1 {
				f.fps = min(num/den, 60)
			}
		}
		mfRelease(&vmt)
	}
	if f.W <= 0 || f.H <= 0 || f.W > 8192 || f.H > 8192 || int64(f.W)*int64(f.H) > 33554432 {
		return nil, errors.New("media: missing or oversized video track")
	}

	var amt uintptr
	if hr := mfCall(reader, vtSRGetNativeMediaType, mfFirstAudioStream, 0, uintptr(unsafe.Pointer(&amt))); !mfFailed(hr) && amt != 0 {
		f.hasAudio = true
		mfRelease(&amt)
	}

	var pv propVariant
	if hr := mfCall(reader, vtSRGetPresentationAttribute, mfMediaSource, uintptr(unsafe.Pointer(&mfPDDuration)), uintptr(unsafe.Pointer(&pv))); !mfFailed(hr) {
		if pv.vt == vtUI8 || pv.vt == vtI8 {
			if pv.val > 0 {
				f.Duration = time.Duration(pv.val) * 100 // 100ns units
			}
		}
		procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	}
	return f, nil
}

func (f *File) Close() {}

type VideoDecoder struct {
	mu      sync.Mutex
	f       *File
	reader  uintptr
	w, h    int   // the size of the pictures the reader puts out now
	rows    int   // the rows of each in its buffer, the decoder's padding with them
	stride  int32
	at      time.Duration
	closed  bool
}

func (f *File) Video() (*VideoDecoder, error) {
	return &VideoDecoder{f: f}, nil
}

func (v *VideoDecoder) openLocked() error {
	if v.reader != 0 {
		return nil
	}
	reader, err := createSourceReader(v.f.source, true)
	if err != nil {
		return err
	}
	mfCall(reader, vtSRSetStreamSelection, mfAllStreams, 0)
	mfCall(reader, vtSRSetStreamSelection, mfFirstVideoStream, 1)

	var mt uintptr
	if hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&mt))); mfFailed(hr) {
		mfRelease(&reader)
		return fmt.Errorf("media: MFCreateMediaType failed (%#x)", uint32(hr))
	}
	mfCall(mt, vtAttrSetGUID, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeVideo)))
	mfCall(mt, vtAttrSetGUID, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&mfVideoFormatRGB32)))
	hr := mfCall(reader, vtSRSetCurrentMediaType, mfFirstVideoStream, 0, mt)
	mfRelease(&mt)
	if mfFailed(hr) {
		mfRelease(&reader)
		return fmt.Errorf("media: set RGB32 output failed (%#x)", uint32(hr))
	}

	v.reader = reader
	v.refreshFormatLocked()
	if v.at > 0 {
		_ = v.seekLocked(v.at)
	}
	return nil
}

// refreshFormatLocked reads the size and stride of the pictures the reader
// puts out, which change with the stream's resolution.
func (v *VideoDecoder) refreshFormatLocked() {
	v.w, v.h, v.rows, v.stride = v.f.W, v.f.H, v.f.H, int32(v.f.W*4)
	var cur uintptr
	if mfFailed(mfCall(v.reader, vtSRGetCurrentMediaType, mfFirstVideoStream, uintptr(unsafe.Pointer(&cur)))) || cur == 0 {
		return
	}
	defer mfRelease(&cur)
	var sz uint64
	if !mfFailed(mfCall(cur, vtAttrGetUINT64, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(unsafe.Pointer(&sz)))) {
		if w, h := int(sz>>32), int(uint32(sz)); w > 0 && h > 0 && w <= 8192 && h <= 8192 {
			v.w, v.h, v.rows, v.stride = w, h, h, int32(w*4)
		}
	}
	// The frame size counts the padding: 640x368 for a 640x360 video. Its
	// offset is 0 in what Media Foundation decodes.
	var area struct {
		offX, offY [2]uint16 // MFOffset: fract, value
		cx, cy     int32
	}
	var n uint32
	if !mfFailed(mfCall(cur, vtAttrGetBlob, uintptr(unsafe.Pointer(&mfMTMinimumDisplayAperture)), uintptr(unsafe.Pointer(&area)), unsafe.Sizeof(area), uintptr(unsafe.Pointer(&n)))) &&
		area.cx > 0 && area.cy > 0 && int(area.cx) <= v.w && int(area.cy) <= v.h {
		v.w, v.h = int(area.cx), int(area.cy)
	}
	var s uint32
	if !mfFailed(mfCall(cur, vtAttrGetUINT32, uintptr(unsafe.Pointer(&mfMTDefaultStride)), uintptr(unsafe.Pointer(&s)))) && s != 0 {
		v.stride = int32(s)
	}
}

func (v *VideoDecoder) seekLocked(at time.Duration) error {
	pv := propVariant{vt: vtI8, val: max(0, at.Nanoseconds()/100)}
	hr := mfCall(v.reader, vtSRSetCurrentPosition, uintptr(unsafe.Pointer(&guidNull)), uintptr(unsafe.Pointer(&pv)))
	if mfFailed(hr) {
		return fmt.Errorf("media: seek failed (%#x)", uint32(hr))
	}
	return nil
}

func (v *VideoDecoder) Rewind() error { return v.RewindTo(0) }

func (v *VideoDecoder) RewindTo(at time.Duration) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return io.ErrClosedPipe
	}
	v.at = max(0, at)
	if v.reader != 0 {
		return v.seekLocked(v.at)
	}
	return nil
}

func (v *VideoDecoder) Next() (ui.VideoFrame, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ui.VideoFrame{}, io.EOF
	}
	if err := v.openLocked(); err != nil {
		return ui.VideoFrame{}, err
	}
	for {
		var streamIdx, flags uint32
		var ts int64
		var sample uintptr
		hr := mfCall(v.reader, vtSRReadSample, mfFirstVideoStream, 0,
			uintptr(unsafe.Pointer(&streamIdx)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&ts)),
			uintptr(unsafe.Pointer(&sample)))
		if mfFailed(hr) {
			return ui.VideoFrame{}, fmt.Errorf("media: ReadSample failed (%#x)", uint32(hr))
		}
		if flags&mfStreamFlagError != 0 { // the reader stops: no sample comes again
			mfRelease(&sample)
			return ui.VideoFrame{}, errors.New("media: the video stream failed to decode")
		}
		if flags&(mfStreamFlagNativeTypeChanged|mfStreamFlagCurrentTypeChanged) != 0 {
			v.refreshFormatLocked()
		}
		if flags&mfStreamFlagEOS != 0 {
			mfRelease(&sample)
			return ui.VideoFrame{}, io.EOF
		}
		if sample == 0 {
			continue
		}

		var dur100 int64
		_ = mfCall(sample, vtSampleGetDuration, uintptr(unsafe.Pointer(&dur100)))
		dur := time.Duration(dur100) * 100
		if dur <= 0 {
			dur = time.Duration(float64(time.Second) / v.f.fps)
		}

		var buf uintptr
		hr = mfCall(sample, vtSampleConvertToContiguousBuf, uintptr(unsafe.Pointer(&buf)))
		mfRelease(&sample)
		if mfFailed(hr) || buf == 0 {
			continue
		}

		var dataPtr uintptr
		var maxLen, curLen uint32
		hr = mfCall(buf, vtBufLock, uintptr(unsafe.Pointer(&dataPtr)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&curLen)))
		if mfFailed(hr) || dataPtr == 0 {
			mfRelease(&buf)
			continue
		}

		w, h := v.w, v.h
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		stride := int(v.stride)
		absStride := stride
		if absStride < 0 {
			absStride = -absStride
		}
		if absStride < w*4 {
			absStride = w * 4
		}
		srcBytes := unsafe.Slice((*byte)(mfCPtr(dataPtr)), int(curLen))
		for y := 0; y < h; y++ {
			srcY := y
			if stride < 0 { // bottom-up: the buffer's first row is the padded picture's last
				srcY = v.rows - 1 - y
			}
			rowStart := srcY * absStride
			if rowStart+w*4 > len(srcBytes) {
				break
			}
			srcRow := srcBytes[rowStart : rowStart+w*4]
			dstRow := img.Pix[y*img.Stride : y*img.Stride+w*4]
			for x := 0; x < w*4; x += 4 {
				// RGB32 in memory is B, G, R, X.
				dstRow[x] = srcRow[x+2]
				dstRow[x+1] = srcRow[x+1]
				dstRow[x+2] = srcRow[x]
				dstRow[x+3] = 255
			}
		}
		mfCall(buf, vtBufUnlock)
		mfRelease(&buf)
		return ui.VideoFrame{Image: img, Duration: dur}, nil
	}
}

func (v *VideoDecoder) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	mfRelease(&v.reader)
	return nil
}

type AudioDecoder struct {
	mu      sync.Mutex
	f       *File
	reader  uintptr
	at      time.Duration
	pending []float32
	closed  bool
}

func (f *File) Audio() (*AudioDecoder, error) {
	if !f.hasAudio {
		return nil, errors.New("media: no audio track")
	}
	return &AudioDecoder{f: f}, nil
}

func (a *AudioDecoder) SampleRate() int { return sampleRate }
func (a *AudioDecoder) Channels() int   { return 2 }
func (a *AudioDecoder) Len() int64 {
	if a.f.Duration <= 0 {
		return -1
	}
	return int64(a.f.Duration.Seconds() * sampleRate)
}

func (a *AudioDecoder) openLocked() error {
	if a.reader != 0 {
		return nil
	}
	reader, err := createSourceReader(a.f.source, false)
	if err != nil {
		return err
	}
	mfCall(reader, vtSRSetStreamSelection, mfAllStreams, 0)
	mfCall(reader, vtSRSetStreamSelection, mfFirstAudioStream, 1)

	var mt uintptr
	if hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&mt))); mfFailed(hr) {
		mfRelease(&reader)
		return fmt.Errorf("media: MFCreateMediaType failed (%#x)", uint32(hr))
	}
	mfCall(mt, vtAttrSetGUID, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeAudio)))
	mfCall(mt, vtAttrSetGUID, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&mfAudioFormatFloat)))
	mfCall(mt, vtAttrSetUINT32, uintptr(unsafe.Pointer(&mfMTAudioNumChannels)), 2)
	mfCall(mt, vtAttrSetUINT32, uintptr(unsafe.Pointer(&mfMTAudioSamplesPerSec)), sampleRate)
	mfCall(mt, vtAttrSetUINT32, uintptr(unsafe.Pointer(&mfMTAudioBitsPerSample)), 32)
	hr := mfCall(reader, vtSRSetCurrentMediaType, mfFirstAudioStream, 0, mt)
	mfRelease(&mt)
	if mfFailed(hr) {
		mfRelease(&reader)
		return fmt.Errorf("media: set float32 audio output failed (%#x)", uint32(hr))
	}

	a.reader = reader
	if a.at > 0 {
		_ = a.seekLocked(a.at)
	}
	return nil
}

func (a *AudioDecoder) seekLocked(at time.Duration) error {
	a.pending = a.pending[:0]
	pv := propVariant{vt: vtI8, val: max(0, at.Nanoseconds()/100)}
	hr := mfCall(a.reader, vtSRSetCurrentPosition, uintptr(unsafe.Pointer(&guidNull)), uintptr(unsafe.Pointer(&pv)))
	if mfFailed(hr) {
		return fmt.Errorf("media: audio seek failed (%#x)", uint32(hr))
	}
	return nil
}

func (a *AudioDecoder) SetPosition(frame int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return io.ErrClosedPipe
	}
	a.at = time.Duration(float64(max(0, frame)) / sampleRate * float64(time.Second))
	if a.reader != 0 {
		return a.seekLocked(a.at)
	}
	return nil
}

func (a *AudioDecoder) Read(samples []float32) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0, io.EOF
	}
	samples = samples[:len(samples)&^1]
	if len(samples) == 0 {
		return 0, nil
	}
	if len(a.pending) > 0 {
		n := copy(samples, a.pending)
		a.pending = a.pending[n:]
		return n, nil
	}
	if err := a.openLocked(); err != nil {
		return 0, err
	}
	for {
		var streamIdx, flags uint32
		var ts int64
		var sample uintptr
		hr := mfCall(a.reader, vtSRReadSample, mfFirstAudioStream, 0,
			uintptr(unsafe.Pointer(&streamIdx)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&ts)),
			uintptr(unsafe.Pointer(&sample)))
		if mfFailed(hr) {
			return 0, fmt.Errorf("media: audio ReadSample failed (%#x)", uint32(hr))
		}
		if flags&mfStreamFlagError != 0 {
			mfRelease(&sample)
			return 0, errors.New("media: the audio stream failed to decode")
		}
		if flags&mfStreamFlagEOS != 0 {
			mfRelease(&sample)
			return 0, io.EOF
		}
		if sample == 0 {
			continue
		}
		var buf uintptr
		hr = mfCall(sample, vtSampleConvertToContiguousBuf, uintptr(unsafe.Pointer(&buf)))
		mfRelease(&sample)
		if mfFailed(hr) || buf == 0 {
			continue
		}
		var dataPtr uintptr
		var maxLen, curLen uint32
		hr = mfCall(buf, vtBufLock, uintptr(unsafe.Pointer(&dataPtr)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&curLen)))
		if mfFailed(hr) || dataPtr == 0 {
			mfRelease(&buf)
			continue
		}
		raw := unsafe.Slice((*byte)(mfCPtr(dataPtr)), int(curLen))
		total := (len(raw) / 4) &^ 1
		if total == 0 {
			mfCall(buf, vtBufUnlock)
			mfRelease(&buf)
			continue
		}
		n := min(total, len(samples))
		for i := 0; i < n; i++ {
			samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		}
		if total > n {
			rem := total - n
			if cap(a.pending) < rem {
				a.pending = make([]float32, rem)
			} else {
				a.pending = a.pending[:rem]
			}
			for i := 0; i < rem; i++ {
				a.pending[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[(n+i)*4:]))
			}
		}
		mfCall(buf, vtBufUnlock)
		mfRelease(&buf)
		return n, nil
	}
}

func (a *AudioDecoder) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	a.pending = nil
	mfRelease(&a.reader)
	return nil
}
