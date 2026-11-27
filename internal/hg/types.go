package hg

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ReleaseStatus uint8

const (
	ReleaseUnknown ReleaseStatus = iota
	ReleaseUpcoming
	ReleaseReleased
	ReleaseOngoing
	ReleaseFinished
)

func (r ReleaseStatus) IsUpcoming() bool {
	return r == ReleaseUpcoming
}

func (r ReleaseStatus) IsReleased() bool {
	return r == ReleaseReleased || r == ReleaseOngoing || r == ReleaseFinished
}

func isZeroEpisodeStatusText(raw string) bool {
	compact := strings.Join(strings.Fields(raw), "")
	switch compact {
	case "全0集", "更新至0集", "共0集", "0集":
		return true
	default:
		return false
	}
}

type Config struct {
	ProfileDir  string
	SignerURL   string
	SignerToken string
	Direct      bool
}

type Series struct {
	ID            string
	Title         string
	CoverURL      string
	Abstract      string
	Score         string
	Category      string
	StatusText    string
	AlbumID       string
	EpisodeCount  int
	Finished      bool
	ReleaseStatus ReleaseStatus
	Actors        []string
	Tags          []string
}

func (s Series) IsUpcoming() bool {
	return s.ReleaseStatus == ReleaseUpcoming
}

func (s Series) DisplayStatus() string {
	st := strings.TrimSpace(s.StatusText)
	if isZeroEpisodeStatusText(st) {
		st = ""
	}
	if s.IsUpcoming() {
		if st != "" {
			return st
		}
		return "暂未上线"
	}
	if st != "" {
		return st
	}
	if s.EpisodeCount > 0 {
		if s.ReleaseStatus == ReleaseOngoing {
			return fmt.Sprintf("更新至%d集", s.EpisodeCount)
		}
		return fmt.Sprintf("全%d集", s.EpisodeCount)
	}
	switch {
	case s.ReleaseStatus == ReleaseOngoing:
		return "连载中"
	case s.ReleaseStatus == ReleaseFinished || s.Finished:
		return "已完结"
	default:
		return ""
	}
}

type Episode struct {
	ID       string
	Index    int
	Title    string
	Duration time.Duration
	CoverURL string
}

type Stream struct {
	Definition string
	Quality    string
	Codec      string
	URL        string
	BackupURL  string
	Encryption string
	Width      int
	Height     int
	Bitrate    int64
	Size       int64
	HLS        bool
	Dash       bool
	Protected  bool
}

type Playback struct {
	EpisodeID string
	VideoID   string
	KeyHex    string
	AudioURL  string
	Duration  time.Duration
	ExpiresAt time.Time
	Protected bool
	Streams   []Stream
}

type Danmaku struct {
	ID        string
	At        time.Duration
	Text      string
	Likes     int64
	PlusOnes  int64
	CreatedAt time.Time
}

type DanmakuPage struct {
	Items   []Danmaku
	Cursor  string
	HasMore bool
	Total   int
}

type Comment struct {
	ID         string
	Author     string
	AvatarURL  string
	Text       string
	CreatedAt  time.Time
	Likes      int64
	ReplyCount int64
	Score      string // non-empty for series reviews (10-point scale)
	ReplyTo    string // non-empty for nested replies targeting another user
}

type CommentPage struct {
	Items      []Comment
	Total      int64
	Cursor     string
	HasMore    bool
	Score      string // optional series average score on first page of series reviews
	ScoreCount int64
}

type HotItem struct {
	Word     string
	SeriesID string
	Score    string
}

type HotBoard struct {
	Name  string
	Items []HotItem
}

type HotSearch struct {
	Guesses []string
	Boards  []HotBoard
}

type RankBoard struct {
	ID       string
	Name     string
	Selected bool
}

type RankContent struct {
	ID       string
	Name     string
	Selected bool
	Boards   []RankBoard
}

type RankItem struct {
	Position     int
	SeriesID     string
	Title        string
	CoverURL     string
	Score        string
	Actor        string
	UserID       string
	AvatarURL    string
	Intro        string
	EpisodeCount int
	Promoted     bool
}

type ShortRankPage struct {
	Contents []RankContent
	Content  string
	Board    string
	Cursor   string
	HasMore  bool
	Items    []RankItem
}

type FilterOption struct {
	Name     string
	Value    string
	Selected bool
}

type FilterRow struct {
	Key     string
	Name    string
	Options []FilterOption
}

type NewReleaseDay struct {
	Date     string
	Label    string
	Selected bool
}

type NewReleasePage struct {
	Days    []NewReleaseDay
	Items   []Series
	Cursor  string
	HasMore bool
}

type Season struct {
	SeriesID      string
	Title         string
	Label         string
	CoverURL      string
	EpisodeCount  int
	ReleaseStatus ReleaseStatus
	StatusText    string
	Current       bool
}

func (s Season) IsUpcoming() bool {
	return s.ReleaseStatus == ReleaseUpcoming
}

func (s Season) DisplayStatus() string {
	st := strings.TrimSpace(s.StatusText)
	if isZeroEpisodeStatusText(st) {
		st = ""
	}
	if s.IsUpcoming() {
		if st != "" {
			return st
		}
		return "待上线"
	}
	return st
}

type Person struct {
	UserID     string
	Name       string
	AvatarURL  string
	Intro      string
	WorksCount int
}

type Page[T any] struct {
	Items   []T
	Cursor  string
	HasMore bool
}

type Service interface {
	Discover(ctx context.Context, channel, cursor string) (Page[Series], error)
	Search(ctx context.Context, query, cursor string, filters []string) (Page[Series], error)
	Suggest(ctx context.Context, prefix string) ([]string, error)
	HotSearch(ctx context.Context) (HotSearch, error)
	ShortRank(ctx context.Context, content, board, cursor string) (ShortRankPage, error)
	ShortFilterPanel(ctx context.Context, genre string) ([]FilterRow, error)
	ShortFilter(ctx context.Context, genre string, selected map[string]string, cursor string) (Page[Series], error)
	NewReleases(ctx context.Context, date, cursor string) (NewReleasePage, error)
	GetSeries(ctx context.Context, seriesID string) (Series, error)
	ListEpisodes(ctx context.Context, seriesID string) ([]Episode, error)
	Seasons(ctx context.Context, seriesID string) ([]Season, error)
	SeriesRecommend(ctx context.Context, seriesID string) ([]Series, error)
	Person(ctx context.Context, userID string) (Person, error)
	PersonWorks(ctx context.Context, userID, cursor string) (Page[Series], error)
	ResolvePlayback(ctx context.Context, episodeID string) (Playback, error)
	ListDanmaku(ctx context.Context, seriesID, episodeID string, startMs int64, cursor string) (DanmakuPage, error)
	ListEpisodeComments(ctx context.Context, seriesID, episodeID, cursor string) (CommentPage, error)
	ListEpisodeReplies(ctx context.Context, seriesID, episodeID, commentID, cursor string) (CommentPage, error)
	ListSeriesComments(ctx context.Context, seriesID, cursor string) (CommentPage, error)
	ListSeriesReplies(ctx context.Context, seriesID, commentID, cursor string) (CommentPage, error)
	ResetDevice(ctx context.Context) error
	Image(ctx context.Context, rawURL string) ([]byte, error)
	Reconfigure(cfg Config) error
}
