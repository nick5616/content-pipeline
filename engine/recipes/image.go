package recipes

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nick5616/canon/engine/media"
)

// Widths cover the portfolios (320, 800, 1600) and the holodeck's wall and
// inspect views (2048, 3200). Never upscaled (D47).
var thumbWidths = []int{320, 800, 1600, 2048, 3200}

var thumb = Recipe{
	Name: "thumb", Version: 1,
	Params:  map[string]any{"widths": thumbWidths, "quality": 80, "format": "webp"},
	Applies: isImage,
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		files, err := sizes(ctx, t, s.Path, scratch, thumbWidths, 80)
		return Result{Files: files}, err
	},
}

var lqip = Recipe{
	Name: "lqip", Version: 1,
	Params:  map[string]any{"width": 32, "quality": 40, "format": "webp"},
	Applies: isImage,
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		out := filepath.Join(scratch, "lqip.webp")
		if _, err := resize(ctx, t, s.Path, out, 32, 40); err != nil {
			return Result{}, err
		}
		return Result{Files: []File{{Name: "lqip.webp", Type: &media.WebP}}}, nil
	},
}

var palette = Recipe{
	Name: "palette", Version: 1,
	Params:  map[string]any{"colors": 5, "sample": 64},
	Applies: func(s Source) bool { return isImage(s) && s.FirstImage },
	Facts:   []string{"palette"},
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		small := filepath.Join(scratch, "sample.png")
		if _, err := resize(ctx, t, s.Path, small, 64, 0); err != nil {
			return Result{}, err
		}
		f, err := os.Open(small)
		if err != nil {
			return Result{}, err
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			return Result{}, fmt.Errorf("palette: %w", err)
		}
		cols := medianCut(img, 5)
		if len(cols) == 0 {
			return Result{}, nil // fully transparent
		}
		hex := make([]any, len(cols))
		for i, c := range cols {
			hex[i] = c
		}
		// sample.png is scratch only; it isn't listed, so it isn't kept.
		return Result{Facts: map[string]any{"palette": hex}}, nil
	},
}

// sizes writes w<width>.webp for each width up to the source's own, which
// replaces any wider one.
func sizes(ctx context.Context, t Tools, src, scratch string, widths []int, q int) ([]File, error) {
	var files []File
	for _, w := range widths {
		tmp := filepath.Join(scratch, fmt.Sprintf("tmp-%d.webp", w))
		got, err := resize(ctx, t, src, tmp, w, q)
		if err != nil {
			return nil, err
		}
		if got < w && len(files) > 0 && files[len(files)-1].Name == fmt.Sprintf("w%d.webp", got) {
			os.Remove(tmp)
			break
		}
		name := fmt.Sprintf("w%d.webp", got)
		if err := os.Rename(tmp, filepath.Join(scratch, name)); err != nil {
			return nil, err
		}
		files = append(files, File{Name: name, Type: &media.WebP})
		if got < w {
			break
		}
	}
	return files, nil
}

// resize writes src at most width px wide (height unbounded), rotated
// upright, converted to sRGB and stripped of metadata. out's extension
// picks the format; q is WebP quality (0 for PNG). It returns the output's
// width.
func resize(ctx context.Context, t Tools, src, out string, width, q int) (int, error) {
	save := out + "[strip]"
	if q > 0 {
		save = fmt.Sprintf("%s[Q=%d,strip]", out, q)
	}
	_, err := t.Run(ctx, "vips", "thumbnail", src, save, strconv.Itoa(width),
		"--height", "100000", "--size", "down", "--export-profile", "srgb")
	if err != nil {
		return 0, err
	}
	b, err := t.Run(ctx, "vipsheader", "-f", "width", out)
	if err != nil {
		return 0, err
	}
	w, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("vipsheader: %q", b)
	}
	return w, nil
}

// medianCut picks up to n colors: split the box with the widest channel
// range at its median until there are n, then average each box. Opaque
// pixels only. Deterministic: same pixels, same palette, most-used first.
func medianCut(img image.Image, n int) []string {
	type px [3]uint8
	var all []px
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			// Un-premultiply.
			all = append(all, px{uint8(r * 0xffff / a >> 8), uint8(g * 0xffff / a >> 8), uint8(bl * 0xffff / a >> 8)})
		}
	}
	if len(all) == 0 {
		return nil
	}
	boxes := [][]px{all}
	rng := func(box []px) (int, int) {
		best, ch := -1, 0
		for c := 0; c < 3; c++ {
			lo, hi := 255, 0
			for _, p := range box {
				lo, hi = min(lo, int(p[c])), max(hi, int(p[c]))
			}
			if hi-lo > best {
				best, ch = hi-lo, c
			}
		}
		return best, ch
	}
	for len(boxes) < n {
		bi, bestScore, bestCh := -1, 0, 0
		for i, box := range boxes {
			r, ch := rng(box)
			if score := r * len(box); len(box) > 1 && r > 0 && score > bestScore {
				bi, bestScore, bestCh = i, score, ch
			}
		}
		if bi < 0 {
			break
		}
		box := boxes[bi]
		sort.SliceStable(box, func(i, j int) bool {
			if box[i][bestCh] != box[j][bestCh] {
				return box[i][bestCh] < box[j][bestCh]
			}
			return string(box[i][:]) < string(box[j][:])
		})
		mid := len(box) / 2
		boxes[bi] = box[:mid]
		boxes = append(boxes, box[mid:])
	}
	sort.SliceStable(boxes, func(i, j int) bool { return len(boxes[i]) > len(boxes[j]) })
	var out []string
	seen := map[string]bool{}
	for _, box := range boxes {
		var sum [3]int
		for _, p := range box {
			for c := 0; c < 3; c++ {
				sum[c] += int(p[c])
			}
		}
		h := fmt.Sprintf("#%02x%02x%02x", (sum[0]+len(box)/2)/len(box), (sum[1]+len(box)/2)/len(box), (sum[2]+len(box)/2)/len(box))
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}
