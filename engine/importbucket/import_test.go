package importbucket

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/ingest"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/internal/testmedia"
)

const src = "old-art"

const crateJSON = `{"artist": "Bird Dog", "tracks": [{"id": "evil-beat", "title": "Evil Beat", "artist": "Bird Dog",
  "audio": "evil-beat.mp3", "analysis": "evil-beat.json", "duration": 80, "bpm": 120, "firstBeat": 0,
  "start": 0.5, "key": "D", "camelot": "10B"}]}`

type env struct {
	im    *Importer
	mem   *gcs.Mem
	cache string
}

func newEnv(t *testing.T) env {
	t.Helper()
	cache := t.TempDir()
	blobs, err := blobstore.Open(cache)
	if err != nil {
		t.Fatal(err)
	}
	w := store.Open(t.TempDir(), "Nicole Belovoskey", surfaces.MustDefault())
	gen := ids.NewWith(func() time.Time { return time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC) }, rand.New(rand.NewSource(1)))
	in := ingest.New(blobs, w, 64<<20)
	in.IDs = gen
	mem := gcs.NewMem(src)
	return env{&Importer{GCS: mem, Ingest: in, Working: w, IDs: gen}, mem, cache}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testmedia.Dir(t), name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// seed lays out a bucket like gs://artandmedia: folders, folder markers,
// both metadata schemes, a private piece, one piece in two folders, and the
// DJ crate with its analysis files.
func seed(t *testing.T, mem *gcs.Mem) {
	jpeg, png, gif := fixture(t, "jpeg.jpg"), fixture(t, "png.png"), fixture(t, "gif.gif")
	mem.PutMeta(src, "digitalart/", nil, nil)
	mem.PutMeta(src, "digitalart/chaos-bird.png", png, map[string]string{
		"art-name": "Chaos Bird", "art-description": "An abstract representation of chaos in avian form",
		"art-date": "Circa 2022", "art-order": "2"})
	mem.PutMeta(src, "digitalart/marvin.gif", gif, map[string]string{
		"art-name": "Marvin Martian", "art-date": "Circa December 2024", "art-order": "0"})
	mem.PutMeta(src, "sketches/IMG_5128 Copy.JPG", jpeg, map[string]string{"private": "true"})
	mem.PutMeta(src, "lefthanded/IMG_5128 Copy.JPG", jpeg, map[string]string{"created": "10/5/2025"})
	mem.PutMeta(src, "paintings/mackbook.png", fixture(t, "webp.webp"), map[string]string{
		"art-name": "Mackbook", "art-date": "3", "art-order": "x"})
	mem.PutMeta(src, "dj-crate/crate.json", []byte(crateJSON), nil)
	mem.PutMeta(src, "dj-crate/evil-beat.json", []byte(`{"fps":100}`), nil)
	mem.PutMeta(src, "dj-crate/evil-beat.mp3", fixture(t, "mp3.mp3"), nil)
}

func byObject(rep Report) map[string]Result {
	out := map[string]Result{}
	for _, r := range rep.Results {
		out[r.Object] = r
	}
	return out
}

func item(t *testing.T, w *store.Working, slug string) manifest.Item {
	t.Helper()
	m, _, _, err := w.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range m.Items {
		if string(it.Slug) == slug {
			return it
		}
	}
	t.Fatalf("no item %q", slug)
	return manifest.Item{}
}

func TestImport(t *testing.T) {
	e := newEnv(t)
	seed(t, e.mem)
	before := map[string][]byte{}
	for _, n := range e.mem.Names(src) {
		before[n], _, _, _ = e.mem.Get(src, n)
	}

	rep, err := e.im.Import(context.Background(), src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() != 0 {
		t.Fatalf("failures: %+v", rep.Results)
	}
	res := byObject(rep)
	for obj, want := range map[string]Status{
		"digitalart/": Skipped, "dj-crate/crate.json": Skipped, "dj-crate/evil-beat.json": Skipped,
		"digitalart/chaos-bird.png": Imported, "digitalart/marvin.gif": Imported,
		// lefthanded/ lists first, so its copy makes the item.
		"lefthanded/IMG_5128 Copy.JPG": Imported, "sketches/IMG_5128 Copy.JPG": Existing,
		"paintings/mackbook.png": Imported, "dj-crate/evil-beat.mp3": Imported,
	} {
		if got := res[obj].Status; got != want {
			t.Errorf("%s: %s, want %s (%+v)", obj, got, want, res[obj])
		}
	}

	// The source bucket was only read.
	for _, c := range e.mem.Calls {
		if c.Method != "list" && c.Method != "read" {
			t.Errorf("source bucket got a %s call", c.Method)
		}
	}
	for n, b := range before {
		if got, _, _, _ := e.mem.Get(src, n); string(got) != string(b) {
			t.Errorf("%s changed", n)
		}
	}

	// Metadata lands on the drafts.
	cb := item(t, e.im.Working, res["digitalart/chaos-bird.png"].Slug)
	if cb.Title != "Chaos Bird" || cb.Summary == nil || !strings.HasPrefix(*cb.Summary, "An abstract") ||
		cb.Kind != manifest.KindArtwork || cb.Visibility != manifest.ItemVisibilityDraft {
		t.Errorf("chaos bird: %+v", cb)
	}
	if cb.Date == nil || cb.Date.Value != "2022" || cb.Date.Label == nil || *cb.Date.Label != "Circa 2022" {
		t.Errorf("chaos bird date: %+v", cb.Date)
	}
	if cb.Private == nil || !strings.Contains(*cb.Private.Notes, "gs://old-art/digitalart/chaos-bird.png") {
		t.Errorf("chaos bird notes: %+v", cb.Private)
	}

	// The camera JPEG is an artwork, dated by the copy that made it, and
	// private because the other copy says so. It is in both folders'
	// collections.
	sk := item(t, e.im.Working, res["sketches/IMG_5128 Copy.JPG"].Slug)
	if sk.Kind != manifest.KindArtwork || !strings.Contains(*sk.Private.Notes, "keep it unpublished") {
		t.Errorf("sketch: kind %s, notes %v", sk.Kind, deref(sk.Private.Notes))
	}
	if sk.Date == nil || sk.Date.Value != "2025-10-05" {
		t.Errorf("sketch date: %+v", sk.Date)
	}
	if res["lefthanded/IMG_5128 Copy.JPG"].Item != res["sketches/IMG_5128 Copy.JPG"].Item {
		t.Error("the same file in two folders made two items")
	}

	// Bad metadata is flagged, not fatal.
	mb := res["paintings/mackbook.png"]
	if !slices.ContainsFunc(mb.Warnings, func(w string) bool { return strings.Contains(w, `art-date "3"`) }) ||
		!slices.ContainsFunc(mb.Warnings, func(w string) bool { return strings.Contains(w, `art-order "x"`) }) {
		t.Errorf("mackbook warnings: %v", mb.Warnings)
	}

	// The track gets crate.json's title, artist and grid.
	tr := item(t, e.im.Working, res["dj-crate/evil-beat.mp3"].Slug)
	if tr.Kind != manifest.KindTrack || tr.Title != "Evil Beat" || len(tr.Credits) != 1 || tr.Credits[0].Name != "Bird Dog" ||
		tr.Facts["bpm"] != 120.0 || tr.Facts["camelot"] != "10B" || tr.Facts["start"] != 0.5 {
		t.Errorf("track: %+v", tr)
	}

	// Collections: drafts, in art-order, dj-crate as music (D15).
	da := item(t, e.im.Working, "digitalart")
	want := []manifest.Ulid{manifest.Ulid(res["digitalart/marvin.gif"].Item), manifest.Ulid(res["digitalart/chaos-bird.png"].Item)}
	if da.Kind != manifest.KindCollection || da.Title != "Digital Art" || !slices.Equal(da.Members, want) ||
		da.Visibility != manifest.ItemVisibilityDraft {
		t.Errorf("digitalart: %+v, want members %v", da, want)
	}
	if m := item(t, e.im.Working, "music"); len(m.Members) != 1 || m.Title != "Music" {
		t.Errorf("music: %+v", m)
	}
	for _, slug := range []string{"sketches", "lefthanded"} {
		if c := item(t, e.im.Working, slug); !slices.Equal(c.Members, []manifest.Ulid{manifest.Ulid(res["sketches/IMG_5128 Copy.JPG"].Item)}) {
			t.Errorf("%s members: %v", slug, c.Members)
		}
	}
	if res["dj-crate/evil-beat.mp3"].Collection != "music" {
		t.Errorf("track collection: %q", res["dj-crate/evil-beat.mp3"].Collection)
	}

	// Originals are kept for the archive, one per distinct file.
	kept, _ := filepath.Glob(filepath.Join(e.cache, "originals", "sha256", "*", "*", "*"))
	if len(kept) != 5 {
		t.Errorf("kept %d originals, want 5", len(kept))
	}
}

func TestImportRerunKeepsEdits(t *testing.T) {
	e := newEnv(t)
	seed(t, e.mem)
	ctx := context.Background()
	rep, err := e.im.Import(ctx, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	slug := byObject(rep)["digitalart/chaos-bird.png"].Slug
	if _, err := e.im.Working.Update(func(m *manifest.Manifest) error {
		for i := range m.Items {
			if string(m.Items[i].Slug) == slug {
				m.Items[i].Title = "Chaos Bird (edited)"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, rev1, _, _ := e.im.Working.Load()

	rep, err = e.im.Import(ctx, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Status != Existing && r.Status != Skipped {
			t.Errorf("rerun: %s is %s", r.Object, r.Status)
		}
	}
	if _, rev2, _, _ := e.im.Working.Load(); rev2 != rev1 {
		t.Error("a rerun changed the manifest")
	}
	if it := item(t, e.im.Working, slug); it.Title != "Chaos Bird (edited)" {
		t.Errorf("edit lost: %q", it.Title)
	}
}

func TestImportDryRunStoresNothing(t *testing.T) {
	e := newEnv(t)
	seed(t, e.mem)
	rep, err := e.im.Import(context.Background(), src, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Status != Checked && r.Status != Skipped {
			t.Errorf("%s: %s %s", r.Object, r.Status, r.Error)
		}
	}
	if m, _, _, _ := e.im.Working.Load(); len(m.Items) != 0 {
		t.Errorf("dry run drafted %d items", len(m.Items))
	}
}

func TestImportPrefixAndFailures(t *testing.T) {
	e := newEnv(t)
	seed(t, e.mem)
	e.mem.PutMeta(src, "digitalart/notes.txt", []byte("just text"), nil)
	rep, err := e.im.Import(context.Background(), src, Options{Prefix: "digitalart/"})
	if err != nil {
		t.Fatal(err)
	}
	res := byObject(rep)
	if len(res) != 4 {
		t.Errorf("prefix: got %d results, want 4", len(res))
	}
	if r := res["digitalart/notes.txt"]; r.Status != Failed || r.Error == "" {
		t.Errorf("text file: %+v", r)
	}
	if rep.Failed() != 1 {
		t.Errorf("failed = %d", rep.Failed())
	}
	// One bad file doesn't stop the rest or the collection.
	if c := item(t, e.im.Working, "digitalart"); len(c.Members) != 2 {
		t.Errorf("digitalart members: %v", c.Members)
	}
}
