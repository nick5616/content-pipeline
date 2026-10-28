// Package surfaces registers the sites Canon feeds. Each surface owns a
// placement config schema and a view schema; adding a portfolio means adding
// a surface here, never changing the item schema.
package surfaces

import (
	"fmt"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/nick5616/canon/schema"
)

// IDs are the registered surfaces, in display order.
var IDs = []string{
	"nickel-desktop",
	"software-portfolio",
	"design-portfolio",
	"art-portfolio",
	"holodeck-showroom",
	"holodeck-art-room",
	"holodeck-dj-room",
}

// Surface is one registered surface and its compiled schemas.
type Surface struct {
	ID        string
	Placement *jsonschema.Schema
	View      *jsonschema.Schema
}

// Registry looks surfaces up by id.
type Registry struct {
	byID map[string]Surface
}

// Default compiles the registry of every surface in IDs.
func Default() (Registry, error) {
	r := Registry{byID: map[string]Surface{}}
	for _, id := range IDs {
		p, err := schema.Compile(schema.PlacementPath(id))
		if err != nil {
			return Registry{}, fmt.Errorf("surface %s placement: %w", id, err)
		}
		v, err := schema.Compile(schema.ViewPath(id))
		if err != nil {
			return Registry{}, fmt.Errorf("surface %s view: %w", id, err)
		}
		r.byID[id] = Surface{ID: id, Placement: p, View: v}
	}
	return r, nil
}

// MustDefault is Default for callers that treat a broken embedded schema as a
// programming error.
func MustDefault() Registry {
	r, err := Default()
	if err != nil {
		panic(err)
	}
	return r
}

// Get returns the surface with id.
func (r Registry) Get(id string) (Surface, bool) {
	s, ok := r.byID[id]
	return s, ok
}

// IDs returns the registered ids, sorted.
func (r Registry) IDs() []string {
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
