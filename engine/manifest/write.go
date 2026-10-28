package manifest

import (
	"slices"
	"strings"

	"github.com/nick5616/canon/engine/canonjson"
)

// Marshal writes m in canonical form: items sorted by id, object keys
// sorted, 2-space indent, trailing newline. The same manifest always gives
// the same bytes, whatever order its items arrived in.
func Marshal(m *Manifest) ([]byte, error) {
	return canonjson.Marshal(normalized(m))
}

// normalized returns a shallow copy with items sorted and required arrays
// non-nil, so they encode as [] rather than null.
func normalized(m *Manifest) *Manifest {
	c := *m
	c.Items = slices.Clone(m.Items)
	slices.SortStableFunc(c.Items, func(a, b Item) int { return strings.Compare(string(a.ID), string(b.ID)) })
	orEmpty(&c.Items)
	orEmpty(&c.OwnedDomains)
	orEmpty(&c.Surfaces)
	orEmpty(&c.Taxonomy.Categories)
	orEmpty(&c.Taxonomy.Tech)
	orEmpty(&c.Taxonomy.Mediums)
	orEmpty(&c.Taxonomy.Tags)
	orEmpty(&c.Taxonomy.Viewports)
	return &c
}

func orEmpty[T any](s *[]T) {
	if *s == nil {
		*s = []T{}
	}
}

// Empty is a valid manifest with nothing in it: the starting point for a new
// bucket (generation 0).
func Empty(profileName string) *Manifest {
	m := &Manifest{SchemaVersion: 1, Profile: Profile{Name: profileName}}
	return normalized(m)
}
