// Package platform defines the multi-platform identifiers and capability flags
// shared between Douyin and TikTok modes inside the desktop app.
package platform

import (
	"strings"

	"douyin/internal/dy"
)

// ID identifies the active short-video platform.
type ID string

const (
	Douyin ID = "douyin"
	TikTok ID = "tiktok"
)

// Parse normalizes a persisted or user-supplied platform name, defaulting to Douyin.
func Parse(s string) ID {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(TikTok):
		return TikTok
	default:
		return Douyin
	}
}

// DisplayName returns the user-facing label for p.
func (p ID) DisplayName() string {
	if p == TikTok {
		return "TikTok"
	}
	return "抖音"
}

// Capabilities describes which platform-specific features the UI should expose.
type Capabilities struct {
	Danmaku    bool
	Drama      bool
	Live       bool
	WatchLater bool
	HotSearch  bool
	Location   bool
	Subtitles  bool
}

// Capabilities returns the feature matrix for p.
func (p ID) Capabilities() Capabilities {
	if p == TikTok {
		return Capabilities{
			Live:      true,
			Subtitles: true,
		}
	}
	return Capabilities{
		Danmaku:    true,
		Drama:      true,
		Live:       true,
		WatchLater: true,
		HotSearch:  true,
		Location:   true,
	}
}

// Client is the minimal cross-platform contract implemented by both dy.Client
// (via adapter) and tiktok.Client.
type Client interface {
	Platform() ID
	Feed() ([]dy.Video, error)
	FollowFeed() dy.Pager[dy.Video]
	Search(keyword string) dy.Pager[dy.Video]
	SearchUsers(keyword string) ([]dy.User, error)
	Profile(secUID string) (dy.User, error)
	Posts(secUID string) dy.Pager[dy.Video]
	Comments(videoID string) dy.Pager[dy.Comment]
	Replies(videoID, commentID string) dy.Pager[dy.Comment]
	Video(id string) (dy.Video, error)
	Self() (dy.User, error)
	SetLiked(id string, on bool) error
	SetSaved(id string, on bool) error
	SetFollowed(uid, secUID string, on bool) (int, error)
	PublishComment(req dy.PublishCommentReq) (dy.Comment, error)
	MediaHeaders(v dy.Video) map[string]string
	Image(u string) ([]byte, error)
	CheckLogin(cs []dy.Cookie) (bool, error)
	SaveCookies(cs []dy.Cookie) error
	Logout() error
	SetWeb(w dy.Web)
}
