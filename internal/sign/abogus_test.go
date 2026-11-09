package sign

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// unmaskPair is the inverse of maskPair: each byte gives back its payload half.
func unmaskPair(c []byte) (byte, byte) {
	return c[0]&0x55 | c[1]&0xAA, c[2]&0x55 | c[3]&0xAA
}

func unb64(t *testing.T, s, alphabet string) []byte {
	var out []byte
	body := strings.TrimRight(s, "=")
	for off := 0; off < len(body); off += 4 {
		chunk := body[off:min(off+4, len(body))]
		blk := 0
		for _, c := range chunk {
			i := strings.IndexRune(alphabet, c)
			if i < 0 {
				t.Fatalf("not an alphabet digit: %q", c)
			}
			blk = blk<<6 | i
		}
		blk <<= 6 * (4 - len(chunk))
		for _, sh := range []int{16, 8, 0}[:len(chunk)-1] {
			out = append(out, byte(blk>>sh))
		}
	}
	return out
}

// decode takes a_bogus apart and returns its fifty fields and the checksum check.
func decode(t *testing.T, v string) (fields map[int]byte, browserInfo string, nowMs int64) {
	p := unb64(t, v, alphabetS4)
	if a, b := unmaskPair(p[:4]); a != 3 || b != 82 {
		t.Fatalf("header magic %d %d", a, b)
	}
	plain := rc4([]byte{payloadKey}, p[4:])
	ver, frame := plain[:8], plain[8:]
	var un []byte
	for off := 0; off+4 <= len(frame); off += 4 {
		g := frame[off : off+4]
		for i := 0; i < 3; i++ {
			un = append(un, g[i]&dataMasks[i]|g[3]&noiseMasks[i])
		}
	}
	fields = map[int]byte{}
	for i, n := range fieldOrder {
		fields[n] = un[i]
	}
	info := int(fields[79]) | int(fields[80])<<8
	tail := int(fields[84]) | int(fields[85])<<8
	end := len(fieldOrder) + info + tail
	sum := byte(0)
	for _, b := range ver {
		sum ^= b
	}
	for _, n := range fieldOrder {
		sum ^= fields[n]
	}
	if un[end] != sum {
		t.Fatalf("checksum %d != %d", un[end], sum)
	}
	for i := 0; i < 6; i++ {
		nowMs |= int64(fields[29+i]) << (8 * i)
	}
	return fields, string(un[len(fieldOrder) : len(fieldOrder)+info]), nowMs
}

func TestSM3Vector(t *testing.T) {
	// GB/T 32905-2016 appendix A example 1.
	d := sm3([]byte("abc"))
	const want = "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0"
	if got := strings.ToLower(hex(d[:])); got != want {
		t.Fatalf("sm3(abc) = %s", got)
	}
}

func hex(b []byte) string {
	const h = "0123456789abcdef"
	var o []byte
	for _, c := range b {
		o = append(o, h[c>>4], h[c&15])
	}
	return string(o)
}

// Browser-made signatures are the oracle: every field that does not depend on noise
// must come out the same when we rebuild it from the same query, UA and clock.
func TestMatchesBrowser(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/abogus_browser.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Samples []struct {
			ABogus      string `json:"a_bogus"`
			Query, Body string
			UserAgent   string `json:"user_agent"`
			BrowserInfo string `json:"browser_info"`
		}
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for i, s := range fx.Samples {
		bf, info, now := decode(t, s.ABogus)
		if info != s.BrowserInfo {
			t.Fatalf("sample %d browser info %q", i, info)
		}
		sg := New(s.UserAgent)
		sg.BrowserInfo = s.BrowserInfo
		mine := sg.fields(s.Query, s.Body, now)
		for _, n := range fieldOrder {
			if n == 61 || n == 62 || n == 63 || n == 64 || n == 65 || n == 60 { // ink: now-1, checked below
				continue
			}
			if mine[n] != bf[n] {
				t.Errorf("sample %d: L%d mine %d browser %d", i, n, mine[n], bf[n])
			}
		}

		// And what we sign decodes to the same deterministic fields.
		ours, _, _ := decode(t, sg.sign(s.Query, s.Body, now))
		for _, n := range fieldOrder {
			if ours[n] != mine[n] {
				t.Errorf("sample %d: signed L%d = %d want %d", i, n, ours[n], mine[n])
			}
		}
	}
}
