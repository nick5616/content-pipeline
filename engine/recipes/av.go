package recipes

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nick5616/canon/engine/media"
)

// ffmpegBase starts every ffmpeg run. Outputs carry no metadata, chapters
// or encoder tags, like the sanitizer's (so they pass the same verify).
var ffmpegBase = []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-protocol_whitelist", "file,pipe"}

var clean = []string{"-map_metadata", "-1", "-map_chapters", "-1",
	"-fflags", "+bitexact", "-flags:v", "+bitexact", "-flags:a", "+bitexact"}

func ffmpeg(ctx context.Context, t Tools, args ...string) error {
	_, err := t.Run(ctx, "ffmpeg", append(append([]string{}, ffmpegBase...), args...)...)
	return err
}

// duration asks ffprobe for a file's length in seconds.
func duration(ctx context.Context, t Tools, path string) (float64, error) {
	out, err := t.Run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path)
	if err != nil {
		return 0, err
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("ffprobe: no duration for %s (%q)", filepath.Base(path), out)
	}
	return d, nil
}

func secs(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

// v2: receipts record each size's width and height, like thumb@v2.
var poster = Recipe{
	Name: "poster", Version: 2,
	Params:  map[string]any{"at": 0.1, "widths": thumbWidths, "quality": 80, "format": "webp"},
	Applies: isVideo,
	FFmpeg:  true,
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		d, err := duration(ctx, t, s.Path)
		if err != nil {
			return Result{}, err
		}
		frame := filepath.Join(scratch, "frame.png")
		if err := ffmpeg(ctx, t, "-ss", secs(d*0.1), "-i", s.Path, "-map", "0:V:0", "-frames:v", "1", "-f", "image2", frame); err != nil {
			return Result{}, err
		}
		files, err := sizes(ctx, t, frame, scratch, thumbWidths, 80)
		return Result{Files: files}, err
	},
}

var web = Recipe{
	Name: "web", Version: 1,
	Params: map[string]any{"codec": "h264", "crf": 23, "preset": "veryfast", "maxWidth": 1920,
		"audio": "aac", "audioKbps": 128},
	Applies: isVideo,
	FFmpeg:  true,
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		args := []string{"-i", s.Path, "-map", "0:V:0", "-map", "0:a:0?",
			"-c:v", "libx264", "-crf", "23", "-preset", "veryfast", "-pix_fmt", "yuv420p",
			"-vf", "scale='min(1920,iw)':-2", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart"}
		args = append(append(args, clean...), "-f", "mp4", filepath.Join(scratch, "web.mp4"))
		if err := ffmpeg(ctx, t, args...); err != nil {
			return Result{}, err
		}
		return Result{Files: []File{{Name: "web.mp4", Type: &media.MP4}}}, nil
	},
}

// mp3 encodes the first audio stream at kbps, stereo 44.1 kHz, with no tags
// and no Xing/LAME header (CBR doesn't need one for seeking). in goes
// before -i (seeking), out after it (filters).
func mp3(ctx context.Context, t Tools, src, dst string, kbps int, in, out []string) error {
	args := append([]string{}, in...)
	args = append(args, "-i", src, "-map", "0:a:0", "-ac", "2", "-ar", "44100",
		"-c:a", "libmp3lame", "-b:a", fmt.Sprintf("%dk", kbps))
	args = append(append(append(args, out...), clean...), "-write_id3v2", "0", "-write_xing", "0", "-f", "mp3", dst)
	return ffmpeg(ctx, t, args...)
}

var preview = Recipe{
	Name: "preview", Version: 1,
	Params:  map[string]any{"kbps": 128, "clip": 30, "from": "third", "fade": 1},
	Applies: isAudio,
	FFmpeg:  true,
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		if err := mp3(ctx, t, s.Path, filepath.Join(scratch, "full.mp3"), 128, nil, nil); err != nil {
			return Result{}, err
		}
		d, err := duration(ctx, t, s.Path)
		if err != nil {
			return Result{}, err
		}
		start, length := d/3, 30.0
		if d <= 30 {
			start, length = 0, d
		} else if start+length > d {
			start = d - length
		}
		fade := fmt.Sprintf("afade=t=in:d=1,afade=t=out:st=%s:d=1", secs(math.Max(0, length-1)))
		if err := mp3(ctx, t, s.Path, filepath.Join(scratch, "clip.mp3"), 128,
			[]string{"-ss", secs(start), "-t", secs(length)}, []string{"-af", fade}); err != nil {
			return Result{}, err
		}
		return Result{Files: []File{{Name: "clip.mp3", Type: &media.MP3}, {Name: "full.mp3", Type: &media.MP3}}}, nil
	},
}

var djMP3 = Recipe{
	Name: "dj-mp3", Version: 1,
	Params: map[string]any{"kbps": 192},
	// Lossless sources only: an MP3 plays as it is (D49).
	Applies: func(s Source) bool {
		return s.Type.Name == media.AIFF.Name || s.Type.Name == media.WAV.Name || s.Type.Name == media.FLAC.Name
	},
	FFmpeg: true,
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		if err := mp3(ctx, t, s.Path, filepath.Join(scratch, "track.mp3"), 192, nil, nil); err != nil {
			return Result{}, err
		}
		return Result{Files: []File{{Name: "track.mp3", Type: &media.MP3}}}, nil
	},
}

// decodeMono decodes the first audio stream to mono float32 PCM at rate.
func decodeMono(ctx context.Context, t Tools, path string, rate int) ([]float32, error) {
	raw, err := t.Run(ctx, "ffmpeg", append(append([]string{}, ffmpegBase...),
		"-i", path, "-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(rate), "-f", "f32le", "-")...)
	if err != nil {
		return nil, err
	}
	pcm := make([]float32, len(raw)/4)
	for i := range pcm {
		pcm[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return pcm, nil
}

// peaksData is the audiowaveform JSON format (version 2, 8-bit), which
// players such as peaks.js read directly.
type peaksData struct {
	Version         int    `json:"version"`
	Channels        int    `json:"channels"`
	SampleRate      int    `json:"sample_rate"`
	SamplesPerPixel int    `json:"samples_per_pixel"`
	Bits            int    `json:"bits"`
	Length          int    `json:"length"`
	Data            []int8 `json:"data"`
}

const peaksRate, peaksColumns = 8000, 1000

var peaks = Recipe{
	Name: "peaks", Version: 1,
	Params:  map[string]any{"columns": peaksColumns, "rate": peaksRate, "bits": 8},
	Applies: isAudio,
	FFmpeg:  true,
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		pcm, err := decodeMono(ctx, t, s.Path, peaksRate)
		if err != nil {
			return Result{}, err
		}
		return Result{Files: []File{{Name: "peaks.json", JSON: peaksOf(pcm)}}}, nil
	},
}

func peaksOf(pcm []float32) peaksData {
	per := max(1, (len(pcm)+peaksColumns-1)/peaksColumns)
	cols := (len(pcm) + per - 1) / per
	p := peaksData{Version: 2, Channels: 1, SampleRate: peaksRate, SamplesPerPixel: per, Bits: 8, Length: cols, Data: make([]int8, 0, cols*2)}
	q := func(v float32) int8 { return int8(math.Round(math.Max(-1, math.Min(1, float64(v))) * 127)) }
	for c := 0; c < cols; c++ {
		lo, hi := float32(0), float32(0)
		for _, v := range pcm[c*per : min(len(pcm), (c+1)*per)] {
			lo, hi = min(lo, v), max(hi, v)
		}
		p.Data = append(p.Data, q(lo), q(hi))
	}
	return p
}
