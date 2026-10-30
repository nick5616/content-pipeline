package apply_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/apply"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/internal/testenv"
)

// edit changes items by slug in the working manifest.
func edit(t *testing.T, e *testenv.Env, fn func(it *manifest.Item)) {
	t.Helper()
	if _, err := e.Working.Update(func(m *manifest.Manifest) error {
		if len(m.Surfaces) == 0 {
			m.Surfaces = []manifest.Surface{{ID: "art-portfolio", Label: "Art portfolio"}, {ID: "holodeck-dj-room", Label: "DJ room"}}
		}
		for i := range m.Items {
			fn(&m.Items[i])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func publishArt(it *manifest.Item) {
	switch it.Slug {
	case "png":
		it.Visibility = manifest.ItemVisibilityPublic
		it.Placements = []manifest.Placement{{Surface: "art-portfolio", Config: manifest.PlacementConfig{"section": "primary"}}}
	case "mp3":
		it.Visibility = manifest.ItemVisibilityPublic
		it.Placements = []manifest.Placement{{Surface: "holodeck-dj-room", Config: manifest.PlacementConfig{}}}
	}
}

func unpublishArt(it *manifest.Item) {
	if it.Slug == "png" {
		it.Visibility = manifest.ItemVisibilityDraft
	}
}

var urlRe = regexp.MustCompile(`https://storage\.googleapis\.com/` + testenv.Public + `/([^"]+)`)

// assertLinksExist: every URL in every public view is an object in the
// public bucket, and nothing outside blobs/, derived/, views/ and
// manifest.json is there.
func assertLinksExist(t *testing.T, mem *gcs.Mem) map[string]string {
	t.Helper()
	names := map[string]bool{}
	for _, n := range mem.Names(testenv.Public) {
		names[n] = true
	}
	views := map[string]string{}
	for n := range names {
		if !strings.HasPrefix(n, "views/") && n != "manifest.json" {
			continue
		}
		b, _, _, _ := mem.Get(testenv.Public, n)
		views[n] = string(b)
		for _, m := range urlRe.FindAllStringSubmatch(string(b), -1) {
			if !names[m[1]] {
				t.Errorf("%s links %s, which isn't in the public bucket", n, m[1])
			}
		}
	}
	return views
}

func TestPublishUnpublish(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png", "mp3.mp3").WithPublic()

	// Nothing public: seven empty views and an empty public manifest.
	e.Converge()
	views := assertLinksExist(t, mem)
	if len(views) != 8 || strings.Contains(views["manifest.json"], `"slug"`) {
		t.Fatalf("empty publish: %d objects, manifest %s", len(views), views["manifest.json"])
	}
	for _, n := range mem.Names(testenv.Public) {
		if strings.HasPrefix(n, "blobs/") || strings.HasPrefix(n, "derived/") {
			t.Errorf("published %s with nothing public", n)
		}
	}

	// Publish the artwork and the track.
	edit(t, e, publishArt)
	p := e.Plan()
	if p.Summary.Publish == 0 {
		t.Fatalf("nothing to publish: %+v", p.Ops)
	}
	e.Apply(p)
	e.AssertEmpty()
	views = assertLinksExist(t, mem)
	art, dj := views["views/art-portfolio.json"], views["views/holodeck-dj-room.json"]
	if !strings.Contains(art, `"slug": "png"`) || !strings.Contains(dj, `"slug": "mp3"`) {
		t.Fatalf("views:\nart %s\ndj %s", art, dj)
	}
	var blobs, webp int
	for _, n := range mem.Names(testenv.Public) {
		_, _, cc, _ := mem.Get(testenv.Public, n)
		switch {
		case strings.HasPrefix(n, "blobs/"):
			blobs++
			if !strings.HasSuffix(n, ".mp3") {
				t.Errorf("published a source that isn't the track's MP3 (D51): %s", n)
			}
		case strings.HasSuffix(n, ".webp"):
			webp++
			if strings.Contains(n, "lqip") {
				t.Errorf("published lqip.webp; it's inlined: %s", n)
			}
		}
		want := "public, max-age=86400"
		if strings.HasPrefix(n, "views/") || n == "manifest.json" {
			want = "public, max-age=60"
		}
		if cc != want {
			t.Errorf("%s: Cache-Control %q, want %q (D53)", n, cc, want)
		}
	}
	if blobs != 1 || webp == 0 {
		t.Errorf("published %d blobs, %d webp", blobs, webp)
	}
	if strings.Contains(views["manifest.json"], `"private"`) || strings.Contains(views["manifest.json"], `"original": "sha256:`) {
		t.Error("the public manifest has private fields")
	}

	// Unpublish the artwork: its files go, the views stop mentioning it.
	edit(t, e, unpublishArt)
	p = e.Plan()
	if p.Summary.Unpublish == 0 {
		t.Fatalf("nothing to unpublish: %+v", p.Ops)
	}
	e.Apply(p)
	e.AssertEmpty()
	views = assertLinksExist(t, mem)
	for n, v := range views {
		if strings.Contains(v, `"slug": "png"`) {
			t.Errorf("%s still mentions the unpublished artwork", n)
		}
	}
	for _, n := range mem.Names(testenv.Public) {
		if strings.HasSuffix(n, ".webp") {
			t.Errorf("%s is still public", n)
		}
	}
}

// A failure at any public write converges on rerun, and no view ever links
// a file that isn't there yet.
func TestPublishFailAtEveryWrite(t *testing.T) {
	for n := 1; ; n++ {
		mem := testenv.Mem()
		e := testenv.Seeded(t, mem, "png.png", "mp3.mp3").WithPublic()
		edit(t, e, publishArt)
		p := e.Plan()
		count, failed := 0, false
		mem.Fault = func(c gcs.Call) error {
			if c.Bucket == testenv.Public && (c.Method == "write" || c.Method == "copy" || c.Method == "delete") {
				count++
				if count == n {
					failed = true
					return errors.New("injected 503")
				}
			}
			return nil
		}
		_, err := apply.Apply(context.Background(), p, e.Deps(), nil)
		mem.Fault = nil
		if !failed {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if err == nil {
			t.Fatalf("n=%d: apply should fail", n)
		}
		assertLinksExist(t, mem)
		e.Converge()
		assertLinksExist(t, mem)
	}
}

func TestDraftFilesNeverPublic(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png", "mp3.mp3", "jpeg.jpg").WithPublic()
	edit(t, e, publishArt) // jpeg stays a draft
	e.Converge()
	var m struct {
		Items []struct{ Slug string }
	}
	b, _, _, _ := mem.Get(testenv.Public, "manifest.json")
	json.Unmarshal(b, &m)
	for _, it := range m.Items {
		if it.Slug == "jpeg" {
			t.Error("a draft is in the public manifest")
		}
	}
	// The draft's job outputs exist privately but not publicly.
	priv := map[string]bool{}
	for _, n := range mem.Names(testenv.Private) {
		priv[n] = true
	}
	pubN := 0
	for _, n := range mem.Names(testenv.Public) {
		if strings.HasPrefix(n, "derived/") || strings.HasPrefix(n, "blobs/") {
			pubN++
			if !priv[n] {
				t.Errorf("%s is public but not private: publish only copies", n)
			}
		}
	}
	if pubN == 0 || len(priv) <= pubN {
		t.Errorf("public %d, private %d", pubN, len(priv))
	}
}
