package media

import (
	"context"
	"errors"
	"sync"

	"github.com/ebitengine/purego/objc"
)

// WarmVideo holds an asset whose metadata is loading without a player or decoder,
// or a pre-registered loopback proxy route (a CENC stream, or one with its own
// Client) whose moov + initial bytes are cached in RAM. Its owner either
// transfers it to Open/OpenContext or cancels it with Close.
type WarmVideo struct {
	asset objc.ID

	mu       sync.Mutex
	looped   bool // a loopback route rather than an asset
	done     chan struct{}
	cancel   context.CancelFunc
	proxyURL string
	release  func()
	err      error
	closed   bool
	opened   bool
}

func PreloadVideo(url string, headers map[string]string) *WarmVideo {
	defer autorelease()()
	asset, err := videoAsset(url, headers)
	if err != nil || asset == 0 {
		return nil
	}
	asset.Send(sel("retain"))
	completed := objc.NewBlock(func(objc.Block) {})
	defer completed.Release()
	keys := objc.ID(objc.GetClass("NSArray")).Send(sel("arrayWithObject:"), nsString("tracks"))
	asset.Send(sel("loadValuesAsynchronouslyForKeys:completionHandler:"), keys, completed)
	return &WarmVideo{asset: asset}
}

func PreloadStream(ctx context.Context, source string, opts StreamOptions) *WarmVideo {
	if source == "" {
		return nil
	}
	if len(opts.DecryptKey) == 0 && opts.Client == nil {
		return PreloadVideo(source, opts.Headers)
	}
	if len(opts.DecryptKey) != 0 && len(opts.DecryptKey) != 16 {
		return nil
	}
	wctx, cancel := context.WithCancel(ctx)
	w := &WarmVideo{
		looped: true,
		done:   make(chan struct{}),
		cancel: cancel,
	}
	var hdrCopy map[string]string
	if len(opts.Headers) > 0 {
		hdrCopy = make(map[string]string, len(opts.Headers))
		for k, v := range opts.Headers {
			hdrCopy[k] = v
		}
	}
	keyCopy := append([]byte(nil), opts.DecryptKey...)
	go func() {
		defer close(w.done)
		pu, rel, rerr := localProxy.registerOptions(wctx, source, StreamOptions{Headers: hdrCopy, DecryptKey: keyCopy, Client: opts.Client}, true)
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			if rel != nil {
				rel()
			}
			return
		}
		w.proxyURL, w.release, w.err = pu, rel, rerr
		w.mu.Unlock()
	}()
	return w
}

func (w *WarmVideo) Open() (*Live, error) {
	return w.OpenContext(context.Background(), true)
}

func (w *WarmVideo) OpenContext(ctx context.Context, native bool) (*Live, error) {
	if w == nil {
		return nil, errors.New("media: nil warm video")
	}
	if w.looped {
		select {
		case <-w.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		w.mu.Lock()
		if w.closed || w.opened {
			w.mu.Unlock()
			return nil, errors.New("media: video preload already consumed or closed")
		}
		if w.err != nil || w.proxyURL == "" {
			err := w.err
			w.mu.Unlock()
			if err == nil {
				err = errors.New("media: video preload failed")
			}
			return nil, err
		}
		w.opened = true
		pu, rel := w.proxyURL, w.release
		w.release = nil
		w.mu.Unlock()

		l, err := openStream(pu, nil, true, !native)
		if err != nil {
			if rel != nil {
				rel()
			}
			return nil, err
		}
		l.release = rel
		return l, nil
	}
	defer autorelease()()
	asset := w.asset
	w.asset = 0
	if asset == 0 {
		return nil, errors.New("media: video preload already closed")
	}
	defer asset.Send(sel("release"))
	if status := objc.Send[int](asset, sel("statusOfValueForKey:error:"), nsString("tracks"), uintptr(0)); status >= 3 {
		return nil, errors.New("media: video preload failed")
	}
	return openAsset(asset, true, !native)
}

func (w *WarmVideo) Close() {
	if w == nil {
		return
	}
	if w.looped {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		w.closed = true
		cancel := w.cancel
		rel := w.release
		w.release = nil
		w.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if rel != nil {
			rel()
		}
		return
	}
	if w.asset == 0 {
		return
	}
	defer autorelease()()
	w.asset.Send(sel("cancelLoading"))
	w.asset.Send(sel("release"))
	w.asset = 0
}
