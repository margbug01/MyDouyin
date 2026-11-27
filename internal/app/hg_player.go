package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
	"douyin/internal/hg"
	"douyin/internal/media"
)

const (
	hgCheckpointInterval = 2 * time.Second
	hgDrawerDefaultMinW  = 1060
	hgDrawerWidth        = 340
	hgDashUnsupportedMsg = "该电影为音画分离 DASH 格式，当前版本暂不支持双轨合流播放"
)

// hgClockPlayer is an optional interface implemented by test doubles for
// videoPlayer so hgPlayer can drive position, duration, seek, volume, mute,
// and speed without instantiating a real AVPlayer or Media Foundation engine.
type hgClockPlayer interface {
	Position() time.Duration
	Duration() time.Duration
	Seeking() bool
	Seek(time.Duration)
	SetVolume(float64)
	SetMuted(bool)
	SetSpeed(float64)
}

// hgStageHUD tracks auto-hide state for the Hongguo player overlays and cursor.
type hgStageHUD struct {
	key            string
	activeAt       time.Time
	pointerX       float32
	pointerY       float32
	wasOver        bool
	overlayHovered bool
}

func (h *hgStageHUD) want(key string, now time.Time, px, py float32, over, pinned bool) (show bool, hideIn time.Duration) {
	if h.key != key {
		h.key = key
		h.activeAt = now
	}
	switch {
	case over && (!h.wasOver || px != h.pointerX || py != h.pointerY):
		h.activeAt = now
		h.pointerX, h.pointerY = px, py
	case !over && h.wasOver:
		h.activeAt = time.Time{}
	}
	h.wasOver = over
	if pinned {
		return true, 0
	}
	if h.activeAt.IsZero() {
		return false, 0
	}
	elapsed := now.Sub(h.activeAt)
	if elapsed < controlsHideAfter {
		return true, controlsHideAfter - elapsed
	}
	return false, 0
}

// hgPlayer manages active episode playback, 75% next-episode pre-resolution,
// per-series progress checkpointing, danmaku playback, and the episode drawer.
type hgPlayer struct {
	series     hg.Series
	episodes   []hg.Episode
	seasons    []hg.Season
	epIdx      int
	epGroup    int
	drawerOpen bool
	drawerInit bool
	drawerTab  int // 0 = 选集, 1 = 评论, 2 = 弹幕
	commentTab int // 0 = 本集评论, 1 = 剧评

	listLoading bool
	listLoaded  bool
	listErr     hg.ErrorInfo
	listGen     int

	pb              hg.Playback
	quality         string
	pl              players
	loading         bool
	cancel          context.CancelFunc
	loadGen         int
	errInfo         hg.ErrorInfo
	dashUnsupported bool

	paused     bool
	resume     time.Duration
	lastAt     time.Duration
	seekTarget time.Duration
	seeking    bool
	savedAt    time.Time
	finished   bool

	dragging bool
	dragAt   float32

	hud     hgStageHUD
	swipeAt time.Time
	lastNav time.Time

	qualityOpen    bool
	speedOpen      bool
	volumeOpen     bool
	dmSettingsOpen bool

	// 75% next-episode pre-resolve & stream pre-warm cache (never populated from browse grids).
	prefetchEpID string
	prefetchPB   *hg.Playback
	prefetchURL  string
	prefetchWarm *media.WarmVideo
	prefetchBusy bool
	prefetchGen  int

	// Danmaku playback state.
	danmaku   []hg.Danmaku
	dm        dmField // where they fly
	dmCursor  string
	dmHasMore bool
	dmTotal   int
	dmLoaded  bool
	dmLoading bool
	dmGen     int
	dmList    ui.ListState

	// Comments & series reviews state.
	epComments     *hgCommentFeed
	seriesComments *hgCommentFeed
}

func (hp *hgPlayer) currentEpisode() (hg.Episode, bool) {
	if hp == nil || hp.epIdx < 0 || hp.epIdx >= len(hp.episodes) {
		return hg.Episode{}, false
	}
	return hp.episodes[hp.epIdx], true
}

// coverOf is episode idx's cover, else the series'.
func (hp *hgPlayer) coverOf(idx int) string {
	if idx >= 0 && idx < len(hp.episodes) && hp.episodes[idx].CoverURL != "" {
		return hp.episodes[idx].CoverURL
	}
	return hp.series.CoverURL
}

// epKey tells episode idx of the series apart from others, as a slide's end.
func (hp *hgPlayer) epKey(idx int) string { return fmt.Sprintf("%s:%d", hp.series.ID, idx) }

func (hp *hgPlayer) episodeNumber(idx int) int {
	if hp != nil && idx >= 0 && idx < len(hp.episodes) && hp.episodes[idx].Index > 0 {
		return hp.episodes[idx].Index
	}
	return idx + 1
}

func (hp *hgPlayer) releasePlayer() {
	if hp == nil {
		return
	}
	if hp.cancel != nil {
		hp.cancel()
		hp.cancel = nil
	}
	hp.loading = false
	hp.loadGen++
	hp.pl.close()
	hp.pl = players{}
	hp.seeking = false
}

func (hp *hgPlayer) closePrefetch() {
	if hp == nil {
		return
	}
	if hp.prefetchWarm != nil {
		hp.prefetchWarm.Close()
		hp.prefetchWarm = nil
	}
	hp.prefetchEpID = ""
	hp.prefetchPB = nil
	hp.prefetchURL = ""
	hp.prefetchBusy = false
	hp.prefetchGen++
}

func hgStartPrewarm(pb hg.Playback, quality string) (string, *media.WarmVideo) {
	if strings.TrimSpace(pb.AudioURL) != "" {
		return "", nil
	}
	var opts media.StreamOptions
	if pb.Protected {
		key, err := hg.DecodeKeyHex(pb.KeyHex)
		if err != nil {
			return "", nil
		}
		opts.DecryptKey = key
	}
	_, candidates := hg.SelectStream(pb, quality, media.PreferH264)
	if len(candidates) == 0 {
		return "", nil
	}
	warm := media.PreloadStream(context.Background(), candidates[0], opts)
	if warm == nil {
		return "", nil
	}
	return candidates[0], warm
}

func (hp *hgPlayer) clearDanmaku() {
	if hp == nil {
		return
	}
	hp.danmaku = nil
	hp.dm = dmField{}
	hp.dmCursor = ""
	hp.dmHasMore = false
	hp.dmTotal = 0
	hp.dmLoaded = false
	hp.dmLoading = false
	hp.dmList = ui.ListState{}
}

func (hp *hgPlayer) resetDanmakuLayout() {
	if hp == nil {
		return
	}
	hp.dm = dmField{}
}

func (hp *hgPlayer) isSeeking() bool {
	if hp == nil {
		return false
	}
	if hp.pl.live != nil {
		return hp.pl.live.Seeking()
	}
	if cp, ok := hp.pl.vp.(hgClockPlayer); ok {
		return cp.Seeking()
	}
	return false
}

func (hp *hgPlayer) at() time.Duration {
	if hp == nil {
		return 0
	}
	switch {
	case hp.pl.live != nil:
		if p, ok := hp.pl.vp.(*streamPlayer); ok {
			p.update()
		}
		return hp.pl.live.Position()
	case hp.pl.vp != nil:
		if cp, ok := hp.pl.vp.(hgClockPlayer); ok {
			return cp.Position()
		}
	case hp.pl.dec != nil:
		return time.Duration(hp.pl.dec.done.Load())
	}
	return hp.resume
}

func (hp *hgPlayer) currentPos() time.Duration {
	if hp == nil {
		return 0
	}
	if hp.seeking && hp.isSeeking() {
		return hp.seekTarget
	}
	return hp.at()
}

func (hp *hgPlayer) duration() time.Duration {
	if hp == nil {
		return 0
	}
	if hp.pl.vp != nil {
		if sp, ok := hp.pl.vp.(*streamPlayer); ok && sp.engine != nil && sp.engine.Duration() > 0 {
			return sp.engine.Duration()
		}
		if cp, ok := hp.pl.vp.(hgClockPlayer); ok && cp.Duration() > 0 {
			return cp.Duration()
		}
	}
	if hp.pb.Duration > 0 {
		return hp.pb.Duration
	}
	if ep, ok := hp.currentEpisode(); ok && ep.Duration > 0 {
		return ep.Duration
	}
	if hp.pl.live != nil {
		if rem := hp.pl.live.UntilEnd(); rem > 0 && rem < time.Hour {
			return hp.pl.live.Position() + rem
		}
	}
	return 0
}

func (hp *hgPlayer) ended(at, dur time.Duration) bool {
	if hp == nil || hp.pl.vp == nil || hp.paused || hp.finished {
		return false
	}
	if hp.seeking && hp.isSeeking() {
		return false
	}
	if errors.Is(hp.pl.vp.Err(), io.EOF) {
		return true
	}
	if sp, ok := hp.pl.vp.(*streamPlayer); ok && sp.engine != nil && sp.engine.Ended() {
		return true
	}
	if dur > 2*time.Second && at >= dur-300*time.Millisecond {
		return true
	}
	if dur > 5*time.Second && at+time.Second < hp.lastAt && hp.lastAt > dur-5*time.Second {
		return true
	}
	return false
}

func (hp *hgPlayer) applyVolume(gain float64, muted bool) {
	if hp == nil {
		return
	}
	if hp.pl.live != nil {
		hp.pl.live.SetMuted(muted)
		hp.pl.live.SetVolume(gain)
	}
	if cp, ok := hp.pl.vp.(hgClockPlayer); ok {
		cp.SetMuted(muted)
		cp.SetVolume(gain)
	}
}

func (hp *hgPlayer) applySpeed(speed float64) {
	if hp == nil || speed <= 0 {
		return
	}
	if hp.pl.live != nil {
		hp.pl.live.SetSpeed(speed)
	}
	if cp, ok := hp.pl.vp.(hgClockPlayer); ok {
		cp.SetSpeed(speed)
	}
}

func (hp *hgPlayer) playMedia(gain float64, muted bool) {
	if hp == nil || hp.pl.vp == nil {
		return
	}
	hp.pl.vp.Play()
	hp.applyVolume(gain, muted)
	if hp.pl.live != nil {
		hp.pl.live.Play()
	}
}

func (hp *hgPlayer) pauseMedia() {
	if hp == nil || hp.pl.vp == nil {
		return
	}
	hp.pl.vp.Pause()
	if hp.pl.live != nil {
		hp.pl.live.Pause()
	}
}

func (hp *hgPlayer) seekMedia(t time.Duration) {
	if hp == nil {
		return
	}
	if hp.pl.live != nil {
		hp.pl.live.Seek(t)
	}
	if cp, ok := hp.pl.vp.(hgClockPlayer); ok {
		cp.Seek(t)
	}
	hp.seekTarget = t
	hp.seeking = true
	hp.lastAt = t
	hp.resume = t
	hp.finished = false
}

func (hp *hgPlayer) qualityOptions() []string {
	out := []string{"智能"}
	seen := map[string]bool{"智能": true}
	if hp == nil {
		return out
	}
	for _, st := range hp.pb.Streams {
		label := strings.TrimSpace(hgFirstNonEmpty(st.Definition, st.Quality))
		if label == "" {
			continue
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, label)
	}
	return out
}

func (h *hgApp) savedPosition(seriesID string, ep hg.Episode) time.Duration {
	if h == nil || h.hist == nil || seriesID == "" {
		return 0
	}
	entry, ok := h.hist.Get(seriesID)
	if !ok {
		return 0
	}
	if ep.ID != "" {
		if pos, exists := entry.Positions[ep.ID]; exists && pos > 0 {
			return pos
		}
	}
	if (ep.ID != "" && entry.EpisodeID == ep.ID) || (ep.Index > 0 && entry.EpisodeIndex == ep.Index) {
		if entry.Position > 0 {
			return entry.Position
		}
	}
	return 0
}

// hgPlaying reports whether MyHongguo currently shows the episode player page.
func (a *app) hgPlaying() bool {
	if a.mode != modeHongguo || a.hg == nil {
		return false
	}
	top := a.hg.top()
	return top != nil && top.kind == hgPagePlayer
}

func (a *app) hgApplyVolume(gain float64, muted bool) {
	if a.hg != nil && a.hg.player != nil {
		a.hg.player.applyVolume(gain, muted)
	}
}

func (a *app) hgApplySpeed(speed float64) {
	if a.hg != nil && a.hg.player != nil {
		a.hg.player.applySpeed(speed)
	}
}

// hgOpenPlayer pushes hgPagePlayer, initializes hgPlayer, and starts playback
// (fetching episodes first when launched directly from a 追剧 card).
func (a *app) hgOpenPlayer(series hg.Series, episodes []hg.Episode, epIndex int) {
	h := a.ensureHG()
	a.hgStopPlayer()

	var seasons []hg.Season
	var (
		preEpID string
		prePB   *hg.Playback
		preURL  string
		preWarm *media.WarmVideo
	)
	if top := h.top(); top != nil && top.kind == hgPageDetail && top.seriesID == series.ID {
		if len(top.seasons) > 0 {
			seasons = slices.Clone(top.seasons)
		}
		if top.prefetchEpID != "" && top.prefetchPB != nil {
			preEpID = top.prefetchEpID
			prePB = top.prefetchPB
			preURL = top.prefetchURL
			preWarm = top.prefetchWarm
			top.prefetchWarm = nil
			top.prefetchPB = nil
			top.prefetchEpID = ""
			top.prefetchURL = ""
		}
	}

	eps := slices.Clone(episodes)
	if len(eps) > 0 {
		epIndex = max(0, min(epIndex, len(eps)-1))
	} else {
		epIndex = 0
	}

	hp := &hgPlayer{
		series:       series,
		episodes:     eps,
		seasons:      seasons,
		epIdx:        epIndex,
		epGroup:      epIndex / hgEpisodeGroupSize,
		quality:      a.quality,
		prefetchEpID: preEpID,
		prefetchPB:   prePB,
		prefetchURL:  preURL,
		prefetchWarm: preWarm,
	}
	p := &hgPage{
		kind:     hgPagePlayer,
		title:    hgFirstNonEmpty(series.Title, "播放"),
		seriesID: series.ID,
		series:   series,
		episodes: eps,
		epIdx:    epIndex,
	}
	h.stack = append(h.stack, p)
	h.player = hp

	if len(hp.seasons) == 0 && series.ID != "" {
		a.hgFetchPlayerSeasons(hp)
	}
	if len(hp.episodes) == 0 {
		if hp.series.IsUpcoming() {
			hp.listLoaded = true
			return
		}
		a.hgFetchPlayerEpisodes(hp)
		return
	}
	hp.listLoaded = true
	a.hgPlayEpisode(epIndex)
}

func (a *app) hgFetchPlayerSeasons(hp *hgPlayer) {
	h := a.hg
	if h == nil || h.svc == nil || hp == nil || hp.series.ID == "" {
		return
	}
	id, svc := hp.series.ID, h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		seasons, err := svc.Seasons(ctx, id)
		cancel()
		a.post(func() {
			if a.hg != h || h.player != hp || hp.series.ID != id {
				return
			}
			if err == nil && len(seasons) > 0 {
				hp.seasons = seasons
			}
		})
	}()
}

func (a *app) hgFetchPlayerEpisodes(hp *hgPlayer) {
	h := a.hg
	if h == nil || h.svc == nil || hp == nil || hp.series.ID == "" {
		return
	}
	hp.listGen++
	hp.listLoading = true
	hp.listLoaded = false
	hp.listErr = hg.ErrorInfo{}
	gen, id, svc := hp.listGen, hp.series.ID, h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		eps, err := svc.ListEpisodes(ctx, id)
		cancel()
		a.post(func() {
			if a.hg != h || h.player != hp || hp.listGen != gen {
				return
			}
			hp.listLoading = false
			if err != nil {
				hp.listErr = hg.ClassifyError(err)
				return
			}
			hp.listLoaded = true
			hp.listErr = hg.ErrorInfo{}
			hp.episodes = eps
			if len(eps) > 0 {
				if hp.series.EpisodeCount == 0 {
					hp.series.EpisodeCount = len(eps)
				}
				if hp.series.IsUpcoming() || hp.series.ReleaseStatus == hg.ReleaseUnknown {
					hp.series.ReleaseStatus = hg.ReleaseReleased
				}
			} else if hp.series.ReleaseStatus == hg.ReleaseUnknown {
				hp.series.ReleaseStatus = hg.ReleaseUpcoming
			}
			if top := h.top(); top != nil && top.kind == hgPagePlayer {
				top.episodes = eps
				top.series = hp.series
			}
			if len(eps) == 0 {
				hp.errInfo = hg.ErrorInfo{}
				return
			}
			idx := 0
			if h.hist != nil {
				if entry, ok := h.hist.Get(id); ok {
					idx = hgResumeEpisodeIndex(eps, entry)
				}
			}
			a.hgPlayEpisode(idx)
		})
	}()
}

// hgPlayEpisode checkpoints the outgoing episode, releases its media player,
// and resolves + starts playback of episodes[idx] (reusing a fresh 75% prefetch
// cache entry when available).
func (a *app) hgPlayEpisode(idx int) {
	h := a.hg
	if h == nil || h.player == nil {
		return
	}
	hp := h.player
	if idx < 0 || idx >= len(hp.episodes) {
		return
	}

	// Checkpoint the outgoing episode before switching away.
	if hp.pl.vp != nil && !hp.finished {
		a.hgCheckpoint(true)
	}
	hp.releasePlayer()

	hp.epIdx = idx
	hp.epGroup = idx / hgEpisodeGroupSize
	if top := h.top(); top != nil && top.kind == hgPagePlayer {
		top.epIdx = idx
	}

	ep := hp.episodes[idx]
	resume := h.savedPosition(hp.series.ID, ep)

	hp.pb = hg.Playback{}
	hp.errInfo = hg.ErrorInfo{}
	hp.dashUnsupported = false
	hp.finished = false
	hp.paused = false
	hp.dragging = false
	hp.resume = resume
	hp.lastAt = resume
	hp.savedAt = time.Time{}

	// Consume matching non-expired prefetch entry (from detail page or 75% next-episode pre-resolve).
	var cachedPB *hg.Playback
	var warm *media.WarmVideo
	var warmURL string
	if hp.prefetchEpID == ep.ID && hp.prefetchPB != nil && !hg.IsExpired(*hp.prefetchPB, time.Now()) {
		cp := *hp.prefetchPB
		cachedPB = &cp
		warm = hp.prefetchWarm
		warmURL = hp.prefetchURL
		hp.prefetchWarm = nil
	}
	hp.closePrefetch()

	hp.clearDanmaku()
	hp.dmGen++
	hp.epComments = newHGCommentFeed(hp.series.ID, ep.ID)
	if hp.seriesComments == nil || hp.seriesComments.seriesID != hp.series.ID {
		hp.seriesComments = newHGCommentFeed(hp.series.ID, "")
	}
	a.ensureEmojis()

	a.hgLoadEpisodeStream(hp, ep, cachedPB, warm, warmURL, resume)
	a.hgLoadCommentFeed(hp.epComments, false)
	if hp.drawerOpen && hp.drawerTab == hgDrawerTabComments && hp.commentTab == hgCommentTabSeries {
		a.hgEnsureActiveComments(hp)
	}
	if !a.dmOff || (hp.drawerOpen && hp.drawerTab == hgDrawerTabDanmaku) {
		a.hgLoadDanmaku(hp, ep, 0, "")
	}
}

func (a *app) hgRetryPlayback() {
	h := a.hg
	if h == nil || h.player == nil {
		return
	}
	hp := h.player
	if hp.listErr.Kind != hg.ErrKindNone || len(hp.episodes) == 0 {
		a.hgFetchPlayerEpisodes(hp)
		return
	}
	ep, ok := hp.currentEpisode()
	if !ok {
		return
	}
	pos := hp.resume
	if hp.pl.vp != nil {
		pos = hp.currentPos()
	}
	hp.releasePlayer()
	hp.errInfo = hg.ErrorInfo{}
	hp.dashUnsupported = false
	hp.finished = false
	a.hgLoadEpisodeStream(hp, ep, nil, nil, "", pos)
}

func (a *app) hgSetQuality(q string) {
	h := a.hg
	if h == nil || h.player == nil {
		return
	}
	hp := h.player
	if q == "" {
		q = "智能"
	}
	cur := hp.quality
	if cur == "" {
		cur = "智能"
	}
	if q == cur {
		return
	}
	hp.quality = q
	a.toast = "清晰度：" + q
	ep, ok := hp.currentEpisode()
	if !ok {
		return
	}
	pos := hp.resume
	if hp.pl.vp != nil {
		pos = hp.currentPos()
		a.hgCheckpoint(true)
	}
	var cachedPB *hg.Playback
	if hp.pb.EpisodeID == ep.ID && len(hp.pb.Streams) > 0 && !hg.IsExpired(hp.pb, time.Now()) {
		cp := hp.pb
		cachedPB = &cp
	}
	hp.releasePlayer()
	hp.errInfo = hg.ErrorInfo{}
	hp.dashUnsupported = false
	a.hgLoadEpisodeStream(hp, ep, cachedPB, nil, "", pos)
}

func (a *app) hgPlayerSwitchSeason(sn hg.Season) {
	h := a.hg
	if h == nil || h.player == nil || sn.SeriesID == "" || sn.SeriesID == h.player.series.ID {
		return
	}
	if sn.IsUpcoming() {
		a.toast = hgSeasonBaseLabel(sn, 0) + "暂未上线"
		return
	}
	hp := h.player
	if hp.pl.vp != nil && !hp.finished {
		a.hgCheckpoint(true)
	}
	hp.releasePlayer()
	hp.series = hg.Series{
		ID:            sn.SeriesID,
		Title:         hgFirstNonEmpty(sn.Title, hp.series.Title),
		CoverURL:      hgFirstNonEmpty(sn.CoverURL, hp.series.CoverURL),
		EpisodeCount:  sn.EpisodeCount,
		ReleaseStatus: sn.ReleaseStatus,
		StatusText:    sn.StatusText,
	}
	for i := range hp.seasons {
		hp.seasons[i].Current = hp.seasons[i].SeriesID == sn.SeriesID
	}
	hp.episodes = nil
	hp.listLoaded = false
	hp.listErr = hg.ErrorInfo{}
	hp.epIdx = 0
	hp.epGroup = 0
	hp.pb = hg.Playback{}
	hp.errInfo = hg.ErrorInfo{}
	hp.dashUnsupported = false
	hp.finished = false
	hp.resume = 0
	hp.lastAt = 0
	hp.closePrefetch()
	hp.clearDanmaku()
	hp.dmGen++
	hp.epComments = nil
	hp.seriesComments = newHGCommentFeed(hp.series.ID, "")
	if top := h.top(); top != nil && top.kind == hgPagePlayer {
		top.title = hgFirstNonEmpty(hp.series.Title, "播放")
		top.seriesID = hp.series.ID
		top.series = hp.series
		top.episodes = nil
		top.epIdx = 0
	}
	a.hgFetchPlayerEpisodes(hp)
}

func (a *app) openHGStreamDefault(ctx context.Context, rawURL string, opts media.StreamOptions, native bool) (players, error) {
	var pl players
	var err error
	if native {
		pl.live, err = media.OpenNativeWithOptions(ctx, rawURL, opts, true)
	} else {
		pl.live, err = media.OpenVideoWithOptions(ctx, rawURL, opts)
	}
	if err != nil {
		return players{}, err
	}
	stream := pl.live
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	newPlayer := newStreamPlayer
	if native {
		newPlayer = newNativePlayer
	}
	vp, err := newPlayer(stream, false)
	stop()
	if err == nil {
		pl.vp = vp
		err = ctx.Err()
	} else if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		pl.close()
		return players{}, err
	}
	pl.vp.Pause()
	stream.Pause()
	return pl, nil
}

func (a *app) openHGStreamFromWarm(ctx context.Context, warm *media.WarmVideo, native bool) (players, error) {
	if warm == nil {
		return players{}, errors.New("media: nil warm video")
	}
	live, err := warm.OpenContext(ctx, native)
	if err != nil {
		return players{}, err
	}
	pl := players{live: live}
	stop := context.AfterFunc(ctx, func() { live.Close() })
	newPlayer := newStreamPlayer
	if native {
		newPlayer = newNativePlayer
	}
	vp, err := newPlayer(live, false)
	stop()
	if err == nil {
		pl.vp = vp
		err = ctx.Err()
	} else if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		pl.close()
		return players{}, err
	}
	pl.vp.Pause()
	live.Pause()
	return pl, nil
}

func (a *app) hgLoadEpisodeStream(hp *hgPlayer, ep hg.Episode, cachedPB *hg.Playback, warm *media.WarmVideo, warmURL string, resume time.Duration) {
	h := a.hg
	if h == nil || h.svc == nil || hp == nil {
		warm.Close()
		return
	}
	if hp.cancel != nil {
		hp.cancel()
		hp.cancel = nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	hp.cancel = cancel
	hp.loading = true
	hp.loadGen++
	hp.errInfo = hg.ErrorInfo{}
	hp.dashUnsupported = false

	gen := hp.loadGen
	svc := h.svc
	quality := hp.quality
	native := a.win != nil && media.NativeAvailable
	customOpen := a.openHGStream != nil
	openFn := a.openHGStream
	if openFn == nil {
		openFn = a.openHGStreamDefault
	}

	go func() {
		defer warm.Close()
		openFromPlayback := func(pb hg.Playback) (players, bool, error) {
			if strings.TrimSpace(pb.AudioURL) != "" {
				return players{}, true, nil
			}
			var opts media.StreamOptions
			if pb.Protected {
				key, err := hg.DecodeKeyHex(pb.KeyHex)
				if err != nil {
					return players{}, false, err
				}
				opts.DecryptKey = key
			}
			_, candidates := hg.SelectStream(pb, quality, media.PreferH264)
			if len(candidates) == 0 {
				return players{}, false, errors.New("hg: 没有可用的播放流地址")
			}
			var lastErr error
			for i, u := range candidates {
				if err := ctx.Err(); err != nil {
					return players{}, false, err
				}
				attemptCtx := ctx
				var attemptCancel context.CancelFunc
				if i < len(candidates)-1 {
					attemptCtx, attemptCancel = context.WithTimeout(ctx, 12*time.Second)
				}
				var (
					pl  players
					err error
				)
				if warm != nil && !customOpen && warmURL == u {
					w := warm
					warm = nil
					pl, err = a.openHGStreamFromWarm(attemptCtx, w, native)
					if err != nil {
						w.Close()
					}
				}
				if pl.vp == nil {
					pl, err = openFn(attemptCtx, u, opts, native)
				}
				if attemptCancel != nil {
					attemptCancel()
				}
				if err == nil {
					return pl, false, nil
				}
				lastErr = err
				if ctx.Err() != nil {
					return players{}, false, ctx.Err()
				}
			}
			return players{}, false, lastErr
		}

		resolveFresh := func() (hg.Playback, error) {
			rctx, rcancel := context.WithTimeout(ctx, hgRequestTimeout)
			defer rcancel()
			return svc.ResolvePlayback(rctx, ep.ID)
		}

		var (
			pb              hg.Playback
			pl              players
			dashUnsupported bool
			err             error
		)
		if cachedPB != nil && !hg.IsExpired(*cachedPB, time.Now()) {
			pb = *cachedPB
		} else {
			pb, err = resolveFresh()
		}
		if err == nil {
			pl, dashUnsupported, err = openFromPlayback(pb)
			// If opening every candidate failed (e.g. cached or signed URL expired),
			// do one fresh ResolvePlayback retry before surfacing the error.
			if err != nil && !dashUnsupported && ctx.Err() == nil {
				if freshPB, ferr := resolveFresh(); ferr == nil {
					pb = freshPB
					pl, dashUnsupported, err = openFromPlayback(pb)
				} else {
					err = ferr
				}
			}
		}

		a.post(func() {
			if a.hg != h || h.player != hp || hp.loadGen != gen || ctx.Err() != nil {
				pl.close()
				cancel()
				return
			}
			hp.loading = false
			hp.cancel = nil
			cancel()
			hp.pb = pb
			if dashUnsupported {
				pl.close()
				hp.dashUnsupported = true
				hp.errInfo = hg.ErrorInfo{}
				return
			}
			if err != nil {
				pl.close()
				hp.errInfo = hg.ClassifyError(err)
				return
			}
			hp.pl = pl
			hp.errInfo = hg.ErrorInfo{}
			hp.dashUnsupported = false
			hp.finished = false

			startPos := resume
			if dur := hp.duration(); dur > 2*time.Second && startPos >= dur-time.Second {
				startPos = 0
			}
			speed := a.speed
			if speed <= 0 {
				speed = 1
			}
			hp.applySpeed(speed)
			if startPos > 0 {
				hp.seekMedia(startPos)
			} else {
				hp.lastAt = 0
				hp.resume = 0
			}
			volumeNow.Store(a.loudness())
			hp.paused = false
			hp.playMedia(a.loudness(), a.muted)
			if h.hist != nil && hp.series.ID != "" {
				_ = h.hist.RecordProgress(hp.series, ep, startPos)
				hp.savedAt = time.Now()
			}
		})
	}()
}

// hgEnsurePlayerResumed reopens the active Hongguo episode when returning to
// modeHongguo after a suspend that kept hgPagePlayer on the navigation stack.
func (a *app) hgEnsurePlayerResumed() {
	h := a.hg
	if h == nil || h.player == nil {
		return
	}
	hp := h.player
	if hp.pl.vp != nil || hp.loading || hp.listLoading || hp.errInfo.Kind != hg.ErrKindNone || hp.dashUnsupported || hp.finished {
		return
	}
	ep, ok := hp.currentEpisode()
	if !ok {
		if len(hp.episodes) == 0 && hp.series.ID != "" && !hp.listLoaded && hp.listErr.Kind == hg.ErrKindNone && !hp.series.IsUpcoming() {
			a.hgFetchPlayerEpisodes(hp)
		}
		return
	}
	resume := hp.resume
	if resume <= 0 {
		resume = h.savedPosition(hp.series.ID, ep)
	}
	a.hgLoadEpisodeStream(hp, ep, nil, nil, "", resume)
	if !a.dmOff && !hp.dmLoaded && !hp.dmLoading {
		a.hgLoadDanmaku(hp, ep, 0, "")
	}
}

// hgCheckpoint saves the current episode playback position to HistoryStore.
func (a *app) hgCheckpoint(force bool) {
	h := a.hg
	if h == nil || h.player == nil || h.hist == nil {
		return
	}
	hp := h.player
	if hp.pl.vp == nil || hp.finished || hp.series.ID == "" {
		return
	}
	ep, ok := hp.currentEpisode()
	if !ok {
		return
	}
	now := time.Now()
	if !force && !hp.savedAt.IsZero() && now.Sub(hp.savedAt) < hgCheckpointInterval {
		return
	}
	pos := hp.currentPos()
	hp.resume = pos
	hp.savedAt = now
	_ = h.hist.RecordProgress(hp.series, ep, pos)
}

// hgStopPlayer checkpoints progress, releases active media, clears hp, and exits theater.
func (a *app) hgStopPlayer() {
	if a.hg != nil && a.hg.player != nil {
		hp := a.hg.player
		a.hgCheckpoint(true)
		hp.releasePlayer()
		hp.listGen++
		hp.closePrefetch()
		hp.dmGen++
		a.hg.player = nil
	}
	a.mainVideo.hide()
	a.mainVideo.player = nil
	if a.theater || a.fullScreen() {
		a.setFullScreen(false)
	}
}

func (a *app) hgTogglePlay() {
	if a.hg == nil || a.hg.player == nil {
		return
	}
	hp := a.hg.player
	if hp.pl.vp == nil {
		return
	}
	if hp.finished {
		// Replay current episode from start when user clicks play after ending.
		hp.finished = false
		hp.seekMedia(0)
		hp.paused = false
		hp.playMedia(a.loudness(), a.muted)
		return
	}
	hp.paused = !hp.paused
	if hp.paused {
		hp.pauseMedia()
		a.hgCheckpoint(true)
	} else {
		volumeNow.Store(a.loudness())
		hp.playMedia(a.loudness(), a.muted)
	}
}

func (a *app) hgStepEpisode(delta int) {
	if a.hg == nil || a.hg.player == nil || delta == 0 {
		return
	}
	hp := a.hg.player
	now := time.Now()
	if now.Sub(hp.lastNav) < 400*time.Millisecond {
		return
	}
	next := hp.epIdx + delta
	if next < 0 || next >= len(hp.episodes) {
		return
	}
	hp.lastNav = now
	a.slides++ // as the feed's step slides
	a.slide = &slide{n: a.slides, from: hp.coverOf(hp.epIdx), to: hp.epKey(next), dir: delta}
	a.hgPlayEpisode(next)
}

func (a *app) hgSeek(hp *hgPlayer, target time.Duration) {
	if hp == nil || hp.pl.vp == nil {
		return
	}
	dur := hp.duration()
	if dur > time.Second {
		target = max(0, min(target, dur-time.Second))
	} else {
		target = max(0, target)
	}
	hp.seekMedia(target)
	hp.resetDanmakuLayout()
	a.hgCheckpoint(true)
}

func (a *app) hgPrefetchEpisode(hp *hgPlayer, ep hg.Episode) {
	h := a.hg
	if h == nil || h.svc == nil || hp == nil || ep.ID == "" {
		return
	}
	hp.closePrefetch()
	hp.prefetchBusy = true
	hp.prefetchEpID = ep.ID
	gen, epID, svc, quality := hp.prefetchGen, ep.ID, h.svc, hp.quality
	prewarm := a.win != nil && a.openHGStream == nil
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		pb, err := svc.ResolvePlayback(ctx, epID)
		cancel()
		var (
			warmURL string
			warm    *media.WarmVideo
		)
		if err == nil && prewarm {
			warmURL, warm = hgStartPrewarm(pb, quality)
		}
		a.post(func() {
			if a.hg != h || h.player != hp || hp.prefetchGen != gen {
				warm.Close()
				return
			}
			hp.prefetchBusy = false
			if err == nil {
				cp := pb
				hp.prefetchPB = &cp
				hp.prefetchURL = warmURL
				hp.prefetchWarm = warm
			} else {
				warm.Close()
			}
		})
	}()
}

// hgTick advances playback housekeeping once per frame: seek settling,
// periodic checkpointing, 75% next-episode pre-resolve, danmaku paging,
// and EOF auto-next.
func (a *app) hgTick(hp *hgPlayer, at, dur time.Duration) {
	h := a.hg
	if h == nil || hp == nil || hp.pl.vp == nil {
		return
	}
	if hp.seeking {
		if hp.isSeeking() {
			return
		}
		hp.seeking = false
		hp.lastAt = at
		return
	}
	if hp.paused || hp.finished {
		return
	}

	// EOF / episode completion detection.
	if hp.ended(at, dur) {
		hp.finished = true
		if ep, ok := hp.currentEpisode(); ok && h.hist != nil && hp.series.ID != "" {
			_ = h.hist.ClearEpisodePosition(hp.series.ID, ep.ID)
		}
		if hp.epIdx+1 < len(hp.episodes) {
			nextIdx := hp.epIdx + 1
			a.post(func() {
				if a.hg == h && h.player == hp && hp.epIdx+1 == nextIdx {
					hp.lastNav = time.Time{}
					a.hgStepEpisode(1)
				}
			})
		} else {
			hp.paused = true
			hp.pauseMedia()
			a.toast = "全剧播放完毕"
		}
		return
	}

	hp.lastAt = at
	hp.resume = at

	// Periodic progress checkpointing every 2s.
	a.hgCheckpoint(false)

	// 75% pre-resolve for the next episode (at most once per next episode).
	if dur > 0 && at >= dur*3/4 && hp.epIdx+1 < len(hp.episodes) {
		nextEp := hp.episodes[hp.epIdx+1]
		if nextEp.ID != "" && !hp.prefetchBusy && hp.prefetchEpID != nextEp.ID {
			a.hgPrefetchEpisode(hp, nextEp)
		}
	}

	// Load more danmaku if paginated and playhead nears the end of loaded bullets.
	if !a.dmOff && !hp.dmLoading {
		if ep, ok := hp.currentEpisode(); ok {
			if !hp.dmLoaded {
				a.hgLoadDanmaku(hp, ep, at.Milliseconds(), "")
			} else if hp.dmHasMore {
				var maxAt time.Duration
				if len(hp.danmaku) > 0 {
					maxAt = hp.danmaku[len(hp.danmaku)-1].At
				}
				if at+15*time.Second >= maxAt {
					a.hgLoadDanmaku(hp, ep, at.Milliseconds(), hp.dmCursor)
				}
			}
		}
	}
}

func (a *app) hgLoadDanmaku(hp *hgPlayer, ep hg.Episode, startMs int64, cursor string) {
	h := a.hg
	if h == nil || h.svc == nil || hp == nil || hp.dmLoading || ep.ID == "" {
		return
	}
	hp.dmLoading = true
	gen, seriesID, epID, svc := hp.dmGen, hp.series.ID, ep.ID, h.svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		page, err := svc.ListDanmaku(ctx, seriesID, epID, startMs, cursor)
		cancel()
		a.post(func() {
			if a.hg != h || h.player != hp || hp.dmGen != gen {
				return
			}
			hp.dmLoading = false
			hp.dmLoaded = true
			if err != nil {
				hp.dmHasMore = false
				return
			}
			if len(page.Items) > 0 {
				existing := make(map[hg.Danmaku]struct{}, len(hp.danmaku))
				for _, d := range hp.danmaku {
					existing[d] = struct{}{}
				}
				for _, d := range page.Items {
					if strings.TrimSpace(d.Text) == "" {
						continue
					}
					if _, dup := existing[d]; dup {
						continue
					}
					existing[d] = struct{}{}
					hp.danmaku = append(hp.danmaku, d)
				}
				sort.SliceStable(hp.danmaku, func(i, j int) bool {
					return hp.danmaku[i].At < hp.danmaku[j].At
				})
			}
			if page.Total > hp.dmTotal {
				hp.dmTotal = page.Total
			}
			nextCursor := page.Cursor
			hp.dmHasMore = page.HasMore && nextCursor != "" && nextCursor != cursor
			hp.dmCursor = nextCursor
		})
	}()
}

// hgPlayerView renders the Hongguo native playback stage beside a collapsible
// episode drawer (and supports full-window theater mode).
func (a *app) hgPlayerView(c *ui.Context, h *hgApp, p *hgPage) {
	hp := h.player
	if hp == nil {
		// Recover cleanly if a page stack entry exists without an active player controller.
		hp = &hgPlayer{
			series:   p.series,
			episodes: slices.Clone(p.episodes),
			epIdx:    p.epIdx,
			epGroup:  p.epIdx / hgEpisodeGroupSize,
			quality:  a.quality,
		}
		h.player = hp
	}
	if !hp.drawerInit {
		winW, _ := c.Size()
		hp.drawerOpen = winW >= hgDrawerDefaultMinW
		hp.drawerInit = true
	}

	row := ui.Row(c.Key("hg-player-row")).Grow(1).AlignItems(ui.Stretch)
	if !a.theater {
		row.Padding(0, 16, 16, 0).Gap(16)
	}
	row.Children(func() {
		a.hgStage(c, h, hp)
		if hp.drawerOpen {
			if a.theater {
				ui.Row(c.Key("hg-theater-drawer")).Padding(16, 16, 16, 0).AlignItems(ui.Stretch).Children(func() {
					a.hgEpisodeDrawer(c, h, hp)
				})
			} else {
				a.hgEpisodeDrawer(c, h, hp)
			}
		}
	})
}

func (a *app) hgStage(c *ui.Context, h *hgApp, hp *hgPlayer) {
	radius := float32(12)
	if a.theater && !hp.drawerOpen {
		radius = 0
	}
	box := ui.Box(c.Key("hg-stage")).Grow(1).Radius(radius).Clip().Background(ui.RGB(0, 0, 0))
	if nativePlayer(hp.pl.vp) {
		box.Background(ui.Color{})
	}
	box.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputScroll || ev.Momentum || ev.DY == 0 {
			return false
		}
		if ev.Precise {
			same := ev.Time.Sub(hp.swipeAt) < 200*time.Millisecond
			hp.swipeAt = ev.Time
			if same {
				return true
			}
		}
		if ev.DY > 0 {
			a.hgStepEpisode(1)
		} else {
			a.hgStepEpisode(-1)
		}
		return true
	})

	box.Children(func() {
		picture := ui.Box(c.Key("hg-picture")).Absolute().Top(0).Left(0).Right(0).Bottom(0).HandleInput(func(ev ui.InputEvent) bool {
			switch {
			case ev.Kind == ui.InputPointerDown && ev.Button == 0:
				return true
			case ev.Kind == ui.InputPointerUp && !ev.Canceled && ev.Button == 0:
				a.hgTogglePlay()
				return true
			}
			return false
		})

		bounds := box.Bounds()
		now := c.Now()
		px, py, over := box.PointerPosition()
		overBar := over && bounds.H > 0 && py >= bounds.H-72
		overOverlay := over && !a.clear && hp.hud.overlayHovered
		pinned := hp.paused || hp.dragging || hp.loading || hp.listLoading || hp.dashUnsupported ||
			(len(hp.episodes) == 0 && (hp.series.IsUpcoming() || hp.listLoaded)) ||
			hp.errInfo.Kind != hg.ErrKindNone || hp.listErr.Kind != hg.ErrKindNone ||
			hp.qualityOpen || hp.speedOpen || hp.volumeOpen || hp.dmSettingsOpen || overBar || overOverlay
		epKey := hp.epKey(hp.epIdx)
		wantControls, hideIn := hp.hud.want(epKey, now, px, py, over, pinned)
		if hideIn > 0 {
			c.After(hideIn)
		}
		targetAlpha := onOff(wantControls, float32(1), float32(0))
		alpha := box.AnimateTo("hg-controls-alpha", targetAlpha, ui.Animation{Duration: 200 * time.Millisecond})
		if !wantControls {
			box.Cursor(ui.CursorNone)
			picture.Cursor(ui.CursorNone)
		}

		picture.Children(func() {
			if hp.pl.vp != nil {
				a.mainVideo.video(c, hp.pl.vp, false, ui.Element{}, radius).Fill().Fit(ui.Contain)
			}
			if !videoPictureReady(hp.pl.vp) {
				a.hgPlaybackCover(c, h, hp.coverOf(hp.epIdx))
			}
			a.slideIn(c, epKey, hp.coverOf(hp.epIdx), h.img, bounds.H)
			if hp.paused && hp.pl.vp != nil && !hp.dashUnsupported && hp.errInfo.Kind == hg.ErrKindNone {
				ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().PassThrough().Children(func() {
					ui.Icon(c, iconPlay).Size(60, 60).TextColor(white).Opacity(0.85)
				})
			}
		})

		at := hp.at()
		dur := hp.duration()
		a.hgTick(hp, at, dur)

		if !a.clear {
			a.danmakuLayer(c, "hg-danmaku-layer", !hp.paused, func(w, h float32) ([]dmFlying, time.Duration) {
				flying, next := flyDanmakuArea(a, &hp.dm, hp.danmaku, func(d hg.Danmaku) dy.Danmaku { return dy.Danmaku{At: d.At, Text: d.Text} }, hp.at(), w, h)
				if next > 0 {
					next = time.Duration(float64(next) / a.playSpeed())
				}
				return flying, next
			})
		}
		if hp.pl.vp != nil && !hp.paused {
			c.After(500 * time.Millisecond)
		}

		a.hgStageStatusOverlay(c, hp)

		overlayHovered := false
		if !a.clear {
			overlayHovered = a.hgStageTopOverlay(c, h, hp, alpha, wantControls)
		}
		hp.hud.overlayHovered = overlayHovered
		if overOverlay && !overlayHovered {
			c.After(controlsHideAfter)
		}

		a.hgControls(c, hp, at, dur, bounds.W, alpha, wantControls)
	})
}

func (a *app) hgPlaybackCover(c *ui.Context, h *hgApp, url string) {
	ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(ui.RGB(0, 0, 0)).PassThrough().Children(func() {
		if h != nil && h.img != nil && url != "" {
			if bmp := h.img.get(url); bmp != nil {
				ui.Image(c, bmp).Absolute().Top(0).Left(0).Right(0).Bottom(0).Fit(ui.Contain).PassThrough()
			}
		}
	})
}

func (a *app) hgStageTopOverlay(c *ui.Context, h *hgApp, hp *hgPlayer, alpha float32, active bool) bool {
	if alpha <= 0.01 && !active {
		return false
	}
	fav := h != nil && h.hist != nil && hp.series.ID != "" && h.hist.IsFavorited(hp.series.ID)
	epNum := hp.episodeNumber(hp.epIdx)
	row := ui.Row(c.Key("hg-stage-top")).Absolute().Top(0).Left(0).Right(0).Padding(14, 16, 28, 16).Gap(10).
		AlignItems(ui.Center).Opacity(alpha).
		Gradient(ui.RGBA(0, 0, 0, 0.56), ui.RGBA(0, 0, 0, 0), 180)
	if !active {
		row.PassThrough()
	}
	hovered := false
	row.Children(func() {
		if a.theater {
			back := iconButton(c.Key("hg-theater-back"), iconBack, 18, white, "退出网页全屏")
			if back.Hovered() {
				hovered = true
			}
			if back.Clicked() {
				a.setFullScreen(false)
			}
		}
		ui.Text(c, hgFirstNonEmpty(hp.series.Title, "剧集播放")).FontSize(15).FontWeight(700).TextColor(white).SingleLine().Shrink(1).MinWidth(0)
		if len(hp.episodes) > 0 {
			ui.Row(c).Height(24).PaddingX(9).Radius(12).AlignItems(ui.Center).Background(ui.RGBA(255, 255, 255, 0.16)).Children(func() {
				ui.Text(c, fmt.Sprintf("第 %d 集", epNum)).FontSize(12).FontWeight(600).TextColor(white).SingleLine()
			})
		}
		if hp.series.ID != "" {
			favLabel := onOff(fav, "已追剧", "追剧")
			fb := ui.ButtonBase(c.Key("hg-stage-fav")).Height(28).PaddingX(10).Gap(5).Radius(14).
				AlignItems(ui.Center).FocusRing(false).Label(favLabel)
			if fb.Hovered() {
				hovered = true
			}
			if fav {
				fb.Background(pal.accent.Alpha(0.28)).Border(1, pal.accent.Alpha(0.65))
			} else {
				fb.Background(onOff(fb.Hovered(), ui.RGBA(255, 255, 255, 0.22), ui.RGBA(255, 255, 255, 0.12)))
			}
			fb.Children(func() {
				ui.Icon(c, onOff(fav, iconHeartFill, iconHeart)).Size(13, 13).TextColor(onOff(fav, pal.accent, white))
				ui.Text(c, favLabel).FontSize(12).FontWeight(600).TextColor(white).SingleLine()
			})
			if fb.Clicked() {
				a.hgToggleFavorite(hp.series)
			}
		}
	})
	return hovered
}

func (a *app) hgStageStatusOverlay(c *ui.Context, hp *hgPlayer) {
	if hp.dashUnsupported {
		ui.Column(c.Key("hg-dash-notice")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Padding(24).Children(func() {
			ui.Column(c).MaxWidth(420).Padding(20, 24).Gap(12).Radius(14).AlignItems(ui.Center).
				Background(ui.RGBA(20, 22, 30, 0.9)).Border(1, ui.RGBA(255, 255, 255, 0.16)).Children(func() {
				ui.Text(c, hgDashUnsupportedMsg).FontSize(14).FontWeight(600).TextColor(white).TextAlign(ui.Center)
				if ui.Button(c.Key("hg-dash-back"), "返回").Clicked() {
					a.hgPop()
				}
			})
		})
		return
	}

	var errInfo hg.ErrorInfo
	var onRetry func()
	switch {
	case hp.listErr.Kind != hg.ErrKindNone:
		errInfo = hp.listErr
		onRetry = func() { a.hgFetchPlayerEpisodes(hp) }
	case hp.errInfo.Kind != hg.ErrKindNone:
		errInfo = hp.errInfo
		onRetry = a.hgRetryPlayback
	case hp.pl.vp != nil && hp.pl.vp.Err() != nil && !errors.Is(hp.pl.vp.Err(), io.EOF):
		errInfo = hg.ClassifyError(hp.pl.vp.Err())
		onRetry = a.hgRetryPlayback
	}
	if errInfo.Kind != hg.ErrKindNone {
		ui.Column(c.Key("hg-player-err")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Padding(24).Children(func() {
			a.hgErrorBanner(c, "player", errInfo, onRetry)
		})
		return
	}

	if len(hp.episodes) == 0 && (hp.series.IsUpcoming() || (hp.listLoaded && !hp.listLoading)) {
		msg := hgEmptyEpisodesNotice(hp.series, len(hp.seasons) > 1)
		ui.Column(c.Key("hg-player-empty")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Padding(24).Children(func() {
			ui.Column(c).MaxWidth(420).Padding(20, 24).Gap(12).Radius(14).AlignItems(ui.Center).
				Background(ui.RGBA(20, 22, 30, 0.9)).Border(1, ui.RGBA(255, 255, 255, 0.16)).Children(func() {
				ui.Text(c, msg).FontSize(14).FontWeight(600).TextColor(white).TextAlign(ui.Center)
				if ui.Button(c.Key("hg-empty-back"), "返回").Clicked() {
					a.hgPop()
				}
			})
		})
		return
	}

	if hp.listLoading || hp.loading {
		msg := "正在解析播放地址…"
		if hp.listLoading {
			msg = "正在加载剧集列表…"
		}
		ui.Column(c.Key("hg-player-loading")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().PassThrough().Children(func() {
			ui.Row(c).Padding(10, 16).Radius(10).Background(ui.RGBA(0, 0, 0, 0.62)).PassThrough().Children(func() {
				ui.Text(c, msg).FontSize(13).TextColor(white).PassThrough()
			})
		})
	}
}

func (a *app) hgControls(c *ui.Context, hp *hgPlayer, at, dur time.Duration, w, alpha float32, active bool) {
	if alpha <= 0.01 && !active {
		return
	}
	col := ui.Column(c.Key("hg-controls")).Absolute().Left(0).Right(0).Bottom(0).
		Opacity(alpha).Translate(0, (1-alpha)*8).
		Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.65), 180)
	if !active {
		col.PassThrough()
	}
	col.Children(func() {
		a.hgProgress(c, hp, at, dur)
		ui.Row(c).Height(44).PaddingX(10).Gap(4).AlignItems(ui.Center).Children(func() {
			icon, label := iconPause, "暂停"
			if hp.paused {
				icon, label = iconPlay, "播放"
			}
			if iconButton(c.Key("hg-toggle"), icon, 16, white, label).Clicked() {
				a.hgTogglePlay()
			}
			prev := iconButton(c.Key("hg-prev-ep"), iconBack, 15, onOff(hp.epIdx > 0, white, vid.faint), "上一集")
			if hp.epIdx <= 0 {
				prev.Disabled(true)
			} else if prev.Clicked() {
				hp.lastNav = time.Time{}
				a.hgStepEpisode(-1)
			}
			next := iconButton(c.Key("hg-next-ep"), iconNext, 15, onOff(hp.epIdx+1 < len(hp.episodes), white, vid.faint), "下一集")
			if hp.epIdx+1 >= len(hp.episodes) {
				next.Disabled(true)
			} else if next.Clicked() {
				hp.lastNav = time.Time{}
				a.hgStepEpisode(1)
			}

			shown := at
			if hp.dragging {
				shown = time.Duration(float64(dur) * float64(hp.dragAt))
			}
			ui.Text(c, clock(shown)+" / "+clock(dur)).FontSize(12).FontWeight(500).TextColor(ui.RGBA(255, 255, 255, 0.85)).Margin(0, 6, 0, 4).SingleLine()

			dmBtn := chip(c.Key("hg-danmaku"), "弹", !a.dmOff).Label(onOff(a.dmOff, "开启弹幕", "关闭弹幕"))
			if dmBtn.Clicked() {
				a.dmOff = !a.dmOff
				if !a.dmOff && !hp.dmLoaded && !hp.dmLoading {
					if ep, ok := hp.currentEpisode(); ok {
						a.hgLoadDanmaku(hp, ep, at.Milliseconds(), "")
					}
				}
			}
			a.danmakuSettingsButton(c, "hg-dm-set", &hp.dmSettingsOpen, func() {
				hp.qualityOpen, hp.speedOpen, hp.volumeOpen = false, false, false
			})

			ui.Box(c).Grow(1)

			if (w == 0 || w >= 620) && toggleChip(c.Key("hg-clear"), "清屏", a.clear).Clicked() {
				a.clear = !a.clear
			}
			a.hgQualityMenu(c, hp)
			a.hgSpeedMenu(c, hp)
			a.hgVolumeButton(c, hp)

			if iconButton(c.Key("hg-theater"), onOff(a.theater && !a.fullScreen(), iconUnfill, iconFillWindow), 16, white, onOff(a.theater && !a.fullScreen(), "退出网页全屏", "网页全屏")).Clicked() {
				if a.fullScreen() {
					a.win.SetFullScreen(false)
				}
				a.theater = !a.theater || a.fullScreen()
			}
			full := a.fullScreen()
			if iconButton(c.Key("hg-full"), onOff(full, iconShrink, iconExpand), 16, white, onOff(full, "退出全屏", "全屏")).Clicked() {
				a.setFullScreen(!full)
			}
			if chip(c.Key("hg-comments-toggle"), "评论", hp.drawerOpen && hp.drawerTab == hgDrawerTabComments).Label("评论").Clicked() {
				if hp.drawerOpen && hp.drawerTab == hgDrawerTabComments {
					hp.drawerOpen = false
				} else {
					hp.drawerOpen = true
					hp.drawerTab = hgDrawerTabComments
					a.hgEnsureActiveComments(hp)
				}
			}
			if chip(c.Key("hg-drawer-toggle"), "选集", hp.drawerOpen && hp.drawerTab == hgDrawerTabEpisodes).Label("选集").Clicked() {
				if hp.drawerOpen && hp.drawerTab == hgDrawerTabEpisodes {
					hp.drawerOpen = false
				} else {
					hp.drawerOpen = true
					hp.drawerTab = hgDrawerTabEpisodes
				}
			}
		})
	})
}

func (a *app) hgProgress(c *ui.Context, hp *hgPlayer, at, dur time.Duration) {
	bar := ui.Box(c.Key("hg-progress")).Height(18).Margin(0, 12).Cursor(ui.CursorPointer).Label("进度")
	w := bar.Bounds().W
	frac := func(x float32) float32 {
		if w <= 0 {
			return 0
		}
		return max(0, min(1, x/w))
	}
	bar.HandleInput(func(ev ui.InputEvent) bool {
		switch {
		case ev.Kind == ui.InputPointerDown && ev.Button == 0 && dur > 0 && hp.pl.vp != nil:
			hp.dragging, hp.dragAt = true, frac(ev.X)
			return true
		case ev.Kind == ui.InputPointerMove && hp.dragging:
			hp.dragAt = frac(ev.X)
			return true
		case ev.Kind == ui.InputPointerUp && hp.dragging:
			hp.dragging = false
			if !ev.Canceled {
				a.hgSeek(hp, time.Duration(float64(dur)*float64(frac(ev.X))))
			}
			return true
		}
		return false
	})

	f := float32(0)
	if dur > 0 {
		f = min(1, float32(at)/float32(dur))
	}
	if hp.dragging {
		f = hp.dragAt
	}
	px, _, over := bar.PointerPosition()
	hot := over || hp.dragging
	pointer := frac(px)
	if hp.dragging {
		pointer = hp.dragAt
	}
	bar.Draw(func(p *ui.Painter, r ui.Rect) {
		f := f
		if hp.pl.vp != nil && !hp.dragging && dur > 0 { // the clock as it paints, gliding without building the view
			f = min(1, float32(hp.at())/float32(dur))
			if !hp.paused {
				p.AnimationFrameRate(progressFPS)
			}
		}
		h := float32(3)
		if hot {
			h = 5
		}
		y := r.Y + r.H/2 - h/2
		p.Fill(ui.Rect{X: r.X - 1, Y: y - 1, W: r.W + 2, H: h + 2}, ui.RGBA(0, 0, 0, 0.25), h/2+1)
		p.Fill(ui.Rect{X: r.X, Y: y, W: r.W, H: h}, ui.RGBA(255, 255, 255, 0.3), h/2)
		if hot && !hp.dragging && pointer > f {
			p.Fill(ui.Rect{X: r.X, Y: y, W: r.W * pointer, H: h}, ui.RGBA(255, 255, 255, 0.5), h/2)
		}
		p.Fill(ui.Rect{X: r.X, Y: y, W: r.W * f, H: h}, pal.accent, h/2)
		if hot {
			const k = 13
			p.Fill(ui.Rect{X: r.X + r.W*f - k/2, Y: y + h/2 - k/2, W: k, H: k}, white, k/2)
		}
	})
	if hot && dur > 0 && w > 0 {
		bar.Children(func() {
			const tipW = 56
			x := max(0, min(w-tipW, w*pointer-tipW/2))
			ui.Box(c.Key("hg-tip")).Absolute().Bottom(20).Left(x).Width(tipW).Height(22).Radius(6).Center().
				Background(ui.RGBA(0, 0, 0, 0.72)).PassThrough().Children(func() {
				ui.Text(c, clock(time.Duration(float64(dur)*float64(pointer)))).FontSize(11.5).FontWeight(600).TextColor(white)
			})
		})
	}
}

func (a *app) hgQualityMenu(c *ui.Context, hp *hgPlayer) {
	cur := hp.quality
	if cur == "" {
		cur = "智能"
	}
	opts := hp.qualityOptions()
	shown := cur
	if cur != "智能" {
		found := false
		for _, opt := range opts {
			if strings.EqualFold(opt, cur) {
				shown = opt
				found = true
				break
			}
		}
		if !found {
			shown = "智能"
		}
	}
	b := chip(c.Key("hg-quality"), shown, shown != "智能")
	b.Label("清晰度：" + shown)
	if b.Clicked() {
		hp.qualityOpen = !hp.qualityOpen
		if hp.qualityOpen {
			hp.speedOpen, hp.volumeOpen, hp.dmSettingsOpen = false, false, false
		}
	}
	barMenu(c, "hg-quality-menu", b, &hp.qualityOpen, 96, func() {
		for _, n := range opts {
			if choice(c, n, strings.EqualFold(n, shown)).Clicked() {
				hp.qualityOpen = false
				a.hgSetQuality(n)
			}
		}
	})
}

func (a *app) hgSpeedMenu(c *ui.Context, hp *hgPlayer) {
	label := "倍速"
	if a.speed != 0 && a.speed != 1 {
		label = strconv.FormatFloat(a.speed, 'f', -1, 64) + "x"
	}
	b := chip(c.Key("hg-speed"), label, a.speed != 0 && a.speed != 1)
	b.Label("倍速：" + label)
	if b.Clicked() {
		hp.speedOpen = !hp.speedOpen
		if hp.speedOpen {
			hp.qualityOpen, hp.volumeOpen, hp.dmSettingsOpen = false, false, false
		}
	}
	barMenu(c, "hg-speed-menu", b, &hp.speedOpen, 84, func() {
		for _, sp := range speeds {
			name := strconv.FormatFloat(sp, 'f', -1, 64) + "x"
			if choice(c, name, sp == a.speed || a.speed == 0 && sp == 1).Clicked() {
				hp.speedOpen = false
				a.setSpeed(sp)
			}
		}
	})
}

func (a *app) hgVolumeButton(c *ui.Context, hp *hgPlayer) {
	icon := iconVolume
	if a.muted || a.volume == 0 {
		icon = iconMuted
	}
	b := iconButton(c.Key("hg-volume"), icon, 17, white, "音量")
	if b.Clicked() {
		hp.volumeOpen = !hp.volumeOpen
		if hp.volumeOpen {
			hp.qualityOpen, hp.speedOpen, hp.dmSettingsOpen = false, false, false
		}
	}
	barMenu(c, "hg-volume-menu", b, &hp.volumeOpen, 48, func() {
		ui.Text(c, fmt.Sprint(int(math.Round(a.loudness()*100)))).FontSize(12).TextColor(vid.text).AlignSelf(ui.Center).Margin(4, 0)
		const knob, track = 14, 4
		v := a.volume
		if a.muted {
			v = 0
		}
		sl := ui.SliderBase(c.Key("hg-volume-slider"), &v, 0, 1).Vertical().Size(knob+12, 110).PaddingY(knob / 2).Step(0.01).
			AlignSelf(ui.Center).FocusRing(false).Label("音量大小")
		if sl.Changed() {
			a.setVolume(v, false)
		}
		sl.Draw(func(p *ui.Painter, r ui.Rect) {
			top, foot := r.Y+knob/2, r.Y+r.H-knob/2
			x := r.X + (r.W-track)/2
			y := foot - (foot-top)*float32(v)
			p.Fill(ui.Rect{X: x, Y: top - track/2, W: track, H: foot - top + track}, ui.RGBA(255, 255, 255, 0.2), track/2)
			p.Fill(ui.Rect{X: x, Y: y, W: track, H: foot - y + track/2}, pal.accent, track/2)
			p.Fill(ui.Rect{X: r.X + (r.W-knob)/2, Y: y - knob/2, W: knob, H: knob}, white, knob/2)
		})
		m := iconButton(c.Key("hg-mute"), onOff(a.muted, iconMuted, iconVolume), 15, vid.text, onOff(a.muted, "取消静音", "静音")).AlignSelf(ui.Center)
		if m.Clicked() {
			if a.volume == 0 {
				a.volume = 0.5
			}
			a.setVolume(a.volume, !a.muted)
		}
	})
}

func (a *app) hgEpisodeDrawer(c *ui.Context, h *hgApp, hp *hgPlayer) {
	var (
		histEntry hg.WatchEntry
		hasHist   bool
		fav       bool
	)
	if h != nil && h.hist != nil && hp.series.ID != "" {
		histEntry, hasHist = h.hist.Get(hp.series.ID)
		fav = h.hist.IsFavorited(hp.series.ID)
	}

	ui.Column(c.Key("hg-drawer")).Width(hgDrawerWidth).Radius(12).Clip().Background(pal.raised).Border(1, pal.cardBorder).Children(func() {
		// Series header
		ui.Column(c.Key("hg-drawer-head")).Padding(14, 14, 10, 16).Gap(8).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, hgFirstNonEmpty(hp.series.Title, "选集")).FontSize(15).FontWeight(700).TextColor(pal.strong).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if iconButton(c.Key("hg-drawer-close"), iconClose, 15, pal.muted, "收起选集").Clicked() {
					hp.drawerOpen = false
				}
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				meta := hp.series.DisplayStatus()
				if meta == "" && len(hp.episodes) > 0 {
					meta = fmt.Sprintf("全 %d 集", len(hp.episodes))
				}
				if meta != "" {
					ui.Text(c, meta).FontSize(12).TextColor(pal.muted).SingleLine().Grow(1)
				} else {
					ui.Spacer(c)
				}
				if hp.series.ID != "" {
					favLabel := onOff(fav, "已追剧", "追剧")
					fb := ui.ButtonBase(c.Key("hg-drawer-fav")).Height(26).PaddingX(10).Gap(4).Radius(13).
						AlignItems(ui.Center).FocusRing(false).Label(favLabel)
					if fav {
						fb.Background(pal.accent.Alpha(0.14)).Border(1, pal.accent.Alpha(0.45))
					} else {
						fb.Background(onOff(fb.Hovered(), pal.pillHoverBg, pal.pillBg)).Border(1, pal.pillBorder)
					}
					fb.Children(func() {
						ui.Icon(c, onOff(fav, iconHeartFill, iconHeart)).Size(12, 12).TextColor(onOff(fav, pal.accent, pal.muted))
						ui.Text(c, favLabel).FontSize(12).FontWeight(600).TextColor(onOff(fav, pal.accent, pal.text)).SingleLine()
					})
					if fb.Clicked() {
						a.hgToggleFavorite(hp.series)
					}
				}
			})
		})

		ui.Box(c).Height(1).Background(pal.hairline)

		// Drawer tabs: 选集 / 评论 / 弹幕
		cmLabel := "评论"
		if hp.epComments != nil {
			switch {
			case hp.epComments.total > 0:
				cmLabel = fmt.Sprintf("评论 %s", count(int(hp.epComments.total)))
			case len(hp.epComments.items) > 0:
				cmLabel = fmt.Sprintf("评论 %d", len(hp.epComments.items))
			}
		}
		dmLabel := "弹幕"
		switch {
		case hp.dmTotal > 0:
			dmLabel = fmt.Sprintf("弹幕 %s", count(hp.dmTotal))
		case len(hp.danmaku) > 0:
			dmLabel = fmt.Sprintf("弹幕 %d", len(hp.danmaku))
		}
		ui.Row(c.Key("hg-drawer-tabs")).Height(40).PaddingX(14).Gap(16).AlignItems(ui.Center).Children(func() {
			tabs := []struct {
				idx   int
				key   string
				label string
			}{
				{idx: hgDrawerTabEpisodes, key: "episodes", label: "选集"},
				{idx: hgDrawerTabComments, key: "comments", label: cmLabel},
				{idx: hgDrawerTabDanmaku, key: "danmaku", label: dmLabel},
			}
			for _, tb := range tabs {
				on := hp.drawerTab == tb.idx
				b := ui.ButtonBase(c.Key("hg-dtab:"+tb.key)).Column().Gap(4).AlignItems(ui.Center).FocusRing(false).Label(tb.label)
				b.Children(func() {
					col := pal.muted
					if on {
						col = pal.strong
					} else if b.Hovered() {
						col = pal.text
					}
					ui.Text(c, tb.label).FontSize(13.5).FontWeight(onOff(on, 700, 500)).TextColor(col).SingleLine()
					bar := ui.Box(c).Width(18).Height(2.5).Radius(2)
					if on {
						bar.Background(pal.accent)
					}
				})
				if b.Clicked() {
					hp.drawerTab = tb.idx
					switch tb.idx {
					case hgDrawerTabComments:
						a.hgEnsureActiveComments(hp)
					case hgDrawerTabDanmaku:
						if !hp.dmLoaded && !hp.dmLoading {
							if ep, ok := hp.currentEpisode(); ok {
								a.hgLoadDanmaku(hp, ep, 0, "")
							}
						}
					}
				}
			}
		})
		ui.Box(c).Height(1).Background(pal.hairline)

		switch hp.drawerTab {
		case hgDrawerTabComments:
			a.hgCommentsDrawerBody(c, h, hp)
			return
		case hgDrawerTabDanmaku:
			a.hgDanmakuDrawerBody(c, hp)
			return
		}

		// Multi-season pills
		if len(hp.seasons) > 1 {
			ui.ScrollHorizontal(c.Key("hg-drawer-seasons")).Height(34+barGutter).Padding(6, 14, barGutter, 14).Gap(6).AlignItems(ui.Center).Children(func() {
				for i, sn := range hp.seasons {
					label := hgSeasonPillLabel(sn, i)
					on := sn.SeriesID == hp.series.ID
					if a.hgSeasonPill(c, fmt.Sprintf("pseason:%s:%d", sn.SeriesID, i), label, on, sn.IsUpcoming()) {
						a.hgPlayerSwitchSeason(sn)
					}
				}
			})
		}

		// Grouped episode tabs
		if len(hp.episodes) > hgEpisodeGroupSize {
			groups := (len(hp.episodes) + hgEpisodeGroupSize - 1) / hgEpisodeGroupSize
			if hp.epGroup < 0 || hp.epGroup >= groups {
				hp.epGroup = hp.epIdx / hgEpisodeGroupSize
			}
			ui.ScrollHorizontal(c.Key("hg-drawer-groups")).Height(34+barGutter).Padding(6, 14, barGutter, 14).Gap(6).AlignItems(ui.Center).Children(func() {
				for g := range groups {
					start := g * hgEpisodeGroupSize
					end := min(len(hp.episodes), start+hgEpisodeGroupSize)
					lo := hp.episodeNumber(start)
					hi := hp.episodeNumber(end - 1)
					label := fmt.Sprintf("%d-%d", lo, hi)
					if a.hgPill(c, fmt.Sprintf("pepg:%d", g), label, hp.epGroup == g) {
						hp.epGroup = g
					}
				}
			})
		}

		// Episode grid
		ui.Scroll(c.Key("hg-drawer-scroll")).Grow(1).Padding(10, 14, 16, 14).Children(func() {
			if len(hp.episodes) == 0 {
				switch {
				case hp.series.IsUpcoming():
					ui.Text(c, hgEmptyEpisodesNotice(hp.series, len(hp.seasons) > 1)).FontSize(12.5).TextColor(pal.faint)
				case hp.listErr.Kind != hg.ErrKindNone:
					ui.Text(c, "剧集列表加载失败").FontSize(12.5).TextColor(pal.faint)
				case hp.listLoading:
					ui.Text(c, "正在加载剧集列表…").FontSize(12.5).TextColor(pal.faint)
				default:
					ui.Text(c, "暂无可播放剧集").FontSize(12.5).TextColor(pal.faint)
				}
				return
			}
			groups := (len(hp.episodes) + hgEpisodeGroupSize - 1) / hgEpisodeGroupSize
			if hp.epGroup < 0 || hp.epGroup >= groups {
				hp.epGroup = 0
			}
			start := hp.epGroup * hgEpisodeGroupSize
			end := min(len(hp.episodes), start+hgEpisodeGroupSize)
			ui.Row(c.Key(fmt.Sprintf("hg-drawer-grid:%d", hp.epGroup))).Wrap().Gap(8).Children(func() {
				for i := start; i < end; i++ {
					ep := hp.episodes[i]
					num := hp.episodeNumber(i)
					active := i == hp.epIdx
					watched := !active && hasHist && ((ep.ID != "" && histEntry.Positions[ep.ID] > 0) || (ep.ID != "" && histEntry.EpisodeID == ep.ID))
					epLabel := fmt.Sprintf("%d", num)
					eb := ui.ButtonBase(c.Key(fmt.Sprintf("dep:%s:%d", ep.ID, i))).Width(50).Height(36).Radius(8).
						Center().FocusRing(false).Label(fmt.Sprintf("第%d集", num))
					switch {
					case active:
						eb.Background(pal.accent)
					case watched:
						eb.Background(pal.accent.Alpha(0.12)).Border(1, pal.accent.Alpha(0.35))
					case eb.Hovered():
						eb.Background(pal.hover).Border(1, pal.cardHoverBd)
					default:
						eb.Background(pal.pillBg).Border(1, pal.hairline)
					}
					eb.Children(func() {
						tc := pal.text
						if active {
							tc = white
						} else if watched {
							tc = pal.accent
						}
						ui.Text(c, epLabel).FontSize(13).FontWeight(onOff(active || watched, 700, 500)).TextColor(tc).SingleLine()
					})
					if eb.Clicked() {
						a.hgPlayEpisode(i)
					}
				}
			})
		})
	})
}

func (a *app) hgDanmakuDrawerBody(c *ui.Context, hp *hgPlayer) {
	ep, hasEp := hp.currentEpisode()
	if hasEp && !hp.dmLoaded && !hp.dmLoading {
		a.hgLoadDanmaku(hp, ep, 0, "")
	}
	at := hp.currentPos()

	// Compact toolbar: count + screen danmaku quick switch.
	ui.Row(c.Key("hg-dm-toolbar")).Padding(10, 14, 8, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		info := fmt.Sprintf("已加载 %d 条", len(hp.danmaku))
		if hp.dmTotal > len(hp.danmaku) {
			info = fmt.Sprintf("已加载 %d / %s 条", len(hp.danmaku), count(hp.dmTotal))
		}
		ui.Text(c, info).FontSize(11.5).TextColor(pal.faint).SingleLine().Grow(1)
		screenOn := !a.dmOff
		sb := ui.ButtonBase(c.Key("hg-dm-screen-toggle")).Height(24).PaddingX(8).Radius(12).
			Center().FocusRing(false).Label(onOff(screenOn, "关闭屏幕弹幕", "开启屏幕弹幕"))
		if screenOn {
			sb.Background(pal.accent.Alpha(0.14)).Border(1, pal.accent.Alpha(0.4))
		} else {
			sb.Background(onOff(sb.Hovered(), pal.hover, pal.pillBg)).Border(1, pal.hairline)
		}
		sb.Children(func() {
			ui.Text(c, onOff(screenOn, "屏幕弹幕 开", "屏幕弹幕 关")).FontSize(11).FontWeight(600).
				TextColor(onOff(screenOn, pal.accent, pal.muted)).SingleLine()
		})
		if sb.Clicked() {
			a.dmOff = !a.dmOff
		}
	})

	if len(hp.danmaku) == 0 {
		ui.Column(c.Key("hg-dm-empty")).Grow(1).Center().Padding(20).Gap(8).Children(func() {
			msg := "本集暂无弹幕"
			if hp.dmLoading {
				msg = "正在加载弹幕…"
			}
			ui.Text(c, msg).FontSize(12.5).TextColor(pal.faint)
		})
		return
	}

	nItems := len(hp.danmaku)
	totalRows := nItems + 1
	hp.dmList.KeepOffset = true
	hp.dmList.Key = func(row int) any {
		if row == nItems {
			return "dm-foot"
		}
		d := hp.danmaku[row]
		if d.ID != "" {
			return d.ID
		}
		return fmt.Sprintf("%d:%d", row, d.At.Milliseconds())
	}

	ui.List(c.Key("hg-dm-list"), &hp.dmList, totalRows, func(row int) {
		if row == nItems {
			ui.Row(c.Key("hg-dm-foot")).Padding(10, 14, 18, 14).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				switch {
				case hp.dmLoading:
					ui.Text(c, "正在加载更多弹幕…").FontSize(12).TextColor(pal.faint)
				case hp.dmHasMore && hasEp:
					if ui.Button(c.Key("hg-dm-more"), "加载更多弹幕").Clicked() {
						a.hgLoadDanmaku(hp, ep, at.Milliseconds(), hp.dmCursor)
					}
				default:
					ui.Text(c, "点击任意弹幕可跳转到对应时间").FontSize(11.5).TextColor(pal.faint)
				}
			})
			return
		}

		d := hp.danmaku[row]
		near := at > 0 && d.At >= at-2*time.Second && d.At <= at+3*time.Second
		itemBtn := ui.ButtonBase(c.Key(fmt.Sprintf("dmrow:%d", row))).Row().Padding(7, 14).Gap(10).
			AlignItems(ui.Start).FocusRing(false).Label(fmt.Sprintf("%s %s", clock(d.At), d.Text))
		switch {
		case itemBtn.Hovered():
			itemBtn.Background(pal.hover)
		case near:
			itemBtn.Background(pal.accent.Alpha(0.08))
		}
		itemBtn.Children(func() {
			ui.Box(c).MinWidth(42).Padding(2, 6).Radius(5).Center().
				Background(onOff(near, pal.accent.Alpha(0.18), pal.pillBg)).Children(func() {
				ui.Text(c, clock(d.At)).FontSize(11).FontWeight(600).
					TextColor(onOff(near, pal.accent, pal.muted)).SingleLine()
			})
			spans, pics := a.emojiSpans(d.Text, 12.5, 0)
			if len(pics) == 0 {
				ui.Text(c, d.Text).FontSize(12.5).TextColor(onOff(near, pal.strong, pal.text)).
					MaxLines(3).Grow(1).Shrink(1).MinWidth(0)
			} else {
				ui.RichText(c, spans...).FontSize(12.5).TextColor(onOff(near, pal.strong, pal.text)).
					Label(d.Text).MaxLines(3).Grow(1).Shrink(1).MinWidth(0).DrawObjects(pics.draw)
			}
			if d.Likes > 0 {
				ui.Row(c).Gap(3).AlignItems(ui.Center).Margin(2, 0, 0, 0).Children(func() {
					ui.Icon(c, iconHeart).Size(11, 11).TextColor(pal.faint)
					ui.Text(c, count(int(d.Likes))).FontSize(11).TextColor(pal.faint).SingleLine()
				})
			}
		})
		if itemBtn.Clicked() {
			a.hgSeek(hp, d.At)
		}
	}).Grow(1)
}
