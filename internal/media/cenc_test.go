package media

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func makeBox(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func makeFullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	body := make([]byte, 4+len(payload))
	body[0] = version
	body[1] = byte(flags >> 16)
	body[2] = byte(flags >> 8)
	body[3] = byte(flags)
	copy(body[4:], payload)
	return makeBox(typ, body)
}

func makeSinfBox(origFmt, scheme string, ivSize byte, constIV []byte) []byte {
	frma := makeBox("frma", []byte(origFmt))
	schmBody := make([]byte, 8)
	copy(schmBody[0:4], scheme)
	binary.BigEndian.PutUint32(schmBody[4:8], 0x00010000)
	schm := makeFullBox("schm", 0, 0, schmBody)

	tencLen := 20
	if ivSize == 0 && len(constIV) > 0 {
		tencLen += 1 + len(constIV)
	}
	tencBody := make([]byte, tencLen)
	tencBody[2] = 1 // default_isProtected = 1
	tencBody[3] = ivSize
	for i := range 16 {
		tencBody[4+i] = byte(0x10 + i) // KID
	}
	if ivSize == 0 && len(constIV) > 0 {
		tencBody[20] = byte(len(constIV))
		copy(tencBody[21:], constIV)
	}
	tenc := makeFullBox("tenc", 1, 0, tencBody)
	schi := makeBox("schi", tenc)

	var buf bytes.Buffer
	buf.Write(frma)
	buf.Write(schm)
	buf.Write(schi)
	return makeBox("sinf", buf.Bytes())
}

func makeSencBox(entries []sencEntry, ivSize int, useSubsamples bool) []byte {
	var body bytes.Buffer
	var cnt [4]byte
	binary.BigEndian.PutUint32(cnt[:], uint32(len(entries)))
	body.Write(cnt[:])
	for _, e := range entries {
		if ivSize > 0 {
			body.Write(e.iv[:ivSize])
		}
		if useSubsamples {
			var sc [2]byte
			binary.BigEndian.PutUint16(sc[:], uint16(len(e.subsamples)))
			body.Write(sc[:])
			for _, sub := range e.subsamples {
				var pair [6]byte
				binary.BigEndian.PutUint16(pair[0:2], uint16(sub.clearBytes))
				binary.BigEndian.PutUint32(pair[2:6], sub.protectedBytes)
				body.Write(pair[:])
			}
		}
	}
	var flags uint32
	if useSubsamples {
		flags = 0x000002
	}
	return makeFullBox("senc", 0, flags, body.Bytes())
}

func encodeAuxData(entries []sencEntry, ivSize int) ([]byte, []uint8) {
	var all bytes.Buffer
	sizes := make([]uint8, len(entries))
	for i, e := range entries {
		start := all.Len()
		if ivSize > 0 {
			all.Write(e.iv[:ivSize])
		}
		if len(e.subsamples) > 0 {
			var sc [2]byte
			binary.BigEndian.PutUint16(sc[:], uint16(len(e.subsamples)))
			all.Write(sc[:])
			for _, sub := range e.subsamples {
				var pair [6]byte
				binary.BigEndian.PutUint16(pair[0:2], uint16(sub.clearBytes))
				binary.BigEndian.PutUint32(pair[2:6], sub.protectedBytes)
				all.Write(pair[:])
			}
		}
		sizes[i] = uint8(all.Len() - start)
	}
	return all.Bytes(), sizes
}

func makeSaizBox(sizes []uint8) []byte {
	var body bytes.Buffer
	body.WriteByte(0) // default_sample_info_size = 0
	var cnt [4]byte
	binary.BigEndian.PutUint32(cnt[:], uint32(len(sizes)))
	body.Write(cnt[:])
	body.Write(sizes)
	return makeFullBox("saiz", 0, 0, body.Bytes())
}

func makeSaioBox(offset uint64) []byte {
	var body bytes.Buffer
	var cnt [4]byte
	binary.BigEndian.PutUint32(cnt[:], 1)
	body.Write(cnt[:])
	var off [8]byte
	binary.BigEndian.PutUint64(off[:], offset)
	body.Write(off[:])
	return makeFullBox("saio", 1, 0, body.Bytes())
}

func encryptSampleInPlace(block cipher.Block, sample []byte, entry sencEntry) {
	if len(entry.subsamples) == 0 {
		cipher.NewCTR(block, entry.iv[:]).XORKeyStream(sample, sample)
		return
	}
	stream := cipher.NewCTR(block, entry.iv[:])
	pos := 0
	for _, sub := range entry.subsamples {
		pos += int(sub.clearBytes)
		if pos >= len(sample) {
			break
		}
		end := min(pos+int(sub.protectedBytes), len(sample))
		if end > pos {
			stream.XORKeyStream(sample[pos:end], sample[pos:end])
		}
		pos = end
	}
}

type testEncOpts struct {
	ivSize        int
	useSubsamples bool
	useSAIO       bool
	moovAtStart   bool
	scheme        string
}

// encryptSilentMP4 transforms real testdata/silent.mp4 into a valid ISO-BMFF CENC MP4
// and returns (encryptedMP4, expectedDecryptedMP4).
func encryptSilentMP4(t *testing.T, key []byte, opts testEncOpts) ([]byte, []byte) {
	t.Helper()
	if opts.scheme == "" {
		opts.scheme = "cenc"
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "silent.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	top, err := parseBoxes(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ftypBytes, mdatPayload, moovRaw []byte
	var oldMdatDataOffset uint64
	for _, b := range top {
		switch b.typ {
		case fourCC("ftyp"):
			ftypBytes = append([]byte(nil), raw[b.offset:b.end()]...)
		case fourCC("mdat"):
			oldMdatDataOffset = b.payloadOffset()
			mdatPayload = append([]byte(nil), raw[b.payloadOffset():b.end()]...)
		case fourCC("moov"):
			moovRaw = append([]byte(nil), raw[b.offset:b.end()]...)
		}
	}

	// Parse silent.mp4's stbl to extract sample sizes, stsc, and chunk offsets.
	moovBox, err := parseBoxHeader(moovRaw, 0, uint64(len(moovRaw)))
	if err != nil {
		t.Fatal(err)
	}
	var sampleSizes []uint32
	var stscEntries []stscEntry
	var origChunkOffsets []uint64

	moovKids, _ := parseBoxes(moovRaw[moovBox.headerSize:], moovBox.headerSize)
	for _, mk := range moovKids {
		if mk.typ != fourCC("trak") {
			continue
		}
		tkKids, _ := parseBoxes(mk.payload(moovRaw, 0), mk.payloadOffset())
		for _, tk := range tkKids {
			if tk.typ != fourCC("mdia") {
				continue
			}
			mdKids, _ := parseBoxes(tk.payload(moovRaw, 0), tk.payloadOffset())
			for _, md := range mdKids {
				if md.typ != fourCC("minf") {
					continue
				}
				miKids, _ := parseBoxes(md.payload(moovRaw, 0), md.payloadOffset())
				for _, mi := range miKids {
					if mi.typ != fourCC("stbl") {
						continue
					}
					sbKids, _ := parseBoxes(mi.payload(moovRaw, 0), mi.payloadOffset())
					for _, sb := range sbKids {
						switch sb.typ {
						case fourCC("stsz"):
							sampleSizes, err = parseStsz(sb.payload(moovRaw, 0))
							if err != nil {
								t.Fatal(err)
							}
						case fourCC("stsc"):
							stscEntries, err = parseStsc(sb.payload(moovRaw, 0))
							if err != nil {
								t.Fatal(err)
							}
						case fourCC("stco"):
							origChunkOffsets, err = parseStco(sb.payload(moovRaw, 0))
							if err != nil {
								t.Fatal(err)
							}
						}
					}
				}
			}
		}
	}

	origSamples, _ := buildProgressiveSamples(sampleSizes, origChunkOffsets, stscEntries)
	if len(origSamples) == 0 {
		t.Fatal("silent.mp4 has no samples")
	}

	// Build deterministic per-sample IVs and optional subsamples.
	entries := make([]sencEntry, len(origSamples))
	for i, s := range origSamples {
		var iv [16]byte
		if opts.ivSize == 16 {
			binary.BigEndian.PutUint64(iv[0:8], uint64(0x0102030405060700)+uint64(i))
			// Exercise 128-bit counter carry on sample 0!
			if i == 0 {
				binary.BigEndian.PutUint64(iv[8:16], ^uint64(0))
			} else {
				binary.BigEndian.PutUint64(iv[8:16], uint64(i*7+3))
			}
		} else {
			binary.BigEndian.PutUint64(iv[0:8], uint64(0xaabbccdd00000000)+uint64(i))
		}
		entries[i].iv = iv
		if opts.useSubsamples && s.size > 12 {
			// Split into 2 subsamples with non-16-byte-aligned protected sizes to stress CTR continuity.
			c1 := uint32(5)
			p1 := uint32(7)
			if uint64(c1+p1+3) < s.size {
				c2 := uint32(3)
				p2 := uint32(s.size) - c1 - p1 - c2
				entries[i].subsamples = []cencSubsample{
					{clearBytes: c1, protectedBytes: p1},
					{clearBytes: c2, protectedBytes: p2},
				}
			} else {
				entries[i].subsamples = []cencSubsample{
					{clearBytes: 4, protectedBytes: uint32(s.size) - 4},
				}
			}
		}
	}

	// Encrypt a copy of mdat payload (and optionally append aux info inside mdat for saio+saiz tests).
	encMdatPayload := append([]byte(nil), mdatPayload...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range origSamples {
		rel := s.offset - oldMdatDataOffset
		encryptSampleInPlace(block, encMdatPayload[rel:rel+s.size], entries[i])
	}

	var auxBlob []byte
	var auxSizes []uint8
	auxRelInMdat := uint64(len(encMdatPayload))
	if opts.useSAIO {
		auxBlob, auxSizes = encodeAuxData(entries, opts.ivSize)
		encMdatPayload = append(encMdatPayload, auxBlob...)
		mdatPayload = append(mdatPayload, auxBlob...)
	}

	buildMoov := func(newMdatDataOffset uint64, decryptedExpected bool) []byte {
		var rebuild func(buf []byte, typ [4]byte) []byte
		rebuild = func(buf []byte, typ [4]byte) []byte {
			kids, err := parseBoxes(buf[8:], 8)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			for _, k := range kids {
				rawKid := buf[k.offset:k.end()]
				switch k.typ {
				case fourCC("trak"), fourCC("mdia"), fourCC("minf"):
					out.Write(rebuild(rawKid, k.typ))
				case fourCC("stbl"):
					out.Write(rebuild(rawKid, k.typ))
				case fourCC("stsd"):
					// Transform avc1 -> encv + sinf (or avc1 + free for expected).
					stsdData := rawKid[8:]
					entryBoxes, err := parseBoxes(stsdData[8:], 16)
					if err != nil {
						t.Fatal(err)
					}
					var stsdBody bytes.Buffer
					stsdBody.Write(stsdData[:8]) // version+flags+entry_count
					for _, eb := range entryBoxes {
						ebRaw := append([]byte(nil), rawKid[eb.offset:eb.end()]...)
						sinf := makeSinfBox(string(eb.typ[:]), opts.scheme, byte(opts.ivSize), nil)
						if decryptedExpected {
							// Expected decrypted: original avc1 + sinf turned to free(zeroed).
							freeBox := make([]byte, len(sinf))
							binary.BigEndian.PutUint32(freeBox[0:4], uint32(len(sinf)))
							copy(freeBox[4:8], "free")
							ebRaw = append(ebRaw, freeBox...)
						} else {
							copy(ebRaw[4:8], "encv")
							ebRaw = append(ebRaw, sinf...)
						}
						binary.BigEndian.PutUint32(ebRaw[0:4], uint32(len(ebRaw)))
						stsdBody.Write(ebRaw)
					}
					out.Write(makeBox("stsd", stsdBody.Bytes()))
				case fourCC("stco"):
					// Adjust chunk offsets to new mdat payload offset.
					delta := int64(newMdatDataOffset) - int64(oldMdatDataOffset)
					var body bytes.Buffer
					var cnt [4]byte
					binary.BigEndian.PutUint32(cnt[:], uint32(len(origChunkOffsets)))
					body.Write(cnt[:])
					for _, co := range origChunkOffsets {
						var v [4]byte
						binary.BigEndian.PutUint32(v[:], uint32(int64(co)+delta))
						body.Write(v[:])
					}
					out.Write(makeFullBox("stco", 0, 0, body.Bytes()))
				default:
					out.Write(rawKid)
				}
			}
			if typ == fourCC("stbl") {
				if opts.useSAIO {
					saiz := makeSaizBox(auxSizes)
					saio := makeSaioBox(newMdatDataOffset + auxRelInMdat)
					if decryptedExpected {
						for _, b := range [][]byte{saiz, saio} {
							fb := make([]byte, len(b))
							binary.BigEndian.PutUint32(fb[0:4], uint32(len(b)))
							copy(fb[4:8], "free")
							out.Write(fb)
						}
					} else {
						out.Write(saiz)
						out.Write(saio)
					}
				} else {
					senc := makeSencBox(entries, opts.ivSize, opts.useSubsamples)
					if decryptedExpected {
						fb := make([]byte, len(senc))
						binary.BigEndian.PutUint32(fb[0:4], uint32(len(senc)))
						copy(fb[4:8], "free")
						out.Write(fb)
					} else {
						out.Write(senc)
					}
				}
			}
			if typ == fourCC("moov") {
				pssh := makeFullBox("pssh", 0, 0, make([]byte, 20))
				if decryptedExpected {
					fb := make([]byte, len(pssh))
					binary.BigEndian.PutUint32(fb[0:4], uint32(len(pssh)))
					copy(fb[4:8], "free")
					out.Write(fb)
				} else {
					out.Write(pssh)
				}
			}
			return makeBox(string(typ[:]), out.Bytes())
		}
		return rebuild(moovRaw, fourCC("moov"))
	}

	mdatEnc := makeBox("mdat", encMdatPayload)
	mdatClear := makeBox("mdat", mdatPayload)

	var newMdatDataOffset uint64
	if opts.moovAtStart {
		// Measure moov size first with dummy offset (size is invariant to offset values).
		probeMoov := buildMoov(0, false)
		newMdatDataOffset = uint64(len(ftypBytes)) + uint64(len(probeMoov)) + 8
	} else {
		newMdatDataOffset = uint64(len(ftypBytes)) + 8
	}

	encMoov := buildMoov(newMdatDataOffset, false)
	expMoov := buildMoov(newMdatDataOffset, true)

	var encOut, expOut bytes.Buffer
	encOut.Write(ftypBytes)
	expOut.Write(ftypBytes)
	if opts.moovAtStart {
		encOut.Write(encMoov)
		encOut.Write(mdatEnc)
		expOut.Write(expMoov)
		expOut.Write(mdatClear)
	} else {
		encOut.Write(mdatEnc)
		encOut.Write(encMoov)
		expOut.Write(mdatClear)
		expOut.Write(expMoov)
	}
	return encOut.Bytes(), expOut.Bytes()
}

// buildSyntheticFragmentedMP4 constructs a fragmented MP4 (ftyp + moov + optional sidx + 2 moof/mdat fragments)
// with two tracks (encrypted video track 1 + clear audio track 2) and returns (encrypted, expectedDecrypted).
func buildSyntheticFragmentedMP4(t *testing.T, key []byte, withSIDX bool, useSAIO bool) ([]byte, []byte) {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	ftyp := makeBox("ftyp", []byte("isom\x00\x00\x02\x00isomiso6mp41"))

	makeEmptyTrak := func(trackID uint32, sampleEntry []byte) []byte {
		tkhdBody := make([]byte, 80)
		binary.BigEndian.PutUint32(tkhdBody[8:12], trackID)
		tkhd := makeFullBox("tkhd", 0, 3, tkhdBody)

		stsdBody := make([]byte, 4+len(sampleEntry))
		binary.BigEndian.PutUint32(stsdBody[0:4], 1)
		copy(stsdBody[4:], sampleEntry)
		stsd := makeFullBox("stsd", 0, 0, stsdBody)
		stsz := makeFullBox("stsz", 0, 0, make([]byte, 8))
		stsc := makeFullBox("stsc", 0, 0, make([]byte, 4))
		stco := makeFullBox("stco", 0, 0, make([]byte, 4))

		var stbl bytes.Buffer
		stbl.Write(stsd)
		stbl.Write(stsz)
		stbl.Write(stsc)
		stbl.Write(stco)
		minf := makeBox("minf", makeBox("stbl", stbl.Bytes()))
		mdia := makeBox("mdia", minf)
		return makeBox("trak", append(tkhd, mdia...))
	}

	makeVisualEntry := func(typ string, extra []byte) []byte {
		body := make([]byte, 78+len(extra))
		binary.BigEndian.PutUint16(body[6:8], 1)   // data_ref_idx
		binary.BigEndian.PutUint16(body[24:26], 160)
		binary.BigEndian.PutUint16(body[26:28], 90)
		copy(body[78:], extra)
		return makeBox(typ, body)
	}
	makeAudioEntry := func(typ string) []byte {
		body := make([]byte, 28)
		binary.BigEndian.PutUint16(body[6:8], 1)
		return makeBox(typ, body)
	}

	sinf := makeSinfBox("avc1", "cenc", 8, nil)
	freeSinf := make([]byte, len(sinf))
	binary.BigEndian.PutUint32(freeSinf[0:4], uint32(len(sinf)))
	copy(freeSinf[4:8], "free")

	makeTrex := func(trackID uint32) []byte {
		b := make([]byte, 20)
		binary.BigEndian.PutUint32(b[0:4], trackID)
		binary.BigEndian.PutUint32(b[4:8], 1) // default_sample_description_index
		return makeFullBox("trex", 0, 0, b)
	}
	mvex := makeBox("mvex", append(makeTrex(1), makeTrex(2)...))
	mvhd := makeFullBox("mvhd", 0, 0, make([]byte, 96))

	buildMoov := func(expected bool) []byte {
		var ventry []byte
		if expected {
			ventry = makeVisualEntry("avc1", freeSinf)
		} else {
			ventry = makeVisualEntry("encv", sinf)
		}
		var body bytes.Buffer
		body.Write(mvhd)
		body.Write(makeEmptyTrak(1, ventry))
		body.Write(makeEmptyTrak(2, makeAudioEntry("mp4a")))
		body.Write(mvex)
		return makeBox("moov", body.Bytes())
	}

	encMoov := buildMoov(false)
	expMoov := buildMoov(true)

	buildFragment := func(fragIdx int) (encFrag, expFrag []byte) {
		// Video: 3 samples with subsample encryption; Audio: 2 clear samples.
		vClear := [][]byte{
			bytes.Repeat([]byte{byte(0x21 + fragIdx)}, 53),
			bytes.Repeat([]byte{byte(0x31 + fragIdx)}, 87),
			bytes.Repeat([]byte{byte(0x41 + fragIdx)}, 41),
		}
		aClear := [][]byte{
			bytes.Repeat([]byte{byte(0x71 + fragIdx)}, 32),
			bytes.Repeat([]byte{byte(0x81 + fragIdx)}, 24),
		}
		entries := make([]sencEntry, len(vClear))
		vEnc := make([][]byte, len(vClear))
		for i, s := range vClear {
			var iv [16]byte
			binary.BigEndian.PutUint64(iv[0:8], uint64(0xfeed0000+fragIdx*100+i))
			entries[i] = sencEntry{
				iv: iv,
				subsamples: []cencSubsample{
					{clearBytes: 6, protectedBytes: 11},
					{clearBytes: 4, protectedBytes: uint32(len(s) - 21)},
				},
			}
			vEnc[i] = append([]byte(nil), s...)
			encryptSampleInPlace(block, vEnc[i], entries[i])
		}

		// Assemble mdat payload: [v0, v1, v2, a0, a1, (optional auxBlob)]
		var encMdatBody, clearMdatBody bytes.Buffer
		for i := range vClear {
			encMdatBody.Write(vEnc[i])
			clearMdatBody.Write(vClear[i])
		}
		audioStartInMdat := encMdatBody.Len()
		for i := range aClear {
			encMdatBody.Write(aClear[i])
			clearMdatBody.Write(aClear[i])
		}
		auxRelInMdat := encMdatBody.Len()
		var auxSizes []uint8
		if useSAIO {
			var auxBlob []byte
			auxBlob, auxSizes = encodeAuxData(entries, 8)
			encMdatBody.Write(auxBlob)
			clearMdatBody.Write(auxBlob)
		}

		encMdat := makeBox("mdat", encMdatBody.Bytes())
		clearMdat := makeBox("mdat", clearMdatBody.Bytes())

		buildMoof := func(dataOffV, dataOffA, saioRel uint32, expected bool) []byte {
			mfhd := makeFullBox("mfhd", 0, 0, binary.BigEndian.AppendUint32(nil, uint32(fragIdx+1)))

			// Video traf (tfhd with default-base-is-moof = 0x020000)
			tfhdV := makeFullBox("tfhd", 0, 0x020000, binary.BigEndian.AppendUint32(nil, 1))
			var trunVBody bytes.Buffer
			trunVBody.Write(binary.BigEndian.AppendUint32(nil, uint32(len(vClear))))
			trunVBody.Write(binary.BigEndian.AppendUint32(nil, dataOffV))
			for _, s := range vClear {
				trunVBody.Write(binary.BigEndian.AppendUint32(nil, uint32(len(s))))
			}
			trunV := makeFullBox("trun", 0, 0x000201, trunVBody.Bytes()) // data-offset + sample-size

			var trafVBody bytes.Buffer
			trafVBody.Write(tfhdV)
			trafVBody.Write(trunV)
			if useSAIO {
				saiz := makeSaizBox(auxSizes)
				saio := makeSaioBox(uint64(saioRel))
				if expected {
					for _, b := range [][]byte{saiz, saio} {
						fb := make([]byte, len(b))
						binary.BigEndian.PutUint32(fb[0:4], uint32(len(b)))
						copy(fb[4:8], "free")
						trafVBody.Write(fb)
					}
				} else {
					trafVBody.Write(saiz)
					trafVBody.Write(saio)
				}
			} else {
				senc := makeSencBox(entries, 8, true)
				if expected {
					fb := make([]byte, len(senc))
					binary.BigEndian.PutUint32(fb[0:4], uint32(len(senc)))
					copy(fb[4:8], "free")
					trafVBody.Write(fb)
				} else {
					trafVBody.Write(senc)
				}
			}
			trafV := makeBox("traf", trafVBody.Bytes())

			// Clear Audio traf
			tfhdA := makeFullBox("tfhd", 0, 0x020000, binary.BigEndian.AppendUint32(nil, 2))
			var trunABody bytes.Buffer
			trunABody.Write(binary.BigEndian.AppendUint32(nil, uint32(len(aClear))))
			trunABody.Write(binary.BigEndian.AppendUint32(nil, dataOffA))
			for _, s := range aClear {
				trunABody.Write(binary.BigEndian.AppendUint32(nil, uint32(len(s))))
			}
			trunA := makeFullBox("trun", 0, 0x000201, trunABody.Bytes())
			trafA := makeBox("traf", append(tfhdA, trunA...))

			var moofBody bytes.Buffer
			moofBody.Write(mfhd)
			moofBody.Write(trafV)
			moofBody.Write(trafA)
			return makeBox("moof", moofBody.Bytes())
		}

		// Two-pass to resolve exact moof size -> data_offset into mdat.
		probe := buildMoof(0, 0, 0, false)
		moofSize := uint32(len(probe))
		dataOffV := moofSize + 8
		dataOffA := dataOffV + uint32(audioStartInMdat)
		saioRel := dataOffV + uint32(auxRelInMdat)

		encMoof := buildMoof(dataOffV, dataOffA, saioRel, false)
		expMoof := buildMoof(dataOffV, dataOffA, saioRel, true)
		return append(encMoof, encMdat...), append(expMoof, clearMdat...)
	}

	encF0, expF0 := buildFragment(0)
	encF1, expF1 := buildFragment(1)

	var sidxBox []byte
	if withSIDX {
		var body bytes.Buffer
		body.Write(binary.BigEndian.AppendUint32(nil, 1))     // reference_ID
		body.Write(binary.BigEndian.AppendUint32(nil, 1000))  // timescale
		body.Write(binary.BigEndian.AppendUint32(nil, 0))     // earliest_presentation_time
		body.Write(binary.BigEndian.AppendUint32(nil, 0))     // first_offset
		body.Write([]byte{0, 0, 0, 2})                        // reserved + reference_count = 2
		for _, sz := range []int{len(encF0), len(encF1)} {
			body.Write(binary.BigEndian.AppendUint32(nil, uint32(sz)))
			body.Write(binary.BigEndian.AppendUint32(nil, 1000)) // subsegment_duration
			body.Write(binary.BigEndian.AppendUint32(nil, 0x80000000))
		}
		sidxBox = makeFullBox("sidx", 0, 0, body.Bytes())
	}

	var encAll, expAll bytes.Buffer
	encAll.Write(ftyp)
	encAll.Write(encMoov)
	expAll.Write(ftyp)
	expAll.Write(expMoov)
	if len(sidxBox) > 0 {
		encAll.Write(sidxBox)
		expAll.Write(sidxBox)
	}
	encAll.Write(encF0)
	encAll.Write(encF1)
	expAll.Write(expF0)
	expAll.Write(expF1)
	return encAll.Bytes(), expAll.Bytes()
}

func assertProxyDecryptMatches(t *testing.T, encMP4, wantMP4, key []byte) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://hongguo.test/" {
			http.Error(w, "missing referer", http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "drama.mp4", time.Time{}, bytes.NewReader(encMP4))
	}))
	defer srv.Close()

	pu, release, err := localProxy.registerOptions(context.Background(), srv.URL+"/drama.mp4", StreamOptions{
		Headers:    map[string]string{"Referer": "https://hongguo.test/"},
		DecryptKey: key,
	}, true)
	if err != nil {
		t.Fatalf("registerOptions failed: %v", err)
	}
	defer release()

	// 1. HEAD request
	hresp, err := http.Head(pu)
	if err != nil {
		t.Fatal(err)
	}
	hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK || hresp.ContentLength != int64(len(wantMP4)) ||
		hresp.Header.Get("Accept-Ranges") != "bytes" || hresp.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("HEAD mismatch: status=%d len=%d ar=%q ct=%q",
			hresp.StatusCode, hresp.ContentLength, hresp.Header.Get("Accept-Ranges"), hresp.Header.Get("Content-Type"))
	}

	// 2. Full GET
	resp, err := http.Get(pu)
	if err != nil {
		t.Fatal(err)
	}
	gotFull, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("full GET status=%d", resp.StatusCode)
	}
	if !bytes.Equal(gotFull, wantMP4) {
		for i := range min(len(gotFull), len(wantMP4)) {
			if gotFull[i] != wantMP4[i] {
				t.Fatalf("full GET mismatch at byte %d (got len=%d want len=%d): got=%x want=%x",
					i, len(gotFull), len(wantMP4), gotFull[i:min(i+16, len(gotFull))], wantMP4[i:min(i+16, len(wantMP4))])
			}
		}
		t.Fatalf("full GET length mismatch: got=%d want=%d", len(gotFull), len(wantMP4))
	}

	// 3. Arbitrary Range requests crossing header, sample, subsample, and 16-byte AES block boundaries.
	total := len(wantMP4)
	ranges := []struct {
		hdr   string
		start int
		end   int
	}{
		{"bytes=0-1", 0, 1},
		{"bytes=0-63", 0, 63},
		{"bytes=17-129", 17, 129},
		{fmt.Sprintf("bytes=%d-%d", total/3+3, total/2+19), total/3 + 3, total/2 + 19},
		{fmt.Sprintf("bytes=%d-%d", total/2-7, total-1), total/2 - 7, total - 1},
		{fmt.Sprintf("bytes=%d-", total-111), total - 111, total - 1},
		{"bytes=-97", total - 97, total - 1},
		{fmt.Sprintf("bytes=%d-%d", total-10, total+500), total - 10, total - 1}, // clamp end
	}
	for _, tc := range ranges {
		req, _ := http.NewRequest(http.MethodGet, pu, nil)
		req.Header.Set("Range", tc.hdr)
		rresp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("range %s: %v", tc.hdr, err)
		}
		got, err := io.ReadAll(rresp.Body)
		rresp.Body.Close()
		if err != nil {
			t.Fatalf("range %s read: %v", tc.hdr, err)
		}
		wantCR := fmt.Sprintf("bytes %d-%d/%d", tc.start, tc.end, total)
		if rresp.StatusCode != http.StatusPartialContent || rresp.Header.Get("Content-Range") != wantCR {
			t.Fatalf("range %s: status=%d CR=%q want=%q", tc.hdr, rresp.StatusCode, rresp.Header.Get("Content-Range"), wantCR)
		}
		wantSlice := wantMP4[tc.start : tc.end+1]
		if !bytes.Equal(got, wantSlice) {
			t.Fatalf("range %s body mismatch (len %d vs %d)", tc.hdr, len(got), len(wantSlice))
		}
	}

	// 4. Unsatisfiable range returns 416.
	badReq, _ := http.NewRequest(http.MethodGet, pu, nil)
	badReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", total+10, total+20))
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected 416, got %d", badResp.StatusCode)
	}
}

func TestCENCProgressiveVariants(t *testing.T) {
	key := []byte("0123456789abcdef")
	cases := []struct {
		name string
		opts testEncOpts
	}{
		{"8byteIV_fullSample_moovEnd", testEncOpts{ivSize: 8, useSubsamples: false, moovAtStart: false}},
		{"8byteIV_subsample_moovStart", testEncOpts{ivSize: 8, useSubsamples: true, moovAtStart: true}},
		{"16byteIV_subsample_moovEnd", testEncOpts{ivSize: 16, useSubsamples: true, moovAtStart: false}},
		{"8byteIV_saio_saiz_moovStart", testEncOpts{ivSize: 8, useSubsamples: true, useSAIO: true, moovAtStart: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, want := encryptSilentMP4(t, key, tc.opts)
			assertProxyDecryptMatches(t, enc, want, key)
		})
	}
}

func TestCENCFragmentedMP4(t *testing.T) {
	key := []byte("fedcba9876543210")
	cases := []struct {
		name     string
		withSIDX bool
		useSAIO  bool
	}{
		{"noSIDX_senc", false, false},
		{"withSIDX_senc", true, false},
		{"withSIDX_saio_saiz", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, want := buildSyntheticFragmentedMP4(t, key, tc.withSIDX, tc.useSAIO)
			// Test seeking directly into fragment 2 BEFORE any full GET so lazy moof parsing is exercised.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.ServeContent(w, r, "frag.mp4", time.Time{}, bytes.NewReader(enc))
			}))
			defer srv.Close()

			pu, release, err := localProxy.registerOptions(context.Background(), srv.URL+"/frag.mp4", StreamOptions{DecryptKey: key}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			tailStart := len(want) - 120
			req, _ := http.NewRequest(http.MethodGet, pu, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", tailStart, len(want)-1))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			gotTail, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(gotTail, want[tailStart:]) {
				t.Fatalf("lazy seek into fragment 2 failed: status=%d len=%d", resp.StatusCode, len(gotTail))
			}

			// Now run full + multi-range matrix.
			assertProxyDecryptMatches(t, enc, want, key)
		})
	}
}

func TestCENCValidationErrors(t *testing.T) {
	key := []byte("0123456789abcdef")

	// Invalid key lengths must fail immediately.
	for _, badLen := range []int{1, 8, 15, 17, 32} {
		badKey := make([]byte, badLen)
		if _, _, err := localProxy.registerOptions(context.Background(), "http://127.0.0.1/a.mp4", StreamOptions{DecryptKey: badKey}, true); err == nil {
			t.Fatalf("registerOptions accepted key length %d", badLen)
		}
		if _, err := OpenVideoWithOptions(t.Context(), "http://127.0.0.1/a.mp4", StreamOptions{DecryptKey: badKey}); err == nil {
			t.Fatalf("OpenVideoWithOptions accepted key length %d", badLen)
		}
		if _, err := OpenNativeWithOptions(t.Context(), "http://127.0.0.1/a.mp4", StreamOptions{DecryptKey: badKey}, true); err == nil {
			t.Fatalf("OpenNativeWithOptions accepted key length %d", badLen)
		}
	}

	// Unsupported scheme (cbcs) must fail cleanly during registration.
	cbcsMP4, _ := encryptSilentMP4(t, key, testEncOpts{ivSize: 8, scheme: "cbcs"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "cbcs.mp4", time.Time{}, bytes.NewReader(cbcsMP4))
	}))
	defer srv.Close()

	if _, _, err := localProxy.registerOptions(context.Background(), srv.URL+"/cbcs.mp4", StreamOptions{DecryptKey: key}, true); err == nil {
		t.Fatal("expected error for unsupported cbcs scheme")
	}
}

// TestCENCLargeMultiTrackStreaming exercises the non-cached upstream Range streaming
// path (>64 KiB probe window, >32 KiB stream chunks) with moov-at-end and multiple
// tracks (encrypted video encv->hvc1 + encrypted audio enca->mp4a + clear track).
func TestCENCLargeMultiTrackStreaming(t *testing.T) {
	key := []byte("0123456789abcdef")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	ftyp := makeBox("ftyp", []byte("isom\x00\x00\x02\x00isommp41"))

	// Build >1.3 MiB of sample payloads across 3 tracks so mdat far exceeds cencProbeSize (512 KiB).
	const (
		vSamples = 8
		vSize    = 160 << 10 // 1280 KiB encrypted video
		aSamples = 8
		aSize    = 4 << 10 // 32 KiB encrypted audio
		cSamples = 4
		cSize    = 8 << 10 // 32 KiB clear track
	)
	var clearMdat, encMdat bytes.Buffer
	mdatDataStart := uint64(len(ftyp) + 8)

	vOff := mdatDataStart
	vEntries := make([]sencEntry, vSamples)
	for i := range vSamples {
		sample := bytes.Repeat([]byte{byte(0x10 + i)}, vSize)
		clearMdat.Write(sample)
		var iv [16]byte
		binary.BigEndian.PutUint64(iv[0:8], uint64(0x11110000+i))
		vEntries[i] = sencEntry{
			iv: iv,
			subsamples: []cencSubsample{
				{clearBytes: 12, protectedBytes: 1003},
				{clearBytes: 7, protectedBytes: uint32(vSize - 1022)},
			},
		}
		enc := append([]byte(nil), sample...)
		encryptSampleInPlace(block, enc, vEntries[i])
		encMdat.Write(enc)
	}

	aOff := mdatDataStart + uint64(clearMdat.Len())
	aEntries := make([]sencEntry, aSamples)
	for i := range aSamples {
		sample := bytes.Repeat([]byte{byte(0x60 + i)}, aSize)
		clearMdat.Write(sample)
		var iv [16]byte
		binary.BigEndian.PutUint64(iv[0:8], uint64(0x22220000+i))
		aEntries[i] = sencEntry{iv: iv}
		enc := append([]byte(nil), sample...)
		encryptSampleInPlace(block, enc, aEntries[i])
		encMdat.Write(enc)
	}

	cOff := mdatDataStart + uint64(clearMdat.Len())
	for i := range cSamples {
		sample := bytes.Repeat([]byte{byte(0xa0 + i)}, cSize)
		clearMdat.Write(sample)
		encMdat.Write(sample)
	}

	makeStsz := func(n, sz int) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint32(b[0:4], uint32(sz))
		binary.BigEndian.PutUint32(b[4:8], uint32(n))
		return makeFullBox("stsz", 0, 0, b)
	}
	makeStsc := func(n int) []byte {
		b := make([]byte, 16)
		binary.BigEndian.PutUint32(b[0:4], 1)
		binary.BigEndian.PutUint32(b[4:8], 1)
		binary.BigEndian.PutUint32(b[8:12], uint32(n))
		binary.BigEndian.PutUint32(b[12:16], 1)
		return makeFullBox("stsc", 0, 0, b)
	}
	makeStco := func(off uint64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint32(b[0:4], 1)
		binary.BigEndian.PutUint32(b[4:8], uint32(off))
		return makeFullBox("stco", 0, 0, b)
	}
	makeTrak := func(id uint32, entry, stsz, stsc, stco, senc []byte) []byte {
		tkhdBody := make([]byte, 80)
		binary.BigEndian.PutUint32(tkhdBody[8:12], id)
		tkhd := makeFullBox("tkhd", 0, 3, tkhdBody)
		stsdBody := make([]byte, 4+len(entry))
		binary.BigEndian.PutUint32(stsdBody[0:4], 1)
		copy(stsdBody[4:], entry)
		var stbl bytes.Buffer
		stbl.Write(makeFullBox("stsd", 0, 0, stsdBody))
		stbl.Write(stsz)
		stbl.Write(stsc)
		stbl.Write(stco)
		if len(senc) > 0 {
			stbl.Write(senc)
		}
		return makeBox("trak", append(tkhd, makeBox("mdia", makeBox("minf", makeBox("stbl", stbl.Bytes())))...))
	}

	vSinf := makeSinfBox("hvc1", "cenc", 8, nil)
	aSinf := makeSinfBox("mp4a", "cenc", 8, nil)
	vSenc := makeSencBox(vEntries, 8, true)
	aSenc := makeSencBox(aEntries, 8, false)
	toFree := func(b []byte) []byte {
		fb := make([]byte, len(b))
		binary.BigEndian.PutUint32(fb[0:4], uint32(len(b)))
		copy(fb[4:8], "free")
		return fb
	}

	makeVis := func(typ string, extra []byte) []byte {
		body := make([]byte, 78+len(extra))
		copy(body[78:], extra)
		return makeBox(typ, body)
	}
	makeAud := func(typ string, extra []byte) []byte {
		body := make([]byte, 28+len(extra))
		copy(body[28:], extra)
		return makeBox(typ, body)
	}

	buildMoov := func(expected bool) []byte {
		var body bytes.Buffer
		body.Write(makeFullBox("mvhd", 0, 0, make([]byte, 96)))
		if expected {
			body.Write(makeTrak(1, makeVis("hvc1", toFree(vSinf)), makeStsz(vSamples, vSize), makeStsc(vSamples), makeStco(vOff), toFree(vSenc)))
			body.Write(makeTrak(2, makeAud("mp4a", toFree(aSinf)), makeStsz(aSamples, aSize), makeStsc(aSamples), makeStco(aOff), toFree(aSenc)))
		} else {
			body.Write(makeTrak(1, makeVis("encv", vSinf), makeStsz(vSamples, vSize), makeStsc(vSamples), makeStco(vOff), vSenc))
			body.Write(makeTrak(2, makeAud("enca", aSinf), makeStsz(aSamples, aSize), makeStsc(aSamples), makeStco(aOff), aSenc))
		}
		body.Write(makeTrak(3, makeAud("mp4a", nil), makeStsz(cSamples, cSize), makeStsc(cSamples), makeStco(cOff), nil))
		return makeBox("moov", body.Bytes())
	}

	var encMP4, wantMP4 bytes.Buffer
	encMP4.Write(ftyp)
	encMP4.Write(makeBox("mdat", encMdat.Bytes()))
	encMP4.Write(buildMoov(false))

	wantMP4.Write(ftyp)
	wantMP4.Write(makeBox("mdat", clearMdat.Bytes()))
	wantMP4.Write(buildMoov(true))

	// Also verify registration reads only bounded probe + moov (far less than full 192 KiB file).
	var bytesServed atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &countingResponseWriter{ResponseWriter: w, n: &bytesServed}
		http.ServeContent(cw, r, "big.mp4", time.Time{}, bytes.NewReader(encMP4.Bytes()))
	}))
	defer srv.Close()

	_, rel, err := localProxy.registerOptions(context.Background(), srv.URL+"/big.mp4", StreamOptions{DecryptKey: key}, true)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if got := bytesServed.Load(); got >= int64(encMP4.Len()/2) {
		t.Fatalf("registration downloaded too much data: %d / %d bytes", got, encMP4.Len())
	}

	assertProxyDecryptMatches(t, encMP4.Bytes(), wantMP4.Bytes(), key)
}

type countingResponseWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (w *countingResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.n.Add(int64(n))
	return n, err
}

func TestCENCAVFoundationPlayback(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("AVFoundation playback test runs on macOS")
	}
	key := []byte("hongguo-cenc-key")

	for _, tc := range []struct {
		name string
		opts testEncOpts
	}{
		{"fullSample_8byteIV_moovEnd", testEncOpts{ivSize: 8, useSubsamples: false, moovAtStart: false}},
		{"subsample_16byteIV_moovStart", testEncOpts{ivSize: 16, useSubsamples: true, moovAtStart: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encMP4, _ := encryptSilentMP4(t, key, tc.opts)
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Referer") != "https://hongguo.test/" {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				hits.Add(1)
				http.ServeContent(w, r, "enc.mp4", time.Time{}, bytes.NewReader(encMP4))
			}))
			defer srv.Close()

			opts := StreamOptions{
				Headers:    map[string]string{"Referer": "https://hongguo.test/"},
				DecryptKey: key,
			}

			// Pixel output path (OpenVideoWithOptions + Next)
			live, err := OpenVideoWithOptions(t.Context(), srv.URL+"/enc.mp4", opts)
			if err != nil {
				t.Fatalf("OpenVideoWithOptions: %v", err)
			}
			frame, err := live.Next()
			if err != nil {
				live.Close()
				t.Fatalf("Next() on encrypted MP4: %v", err)
			}
			b := frame.Image.Bounds()
			if b.Dx() != 160 || b.Dy() != 90 {
				live.Close()
				t.Fatalf("unexpected decoded frame size: %v", b)
			}
			live.Close()

			// Native layer path (OpenNativeWithOptions + WaitReady)
			nat, err := OpenNativeWithOptions(t.Context(), srv.URL+"/enc.mp4", opts, true)
			if err != nil {
				t.Fatalf("OpenNativeWithOptions: %v", err)
			}
			w, h, err := nat.WaitReady()
			nat.Close()
			if err != nil || w != 160 || h != 90 {
				t.Fatalf("WaitReady on encrypted MP4: %dx%d err=%v", w, h, err)
			}
			if hits.Load() == 0 {
				t.Fatal("upstream server received no requests")
			}
		})
	}
}

func BenchmarkCENCDecryptRange(b *testing.B) {
	const totalBytes = 4 << 20 // 4 MiB
	const sampleSize = 16 << 10
	key := []byte("0123456789abcdef")
	block, err := aes.NewCipher(key)
	if err != nil {
		b.Fatal(err)
	}

	var spans []cencSpan
	for off := uint64(0); off < totalBytes; off += sampleSize {
		var iv [16]byte
		binary.BigEndian.PutUint64(iv[0:8], off)
		// Realistic subsample layout: 16 clear bytes + remaining protected bytes per 16 KiB sample.
		spans = append(spans, cencSpan{
			fileStart:         off + 16,
			fileEnd:           off + sampleSize,
			iv:                iv,
			encOffsetInSample: 0,
		})
	}
	plan := &cencPlan{
		block: block,
		spans: spans,
	}
	buf := make([]byte, totalBytes)

	b.SetBytes(totalBytes)
	b.ResetTimer()
	for range b.N {
		plan.transformChunk(buf, 0)
	}
}
