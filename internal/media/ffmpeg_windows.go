// Windows uses the FFmpeg tools shipped beside the executable. Decoders feed
// MyGo's existing video/audio interfaces; they never open a console window.
package media

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/egoist/mygo/ui"
)

const NativeAvailable = false
const sampleRate = 48000

func toolPath(name string) (string, error) {
	if exe, err := os.Executable(); err == nil {
		path := filepath.Join(filepath.Dir(exe), name+".exe")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	path, err := exec.LookPath(name + ".exe")
	if err != nil {
		return "", fmt.Errorf("media: %s.exe is missing; keep the FFmpeg tools beside MyDouyin.exe", name)
	}
	return path, nil
}

func command(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	path, err := toolPath(name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd, nil
}

type File struct {
	W, H     int
	Duration time.Duration
	fps      float64
	source   string
	headers  string
	hasAudio bool
}

func Open(path string) (*File, error) { return openFile(path, nil) }

func openFile(source string, headers map[string]string) (*File, error) {
	if strings.HasPrefix(source, "file://") {
		raw := strings.ReplaceAll(strings.TrimPrefix(source, "file://"), `\`, "/")
		// Existing callers also pass file://C:\path rather than a URL.
		if len(raw) >= 2 && raw[1] == ':' {
			source = filepath.FromSlash(raw)
		} else {
			u, err := url.Parse("file://" + raw)
			if err != nil {
				return nil, err
			}
			source = strings.TrimPrefix(u.Path, "/")
			if u.Host != "" {
				source = "//" + u.Host + u.Path
			}
			source = filepath.FromSlash(source)
		}
	}
	f := &File{source: source, fps: 30}
	keys := make([]string, 0, len(headers))
	for key, value := range headers {
		if strings.ContainsAny(key, "\r\n:") || strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("media: invalid HTTP header")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		f.headers += key + ": " + headers[key] + "\r\n"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := append([]string{"-v", "error"}, f.inputArgs(0)...)
	// ffprobe takes a bare input; ffmpeg takes -i.
	args = append(args[:len(args)-2], source, "-show_entries", "format=duration:stream=codec_type,width,height,avg_frame_rate", "-of", "json")
	cmd, err := command(ctx, "ffprobe", args...)
	if err != nil {
		return nil, err
	}
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("media: cannot inspect video: %w", err)
	}
	var info struct {
		Streams []struct {
			Type string `json:"codec_type"`
			W    int    `json:"width"`
			H    int    `json:"height"`
			Rate string `json:"avg_frame_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	for _, s := range info.Streams {
		if s.Type == "audio" {
			f.hasAudio = true
		}
		if s.Type != "video" || f.W != 0 {
			continue
		}
		f.W, f.H = s.W, s.H
		if parts := strings.Split(s.Rate, "/"); len(parts) == 2 {
			n, _ := strconv.ParseFloat(parts[0], 64)
			d, _ := strconv.ParseFloat(parts[1], 64)
			if d > 0 && n/d >= 1 {
				// shortcut: output is capped at 60 fps; revisit for high refresh video.
				f.fps = min(n/d, 60)
			}
		}
	}
	if f.W <= 0 || f.H <= 0 || f.W > 8192 || f.H > 8192 || int64(f.W)*int64(f.H) > 33554432 {
		return nil, errors.New("media: missing or oversized video track")
	}
	seconds, _ := strconv.ParseFloat(info.Format.Duration, 64)
	if seconds > 0 && seconds < 86400*365 {
		f.Duration = time.Duration(seconds * float64(time.Second))
	}
	return f, nil
}

func (f *File) inputArgs(at time.Duration) []string {
	protocols := "file,http,https,tcp,tls,crypto,httpproxy"
	if strings.HasPrefix(f.source, "http://") || strings.HasPrefix(f.source, "https://") {
		protocols = "http,https,tcp,tls,crypto,httpproxy" // remote playlists cannot read local files
	}
	args := []string{"-rw_timeout", "20000000", "-protocol_whitelist", protocols}
	if f.headers != "" {
		args = append(args, "-headers", f.headers)
	}
	if at > 0 {
		args = append(args, "-ss", strconv.FormatFloat(at.Seconds(), 'f', 6, 64))
	}
	return append(args, "-i", f.source)
}

func (f *File) Close() {}

// A pipe may be closed from another goroutine while Read waits on the CDN.
// Always kill and reap it before audio/video waits for its decoder to stop.
type decodePipe struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	cancel context.CancelFunc
	wait   sync.Once
}

func startPipe(f *File, at time.Duration, output ...string) (*decodePipe, error) {
	ctx, cancel := context.WithCancel(context.Background())
	args := append([]string{"-nostdin", "-v", "error", "-threads", "2"}, f.inputArgs(at)...)
	args = append(args, output...)
	cmd, err := command(ctx, "ffmpeg", args...)
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		if out != nil {
			out.Close()
		}
		return nil, err
	}
	return &decodePipe{cmd: cmd, out: out, cancel: cancel}, nil
}

func (p *decodePipe) close() {
	if p == nil {
		return
	}
	p.cancel()
	p.out.Close()
	p.wait.Do(func() { _ = p.cmd.Wait() })
}

func (p *decodePipe) read(buf []byte) (int, error) {
	n, err := io.ReadFull(p.out, buf)
	if err != nil {
		var exit error
		p.wait.Do(func() { exit = p.cmd.Wait() })
		if exit != nil {
			return n, fmt.Errorf("media: decoder stopped: %w", exit)
		}
	}
	return n, err
}

type VideoDecoder struct {
	mu     sync.Mutex
	f      *File
	pipe   *decodePipe
	at     time.Duration
	closed bool
}

func (f *File) Video() (*VideoDecoder, error) { return &VideoDecoder{f: f}, nil }
func (v *VideoDecoder) Rewind() error         { return v.RewindTo(0) }
func (v *VideoDecoder) RewindTo(at time.Duration) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return io.ErrClosedPipe
	}
	v.pipe.close()
	v.pipe, v.at = nil, max(0, at)
	return nil
}
func (v *VideoDecoder) Next() (ui.VideoFrame, error) {
	return v.next(image.NewRGBA(image.Rect(0, 0, v.f.W, v.f.H)))
}
func (v *VideoDecoder) next(img *image.RGBA) (ui.VideoFrame, error) {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return ui.VideoFrame{}, io.EOF
	}
	if v.pipe == nil {
		var err error
		v.pipe, err = startPipe(v.f, v.at, "-map", "0:v:0", "-an", "-vf", "fps="+strconv.FormatFloat(v.f.fps, 'f', 6, 64), "-pix_fmt", "rgba", "-f", "rawvideo", "pipe:1")
		if err != nil {
			v.mu.Unlock()
			return ui.VideoFrame{}, err
		}
	}
	pipe := v.pipe
	v.mu.Unlock()
	_, err := pipe.read(img.Pix)
	if err != nil {
		return ui.VideoFrame{}, err
	}
	return ui.VideoFrame{Image: img, Duration: time.Duration(float64(time.Second) / v.f.fps)}, nil
}
func (v *VideoDecoder) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	v.pipe.close()
	return nil
}

type AudioDecoder struct {
	mu     sync.Mutex
	f      *File
	pipe   *decodePipe
	at     time.Duration
	closed bool
	buf    []byte
}

func (f *File) Audio() (*AudioDecoder, error) {
	if !f.hasAudio {
		return nil, errors.New("media: no audio track")
	}
	return &AudioDecoder{f: f}, nil
}
func (a *AudioDecoder) SampleRate() int { return sampleRate }
func (a *AudioDecoder) Channels() int   { return 2 }
func (a *AudioDecoder) Len() int64 {
	if a.f.Duration <= 0 {
		return -1
	}
	return int64(a.f.Duration.Seconds() * sampleRate)
}
func (a *AudioDecoder) SetPosition(frame int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return io.ErrClosedPipe
	}
	a.pipe.close()
	a.pipe, a.at = nil, time.Duration(float64(max(0, frame))/sampleRate*float64(time.Second))
	return nil
}
func (a *AudioDecoder) Read(samples []float32) (int, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return 0, io.EOF
	}
	if a.pipe == nil {
		var err error
		a.pipe, err = startPipe(a.f, a.at, "-map", "0:a:0", "-vn", "-ac", "2", "-ar", strconv.Itoa(sampleRate), "-f", "f32le", "pipe:1")
		if err != nil {
			a.mu.Unlock()
			return 0, err
		}
	}
	pipe := a.pipe
	a.mu.Unlock()
	if cap(a.buf) < len(samples)*4 {
		a.buf = make([]byte, len(samples)*4)
	}
	buf := a.buf[:len(samples)*4]
	n, err := pipe.read(buf)
	for i := 0; i < n/4; i++ {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return n / 4, err
}
func (a *AudioDecoder) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	a.pipe.close()
	return nil
}
