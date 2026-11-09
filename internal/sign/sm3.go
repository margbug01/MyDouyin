package sign

import (
	"encoding/binary"
	"math/bits"
)

// sm3 is GB/T 32905-2016. Go has no SM3 in the standard library, and a_bogus
// needs it for three digests.
func sm3(msg []byte) [32]byte {
	state := [8]uint32{0x7380166F, 0x4914B2B9, 0x172442D7, 0xDA8A0600, 0xA96F30BC, 0x163138AA, 0xE38DEE4D, 0xB0FB0E4E}
	padded := append(append([]byte{}, msg...), 0x80)
	for len(padded)%64 != 56 {
		padded = append(padded, 0)
	}
	padded = binary.BigEndian.AppendUint64(padded, uint64(len(msg))*8)

	p1 := func(x uint32) uint32 { return x ^ bits.RotateLeft32(x, 15) ^ bits.RotateLeft32(x, 23) }
	for off := 0; off < len(padded); off += 64 {
		var w [68]uint32
		for i := 0; i < 16; i++ {
			w[i] = binary.BigEndian.Uint32(padded[off+4*i:])
		}
		for j := 16; j < 68; j++ {
			w[j] = p1(w[j-16]^w[j-9]^bits.RotateLeft32(w[j-3], 15)) ^ bits.RotateLeft32(w[j-13], 7) ^ w[j-6]
		}
		a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3], state[4], state[5], state[6], state[7]
		for j := 0; j < 64; j++ {
			t, ff, gg := uint32(0x79CC4519), a^b^c, e^f^g
			if j >= 16 {
				t, ff, gg = 0x7A879D8A, a&b|a&c|b&c, e&f|^e&g
			}
			ss1 := bits.RotateLeft32(bits.RotateLeft32(a, 12)+e+bits.RotateLeft32(t, j), 7)
			ss2 := ss1 ^ bits.RotateLeft32(a, 12)
			tt1 := ff + d + ss2 + (w[j] ^ w[j+4])
			tt2 := gg + h + ss1 + w[j]
			d, c, b, a = c, bits.RotateLeft32(b, 9), a, tt1
			h, g, f = g, bits.RotateLeft32(f, 19), e
			e = tt2 ^ bits.RotateLeft32(tt2, 9) ^ bits.RotateLeft32(tt2, 17)
		}
		for i, v := range [8]uint32{a, b, c, d, e, f, g, h} {
			state[i] ^= v
		}
	}
	var out [32]byte
	for i, v := range state {
		binary.BigEndian.PutUint32(out[4*i:], v)
	}
	return out
}
