//go:build darwin

package main

import (
	"douyin/internal/drama"
	"douyin/internal/media"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// Exercise real AVFoundation frames and restore a saved position on the second clip.
func TestSecondEpisodeResume(t *testing.T) {
	clips, e := fs.Sub(fixtures, "clips")
	if e != nil {
		t.Fatal(e)
	}
	s := httptest.NewServer(http.FileServerFS(clips))
	defer s.Close()
	ep := catalog{s.URL}.Series().Episodes[1]
	path := filepath.Join(t.TempDir(), "progress.json")
	p, e := media.OpenVideo(t.Context(), ep.URL, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Next(); e != nil {
		p.Close()
		t.Fatal(e)
	}
	p.Seek(5 * time.Second)
	e = waitAt(p, 5*time.Second)
	pos := p.Position()
	p.Close()
	if e != nil {
		t.Fatal(e)
	}
	state := drama.Progress{Episode: ep.ID, Positions: map[string]time.Duration{ep.ID: pos}}
	if e = state.Save(path); e != nil {
		t.Fatal(e)
	}
	got, e := drama.Load(path)
	if e != nil || got.Episode != ep.ID || got.Positions[ep.ID] != pos {
		t.Fatalf("restore: %+v %v", got, e)
	}
	p, e = media.OpenVideo(t.Context(), ep.URL, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if _, e = p.Next(); e != nil {
		t.Fatal(e)
	}
	p.Seek(got.Positions[ep.ID])
	if e = waitAt(p, pos); e != nil {
		t.Fatal(e)
	}
}
func waitAt(p *media.Live, at time.Duration) error {
	for end := time.Now().Add(6 * time.Second); time.Now().Before(end); {
		f, e := p.Frame()
		if e != nil {
			return e
		}
		if f.Image != nil && !p.Seeking() && p.Position() >= at-200*time.Millisecond && p.Position() < at+2*time.Second {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("seek: wanted %v, got %v", at, p.Position())
}
