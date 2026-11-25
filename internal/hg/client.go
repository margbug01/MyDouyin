package hg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/beilunyang/openhongguo/adapters/filestore"
	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/adapters/memstore"
	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
)

const (
	defaultProfileFile = "hongguo-device.json"
	remoteProfileFile  = "hongguo-device-remote.json"
	maxResponseBytes   = 8 << 20
	maxImageBytes      = 16 << 20
	maxEpisodePages    = 30
)

var _ Service = (*Client)(nil)

type Client struct {
	mu                sync.RWMutex
	cfg               Config
	profilePath       string
	remoteProfilePath string
	sdk               *sdk.Client
	remoteSDK         *sdk.Client
	imgClient         *http.Client
	commentClient     *http.Client
	commentOrigin     string
	commentDeviceID   string
	commentIID        string
}

func New(cfg Config) (*Client, error) {
	sc, remoteSC, imgClient, commentClient, pPath, rPath, err := buildRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:               cfg,
		profilePath:       pPath,
		remoteProfilePath: rPath,
		sdk:               sc,
		remoteSDK:         remoteSC,
		imgClient:         imgClient,
		commentClient:     commentClient,
		commentOrigin:     defaultCommentOrigin,
	}, nil
}

func (c *Client) Reconfigure(cfg Config) error {
	sc, remoteSC, imgClient, commentClient, pPath, rPath, err := buildRuntime(cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cfg = cfg
	c.profilePath = pPath
	c.remoteProfilePath = rPath
	c.sdk = sc
	c.remoteSDK = remoteSC
	c.imgClient = imgClient
	c.commentClient = commentClient
	if c.commentOrigin == "" {
		c.commentOrigin = defaultCommentOrigin
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) sdkClient() *sdk.Client {
	c.mu.RLock()
	sc := c.sdk
	c.mu.RUnlock()
	return sc
}

func (c *Client) playbackClients() (local *sdk.Client, remote *sdk.Client) {
	c.mu.RLock()
	local, remote = c.sdk, c.remoteSDK
	c.mu.RUnlock()
	return local, remote
}

func (c *Client) imageClient() *http.Client {
	c.mu.RLock()
	hc := c.imgClient
	c.mu.RUnlock()
	return hc
}

func buildRuntime(cfg Config) (*sdk.Client, *sdk.Client, *http.Client, *http.Client, string, string, error) {
	signerURL := strings.TrimSpace(cfg.SignerURL)
	signerToken := strings.TrimSpace(cfg.SignerToken)
	localPath, remotePath := resolveProfilePaths(cfg)

	newStore := func(p string) sdk.ProfileStore {
		if p == "" {
			return memstore.New()
		}
		return filestore.New(p)
	}

	var transport sdk.Transport
	if cfg.Direct {
		transport = newDirectTransport()
	} else {
		transport = httptransport.New()
	}

	localSC, err := sdk.NewClient(sdk.Options{
		Transport:     transport,
		ProfileStore:  newStore(localPath),
		RetryAttempts: 2,
	})
	if err != nil {
		return nil, nil, nil, nil, "", "", err
	}

	var remoteSC *sdk.Client
	if signerURL != "" {
		remoteSC, err = sdk.NewClient(sdk.Options{
			Transport:     transport,
			ProfileStore:  newStore(remotePath),
			SignerURL:     signerURL,
			SignerToken:   signerToken,
			RetryAttempts: 2,
		})
		if err != nil {
			return nil, nil, nil, nil, "", "", err
		}
	}

	imgClient := newImageHTTPClient(cfg.Direct)
	commentClient := newCommentHTTPClient(cfg.Direct)
	return localSC, remoteSC, imgClient, commentClient, localPath, remotePath, nil
}

func resolveProfilePaths(cfg Config) (localPath, remotePath string) {
	dir := strings.TrimSpace(cfg.ProfileDir)
	if dir == "" {
		return "", ""
	}
	localPath = filepath.Join(dir, defaultProfileFile)
	if strings.TrimSpace(cfg.SignerURL) != "" {
		remotePath = filepath.Join(dir, remoteProfileFile)
	}
	return localPath, remotePath
}

func newDirectTransport() sdk.Transport {
	tr := cloneDefaultHTTPTransport()
	tr.Proxy = nil
	tr.DisableCompression = true
	return &rawHTTPTransport{
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: tr,
		},
	}
}

func newImageHTTPClient(direct bool) *http.Client {
	if !direct {
		return &http.Client{Timeout: 15 * time.Second}
	}
	tr := cloneDefaultHTTPTransport()
	tr.Proxy = nil
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: tr,
	}
}

func cloneDefaultHTTPTransport() *http.Transport {
	if dt, ok := http.DefaultTransport.(*http.Transport); ok && dt != nil {
		return dt.Clone()
	}
	return &http.Transport{}
}

type rawHTTPTransport struct {
	client *http.Client
}

func (t *rawHTTPTransport) Do(ctx context.Context, req *sdk.Request) (*sdk.Response, error) {
	if req == nil {
		return nil, errors.New("hg: nil request")
	}
	for name, value := range req.Headers {
		if strings.EqualFold(name, "Authorization") || (strings.EqualFold(name, "Cookie") && !guestCookieAllowed(value)) {
			return nil, fmt.Errorf("%w: credential header refused", sdk.ErrAuthenticationRequired)
		}
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	hreq, err := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	resp, err := t.client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	headers := make(map[string]string, len(resp.Header))
	for k, values := range resp.Header {
		if strings.EqualFold(k, "Set-Cookie") || strings.EqualFold(k, "Cookie") {
			continue
		}
		if len(values) > 0 {
			headers[k] = values[0]
		}
	}
	cookies := make(map[string]string)
	for _, cookie := range resp.Cookies() {
		if cookie.Name != "" {
			cookies[cookie.Name] = cookie.Value
		}
	}
	return &sdk.Response{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       body,
		Cookies:    cookies,
	}, nil
}

func guestCookieAllowed(value string) bool {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "session") || strings.Contains(lower, "odin_tt") {
		return false
	}
	return strings.Contains(value, "passport_csrf_token=;") &&
		strings.Contains(value, "install_id=") &&
		strings.Contains(value, "ttreq=")
}

func (c *Client) Discover(ctx context.Context, channel, cursor string) (Page[Series], error) {
	page, err := c.sdkClient().Discover(ctx, sdk.DiscoverOptions{
		Channel: channel,
		Cursor:  cursor,
	})
	if err != nil {
		return Page[Series]{}, err
	}
	return mapSummaryPage(page), nil
}

func (c *Client) Search(ctx context.Context, query, cursor string, filters []string) (Page[Series], error) {
	page, err := c.sdkClient().Search(ctx, query, sdk.SearchOptions{
		Cursor:  cursor,
		Filters: filters,
	})
	if err != nil {
		return Page[Series]{}, err
	}
	return mapSummaryPage(page), nil
}

func (c *Client) Suggest(ctx context.Context, prefix string) ([]string, error) {
	items, err := c.sdkClient().Suggest(ctx, prefix)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		word := strings.TrimSpace(firstNonEmpty(item.Text, item.Keyword))
		if word == "" {
			continue
		}
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		out = append(out, word)
	}
	return out, nil
}

func (c *Client) HotSearch(ctx context.Context) (HotSearch, error) {
	hot, err := c.sdkClient().HotSearch(ctx)
	if err != nil {
		return HotSearch{}, err
	}
	return mapHotSearch(hot), nil
}

func (c *Client) ShortRank(ctx context.Context, content, board, cursor string) (ShortRankPage, error) {
	page, err := c.sdkClient().ShortRank(ctx, sdk.ShortRankQuery{
		Content: content,
		Board:   board,
		Cursor:  cursor,
	})
	if err != nil {
		return ShortRankPage{}, err
	}
	return mapShortRank(page), nil
}

func (c *Client) ShortFilterPanel(ctx context.Context, genre string) ([]FilterRow, error) {
	rows, err := c.sdkClient().ShortFilterPanel(ctx, genre)
	if err != nil {
		return nil, err
	}
	return mapFilterRows(rows, genre), nil
}

func (c *Client) ShortFilter(ctx context.Context, genre string, selected map[string]string, cursor string) (Page[Series], error) {
	selections := buildShortFilterSelections(genre, selected)
	page, err := c.sdkClient().ShortFilter(ctx, sdk.ShortFilterQuery{
		Selections: selections,
		Cursor:     cursor,
	})
	if err != nil {
		return Page[Series]{}, err
	}
	if page == nil {
		return Page[Series]{}, nil
	}
	return Page[Series]{
		Items:   mapSeriesSummaries(page.Items),
		Cursor:  page.Cursor,
		HasMore: page.HasMore,
	}, nil
}

func (c *Client) NewReleases(ctx context.Context, date, cursor string) (NewReleasePage, error) {
	page, err := c.sdkClient().NewReleases(ctx, sdk.NewReleaseQuery{
		Date:   date,
		Cursor: cursor,
	})
	if err != nil {
		return NewReleasePage{}, err
	}
	return mapNewReleasePage(page, date), nil
}

func (c *Client) GetSeries(ctx context.Context, seriesID string) (Series, error) {
	series, err := c.sdkClient().GetSeries(ctx, seriesID)
	if err != nil {
		return Series{}, err
	}
	return mapSeriesDetail(series), nil
}

func (c *Client) ListEpisodes(ctx context.Context, seriesID string) ([]Episode, error) {
	sc := c.sdkClient()
	var (
		cursor string
		seen   = make(map[string]struct{})
		out    []Episode
	)
	for pageNum := 0; pageNum < maxEpisodePages; pageNum++ {
		page, err := sc.ListEpisodes(ctx, seriesID, sdk.EpisodeOptions{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if page == nil {
			break
		}
		for _, ep := range page.Items {
			if ep.ID != "" {
				if _, ok := seen[ep.ID]; ok {
					continue
				}
				seen[ep.ID] = struct{}{}
			}
			idx := ep.Number
			if idx <= 0 {
				idx = len(out) + 1
			}
			title := strings.TrimSpace(ep.Title)
			if title == "" {
				title = fmt.Sprintf("第%d集", idx)
			}
			out = append(out, Episode{
				ID:       ep.ID,
				Index:    idx,
				Title:    title,
				Duration: ep.Duration,
				CoverURL: ep.CoverURL,
			})
		}
		if !page.HasMore || page.Cursor == "" || page.Cursor == cursor {
			break
		}
		cursor = page.Cursor
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Index < out[j].Index
	})
	return out, nil
}

func (c *Client) Seasons(ctx context.Context, seriesID string) ([]Season, error) {
	album, err := c.sdkClient().Seasons(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	if album == nil {
		return nil, nil
	}
	out := make([]Season, 0, len(album.Seasons))
	for _, s := range album.Seasons {
		label := strings.TrimSpace(s.Title)
		if s.Index > 0 {
			label = fmt.Sprintf("第%d季", s.Index)
		}
		out = append(out, Season{
			SeriesID:     s.SeriesID,
			Title:        s.Title,
			Label:        label,
			CoverURL:     s.CoverURL,
			EpisodeCount: s.EpisodeCount,
			Current:      s.SeriesID == seriesID,
		})
	}
	return out, nil
}

func (c *Client) SeriesRecommend(ctx context.Context, seriesID string) ([]Series, error) {
	rec, err := c.sdkClient().SeriesRecommend(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}
	return mapSeriesSummaries(rec.Items), nil
}

func (c *Client) Person(ctx context.Context, userID string) (Person, error) {
	p, err := c.sdkClient().Person(ctx, userID)
	if err != nil {
		return Person{}, err
	}
	if p == nil {
		return Person{}, nil
	}
	intro := strings.TrimSpace(p.Description)
	if intro == "" {
		meta := append(append([]string(nil), p.Tags...), p.Labels...)
		intro = strings.Join(compactStrings(meta), " · ")
	}
	return Person{
		UserID:    firstNonEmpty(p.UserID, userID),
		Name:      p.Name,
		AvatarURL: p.AvatarURL,
		Intro:     intro,
	}, nil
}

func (c *Client) PersonWorks(ctx context.Context, userID, cursor string) (Page[Series], error) {
	page, err := c.sdkClient().PersonWorks(ctx, userID, cursor)
	if err != nil {
		return Page[Series]{}, err
	}
	if page == nil {
		return Page[Series]{}, nil
	}
	items := make([]Series, 0, len(page.Items))
	for _, w := range page.Items {
		cat := ""
		if len(w.Tags) > 0 {
			cat = w.Tags[0]
		}
		items = append(items, Series{
			ID:           w.SeriesID,
			Title:        w.Title,
			CoverURL:     w.CoverURL,
			Abstract:     w.Description,
			Score:        formatHeat(w.Heat),
			Category:     cat,
			EpisodeCount: w.EpisodeCount,
			Tags:         compactStrings(w.Tags),
		})
	}
	return Page[Series]{
		Items:   items,
		Cursor:  page.Cursor,
		HasMore: page.HasMore,
	}, nil
}

func shouldFallbackToRemoteSigner(err error) bool {
	return errors.Is(err, sdk.ErrAnonymousLimited) ||
		errors.Is(err, sdk.ErrSignatureRejected) ||
		errors.Is(err, sdk.ErrDeviceRejected) ||
		errors.Is(err, sdk.ErrSignerUnavailable)
}

func resolvePlaybackWith(ctx context.Context, sc *sdk.Client, episodeID string) (*model.Playback, error) {
	pb, err := sc.ResolvePlayback(ctx, episodeID, sdk.PlaybackOptions{})
	if err != nil {
		if !errors.Is(err, sdk.ErrDRMProtected) || pb == nil {
			return nil, err
		}
	}
	if pb == nil {
		return nil, fmt.Errorf("hg: empty playback response")
	}
	return pb, nil
}

func (c *Client) ResolvePlayback(ctx context.Context, episodeID string) (Playback, error) {
	localSC, remoteSC := c.playbackClients()
	pb, err := resolvePlaybackWith(ctx, localSC, episodeID)
	if err != nil && remoteSC != nil && shouldFallbackToRemoteSigner(err) && ctx.Err() == nil {
		if rpb, rerr := resolvePlaybackWith(ctx, remoteSC, episodeID); rerr == nil {
			pb, err = rpb, nil
		} else {
			err = rerr
		}
	}
	if err != nil {
		return Playback{}, err
	}

	out := mapPlayback(pb, episodeID)
	if pb.Protected {
		key, kerr := sdk.ContentKey(pb)
		if kerr != nil {
			return Playback{}, kerr
		}
		out.KeyHex = key.Hex
		out.Protected = true
	}
	return out, nil
}

func (c *Client) ListDanmaku(ctx context.Context, seriesID, episodeID string, startMs int64, cursor string) (DanmakuPage, error) {
	opts := sdk.DanmakuOptions{}
	if cursor != "" {
		opts.Cursor = cursor
	} else {
		if startMs < 0 {
			startMs = 0
		}
		opts.StartMs = startMs
	}
	page, err := c.sdkClient().ListDanmaku(ctx, seriesID, episodeID, opts)
	if err != nil {
		return DanmakuPage{}, err
	}
	if page == nil {
		return DanmakuPage{}, nil
	}
	items := make([]Danmaku, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, Danmaku{
			ID:        item.ID,
			At:        time.Duration(item.OffsetMS) * time.Millisecond,
			Text:      item.Text,
			Likes:     item.Likes,
			PlusOnes:  item.PlusOnes,
			CreatedAt: item.CreatedAt,
		})
	}
	return DanmakuPage{
		Items:   items,
		Cursor:  page.Cursor,
		HasMore: page.HasMore,
		Total:   int(page.Total),
	}, nil
}

func (c *Client) ResetDevice(ctx context.Context) error {
	localSC, remoteSC := c.playbackClients()
	err := localSC.ResetDevice(ctx)
	if remoteSC != nil {
		if rerr := remoteSC.ResetDevice(ctx); err == nil && rerr != nil {
			err = rerr
		}
	}
	return err
}

func (c *Client) Image(ctx context.Context, rawURL string) ([]byte, error) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("hg: invalid image url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.imageClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("hg: image request failed with HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("hg: image exceeds %d byte limit", maxImageBytes)
	}
	return data, nil
}

func mapSummaryPage(page *sdk.Page[model.SeriesSummary]) Page[Series] {
	if page == nil {
		return Page[Series]{}
	}
	return Page[Series]{
		Items:   mapSeriesSummaries(page.Items),
		Cursor:  page.Cursor,
		HasMore: page.HasMore,
	}
}

func mapSeriesSummaries(items []model.SeriesSummary) []Series {
	out := make([]Series, 0, len(items))
	for _, item := range items {
		out = append(out, Series{
			ID:           item.ID,
			Title:        item.Title,
			CoverURL:     item.CoverURL,
			Abstract:     item.Description,
			Category:     item.Category,
			EpisodeCount: item.EpisodeCount,
		})
	}
	return out
}

func mapSeriesDetail(m *model.Series) Series {
	if m == nil {
		return Series{}
	}
	s := Series{
		ID:           m.ID,
		Title:        m.Title,
		CoverURL:     m.CoverURL,
		Abstract:     m.Description,
		Category:     m.Category,
		StatusText:   strings.TrimSpace(m.RightText),
		AlbumID:      m.RelatedAlbumID,
		EpisodeCount: m.EpisodeCount,
		Finished:     m.Status == model.SeriesEnd,
	}

	var actors []string
	var tags []string
	if m.Category != "" {
		tags = append(tags, m.Category)
	}
	for _, row := range m.Rows {
		text := strings.TrimSpace(row.Text)
		if text == "" {
			continue
		}
		switch row.Kind {
		case model.InfoCategory:
			if s.Category == "" {
				s.Category = text
			}
			tags = append(tags, text)
		case model.InfoScore:
			if s.Score == "" {
				s.Score = text
			}
		case model.InfoTags, model.InfoPlotTag:
			tags = append(tags, splitList(text)...)
		case model.InfoTwoActors:
			actors = append(actors, parseActors(text)...)
		case model.InfoCreationStatus:
			if s.StatusText == "" {
				s.StatusText = text
			}
		}
	}
	if s.Score == "" && m.Heat > 0 {
		s.Score = formatHeat(m.Heat)
	}
	if s.StatusText == "" {
		s.StatusText = formatSeriesStatus(m.Status, m.EpisodeCount)
	}
	s.Actors = compactStrings(actors)
	s.Tags = compactStrings(tags)
	return s
}

func mapHotSearch(hot *model.HotSearch) HotSearch {
	if hot == nil {
		return HotSearch{}
	}
	guesses := make([]string, 0, len(hot.Guesses))
	for _, g := range hot.Guesses {
		if title := strings.TrimSpace(g.Title); title != "" {
			guesses = append(guesses, title)
		}
	}
	boards := make([]HotBoard, 0, len(hot.Boards))
	for _, b := range hot.Boards {
		items := make([]HotItem, 0, len(b.Items)+len(b.Topics))
		for _, it := range b.Items {
			word := strings.TrimSpace(it.Title)
			if word == "" {
				continue
			}
			items = append(items, HotItem{
				Word:     word,
				SeriesID: it.SeriesID,
				Score:    firstNonEmpty(it.SearchScore, it.Heat, it.Tag),
			})
		}
		for _, tp := range b.Topics {
			word := strings.TrimSpace(firstNonEmpty(tp.Query, tp.Title))
			if word == "" {
				continue
			}
			items = append(items, HotItem{
				Word:  word,
				Score: tp.Heat,
			})
		}
		boards = append(boards, HotBoard{
			Name:  b.Name,
			Items: items,
		})
	}
	return HotSearch{
		Guesses: compactStrings(guesses),
		Boards:  boards,
	}
}

func mapShortRank(page *model.ShortRanking) ShortRankPage {
	if page == nil {
		return ShortRankPage{}
	}
	contents := make([]RankContent, 0, len(page.Contents))
	for _, rc := range page.Contents {
		boards := make([]RankBoard, 0, len(rc.Boards))
		for _, rb := range rc.Boards {
			boards = append(boards, RankBoard{
				ID:       rb.ID,
				Name:     rb.Name,
				Selected: rb.Selected,
			})
		}
		contents = append(contents, RankContent{
			ID:       rc.ID,
			Name:     rc.Name,
			Selected: rc.Selected,
			Boards:   boards,
		})
	}
	items := make([]RankItem, 0, len(page.Items))
	for _, it := range page.Items {
		pos := it.Position
		if pos == 0 {
			pos = it.Rank
		}
		intro := strings.TrimSpace(it.Intro)
		if intro == "" && len(it.Details) > 0 {
			intro = strings.Join(compactStrings(it.Details), " · ")
		}
		items = append(items, RankItem{
			Position:     pos,
			SeriesID:     it.SeriesID,
			Title:        it.Title,
			CoverURL:     it.CoverURL,
			Score:        it.Score,
			Actor:        it.Actor,
			UserID:       it.UserID,
			AvatarURL:    it.AvatarURL,
			Intro:        intro,
			EpisodeCount: it.EpisodeCount,
			Promoted:     it.Promoted,
		})
	}
	return ShortRankPage{
		Contents: contents,
		Content:  page.Content,
		Board:    page.Board,
		Cursor:   page.Cursor,
		HasMore:  page.HasMore,
		Items:    items,
	}
}

func mapFilterRows(rows []model.FilterRow, genre string) []FilterRow {
	out := make([]FilterRow, 0, len(rows))
	for _, row := range rows {
		opts := make([]FilterOption, 0, len(row.Options))
		for i, opt := range row.Options {
			selected := false
			if row.Key == "genre" {
				selected = opt.Value == genre
			} else if i == 0 && opt.Value == "" {
				selected = true
			}
			opts = append(opts, FilterOption{
				Name:     opt.Name,
				Value:    opt.Value,
				Selected: selected,
			})
		}
		out = append(out, FilterRow{
			Key:     row.Key,
			Name:    row.Name,
			Options: opts,
		})
	}
	return out
}

func buildShortFilterSelections(genre string, selected map[string]string) map[string][]string {
	var out map[string][]string
	for k, v := range selected {
		val := strings.TrimSpace(v)
		if k == "" || val == "" {
			continue
		}
		if out == nil {
			out = make(map[string][]string)
		}
		out[k] = []string{val}
	}
	g := strings.TrimSpace(genre)
	if g != "" && (out == nil || len(out["genre"]) == 0) {
		if out == nil {
			out = make(map[string][]string)
		}
		out["genre"] = []string{g}
	}
	return out
}

func mapNewReleasePage(page *model.NewReleasePage, reqDate string) NewReleasePage {
	if page == nil {
		return NewReleasePage{}
	}
	activeDate := firstNonEmpty(page.Date, reqDate)
	days := make([]NewReleaseDay, 0, len(page.Dates))
	for _, d := range page.Dates {
		days = append(days, NewReleaseDay{
			Date:     d,
			Label:    formatReleaseDayLabel(d),
			Selected: d != "" && d == activeDate,
		})
	}
	items := make([]Series, 0, len(page.Items))
	for _, nr := range page.Items {
		cat := ""
		if len(nr.Categories) > 0 {
			cat = nr.Categories[0]
		}
		status := strings.TrimSpace(nr.Note)
		if status == "" {
			if nr.Online {
				status = "已上线"
			} else if !nr.PublishAt.IsZero() {
				status = nr.PublishAt.Format("01-02上线")
			}
		}
		items = append(items, Series{
			ID:           nr.SeriesID,
			Title:        nr.Title,
			CoverURL:     nr.CoverURL,
			Abstract:     nr.Description,
			Score:        nr.Heat,
			Category:     cat,
			StatusText:   status,
			EpisodeCount: nr.EpisodeCount,
			Tags:         compactStrings(nr.Categories),
		})
	}
	return NewReleasePage{
		Days:    days,
		Items:   items,
		Cursor:  page.Cursor,
		HasMore: page.HasMore,
	}
}

func mapPlayback(pb *model.Playback, fallbackEpisodeID string) Playback {
	streams := make([]Stream, 0, len(pb.Streams))
	for _, s := range pb.Streams {
		streams = append(streams, Stream{
			Definition: s.Definition,
			Quality:    s.Quality,
			Codec:      s.Codec,
			URL:        s.URL,
			BackupURL:  s.BackupURL,
			Encryption: s.Encryption,
			Width:      s.Width,
			Height:     s.Height,
			Bitrate:    s.Bitrate,
			Size:       s.Size,
			HLS:        s.HLS,
			Dash:       s.Dash,
			Protected:  s.Protected,
		})
	}
	var expiresAt time.Time
	if pb.ExpiresAt > 0 {
		expiresAt = time.Unix(pb.ExpiresAt, 0)
	}
	return Playback{
		EpisodeID: firstNonEmpty(pb.EpisodeID, fallbackEpisodeID),
		VideoID:   pb.VideoID,
		AudioURL:  pb.AudioURL,
		Duration:  time.Duration(pb.DurationSeconds) * time.Second,
		ExpiresAt: expiresAt,
		Protected: pb.Protected,
		Streams:   streams,
	}
}

func formatReleaseDayLabel(yyyymmdd string) string {
	t, err := time.Parse("20060102", yyyymmdd)
	if err != nil {
		return yyyymmdd
	}
	return fmt.Sprintf("%d月%d日", t.Month(), t.Day())
}

func formatSeriesStatus(status model.SeriesStatus, epCount int) string {
	switch status {
	case model.SeriesEnd:
		if epCount > 0 {
			return fmt.Sprintf("全%d集", epCount)
		}
		return "已完结"
	case model.SeriesUpdating:
		if epCount > 0 {
			return fmt.Sprintf("更新至%d集", epCount)
		}
		return "连载中"
	case model.SeriesUpdatedToday:
		return "今日更新"
	case model.SeriesUpdateStopped:
		return "已停更"
	default:
		return ""
	}
}

func formatHeat(heat int64) string {
	if heat <= 0 {
		return ""
	}
	if heat >= 10000 {
		return fmt.Sprintf("%.1f万热度", float64(heat)/10000.0)
	}
	return fmt.Sprintf("%d热度", heat)
}

func parseActors(raw string) []string {
	s := strings.TrimSpace(raw)
	for _, prefix := range []string{"演员·", "主演·", "演员:", "演员：", "主演:", "主演："} {
		s = strings.TrimPrefix(s, prefix)
	}
	return splitList(s)
}

func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ' ', '\t', '、', '/', ',', '，', '·', '|':
			return true
		default:
			return false
		}
	})
	return compactStrings(fields)
}

func compactStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		v := strings.TrimSpace(item)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
