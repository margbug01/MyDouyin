package media

import (
	"image"
	"io"
	"os"
	"testing"
)

// Decoding a real Douyin clip yields frames with picture, audio with sound,
// and rewinding starts over. Skipped unless DY_TEST_MP4 names a clip.
func TestDecodeClip(t *testing.T) {
	path := os.Getenv("DY_TEST_MP4")
	if path == "" {
		t.Skip("DY_TEST_MP4 not set")
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t.Logf("%dx%d %v", f.W, f.H, f.Duration)
	v, err := f.Video()
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	count := func() (frames int, bright int) {
		for {
			fr, err := v.Next()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			frames++
			if frames == 30 {
				for _, p := range fr.Image.(*image.RGBA).Pix {
					bright += int(p)
				}
			}
		}
	}
	n, bright := count()
	if n < 100 || bright == 0 {
		t.Fatalf("frames %d, brightness %d", n, bright)
	}
	if err := v.Rewind(); err != nil {
		t.Fatal(err)
	}
	if n2, _ := count(); n2 != n {
		t.Fatalf("after rewind %d frames, was %d", n2, n)
	}

	a, err := f.Audio()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	buf := make([]float32, 4800)
	var total int
	var peak float32
	for {
		k, err := a.Read(buf)
		total += k
		for _, s := range buf[:k] {
			peak = max(peak, s, -s)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	secs := float64(total) / 2 / rate
	t.Logf("audio %.1fs peak %.2f, frames %d", secs, peak, n)
	if secs < f.Duration.Seconds()-1 || peak == 0 {
		t.Fatalf("audio %.1fs of %v, peak %v", secs, f.Duration, peak)
	}
	// Seeking half-way leaves about half the audio.
	if err := a.SetPosition(a.Len() / 2); err != nil {
		t.Fatal(err)
	}
	total = 0
	for {
		k, err := a.Read(buf)
		total += k
		if err != nil {
			break
		}
	}
	if half := float64(total) / 2 / rate; half < secs/2-1 || half > secs/2+1 {
		t.Fatalf("after seek %.1fs, want about %.1fs", half, secs/2)
	}
}
