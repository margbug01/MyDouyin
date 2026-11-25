//go:build darwin

package app

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"structs"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

type cgPoint struct {
	_    structs.HostLayout
	X, Y float64
}

type cgSize struct {
	_    structs.HostLayout
	W, H float64
}

type cgRect struct {
	_      structs.HostLayout
	Origin cgPoint
	Size   cgSize
}

var (
	imageIOOnce sync.Once
	imageIOErr  error

	cfDataCreate              func(allocator uintptr, bytes *byte, length int) uintptr
	cfReleaseImg              func(cf uintptr)
	cgImageSourceCreateData   func(data uintptr, options uintptr) uintptr
	cgImageSourceCreateImage  func(src uintptr, index uintptr, options uintptr) uintptr
	cgImageGetWidth           func(img uintptr) uintptr
	cgImageGetHeight          func(img uintptr) uintptr
	cgImageRelease            func(img uintptr)
	cgColorSpaceCreateRGB     func() uintptr
	cgColorSpaceRelease       func(cs uintptr)
	cgBitmapContextCreate     func(data unsafe.Pointer, w, h, bpc, bpr uintptr, cs uintptr, bitmapInfo uint32) uintptr
	cgContextSetInterpolation func(ctx uintptr, quality int32)
	cgContextDrawImage        func(ctx uintptr, rect cgRect, img uintptr)
	cgContextRelease          func(ctx uintptr)
)

func initImageIO() error {
	imageIOOnce.Do(func() {
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			imageIOErr = err
			return
		}
		iio, err := purego.Dlopen("/System/Library/Frameworks/ImageIO.framework/ImageIO", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			imageIOErr = err
			return
		}
		cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			imageIOErr = err
			return
		}
		purego.RegisterLibFunc(&cfDataCreate, cf, "CFDataCreate")
		purego.RegisterLibFunc(&cfReleaseImg, cf, "CFRelease")
		purego.RegisterLibFunc(&cgImageSourceCreateData, iio, "CGImageSourceCreateWithData")
		purego.RegisterLibFunc(&cgImageSourceCreateImage, iio, "CGImageSourceCreateImageAtIndex")
		purego.RegisterLibFunc(&cgImageGetWidth, cg, "CGImageGetWidth")
		purego.RegisterLibFunc(&cgImageGetHeight, cg, "CGImageGetHeight")
		purego.RegisterLibFunc(&cgImageRelease, cg, "CGImageRelease")
		purego.RegisterLibFunc(&cgColorSpaceCreateRGB, cg, "CGColorSpaceCreateDeviceRGB")
		purego.RegisterLibFunc(&cgColorSpaceRelease, cg, "CGColorSpaceRelease")
		purego.RegisterLibFunc(&cgBitmapContextCreate, cg, "CGBitmapContextCreate")
		purego.RegisterLibFunc(&cgContextSetInterpolation, cg, "CGContextSetInterpolationQuality")
		purego.RegisterLibFunc(&cgContextDrawImage, cg, "CGContextDrawImage")
		purego.RegisterLibFunc(&cgContextRelease, cg, "CGContextRelease")
	})
	return imageIOErr
}

// decodeNativeImage decodes OS-supported image formats that Go's standard image
// package does not register (such as HEIC/HEIF used by Hongguo covers) via
// macOS ImageIO + CoreGraphics without external binaries or temp files.
func decodeNativeImage(data []byte, maxEdge int) (*image.RGBA, error) {
	if len(data) == 0 {
		return nil, errors.New("empty image data")
	}
	if err := initImageIO(); err != nil {
		return nil, err
	}
	cfData := cfDataCreate(0, &data[0], len(data))
	runtime.KeepAlive(data)
	if cfData == 0 {
		return nil, errors.New("CFDataCreate failed")
	}
	defer cfReleaseImg(cfData)

	src := cgImageSourceCreateData(cfData, 0)
	if src == 0 {
		return nil, errors.New("CGImageSourceCreateWithData failed")
	}
	defer cfReleaseImg(src)

	cgImg := cgImageSourceCreateImage(src, 0, 0)
	if cgImg == 0 {
		return nil, errors.New("CGImageSourceCreateImageAtIndex failed")
	}
	defer cgImageRelease(cgImg)

	w := int(cgImageGetWidth(cgImg))
	h := int(cgImageGetHeight(cgImg))
	if w <= 0 || h <= 0 || int64(w)*int64(h) > 16<<20 {
		return nil, fmt.Errorf("native image dimensions invalid: %dx%d", w, h)
	}
	dw, dh := w, h
	if maxEdge > 0 && max(w, h) > maxEdge {
		dw = max(1, w*maxEdge/max(w, h))
		dh = max(1, h*maxEdge/max(w, h))
	}

	cs := cgColorSpaceCreateRGB()
	if cs == 0 {
		return nil, errors.New("CGColorSpaceCreateDeviceRGB failed")
	}
	defer cgColorSpaceRelease(cs)

	rgba := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// kCGImageAlphaPremultipliedLast (1) | kCGBitmapByteOrder32Big (4<<12) = RGBA bytes.
	const bitmapInfoRGBA = uint32(1 | (4 << 12))
	ctx := cgBitmapContextCreate(unsafe.Pointer(&rgba.Pix[0]), uintptr(dw), uintptr(dh), 8, uintptr(rgba.Stride), cs, bitmapInfoRGBA)
	if ctx == 0 {
		return nil, errors.New("CGBitmapContextCreate failed")
	}
	defer cgContextRelease(ctx)

	const interpolationMedium = 4
	cgContextSetInterpolation(ctx, interpolationMedium)
	cgContextDrawImage(ctx, cgRect{Size: cgSize{W: float64(dw), H: float64(dh)}}, cgImg)
	runtime.KeepAlive(rgba)
	return rgba, nil
}
