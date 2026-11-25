// Package drama holds catalog and resume data independent of the desktop shell.
package drama

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type Episode struct{ ID, Title, URL string }
type Series struct {
	ID, Title string
	Episodes  []Episode
}
type Catalog interface{ Series() Series }

// A separate progress file is used for each series.
type Progress struct {
	Episode   string                   `json:"episode"`
	Positions map[string]time.Duration `json:"positions"`
}

func Load(path string) (Progress, error) {
	p := Progress{Positions: map[string]time.Duration{}}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return p, nil
	}
	if e != nil {
		return p, e
	}
	e = json.Unmarshal(b, &p)
	if p.Positions == nil {
		p.Positions = map[string]time.Duration{}
	}
	return p, e
}
func (p Progress) Save(path string) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".progress-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
