//go:build !darwin

package app

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/gen2brain/h265/heic"
)

func TestDecodeHEICCoverOnNonDarwin(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 64; x++ {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 2), B: 180, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := heic.Encode(&raw, src, nil); err != nil {
		t.Fatalf("heic.Encode failed: %v", err)
	}

	bmp, encoded, err := decodePictureEdgeBytes(raw.Bytes(), coverEdge)
	if err != nil {
		t.Fatalf("decodePictureEdgeBytes(heic) failed: %v", err)
	}
	if bmp == nil {
		t.Fatal("expected non-nil decoded bitmap")
	}
	if w, h := bmp.Size(); w != 64 || h != 96 {
		t.Fatalf("decoded size = %dx%d, want 64x96", w, h)
	}
	if len(encoded) < 2 || encoded[0] != 0xff || encoded[1] != 0xd8 {
		t.Fatalf("expected re-encoded JPEG bytes for disk cache, got prefix %x", encoded[:min(4, len(encoded))])
	}
}
