package dy

import (
	"os"
	"testing"
)

// The signed feed request returns playable videos, and the first one downloads
// as an mp4. Live: skipped unless LIVE is set.
func TestLiveFeed(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var vs []Video
	for range 2 { // the first page is short
		more, err := c.Feed()
		if err != nil {
			t.Fatal(err)
		}
		vs = append(vs, more...)
	}
	if len(vs) < 3 {
		t.Fatalf("only %d videos", len(vs))
	}
	for _, v := range vs {
		t.Logf("%s %q %dx%d %v %.1fMB", v.ID[len(v.ID)-5:], v.Author, v.W, v.H, v.Duration, float64(v.Size)/1e6)
	}
	// Pick the shortest so the test is quick.
	v := vs[0]
	for _, x := range vs {
		if x.Duration < v.Duration {
			v = x
		}
	}
	dst := t.TempDir() + "/v.mp4"
	if err := c.Download(v.PlayURL, dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if len(b) < 1000 || string(b[4:8]) != "ftyp" {
		t.Fatalf("not an mp4: %d bytes", len(b))
	}
}
