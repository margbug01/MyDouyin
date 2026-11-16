package media

import (
	"io"
	"os"
	"testing"
)

func BenchmarkDecodeFrames(b *testing.B) {
	path := os.Getenv("DY_TEST_MP4")
	if path == "" {
		b.Skip("DY_TEST_MP4 not set")
	}
	f, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	v, err := f.Video()
	if err != nil {
		b.Fatal(err)
	}
	defer v.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		frame, err := v.Next()
		if err == io.EOF {
			if err = v.Rewind(); err == nil {
				frame, err = v.Next()
			}
		}
		if err != nil {
			b.Fatal(err)
		}
		if frame.Image.Bounds().Empty() {
			b.Fatal("empty frame")
		}
	}
}
