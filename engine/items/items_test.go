package items

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
)

func working(t *testing.T) *store.Working {
	t.Helper()
	return store.Open(t.TempDir(), "Test Person", surfaces.MustDefault())
}

func load(t *testing.T, w *store.Working) (*manifest.Manifest, string) {
	t.Helper()
	m, rev, _, err := w.Load()
	if err != nil {
		t.Fatal(err)
	}
	return m, rev
}

func TestNewSetRemove(t *testing.T) {
	w := working(t)
	_, rev := load(t, w)
	it, rev, err := New(w, rev, []byte(`{"slug":"night-market","kind":"artwork","title":"Night Market"}`))
	if err != nil {
		t.Fatal(err)
	}
	if it.Visibility != manifest.ItemVisibilityDraft || len(it.ID) != 26 {
		t.Fatalf("%+v", it)
	}

	it, rev, err = Patch(w, rev, "night-market", []byte(`{"summary":"A study.","aliases":["nm"]}`))
	if err != nil || it.Summary == nil || *it.Summary != "A study." {
		t.Fatalf("%+v %v", it, err)
	}
	// Found by alias; null removes a field.
	it, rev, err = Patch(w, rev, "nm", []byte(`{"summary":null}`))
	if err != nil || it.Summary != nil {
		t.Fatalf("%+v %v", it, err)
	}

	m, _ := load(t, w)
	if got := List(m, Filter{Query: "MARKET"}); len(got) != 1 {
		t.Fatalf("query: %v", got)
	}
	if got := List(m, Filter{Kind: manifest.KindPhoto}); len(got) != 0 {
		t.Fatalf("kind filter: %v", got)
	}

	if _, err := Remove(w, rev, "night-market"); err != nil {
		t.Fatal(err)
	}
	if m, _ := load(t, w); len(m.Items) != 0 {
		t.Fatal("not removed")
	}
}

func TestInvalidChangesNothing(t *testing.T) {
	w := working(t)
	_, rev := load(t, w)
	_, rev, err := New(w, rev, []byte(`{"slug":"a","kind":"artwork","title":"A"}`))
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := w.Bytes()
	for _, patch := range []string{
		`{"slug":"Not A Slug"}`,   // schema
		`{"tags":["undeclared"]}`, // semantic: unknown taxonomy slug
		`{"kind":"spaceship"}`,
	} {
		_, _, err := Patch(w, rev, "a", []byte(patch))
		var inv *store.InvalidError
		if !errors.As(err, &inv) {
			t.Errorf("%s: want InvalidError, got %v", patch, err)
		}
	}
	for _, patch := range []string{`{"id":"01JB7Z0000000000000000000A"}`, `{"nonsense":1}`, `[1]`} {
		if _, _, err := Patch(w, rev, "a", []byte(patch)); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: want ErrBadRequest, got %v", patch, err)
		}
	}
	if _, _, err := New(w, rev, []byte(`{"slug":"b","kind":"artwork","title":"B","visibility":"public"}`)); !errors.Is(err, ErrBadRequest) {
		t.Errorf("new with visibility: %v", err)
	}
	after, _, _ := w.Bytes()
	if string(before) != string(after) {
		t.Fatal("a refused edit changed the working manifest")
	}
}

func TestStaleRevision(t *testing.T) {
	w := working(t)
	_, rev := load(t, w)
	if _, _, err := New(w, rev, []byte(`{"slug":"a","kind":"artwork","title":"A"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(w, rev, []byte(`{"slug":"b","kind":"artwork","title":"B"}`)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("want ErrStale, got %v", err)
	}
}

func TestRemovePublishedRefused(t *testing.T) {
	w := working(t)
	_, rev := load(t, w)
	it, _, err := New(w, rev, []byte(`{"slug":"a","kind":"artwork","title":"A"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Patch(w, "", "a", []byte(`{"visibility":"public"}`)); err != nil {
		t.Fatal(err)
	}
	b, _, _ := w.Bytes()
	if err := w.SetBase(b, 5); err != nil { // as if applied
		t.Fatal(err)
	}
	if _, err := Remove(w, "", string(it.ID)); !errors.Is(err, ErrPublished) {
		t.Fatalf("want ErrPublished, got %v", err)
	}
	if _, err := Remove(w, "", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSetTerm(t *testing.T) {
	w := working(t)
	tax, rev, err := SetTerm(w, "", "tags", "journaling", []byte(`{"slug":"journaling","label":"Journaling"}`))
	if err != nil || len(tax.Tags) != 1 {
		t.Fatalf("%+v %v", tax, err)
	}
	tax, _, err = SetTerm(w, rev, "tags", "journaling", []byte(`{"slug":"journaling","label":"Journals"}`))
	if err != nil || len(tax.Tags) != 1 || tax.Tags[0].Label != "Journals" {
		t.Fatalf("update: %+v %v", tax, err)
	}
	if _, _, err := SetTerm(w, "", "tags", "x", []byte(`{"slug":"y","label":"Y"}`)); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("slug mismatch: %v", err)
	}
	if _, _, err := SetTerm(w, "", "colors", "x", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "unknown vocabulary") {
		t.Fatalf("bad vocab: %v", err)
	}
}

func TestMergePatchRFC7396(t *testing.T) {
	// The examples from RFC 7396, appendix A (the object-valued ones).
	tests := []struct{ target, patch, want any }{
		{map[string]any{"a": "b"}, map[string]any{"a": "c"}, map[string]any{"a": "c"}},
		{map[string]any{"a": "b"}, map[string]any{"b": "c"}, map[string]any{"a": "b", "b": "c"}},
		{map[string]any{"a": "b"}, map[string]any{"a": nil}, map[string]any{}},
		{map[string]any{"a": "b", "b": "c"}, map[string]any{"a": nil}, map[string]any{"b": "c"}},
		{map[string]any{"a": []any{"b"}}, map[string]any{"a": "c"}, map[string]any{"a": "c"}},
		{map[string]any{"a": map[string]any{"b": "c"}}, map[string]any{"a": map[string]any{"b": "d", "c": nil}}, map[string]any{"a": map[string]any{"b": "d"}}},
		{map[string]any{"a": []any{map[string]any{"b": "c"}}}, map[string]any{"a": []any{1.0}}, map[string]any{"a": []any{1.0}}},
		{map[string]any{"e": nil}, map[string]any{"a": 1.0}, map[string]any{"e": nil, "a": 1.0}},
		{[]any{1.0, 2.0}, map[string]any{"a": "b", "c": nil}, map[string]any{"a": "b"}},
		{map[string]any{}, map[string]any{"a": map[string]any{"bb": map[string]any{"ccc": nil}}}, map[string]any{"a": map[string]any{"bb": map[string]any{}}}},
	}
	for i, tt := range tests {
		if got := MergePatch(tt.target, tt.patch); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%d: got %v, want %v", i, got, tt.want)
		}
	}
}
