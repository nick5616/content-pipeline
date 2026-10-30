package importts

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/ingest"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/internal/testmedia"
)

// source extracts the fixture with Canon's own TypeScript (client/).
func source(t *testing.T) *Source {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	ts, _ := filepath.Abs("../../client")
	if _, err := os.Stat(filepath.Join(ts, "node_modules", "typescript")); err != nil {
		t.Skip("client/node_modules has no typescript (task client:install)")
	}
	src, err := Extract(context.Background(), "testdata/data/content.ts", ts)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func importer(t *testing.T) *Importer {
	t.Helper()
	blobs, err := blobstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := store.Open(t.TempDir(), "Test Person", surfaces.MustDefault())
	png, _ := os.ReadFile(filepath.Join(testmedia.Dir(t), "png.png"))
	gen := ids.NewWith(func() time.Time { return time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC) }, rand.New(rand.NewSource(1)))
	// A paintings collection, as canon import bucket would have made it.
	if _, err := w.Update(func(m *manifest.Manifest) error {
		m.Items = append(m.Items, manifest.Item{ID: manifest.Ulid(gen.New().String()), Slug: "paintings",
			Kind: manifest.KindCollection, Visibility: manifest.ItemVisibilityPublic, Title: "Paintings"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &Importer{Working: w, Ingest: ingest.New(blobs, w, 64<<20), IDs: gen,
		Fetch: func(_ context.Context, u string) (io.ReadCloser, error) {
			if u != "https://nickeltools.dev/project-screenshots/world.png" {
				t.Errorf("fetched %s", u)
			}
			return io.NopCloser(bytes.NewReader(png)), nil
		}}
}

func bySlug(t *testing.T, w *store.Working) map[string]manifest.Item {
	m, _, _, err := w.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]manifest.Item{}
	for _, it := range m.Items {
		out[string(it.Slug)] = it
	}
	return out
}

func placement(it manifest.Item, surface string) *manifest.Placement {
	for i := range it.Placements {
		if string(it.Placements[i].Surface) == surface {
			return &it.Placements[i]
		}
	}
	return nil
}

func TestExtract(t *testing.T) {
	src := source(t)
	if len(src.Content) != 8 || src.Featured[0] != "toy" || src.Icons["Games"] != "🎮" || src.Profile.Name != "Test Person" {
		t.Fatalf("%+v", src)
	}
	var w, q Entry
	for _, e := range src.Content {
		switch e.ID {
		case "world":
			w = e
		case "videogamequest":
			q = e
		}
	}
	if w.Section != "Section header" || len(w.Comments) != 0 {
		t.Errorf("section header: %q, comments %v", w.Section, w.Comments)
	}
	if len(q.Comments) != 1 || !strings.HasPrefix(q.Comments[0], "TODO") {
		t.Errorf("comments: %v", q.Comments)
	}
	if string(w.Overrides["software"]["title"]) != `"World (software)"` {
		t.Errorf("overrides: %v", w.Overrides)
	}
}

func TestImport(t *testing.T) {
	src := source(t)
	im := importer(t)
	rep, err := im.Import(context.Background(), src, "content.ts")
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]Status{}
	for _, r := range rep.Results {
		status[r.Entry] = r.Status
	}
	if status["about"] != Skipped || status["art-paintings"] != Merged || status["world"] != Imported {
		t.Fatalf("statuses: %v", status)
	}
	items := bySlug(t, im.Working)

	world := items["world"]
	if world.Kind != manifest.KindProject || world.Visibility != manifest.ItemVisibilityDraft || len(world.Assets) != 1 ||
		world.Assets[0].Role != manifest.AssetRoleThumbnail || world.Assets[0].Original == nil {
		t.Errorf("world: %+v", world)
	}
	if strings.Join(slugStrs(world.Tech), ",") != "typescript,csharp" {
		t.Errorf("tech: %v", world.Tech)
	}
	if c := world.Copy["software-portfolio"]; c.Title == nil || *c.Title != "World (software)" || *c.Summary != "Software words." {
		t.Errorf("copy: %+v", world.Copy)
	}
	if p := placement(world, "software-portfolio"); p == nil || p.Config["preview"].(map[string]any)["kind"] != "laser-room" {
		t.Errorf("world preview: %+v", p)
	}
	if placement(world, "holodeck-showroom") == nil || placement(world, "design-portfolio") == nil {
		t.Error("world: software entries go to the showroom too")
	}
	if len(world.Links) != 2 || world.Links[1].Rel != "source" {
		t.Errorf("links: %+v", world.Links)
	}

	if items["art-room"].Kind != manifest.KindExperience || items["art-room"].Facts["site"] != "holodeck-art-room" {
		t.Errorf("art-room: %+v", items["art-room"])
	}

	q := items["rpg-quests"]
	if len(q.Aliases) != 1 || q.Aliases[0] != "videogamequest" || q.Date.Value != "2024-11" || *q.Date.Label != "Sometime in like Nov 2024" {
		t.Errorf("rpg-quests: %+v", q)
	}
	pv := placement(q, "software-portfolio").Config["preview"].(map[string]any)
	if pv["kind"] != "phones" || pv["urls"].([]any)[1] != "https://rpg-quests.netlify.app/demo" {
		t.Errorf("rewritten preview: %v", pv)
	}
	if !strings.Contains(*q.Private.Notes, "TODO: the link") {
		t.Errorf("notes: %v", *q.Private.Notes)
	}

	toy := placement(items["toy"], "nickel-desktop")
	if toy.Config["route"] != "/toy" || toy.Config["open"] != "in-window" || toy.Config["pinned"] != true ||
		toy.Config["component"] != "batch-analyzer" {
		t.Errorf("toy desktop: %v", toy.Config)
	}
	// Ranked after big (D59 style), though content.ts lists it first.
	big := items["big"]
	if *placement(items["toy"], "software-portfolio").Order <= *placement(big, "software-portfolio").Order {
		t.Error("toy should rank after big")
	}
	if big.Visibility != manifest.ItemVisibilityDraft || !strings.Contains(*big.Private.Notes, "hide: true") ||
		placement(big, "nickel-desktop").Config["route"] != nil {
		t.Errorf("big: %+v", big)
	}
	if *big.Category != "social" || *items["toy"].Category != "gaming" || *items["ext"].Category != "social" {
		t.Error("categories didn't merge")
	}
	if placement(items["ext"], "software-portfolio").Config["preview"].(map[string]any)["kind"] != "none" {
		t.Error("an unowned page was framed")
	}

	p := items["paintings"]
	if len(p.Aliases) != 1 || *p.Summary != "Paintings, for the art page." || placement(p, "art-portfolio").Config["section"] != "collections" ||
		placement(p, "nickel-desktop").Config["route"] != "/art-gallery/paintings" {
		t.Errorf("paintings: %+v", p)
	}

	m, rev, _, _ := im.Working.Load()
	if m.Profile.Headline == nil || *m.Profile.Headline != "Engineer" || len(m.Surfaces) < 5 {
		t.Errorf("profile/surfaces: %+v %v", m.Profile, m.Surfaces)
	}
	var cats []string
	for _, c := range m.Taxonomy.Categories {
		cats = append(cats, string(c.Slug))
	}
	if strings.Contains(strings.Join(cats, ","), "games,") || !strings.Contains(strings.Join(cats, ","), "social") {
		t.Errorf("categories: %v", cats)
	}

	// Rerun: nothing changes.
	rep, err = im.Import(context.Background(), src, "content.ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Status == Imported || r.Status == Merged {
			t.Errorf("rerun %s: %s", r.Entry, r.Status)
		}
	}
	if _, rev2, _, _ := im.Working.Load(); rev2 != rev {
		t.Error("a rerun changed the manifest")
	}
}

func slugStrs(ss []manifest.Slug) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return out
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"AI / Productivity": "ai-productivity", "C#": "csharp", "Next.js": "next-js",
		"TypeScript": "typescript", "Google Cloud Platform": "google-cloud-platform"} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
	if componentSlug("BatchAnalyzer") != "batch-analyzer" {
		t.Error(componentSlug("BatchAnalyzer"))
	}
}
