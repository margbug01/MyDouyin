package media

import (
	"errors"

	"github.com/ebitengine/purego/objc"
)

// WarmVideo holds an asset whose metadata is loading without a player or decoder.
// Its owner either transfers it to Open or cancels it with Close.
type WarmVideo struct{ asset objc.ID }

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

func (w *WarmVideo) Open() (*Live, error) {
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
	return openAsset(asset, true, false)
}

func (w *WarmVideo) Close() {
	if w.asset == 0 {
		return
	}
	defer autorelease()()
	w.asset.Send(sel("cancelLoading"))
	w.asset.Send(sel("release"))
	w.asset = 0
}
