package tiktok

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"douyin/internal/dy"
)

// flexInt parses JSON numbers or numeric strings (TikTok statsV2 uses strings).
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		u, err := strconv.Unquote(s)
		if err != nil {
			*f = 0
			return nil
		}
		if u == "" {
			*f = 0
			return nil
		}
		n, err := strconv.ParseInt(u, 10, 64)
		if err != nil {
			*f = 0
			return nil
		}
		*f = flexInt(n)
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// Fall back to float truncation if TikTok emits a decimal.
		fl, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return err
		}
		*f = flexInt(int64(fl))
		return nil
	}
	*f = flexInt(n)
	return nil
}

type ttPlayAddr struct {
	DataSize flexInt  `json:"DataSize"`
	Width    int      `json:"Width"`
	Height   int      `json:"Height"`
	URLList  []string `json:"UrlList"`
}

type ttBitrateInfo struct {
	GearName  string     `json:"GearName"`
	Bitrate   int64      `json:"Bitrate"`
	CodecType string     `json:"CodecType"`
	PlayAddr  ttPlayAddr `json:"PlayAddr"`
}

type ttAuthor struct {
	ID          string `json:"id"`
	UniqueID    string `json:"uniqueId"`
	Nickname    string `json:"nickname"`
	SecUID      string `json:"secUid"`
	AvatarThumb string `json:"avatarThumb"`
	AvatarMed   string `json:"avatarMedium"`
	Signature   string `json:"signature"`
	Relation    int    `json:"relation"`
}

type ttStats struct {
	DiggCount    flexInt `json:"diggCount"`
	CommentCount flexInt `json:"commentCount"`
	CollectCount flexInt `json:"collectCount"`
	ShareCount   flexInt `json:"shareCount"`
}

type ttSubtitleInfo struct {
	LanguageCodeName string `json:"LanguageCodeName"`
	URL              string `json:"Url"`
	Format           string `json:"Format"`
	Source           string `json:"Source"`
	ClaSubtitleID    string `json:"ClaSubtitleID"`
	SubtitleID       string `json:"SubtitleID"`
}

type ttCLACaptionInfo struct {
	Language          string   `json:"language"`
	LanguageCode      string   `json:"languageCode"`
	URL               string   `json:"url"`
	URLList           []string `json:"urlList"`
	CaptionFormat     string   `json:"captionFormat"`
	IsOriginalCaption bool     `json:"isOriginalCaption"`
	ClaSubtitleID     string   `json:"claSubtitleID"`
}

type ttCLAInfo struct {
	CaptionInfos []ttCLACaptionInfo `json:"captionInfos"`
}

type ttItem struct {
	ID         string   `json:"id"`
	Desc       string   `json:"desc"`
	CreateTime flexInt  `json:"createTime"`
	Digged     bool     `json:"digged"`
	Collected  bool     `json:"itemControl"` // ignored if non-bool; see below
	IsPinned   bool     `json:"isPinnedItem"`
	Author     ttAuthor `json:"author"`
	Video      struct {
		Width         int              `json:"width"`
		Height        int              `json:"height"`
		Duration      int              `json:"duration"` // seconds on TikTok web API
		Cover         string           `json:"cover"`
		OriginCover   string           `json:"originCover"`
		DynamicCover  string           `json:"dynamicCover"`
		PlayAddr      string           `json:"playAddr"`
		DownloadAddr  string           `json:"downloadAddr"`
		BitrateInfo   []ttBitrateInfo  `json:"bitrateInfo"`
		SubtitleInfos []ttSubtitleInfo `json:"subtitleInfos"`
		CLAInfo       ttCLAInfo        `json:"claInfo"`
	} `json:"video"`
	Stats   ttStats `json:"stats"`
	StatsV2 ttStats `json:"statsV2"`
}

// UnmarshalJSON keeps Collected tolerant of TikTok payloads where itemControl
// is an object rather than a boolean.
func (it *ttItem) UnmarshalJSON(b []byte) error {
	type alias ttItem
	aux := struct {
		*alias
		RawControl json.RawMessage `json:"itemControl"`
		Collected  bool            `json:"collected"`
	}{alias: (*alias)(it)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	it.Collected = aux.Collected
	return nil
}

func firstValidHTTPURL(urls []string) string {
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		if u.Scheme == "https" || u.Scheme == "http" {
			return raw
		}
	}
	return ""
}

func isHEVC(codec, gear string) bool {
	c := strings.ToLower(codec)
	g := strings.ToLower(gear)
	return strings.Contains(c, "265") || strings.Contains(c, "hevc") || strings.Contains(c, "bytevc1") ||
		strings.Contains(g, "265") || strings.Contains(g, "hevc") || strings.Contains(g, "bytevc1")
}

func pickTikTokPlayURL(item ttItem) (playURL string, w, h int, size int64, qs []dy.Quality) {
	w, h = item.Video.Width, item.Video.Height

	smaller := func(cand, have int64) bool {
		if have == 0 {
			return cand > 0
		}
		return cand > 0 && cand < have
	}

	type choice struct {
		dy.Quality
		bitrate int64
		h265    bool
	}
	byClass := [5]choice{}
	bestH265 := false

	for _, bi := range item.Video.BitrateInfo {
		u := firstValidHTTPURL(bi.PlayAddr.URLList)
		if u == "" {
			continue
		}
		gw, gh := bi.PlayAddr.Width, bi.PlayAddr.Height
		if gw == 0 || gh == 0 {
			gw, gh = w, h
		}
		short := min(gw, gh)
		long := max(gw, gh)
		var class int
		var name string
		switch {
		case short >= 2000 || long >= 3840:
			class, name = 0, "4K"
		case short >= 1400 || long >= 2560:
			class, name = 1, "2K"
		case short >= 1080 || long >= 1920:
			class, name = 2, "1080P"
		case short >= 720 || long >= 1280:
			class, name = 3, "720P"
		case short >= 540 || short > 0:
			class, name = 4, "540P"
		default:
			class, name = 4, "540P"
		}
		h265 := isHEVC(bi.CodecType, bi.GearName)
		gSize := int64(bi.PlayAddr.DataSize)

		if playURL == "" || (bestH265 && !h265) || (bestH265 == h265 && smaller(gSize, size)) {
			playURL, w, h, size, bestH265 = u, gw, gh, gSize, h265
		}

		c := choice{
			Quality: dy.Quality{Name: name, URL: u, Size: gSize},
			bitrate: bi.Bitrate,
			h265:    h265,
		}
		have := byClass[class]
		better := smaller(c.Size, have.Size)
		if class <= 1 && c.bitrate != have.bitrate {
			better = c.bitrate > have.bitrate
		}
		if have.URL == "" || (have.h265 && !h265) || (have.h265 == h265 && better) {
			byClass[class] = c
		}
	}

	if playURL == "" {
		playURL = firstValidHTTPURL([]string{item.Video.PlayAddr, item.Video.DownloadAddr})
	}
	for _, c := range byClass {
		if c.URL != "" {
			qs = append(qs, c.Quality)
		}
	}
	return playURL, w, h, size, qs
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func parseSubtitleTracks(infos []ttSubtitleInfo) []dy.SubtitleTrack {
	return parseAllSubtitleTracks(infos, nil)
}

func parseAllSubtitleTracks(infos []ttSubtitleInfo, claInfos []ttCLACaptionInfo) []dy.SubtitleTrack {
	if len(infos) == 0 && len(claInfos) == 0 {
		return nil
	}
	seen := make(map[string]int, len(infos)+len(claInfos))
	out := make([]dy.SubtitleTrack, 0, len(infos)+len(claInfos))
	addTrack := func(rawLang, rawURL, format, id string, prefer, original bool) {
		u := firstValidHTTPURL([]string{rawURL})
		if u == "" {
			return
		}
		format = strings.ToLower(strings.TrimSpace(format))
		if format != "" && !strings.Contains(format, "vtt") && format != "srt" {
			return
		}
		lang, label := NormalizeSubtitleLang(rawLang)
		tr := dy.SubtitleTrack{
			ID:       strings.TrimSpace(id),
			Lang:     lang,
			Label:    label,
			URL:      u,
			Format:   firstNonEmpty(format, "webvtt"),
			Original: original,
		}
		if idx, exists := seen[lang]; exists {
			existing := out[idx]
			if prefer {
				if tr.ID == "" {
					tr.ID = existing.ID
				}
				if !tr.Original {
					tr.Original = existing.Original
				}
				out[idx] = tr
			} else {
				if existing.ID == "" && tr.ID != "" {
					out[idx].ID = tr.ID
				}
				if !existing.Original && tr.Original {
					out[idx].Original = true
				}
			}
			return
		}
		seen[lang] = len(out)
		out = append(out, tr)
	}

	// CLA captionInfos carry claSubtitleID, isOriginalCaption, and official MT tracks.
	for _, ci := range claInfos {
		u := firstValidHTTPURL(append([]string{ci.URL}, ci.URLList...))
		rawLang := firstNonEmpty(ci.Language, ci.LanguageCode)
		addTrack(rawLang, u, ci.CaptionFormat, ci.ClaSubtitleID, ci.IsOriginalCaption, ci.IsOriginalCaption)
	}
	for _, info := range infos {
		id := firstNonEmpty(info.ClaSubtitleID, info.SubtitleID)
		prefer := strings.EqualFold(info.Source, "ASR") || strings.EqualFold(info.Source, "CREATOR")
		addTrack(info.LanguageCodeName, info.URL, info.Format, id, prefer, prefer)
	}
	return out
}

func parseItems(items []ttItem) []dy.Video {
	out := make([]dy.Video, 0, len(items))
	for _, it := range items {
		playURL, w, h, size, qs := pickTikTokPlayURL(it)
		if playURL == "" {
			continue
		}
		likes := int(it.Stats.DiggCount)
		if likes == 0 {
			likes = int(it.StatsV2.DiggCount)
		}
		comments := int(it.Stats.CommentCount)
		if comments == 0 {
			comments = int(it.StatsV2.CommentCount)
		}
		collects := int(it.Stats.CollectCount)
		if collects == 0 {
			collects = int(it.StatsV2.CollectCount)
		}
		shares := int(it.Stats.ShareCount)
		if shares == 0 {
			shares = int(it.StatsV2.ShareCount)
		}
		authorName := firstNonEmpty(it.Author.Nickname, it.Author.UniqueID)
		cover := firstNonEmpty(it.Video.Cover, it.Video.OriginCover, it.Video.DynamicCover)
		originCover := firstNonEmpty(it.Video.OriginCover, it.Video.Cover)
		avatar := firstNonEmpty(it.Author.AvatarThumb, it.Author.AvatarMed)

		out = append(out, dy.Video{
			ID:           it.ID,
			Desc:         it.Desc,
			Author:       authorName,
			AuthorID:     firstNonEmpty(it.Author.SecUID, it.Author.UniqueID),
			AuthorUID:    it.Author.ID,
			AuthorFollow: it.Author.Relation,
			Size:         size,
			PlayURL:      playURL,
			W:            w,
			H:            h,
			Duration:     time.Duration(it.Video.Duration) * time.Second,
			Likes:        likes,
			Comments:     comments,
			Collects:     collects,
			Shares:       shares,
			Cover:        cover,
			Poster:       originCover,
			Avatar:       avatar,
			Created:      time.Unix(int64(it.CreateTime), 0),
			Pinned:       it.IsPinned,
			Liked:        it.Digged,
			Saved:        it.Collected,
			Qualities:    qs,
			Subtitles:    parseAllSubtitleTracks(it.Video.SubtitleInfos, it.Video.CLAInfo.CaptionInfos),
		})
	}
	return out
}

type ttUserDetail struct {
	User  ttAuthor `json:"user"`
	Stats struct {
		FollowerCount  flexInt `json:"followerCount"`
		FollowingCount flexInt `json:"followingCount"`
		HeartCount     flexInt `json:"heartCount"`
		VideoCount     flexInt `json:"videoCount"`
	} `json:"stats"`
}

func (u ttUserDetail) toUser() dy.User {
	return dy.User{
		ID:           firstNonEmpty(u.User.SecUID, u.User.UniqueID),
		UID:          u.User.ID,
		Name:         firstNonEmpty(u.User.Nickname, u.User.UniqueID),
		Handle:       u.User.UniqueID,
		Avatar:       firstNonEmpty(u.User.AvatarMed, u.User.AvatarThumb),
		Signature:    u.User.Signature,
		Followers:    int(u.Stats.FollowerCount),
		Following:    int(u.Stats.FollowingCount),
		Liked:        int(u.Stats.HeartCount),
		FollowStatus: u.User.Relation,
	}
}

type ttCommentAvatar struct {
	URLList []string `json:"url_list"`
}

func (a *ttCommentAvatar) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil && u != "" {
			a.URLList = []string{u}
		}
		return nil
	}
	var obj struct {
		URLList []string `json:"url_list"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil
	}
	a.URLList = obj.URLList
	return nil
}

type ttCommentUser struct {
	UID         string          `json:"uid"`
	ID          string          `json:"id"`
	SecUID      string          `json:"sec_uid"`
	SecUIDCamel string          `json:"secUid"`
	UniqueID    string          `json:"unique_id"`
	UniqueCamel string          `json:"uniqueId"`
	Nickname    string          `json:"nickname"`
	AvatarThumb ttCommentAvatar `json:"avatar_thumb"`
	AvatarCamel string          `json:"avatarThumb"`
}

type ttComment struct {
	CID             string        `json:"cid"`
	Text            string        `json:"text"`
	CreateTime      flexInt       `json:"create_time"`
	DiggCount       flexInt       `json:"digg_count"`
	UserDigged      int           `json:"user_digged"`
	ReplyCommentTot flexInt       `json:"reply_comment_total"`
	ReplyToNickname string        `json:"reply_to_nickname"`
	User            ttCommentUser `json:"user"`
}

func (c ttComment) toComment() dy.Comment {
	avatar := firstValidHTTPURL(c.User.AvatarThumb.URLList)
	if avatar == "" {
		avatar = c.User.AvatarCamel
	}
	return dy.Comment{
		ID:       c.CID,
		Text:     c.Text,
		Author:   firstNonEmpty(c.User.Nickname, c.User.UniqueID, c.User.UniqueCamel),
		AuthorID: firstNonEmpty(c.User.SecUID, c.User.SecUIDCamel, c.User.UniqueID, c.User.UniqueCamel),
		Avatar:   avatar,
		ReplyTo:  c.ReplyToNickname,
		Likes:    int(c.DiggCount),
		Liked:    c.UserDigged == 1,
		Replies:  int(c.ReplyCommentTot),
		Time:     time.Unix(int64(c.CreateTime), 0),
	}
}
