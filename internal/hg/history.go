package hg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const maxNonFavoriteHistory = 200

type WatchEntry struct {
	SeriesID     string                   `json:"series_id"`
	Title        string                   `json:"title"`
	CoverURL     string                   `json:"cover_url,omitempty"`
	EpisodeCount int                      `json:"episode_count,omitempty"`
	EpisodeID    string                   `json:"episode_id,omitempty"`
	EpisodeIndex int                      `json:"episode_index,omitempty"`
	Position     time.Duration            `json:"position,omitempty"`
	UpdatedAt    time.Time                `json:"updated_at,omitempty"`
	Favorited    bool                     `json:"favorited,omitempty"`
	Positions    map[string]time.Duration `json:"positions,omitempty"`
}

type historyFile struct {
	Entries []WatchEntry `json:"entries"`
}

type HistoryStore struct {
	mu      sync.RWMutex
	path    string
	entries map[string]WatchEntry
	now     func() time.Time
}

func NewHistoryStore(path string) (*HistoryStore, error) {
	s := &HistoryStore{
		path:    path,
		entries: map[string]WatchEntry{},
		now:     time.Now,
	}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var payload historyFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	for _, e := range payload.Entries {
		if e.SeriesID == "" {
			continue
		}
		if e.Positions == nil {
			e.Positions = map[string]time.Duration{}
		}
		s.entries[e.SeriesID] = e
	}
	return s, nil
}

func (s *HistoryStore) RecordProgress(series Series, ep Episode, pos time.Duration) error {
	if series.ID == "" {
		return fmt.Errorf("hg: missing series id")
	}
	if pos < 0 {
		pos = 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.entries[series.ID]
	entry.SeriesID = series.ID
	mergeSeriesMeta(&entry, series)
	entry.EpisodeID = ep.ID
	entry.EpisodeIndex = ep.Index
	entry.Position = pos
	entry.UpdatedAt = s.now()
	if entry.Positions == nil {
		entry.Positions = map[string]time.Duration{}
	}
	if ep.ID != "" {
		entry.Positions[ep.ID] = pos
	}
	s.entries[series.ID] = entry
	s.pruneLocked()
	return s.saveLocked()
}

func (s *HistoryStore) ClearEpisodePosition(seriesID, episodeID string) error {
	if seriesID == "" || episodeID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[seriesID]
	if !ok {
		return nil
	}
	changed := false
	if _, has := entry.Positions[episodeID]; has {
		delete(entry.Positions, episodeID)
		changed = true
	}
	if entry.EpisodeID == episodeID && entry.Position != 0 {
		entry.Position = 0
		changed = true
	}
	if !changed {
		return nil
	}
	s.entries[seriesID] = entry
	return s.saveLocked()
}

func (s *HistoryStore) ToggleFavorite(series Series) (bool, error) {
	if series.ID == "" {
		return false, fmt.Errorf("hg: missing series id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.entries[series.ID]
	entry.SeriesID = series.ID
	mergeSeriesMeta(&entry, series)
	entry.Favorited = !entry.Favorited
	entry.UpdatedAt = s.now()

	if !entry.Favorited && !hasWatchHistory(entry) {
		delete(s.entries, series.ID)
	} else {
		if entry.Positions == nil {
			entry.Positions = map[string]time.Duration{}
		}
		s.entries[series.ID] = entry
		s.pruneLocked()
	}
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	return entry.Favorited, nil
}

func (s *HistoryStore) IsFavorited(seriesID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[seriesID].Favorited
}

func (s *HistoryStore) Get(seriesID string) (WatchEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[seriesID]
	if !ok {
		return WatchEntry{}, false
	}
	return cloneWatchEntry(entry), true
}

func (s *HistoryStore) Favorites() []WatchEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]WatchEntry, 0)
	for _, entry := range s.entries {
		if entry.Favorited {
			out = append(out, cloneWatchEntry(entry))
		}
	}
	sortEntriesNewestFirst(out)
	return out
}

func (s *HistoryStore) Recent() []WatchEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]WatchEntry, 0)
	for _, entry := range s.entries {
		if hasWatchHistory(entry) {
			out = append(out, cloneWatchEntry(entry))
		}
	}
	sortEntriesNewestFirst(out)
	return out
}

func (s *HistoryStore) RemoveHistory(seriesID string) error {
	if seriesID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[seriesID]
	if !ok {
		return nil
	}
	if entry.Favorited {
		entry.EpisodeID = ""
		entry.EpisodeIndex = 0
		entry.Position = 0
		entry.Positions = map[string]time.Duration{}
		s.entries[seriesID] = entry
	} else {
		delete(s.entries, seriesID)
	}
	return s.saveLocked()
}

func (s *HistoryStore) pruneLocked() {
	nonFav := make([]WatchEntry, 0)
	for _, entry := range s.entries {
		if !entry.Favorited {
			nonFav = append(nonFav, entry)
		}
	}
	if len(nonFav) <= maxNonFavoriteHistory {
		return
	}
	sortEntriesNewestFirst(nonFav)
	for _, drop := range nonFav[maxNonFavoriteHistory:] {
		delete(s.entries, drop.SeriesID)
	}
}

func (s *HistoryStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	entries := make([]WatchEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, cloneWatchEntry(entry))
	}
	sortEntriesNewestFirst(entries)

	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(historyFile{Entries: entries})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".hg-history-*")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	defer os.Remove(tmpName)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func mergeSeriesMeta(entry *WatchEntry, series Series) {
	if series.Title != "" {
		entry.Title = series.Title
	}
	if series.CoverURL != "" {
		entry.CoverURL = series.CoverURL
	}
	if series.EpisodeCount > 0 {
		entry.EpisodeCount = series.EpisodeCount
	}
}

func hasWatchHistory(entry WatchEntry) bool {
	return entry.EpisodeID != "" || entry.EpisodeIndex > 0 || len(entry.Positions) > 0
}

func cloneWatchEntry(entry WatchEntry) WatchEntry {
	cp := entry
	if len(entry.Positions) > 0 {
		cp.Positions = make(map[string]time.Duration, len(entry.Positions))
		for k, v := range entry.Positions {
			cp.Positions[k] = v
		}
	} else {
		cp.Positions = nil
	}
	return cp
}

func sortEntriesNewestFirst(entries []WatchEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
		}
		return entries[i].SeriesID < entries[j].SeriesID
	})
}
