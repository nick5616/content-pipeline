package recipes

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/sanitize"
	"github.com/nick5616/canon/internal/testmedia"
)

func src(t *testing.T, name string, typ media.Type) Source {
	t.Helper()
	return Source{Path: filepath.Join(testmedia.Dir(t), name), Type: typ, FirstImage: true}
}

// run runs r on s and verifies every media output, as the job runner does.
func run(t *testing.T, r Recipe, s Source) (Result, string) {
	t.Helper()
	if !r.Applies(s) {
		t.Fatalf("%s doesn't apply to %s", r.ID(), s.Type.Name)
	}
	scratch := t.TempDir()
	res, err := r.Run(context.Background(), NewTools(), s, scratch)
	if err != nil {
		t.Fatalf("%s on %s: %v", r.ID(), s.Type.Name, err)
	}
	for _, f := range res.Files {
		if f.Type == nil {
			continue
		}
		if _, err := sanitize.NewVerifier().Verify(context.Background(), *f.Type, filepath.Join(scratch, f.Name)); err != nil {
			t.Errorf("%s: %s fails verify: %v", r.ID(), f.Name, err)
		}
	}
	return res, scratch
}

func header(t *testing.T, path, field string) int {
	t.Helper()
	out, err := exec.Command("vipsheader", "-f", field, path).Output()
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func names(fs []File) string {
	var n []string
	for _, f := range fs {
		n = append(n, f.Name)
	}
	return strings.Join(n, ",")
}

func TestThumbNeverUpscales(t *testing.T) {
	// The fixtures are tiny: each gets exactly one size, its own.
	for _, c := range []struct {
		file string
		typ  media.Type
		want string
	}{
		{"png.png", media.PNG, "w64.webp"},
		{"gif.gif", media.GIF, "w64.webp"},
		{"webp.webp", media.WebP, "w64.webp"},
		{"tiff.tif", media.TIFF, "w64.webp"},
		{"heic.heic", media.HEIC, "w128.webp"},
		// Orientation 6: 320x240 stored, 240x320 upright.
		{"jpeg.jpg", media.JPEG, "w240.webp"},
	} {
		res, dir := run(t, thumb, src(t, c.file, c.typ))
		if got := names(res.Files); got != c.want {
			t.Errorf("%s: %s, want %s", c.file, got, c.want)
		}
		if c.file == "jpeg.jpg" {
			if h := header(t, filepath.Join(dir, c.want), "height"); h != 320 {
				t.Errorf("jpeg: height %d, want 320 (rotated upright)", h)
			}
		}
	}
}

func TestThumbWidths(t *testing.T) {
	// A 2500 px wide image gets 320, 800, 1600, 2048 and its own 2500.
	dir := t.TempDir()
	big := filepath.Join(dir, "big.png")
	if out, err := exec.Command("vips", "black", big, "2500", "400").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	res, out := run(t, thumb, Source{Path: big, Type: media.PNG})
	if got := names(res.Files); got != "w320.webp,w800.webp,w1600.webp,w2048.webp,w2500.webp" {
		t.Errorf("got %s", got)
	}
	if w := header(t, filepath.Join(out, "w800.webp"), "width"); w != 800 {
		t.Errorf("w800 is %d wide", w)
	}
}

// Display P3 art must come out as sRGB (D46): pure sRGB red, stored as
// P3 with its profile, converts back to about (255, 0, 0).
func TestP3BecomesSRGB(t *testing.T) {
	dir := t.TempDir()
	red, p3 := filepath.Join(dir, "red.png"), filepath.Join(dir, "p3.png")
	cmds := [][]string{
		{"vips", "black", filepath.Join(dir, "k.v"), "40", "40", "--bands", "3"},
		{"vips", "linear", filepath.Join(dir, "k.v"), red, "1 1 1", "255 0 0", "--uchar"},
		{"vips", "icc_transform", red, p3, "p3", "--input-profile", "srgb", "--embedded"},
	}
	for _, c := range cmds {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", c, err, out)
		}
	}
	// Sanity: the P3 file's raw pixels aren't sRGB red.
	if px := point(t, p3); px[0] > 245 && px[1] < 10 {
		t.Fatalf("p3 fixture looks like plain sRGB: %v", px)
	}
	res, out := run(t, thumb, Source{Path: p3, Type: media.PNG})
	px := point(t, filepath.Join(out, res.Files[0].Name))
	if px[0] < 245 || px[1] > 15 || px[2] > 15 {
		t.Errorf("P3 red came out as %v, want about 255 0 0", px)
	}
}

func point(t *testing.T, path string) []int {
	t.Helper()
	out, err := exec.Command("vips", "getpoint", path, "20", "20").Output()
	if err != nil {
		t.Fatal(err)
	}
	var px []int
	for _, f := range strings.Fields(string(out)) {
		n, _ := strconv.ParseFloat(f, 64)
		px = append(px, int(n+0.5))
	}
	return px
}

func TestLQIP(t *testing.T) {
	res, dir := run(t, lqip, src(t, "png.png", media.PNG))
	if names(res.Files) != "lqip.webp" || header(t, filepath.Join(dir, "lqip.webp"), "width") != 32 {
		t.Errorf("lqip: %s", names(res.Files))
	}
}

func TestPalette(t *testing.T) {
	a, _ := run(t, palette, src(t, "png.png", media.PNG))
	b, _ := run(t, palette, src(t, "png.png", media.PNG))
	p, ok := a.Facts["palette"].([]any)
	if !ok || len(p) == 0 || len(p) > 5 || len(a.Files) != 0 {
		t.Fatalf("palette: %+v", a)
	}
	for _, c := range p {
		if s, _ := c.(string); len(s) != 7 || s[0] != '#' || strings.ToLower(s) != s {
			t.Errorf("color %q isn't #rrggbb", c)
		}
	}
	if !reflect.DeepEqual(a.Facts, b.Facts) {
		t.Errorf("not deterministic: %v vs %v", a.Facts, b.Facts)
	}
	if palette.Applies(Source{Type: media.PNG}) {
		t.Error("palette applies to an item's second image")
	}
}

func TestVideo(t *testing.T) {
	for _, f := range []struct {
		name string
		typ  media.Type
	}{{"mp4.mp4", media.MP4}, {"mov.mov", media.MOV}} {
		res, _ := run(t, poster, src(t, f.name, f.typ))
		if names(res.Files) != "w160.webp" {
			t.Errorf("poster of %s: %s", f.name, names(res.Files))
		}
		res, _ = run(t, web, src(t, f.name, f.typ))
		if names(res.Files) != "web.mp4" {
			t.Errorf("web of %s: %s", f.name, names(res.Files))
		}
	}
}

func TestAudio(t *testing.T) {
	for _, f := range []struct {
		name     string
		typ      media.Type
		lossless bool
	}{{"mp3.mp3", media.MP3, false}, {"wav.wav", media.WAV, true}, {"flac.flac", media.FLAC, true},
		{"aiff.aiff", media.AIFF, true}, {"m4a.m4a", media.M4A, false}} {
		s := src(t, f.name, f.typ)
		res, _ := run(t, preview, s)
		if names(res.Files) != "clip.mp3,full.mp3" {
			t.Errorf("preview of %s: %s", f.name, names(res.Files))
		}
		res, _ = run(t, peaks, s)
		pk, ok := res.Files[0].JSON.(peaksData)
		if !ok || pk.Version != 2 || pk.Length == 0 || len(pk.Data) != pk.Length*2 {
			t.Errorf("peaks of %s: %+v", f.name, res.Files[0].JSON)
		}
		if djMP3.Applies(s) != f.lossless {
			t.Errorf("dj-mp3 applies to %s: %v (D49)", f.name, !f.lossless)
		}
		if f.lossless {
			if res, _ := run(t, djMP3, s); names(res.Files) != "track.mp3" {
				t.Errorf("dj-mp3 of %s: %s", f.name, names(res.Files))
			}
		}
		res, _ = run(t, djgridRecipe, s)
		if _, ok := res.Facts["bpm"].(float64); !ok || res.Files[0].Name != "analysis.json" {
			t.Errorf("djgrid of %s: %+v", f.name, res)
		}
		b, _ := json.Marshal(res.Files[0].JSON)
		if !strings.HasPrefix(string(b), `{"fps":100,"bands":3,"waveform":"`) {
			t.Errorf("analysis.json isn't the crate's format: %.60s", b)
		}
	}
}

func TestMedianCutTransparent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "clear.png")
	if out, err := exec.Command("vips", "black", p, "8", "8", "--bands", "4").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	res, _ := run(t, palette, Source{Path: p, Type: media.PNG, FirstImage: true})
	if _, ok := res.Facts["palette"]; ok {
		t.Errorf("a fully transparent image got a palette: %v", res.Facts)
	}
}

func TestRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range All() {
		if seen[r.Name] || r.Version < 1 || r.Run == nil || r.Applies == nil || r.Params == nil {
			t.Errorf("bad recipe %+v", r.ID())
		}
		seen[r.Name] = true
	}
	if len(seen) != 9 {
		t.Errorf("%d recipes, want the plan's 9", len(seen))
	}
}
