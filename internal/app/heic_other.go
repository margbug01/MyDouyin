//go:build !darwin

package app

import (
	"bytes"
	"errors"
	"fmt"
	"image"

	"github.com/gen2brain/h265/heic"
	"golang.org/x/image/draw"
)

// decodeNativeImage decodes HEIC/HEIF images (such as Hongguo covers) in pure Go
// on non-Darwin platforms without requiring CGO or Windows Store codec packs.
func decodeNativeImage(data []byte, maxEdge int) (*image.RGBA, error) {
	if len(data) == 0 {
		return nil, errors.New("empty image data")
	}
	cfg, err := heic.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16<<20 {
		return nil, fmt.Errorf("native image dimensions invalid: %dx%d", cfg.Width, cfg.Height)
	}
	img, err := heic.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("native image dimensions invalid: %dx%d", w, h)
	}
	dw, dh := w, h
	if maxEdge > 0 && max(w, h) > maxEdge {
		dw = max(1, w*maxEdge/max(w, h))
		dh = max(1, h*maxEdge/max(w, h))
	}
	rgba := image.NewRGBA(image.Rect(0, 0, dw, dh))
	if dw == w && dh == h {
		draw.Copy(rgba, image.Point{}, img, b, draw.Src, nil)
	} else {
		draw.ApproxBiLinear.Scale(rgba, rgba.Bounds(), img, b, draw.Src, nil)
	}
	return rgba, nil
}
