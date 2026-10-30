// Package items edits the working manifest one item or taxonomy term at a
// time (D36). Both the CLI and the API go through here, and every change
// goes through store.UpdateIf, so it validates before anything is written.
package items

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
)

var (
	// ErrNotFound means no item has that id, slug or alias.
	ErrNotFound = errors.New("no such item")
	// ErrPublished means the item is public in the remote manifest, so it
	// can't be removed; unpublish it instead (visibility draft).
	ErrPublished = errors.New("this item is public in the remote manifest; set visibility to draft instead of removing it")
	// ErrBadRequest is a malformed edit (as opposed to an invalid result).
	ErrBadRequest = errors.New("bad request")
)

// Filter selects items for List. Zero fields match everything.
type Filter struct {
	Kind       manifest.Kind
	Surface    string
	Visibility manifest.ItemVisibility
	Tag        string
	Query      string // case-insensitive, on title, slug and aliases
}

// List returns the matching items, sorted by id (the manifest's order).
func List(m *manifest.Manifest, f Filter) []manifest.Item {
	out := []manifest.Item{}
	q := strings.ToLower(f.Query)
	for _, it := range m.Items {
		if f.Kind != "" && it.Kind != f.Kind ||
			f.Visibility != "" && it.Visibility != f.Visibility ||
			f.Tag != "" && !slices.Contains(it.Tags, manifest.Slug(f.Tag)) {
			continue
		}
		if f.Surface != "" && !slices.ContainsFunc(it.Placements, func(p manifest.Placement) bool { return string(p.Surface) == f.Surface }) {
			continue
		}
		if q != "" && !matches(it, q) {
			continue
		}
		out = append(out, it)
	}
	return out
}

func matches(it manifest.Item, q string) bool {
	if strings.Contains(strings.ToLower(it.Title), q) || strings.Contains(string(it.Slug), q) {
		return true
	}
	return slices.ContainsFunc(it.Aliases, func(a manifest.Slug) bool { return strings.Contains(string(a), q) })
}

// Find returns the index of the item with this id, slug or alias.
func Find(m *manifest.Manifest, ref string) (int, error) {
	for i, it := range m.Items {
		if string(it.ID) == ref || string(it.Slug) == ref || slices.Contains(it.Aliases, manifest.Slug(ref)) {
			return i, nil
		}
	}
	return -1, fmt.Errorf("%w: %s", ErrNotFound, ref)
}

// New adds a draft from body, a JSON item without id or visibility (the
// server sets both). rev is the If-Match revision ("" skips the check).
func New(w *store.Working, rev string, body []byte) (manifest.Item, string, error) {
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return manifest.Item{}, "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	for _, k := range []string{"id", "visibility"} {
		if _, ok := fields[k]; ok {
			return manifest.Item{}, "", fmt.Errorf("%w: %s is set by Canon", ErrBadRequest, k)
		}
	}
	fields["id"] = ids.New().String()
	fields["visibility"] = string(manifest.ItemVisibilityDraft)
	it, err := decodeItem(fields)
	if err != nil {
		return manifest.Item{}, "", err
	}
	newRev, err := w.UpdateIf(rev, func(m *manifest.Manifest) error {
		m.Items = append(m.Items, it)
		return nil
	})
	return it, newRev, err
}

// Patch applies a JSON Merge Patch (RFC 7396) to one item. id can't change.
func Patch(w *store.Working, rev, ref string, patch []byte) (manifest.Item, string, error) {
	var p any
	if err := json.Unmarshal(patch, &p); err != nil {
		return manifest.Item{}, "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	pm, ok := p.(map[string]any)
	if !ok {
		return manifest.Item{}, "", fmt.Errorf("%w: a merge patch for an item must be an object", ErrBadRequest)
	}
	if _, ok := pm["id"]; ok {
		return manifest.Item{}, "", fmt.Errorf("%w: id never changes", ErrBadRequest)
	}
	var out manifest.Item
	newRev, err := w.UpdateIf(rev, func(m *manifest.Manifest) error {
		i, err := Find(m, ref)
		if err != nil {
			return err
		}
		cur, err := toMap(m.Items[i])
		if err != nil {
			return err
		}
		it, err := decodeItem(MergePatch(cur, pm).(map[string]any))
		if err != nil {
			return err
		}
		m.Items[i], out = it, it
		return nil
	})
	return out, newRev, err
}

// Remove deletes a draft. An item that's public in base (the remote
// manifest) is refused with ErrPublished.
func Remove(w *store.Working, rev, ref string) (string, error) {
	base, _, err := w.Base()
	if err != nil {
		return "", err
	}
	var published map[manifest.Ulid]bool
	if base != nil {
		bm, _, err := manifest.Load(bytes.NewReader(base))
		if err == nil && bm != nil {
			published = map[manifest.Ulid]bool{}
			for _, it := range bm.Items {
				published[it.ID] = it.Visibility == manifest.ItemVisibilityPublic
			}
		}
	}
	return w.UpdateIf(rev, func(m *manifest.Manifest) error {
		i, err := Find(m, ref)
		if err != nil {
			return err
		}
		if published[m.Items[i].ID] {
			return ErrPublished
		}
		m.Items = slices.Delete(m.Items, i, i+1)
		return nil
	})
}

// Vocabs are the taxonomy vocabularies.
var Vocabs = []string{"categories", "tech", "mediums", "tags", "viewports"}

// SetTerm creates or replaces one taxonomy term. body is a term (a
// viewport for vocab viewports) whose slug must match.
func SetTerm(w *store.Working, rev, vocab, slug string, body []byte) (manifest.Taxonomy, string, error) {
	if !slices.Contains(Vocabs, vocab) {
		return manifest.Taxonomy{}, "", fmt.Errorf("%w: unknown vocabulary %q (want one of %s)", ErrBadRequest, vocab, strings.Join(Vocabs, ", "))
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var term manifest.TaxonomyTerm
	var vp manifest.Viewport
	var err error
	if vocab == "viewports" {
		err = dec.Decode(&vp)
	} else {
		err = dec.Decode(&term)
	}
	if err != nil {
		return manifest.Taxonomy{}, "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if got := string(term.Slug) + string(vp.Slug); got != slug {
		return manifest.Taxonomy{}, "", fmt.Errorf("%w: body slug %q doesn't match %q", ErrBadRequest, got, slug)
	}
	var out manifest.Taxonomy
	newRev, err := w.UpdateIf(rev, func(m *manifest.Manifest) error {
		t := &m.Taxonomy
		switch vocab {
		case "viewports":
			t.Viewports = upsert(t.Viewports, vp, func(v manifest.Viewport) bool { return string(v.Slug) == slug })
		case "categories":
			t.Categories = upsert(t.Categories, term, bySlug(slug))
		case "tech":
			t.Tech = upsert(t.Tech, term, bySlug(slug))
		case "mediums":
			t.Mediums = upsert(t.Mediums, term, bySlug(slug))
		case "tags":
			t.Tags = upsert(t.Tags, term, bySlug(slug))
		}
		out = *t
		return nil
	})
	return out, newRev, err
}

func bySlug(slug string) func(manifest.TaxonomyTerm) bool {
	return func(t manifest.TaxonomyTerm) bool { return string(t.Slug) == slug }
}

func upsert[T any](s []T, v T, match func(T) bool) []T {
	if i := slices.IndexFunc(s, match); i >= 0 {
		s[i] = v
		return s
	}
	return append(s, v)
}

// MergePatch applies an RFC 7396 merge patch to target.
func MergePatch(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	out := map[string]any{}
	for k, v := range tm {
		out[k] = v
	}
	for k, v := range pm {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = MergePatch(out[k], v)
	}
	return out
}

func toMap(it manifest.Item) (map[string]any, error) {
	b, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

// decodeItem turns fields into an Item, refusing unknown fields. The
// whole-manifest validation in UpdateIf then decides whether it's valid.
func decodeItem(fields map[string]any) (manifest.Item, error) {
	b, err := json.Marshal(fields)
	if err != nil {
		return manifest.Item{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var it manifest.Item
	if err := dec.Decode(&it); err != nil {
		return manifest.Item{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	return it, nil
}

// SetSurface declares or updates a surface in the manifest's surfaces
// table: body is {id, label, baseUrl?}. Placements can only name declared
// surfaces, and only registered ones validate.
func SetSurface(w *store.Working, rev, id string, body []byte) ([]manifest.Surface, string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var s manifest.Surface
	if err := dec.Decode(&s); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if string(s.ID) != id {
		return nil, "", fmt.Errorf("%w: body id %q doesn't match %q", ErrBadRequest, s.ID, id)
	}
	var out []manifest.Surface
	newRev, err := w.UpdateIf(rev, func(m *manifest.Manifest) error {
		m.Surfaces = upsert(m.Surfaces, s, func(x manifest.Surface) bool { return string(x.ID) == id })
		out = m.Surfaces
		return nil
	})
	return out, newRev, err
}
