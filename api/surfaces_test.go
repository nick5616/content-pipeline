package api

import (
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/internal/testenv"
)

func TestSurfacesAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	x.env.WithJobs()
	x.e.Derived, x.e.Jobs = x.env.Derived, x.env.Jobs
	if _, err := x.env.Runner().Run(t.Context(), jobsOpts()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.env.Working.Update(func(m *manifest.Manifest) error {
		m.Surfaces = []manifest.Surface{{ID: "art-portfolio", Label: "Art"}, {ID: "holodeck-dj-room", Label: "DJ room"}}
		m.Items[0].Placements = []manifest.Placement{{Surface: "art-portfolio", Config: manifest.PlacementConfig{"section": "primary"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var ss []SurfaceInfo
	x.call("GET", "/api/surfaces", "", nil, 200, &ss)
	if len(ss) != 2 || !ss[0].Registered || ss[0].Placements != 1 || ss[1].Placements != 0 {
		t.Fatalf("surfaces: %+v", ss)
	}
	// The item is a draft: the preview (what would publish) is empty.
	var v struct {
		View    string
		Entries []struct{ Slug string }
	}
	x.call("GET", "/api/surfaces/art-portfolio/preview", "", nil, 200, &v)
	if v.View != "art-portfolio" || len(v.Entries) != 0 {
		t.Fatalf("preview: %+v", v)
	}
	// --drafts (CLI) shows it.
	b, ps, err := x.e.Preview(t.Context(), "art-portfolio", true)
	if err != nil || len(ps) != 0 || !strings.Contains(string(b), `"slug": "png"`) {
		t.Fatalf("drafts preview: %v %v %s", err, ps, b)
	}
	x.call("GET", "/api/surfaces/nope/preview", "", nil, 404, nil)
}

func jobsOpts() jobs.Options { return jobs.Options{} }

func TestPutSurfaceAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	h := x.call("GET", "/api/items", "", nil, 200, nil)
	rev := h.Get("ETag")
	var ss []SurfaceInfo
	h = x.call("PUT", "/api/surfaces/holodeck-art-room", `{"id":"holodeck-art-room","label":"Art Room"}`, match(rev), 200, &ss)
	if len(ss) != 1 || ss[0].ID != "holodeck-art-room" || !ss[0].Registered {
		t.Fatalf("%+v", ss)
	}
	x.call("PUT", "/api/surfaces/someday", `{"id":"someday","label":"Nope"}`, match(h.Get("ETag")), 422, nil)
	x.call("PUT", "/api/surfaces/holodeck-dj-room", `{"id":"holodeck-dj-room","label":"DJ"}`, match(rev), 412, nil)
}

// Changes against the live view: nothing published yet, then publish, then
// edit a title.
func TestSurfaceChangesAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	x.env.WithPublic()
	x.e.Derived, x.e.Jobs = x.env.Derived, x.env.Jobs
	x.e.Config.PublicBucket = testenv.Public
	if _, err := x.env.Working.Update(func(m *manifest.Manifest) error {
		m.Surfaces = []manifest.Surface{{ID: "art-portfolio", Label: "Art"}}
		m.Items[0].Visibility = manifest.ItemVisibilityPublic
		m.Items[0].Placements = []manifest.Placement{{Surface: "art-portfolio", Config: manifest.PlacementConfig{"section": "primary"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.env.Runner().Run(t.Context(), jobs.Options{}); err != nil {
		t.Fatal(err)
	}
	var ch SurfaceChanges
	x.call("GET", "/api/surfaces/art-portfolio/changes", "", nil, 200, &ch)
	if ch.Live || len(ch.Added) != 1 {
		t.Fatalf("before publishing: %+v", ch)
	}
	x.env.Converge()
	x.call("GET", "/api/surfaces/art-portfolio/changes", "", nil, 200, &ch)
	if !ch.Live || len(ch.Added)+len(ch.Changed)+len(ch.Removed) != 0 || ch.Header {
		t.Fatalf("after publishing: %+v", ch)
	}
	if _, err := x.env.Working.Update(func(m *manifest.Manifest) error { m.Items[0].Title = "Renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	x.call("GET", "/api/surfaces/art-portfolio/changes", "", nil, 200, &ch)
	if len(ch.Changed) != 1 || ch.Changed[0].Title != "Renamed" || ch.Changed[0].Fields[0] != "title" {
		t.Fatalf("after an edit: %+v", ch)
	}
	x.call("GET", "/api/surfaces/nope/changes", "", nil, 404, nil)
}
