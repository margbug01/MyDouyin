package app

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
	"douyin/internal/tiktok"
)

func (a *app) preferredSubtitleLang() string {
	if a.subtitleLang != "" {
		return a.subtitleLang
	}
	if strings.EqualFold(a.ttRegion, "TW") || strings.EqualFold(a.ttRegion, "HK") {
		return "zh-Hant"
	}
	return "zh-Hans"
}

func (a *app) subtitleTranslateTarget() string {
	if strings.EqualFold(a.ttRegion, "TW") || strings.EqualFold(a.ttRegion, "HK") {
		return "zh-TW"
	}
	return "zh-CN"
}

func isChineseSubtitleLang(lang string) bool {
	low := strings.ToLower(strings.TrimSpace(lang))
	return low == "zh-hans" || low == "zh-hant" || strings.HasPrefix(low, "zh")
}

// ensureSubtitle loads the selected WebVTT subtitle track for s over pure Go HTTP,
// and translates non-Chinese cues into Chinese when auto-translation is enabled.
func (a *app) ensureSubtitle(s *slot) {
	if !a.caps().Subtitles || a.subtitlesOff || s == nil || s.v.Live != nil || len(s.v.Subtitles) == 0 {
		return
	}
	if s.subTrack < 0 || s.subTrack >= len(s.v.Subtitles) || (s.subLoadedURL == "" && !s.subPicked) {
		s.subTrack = tiktok.PickSubtitleTrack(s.v.Subtitles, a.preferredSubtitleLang())
		s.subPicked = true
	}
	if s.subTrack < 0 || s.subTrack >= len(s.v.Subtitles) {
		return
	}
	track := s.v.Subtitles[s.subTrack]
	if track.URL == "" {
		return
	}

	// If raw cues are already loaded, check whether we still need Chinese translation.
	if s.subLoadedURL == track.URL {
		if !a.subTranslateOff && !isChineseSubtitleLang(track.Lang) && len(s.subRawCues) > 0 && len(s.subTransCues) == 0 && !s.subTranslating && !s.subTransFailed {
			a.translateSubtitleCues(s, track.URL, s.subRawCues)
		}
		return
	}
	if s.subLoading {
		return
	}
	fetch := a.fetchSubtitle
	if fetch == nil && a.tt != nil {
		fetch = a.tt.FetchSubtitle
	}
	if fetch == nil {
		return
	}
	s.subLoading = true
	targetURL := track.URL
	trackLang := track.Lang
	wantTrans := !a.subTranslateOff && !isChineseSubtitleLang(trackLang)
	go func() {
		cues, err := fetch(targetURL)
		a.post(func() {
			s.subLoading = false
			s.subLoadedURL = targetURL
			if err != nil {
				return
			}
			s.subRawCues = cues
			s.subTransCues = nil
			s.subTransFailed = false
			if wantTrans && len(cues) > 0 && !a.subTranslateOff {
				a.translateSubtitleCues(s, targetURL, cues)
			}
		})
	}()
}

func originalSubtitleID(tracks []dy.SubtitleTrack, selected int) string {
	for _, tr := range tracks {
		if tr.Original && tr.ID != "" {
			return tr.ID
		}
	}
	if selected >= 0 && selected < len(tracks) && tracks[selected].ID != "" {
		return tracks[selected].ID
	}
	for _, tr := range tracks {
		if tr.ID != "" {
			return tr.ID
		}
	}
	return ""
}

func (a *app) translateSubtitleCues(s *slot, trackURL string, cues []tiktok.SubtitleCue) {
	if s == nil || len(cues) == 0 || s.subTranslating {
		return
	}
	itemID := s.v.ID
	subID := originalSubtitleID(s.v.Subtitles, s.subTrack)
	fn := a.translateCues
	if fn == nil && a.tt != nil {
		fn = func(in []tiktok.SubtitleCue, target string) ([]tiktok.SubtitleCue, error) {
			return a.tt.TranslateSubtitle(itemID, subID, in, target)
		}
	}
	if fn == nil {
		return
	}
	s.subTranslating = true
	target := a.subtitleTranslateTarget()
	copied := append([]tiktok.SubtitleCue(nil), cues...)
	go func() {
		translated, err := fn(copied, target)
		a.post(func() {
			s.subTranslating = false
			if s.subLoadedURL != trackURL {
				return
			}
			if err != nil || len(translated) == 0 {
				s.subTransFailed = true
				return
			}
			s.subTransCues = translated
		})
	}()
}

func (a *app) activeSubtitleCues(s *slot) []tiktok.SubtitleCue {
	if s == nil {
		return nil
	}
	if !a.subTranslateOff && len(s.subTransCues) > 0 {
		return s.subTransCues
	}
	return s.subRawCues
}

// toggleDescTranslation translates s.v.Desc to Chinese or toggles back to the original caption.
func (a *app) toggleDescTranslation(s *slot) {
	if s == nil || s.descTranslating {
		return
	}
	if s.descTrans != "" {
		s.descShowTrans = !s.descShowTrans
		return
	}
	desc := strings.TrimSpace(s.v.Desc)
	if desc == "" {
		return
	}
	itemID := s.v.ID
	fn := a.translateText
	if fn == nil && a.tt != nil {
		fn = func(text, target string) (string, error) {
			return a.tt.TranslateDescription(itemID, text, target)
		}
	}
	if fn == nil {
		a.toast = "翻译服务暂不可用"
		return
	}
	s.descTranslating = true
	target := a.subtitleTranslateTarget()
	go func() {
		translated, err := fn(desc, target)
		a.post(func() {
			s.descTranslating = false
			if err != nil {
				a.toast = "翻译失败：" + err.Error()
				return
			}
			s.descTrans = translated
			s.descShowTrans = true
		})
	}()
}

// subtitleOverlay renders the active WebVTT cue above the bottom caption/controls safe zone.
func (a *app) subtitleOverlay(c *ui.Context, s *slot, at time.Duration, bottomInset float32) {
	cues := a.activeSubtitleCues(s)
	if !a.caps().Subtitles || a.subtitlesOff || a.clear || a.listen || s == nil || s.v.Live != nil || len(cues) == 0 {
		return
	}
	text := tiktok.ActiveSubtitle(cues, at)
	if text == "" {
		return
	}
	ui.Row(c.Key("subtitle-layer")).Absolute().Left(84).Right(84).Bottom(bottomInset).
		Justify(ui.Center).AlignItems(ui.End).PassThrough().ZIndex(6).Children(func() {
		ui.Box(c.Key("subtitle-pill")).MaxWidth(680).Padding(6, 14).Radius(10).
			Background(ui.RGBA(0, 0, 0, 0.62)).PassThrough().Children(func() {
			ui.Text(c, text).FontSize(16).FontWeight(600).TextColor(white).MaxLines(3).PassThrough()
		})
	})
}

// subtitleMenu renders the player-bar caption toggle, auto-translate toggle & language picker.
func (a *app) subtitleMenu(c *ui.Context, s *slot) {
	if !a.caps().Subtitles || s == nil || s.v.Live != nil || len(s.v.Subtitles) == 0 {
		return
	}
	label := "字幕"
	if a.subtitlesOff {
		label = "字幕关"
	} else if !a.subTranslateOff {
		label = "中文字幕"
	}
	b := chip(c.Key("subtitle"), label, !a.subtitlesOff)
	b.Label("字幕：" + label)
	if b.Clicked() {
		a.subtitleOpen = !a.subtitleOpen
	}
	barMenu(c, "subtitle-menu", b, &a.subtitleOpen, 168, func() {
		if choice(c, "关闭字幕", a.subtitlesOff).Clicked() {
			a.subtitleOpen = false
			a.subtitlesOff = true
		}
		if choice(c, "自动译为中文", !a.subtitlesOff && !a.subTranslateOff).Clicked() {
			a.subtitleOpen = false
			a.subtitlesOff = false
			a.subTranslateOff = !a.subTranslateOff
			if !a.subTranslateOff {
				s.subTransFailed = false
				a.ensureSubtitle(s)
			}
		}
		for i, tr := range s.v.Subtitles {
			selected := !a.subtitlesOff && i == s.subTrack
			if choice(c, tr.Label, selected).Clicked() {
				a.subtitleOpen = false
				a.subtitlesOff = false
				a.subtitleLang = tr.Lang
				s.subTrack = i
				s.subPicked = true
				if s.subLoadedURL != tr.URL {
					s.subLoadedURL = ""
					s.subRawCues = nil
					s.subTransCues = nil
					s.subTransFailed = false
					a.ensureSubtitle(s)
				}
			}
		}
	})
}
