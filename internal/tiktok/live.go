package tiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"douyin/internal/dy"
)

type ttImageURL struct {
	URLList []string `json:"url_list"`
}

type ttLiveStreamURL struct {
	HLS    string            `json:"hls_pull_url"`
	HLSMap map[string]string `json:"hls_pull_url_map"`
	SDK    struct {
		Pull struct {
			Data string `json:"stream_data"`
		} `json:"pull_data"`
	} `json:"live_core_sdk_data"`
}

type ttLiveOwner struct {
	ID          flexInt    `json:"id"`
	IDStr       string     `json:"id_str"`
	SecUID      string     `json:"sec_uid"`
	DisplayID   string     `json:"display_id"`
	UniqueID    string     `json:"uniqueId"`
	Nickname    string     `json:"nickname"`
	AvatarThumb ttImageURL `json:"avatar_thumb"`
	AvatarMed   ttImageURL `json:"avatar_medium"`
}

type ttLiveRoom struct {
	ID        flexInt         `json:"id"`
	IDStr     string          `json:"id_str"`
	RoomID    string          `json:"roomId"`
	Title     string          `json:"title"`
	Status    int             `json:"status"` // 2: live, 4: ended
	UserCount flexInt         `json:"user_count"`
	Cover     ttImageURL      `json:"cover"`
	Owner     ttLiveOwner     `json:"owner"`
	StreamURL ttLiveStreamURL `json:"stream_url"`
}

func formatLiveViewers(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n >= 10000:
		return fmt.Sprintf("%.1f万", float64(n)/10000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func tiktokLiveTier(key string) (name string, rank int) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "origin", "origion":
		return "原画", 6
	case "uhd", "uhd_60", "full_hd1":
		return "蓝光", 5
	case "hd", "hd_60", "hd1":
		return "超清", 4
	case "sd", "sd2":
		return "高清", 3
	case "ld", "sd1":
		return "标清", 2
	case "md", "ao":
		return "流畅", 1
	default:
		return "", 0
	}
}

func forceHTTPS(u string) string {
	if strings.HasPrefix(u, "http://") {
		return "https://" + strings.TrimPrefix(u, "http://")
	}
	return u
}

func parseTikTokLiveStream(su ttLiveStreamURL) (hls, h264 string, streams []dy.LiveStream, qs []dy.Quality) {
	byKey := map[string]dy.LiveStream{}

	// 1. Parse live_core_sdk_data.pull_data.stream_data JSON if present.
	if raw := strings.TrimSpace(su.SDK.Pull.Data); raw != "" {
		var sd struct {
			Data map[string]struct {
				Main struct {
					HLS       string `json:"hls"`
					SDKParams string `json:"sdk_params"`
				} `json:"main"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(raw), &sd) == nil {
			for k, v := range sd.Data {
				u := forceHTTPS(firstValidHTTPURL([]string{v.Main.HLS}))
				if u == "" {
					continue
				}
				name, rank := tiktokLiveTier(k)
				if rank == 0 {
					continue
				}
				isH264 := true
				var sp struct {
					VCodec string `json:"VCodec"`
				}
				if json.Unmarshal([]byte(v.Main.SDKParams), &sp) == nil && sp.VCodec != "" {
					isH264 = strings.EqualFold(sp.VCodec, "h264")
				}
				byKey[strings.ToLower(k)] = dy.LiveStream{
					Name:   name,
					SDKKey: strings.ToLower(k),
					URL:    u,
					H264:   isH264,
					Level:  rank,
				}
			}
		}
	}

	// 2. Supplement from hls_pull_url_map.
	for k, rawU := range su.HLSMap {
		u := forceHTTPS(firstValidHTTPURL([]string{rawU}))
		if u == "" {
			continue
		}
		lk := strings.ToLower(k)
		if existing, ok := byKey[lk]; ok {
			if existing.BackupURL == "" && existing.URL != u {
				existing.BackupURL = u
				byKey[lk] = existing
			}
			continue
		}
		name, rank := tiktokLiveTier(k)
		if rank == 0 {
			name, rank = strings.ToUpper(k), 2
		}
		byKey[lk] = dy.LiveStream{
			Name:   name,
			SDKKey: lk,
			URL:    u,
			H264:   true,
			Level:  rank,
		}
	}

	// 3. Fallback single hls_pull_url.
	primary := forceHTTPS(firstValidHTTPURL([]string{su.HLS}))
	if len(byKey) == 0 && primary != "" {
		byKey["hd"] = dy.LiveStream{
			Name:   "高清",
			SDKKey: "hd",
			URL:    primary,
			H264:   true,
			Level:  3,
		}
	}

	streams = make([]dy.LiveStream, 0, len(byKey))
	for _, st := range byKey {
		streams = append(streams, st)
	}
	sort.Slice(streams, func(i, j int) bool {
		if streams[i].Level != streams[j].Level {
			return streams[i].Level > streams[j].Level
		}
		return streams[i].Name < streams[j].Name
	})

	for _, st := range streams {
		if hls == "" {
			hls = st.URL
		}
		if h264 == "" && st.H264 {
			h264 = st.URL
		}
		qs = append(qs, dy.Quality{
			Name: st.Name,
			URL:  st.URL,
		})
	}
	if hls == "" {
		hls = primary
	}
	return hls, h264, streams, qs
}

func parseTikTokLiveRoom(rm ttLiveRoom) (dy.Video, bool) {
	roomID := firstNonEmpty(rm.IDStr, rm.RoomID)
	if roomID == "" && rm.ID > 0 {
		roomID = fmt.Sprintf("%d", rm.ID)
	}
	hls, h264, streams, qs := parseTikTokLiveStream(rm.StreamURL)
	if roomID == "" || hls == "" {
		return dy.Video{}, false
	}
	handle := firstNonEmpty(rm.Owner.DisplayID, rm.Owner.UniqueID)
	author := firstNonEmpty(rm.Owner.Nickname, handle, "TikTok LIVE")
	ownerID := rm.Owner.IDStr
	if ownerID == "" && rm.Owner.ID > 0 {
		ownerID = fmt.Sprintf("%d", rm.Owner.ID)
	}
	cover := firstValidHTTPURL(rm.Cover.URLList)
	avatar := firstNonEmpty(
		firstValidHTTPURL(rm.Owner.AvatarThumb.URLList),
		firstValidHTTPURL(rm.Owner.AvatarMed.URLList),
	)
	desc := strings.TrimSpace(rm.Title)
	if desc == "" {
		desc = author + " 正在直播"
	}
	return dy.Video{
		ID:        "live:" + roomID,
		Desc:      desc,
		Author:    author,
		AuthorID:  firstNonEmpty(rm.Owner.SecUID, handle),
		AuthorUID: ownerID,
		Cover:     cover,
		Poster:    cover,
		Avatar:    avatar,
		Qualities: qs,
		Live: &dy.Live{
			RoomID:  roomID,
			WebRID:  firstNonEmpty(handle, roomID),
			OwnerID: ownerID,
			HLS:     hls,
			H264HLS: h264,
			Ended:   rm.Status == 4,
			Streams: streams,
			Viewers: formatLiveViewers(int64(rm.UserCount)),
		},
	}, true
}

func parseTikTokLiveRooms(rooms []ttLiveRoom) []dy.Video {
	out := make([]dy.Video, 0, len(rooms))
	seen := make(map[string]bool, len(rooms))
	for _, rm := range rooms {
		v, ok := parseTikTokLiveRoom(rm)
		if !ok || seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	return out
}

// LiveRooms pages through currently broadcasting TikTok LIVE rooms.
func (c *Client) LiveRooms() dy.Pager[dy.Video] {
	maxTime := int64(0)
	return pager(func() ([]dy.Video, bool, error) {
		q := fmt.Sprintf("channel_id=1111006&content_type=0&req_from=pc_web_live_tab&style=2&max_time=%d", maxTime)
		b, err := c.call(http.MethodGet, "/api/live/explore/", q, "")
		if err != nil {
			// Fallback: try webcast feed endpoint or return the error cleanly.
			if fb, ferr := c.call(http.MethodGet, "/webcast/feed/", q, ""); ferr == nil {
				b, err = fb, nil
			}
		}
		if err != nil {
			return nil, false, err
		}
		if err := checkTikTokStatus(b); err != nil {
			return nil, false, err
		}
		var r struct {
			Data []struct {
				Data *ttLiveRoom `json:"data"`
				Room *ttLiveRoom `json:"room"`
			} `json:"data"`
			Extra struct {
				HasMore bool    `json:"has_more"`
				MaxTime flexInt `json:"max_time"`
			} `json:"extra"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, false, err
		}
		rooms := make([]ttLiveRoom, 0, len(r.Data))
		for _, entry := range r.Data {
			switch {
			case entry.Data != nil:
				rooms = append(rooms, *entry.Data)
			case entry.Room != nil:
				rooms = append(rooms, *entry.Room)
			}
		}
		next := int64(r.Extra.MaxTime)
		more := r.Extra.HasMore && next > 0 && next != maxTime
		if next > 0 {
			maxTime = next
		}
		return parseTikTokLiveRooms(rooms), more, nil
	})
}

// LiveRoom refreshes a single TikTok LIVE room's stream URLs by roomId or uniqueId.
func (c *Client) LiveRoom(_ context.Context, roomIDOrHandle string) (dy.Video, error) {
	q := "roomId=" + url.QueryEscape(roomIDOrHandle)
	if strings.Trim(roomIDOrHandle, "0123456789") != "" {
		q = "uniqueId=" + url.QueryEscape(roomIDOrHandle)
	}
	b, err := c.call(http.MethodGet, "/api/live/detail/", q, "")
	if err != nil {
		return dy.Video{}, err
	}
	if err := checkTikTokStatus(b); err != nil {
		return dy.Video{}, err
	}
	var r struct {
		LiveRoomInfo struct {
			RoomID  string `json:"roomId"`
			Status  int    `json:"status"`
			Title   string `json:"title"`
			LiveURL string `json:"liveUrl"`
			Cover   string `json:"coverUrl"`
			Owner   struct {
				UniqueID string `json:"uniqueId"`
				Nickname string `json:"nickname"`
				Avatar   string `json:"avatarThumb"`
				SecUID   string `json:"secUid"`
			} `json:"ownerInfo"`
			Stats struct {
				ViewerCount flexInt `json:"viewerCount"`
			} `json:"liveRoomStats"`
		} `json:"LiveRoomInfo"`
		Data *ttLiveRoom `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return dy.Video{}, err
	}
	if r.Data != nil {
		if r.Data.Status == 4 {
			return dy.Video{}, dy.ErrLiveEnded
		}
		if v, ok := parseTikTokLiveRoom(*r.Data); ok {
			return v, nil
		}
	}
	info := r.LiveRoomInfo
	if info.Status == 4 {
		return dy.Video{}, dy.ErrLiveEnded
	}
	hls := forceHTTPS(firstValidHTTPURL([]string{info.LiveURL}))
	if hls == "" {
		return dy.Video{}, dy.ErrLiveEnded
	}
	rm := ttLiveRoom{
		RoomID:    firstNonEmpty(info.RoomID, roomIDOrHandle),
		Title:     info.Title,
		Status:    info.Status,
		UserCount: info.Stats.ViewerCount,
		Cover:     ttImageURL{URLList: []string{info.Cover}},
		Owner: ttLiveOwner{
			SecUID:      info.Owner.SecUID,
			UniqueID:    info.Owner.UniqueID,
			Nickname:    info.Owner.Nickname,
			AvatarThumb: ttImageURL{URLList: []string{info.Owner.Avatar}},
		},
		StreamURL: ttLiveStreamURL{HLS: hls},
	}
	v, ok := parseTikTokLiveRoom(rm)
	if !ok {
		return dy.Video{}, dy.ErrLiveEnded
	}
	return v, nil
}
