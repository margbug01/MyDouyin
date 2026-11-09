// Package sign computes the a_bogus signature Douyin's web API wants on every
// data request. The algorithm is ported from the Apache-2.0 Python
// implementation in Evil0ctal/Douyin_TikTok_Download_API
// (src/dtk/signing/native/abogus.py, bdms.js 1.0.1.19-fix.01); the long
// explanation of why each constant is what it is lives there.
package sign

import (
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	salt       = "dhzx"
	payloadKey = 0xD3
	pageID     = 6241
	aid        = 6383
	// fortnightEpoch is 2024-07-24T16:00:00Z in ms; one field counts fortnights from it.
	fortnightEpoch = 1_721_836_800_000

	alphabetS3 = "ckdp1h4ZKsUB80/Mfvw36XIgR25+WQAlEi7NLboqYTOPuzmFjJnryx9HVGDaStCe"
	alphabetS4 = "Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe"

	// DefaultBrowserInfo is inner/outer/avail/screen sizes and platform, the
	// one readable part of the payload; a constant beats a per-request random one.
	DefaultBrowserInfo = "1920|947|1920|1032|1920|1032|1920|1080|Win32"
)

var (
	noiseMasks = [3]byte{0x91, 0x42, 0x2C}
	dataMasks  = [3]byte{0x6E, 0xBD, 0xD3}
	// fieldOrder is the fixed permutation of the fifty scalar fields (names are L<n>).
	fieldOrder = [...]int{
		34, 44, 56, 61, 73, 29, 70, 45, 35, 49, 38, 66, 51, 68, 28, 48, 64, 47, 30, 71,
		26, 55, 31, 69, 59, 40, 62, 63, 27, 72, 41, 74, 57, 52, 42, 39, 33, 67, 53, 43,
		65, 46, 36, 24, 60, 32, 79, 80, 84, 85,
	}
)

// Signer signs requests as one browser identity.
type Signer struct {
	UserAgent   string
	BrowserInfo string
	rng         *rand.Rand
}

func New(userAgent string) *Signer {
	return &Signer{UserAgent: userAgent, BrowserInfo: DefaultBrowserInfo, rng: rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1))}
}

// Sign returns a_bogus for a query string (without a_bogus itself) and the
// request body ("" for GET).
func (s *Signer) Sign(query, body string) string {
	return s.sign(query, body, time.Now().UnixMilli())
}

func (s *Signer) sign(query, body string, now int64) string {
	f := s.fields(query, body, now)

	version := s.maskPair(1, 0, -1, -1)
	// The low byte is a free draw, the high one reports "tripwires intact".
	version = append(version, s.maskPair(1, 0, int(probeNoise(s.rng)), int(s.rng.IntN(255)&0x4D|0xB2))...)

	checksum := byte(0)
	for _, b := range version {
		checksum ^= b
	}
	plain := make([]byte, 0, 128)
	for _, n := range fieldOrder {
		checksum ^= f[n]
		plain = append(plain, f[n])
	}
	plain = append(plain, jsBytes(s.BrowserInfo)...)
	plain = append(plain, jsBytes(itoa(int((now+3)&0xFF))+",")...)
	plain = append(plain, checksum)

	// Header noise: second byte reports "Chrome" (band 0..39). shortcut: Chrome UA only.
	header := s.maskPair(3, 82, -1, s.rng.IntN(40))
	frame := s.expandNoise(plain)
	sealed := rc4([]byte{payloadKey}, append(version, frame...))
	return b64(append(header, sealed...), alphabetS4)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

// fields returns the scalar fields indexed by their L-number.
func (s *Signer) fields(query, body string, now int64) map[int]byte {
	qd := digestTwice(query)
	bd := digestTwice(body)
	ud := sm3([]byte(b64(rc4([]byte{0, 1, 14}, jsBytes(strings.TrimSpace(s.UserAgent))), alphabetS3)))

	f := map[int]byte{
		24: 41, 26: byte((now - fortnightEpoch) / (1000 * 60 * 60 * 24 * 14)), 27: 6, 28: 3,
		35: 1, 36: 0, 38: 0x21, 39: 0, 66: 3,
		79: byte(len(jsBytes(s.BrowserInfo))), 80: byte(len(jsBytes(s.BrowserInfo)) >> 8),
	}
	tail := jsBytes(itoa(int((now+3)&0xFF)) + ",")
	f[84], f[85] = byte(len(tail)), byte(len(tail)>>8)
	le := func(first int, v int64, n int) {
		for i := 0; i < n; i++ {
			f[first+i] = byte(v >> (8 * i))
		}
	}
	le(29, now, 6)
	le(44, 14, 4)
	le(40, 0, 4)
	le(60, now-1, 6)
	le(67, pageID, 4)
	le(71, aid, 4)
	chain := func(d [32]byte, slots [3]int, i0, i1 int, canary [3]int) {
		f[slots[0]], f[slots[1]] = d[i0], d[i1]
		f[slots[2]] = canaryOf(d[:], canary[1], canary[0], canary[2])
	}
	chain(qd, [3]int{48, 49, 51}, 9, 18, [3]int{3, 11, 12})
	chain(bd, [3]int{52, 53, 55}, 10, 19, [3]int{4, 8, 9})
	chain(ud, [3]int{56, 57, 59}, 11, 21, [3]int{5, 12, 13})
	return f
}

func digestTwice(text string) [32]byte {
	d := sm3([]byte(text + salt))
	return sm3(d[:])
}

// canaryOf is the first digest byte at or after offset that is not sentinel.
func canaryOf(d []byte, sentinel, offset, fallback int) byte {
	for _, b := range d[offset:] {
		if int(b) != sentinel {
			return b
		}
	}
	return byte(fallback)
}

// jsBytes turns a string into bytes the way the SDK does: one byte per UTF-16
// unit below U+0100, two big-endian bytes above it.
func jsBytes(s string) []byte {
	var out []byte
	for _, u := range utf16.Encode([]rune(s)) {
		if u&0xFF00 != 0 {
			out = append(out, byte(u>>8))
		}
		out = append(out, byte(u))
	}
	return out
}

func probeNoise(r *rand.Rand) byte {
	v := r.IntN(240)
	if v > 109 {
		v += v%2 + 1
	}
	return byte(v)
}

// maskPair spreads two bytes over four, half of each from noise. low/high < 0 means random.
func (s *Signer) maskPair(a, b byte, low, high int) []byte {
	noise := s.rng.IntN(65535)
	if low < 0 {
		low = noise & 0xFF
	}
	if high < 0 {
		high = (noise >> 8) & 0xFF
	}
	l, h := byte(low), byte(high)
	return []byte{l&0xAA | a&0x55, l&0x55 | a&0xAA, h&0xAA | b&0x55, h&0x55 | b&0xAA}
}

// expandNoise turns each 3 body bytes and 1 noise byte into 4 carrier bytes.
func (s *Signer) expandNoise(body []byte) []byte {
	var out []byte
	for off := 0; off < len(body); off += 3 {
		g := body[off:min(off+3, len(body))]
		if len(g) < 3 {
			out = append(out, g[0])
			if len(g) > 1 && g[1] != 0 {
				out = append(out, g[1])
			}
			continue
		}
		noise := byte(s.rng.IntN(1000))
		for i := 0; i < 3; i++ {
			out = append(out, noise&noiseMasks[i]|g[i]&dataMasks[i])
		}
		out = append(out, g[0]&noiseMasks[0]|g[1]&noiseMasks[1]|g[2]&noiseMasks[2])
	}
	return out
}

// rc4 is the SDK's variant: descending S-box seed and a multiplying key schedule.
func rc4(key, data []byte) []byte {
	var box [256]int
	for i := range box {
		box[255-i] = i
	}
	j := 0
	for i := 0; i < 256; i++ {
		j = (j*box[i] + j + int(key[i%len(key)])) % 256
		box[i], box[j] = box[j], box[i]
	}
	out := make([]byte, len(data))
	i, j := 0, 0
	for n, c := range data {
		i = (i + 1) % 256
		j = (j + box[i]) % 256
		box[i], box[j] = box[j], box[i]
		out[n] = c ^ byte(box[(box[i]+box[j])%256])
	}
	return out
}

// b64 is base64 over a custom alphabet with a literal '=' pad.
func b64(data []byte, alphabet string) string {
	var out []byte
	for off := 0; off < len(data); off += 3 {
		chunk := data[off:min(off+3, len(data))]
		var blk [3]byte
		copy(blk[:], chunk)
		v := int(blk[0])<<16 | int(blk[1])<<8 | int(blk[2])
		for i, sh := range [4]int{18, 12, 6, 0} {
			if i <= len(chunk) {
				out = append(out, alphabet[(v>>sh)&0x3F])
			}
		}
	}
	for len(out)%4 != 0 {
		out = append(out, '=')
	}
	return string(out)
}
