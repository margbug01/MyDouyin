// Package media decodes a local mp4 (H.264 + AAC) into frames and samples for
// MyGo's ui.VideoPlayer and audio.Player. On macOS it asks AVFoundation to
// demux and decode, so there is no mp4 parser or codec of our own to keep.
package media

import (
	"errors"
	"fmt"
	"image"
	"io"
	"runtime"
	"structs"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo/ui"
)

const NativeAvailable = true

type cmTime struct {
	_         structs.HostLayout
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

type cmTimeRange struct {
	_               structs.HostLayout
	Start, Duration cmTime
}

func (t cmTime) valid() bool { return t.Flags&1 != 0 && t.Timescale != 0 }
func (t cmTime) dur() time.Duration {
	if !t.valid() {
		return 0
	}
	scale := int64(t.Timescale)
	return time.Duration(t.Value/scale)*time.Second + time.Duration(t.Value%scale)*time.Second/time.Duration(scale)
}

var (
	cmSampleBufferGetDuration    func(sb uintptr) cmTime
	cmSampleBufferGetImageBuffer func(sb uintptr) uintptr
	cmSampleBufferGetDataBuffer  func(sb uintptr) uintptr
	cmBlockBufferGetDataLength   func(bb uintptr) uintptr
	cmBlockBufferCopyDataBytes   func(bb, off, n uintptr, dst unsafe.Pointer) int32
	cvPixelBufferLock            func(pb uintptr, flags uint64) int32
	cvPixelBufferUnlock          func(pb uintptr, flags uint64) int32
	cvPixelBufferBase            func(pb uintptr) unsafe.Pointer
	cvPixelBufferStride          func(pb uintptr) uintptr
	cvPixelBufferWidth           func(pb uintptr) uintptr
	cvPixelBufferHeight          func(pb uintptr) uintptr
	cfRelease                    func(p uintptr)

	clsString, clsURL, clsNumber, clsDict, clsAsset, clsReader, clsOutput, clsValue objc.Class
)

func sel(s string) objc.SEL { return objc.RegisterName(s) }

func init() {
	for _, p := range []string{"Foundation", "AVFoundation", "CoreMedia", "CoreVideo"} {
		h, err := purego.Dlopen("/System/Library/Frameworks/"+p+".framework/"+p, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		switch p {
		case "CoreMedia":
			purego.RegisterLibFunc(&cmSampleBufferGetDuration, h, "CMSampleBufferGetDuration")
			purego.RegisterLibFunc(&cmSampleBufferGetImageBuffer, h, "CMSampleBufferGetImageBuffer")
			purego.RegisterLibFunc(&cmSampleBufferGetDataBuffer, h, "CMSampleBufferGetDataBuffer")
			purego.RegisterLibFunc(&cmBlockBufferGetDataLength, h, "CMBlockBufferGetDataLength")
			purego.RegisterLibFunc(&cmBlockBufferCopyDataBytes, h, "CMBlockBufferCopyDataBytes")
			purego.RegisterLibFunc(&cfRelease, h, "CFRelease")
		case "CoreVideo":
			purego.RegisterLibFunc(&cvPixelBufferLock, h, "CVPixelBufferLockBaseAddress")
			purego.RegisterLibFunc(&cvPixelBufferUnlock, h, "CVPixelBufferUnlockBaseAddress")
			purego.RegisterLibFunc(&cvPixelBufferBase, h, "CVPixelBufferGetBaseAddress")
			purego.RegisterLibFunc(&cvPixelBufferStride, h, "CVPixelBufferGetBytesPerRow")
			purego.RegisterLibFunc(&cvPixelBufferWidth, h, "CVPixelBufferGetWidth")
			purego.RegisterLibFunc(&cvPixelBufferHeight, h, "CVPixelBufferGetHeight")
		}
	}
	clsString, clsURL, clsNumber = objc.GetClass("NSString"), objc.GetClass("NSURL"), objc.GetClass("NSNumber")
	clsDict, clsAsset = objc.GetClass("NSMutableDictionary"), objc.GetClass("AVURLAsset")
	clsReader, clsOutput = objc.GetClass("AVAssetReader"), objc.GetClass("AVAssetReaderTrackOutput")
	clsValue = objc.GetClass("NSValue")
}

func nsString(s string) objc.ID {
	b := append([]byte(s), 0)
	return objc.ID(clsString).Send(sel("stringWithUTF8String:"), unsafe.Pointer(&b[0]))
}

func nsInt(n int) objc.ID   { return objc.ID(clsNumber).Send(sel("numberWithLong:"), n) }
func nsBool(b bool) objc.ID { return objc.ID(clsNumber).Send(sel("numberWithBool:"), b) }

func nsDict(kv ...any) objc.ID {
	d := objc.ID(clsDict).Send(sel("dictionary"))
	for i := 0; i < len(kv); i += 2 {
		d.Send(sel("setObject:forKey:"), kv[i+1].(objc.ID), nsString(kv[i].(string)))
	}
	return d
}

// File is an opened mp4. Decoders made from it are independent readers.
type File struct {
	asset          objc.ID
	vtrack, atrack objc.ID
	W, H           int
	Duration       time.Duration
	frameDur       time.Duration
}

// Cocoa autoreleased objects belong to an OS thread, not a Go goroutine.
func autorelease() func() {
	runtime.LockOSThread()
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(sel("alloc")).Send(sel("init"))
	return func() { pool.Send(sel("drain")); runtime.UnlockOSThread() }
}

func Open(path string) (*File, error) {
	defer autorelease()()
	url := objc.ID(clsURL).Send(sel("fileURLWithPath:"), nsString(path))
	asset := objc.ID(clsAsset).Send(sel("URLAssetWithURL:options:"), url, objc.ID(0))
	if asset == 0 {
		return nil, errors.New("media: cannot open " + path)
	}
	f := &File{asset: asset, frameDur: time.Second / 30}
	f.asset.Send(sel("retain"))
	track := func(kind string) objc.ID {
		a := asset.Send(sel("tracksWithMediaType:"), nsString(kind))
		if objc.Send[uint](a, sel("count")) == 0 {
			return 0
		}
		return a.Send(sel("objectAtIndex:"), 0)
	}
	f.vtrack, f.atrack = track("vide"), track("soun")
	if f.vtrack == 0 {
		f.Close()
		return nil, errors.New("media: no video track")
	}
	f.Duration = objc.Send[cmTime](asset, sel("duration")).dur()
	sz := objc.Send[struct {
		_    structs.HostLayout
		W, H float64
	}](f.vtrack, sel("naturalSize"))
	f.W, f.H = int(sz.W), int(sz.H)
	if fps := objc.Send[float32](f.vtrack, sel("nominalFrameRate")); fps > 1 {
		f.frameDur = time.Duration(float64(time.Second) / float64(fps))
	}
	return f, nil
}

// reader starts an AVAssetReader on one track, from start (0 for the beginning).
func (f *File) reader(track objc.ID, settings objc.ID, start time.Duration) (rd, out objc.ID, err error) {
	var nserr objc.ID
	rd = objc.ID(clsReader).Send(sel("assetReaderWithAsset:error:"), f.asset, unsafe.Pointer(&nserr))
	if rd == 0 {
		return 0, 0, errors.New("media: cannot create reader")
	}
	out = objc.ID(clsOutput).Send(sel("assetReaderTrackOutputWithTrack:outputSettings:"), track, settings)
	rd.Send(sel("addOutput:"), out)
	if start > 0 {
		const timescale = 48000
		tr := cmTimeRange{
			Start:    cmTime{Value: int64(start.Seconds() * timescale), Timescale: timescale, Flags: 1},
			Duration: cmTime{Flags: 5}, // positive infinity
		}
		rd.Send(sel("setTimeRange:"), tr)
	}
	rd.Send(sel("retain"))
	if !objc.Send[bool](rd, sel("startReading")) {
		rd.Send(sel("release"))
		return 0, 0, errors.New("media: startReading failed")
	}
	return rd, out, nil
}

// Close releases the asset, once its decoders are closed.
func (f *File) Close() { f.asset.Send(sel("release")) }

// VideoDecoder implements ui.VideoDecoder, with frames as RGBA.
type VideoDecoder struct {
	frame   *image.RGBA
	f       *File
	rd, out objc.ID
}

func (f *File) Video() (*VideoDecoder, error) {
	v := &VideoDecoder{f: f}
	return v, v.Rewind()
}

func (v *VideoDecoder) Rewind() error { return v.RewindTo(0) }

// RewindTo goes back, or on, to just before the frame at t.
func (v *VideoDecoder) RewindTo(t time.Duration) error {
	defer autorelease()()
	v.close()
	// 'BGRA' pixel format
	rd, out, err := v.f.reader(v.f.vtrack, nsDict("PixelFormatType", nsInt(0x42475241)), t)
	v.rd, v.out = rd, out
	return err
}

func (v *VideoDecoder) Next() (ui.VideoFrame, error) {
	defer autorelease()()
	sb := uintptr(v.out.Send(sel("copyNextSampleBuffer")))
	if sb == 0 {
		if st := objc.Send[int](v.rd, sel("status")); st == 3 { // AVAssetReaderStatusFailed
			return ui.VideoFrame{}, errors.New("media: video read failed")
		}
		return ui.VideoFrame{}, io.EOF
	}
	defer cfRelease(sb)
	pb := cmSampleBufferGetImageBuffer(sb)
	if pb == 0 {
		return ui.VideoFrame{}, fmt.Errorf("media: sample without image")
	}
	v.frame = bgra(pb, v.frame)
	img := v.frame
	dur := v.f.frameDur
	if t := cmSampleBufferGetDuration(sb); t.valid() && t.dur() > 0 {
		dur = t.dur()
	}
	return ui.VideoFrame{Image: img, Duration: dur}, nil
}

func (v *VideoDecoder) close() {
	if v.rd != 0 {
		v.rd.Send(sel("cancelReading"))
		v.rd.Send(sel("release"))
		v.rd, v.out = 0, 0
	}
}

func (v *VideoDecoder) Close() error { defer autorelease()(); v.close(); return nil }

// AudioDecoder implements audio.Decoder: float32, 48 kHz, stereo, which is
// what MyGo's mixer plays, so AVFoundation does the resampling.
type AudioDecoder struct {
	f       *File
	rd, out objc.ID
	pend    []float32
}

const rate = 48000

func (f *File) Audio() (*AudioDecoder, error) {
	if f.atrack == 0 {
		return nil, errors.New("media: no audio track")
	}
	a := &AudioDecoder{f: f}
	return a, a.SetPosition(0)
}

func (a *AudioDecoder) SampleRate() int { return rate }
func (a *AudioDecoder) Channels() int   { return 2 }
func (a *AudioDecoder) Len() int64      { return int64(a.f.Duration.Seconds() * rate) }

func (a *AudioDecoder) SetPosition(frame int64) error {
	defer autorelease()()
	a.close()
	a.pend = nil
	settings := nsDict(
		"AVFormatIDKey", nsInt(0x6C70636D), // 'lpcm'
		"AVLinearPCMBitDepthKey", nsInt(32),
		"AVLinearPCMIsFloatKey", nsBool(true),
		"AVLinearPCMIsBigEndianKey", nsBool(false),
		"AVLinearPCMIsNonInterleaved", nsBool(false),
		"AVSampleRateKey", nsInt(rate),
		"AVNumberOfChannelsKey", nsInt(2),
	)
	rd, out, err := a.f.reader(a.f.atrack, settings, time.Duration(frame)*time.Second/rate)
	a.rd, a.out = rd, out
	return err
}

func (a *AudioDecoder) Read(buf []float32) (int, error) {
	defer autorelease()()
	n := 0
	for n < len(buf) {
		if len(a.pend) == 0 {
			sb := uintptr(a.out.Send(sel("copyNextSampleBuffer")))
			if sb == 0 {
				if objc.Send[int](a.rd, sel("status")) == 3 {
					return n &^ 1, errors.New("media: audio read failed")
				}
				if n > 0 {
					return n &^ 1, nil
				}
				return 0, io.EOF
			}
			bb := cmSampleBufferGetDataBuffer(sb)
			a.pend = make([]float32, cmBlockBufferGetDataLength(bb)/4)
			if len(a.pend) > 0 {
				cmBlockBufferCopyDataBytes(bb, 0, uintptr(len(a.pend)*4), unsafe.Pointer(&a.pend[0]))
			}
			cfRelease(sb)
		}
		c := copy(buf[n:], a.pend)
		a.pend, n = a.pend[c:], n+c
	}
	return n, nil
}

func (a *AudioDecoder) close() {
	if a.rd != 0 {
		a.rd.Send(sel("cancelReading"))
		a.rd.Send(sel("release"))
		a.rd, a.out = 0, 0
	}
}

func (a *AudioDecoder) Close() error { defer autorelease()(); a.close(); return nil }
