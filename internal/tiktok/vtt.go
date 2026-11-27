package tiktok

import (
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"douyin/internal/dy"
)

// SubtitleCue is one timed caption entry parsed from a WebVTT/SRT track.
type SubtitleCue struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

var (
	vttTagRe     = regexp.MustCompile(`<[^>]+>`)
	vttSpaceRe   = regexp.MustCompile(`[ \t]+`)
	vttTimeArrow = "-->"
)

// ParseWebVTT parses a WebVTT (or SRT-like) subtitle document into sorted cues.
func ParseWebVTT(raw string) []SubtitleCue {
	raw = strings.TrimPrefix(raw, "\uFEFF")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")

	lines := strings.Split(raw, "\n")
	var cues []SubtitleCue
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "WEBVTT") {
			i++
			continue
		}
		if strings.HasPrefix(line, "NOTE") || strings.HasPrefix(line, "STYLE") || strings.HasPrefix(line, "REGION") {
			i++
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				i++
			}
			continue
		}
		// Optional cue identifier line before timestamp line.
		if !strings.Contains(line, vttTimeArrow) {
			if i+1 < len(lines) && strings.Contains(lines[i+1], vttTimeArrow) {
				i++
				line = strings.TrimSpace(lines[i])
			} else {
				i++
				continue
			}
		}
		start, end, ok := parseVTTTimeRange(line)
		i++
		if !ok {
			continue
		}
		var textLines []string
		for i < len(lines) {
			tl := strings.TrimSpace(lines[i])
			if tl == "" {
				break
			}
			cleaned := cleanVTTText(tl)
			if cleaned != "" {
				textLines = append(textLines, cleaned)
			}
			i++
		}
		if len(textLines) == 0 || end <= start {
			continue
		}
		cues = append(cues, SubtitleCue{
			Start: start,
			End:   end,
			Text:  strings.Join(textLines, "\n"),
		})
	}
	sort.SliceStable(cues, func(a, b int) bool {
		if cues[a].Start != cues[b].Start {
			return cues[a].Start < cues[b].Start
		}
		return cues[a].End < cues[b].End
	})
	return cues
}

func parseVTTTimeRange(line string) (time.Duration, time.Duration, bool) {
	idx := strings.Index(line, vttTimeArrow)
	if idx < 0 {
		return 0, 0, false
	}
	left := strings.TrimSpace(line[:idx])
	right := strings.TrimSpace(line[idx+len(vttTimeArrow):])
	// Strip cue settings after the end timestamp (e.g. "align:center position:50%").
	if sp := strings.IndexAny(right, " \t"); sp > 0 {
		right = right[:sp]
	}
	start, ok1 := parseVTTTimestamp(left)
	end, ok2 := parseVTTTimestamp(right)
	return start, end, ok1 && ok2
}

func parseVTTTimestamp(s string) (time.Duration, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	parts := strings.Split(s, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}
	var hours, mins int
	var secPart string
	var err error
	if len(parts) == 3 {
		if hours, err = strconv.Atoi(parts[0]); err != nil || hours < 0 {
			return 0, false
		}
		if mins, err = strconv.Atoi(parts[1]); err != nil || mins < 0 {
			return 0, false
		}
		secPart = parts[2]
	} else {
		if mins, err = strconv.Atoi(parts[0]); err != nil || mins < 0 {
			return 0, false
		}
		secPart = parts[1]
	}
	secStr, msStr, hasMS := strings.Cut(secPart, ".")
	secs, err := strconv.Atoi(secStr)
	if err != nil || secs < 0 {
		return 0, false
	}
	ms := 0
	if hasMS && msStr != "" {
		if len(msStr) > 3 {
			msStr = msStr[:3]
		}
		for len(msStr) < 3 {
			msStr += "0"
		}
		if ms, err = strconv.Atoi(msStr); err != nil || ms < 0 {
			return 0, false
		}
	}
	return time.Duration(hours)*time.Hour +
		time.Duration(mins)*time.Minute +
		time.Duration(secs)*time.Second +
		time.Duration(ms)*time.Millisecond, true
}

func cleanVTTText(s string) string {
	s = vttTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00A0", " ")
	s = vttSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ActiveSubtitle returns the caption text visible at playback position at, or "".
func ActiveSubtitle(cues []SubtitleCue, at time.Duration) string {
	if len(cues) == 0 || at < 0 {
		return ""
	}
	// First cue whose Start > at; active cues must appear before idx.
	idx := sort.Search(len(cues), func(i int) bool { return cues[i].Start > at })
	var active []string
	// Scan backward from idx-1 to collect overlapping active cues.
	for i := idx - 1; i >= 0; i-- {
		if cues[i].End > at && cues[i].Start <= at {
			active = append(active, cues[i].Text)
		}
		if at-cues[i].Start > 15*time.Second {
			break
		}
	}
	if len(active) == 0 {
		return ""
	}
	if len(active) == 1 {
		return active[0]
	}
	// Reverse so earlier cues appear above newer overlapping cues.
	for l, r := 0, len(active)-1; l < r; l, r = l+1, r-1 {
		active[l], active[r] = active[r], active[l]
	}
	return strings.Join(active, "\n")
}

// NormalizeSubtitleLang maps TikTok's 3-letter/regional subtitle codes (e.g.
// "cmn-Hans-CN", "zho-Hant-TW", "eng-US", "jpn-JP") to clean language codes.
func NormalizeSubtitleLang(code string) (norm, label string) {
	low := strings.ToLower(strings.TrimSpace(code))
	switch {
	case strings.Contains(low, "hant") || strings.HasSuffix(low, "-tw") || strings.HasSuffix(low, "-hk"):
		return "zh-Hant", "中文 (繁體)"
	case strings.HasPrefix(low, "cmn") || strings.HasPrefix(low, "zho") || strings.HasPrefix(low, "zh"):
		return "zh-Hans", "中文 (简体)"
	case strings.HasPrefix(low, "eng") || strings.HasPrefix(low, "en"):
		return "en", "English"
	case strings.HasPrefix(low, "jpn") || strings.HasPrefix(low, "ja"):
		return "ja", "日本語"
	case strings.HasPrefix(low, "kor") || strings.HasPrefix(low, "ko"):
		return "ko", "한국어"
	case strings.HasPrefix(low, "spa") || strings.HasPrefix(low, "es"):
		return "es", "Español"
	case strings.HasPrefix(low, "fra") || strings.HasPrefix(low, "fr"):
		return "fr", "Français"
	case strings.HasPrefix(low, "deu") || strings.HasPrefix(low, "de"):
		return "de", "Deutsch"
	case strings.HasPrefix(low, "vie") || strings.HasPrefix(low, "vi"):
		return "vi", "Tiếng Việt"
	case strings.HasPrefix(low, "tha") || strings.HasPrefix(low, "th"):
		return "th", "ไทย"
	case strings.HasPrefix(low, "ind") || strings.HasPrefix(low, "id"):
		return "id", "Bahasa Indonesia"
	case strings.HasPrefix(low, "por") || strings.HasPrefix(low, "pt"):
		return "pt", "Português"
	case strings.HasPrefix(low, "rus") || strings.HasPrefix(low, "ru"):
		return "ru", "Русский"
	case strings.HasPrefix(low, "ara") || strings.HasPrefix(low, "ar"):
		return "ar", "العربية"
	case low == "":
		return "und", "字幕"
	default:
		return strings.TrimSpace(code), strings.ToUpper(strings.TrimSpace(code))
	}
}

// PickSubtitleTrack returns the index of the best matching track for preferredLang,
// falling back to Chinese, English, or the first available track. Returns -1 if empty.
func PickSubtitleTrack(tracks []dy.SubtitleTrack, preferredLang string) int {
	if len(tracks) == 0 {
		return -1
	}
	want, _ := NormalizeSubtitleLang(preferredLang)
	find := func(lang string) int {
		for i, tr := range tracks {
			if strings.EqualFold(tr.Lang, lang) {
				return i
			}
		}
		return -1
	}
	if preferredLang != "" {
		if i := find(want); i >= 0 {
			return i
		}
	}
	for _, fallback := range []string{"zh-Hans", "zh-Hant", "en"} {
		if i := find(fallback); i >= 0 {
			return i
		}
	}
	return 0
}
