package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"douyin/internal/dy"
	"douyin/internal/hg"
)

const (
	fastCacheTTL        = 24 * time.Hour
	coverCacheMaxFiles  = 96
	coverCachePruneStep = 24
	orbitCacheMaxItems  = orbitRing
	hgCacheMaxItems     = 16
)

type orbitSnapshot struct {
	SavedAt time.Time  `json:"savedAt"`
	Videos  []dy.Video `json:"videos"`
}

type hgDiscoverSnapshot struct {
	SavedAt time.Time   `json:"savedAt"`
	Channel string      `json:"channel"`
	Items   []hg.Series `json:"items"`
}

func (a *app) orbitCachePath() string {
	if a.cacheDir == "" {
		return ""
	}
	return filepath.Join(a.cacheDir, "orbit-picks.json")
}

func (a *app) hgDiscoverCachePath(channel string) string {
	if a.cacheDir == "" {
		return ""
	}
	switch channel {
	case "", hgDiscoverChannels[0]:
		return filepath.Join(a.cacheDir, "hg-discover.json")
	case "真人剧":
		return filepath.Join(a.cacheDir, "hg-discover-live.json")
	case "漫剧":
		return filepath.Join(a.cacheDir, "hg-discover-comic.json")
	default:
		return ""
	}
}

func (a *app) coverCacheDir() string {
	if a.cacheDir == "" {
		return ""
	}
	return filepath.Join(a.cacheDir, "covers")
}

// loadOrbitCache seeds 引力场's 精选 ring from the last session's unseen videos
// so the opening frame renders immediately while a fresh batch fetches in parallel.
func (a *app) loadOrbitCache() {
	path := a.orbitCachePath()
	if path == "" || a.orbitPicks == nil || len(a.orbitPicks.list) > 0 {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var snap orbitSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return
	}
	if len(snap.Videos) == 0 || snap.SavedAt.IsZero() || time.Since(snap.SavedAt) > fastCacheTTL {
		return
	}
	n := min(len(snap.Videos), orbitCacheMaxItems)
	for _, v := range snap.Videos[:n] {
		if v.Live != nil {
			continue
		}
		a.orbitPicks.list = append(a.orbitPicks.list, &slot{v: v, cached: true})
	}
}

// saveOrbitCache persists the next unseen ring of 引力场 精选 so the next
// launch opens immediately on fresh, unseen covers.
func (a *app) saveOrbitCache() {
	path := a.orbitCachePath()
	if path == "" || a.orbitPicks == nil || len(a.orbitPicks.list) == 0 {
		return
	}
	start := a.orbit.offset + orbitRing
	if start >= len(a.orbitPicks.list) {
		start = a.orbit.offset
	}
	if start >= len(a.orbitPicks.list) {
		start = 0
	}
	var vs []dy.Video
	for _, s := range a.orbitPicks.list[start:] {
		if s.v.Live != nil {
			continue
		}
		vs = append(vs, s.v)
		if len(vs) >= orbitCacheMaxItems {
			break
		}
	}
	if len(vs) == 0 {
		return
	}
	snap := orbitSnapshot{SavedAt: time.Now(), Videos: vs}
	go writeJSONAtomic(path, snap)
}

// loadHGDiscoverCache seeds a MyHongguo discover channel from disk so the
// first open shows cards immediately while refreshing live data in the background.
func (a *app) loadHGDiscoverCache(h *hgApp, channel string) {
	if channel == "" {
		channel = hgDiscoverChannels[0]
	}
	path := a.hgDiscoverCachePath(channel)
	if path == "" || h == nil {
		return
	}
	l := h.discoverList(channel)
	if len(l.items) > 0 || l.loaded {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var snap hgDiscoverSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return
	}
	if snap.Channel != channel || len(snap.Items) == 0 || snap.SavedAt.IsZero() || time.Since(snap.SavedAt) > fastCacheTTL {
		return
	}
	l.appendUnique(snap.Items)
	l.stale = true
}

func (a *app) saveHGDiscoverCache(channel string, items []hg.Series) {
	path := a.hgDiscoverCachePath(channel)
	if path == "" || len(items) == 0 {
		return
	}
	n := min(len(items), hgCacheMaxItems)
	snap := hgDiscoverSnapshot{
		SavedAt: time.Now(),
		Channel: channel,
		Items:   append([]hg.Series(nil), items[:n]...),
	}
	go writeJSONAtomic(path, snap)
}

func writeJSONAtomic(path string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

var coverCacheWrites atomic.Int64

func coverCacheFile(dir, url string, maxEdge int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s", maxEdge, url)))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".img")
}

func readCoverCache(dir, url string, maxEdge int) []byte {
	if dir == "" || url == "" {
		return nil
	}
	b, err := os.ReadFile(coverCacheFile(dir, url, maxEdge))
	if err != nil || len(b) == 0 {
		return nil
	}
	return b
}

func writeCoverCache(dir, url string, maxEdge int, data []byte) {
	if dir == "" || url == "" || len(data) == 0 {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	dst := coverCacheFile(dir, url, maxEdge)
	tmp := fmt.Sprintf("%s.%d.tmp", dst, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return
	}
	if coverCacheWrites.Add(1)%coverCachePruneStep == 0 {
		pruneCoverCache(dir, coverCacheMaxFiles)
	}
}

func pruneCoverCache(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	type item struct {
		name string
		mod  time.Time
	}
	files := make([]item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, item{name: e.Name(), mod: info.ModTime()})
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].mod.After(files[j].mod)
	})
	for _, f := range files[keep:] {
		_ = os.Remove(filepath.Join(dir, f.name))
	}
}
