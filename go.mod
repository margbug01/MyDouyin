module douyin

go 1.27.3

require (
	github.com/beilunyang/openhongguo v0.4.1
	github.com/ebitengine/purego v0.11.1
	github.com/egoist/mygo v0.10.0
	golang.org/x/image v0.46.0
	golang.org/x/net v0.59.0
)

require (
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/cloudflare/circl v1.6.2 // indirect
	github.com/gen2brain/h265 v0.2.7 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	go.digitalxero.dev/rpm v0.3.4 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

tool github.com/egoist/mygo/cmd/mygo

// Temporary upstream PR #25 fix: keep x-medusa on /novel/player/video_model/v1/
// (required by Hongguo since 2026-11-23). Remove once openhongguo v0.4.2 lands.
replace github.com/beilunyang/openhongguo => github.com/J3n5en/openhongguo v0.4.1-0.20261124180309-09a0160f6f72
