// Package publish computes what the public bucket should hold: the public
// projection of the manifest, and the files views link to. Both are pure;
// plan and apply do the I/O.
package publish

import (
	"encoding/json"
	"fmt"

	"github.com/nick5616/canon/engine/manifest"
)

// PublicManifestPath is the public manifest's object name.
const PublicManifestPath = "manifest.json"

// ViewPath is a surface's view object name.
func ViewPath(surface string) string { return "views/" + surface + ".json" }

// Project returns the public projection: an allowlist, field by field.
// Drafts go; so do private blocks, originals' hashes (D28) and members that
// aren't public (D25). Everything else about a public item is public by
// design (it's what the sites show).
func Project(m *manifest.Manifest) *manifest.Manifest {
	public := map[manifest.Ulid]bool{}
	for _, it := range m.Items {
		if it.Visibility == manifest.ItemVisibilityPublic {
			public[it.ID] = true
		}
	}
	out := &manifest.Manifest{
		SchemaVersion: m.SchemaVersion,
		Profile:       m.Profile,
		OwnedDomains:  m.OwnedDomains,
		Surfaces:      m.Surfaces,
		Taxonomy:      m.Taxonomy,
		Items:         []manifest.Item{},
	}
	for _, it := range m.Items {
		if !public[it.ID] {
			continue
		}
		out.Items = append(out.Items, projectItem(it, public))
	}
	return out
}

// projectItem copies the allowed fields. TestEveryItemFieldIsDecided fails
// when Item gains a field this doesn't mention.
func projectItem(it manifest.Item, public map[manifest.Ulid]bool) manifest.Item {
	p := manifest.Item{
		ID:              it.ID,
		Slug:            it.Slug,
		Aliases:         it.Aliases,
		Kind:            it.Kind,
		Visibility:      it.Visibility,
		Title:           it.Title,
		Summary:         it.Summary,
		Why:             it.Why,
		Date:            it.Date,
		Status:          it.Status,
		Category:        it.Category,
		Tech:            it.Tech,
		Tags:            it.Tags,
		Mediums:         it.Mediums,
		Credits:         it.Credits,
		Facts:           it.Facts,
		Links:           it.Links,
		Copy:            it.Copy,
		Placements:      it.Placements,
		RichDescription: it.RichDescription,
		// Private: never.
	}
	for _, a := range it.Assets {
		a.Original = nil
		p.Assets = append(p.Assets, a)
	}
	for _, id := range it.Members {
		if public[id] {
			p.Members = append(p.Members, id)
		}
	}
	return p
}

// projected lists Item's fields by JSON name: copied, or deliberately not.
var projected = map[string]bool{
	"id": true, "slug": true, "aliases": true, "kind": true, "visibility": true, "title": true,
	"summary": true, "why": true, "date": true, "status": true, "category": true, "tech": true,
	"tags": true, "mediums": true, "credits": true, "facts": true, "links": true, "copy": true,
	"placements": true, "richDescription": true, "assets": true, "members": true,
	"private": false,
}

// Marshal encodes the projection canonically and checks it against the
// public manifest schema; anything that fails is a bug, never published.
func Marshal(pm *manifest.Manifest) ([]byte, error) {
	b, err := manifest.Marshal(pm)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if ps := manifest.SchemaProblems("public-manifest.schema.json", doc); len(ps) > 0 {
		return nil, fmt.Errorf("public manifest fails its schema: %v", ps[0])
	}
	return b, nil
}
