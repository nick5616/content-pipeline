package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
)

// origMeta is what ingest reads from the original before stripping it. It
// stays local: only the date (D30) can end up in the draft, for review.
type origMeta struct {
	camera bool   // the original names a camera make or model (D29)
	date   string // capture day, YYYY-MM-DD, or ""
}

// dateTags are tried in order for the capture date.
var dateTags = []string{
	"ExifIFD:DateTimeOriginal", "Keys:CreationDate", "UserData:DateTimeOriginal",
	"XMP-exif:DateTimeOriginal", "XMP-photoshop:DateCreated", "ExifIFD:CreateDate",
}

func (in *Ingester) originalMeta(ctx context.Context, path string) (origMeta, error) {
	out, err := in.Run(ctx, "exiftool", "-config", "", "-j", "-G1", "-a", "-n",
		"-Make", "-Model", "-DateTimeOriginal", "-CreationDate", "-DateCreated", "-CreateDate", path)
	if err != nil {
		return origMeta{}, err
	}
	var res []map[string]any
	if err := json.Unmarshal(out, &res); err != nil || len(res) != 1 {
		return origMeta{}, fmt.Errorf("exiftool: unexpected output reading the original")
	}
	var m origMeta
	for k, v := range res[0] {
		_, tag, _ := strings.Cut(k, ":")
		if (tag == "Make" || tag == "Model") && strings.TrimSpace(fmt.Sprint(v)) != "" {
			m.camera = true
		}
	}
	for _, k := range dateTags {
		if d := captureDay(fmt.Sprint(res[0][k])); d != "" {
			m.date = d
			break
		}
	}
	return m, nil
}

// captureDay turns "2024:05:06 07:08:09" or "2024-05-06T07:08:09Z" into
// "2024-05-06", rejecting zero and implausible dates.
func captureDay(s string) string {
	if len(s) < 10 {
		return ""
	}
	d := strings.NewReplacer(":", "-").Replace(s[:10])
	t, err := manifest.ParseDate(d)
	if err != nil || t.Year() < 1900 || t.After(time.Now().AddDate(0, 0, 2)) {
		return ""
	}
	return d
}

// facts measures the sanitized blob for the item's facts block.
func (in *Ingester) facts(ctx context.Context, t media.Type, kind manifest.Kind, blob string) (manifest.ItemFacts, error) {
	switch t.Kind {
	case media.Image:
		w, h, err := in.imageSize(ctx, blob)
		if err != nil {
			return nil, err
		}
		return manifest.ItemFacts{"pixelWidth": w, "pixelHeight": h}, nil
	case media.Video, media.Audio:
		return in.avFacts(ctx, t, blob)
	}
	return manifest.ItemFacts{}, nil
}

// imageSize returns display dimensions: swapped for 90/270 degree rotation.
func (in *Ingester) imageSize(ctx context.Context, path string) (int, int, error) {
	out, err := in.Run(ctx, "exiftool", "-config", "", "-j", "-n", "-ImageSize", "-Orientation", "-Rotation", path)
	if err != nil {
		return 0, 0, err
	}
	var res []struct {
		ImageSize   string `json:"ImageSize"`
		Orientation int    `json:"Orientation"`
		Rotation    int    `json:"Rotation"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res) != 1 {
		return 0, 0, fmt.Errorf("exiftool: no image size")
	}
	parts := strings.FieldsFunc(res[0].ImageSize, func(r rune) bool { return r == 'x' || r == ' ' })
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("exiftool: image size %q", res[0].ImageSize)
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || w < 1 || h < 1 {
		return 0, 0, fmt.Errorf("exiftool: image size %q", res[0].ImageSize)
	}
	if o := res[0].Orientation; o >= 5 && o <= 8 || res[0].Rotation == 90 || res[0].Rotation == 270 {
		w, h = h, w
	}
	return w, h, nil
}

func (in *Ingester) avFacts(ctx context.Context, t media.Type, path string) (manifest.ItemFacts, error) {
	out, err := in.Run(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-f", t.Demuxer,
		"-show_streams", "-show_format", "-of", "json", path)
	if err != nil {
		return nil, err
	}
	var p struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
			SampleRate   string `json:"sample_rate"`
			SideData     []struct {
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	f := manifest.ItemFacts{}
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil && d > 0 {
		f["duration"] = round3(d)
	}
	for _, s := range p.Streams {
		switch {
		case s.CodecType == "video" && t.Kind == media.Video && f["pixelWidth"] == nil:
			w, h := s.Width, s.Height
			for _, sd := range s.SideData {
				if r := math.Abs(sd.Rotation); r == 90 || r == 270 {
					w, h = h, w
				}
			}
			if w > 0 && h > 0 {
				f["pixelWidth"], f["pixelHeight"] = w, h
			}
			if fps := rate(s.AvgFrameRate); fps > 0 {
				f["fps"] = round3(fps)
			}
		case s.CodecType == "audio" && t.Kind == media.Audio && f["sampleRate"] == nil:
			if sr, err := strconv.Atoi(s.SampleRate); err == nil && sr > 0 {
				f["sampleRate"] = sr
			}
		}
	}
	return f, nil
}

func rate(s string) float64 {
	n, d, ok := strings.Cut(s, "/")
	num, err1 := strconv.ParseFloat(n, 64)
	if !ok {
		return num
	}
	den, err2 := strconv.ParseFloat(d, 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

// round3 keeps facts stable across platforms' float formatting.
func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
