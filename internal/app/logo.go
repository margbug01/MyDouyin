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

// hongguo_logo.png is the MyHongguo rounded-square badge at 128 pixels.
//
//go:embed hongguo_logo.png
var hongguoLogoPNG []byte

// hongguoLogo is the MyHongguo mode icon, shown in the top-left brand switcher
// and MyHongguo views.
var hongguoLogo = func() *ui.Bitmap {
	b, err := ui.DecodeBitmap(hongguoLogoPNG)
	if err != nil {
		panic(err)
	}
	return b
}()
