package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/internal/testmedia"
)

var reg = surfaces.MustDefault()

func newIngester(t *testing.T) (*Ingester, string) {
	t.Helper()
	cache := t.TempDir()
	blobs, err := blobstore.Open(cache)
	if err != nil {
		t.Fatal(err)
	}
	in := New(blobs, store.Open(t.TempDir(), "Nicole Belovoskey", reg), 64<<20)
	in.IDs = ids.NewWith(func() time.Time { return time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC) }, rand.New(rand.NewSource(1)))
	return in, cache
}

func ingest(t *testing.T, in *Ingester, path string, opts Options) FileResult {
	t.Helper()
	o, err := in.StagePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return in.Process(context.Background(), o, opts, nil)
}

func fileSum(t *testing.T, path string) (string, time.Time) {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), fi.ModTime()
}

func TestIngestEveryFixture(t *testing.T) {
	in, cache := newIngester(t)
	dir := testmedia.Dir(t)
	files, _ := filepath.Glob(filepath.Join(dir, "*.*"))
	want := map[string]manifest.Kind{
		"jpeg.jpg": manifest.KindPhoto, "jpeg-named.HEIC": manifest.KindPhoto, "png.png": manifest.KindArtwork,
		"webp.webp": manifest.KindPhoto, "tiff.tif": manifest.KindPhoto, "gif.gif": manifest.KindArtwork,
		"heic.heic": manifest.KindPhoto, "mov.mov": manifest.KindVideo, "mp4.mp4": manifest.KindVideo,
		"mp3.mp3": manifest.KindTrack, "aiff.aiff": manifest.KindTrack, "wav.wav": manifest.KindTrack,
		"flac.flac": manifest.KindTrack, "m4a.m4a": manifest.KindTrack, "pdf.pdf": manifest.KindWriting,
	}
	notes := map[string]string{}
	for _, f := range files {
		name := filepath.Base(f)
		sum, mtime := fileSum(t, f)
		r := ingest(t, in, f, Options{})
		if r.Stage != Done || r.Error != nil {
			t.Errorf("%s: %s %v", name, r.Stage, deref(r.Error))
			continue
		}
		// Originals on disk are untouched.
		if s, mt := fileSum(t, f); s != sum || !mt.Equal(mtime) {
			t.Errorf("%s: original changed", name)
		}
		// The kept original is byte-identical, for the archive.
		typ := detectFile(t, f)
		if s, _ := fileSum(t, filepath.Join(cache, "originals", "sha256", sum[:2], sum[2:4], sum+"."+typ.Ext)); s != sum {
			t.Errorf("%s: kept original differs", name)
		}
		if r.Note != nil {
			notes[name] = *r.Note
		}
	}
	// jpeg.jpg and jpeg-named.HEIC are the same JPEG: one draft, and the
	// second ingest says so.
	if len(notes) != 1 || notes["jpeg.jpg"] != "already ingested as jpeg-named" {
		t.Errorf("want jpeg.jpg recognized as the same original as jpeg-named.HEIC, got %v", notes)
	}

	m, _, ps, err := in.Working.Load()
	if err != nil || manifest.HasErrors(ps) {
		t.Fatalf("working manifest: %v %v", err, ps)
	}
	bySlug := map[string]manifest.Item{}
	for _, it := range m.Items {
		bySlug[string(it.Slug)] = it
	}
	for file, kind := range want {
		if file == "jpeg.jpg" || (file == "heic.heic" && !has(files, "heic.heic")) {
			continue
		}
		slug := slugify(strings.TrimSuffix(file, filepath.Ext(file)))
		it, ok := bySlug[slug]
		if !ok {
			t.Errorf("%s: no draft %q", file, slug)
			continue
		}
		if it.Kind != kind || it.Visibility != manifest.ItemVisibilityDraft {
			t.Errorf("%s: kind %s visibility %s", file, it.Kind, it.Visibility)
		}
		if len(it.Assets) != 1 || it.Assets[0].Original == nil {
			t.Errorf("%s: assets %+v", file, it.Assets)
		}
	}

	// Facts and dates from the fixtures (make-media.sh).
	jpg := bySlug["jpeg-named"]
	if jpg.Facts["pixelWidth"] != float64(240) || jpg.Facts["pixelHeight"] != float64(320) {
		t.Errorf("jpeg facts (rotated 90): %v", jpg.Facts)
	}
	if jpg.Date == nil || jpg.Date.Value != "2024-05-06" {
		t.Errorf("jpeg date: %+v", jpg.Date)
	}
	if mov := bySlug["mov"]; mov.Facts["duration"] != float64(1) || mov.Facts["pixelWidth"] != float64(160) || mov.Facts["fps"] != float64(10) {
		t.Errorf("mov facts: %v", mov.Facts)
	}
	if aiff := bySlug["aiff"]; aiff.Facts["sampleRate"] != float64(44100) {
		t.Errorf("aiff facts: %v", aiff.Facts)
	}
	if png := bySlug["png"]; png.Date != nil {
		t.Errorf("png has no capture date but got %+v", png.Date)
	}
}

func TestIdempotent(t *testing.T) {
	in, cache := newIngester(t)
	f := filepath.Join(testmedia.Dir(t), "png.png")
	first := ingest(t, in, f, Options{})
	_, rev1, _, _ := in.Working.Load()
	second := ingest(t, in, f, Options{})
	_, rev2, _, _ := in.Working.Load()
	if first.Stage != Done || second.Stage != Done || *first.Item != *second.Item {
		t.Fatalf("%+v / %+v", first, second)
	}
	if second.Note == nil || *second.Note != "already ingested as png" {
		t.Errorf("note: %v", deref(second.Note))
	}
	if rev1 != rev2 {
		t.Error("second ingest changed the manifest")
	}
	blobs, _ := filepath.Glob(filepath.Join(cache, "blobs", "sha256", "*", "*", "*"))
	if len(blobs) != 1 {
		t.Errorf("blobs: %v", blobs)
	}
	tmp, _ := os.ReadDir(filepath.Join(cache, "tmp"))
	if len(tmp) != 0 {
		t.Errorf("staging left behind: %d entries", len(tmp))
	}
}

func TestSlugsDedupe(t *testing.T) {
	in, _ := newIngester(t)
	dir := t.TempDir()
	src := filepath.Join(testmedia.Dir(t), "png.png")
	b, _ := os.ReadFile(src)
	for i, name := range []string{"Sketch.png", "sketch.PNG"} {
		// Different pixels each time, so they aren't the same blob.
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o644)
		appendIEND(t, p, byte(i))
	}
	r1 := ingest(t, in, filepath.Join(dir, "Sketch.png"), Options{})
	r2 := ingest(t, in, filepath.Join(dir, "sketch.PNG"), Options{})
	if r1.Slug != "sketch" || r2.Slug != "sketch-2" {
		t.Fatalf("slugs %q %q (%v %v)", r1.Slug, r2.Slug, deref(r1.Error), deref(r2.Error))
	}
}

// appendIEND changes a PNG's pixels slightly by rewriting the image as a
// different (still valid) one: it re-encodes with ffmpeg at a new size.
func appendIEND(t *testing.T, path string, n byte) {
	t.Helper()
	tools := New(nil, nil, 0).Run
	size := []string{"64x48", "32x24"}[n]
	if _, err := tools(context.Background(), "ffmpeg", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size="+size, "-frames:v", "1", "-f", "image2", "-c:v", "png", path); err != nil {
		t.Fatal(err)
	}
}

func TestKindOverride(t *testing.T) {
	in, _ := newIngester(t)
	f := filepath.Join(testmedia.Dir(t), "jpeg.jpg")
	r := ingest(t, in, f, Options{Kind: manifest.KindArtwork})
	m, _, _, _ := in.Working.Load()
	if r.Stage != Done || m.Items[0].Kind != manifest.KindArtwork {
		t.Fatalf("%+v", r)
	}
	in2, _ := newIngester(t)
	r = ingest(t, in2, f, Options{Kind: manifest.KindTrack})
	if r.Stage != Failed || !strings.Contains(deref(r.Error), "can't be a track") {
		t.Fatalf("%+v", r)
	}
}

func TestDryRunStoresNothing(t *testing.T) {
	in, cache := newIngester(t)
	r := ingest(t, in, filepath.Join(testmedia.Dir(t), "jpeg.jpg"), Options{DryRun: true})
	if r.Stage != Done || r.Blob == nil {
		t.Fatalf("%+v", r)
	}
	blobs, _ := filepath.Glob(filepath.Join(cache, "blobs", "sha256", "*", "*", "*"))
	originals, _ := filepath.Glob(filepath.Join(cache, "originals", "sha256", "*", "*", "*"))
	if len(blobs)+len(originals) != 0 {
		t.Fatal("dry run stored files")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cache), "working.json")); err == nil {
		t.Fatal("dry run wrote a manifest")
	}
}

func TestHostileRejected(t *testing.T) {
	in, cache := newIngester(t)
	dir := filepath.Join(testmedia.Dir(t), "hostile")
	for name, want := range map[string]string{
		"playlist.mp4":         "HLS playlist",
		"concat.mov":           "concat script",
		"archive.jpg":          "ZIP archive",
		"empty.png":            "empty",
		"truncated.jpg":        "",
		"pdf-embedded-gps.pdf": "embedded image carries EXIF",
	} {
		r := ingest(t, in, filepath.Join(dir, name), Options{})
		if r.Stage != Failed || r.Error == nil || !strings.Contains(*r.Error, want) {
			t.Errorf("%s: %s %q", name, r.Stage, deref(r.Error))
		}
	}
	blobs, _ := filepath.Glob(filepath.Join(cache, "blobs", "sha256", "*", "*", "*"))
	if len(blobs) != 0 {
		t.Errorf("rejected files left blobs: %v", blobs)
	}
	if m, _, _, _ := in.Working.Load(); len(m.Items) != 0 {
		t.Errorf("rejected files left drafts")
	}
}

// brokenSanitizer passes input through: the verify gate must stop it, with
// nothing stored and no draft.
type brokenSanitizer struct{}

func (brokenSanitizer) Sanitize(_ context.Context, _ media.Type, in, out string) error {
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o600)
}

func TestGateBlocksIngest(t *testing.T) {
	in, cache := newIngester(t)
	in.Sanitizer = brokenSanitizer{}
	r := ingest(t, in, filepath.Join(testmedia.Dir(t), "jpeg.jpg"), Options{})
	if r.Stage != Failed || !strings.Contains(deref(r.Error), "GPS") {
		t.Fatalf("%+v %s", r, deref(r.Error))
	}
	blobs, _ := filepath.Glob(filepath.Join(cache, "blobs", "sha256", "*", "*", "*"))
	if len(blobs) != 0 {
		t.Fatal("blob stored despite failed verify")
	}
	if m, _, _, _ := in.Working.Load(); len(m.Items) != 0 {
		t.Fatal("draft created despite failed verify")
	}
}

func TestTooLarge(t *testing.T) {
	in, _ := newIngester(t)
	in.MaxBytes = 100
	if _, err := in.StageReader(bytes.NewReader(make([]byte, 101)), "big.bin"); err == nil || !strings.Contains(err.Error(), "maxIngestBytes") {
		t.Fatalf("got %v", err)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Night Market (v2)": "night-market-v2", "IMG_0042": "img-0042", "Café Études": "cafe-etudes",
		"   ": "untitled", "日本": "untitled", "--a--b--": "a-b",
	} {
		if got := slugify(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func detectFile(t *testing.T, path string) media.Type {
	f, _ := os.Open(path)
	defer f.Close()
	typ, err := media.Detect(f)
	if err != nil {
		t.Fatal(err)
	}
	return typ
}

func has(files []string, name string) bool {
	for _, f := range files {
		if filepath.Base(f) == name {
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Two originals that differ only in metadata sanitize to the same bytes:
// one blob, one draft.
func TestSameContentDifferentMetadata(t *testing.T) {
	in, _ := newIngester(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.jpg"), filepath.Join(dir, "b.jpg")
	src, _ := os.ReadFile(filepath.Join(testmedia.Dir(t), "jpeg.jpg"))
	os.WriteFile(a, src, 0o644)
	os.WriteFile(b, src, 0o644)
	if _, err := in.Run(context.Background(), "exiftool", "-config", "", "-q", "-overwrite_original", "-Comment=another note", b); err != nil {
		t.Fatal(err)
	}
	r1, r2 := ingest(t, in, a, Options{}), ingest(t, in, b, Options{})
	if r2.Note == nil || *r2.Note != "same content as a" || *r1.Blob != *r2.Blob {
		t.Fatalf("%+v / %+v", r1, r2)
	}
	if m, _, _, _ := in.Working.Load(); len(m.Items) != 1 {
		t.Fatalf("%d drafts", len(m.Items))
	}
}
