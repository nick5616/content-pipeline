// Package recipes are the named, versioned ways a blob becomes derived
// files and facts (design doc, "Derived assets as jobs"). A recipe writes
// into a scratch dir and returns what it made; the job runner verifies the
// files and commits them. Bump Version whenever output would change,
// including after a tool upgrade that changes it.
package recipes

import (
	"context"
	"fmt"
	"sort"

	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/sanitize"
)

// Recipe is one recipe.
type Recipe struct {
	Name    string
	Version int
	// Params go into the job key: changing one rebuilds that recipe's
	// outputs. They also drive Run.
	Params map[string]any
	// Applies says whether the recipe runs on a source.
	Applies func(Source) bool
	// FFmpeg recipes share a cap of concurrent runs (heavy encodes).
	FFmpeg bool
	// Facts lists the facts keys the recipe owns (D48: filled when empty).
	Facts []string
	Run   func(ctx context.Context, t Tools, s Source, scratch string) (Result, error)
}

// ID is name@vN, as in job lists and receipts.
func (r Recipe) ID() string { return fmt.Sprintf("%s@v%d", r.Name, r.Version) }

// Source is the blob a job runs on, and the asset it came from.
type Source struct {
	Path       string // local blob file; empty when the blob isn't here
	Type       media.Type
	Kind       manifest.Kind // the item's
	FirstImage bool          // the item's first image asset
}

// Tools runs external tools. Tests can replace Run.
type Tools struct{ Run sanitize.Runner }

// NewTools uses the real binaries.
func NewTools() Tools { return Tools{Run: sanitize.NewTools().Run} }

// Result is what a run made: files in scratch, to be verified and
// committed, and facts for the item.
type Result struct {
	Files []File
	Facts map[string]any
}

// File is one output in scratch. Type nil means Go-encoded JSON (see
// derived.JSON); otherwise the verifier checks it as that type.
type File struct {
	Name string
	Type *media.Type
	// JSON is the value to encode when Type is nil.
	JSON any
}

// All is the registry, sorted by name.
func All() []Recipe {
	rs := []Recipe{thumb, lqip, palette, poster, web, preview, peaks, djMP3, djgridRecipe}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
	return rs
}

// Find returns the recipe with name.
func Find(name string) (Recipe, bool) {
	for _, r := range All() {
		if r.Name == name {
			return r, true
		}
	}
	return Recipe{}, false
}

func isImage(s Source) bool { return s.Type.Kind == media.Image }
func isVideo(s Source) bool { return s.Type.Kind == media.Video }
func isAudio(s Source) bool { return s.Type.Kind == media.Audio }
