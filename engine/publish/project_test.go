package publish

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/manifest"
)

func load(t *testing.T, path string) *manifest.Manifest {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, ps, err := manifest.Load(bytes.NewReader(b))
	if err != nil || m == nil {
		t.Fatalf("%s: %v %v", path, err, ps)
	}
	return m
}

func TestEveryItemFieldIsDecided(t *testing.T) {
	ty := reflect.TypeOf(manifest.Item{})
	for i := 0; i < ty.NumField(); i++ {
		name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
		if _, ok := projected[name]; !ok {
			t.Errorf("Item.%s (%q) is new: decide whether the public projection copies it", ty.Field(i).Name, name)
		}
	}
}

func TestProjectFull(t *testing.T) {
	m := load(t, "../../schema/testdata/valid/full.json")
	pm := Project(m)
	b, err := Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"left-hand-sketch", "fog-bridge", "on-journaling", `"private"`, `"original": "sha256:`, "videogamequest.me"} {
		if strings.Contains(s, bad) {
			t.Errorf("public manifest contains %q", bad)
		}
	}
	// The public collection keeps only its public members (D25).
	for _, it := range pm.Items {
		if it.Slug == "sketches" {
			for _, id := range it.Members {
				if id == "01JB7Z00000000000000000004" {
					t.Error("draft member kept")
				}
			}
		}
	}
	b2, _ := Marshal(Project(m))
	if !bytes.Equal(b, b2) {
		t.Error("projection isn't deterministic")
	}
}

// A property test: whatever is draft or private in a random manifest never
// reaches the public bytes.
func TestProjectRandom(t *testing.T) {
	base := load(t, "../../schema/testdata/valid/full.json")
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 200; round++ {
		raw, _ := manifest.Marshal(base)
		var m manifest.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		secret := map[string]bool{}
		for i := range m.Items {
			it := &m.Items[i]
			if rng.Intn(2) == 0 {
				it.Visibility = manifest.ItemVisibilityDraft
			} else {
				it.Visibility = manifest.ItemVisibilityPublic
			}
			note := "SECRET-" + string(it.ID)
			it.Private = &manifest.Private{Notes: &note}
			if it.Visibility == manifest.ItemVisibilityDraft {
				secret[string(it.ID)] = true
			}
		}
		b, err := Marshal(Project(&m))
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		s := string(b)
		if strings.Contains(s, "SECRET-") || strings.Contains(s, `"original": "sha256:`) {
			t.Fatalf("round %d: private data published", round)
		}
		for id := range secret {
			if strings.Contains(s, id) {
				t.Fatalf("round %d: draft %s appears in the public manifest", round, id)
			}
		}
	}
}

func TestFixtures(t *testing.T) {
	// schema/testdata/public: valid.json passes; each other one is a leak
	// the schema catches.
	for _, f := range []string{"with-draft", "with-original", "with-private"} {
		b, _ := os.ReadFile("../../schema/testdata/public/" + f + ".json")
		var doc any
		json.Unmarshal(b, &doc)
		if ps := manifest.SchemaProblems("public-manifest.schema.json", doc); len(ps) == 0 {
			t.Errorf("%s passes the public schema", f)
		}
	}
}
