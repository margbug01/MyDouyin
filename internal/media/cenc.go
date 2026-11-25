package media

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	cencProbeSize    = 512 << 10 // 512 KiB initial HTTP Range probe (covers typical moov + opening frames in 1 RTT)
	cencMaxMoovBytes = 64 << 20  // 64 MiB safety cap for moov
	cencMaxMoofBytes = 16 << 20  // 16 MiB safety cap for a single moof
	cencMaxAuxBytes  = 16 << 20  // 16 MiB safety cap for saio aux info
)

// PIFF SampleEncryptionBox UUID: a2394f52-5a9b-4f14-a244-6c427c648df4
var piffSencUUID = [16]byte{
	0xa2, 0x39, 0x4f, 0x52, 0x5a, 0x9b, 0x4f, 0x14,
	0xa2, 0x44, 0x6c, 0x42, 0x7c, 0x64, 0x8d, 0xf4,
}

type mp4Box struct {
	typ        [4]byte
	offset     uint64
	size       uint64
	headerSize uint64
	uuid       [16]byte
}

func (b mp4Box) payloadOffset() uint64 { return b.offset + b.headerSize }
func (b mp4Box) end() uint64           { return b.offset + b.size }
func (b mp4Box) payload(buf []byte, baseOffset uint64) []byte {
	start := b.payloadOffset() - baseOffset
	end := b.end() - baseOffset
	return buf[start:end]
}

func fourCC(s string) [4]byte {
	return [4]byte{s[0], s[1], s[2], s[3]}
}

// cencSpan describes one contiguous encrypted byte span in the MP4 file.
type cencSpan struct {
	fileStart         uint64
	fileEnd           uint64
	iv                [16]byte
	encOffsetInSample uint64
}

// bytePatch overwrites bytes at a fixed file offset without changing file length.
type bytePatch struct {
	offset uint64
	data   []byte
}

// cachedBlock holds raw upstream bytes fetched during probing (e.g. initial probe + moov).
type cachedBlock struct {
	offset uint64
	data   []byte
}

// trackEncInfo holds per-stsd-entry encryption defaults parsed from sinf/tenc.
type trackEncInfo struct {
	protected       bool
	perSampleIVSize uint8
	hasConstIV      bool
	constIV         [16]byte
}

type trackMeta struct {
	trackID             uint32
	encEntries          []trackEncInfo // indexed by (sampleDescriptionIndex - 1)
	defaultSampleDescID uint32
	defaultSampleSize   uint32
}

type sampleLoc struct {
	offset  uint64
	size    uint64
	descIdx uint32 // 1-based stsd entry index
}

type cencSubsample struct {
	clearBytes     uint32
	protectedBytes uint32
}

type sencEntry struct {
	iv         [16]byte
	subsamples []cencSubsample
}

type saizInfo struct {
	defaultSize uint8
	sampleCount uint32
	sizes       []uint8
}

type saioInfo struct {
	offsets []uint64
}

type sidxEntry struct {
	offset uint64
	size   uint64
}

// cencPlan holds the parsed encryption index and header patches for a CENC MP4 stream.
type cencPlan struct {
	source    string
	headers   map[string]string
	client    *http.Client
	block     cipher.Block
	totalSize int64

	cached []cachedBlock

	// Immutable for progressive MP4s; guarded by fragMu when fragmented is true.
	fragmented bool
	tracks     map[uint32]*trackMeta

	fragMu      sync.RWMutex
	fragScanPos uint64
	parsedMoofs map[uint64]bool
	sidx        []sidxEntry
	spans       []cencSpan
	patches     []bytePatch
}

func newCENCPlan(ctx context.Context, client *http.Client, source string, headers map[string]string, key []byte) (*cencPlan, error) {
	if len(key) != 16 {
		return nil, errors.New("media: decrypt key must be 16 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	p := &cencPlan{
		source:      source,
		headers:     headers,
		client:      client,
		block:       block,
		tracks:      make(map[uint32]*trackMeta),
		parsedMoofs: make(map[uint64]bool),
	}

	// Probe initial bytes and determine total file size.
	probeData, totalSize, err := p.fetchRange(ctx, 0, cencProbeSize-1)
	if err != nil {
		return nil, err
	}
	if totalSize <= 0 || len(probeData) == 0 {
		return nil, errors.New("media: empty CENC stream")
	}
	p.totalSize = totalSize
	p.addCached(0, probeData)

	// Walk top-level boxes to locate moov (whether at start or end of file).
	var (
		moovBox     mp4Box
		foundMoov   bool
		moovBuf     []byte
		hasMoofSeen bool
		firstMoof   uint64
		pos         uint64
		total       = uint64(totalSize)
	)
	for pos < total {
		box, err := p.readBoxHeaderAt(ctx, pos, total)
		if err != nil {
			return nil, fmt.Errorf("media: parse MP4 top-level box at %d: %w", pos, err)
		}
		if box.size == 0 || box.end() > total {
			return nil, fmt.Errorf("media: invalid MP4 box %q size %d at %d", string(box.typ[:]), box.size, pos)
		}
		switch box.typ {
		case fourCC("moov"):
			if box.size > cencMaxMoovBytes {
				return nil, fmt.Errorf("media: moov box too large (%d bytes)", box.size)
			}
			moovBox = box
			foundMoov = true
			moovBuf, err = p.readBytes(ctx, box.offset, box.size)
			if err != nil {
				return nil, fmt.Errorf("media: read moov: %w", err)
			}
			p.addCached(box.offset, moovBuf)
		case fourCC("sidx"):
			if box.size <= cencMaxMoofBytes {
				sb, serr := p.readBytes(ctx, box.offset, box.size)
				if serr == nil {
					p.parseSIDX(sb, box)
				}
			}
		case fourCC("moof"):
			if !hasMoofSeen {
				hasMoofSeen = true
				firstMoof = box.offset
			}
		}
		pos = box.end()
		if foundMoov {
			// If we already have moov, stop scanning top-level boxes unless we need to check fragmentation.
			break
		}
	}
	if !foundMoov {
		return nil, errors.New("media: missing moov box")
	}

	hasMvex, progSamples, err := p.parseMoov(ctx, moovBuf, moovBox)
	if err != nil {
		return nil, err
	}

	if hasMvex || (progSamples == 0 && (hasMoofSeen || moovBox.end() < total)) {
		p.fragmented = true
		if hasMoofSeen {
			p.fragScanPos = firstMoof
		} else {
			p.fragScanPos = moovBox.end()
		}
		// Check if a sidx immediately follows moov before the first moof.
		if len(p.sidx) == 0 && p.fragScanPos < total {
			if nextBox, berr := p.readBoxHeaderAt(ctx, p.fragScanPos, total); berr == nil && nextBox.typ == fourCC("sidx") && nextBox.size <= cencMaxMoofBytes {
				if sb, serr := p.readBytes(ctx, nextBox.offset, nextBox.size); serr == nil {
					p.parseSIDX(sb, nextBox)
					p.fragScanPos = nextBox.end()
				}
			}
		}
	}

	p.sortIndexLocked()
	return p, nil
}

func (p *cencPlan) addCached(offset uint64, data []byte) {
	if len(data) == 0 {
		return
	}
	end := offset + uint64(len(data))
	for _, b := range p.cached {
		if offset >= b.offset && end <= b.offset+uint64(len(b.data)) {
			return
		}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	p.cached = append(p.cached, cachedBlock{offset: offset, data: cp})
	slices.SortFunc(p.cached, func(a, b cachedBlock) int {
		if a.offset < b.offset {
			return -1
		}
		if a.offset > b.offset {
			return 1
		}
		return 0
	})
}

func (p *cencPlan) lookupCached(offset, length uint64) ([]byte, bool) {
	end := offset + length
	for _, b := range p.cached {
		bEnd := b.offset + uint64(len(b.data))
		if offset >= b.offset && end <= bEnd {
			return b.data[offset-b.offset : end-b.offset], true
		}
	}
	return nil, false
}

// lookupCachedPrefix returns the longest contiguous cached slice starting at
// offset (up to maxLen bytes), allowing range responses to stream cached head
// bytes immediately at 0 ms before fetching only the uncached tail upstream.
func (p *cencPlan) lookupCachedPrefix(offset, maxLen uint64) []byte {
	if maxLen == 0 {
		return nil
	}
	var best []byte
	for _, b := range p.cached {
		bEnd := b.offset + uint64(len(b.data))
		if offset >= b.offset && offset < bEnd {
			avail := min(maxLen, bEnd-offset)
			if uint64(len(best)) < avail {
				best = b.data[offset-b.offset : offset-b.offset+avail]
			}
		}
	}
	return best
}

func (p *cencPlan) readBytes(ctx context.Context, offset, length uint64) ([]byte, error) {
	if length == 0 {
		return nil, nil
	}
	if b, ok := p.lookupCached(offset, length); ok {
		return b, nil
	}
	if prefix := p.lookupCachedPrefix(offset, length); len(prefix) > 0 {
		remOff := offset + uint64(len(prefix))
		remLen := length - uint64(len(prefix))
		tail, _, err := p.fetchRange(ctx, int64(remOff), int64(remOff+remLen-1))
		if err != nil {
			return nil, err
		}
		if uint64(len(tail)) < remLen {
			return nil, io.ErrUnexpectedEOF
		}
		out := make([]byte, length)
		copy(out, prefix)
		copy(out[len(prefix):], tail[:remLen])
		return out, nil
	}
	end := offset + length - 1
	data, _, err := p.fetchRange(ctx, int64(offset), int64(end))
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) < length {
		return nil, io.ErrUnexpectedEOF
	}
	return data[:length], nil
}

func (p *cencPlan) readBoxHeaderAt(ctx context.Context, pos, parentEnd uint64) (mp4Box, error) {
	if pos+8 > parentEnd {
		return mp4Box{}, io.ErrUnexpectedEOF
	}
	want := min(uint64(32), parentEnd-pos)
	hdr, err := p.readBytes(ctx, pos, want)
	if err != nil {
		return mp4Box{}, err
	}
	return parseBoxHeader(hdr, pos, parentEnd)
}

func (p *cencPlan) fetchRange(ctx context.Context, start, end int64) ([]byte, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.source, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusPartialContent {
		total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range"))
		wantLen := end - start + 1
		if ok && start+wantLen > total {
			wantLen = total - start
		}
		if wantLen < 0 {
			wantLen = 0
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, wantLen+1))
		if rerr != nil {
			return nil, 0, rerr
		}
		if !ok {
			total = start + int64(len(body))
		}
		return body, total, nil
	}
	if resp.StatusCode == http.StatusOK {
		total := resp.ContentLength
		if start > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, start); err != nil {
				return nil, 0, err
			}
		}
		wantLen := end - start + 1
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, wantLen))
		if rerr != nil {
			return nil, 0, rerr
		}
		if total <= 0 {
			total = start + int64(len(body))
		}
		return body, total, nil
	}
	return nil, 0, fmt.Errorf("media: HTTP %d", resp.StatusCode)
}

func parseContentRangeTotal(cr string) (int64, bool) {
	slash := strings.LastIndexByte(cr, '/')
	if slash < 0 {
		return 0, false
	}
	raw := strings.TrimSpace(cr[slash+1:])
	if raw == "" || raw == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func parseBoxHeader(buf []byte, offset, parentEnd uint64) (mp4Box, error) {
	if len(buf) < 8 {
		return mp4Box{}, io.ErrUnexpectedEOF
	}
	sz32 := binary.BigEndian.Uint32(buf[0:4])
	var typ [4]byte
	copy(typ[:], buf[4:8])
	hdrSize := uint64(8)
	var size uint64
	switch sz32 {
	case 0:
		size = parentEnd - offset
	case 1:
		if len(buf) < 16 {
			return mp4Box{}, io.ErrUnexpectedEOF
		}
		size = binary.BigEndian.Uint64(buf[8:16])
		hdrSize = 16
	default:
		size = uint64(sz32)
	}
	var uuid [16]byte
	if typ == fourCC("uuid") {
		if uint64(len(buf)) < hdrSize+16 {
			return mp4Box{}, io.ErrUnexpectedEOF
		}
		copy(uuid[:], buf[hdrSize:hdrSize+16])
		hdrSize += 16
	}
	if size < hdrSize || offset+size > parentEnd {
		return mp4Box{}, fmt.Errorf("box %q bounds invalid (offset=%d size=%d parentEnd=%d)", string(typ[:]), offset, size, parentEnd)
	}
	return mp4Box{
		typ:        typ,
		offset:     offset,
		size:       size,
		headerSize: hdrSize,
		uuid:       uuid,
	}, nil
}

func parseBoxes(buf []byte, baseOffset uint64) ([]mp4Box, error) {
	var out []mp4Box
	parentEnd := baseOffset + uint64(len(buf))
	pos := uint64(0)
	for pos+8 <= uint64(len(buf)) {
		b, err := parseBoxHeader(buf[pos:], baseOffset+pos, parentEnd)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
		pos += b.size
	}
	return out, nil
}

func makeFreePatches(b mp4Box) []bytePatch {
	if b.size < 8 {
		return nil
	}
	if b.headerSize == 8 {
		data := make([]byte, b.size-4)
		copy(data[:4], "free")
		return []bytePatch{{offset: b.offset + 4, data: data}}
	}
	patches := []bytePatch{{offset: b.offset + 4, data: []byte("free")}}
	if b.size > b.headerSize {
		patches = append(patches, bytePatch{
			offset: b.offset + b.headerSize,
			data:   make([]byte, b.size-b.headerSize),
		})
	}
	return patches
}

func (p *cencPlan) parseMoov(ctx context.Context, moovBuf []byte, moovBox mp4Box) (hasMvex bool, progSamples int, err error) {
	children, err := parseBoxes(moovBuf[moovBox.headerSize:], moovBox.payloadOffset())
	if err != nil {
		return false, 0, fmt.Errorf("media: parse moov children: %w", err)
	}

	var mvexBox *mp4Box
	for i := range children {
		b := children[i]
		switch b.typ {
		case fourCC("pssh"):
			p.patches = append(p.patches, makeFreePatches(b)...)
		case fourCC("mvex"):
			hasMvex = true
			cp := b
			mvexBox = &cp
		case fourCC("trak"):
			n, terr := p.parseTrak(ctx, moovBuf, moovBox.offset, b)
			if terr != nil {
				return false, 0, terr
			}
			progSamples += n
		}
	}

	if mvexBox != nil {
		if err := p.parseMvex(moovBuf, moovBox.offset, *mvexBox); err != nil {
			return false, 0, err
		}
	}
	return hasMvex, progSamples, nil
}

func (p *cencPlan) parseMvex(moovBuf []byte, moovOffset uint64, mvex mp4Box) error {
	children, err := parseBoxes(mvex.payload(moovBuf, moovOffset), mvex.payloadOffset())
	if err != nil {
		return err
	}
	for _, b := range children {
		if b.typ != fourCC("trex") {
			continue
		}
		data := b.payload(moovBuf, moovOffset)
		if len(data) < 24 {
			continue
		}
		trackID := binary.BigEndian.Uint32(data[4:8])
		defDesc := binary.BigEndian.Uint32(data[8:12])
		defSize := binary.BigEndian.Uint32(data[16:20])
		tm := p.tracks[trackID]
		if tm == nil {
			tm = &trackMeta{trackID: trackID}
			p.tracks[trackID] = tm
		}
		tm.defaultSampleDescID = defDesc
		tm.defaultSampleSize = defSize
	}
	return nil
}

func (p *cencPlan) parseTrak(ctx context.Context, moovBuf []byte, moovOffset uint64, trak mp4Box) (int, error) {
	children, err := parseBoxes(trak.payload(moovBuf, moovOffset), trak.payloadOffset())
	if err != nil {
		return 0, err
	}
	var (
		trackID   uint32
		mdiaBox   *mp4Box
		trakSenc  *mp4Box
		trakSaio  *mp4Box
		trakSaiz  *mp4Box
	)
	for i := range children {
		b := children[i]
		switch b.typ {
		case fourCC("tkhd"):
			data := b.payload(moovBuf, moovOffset)
			if len(data) >= 16 {
				ver := data[0]
				if ver == 0 && len(data) >= 16 {
					trackID = binary.BigEndian.Uint32(data[12:16])
				} else if ver == 1 && len(data) >= 24 {
					trackID = binary.BigEndian.Uint32(data[20:24])
				}
			}
		case fourCC("mdia"):
			cp := b
			mdiaBox = &cp
		case fourCC("senc"):
			cp := b
			trakSenc = &cp
			p.patches = append(p.patches, makeFreePatches(b)...)
		case fourCC("uuid"):
			if b.uuid == piffSencUUID {
				cp := b
				trakSenc = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		case fourCC("saio"):
			if isCENCAuxBox(b.payload(moovBuf, moovOffset)) {
				cp := b
				trakSaio = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		case fourCC("saiz"):
			if isCENCAuxBox(b.payload(moovBuf, moovOffset)) {
				cp := b
				trakSaiz = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		}
	}
	if mdiaBox == nil {
		return 0, nil
	}

	mdiaChildren, err := parseBoxes(mdiaBox.payload(moovBuf, moovOffset), mdiaBox.payloadOffset())
	if err != nil {
		return 0, err
	}
	var minfBox *mp4Box
	for i := range mdiaChildren {
		if mdiaChildren[i].typ == fourCC("minf") {
			cp := mdiaChildren[i]
			minfBox = &cp
			break
		}
	}
	if minfBox == nil {
		return 0, nil
	}

	minfChildren, err := parseBoxes(minfBox.payload(moovBuf, moovOffset), minfBox.payloadOffset())
	if err != nil {
		return 0, err
	}
	var stblBox *mp4Box
	for i := range minfChildren {
		if minfChildren[i].typ == fourCC("stbl") {
			cp := minfChildren[i]
			stblBox = &cp
			break
		}
	}
	if stblBox == nil {
		return 0, nil
	}

	stblChildren, err := parseBoxes(stblBox.payload(moovBuf, moovOffset), stblBox.payloadOffset())
	if err != nil {
		return 0, err
	}

	var (
		stsdBox *mp4Box
		stszBox *mp4Box
		stscBox *mp4Box
		stcoBox *mp4Box
		co64Box *mp4Box
		sencBox = trakSenc
		saioBox = trakSaio
		saizBox = trakSaiz
	)
	for i := range stblChildren {
		b := stblChildren[i]
		switch b.typ {
		case fourCC("stsd"):
			cp := b
			stsdBox = &cp
		case fourCC("stsz"):
			cp := b
			stszBox = &cp
		case fourCC("stsc"):
			cp := b
			stscBox = &cp
		case fourCC("stco"):
			cp := b
			stcoBox = &cp
		case fourCC("co64"):
			cp := b
			co64Box = &cp
		case fourCC("senc"):
			cp := b
			sencBox = &cp
			p.patches = append(p.patches, makeFreePatches(b)...)
		case fourCC("uuid"):
			if b.uuid == piffSencUUID {
				cp := b
				sencBox = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		case fourCC("saio"):
			if isCENCAuxBox(b.payload(moovBuf, moovOffset)) {
				cp := b
				saioBox = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		case fourCC("saiz"):
			if isCENCAuxBox(b.payload(moovBuf, moovOffset)) {
				cp := b
				saizBox = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		}
	}

	var encEntries []trackEncInfo
	if stsdBox != nil {
		encEntries, err = p.parseStsd(moovBuf, moovOffset, *stsdBox)
		if err != nil {
			return 0, err
		}
	}

	tm := &trackMeta{
		trackID:    trackID,
		encEntries: encEntries,
	}
	if trackID != 0 {
		p.tracks[trackID] = tm
	}

	// Parse progressive sample table if samples exist.
	if stszBox == nil || stscBox == nil || (stcoBox == nil && co64Box == nil) {
		return 0, nil
	}
	sampleSizes, err := parseStsz(stszBox.payload(moovBuf, moovOffset))
	if err != nil {
		return 0, err
	}
	if len(sampleSizes) == 0 {
		return 0, nil
	}

	hasEncrypted := false
	for _, e := range encEntries {
		if e.protected {
			hasEncrypted = true
			break
		}
	}
	if !hasEncrypted {
		return len(sampleSizes), nil
	}

	var chunkOffsets []uint64
	if co64Box != nil {
		chunkOffsets, err = parseCo64(co64Box.payload(moovBuf, moovOffset))
	} else {
		chunkOffsets, err = parseStco(stcoBox.payload(moovBuf, moovOffset))
	}
	if err != nil {
		return 0, err
	}
	stscEntries, err := parseStsc(stscBox.payload(moovBuf, moovOffset))
	if err != nil {
		return 0, err
	}
	samples, chunkSampleCounts := buildProgressiveSamples(sampleSizes, chunkOffsets, stscEntries)
	if len(samples) == 0 {
		return 0, nil
	}

	defaultEnc := tm.encForDesc(samples[0].descIdx)
	var entries []sencEntry
	if sencBox != nil {
		entries, err = parseSenc(sencBox.payload(moovBuf, moovOffset), defaultEnc)
		if err != nil {
			return 0, fmt.Errorf("media: parse senc: %w", err)
		}
	} else if saioBox != nil && saizBox != nil {
		saiz, serr := parseSaiz(saizBox.payload(moovBuf, moovOffset))
		if serr != nil {
			return 0, fmt.Errorf("media: parse saiz: %w", serr)
		}
		saio, oerr := parseSaio(saioBox.payload(moovBuf, moovOffset))
		if oerr != nil {
			return 0, fmt.Errorf("media: parse saio: %w", oerr)
		}
		entries, err = p.readAuxSencEntries(ctx, 0, moovBuf, moovOffset, saio, saiz, chunkSampleCounts, defaultEnc)
		if err != nil {
			return 0, fmt.Errorf("media: read saio/saiz: %w", err)
		}
	}

	p.appendSampleSpans(tm, samples, entries)
	return len(samples), nil
}

func (tm *trackMeta) encForDesc(descIdx uint32) trackEncInfo {
	if tm == nil || len(tm.encEntries) == 0 {
		return trackEncInfo{}
	}
	if descIdx == 0 {
		descIdx = 1
	}
	i := int(descIdx - 1)
	if i < 0 || i >= len(tm.encEntries) {
		return trackEncInfo{}
	}
	return tm.encEntries[i]
}

func (p *cencPlan) parseStsd(moovBuf []byte, moovOffset uint64, stsd mp4Box) ([]trackEncInfo, error) {
	data := stsd.payload(moovBuf, moovOffset)
	if len(data) < 8 {
		return nil, errors.New("media: truncated stsd")
	}
	entryCount := binary.BigEndian.Uint32(data[4:8])
	entries, err := parseBoxes(data[8:], stsd.payloadOffset()+8)
	if err != nil {
		return nil, err
	}
	if uint32(len(entries)) < entryCount {
		entryCount = uint32(len(entries))
	}
	infos := make([]trackEncInfo, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		entry := entries[i]
		if entry.typ != fourCC("encv") && entry.typ != fourCC("enca") {
			continue
		}
		ePayload := entry.payload(moovBuf, moovOffset)
		var childStart uint64
		if entry.typ == fourCC("encv") {
			childStart = 78
		} else {
			if len(ePayload) < 28 {
				return nil, errors.New("media: truncated enca")
			}
			audioVer := binary.BigEndian.Uint16(ePayload[8:10])
			switch audioVer {
			case 0:
				childStart = 28
			case 1:
				childStart = 44
			case 2:
				childStart = 64
			default:
				childStart = 28
			}
		}
		if uint64(len(ePayload)) < childStart {
			return nil, fmt.Errorf("media: truncated %s sample entry", string(entry.typ[:]))
		}
		children, err := parseBoxes(ePayload[childStart:], entry.payloadOffset()+childStart)
		if err != nil {
			return nil, err
		}
		var sinfBox *mp4Box
		for j := range children {
			if children[j].typ == fourCC("sinf") {
				cp := children[j]
				sinfBox = &cp
				break
			}
		}
		if sinfBox == nil {
			return nil, fmt.Errorf("media: %s missing sinf box", string(entry.typ[:]))
		}
		origFmt, encInfo, err := parseSinf(moovBuf, moovOffset, *sinfBox)
		if err != nil {
			return nil, err
		}
		infos[i] = encInfo
		// Patch encv/enca -> original format (avc1/hvc1/mp4a/...) and sinf -> free.
		fmtCopy := make([]byte, 4)
		copy(fmtCopy, origFmt[:])
		p.patches = append(p.patches, bytePatch{offset: entry.offset + 4, data: fmtCopy})
		p.patches = append(p.patches, makeFreePatches(*sinfBox)...)
	}
	return infos, nil
}

func parseSinf(moovBuf []byte, moovOffset uint64, sinf mp4Box) ([4]byte, trackEncInfo, error) {
	children, err := parseBoxes(sinf.payload(moovBuf, moovOffset), sinf.payloadOffset())
	if err != nil {
		return [4]byte{}, trackEncInfo{}, err
	}
	var (
		origFmt [4]byte
		hasFrma bool
		hasSchm bool
		encInfo trackEncInfo
		hasTenc bool
	)
	for _, b := range children {
		switch b.typ {
		case fourCC("frma"):
			data := b.payload(moovBuf, moovOffset)
			if len(data) < 4 {
				return [4]byte{}, trackEncInfo{}, errors.New("media: truncated frma")
			}
			copy(origFmt[:], data[:4])
			hasFrma = true
		case fourCC("schm"):
			data := b.payload(moovBuf, moovOffset)
			if len(data) < 8 {
				return [4]byte{}, trackEncInfo{}, errors.New("media: truncated schm")
			}
			scheme := string(data[4:8])
			if scheme != "cenc" {
				return [4]byte{}, trackEncInfo{}, fmt.Errorf("media: unsupported protection scheme %q", scheme)
			}
			hasSchm = true
		case fourCC("schi"):
			schiChildren, err := parseBoxes(b.payload(moovBuf, moovOffset), b.payloadOffset())
			if err != nil {
				return [4]byte{}, trackEncInfo{}, err
			}
			for _, sb := range schiChildren {
				if sb.typ != fourCC("tenc") {
					continue
				}
				td := sb.payload(moovBuf, moovOffset)
				if len(td) < 24 {
					return [4]byte{}, trackEncInfo{}, errors.New("media: truncated tenc")
				}
				// td[0]=version, td[1..3]=flags, td[4]=reserved, td[5]=crypt/skip
				isProt := td[6]
				ivSize := td[7]
				if isProt != 0 && ivSize != 0 && ivSize != 8 && ivSize != 16 {
					return [4]byte{}, trackEncInfo{}, fmt.Errorf("media: unsupported tenc IV size %d", ivSize)
				}
				encInfo.protected = isProt != 0
				encInfo.perSampleIVSize = ivSize
				if isProt != 0 && ivSize == 0 {
					if len(td) < 25 {
						return [4]byte{}, trackEncInfo{}, errors.New("media: truncated tenc constant IV")
					}
					cSize := int(td[24])
					if (cSize != 8 && cSize != 16) || len(td) < 25+cSize {
						return [4]byte{}, trackEncInfo{}, fmt.Errorf("media: invalid tenc constant IV size %d", cSize)
					}
					copy(encInfo.constIV[:cSize], td[25:25+cSize])
					encInfo.hasConstIV = true
				}
				hasTenc = true
			}
		}
	}
	if !hasFrma {
		return [4]byte{}, trackEncInfo{}, errors.New("media: sinf missing frma")
	}
	if !hasSchm {
		return [4]byte{}, trackEncInfo{}, errors.New("media: sinf missing schm")
	}
	if !hasTenc {
		return [4]byte{}, trackEncInfo{}, errors.New("media: sinf missing tenc")
	}
	return origFmt, encInfo, nil
}

func isCENCAuxBox(payload []byte) bool {
	if len(payload) < 4 {
		return false
	}
	flags := uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
	if flags&1 == 0 {
		return true
	}
	if len(payload) < 12 {
		return false
	}
	return string(payload[4:8]) == "cenc"
}

func parseStsz(data []byte) ([]uint32, error) {
	if len(data) < 12 {
		return nil, errors.New("media: truncated stsz")
	}
	uniform := binary.BigEndian.Uint32(data[4:8])
	count := binary.BigEndian.Uint32(data[8:12])
	if count == 0 {
		return nil, nil
	}
	out := make([]uint32, count)
	if uniform != 0 {
		for i := range out {
			out[i] = uniform
		}
		return out, nil
	}
	if uint64(len(data)) < 12+uint64(count)*4 {
		return nil, errors.New("media: truncated stsz entries")
	}
	for i := uint32(0); i < count; i++ {
		out[i] = binary.BigEndian.Uint32(data[12+i*4 : 16+i*4])
	}
	return out, nil
}

func parseStco(data []byte) ([]uint64, error) {
	if len(data) < 8 {
		return nil, errors.New("media: truncated stco")
	}
	count := binary.BigEndian.Uint32(data[4:8])
	if uint64(len(data)) < 8+uint64(count)*4 {
		return nil, errors.New("media: truncated stco entries")
	}
	out := make([]uint64, count)
	for i := uint32(0); i < count; i++ {
		out[i] = uint64(binary.BigEndian.Uint32(data[8+i*4 : 12+i*4]))
	}
	return out, nil
}

func parseCo64(data []byte) ([]uint64, error) {
	if len(data) < 8 {
		return nil, errors.New("media: truncated co64")
	}
	count := binary.BigEndian.Uint32(data[4:8])
	if uint64(len(data)) < 8+uint64(count)*8 {
		return nil, errors.New("media: truncated co64 entries")
	}
	out := make([]uint64, count)
	for i := uint32(0); i < count; i++ {
		out[i] = binary.BigEndian.Uint64(data[8+i*8 : 16+i*8])
	}
	return out, nil
}

type stscEntry struct {
	firstChunk      uint32
	samplesPerChunk uint32
	sampleDescIdx   uint32
}

func parseStsc(data []byte) ([]stscEntry, error) {
	if len(data) < 8 {
		return nil, errors.New("media: truncated stsc")
	}
	count := binary.BigEndian.Uint32(data[4:8])
	if uint64(len(data)) < 8+uint64(count)*12 {
		return nil, errors.New("media: truncated stsc entries")
	}
	out := make([]stscEntry, count)
	for i := uint32(0); i < count; i++ {
		off := 8 + i*12
		out[i] = stscEntry{
			firstChunk:      binary.BigEndian.Uint32(data[off : off+4]),
			samplesPerChunk: binary.BigEndian.Uint32(data[off+4 : off+8]),
			sampleDescIdx:   binary.BigEndian.Uint32(data[off+8 : off+12]),
		}
	}
	return out, nil
}

func buildProgressiveSamples(sampleSizes []uint32, chunkOffsets []uint64, stsc []stscEntry) ([]sampleLoc, []int) {
	if len(sampleSizes) == 0 || len(chunkOffsets) == 0 || len(stsc) == 0 {
		return nil, nil
	}
	samples := make([]sampleLoc, 0, len(sampleSizes))
	chunkCounts := make([]int, len(chunkOffsets))
	stscIdx := 0
	sampleIdx := 0
	for c := 0; c < len(chunkOffsets) && sampleIdx < len(sampleSizes); c++ {
		chunkNum := uint32(c + 1)
		for stscIdx+1 < len(stsc) && chunkNum >= stsc[stscIdx+1].firstChunk {
			stscIdx++
		}
		entry := stsc[stscIdx]
		pos := chunkOffsets[c]
		n := 0
		for k := uint32(0); k < entry.samplesPerChunk && sampleIdx < len(sampleSizes); k++ {
			sz := uint64(sampleSizes[sampleIdx])
			samples = append(samples, sampleLoc{
				offset:  pos,
				size:    sz,
				descIdx: entry.sampleDescIdx,
			})
			pos += sz
			sampleIdx++
			n++
		}
		chunkCounts[c] = n
	}
	return samples, chunkCounts
}

func parseSenc(data []byte, enc trackEncInfo) ([]sencEntry, error) {
	if len(data) < 8 {
		return nil, errors.New("media: truncated senc")
	}
	flags := uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	useSubsamples := (flags & 0x000002) != 0
	count := binary.BigEndian.Uint32(data[4:8])
	ivSize := int(enc.perSampleIVSize)
	pos := 8
	entries := make([]sencEntry, count)
	for i := uint32(0); i < count; i++ {
		var e sencEntry
		if ivSize > 0 {
			if pos+ivSize > len(data) {
				return nil, errors.New("media: truncated senc IV")
			}
			copy(e.iv[:ivSize], data[pos:pos+ivSize])
			pos += ivSize
		} else if enc.hasConstIV {
			e.iv = enc.constIV
		}
		if useSubsamples {
			if pos+2 > len(data) {
				return nil, errors.New("media: truncated senc subsample count")
			}
			subCount := int(binary.BigEndian.Uint16(data[pos : pos+2]))
			pos += 2
			if pos+subCount*6 > len(data) {
				return nil, errors.New("media: truncated senc subsamples")
			}
			if subCount > 0 {
				e.subsamples = make([]cencSubsample, subCount)
				for j := range subCount {
					e.subsamples[j] = cencSubsample{
						clearBytes:     uint32(binary.BigEndian.Uint16(data[pos : pos+2])),
						protectedBytes: binary.BigEndian.Uint32(data[pos+2 : pos+6]),
					}
					pos += 6
				}
			}
		}
		entries[i] = e
	}
	return entries, nil
}

func parseSaiz(data []byte) (saizInfo, error) {
	if len(data) < 9 {
		return saizInfo{}, errors.New("media: truncated saiz")
	}
	flags := uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	pos := 4
	if flags&1 != 0 {
		pos += 8
	}
	if pos+5 > len(data) {
		return saizInfo{}, errors.New("media: truncated saiz header")
	}
	defSize := data[pos]
	count := binary.BigEndian.Uint32(data[pos+1 : pos+5])
	pos += 5
	info := saizInfo{defaultSize: defSize, sampleCount: count}
	if defSize == 0 {
		if uint64(pos)+uint64(count) > uint64(len(data)) {
			return saizInfo{}, errors.New("media: truncated saiz sizes")
		}
		info.sizes = make([]uint8, count)
		copy(info.sizes, data[pos:pos+int(count)])
	}
	return info, nil
}

func parseSaio(data []byte) (saioInfo, error) {
	if len(data) < 8 {
		return saioInfo{}, errors.New("media: truncated saio")
	}
	ver := data[0]
	flags := uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	pos := 4
	if flags&1 != 0 {
		pos += 8
	}
	if pos+4 > len(data) {
		return saioInfo{}, errors.New("media: truncated saio header")
	}
	count := binary.BigEndian.Uint32(data[pos : pos+4])
	pos += 4
	elemSize := 4
	if ver == 1 {
		elemSize = 8
	}
	if uint64(pos)+uint64(count)*uint64(elemSize) > uint64(len(data)) {
		return saioInfo{}, errors.New("media: truncated saio offsets")
	}
	offsets := make([]uint64, count)
	for i := uint32(0); i < count; i++ {
		if ver == 1 {
			offsets[i] = binary.BigEndian.Uint64(data[pos : pos+8])
		} else {
			offsets[i] = uint64(binary.BigEndian.Uint32(data[pos : pos+4]))
		}
		pos += elemSize
	}
	return saioInfo{offsets: offsets}, nil
}

func (s saizInfo) sizeAt(i int) int {
	if s.defaultSize != 0 {
		return int(s.defaultSize)
	}
	if i < 0 || i >= len(s.sizes) {
		return 0
	}
	return int(s.sizes[i])
}

func (p *cencPlan) readAuxSencEntries(
	ctx context.Context,
	baseOffset uint64,
	localBuf []byte,
	localBase uint64,
	saio saioInfo,
	saiz saizInfo,
	groupCounts []int,
	enc trackEncInfo,
) ([]sencEntry, error) {
	if saiz.sampleCount == 0 || len(saio.offsets) == 0 {
		return nil, nil
	}
	n := int(saiz.sampleCount)
	// Map each sample index to its exact file offset and aux byte length.
	sampleAuxOff := make([]uint64, n)
	sampleAuxLen := make([]int, n)
	switch {
	case len(saio.offsets) == 1:
		cur := baseOffset + saio.offsets[0]
		for i := range n {
			sz := saiz.sizeAt(i)
			sampleAuxOff[i] = cur
			sampleAuxLen[i] = sz
			cur += uint64(sz)
		}
	case len(saio.offsets) == n:
		for i := range n {
			sampleAuxOff[i] = baseOffset + saio.offsets[i]
			sampleAuxLen[i] = saiz.sizeAt(i)
		}
	case len(saio.offsets) == len(groupCounts):
		idx := 0
		for g, cnt := range groupCounts {
			cur := baseOffset + saio.offsets[g]
			for range cnt {
				if idx >= n {
					break
				}
				sz := saiz.sizeAt(idx)
				sampleAuxOff[idx] = cur
				sampleAuxLen[idx] = sz
				cur += uint64(sz)
				idx++
			}
		}
	default:
		cur := baseOffset + saio.offsets[0]
		for i := range n {
			sz := saiz.sizeAt(i)
			sampleAuxOff[i] = cur
			sampleAuxLen[i] = sz
			cur += uint64(sz)
		}
	}

	// Merge contiguous aux byte ranges to minimize HTTP fetches.
	type auxRun struct {
		start, end uint64
		data       []byte
	}
	var runs []auxRun
	for i := range n {
		sz := uint64(sampleAuxLen[i])
		if sz == 0 {
			continue
		}
		s := sampleAuxOff[i]
		e := s + sz
		if len(runs) > 0 && s == runs[len(runs)-1].end {
			runs[len(runs)-1].end = e
		} else {
			runs = append(runs, auxRun{start: s, end: e})
		}
	}

	localEnd := localBase + uint64(len(localBuf))
	for i := range runs {
		length := runs[i].end - runs[i].start
		if length > cencMaxAuxBytes {
			return nil, fmt.Errorf("media: aux info too large (%d bytes)", length)
		}
		if len(localBuf) > 0 && runs[i].start >= localBase && runs[i].end <= localEnd {
			runs[i].data = localBuf[runs[i].start-localBase : runs[i].end-localBase]
		} else {
			b, err := p.readBytes(ctx, runs[i].start, length)
			if err != nil {
				return nil, err
			}
			runs[i].data = b
		}
	}

	lookupAux := func(off uint64, sz int) []byte {
		for _, r := range runs {
			if off >= r.start && off+uint64(sz) <= r.end {
				return r.data[off-r.start : off-r.start+uint64(sz)]
			}
		}
		return nil
	}

	ivSize := int(enc.perSampleIVSize)
	entries := make([]sencEntry, n)
	for i := range n {
		sz := sampleAuxLen[i]
		if sz == 0 {
			if ivSize == 0 && enc.hasConstIV {
				entries[i].iv = enc.constIV
			}
			continue
		}
		raw := lookupAux(sampleAuxOff[i], sz)
		if len(raw) < ivSize {
			return nil, errors.New("media: truncated aux info IV")
		}
		var e sencEntry
		if ivSize > 0 {
			copy(e.iv[:ivSize], raw[:ivSize])
		} else if enc.hasConstIV {
			e.iv = enc.constIV
		}
		if len(raw) > ivSize {
			pos := ivSize
			if pos+2 > len(raw) {
				return nil, errors.New("media: truncated aux info subsample count")
			}
			subCount := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
			pos += 2
			if pos+subCount*6 > len(raw) {
				return nil, errors.New("media: truncated aux info subsamples")
			}
			if subCount > 0 {
				e.subsamples = make([]cencSubsample, subCount)
				for j := range subCount {
					e.subsamples[j] = cencSubsample{
						clearBytes:     uint32(binary.BigEndian.Uint16(raw[pos : pos+2])),
						protectedBytes: binary.BigEndian.Uint32(raw[pos+2 : pos+6]),
					}
					pos += 6
				}
			}
		}
		entries[i] = e
	}
	return entries, nil
}

func (p *cencPlan) appendSampleSpans(tm *trackMeta, samples []sampleLoc, entries []sencEntry) {
	for i, s := range samples {
		if s.size == 0 {
			continue
		}
		enc := tm.encForDesc(s.descIdx)
		if !enc.protected {
			continue
		}
		var entry sencEntry
		if i < len(entries) {
			entry = entries[i]
		} else if enc.perSampleIVSize == 0 && enc.hasConstIV {
			entry.iv = enc.constIV
		} else {
			continue
		}

		if len(entry.subsamples) == 0 {
			p.spans = append(p.spans, cencSpan{
				fileStart:         s.offset,
				fileEnd:           s.offset + s.size,
				iv:                entry.iv,
				encOffsetInSample: 0,
			})
			continue
		}

		pos := s.offset
		sampleEnd := s.offset + s.size
		var encOff uint64
		for _, sub := range entry.subsamples {
			pos += uint64(sub.clearBytes)
			if pos >= sampleEnd {
				break
			}
			prot := uint64(sub.protectedBytes)
			if prot > 0 {
				end := min(pos+prot, sampleEnd)
				p.spans = append(p.spans, cencSpan{
					fileStart:         pos,
					fileEnd:           end,
					iv:                entry.iv,
					encOffsetInSample: encOff,
				})
				encOff += end - pos
				pos = end
			}
		}
	}
}

func (p *cencPlan) parseSIDX(buf []byte, box mp4Box) {
	data := box.payload(buf, box.offset)
	if len(data) < 12 {
		return
	}
	ver := data[0]
	pos := 12 // version(1)+flags(3)+reference_ID(4)+timescale(4)
	var firstOffset uint64
	if ver == 0 {
		if len(data) < pos+8 {
			return
		}
		firstOffset = uint64(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
	} else {
		if len(data) < pos+16 {
			return
		}
		firstOffset = binary.BigEndian.Uint64(data[pos+8 : pos+16])
		pos += 16
	}
	if len(data) < pos+4 {
		return
	}
	refCount := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
	pos += 4
	if len(data) < pos+refCount*12 {
		return
	}
	cur := box.end() + firstOffset
	entries := make([]sidxEntry, 0, refCount)
	for range refCount {
		word := binary.BigEndian.Uint32(data[pos : pos+4])
		refType := word >> 31
		refSize := uint64(word & 0x7fffffff)
		pos += 12
		if refType == 0 && refSize > 0 {
			entries = append(entries, sidxEntry{offset: cur, size: refSize})
		}
		cur += refSize
	}
	if len(entries) > 0 {
		p.sidx = entries
	}
}

func (p *cencPlan) sortIndexLocked() {
	slices.SortFunc(p.spans, func(a, b cencSpan) int {
		if a.fileStart < b.fileStart {
			return -1
		}
		if a.fileStart > b.fileStart {
			return 1
		}
		return 0
	})
	slices.SortFunc(p.patches, func(a, b bytePatch) int {
		if a.offset < b.offset {
			return -1
		}
		if a.offset > b.offset {
			return 1
		}
		return 0
	})
}

// ensureRange ensures any fragmented MP4 moof boxes needed to decrypt [start, end) have been parsed.
func (p *cencPlan) ensureRange(ctx context.Context, start, end uint64) error {
	if !p.fragmented {
		return nil
	}

	p.fragMu.RLock()
	if len(p.sidx) > 0 {
		allParsed := true
		for _, se := range p.sidx {
			if se.offset < end && se.offset+se.size > start {
				if !p.parsedMoofs[se.offset] {
					allParsed = false
					break
				}
			}
		}
		if allParsed {
			p.fragMu.RUnlock()
			return nil
		}
	} else if p.fragScanPos >= end {
		p.fragMu.RUnlock()
		return nil
	}
	p.fragMu.RUnlock()

	p.fragMu.Lock()
	defer p.fragMu.Unlock()

	total := uint64(p.totalSize)
	changed := false

	if len(p.sidx) > 0 {
		for _, se := range p.sidx {
			if se.offset >= end || se.offset+se.size <= start {
				continue
			}
			if p.parsedMoofs[se.offset] {
				continue
			}
			box, err := p.readBoxHeaderAt(ctx, se.offset, total)
			if err != nil {
				return err
			}
			if box.typ == fourCC("moof") {
				if err := p.loadAndParseMoofLocked(ctx, box); err != nil {
					return err
				}
				p.parsedMoofs[se.offset] = true
				changed = true
			}
		}
	} else {
		for p.fragScanPos < end && p.fragScanPos < total {
			box, err := p.readBoxHeaderAt(ctx, p.fragScanPos, total)
			if err != nil {
				return err
			}
			if box.size == 0 || box.end() > total {
				return fmt.Errorf("media: invalid fragment box %q at %d", string(box.typ[:]), p.fragScanPos)
			}
			if box.typ == fourCC("moof") && !p.parsedMoofs[box.offset] {
				if err := p.loadAndParseMoofLocked(ctx, box); err != nil {
					return err
				}
				p.parsedMoofs[box.offset] = true
				changed = true
			}
			p.fragScanPos = box.end()
		}
	}

	if changed {
		p.sortIndexLocked()
	}
	return nil
}

func (p *cencPlan) loadAndParseMoofLocked(ctx context.Context, moof mp4Box) error {
	if moof.size > cencMaxMoofBytes {
		return fmt.Errorf("media: moof too large (%d bytes)", moof.size)
	}
	buf, err := p.readBytes(ctx, moof.offset, moof.size)
	if err != nil {
		return err
	}
	children, err := parseBoxes(buf[moof.headerSize:], moof.payloadOffset())
	if err != nil {
		return err
	}

	prevTrafEnd := moof.offset
	for i := range children {
		b := children[i]
		switch b.typ {
		case fourCC("pssh"):
			p.patches = append(p.patches, makeFreePatches(b)...)
		case fourCC("traf"):
			nextEnd, err := p.parseTrafLocked(ctx, buf, moof.offset, b, prevTrafEnd)
			if err != nil {
				return err
			}
			prevTrafEnd = nextEnd
		}
	}
	return nil
}

func (p *cencPlan) parseTrafLocked(ctx context.Context, moofBuf []byte, moofOffset uint64, traf mp4Box, prevTrafEnd uint64) (uint64, error) {
	children, err := parseBoxes(traf.payload(moofBuf, moofOffset), traf.payloadOffset())
	if err != nil {
		return prevTrafEnd, err
	}

	var (
		tfhdBox  *mp4Box
		trunList []mp4Box
		sencBox  *mp4Box
		saioBox  *mp4Box
		saizBox  *mp4Box
	)
	for i := range children {
		b := children[i]
		switch b.typ {
		case fourCC("tfhd"):
			cp := b
			tfhdBox = &cp
		case fourCC("trun"):
			trunList = append(trunList, b)
		case fourCC("senc"):
			cp := b
			sencBox = &cp
			p.patches = append(p.patches, makeFreePatches(b)...)
		case fourCC("uuid"):
			if b.uuid == piffSencUUID {
				cp := b
				sencBox = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		case fourCC("saio"):
			if isCENCAuxBox(b.payload(moofBuf, moofOffset)) {
				cp := b
				saioBox = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		case fourCC("saiz"):
			if isCENCAuxBox(b.payload(moofBuf, moofOffset)) {
				cp := b
				saizBox = &cp
				p.patches = append(p.patches, makeFreePatches(b)...)
			}
		}
	}
	if tfhdBox == nil || len(trunList) == 0 {
		return prevTrafEnd, nil
	}

	tfhdData := tfhdBox.payload(moofBuf, moofOffset)
	if len(tfhdData) < 8 {
		return prevTrafEnd, errors.New("media: truncated tfhd")
	}
	tfhdFlags := uint32(tfhdData[1])<<16 | uint32(tfhdData[2])<<8 | uint32(tfhdData[3])
	trackID := binary.BigEndian.Uint32(tfhdData[4:8])
	tm := p.tracks[trackID]

	pos := 8
	baseDataOffset := prevTrafEnd
	if tfhdFlags&0x020000 != 0 { // default-base-is-moof
		baseDataOffset = moofOffset
	}
	if tfhdFlags&0x000001 != 0 { // base-data-offset-present
		if pos+8 > len(tfhdData) {
			return prevTrafEnd, errors.New("media: truncated tfhd base_data_offset")
		}
		baseDataOffset = binary.BigEndian.Uint64(tfhdData[pos : pos+8])
		pos += 8
	}
	sampleDescIdx := uint32(1)
	if tm != nil && tm.defaultSampleDescID != 0 {
		sampleDescIdx = tm.defaultSampleDescID
	}
	if tfhdFlags&0x000002 != 0 {
		if pos+4 > len(tfhdData) {
			return prevTrafEnd, errors.New("media: truncated tfhd sample_description_index")
		}
		sampleDescIdx = binary.BigEndian.Uint32(tfhdData[pos : pos+4])
		pos += 4
	}
	if tfhdFlags&0x000008 != 0 { // default-sample-duration-present
		pos += 4
	}
	defaultSampleSize := uint32(0)
	if tm != nil {
		defaultSampleSize = tm.defaultSampleSize
	}
	if tfhdFlags&0x000010 != 0 {
		if pos+4 > len(tfhdData) {
			return prevTrafEnd, errors.New("media: truncated tfhd default_sample_size")
		}
		defaultSampleSize = binary.BigEndian.Uint32(tfhdData[pos : pos+4])
		pos += 4
	}

	var samples []sampleLoc
	trunCounts := make([]int, len(trunList))
	cursor := baseDataOffset
	for ti, trun := range trunList {
		td := trun.payload(moofBuf, moofOffset)
		if len(td) < 8 {
			return prevTrafEnd, errors.New("media: truncated trun")
		}
		tFlags := uint32(td[1])<<16 | uint32(td[2])<<8 | uint32(td[3])
		sCount := binary.BigEndian.Uint32(td[4:8])
		tp := 8
		if tFlags&0x000001 != 0 { // data-offset-present (signed int32)
			if tp+4 > len(td) {
				return prevTrafEnd, errors.New("media: truncated trun data_offset")
			}
			rel := int32(binary.BigEndian.Uint32(td[tp : tp+4]))
			cursor = uint64(int64(baseDataOffset) + int64(rel))
			tp += 4
		}
		if tFlags&0x000004 != 0 { // first-sample-flags-present
			tp += 4
		}
		for range sCount {
			if tFlags&0x000100 != 0 { // sample-duration
				tp += 4
			}
			sz := defaultSampleSize
			if tFlags&0x000200 != 0 { // sample-size
				if tp+4 > len(td) {
					return prevTrafEnd, errors.New("media: truncated trun sample_size")
				}
				sz = binary.BigEndian.Uint32(td[tp : tp+4])
				tp += 4
			}
			if tFlags&0x000400 != 0 { // sample-flags
				tp += 4
			}
			if tFlags&0x000800 != 0 { // sample-composition-time-offset
				tp += 4
			}
			samples = append(samples, sampleLoc{
				offset:  cursor,
				size:    uint64(sz),
				descIdx: sampleDescIdx,
			})
			cursor += uint64(sz)
		}
		trunCounts[ti] = int(sCount)
	}

	if tm == nil || len(samples) == 0 {
		return cursor, nil
	}
	enc := tm.encForDesc(sampleDescIdx)
	if !enc.protected {
		return cursor, nil
	}

	var entries []sencEntry
	if sencBox != nil {
		entries, err = parseSenc(sencBox.payload(moofBuf, moofOffset), enc)
		if err != nil {
			return cursor, fmt.Errorf("media: parse fragment senc: %w", err)
		}
	} else if saioBox != nil && saizBox != nil {
		saiz, serr := parseSaiz(saizBox.payload(moofBuf, moofOffset))
		if serr != nil {
			return cursor, fmt.Errorf("media: parse fragment saiz: %w", serr)
		}
		saio, oerr := parseSaio(saioBox.payload(moofBuf, moofOffset))
		if oerr != nil {
			return cursor, fmt.Errorf("media: parse fragment saio: %w", oerr)
		}
		entries, err = p.readAuxSencEntries(ctx, baseDataOffset, moofBuf, moofOffset, saio, saiz, trunCounts, enc)
		if err != nil {
			return cursor, fmt.Errorf("media: read fragment saio/saiz: %w", err)
		}
	}
	p.appendSampleSpans(tm, samples, entries)
	return cursor, nil
}

// transformChunk applies header patches and AES-128-CTR decryption in place to buf representing [fileOffset, fileOffset+len(buf)).
func (p *cencPlan) transformChunk(buf []byte, fileOffset uint64) {
	if len(buf) == 0 {
		return
	}
	if p.fragmented {
		p.fragMu.RLock()
		defer p.fragMu.RUnlock()
	}
	applyBytePatches(buf, fileOffset, p.patches)
	decryptCENCSpans(p.block, buf, fileOffset, p.spans)
}

func applyBytePatches(buf []byte, fileOffset uint64, patches []bytePatch) {
	if len(patches) == 0 || len(buf) == 0 {
		return
	}
	bufEnd := fileOffset + uint64(len(buf))
	i := sort.Search(len(patches), func(idx int) bool {
		return patches[idx].offset+uint64(len(patches[idx].data)) > fileOffset
	})
	for ; i < len(patches); i++ {
		pt := patches[i]
		if pt.offset >= bufEnd {
			break
		}
		ptEnd := pt.offset + uint64(len(pt.data))
		ovStart := max(fileOffset, pt.offset)
		ovEnd := min(bufEnd, ptEnd)
		copy(buf[ovStart-fileOffset:ovEnd-fileOffset], pt.data[ovStart-pt.offset:ovEnd-pt.offset])
	}
}

func decryptCENCSpans(block cipher.Block, buf []byte, fileOffset uint64, spans []cencSpan) {
	if len(spans) == 0 || len(buf) == 0 {
		return
	}
	bufEnd := fileOffset + uint64(len(buf))
	i := sort.Search(len(spans), func(idx int) bool {
		return spans[idx].fileEnd > fileOffset
	})
	for ; i < len(spans); i++ {
		sp := spans[i]
		if sp.fileStart >= bufEnd {
			break
		}
		ovStart := max(fileOffset, sp.fileStart)
		ovEnd := min(bufEnd, sp.fileEnd)
		encPos := sp.encOffsetInSample + (ovStart - sp.fileStart)
		xorCTR(block, buf[ovStart-fileOffset:ovEnd-fileOffset], sp.iv, encPos)
	}
}

func addCounter128(iv [16]byte, blocks uint64) [16]byte {
	lo := binary.BigEndian.Uint64(iv[8:16])
	newLo := lo + blocks
	binary.BigEndian.PutUint64(iv[8:16], newLo)
	if newLo < lo {
		hi := binary.BigEndian.Uint64(iv[0:8])
		binary.BigEndian.PutUint64(iv[0:8], hi+1)
	}
	return iv
}

func xorCTR(block cipher.Block, data []byte, iv [16]byte, encPos uint64) {
	if len(data) == 0 {
		return
	}
	ctr := addCounter128(iv, encPos>>4)
	if off := int(encPos & 15); off != 0 {
		var ks [16]byte
		block.Encrypt(ks[:], ctr[:])
		n := min(16-off, len(data))
		for i := range n {
			data[i] ^= ks[off+i]
		}
		data = data[n:]
		if len(data) == 0 {
			return
		}
		ctr = addCounter128(ctr, 1)
	}
	if len(data) <= 16 {
		var ks [16]byte
		block.Encrypt(ks[:], ctr[:])
		for i := range data {
			data[i] ^= ks[i]
		}
		return
	}
	cipher.NewCTR(block, ctr[:]).XORKeyStream(data, data)
}

// parseSingleByteRange parses a single HTTP Range header against total file size.
// Returns (start, end, partial, ok). When ok is false, the caller should return 416.
func parseSingleByteRange(hdr string, total int64) (start, end int64, partial, ok bool) {
	if hdr == "" {
		return 0, total - 1, false, true
	}
	if !strings.HasPrefix(hdr, "bytes=") {
		return 0, 0, false, false
	}
	spec := strings.TrimSpace(strings.TrimPrefix(hdr, "bytes="))
	if spec == "" || strings.Contains(spec, ",") {
		// Multi-range requests fall back to full 200 OK per RFC 9110 §14.2.
		if strings.Contains(spec, ",") {
			return 0, total - 1, false, true
		}
		return 0, 0, false, false
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false, false
	}
	left := strings.TrimSpace(spec[:dash])
	right := strings.TrimSpace(spec[dash+1:])
	if left == "" && right == "" {
		return 0, 0, false, false
	}
	if left == "" {
		suffix, err := strconv.ParseInt(right, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false, false
		}
		if suffix >= total {
			return 0, total - 1, true, true
		}
		return total - suffix, total - 1, true, true
	}
	s, err := strconv.ParseInt(left, 10, 64)
	if err != nil || s < 0 || s >= total {
		return 0, 0, false, false
	}
	if right == "" {
		return s, total - 1, true, true
	}
	e, err := strconv.ParseInt(right, 10, 64)
	if err != nil || e < s {
		return 0, 0, false, false
	}
	if e >= total {
		e = total - 1
	}
	return s, e, true, true
}

func (p *cencPlan) serve(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	total := p.totalSize
	start, end, partial, ok := parseSingleByteRange(r.Header.Get("Range"), total)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	reqLen := end - start + 1
	if err := p.ensureRange(ctx, uint64(start), uint64(end+1)); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeHeaders := func() {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(reqLen, 10))
		w.Header().Set("Cache-Control", "no-store")
		if partial {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}

	if r.Method == http.MethodHead {
		writeHeaders()
		return
	}

	// Fast path: serve any cached prefix immediately (0 ms TTFB), then fetch
	// only the uncached tail from upstream if the request extends further.
	if prefix := p.lookupCachedPrefix(uint64(start), uint64(reqLen)); len(prefix) > 0 {
		writeHeaders()
		var stackBuf [32 << 10]byte
		pos := uint64(start)
		remPrefix := prefix
		for len(remPrefix) > 0 {
			n := copy(stackBuf[:], remPrefix)
			p.transformChunk(stackBuf[:n], pos)
			if _, err := w.Write(stackBuf[:n]); err != nil {
				return
			}
			remPrefix = remPrefix[n:]
			pos += uint64(n)
		}
		if int64(len(prefix)) >= reqLen {
			return
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		tailStart := start + int64(len(prefix))
		remaining := end - tailStart + 1
		upReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.source, nil)
		if err != nil {
			return
		}
		for k, v := range p.headers {
			upReq.Header.Set(k, v)
		}
		upReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", tailStart, end))
		resp, err := p.client.Do(upReq)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			return
		}
		if resp.StatusCode == http.StatusOK && tailStart > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, tailStart); err != nil {
				return
			}
		}
		reader := io.LimitReader(resp.Body, remaining)
		for remaining > 0 {
			n, rerr := reader.Read(stackBuf[:min(int64(len(stackBuf)), remaining)])
			if n > 0 {
				p.transformChunk(stackBuf[:n], pos)
				if _, werr := w.Write(stackBuf[:n]); werr != nil {
					return
				}
				pos += uint64(n)
				remaining -= int64(n)
			}
			if rerr != nil {
				return
			}
		}
		return
	}

	// Stream from upstream with Range and decrypt on the fly.
	upReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.source, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range p.headers {
		upReq.Header.Set(k, v)
	}
	upReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := p.client.Do(upReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("upstream HTTP %d", resp.StatusCode), http.StatusBadGateway)
		return
	}
	if resp.StatusCode == http.StatusOK && start > 0 {
		if _, err := io.CopyN(io.Discard, resp.Body, start); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	writeHeaders()
	var buf [32 << 10]byte
	remaining := reqLen
	pos := uint64(start)
	reader := io.LimitReader(resp.Body, remaining)
	for remaining > 0 {
		n, rerr := reader.Read(buf[:min(int64(len(buf)), remaining)])
		if n > 0 {
			p.transformChunk(buf[:n], pos)
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			pos += uint64(n)
			remaining -= int64(n)
		}
		if rerr != nil {
			return
		}
	}
}
