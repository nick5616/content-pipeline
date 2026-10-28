package surfaces

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestDefault(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.IDs()) != 7 {
		t.Fatalf("want 7 surfaces, got %v", r.IDs())
	}
	for _, id := range IDs {
		s, ok := r.Get(id)
		if !ok || s.Placement == nil || s.View == nil {
			t.Errorf("%s incomplete", id)
		}
	}
	if _, ok := r.Get("saucedog"); ok {
		t.Error("unregistered surface found")
	}
}

func golden(t *testing.T, id string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../schema/testdata/views", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return doc.(map[string]any)
}

// One golden view per surface validates against its view schema, and the
// common ways a view can be wrong are rejected.
func TestGoldenViews(t *testing.T) {
	r := MustDefault()
	for _, id := range IDs {
		t.Run(id, func(t *testing.T) {
			s, _ := r.Get(id)
			if err := s.View.Validate(golden(t, id)); err != nil {
				t.Fatalf("golden view invalid: %v", err)
			}
			mutations := map[string]func(v map[string]any){
				"wrong viewVersion": func(v map[string]any) { v["viewVersion"] = json.Number("2") },
				"wrong view":        func(v map[string]any) { v["view"] = "other" },
				"timestamp added":   func(v map[string]any) { v["generatedAt"] = "2026-10-27T00:00:00Z" },
				"missing taxonomy":  func(v map[string]any) { delete(v, "taxonomy") },
				"entry missing title": func(v map[string]any) {
					delete(v["entries"].([]any)[0].(map[string]any), "title")
				},
				"entry extra field": func(v map[string]any) {
					v["entries"].([]any)[0].(map[string]any)["private"] = map[string]any{"notes": "leak"}
				},
				"entry date as raw string": func(v map[string]any) {
					v["entries"].([]any)[0].(map[string]any)["date"] = "Sometime in like Oct 2025"
				},
			}
			for name, mutate := range mutations {
				v := golden(t, id)
				mutate(v)
				if err := s.View.Validate(v); err == nil {
					t.Errorf("%s: accepted", name)
				}
			}
		})
	}
}
