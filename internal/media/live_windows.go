package media

import (
	"errors"
	"image"
	"image/draw"
	"io"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"
)

type decodedFrame struct {
	image *image.RGBA
	at    time.Duration
	err   error
}

type playback struct {
	video  *VideoDecoder
	audio  *AudioDecoder
	player *audio.Player
	frames chan decodedFrame
	free   chan *image.RGBA
	done   chan struct{}
	stop   sync.Once
}

func newPlayback(f *File, at time.Duration) *playback {
	v, _ := f.Video()
	v.at = at
	s := &playback{video: v, frames: make(chan decodedFrame, 2), free: make(chan *image.RGBA, 5), done: make(chan struct{})}
	if f.hasAudio {
		s.audio, _ = f.Audio()
		s.audio.at = at
		s.player = audio.NewPlayer(s.audio)
	}
	go func() {
		defer close(s.frames)
		duration := time.Duration(float64(time.Second) / f.fps)
		allocated := 0
		for {
			var img *image.RGBA
			select {
			case <-s.done:
				return
			case img = <-s.free:
			default:
				if allocated < cap(s.free) {
					img = image.NewRGBA(image.Rect(0, 0, f.W, f.H))
					allocated++
				} else {
					select {
					case <-s.done:
						return
					case img = <-s.free:
					}
				}
			}
			_, err := v.next(img)
			frame := decodedFrame{image: img, at: at, err: err}
			select {
			case <-s.done:
				return
			case s.frames <- frame:
			}
			if err != nil {
				return
			}
			at += duration
		}
	}()
	return s
}
func (s *playback) close() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		close(s.done)
		s.video.Close()
		if s.audio != nil {
			s.audio.Close()
		}
		if s.player != nil {
			s.player.Close()
		}
	})
}

// Live owns one bounded frame queue and one audio clock. Seeking replaces that
// queue, so frames decoded for the old position can never reach the new one.
type Live struct {
	mu                                           sync.Mutex
	file                                         *File
	session                                      *playback
	pending                                      *decodedFrame
	picture                                      *image.RGBA
	base, offset, end                            time.Duration
	started                                      time.Time
	speed, volume                                float64
	muted, paused, closed, seeking, ready, ended bool
}

// Windows playback starts through FFmpeg, which has no reusable AVURLAsset.
type WarmVideo struct{}

func PreloadVideo(string, map[string]string) *WarmVideo { return nil }
func (*WarmVideo) Close()                               {}
func (*WarmVideo) Open() (*Live, error)                 { return nil, errors.New("media: video preload unavailable") }

func OpenLive(source string) (*Live, error) { return openStream(source, nil, false) }
func OpenNative(source string, headers map[string]string, vod bool) (*Live, error) {
	return openStream(source, headers, vod)
}
func OpenVideo(source string, headers map[string]string) (*Live, error) {
	return openStream(source, headers, true)
}
func openStream(source string, headers map[string]string, vod bool) (*Live, error) {
	if source == "" {
		return nil, errors.New("media: empty stream address")
	}
	f, err := openFile(source, headers)
	if err != nil {
		return nil, err
	}
	if !vod {
		f.Duration = 0
	} // an HLS window's duration is not the end of the live room
	l := &Live{file: f, speed: 1, volume: 1, muted: true}
	l.session = newPlayback(f, 0)
	return l, nil
}

func (l *Live) position() time.Duration {
	at := l.base
	if l.ready && !l.paused {
		if l.session.player != nil {
			at = l.offset + l.session.player.Position()
		} else {
			at += time.Duration(float64(time.Since(l.started)) * l.speed)
		}
	}
	if l.file.Duration > 0 {
		at = min(at, l.file.Duration)
	}
	return at
}
func (l *Live) Position() time.Duration { l.mu.Lock(); defer l.mu.Unlock(); return l.position() }
func (l *Live) Play() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || !l.paused {
		return
	}
	l.paused, l.started = false, time.Now()
	if l.ready && l.session.player != nil {
		l.session.player.Play()
	}
}
func (l *Live) Pause() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.paused {
		return
	}
	l.base = l.position()
	l.paused = true
	if l.session.player != nil {
		l.session.player.Pause()
	}
}
func (l *Live) SetSpeed(speed float64) {
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.base, l.started = l.position(), time.Now()
	l.speed = speed
	if l.session.player != nil {
		l.session.player.SetSpeed(speed).SetPreservesPitch(true)
	}
}
func (l *Live) audioSettings() {
	if p := l.session.player; p != nil {
		volume := l.volume
		if l.muted {
			volume = 0
		}
		p.SetVolume(volume).SetSpeed(l.speed).SetPreservesPitch(true)
	}
}
func (l *Live) SetMuted(muted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.muted = muted
	l.audioSettings()
}
func (l *Live) SetVolume(volume float64) {
	if math.IsNaN(volume) || math.IsInf(volume, 0) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.volume = max(0, min(1, volume))
	l.audioSettings()
}
func (l *Live) Seeking() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.seeking }
func (l *Live) Seek(at time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	at = max(0, at)
	if l.file.Duration > 0 {
		at = min(at, max(0, l.file.Duration-time.Millisecond))
	}
	l.session.close()
	l.session = newPlayback(l.file, at)
	l.pending, l.picture = nil, nil
	l.base, l.offset, l.end = at, at, at
	l.ready, l.seeking, l.ended = false, true, false
	l.audioSettings()
}
func (l *Live) Rewind() error { l.Seek(0); return nil }

func (l *Live) Frame() (ui.VideoFrame, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ui.VideoFrame{}, io.EOF
	}
	var picture *image.RGBA
	for {
		if l.pending == nil {
			select {
			case frame, ok := <-l.session.frames:
				if !ok {
					l.ended = true
					break
				}
				if frame.err != nil {
					if frame.err != io.EOF && frame.err != io.ErrUnexpectedEOF {
						return ui.VideoFrame{}, frame.err
					}
					l.ended = true
					break
				}
				l.pending = &frame
			default:
			}
		}
		if l.pending == nil || l.ready && (l.paused || l.pending.at > l.position()) {
			break
		}
		if l.picture != nil {
			l.session.free <- l.picture
		}
		l.picture, picture = l.pending.image, l.pending.image
		l.end = l.pending.at + time.Duration(float64(time.Second)/l.file.fps)
		l.pending = nil
		if !l.ready {
			l.ready, l.seeking, l.started = true, false, time.Now()
			l.audioSettings()
			if !l.paused && l.session.player != nil {
				l.session.player.Play()
			}
		}
	}
	if l.ended && !l.paused && l.position() >= l.end-time.Millisecond {
		return ui.VideoFrame{}, io.EOF
	}
	frame := ui.VideoFrame{Duration: time.Duration(float64(time.Second) / l.file.fps)}
	if picture != nil {
		frame.Image = picture
	} // do not put a typed nil in an interface
	return frame, nil
}
func (l *Live) Next() (ui.VideoFrame, error) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		f, err := l.Frame()
		if err != nil || f.Image != nil {
			return f, err
		}
		time.Sleep(4 * time.Millisecond)
	}
	return ui.VideoFrame{}, errors.New("media: no video picture for 20 s")
}
func (l *Live) WaitReady() (int, int, error) {
	_, err := l.Next()
	return l.file.W, l.file.H, err
}
func (l *Live) UntilEnd() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file.Duration == 0 {
		return time.Hour
	}
	return max(0, l.file.Duration-l.position())
}
func (l *Live) UsePixels() error { return nil }
func (l *Live) Snapshot() (*image.RGBA, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.picture == nil {
		return nil, errors.New("media: no current picture")
	}
	img := image.NewRGBA(l.picture.Bounds())
	draw.Draw(img, img.Bounds(), l.picture, image.Point{}, draw.Src)
	return img, nil
}
func (l *Live) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.base = l.position()
		l.closed = true
		l.session.close()
		l.picture, l.pending = nil, nil
	}
	return nil
}
