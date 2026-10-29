// Package jobs runs recipes on blobs: which jobs the manifest needs (pure),
// their state (SQLite), and the runner that executes them locally into the
// derived cache. Apply uploads the results; nothing here touches a bucket
// except to ask whether a job's receipt is already there.
package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/nick5616/canon/engine/canonjson"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/recipes"
)

// Key is a job's key: the hash of exactly what produces its outputs.
//
//	sha256(sourceSha256 "\n" name "\n" version "\n" canonicalJSON(params))
//
// Tool versions are left out on purpose (M4 plan): a tool upgrade that
// changes output bumps the recipe's version instead.
func Key(sourceSHA256 string, r recipes.Recipe) string {
	params, err := canonjson.Marshal(r.Params)
	if err != nil {
		panic(fmt.Sprintf("recipe %s: params don't encode: %v", r.ID(), err))
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%d\n%s", sourceSHA256, r.Name, r.Version, params)
	return hex.EncodeToString(h.Sum(nil))
}

// Spec is one job the manifest needs.
type Spec struct {
	Key    string
	Recipe recipes.Recipe
	Source string // the blob's sha256, hex
	Type   media.Type
	Item   manifest.Ulid // the first item that uses the blob
	Slug   string
}

// TypeOf resolves a blob's media type, from wherever its file is known.
type TypeOf func(sum string) (media.Type, bool)

// ByExt maps a content-address extension onto its media type.
func ByExt(ext string) (media.Type, bool) {
	for _, t := range media.Supported {
		if t.Ext == ext {
			return t, true
		}
	}
	return media.Type{}, false
}

// Desired expands the manifest by the registry, in item, asset and recipe
// order. A blob used twice gets one job per recipe. Blobs whose type is
// unknown (not here and not remote) are left out.
func Desired(m *manifest.Manifest, reg []recipes.Recipe, typeOf TypeOf) []Spec {
	var out []Spec
	seen := map[string]bool{}
	for _, it := range m.Items {
		firstImage := true
		for _, a := range it.Assets {
			sum := strings.TrimPrefix(string(a.Blob), "sha256:")
			t, ok := typeOf(sum)
			if !ok {
				continue
			}
			src := recipes.Source{Type: t, Kind: it.Kind, FirstImage: firstImage && t.Kind == media.Image}
			if t.Kind == media.Image {
				firstImage = false
			}
			for _, r := range reg {
				if !r.Applies(src) {
					continue
				}
				k := Key(sum, r)
				if seen[k] {
					continue
				}
				seen[k] = true
				out = append(out, Spec{Key: k, Recipe: r, Source: sum, Type: t, Item: it.ID, Slug: string(it.Slug)})
			}
		}
	}
	return out
}
