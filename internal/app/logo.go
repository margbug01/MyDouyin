package app

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

// logo.png is the app icon's rounded square alone, without the room Apple's
// grid leaves around it, drawn from resources/icon.svg at 128 pixels.
//
//go:embed logo.png
var logoPNG []byte

// logo is the app's icon, at the top left beside its name.
var logo = func() *ui.Bitmap {
	b, err := ui.DecodeBitmap(logoPNG)
	if err != nil {
		panic(err)
	}
	return b
}()
