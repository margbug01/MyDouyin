//go:build !darwin

package app

import (
	"errors"
	"image"
)

func decodeNativeImage([]byte, int) (*image.RGBA, error) {
	return nil, errors.New("unsupported native image format on this platform")
}
