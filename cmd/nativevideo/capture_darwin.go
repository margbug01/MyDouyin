package main

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"structs"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type captureRect struct {
	_          structs.HostLayout
	X, Y, W, H float64
}

// Own-process content avoids asking to capture another application's windows.
func captureOwnWindow(number, width, height int) ([]byte, error) {
	_, err := purego.Dlopen("/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, err
	}
	selector := objc.RegisterName
	contentClass := objc.ID(objc.GetClass("SCShareableContent"))
	method := selector("getCurrentProcessShareableContentWithCompletionHandler:")
	if !objc.Send[bool](contentClass, selector("respondsToSelector:"), method) {
		return nil, errors.New("own-process ScreenCaptureKit API unavailable")
	}
	type result struct {
		bytes []byte
		err   error
	}
	done := make(chan result, 1)
	imageBlock := objc.NewBlock(func(_ objc.Block, image uintptr, nserr objc.ID) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(selector("alloc")).Send(selector("init"))
		defer pool.Send(selector("drain"))
		if image == 0 {
			done <- result{err: fmt.Errorf("ScreenCaptureKit returned no image: %s", nativeError(nserr))}
			return
		}
		rep := objc.ID(objc.GetClass("NSBitmapImageRep")).Send(selector("alloc")).Send(selector("initWithCGImage:"), image)
		if rep == 0 {
			done <- result{err: errors.New("cannot encode native screenshot")}
			return
		}
		defer rep.Send(selector("release"))
		data := rep.Send(selector("representationUsingType:properties:"), uint(4), objc.ID(objc.GetClass("NSDictionary")).Send(selector("dictionary")))
		length := objc.Send[uint](data, selector("length"))
		ptr := objc.Send[unsafe.Pointer](data, selector("bytes"))
		if ptr == nil || length == 0 {
			done <- result{err: errors.New("empty native screenshot")}
			return
		}
		done <- result{bytes: append([]byte(nil), unsafe.Slice((*byte)(ptr), int(length))...)}
	})
	defer imageBlock.Release()
	contentBlock := objc.NewBlock(func(_ objc.Block, content, nserr objc.ID) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(selector("alloc")).Send(selector("init"))
		defer pool.Send(selector("drain"))
		if content == 0 {
			done <- result{err: fmt.Errorf("ScreenCaptureKit content: %s", nativeError(nserr))}
			return
		}
		windows := content.Send(selector("windows"))
		var window objc.ID
		for i, n := uint(0), objc.Send[uint](windows, selector("count")); i < n; i++ {
			w := windows.Send(selector("objectAtIndex:"), i)
			if objc.Send[uint](w, selector("windowID")) == uint(number) {
				window = w
				break
			}
		}
		if window == 0 {
			done <- result{err: errors.New("own window absent from shareable content")}
			return
		}
		frame := objc.Send[captureRect](window, selector("frame"))
		log.Printf("native capture window=%d frame=%+v", number, frame)
		if int(frame.W) != width || int(frame.H) != height {
			done <- result{err: fmt.Errorf("ScreenCaptureKit reports %.0fx%.0f for a %dx%d window; native screenshot not verified", frame.W, frame.H, width, height)}
			return
		}
		filter := objc.ID(objc.GetClass("SCContentFilter")).Send(selector("alloc")).Send(selector("initWithDesktopIndependentWindow:"), window)
		defer filter.Send(selector("release"))
		config := objc.ID(objc.GetClass("SCStreamConfiguration")).Send(selector("alloc")).Send(selector("init"))
		defer config.Send(selector("release"))
		rect := objc.Send[captureRect](filter, selector("contentRect"))
		log.Printf("native capture content=%+v", rect)
		config.Send(selector("setWidth:"), uint(frame.W))
		config.Send(selector("setHeight:"), uint(frame.H))
		config.Send(selector("setSourceRect:"), captureRect{W: rect.W, H: rect.H})
		if objc.Send[bool](config, selector("respondsToSelector:"), selector("setIgnoreShadowsSingleWindow:")) {
			config.Send(selector("setIgnoreShadowsSingleWindow:"), true)
		}
		config.Send(selector("setShowsCursor:"), false)
		objc.ID(objc.GetClass("SCScreenshotManager")).Send(selector("captureImageWithFilter:configuration:completionHandler:"), filter, config, imageBlock)
	})
	defer contentBlock.Release()
	contentClass.Send(method, contentBlock)
	select {
	case r := <-done:
		return r.bytes, r.err
	case <-time.After(10 * time.Second):
		return nil, errors.New("native screenshot timed out")
	}
}

func nativeError(err objc.ID) string {
	if err == 0 {
		return "unknown error"
	}
	ptr := objc.Send[unsafe.Pointer](err.Send(objc.RegisterName("localizedDescription")), objc.RegisterName("UTF8String"))
	if ptr == nil {
		return "unknown error"
	}
	n := 0
	for n < 2048 && *(*byte)(unsafe.Add(ptr, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptr), n))
}
