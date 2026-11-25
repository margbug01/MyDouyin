package hg

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const expirySafetyMargin = 30 * time.Second

func SelectStream(pb Playback, preferredQuality string, preferH264 bool) (chosen Stream, candidates []string) {
	usable := make([]Stream, 0, len(pb.Streams))
	for _, s := range pb.Streams {
		if strings.TrimSpace(s.URL) != "" || strings.TrimSpace(s.BackupURL) != "" {
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		return Stream{}, nil
	}

	want := normalizeQuality(preferredQuality)

	sortDesc := func(list []Stream) []Stream {
		cp := append([]Stream(nil), list...)
		sort.SliceStable(cp, func(i, j int) bool {
			hi, hj := streamHeightRank(cp[i]), streamHeightRank(cp[j])
			if hi != hj {
				return hi > hj
			}
			if cp[i].Bitrate != cp[j].Bitrate {
				return cp[i].Bitrate > cp[j].Bitrate
			}
			return !cp[i].Dash && cp[j].Dash
		})
		return cp
	}

	pickBest := func(list []Stream) Stream {
		if preferH264 {
			var h264 []Stream
			for _, s := range list {
				if isH264Codec(s.Codec) {
					h264 = append(h264, s)
				}
			}
			if len(h264) > 0 {
				return sortDesc(h264)[0]
			}
		}
		return sortDesc(list)[0]
	}

	if want != "" {
		var matched []Stream
		for _, s := range usable {
			if matchesQuality(s, want) {
				matched = append(matched, s)
			}
		}
		if len(matched) > 0 {
			chosen = pickBest(matched)
		}
	}
	if chosen.URL == "" && chosen.BackupURL == "" {
		chosen = pickBest(usable)
	}

	seen := make(map[string]struct{}, len(usable)*2)
	addURL := func(raw string) {
		u := strings.TrimSpace(raw)
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		candidates = append(candidates, u)
	}

	addURL(chosen.URL)
	addURL(chosen.BackupURL)

	ordered := sortDesc(usable)
	if preferH264 {
		sort.SliceStable(ordered, func(i, j int) bool {
			hi, hj := isH264Codec(ordered[i].Codec), isH264Codec(ordered[j].Codec)
			if hi != hj {
				return hi
			}
			return false
		})
	}
	for _, s := range ordered {
		addURL(s.URL)
		addURL(s.BackupURL)
	}

	return chosen, candidates
}

func IsExpired(pb Playback, now time.Time) bool {
	return !pb.ExpiresAt.IsZero() && now.After(pb.ExpiresAt.Add(-expirySafetyMargin))
}

func DecodeKeyHex(hexKey string) ([]byte, error) {
	trimmed := strings.TrimSpace(hexKey)
	if trimmed == "" {
		return nil, fmt.Errorf("hg: empty content key")
	}
	key, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("hg: invalid content key hex: %w", err)
	}
	if len(key) != 16 {
		return nil, fmt.Errorf("hg: content key must be 16 bytes, got %d", len(key))
	}
	return key, nil
}

func normalizeQuality(q string) string {
	s := strings.ToLower(strings.TrimSpace(q))
	switch s {
	case "", "智能", "auto", "best":
		return ""
	default:
		return s
	}
}

func matchesQuality(s Stream, want string) bool {
	if strings.EqualFold(strings.TrimSpace(s.Definition), want) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(s.Quality), want) {
		return true
	}
	wantDigits := leadingDigits(want)
	if wantDigits > 0 && leadingDigits(strings.ToLower(strings.TrimSpace(s.Definition))) == wantDigits {
		return true
	}
	return false
}

func isH264Codec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	c = strings.NewReplacer(".", "", "-", "", "_", "", " ", "").Replace(c)
	return strings.Contains(c, "h264") || strings.Contains(c, "avc")
}

func streamHeightRank(s Stream) int {
	if s.Height > 0 {
		return s.Height
	}
	if digits := leadingDigits(strings.TrimSpace(s.Definition)); digits > 0 {
		return digits
	}
	return s.Width
}

func leadingDigits(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
