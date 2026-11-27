// Package tiktok implements TikTok's pure-Go X-Bogus request signature used for
// lightweight direct HTTP fallback requests when no WebView is attached.
package tiktok

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"time"
)

const xbogusAlphabet = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe"

// rc4Crypt applies standard RC4 with key to data.
func rc4Crypt(key, data []byte) []byte {
	var s [256]byte
	for i := range s {
		s[i] = byte(i)
	}
	var j byte
	for i := 0; i < 256; i++ {
		j += s[i] + key[i%len(key)]
		s[i], s[j] = s[j], s[i]
	}
	out := make([]byte, len(data))
	var i byte
	j = 0
	for k, b := range data {
		i++
		j += s[i]
		s[i], s[j] = s[j], s[i]
		out[k] = b ^ s[byte(s[i]+s[j])]
	}
	return out
}

func doubleMD5(b []byte) [16]byte {
	h1 := md5.Sum(b)
	return md5.Sum(h1[:])
}

// SignXBogus computes a 28-character TikTok X-Bogus signature for query and userAgent
// at Unix timestamp ts (seconds). If ts is 0, time.Now().Unix() is used.
func SignXBogus(query, userAgent string, ts uint32) string {
	if ts == 0 {
		ts = uint32(time.Now().Unix())
	}
	qHash := doubleMD5([]byte(query))
	bodyHash := doubleMD5(nil)

	uaEnc := rc4Crypt([]byte{0x00, 0x01, 0x0e}, []byte(userAgent))
	uaB64 := base64.StdEncoding.EncodeToString(uaEnc)
	uaHash := md5.Sum([]byte(uaB64))

	const canvas = uint32(536919696)
	var payload [19]byte
	payload[0] = 64
	payload[1] = 0x00
	payload[2] = 0x01
	payload[3] = 0x0e
	payload[4] = qHash[14]
	payload[5] = qHash[15]
	payload[6] = bodyHash[14]
	payload[7] = bodyHash[15]
	payload[8] = uaHash[14]
	payload[9] = uaHash[15]
	binary.BigEndian.PutUint32(payload[10:14], ts)
	binary.BigEndian.PutUint32(payload[14:18], canvas)

	var x byte
	for _, b := range payload[:18] {
		x ^= b
	}
	payload[18] = x

	enc := rc4Crypt([]byte{0xff}, payload[:])
	var raw [21]byte
	raw[0] = 0x02
	raw[1] = 0xff
	copy(raw[2:], enc)

	var out [28]byte
	for i, o := 0, 0; i < 21; i, o = i+3, o+4 {
		v := uint32(raw[i])<<16 | uint32(raw[i+1])<<8 | uint32(raw[i+2])
		out[o] = xbogusAlphabet[(v>>18)&0x3f]
		out[o+1] = xbogusAlphabet[(v>>12)&0x3f]
		out[o+2] = xbogusAlphabet[(v>>6)&0x3f]
		out[o+3] = xbogusAlphabet[v&0x3f]
	}
	return string(out[:])
}
