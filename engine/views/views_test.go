package views_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/publish"
	"github.com/nick5616/canon/engine/surfaces"
	. "github.com/nick5616/canon/engine/views"
)

var update = flag.Bool("update", false, "rewrite golden files")

const base = "https://storage.googleapis.com/canon-public-example"

// fakeMedia has finished outputs for every blob except those in missing.
type fakeMedia struct{ missing map[string]bool }

func short(blob string) string { return strings.TrimPrefix(blob, "sha256:")[:8] }

func (f fakeMedia) Image(blob string) (Image, bool) {
	if f.missing[blob] {
		return Image{}, false
	}
	k := "derived/" + short(blob) + "-thumb"
	return Image{Sizes: []Size{{k + "/w320.webp", 320, 240}, {k + "/w800.webp", 800, 600}}, LQIP: []byte("RIFFfake")}, true
}

func (f fakeMedia) Poster(blob string) (Image, bool) {
	if f.missing[blob] {
		return Image{}, false
	}
	k := "derived/" + short(blob) + "-poster"
	return Image{Sizes: []Size{{k + "/w320.webp", 320, 180}}}, true
}

func (f fakeMedia) Track(blob string) (Track, bool) {
	if f.missing[blob] {
		return Track{}, false
	}
	return Track{Audio: "blobs/sha256/" + short(blob) + ".mp3", Analysis: "derived/" + short(blob) + "-djgrid/analysis.json"}, true
}

func full(t *testing.T) *manifest.Manifest {
	t.Helper()
	b, err := os.ReadFile("../../schema/testdata/valid/full.json")
	if err != nil {
		t.Fatal(err)
	}
	m, ps, err := manifest.Load(bytes.NewReader(b))
	if err != nil || m == nil {
		t.Fatalf("%v %v", err, ps)
	}
	return publish.Project(m)
}

func resolve(t *testing.T, m *manifest.Manifest, surface string, media Media) (Result, []byte) {
	t.Helper()
	r, err := Resolve(m, surface, media, base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Bytes(surfaces.MustDefault())
	if err != nil {
		t.Fatal(err)
	}
	return r, b
}

// Every surface's view from the reference manifest: valid against its
// schema (Bytes checks), and equal to its golden file.
func TestGolden(t *testing.T) {
	m := full(t)
	if len(Surfaces()) != len(surfaces.IDs) {
		t.Fatalf("resolvers for %v, surfaces %v", Surfaces(), surfaces.IDs)
	}
	for _, s := range Surfaces() {
		_, b := resolve(t, m, s, fakeMedia{})
		path := filepath.Join("testdata", s+".json")
		if *update {
			os.WriteFile(path, b, 0o644)
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with -update)", s, err)
		}
		if !bytes.Equal(b, want) {
			t.Errorf("%s differs from its golden file (run with -update if intended):\n%s", s, b)
		}
		// Deterministic.
		if _, b2 := resolve(t, full(t), s, fakeMedia{}); !bytes.Equal(b, b2) {
			t.Errorf("%s isn't deterministic", s)
		}
	}
}

func TestNoDraftsInAnyView(t *testing.T) {
	m := full(t)
	for _, s := range Surfaces() {
		_, b := resolve(t, m, s, fakeMedia{})
		for _, draft := range []string{"left-hand-sketch", "fog-bridge", "on-journaling", "Old previews"} {
			if bytes.Contains(b, []byte(draft)) {
				t.Errorf("%s mentions %s", s, draft)
			}
		}
	}
}

func TestFilesAreWhatViewsLink(t *testing.T) {
	m := full(t)
	for _, s := range Surfaces() {
		r, b := resolve(t, m, s, fakeMedia{})
		for f := range r.Files {
			if !bytes.Contains(b, []byte(base+"/"+f)) {
				t.Errorf("%s: %s is listed but not linked", s, f)
			}
		}
		// lqip is inlined, never a file.
		for f := range r.Files {
			if strings.Contains(f, "lqip") {
				t.Errorf("%s publishes %s", s, f)
			}
		}
	}
}

func TestPendingImageLeavesPieceOut(t *testing.T) {
	m := full(t)
	var blob string
	for _, it := range m.Items {
		if it.Slug == "night-market-study" {
			blob = string(it.Assets[0].Blob)
		}
	}
	r, b := resolve(t, m, "holodeck-art-room", fakeMedia{missing: map[string]bool{blob: true}})
	if bytes.Contains(b, []byte("night-market-study")) {
		t.Error("a piece without image sizes is in the view")
	}
	if len(r.Problems) == 0 || r.Problems[0].Code != "view-asset-pending" {
		t.Errorf("problems: %v", r.Problems)
	}
}

func TestCopyPrecedence(t *testing.T) {
	m := full(t)
	_, b := resolve(t, m, "holodeck-showroom", fakeMedia{})
	if !bytes.Contains(b, []byte("RPG Quests ⚔️")) {
		t.Error("the showroom's copy override is missing")
	}
	_, b = resolve(t, m, "software-portfolio", fakeMedia{})
	if !bytes.Contains(b, []byte(`"title": "Tierlistify (mobile)"`)) || bytes.Contains(b, []byte("⚔️")) {
		t.Error("placement copy or surface copy leaked across surfaces")
	}
}

func TestResolveDate(t *testing.T) {
	l := "Sometime in like Nov 2024"
	for _, c := range []struct {
		v       string
		label   *string
		display string
		key     int
	}{
		{"2024", nil, "2024", 20240000},
		{"2024-12", nil, "Dec 2024", 20241200},
		{"2024-12-03", nil, "Dec 3, 2024", 20241203},
		{"2024-11", &l, l, 20241100},
	} {
		d := ResolveDate(c.v, c.label)
		if d.Display != c.display || d.SortKey != c.key {
			t.Errorf("%s: %+v", c.v, d)
		}
	}
}
