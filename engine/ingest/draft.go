package ingest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
)

// roleFor is the asset role of an ingested file.
var roleFor = map[media.Kind]manifest.AssetRole{
	media.Image:    manifest.AssetRoleOriginal,
	media.Video:    manifest.AssetRoleVideo,
	media.Audio:    manifest.AssetRoleAudio,
	media.Document: manifest.AssetRoleDocument,
}

// draft builds a new draft item for an ingested file.
func draft(m *manifest.Manifest, id manifest.Ulid, name string, t media.Type, kind manifest.Kind,
	facts manifest.ItemFacts, date, blob, original string) manifest.Item {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	orig := manifest.BlobRef(original)
	it := manifest.Item{
		ID:         id,
		Slug:       uniqueSlug(m, slugify(base)),
		Kind:       kind,
		Visibility: manifest.ItemVisibilityDraft,
		Title:      titleFrom(base),
		Facts:      facts,
		Assets:     []manifest.AssetRef{{Role: roleFor[t.Kind], Blob: manifest.BlobRef(blob), Original: &orig}},
	}
	if date != "" {
		it.Date = &manifest.Date{Value: manifest.IsoPartialDate(date)}
	}
	return it
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugify makes a slug from a file name: "Night Market (v2).PNG" ->
// "night-market-v2". Accents fold to ASCII.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	slug := strings.Trim(nonSlug.ReplaceAllString(b.String(), "-"), "-")
	if len(slug) > 56 {
		slug = strings.TrimRight(slug[:56], "-")
	}
	if slug == "" {
		slug = "untitled"
	}
	return slug
}

// uniqueSlug appends -2, -3, ... until the slug collides with no slug or
// alias in the manifest.
func uniqueSlug(m *manifest.Manifest, slug string) manifest.Slug {
	taken := map[string]bool{}
	for _, it := range m.Items {
		taken[string(it.Slug)] = true
		for _, a := range it.Aliases {
			taken[string(a)] = true
		}
	}
	if !taken[slug] {
		return manifest.Slug(slug)
	}
	for n := 2; ; n++ {
		if s := fmt.Sprintf("%s-%d", slug, n); !taken[s] {
			return manifest.Slug(s)
		}
	}
}

var spaces = regexp.MustCompile(`[\s_]+`)

// titleFrom turns a file name into a readable title: "night_market-study"
// -> "night market-study". It keeps the case you chose.
func titleFrom(base string) string {
	t := strings.TrimSpace(spaces.ReplaceAllString(base, " "))
	if t == "" {
		return "Untitled"
	}
	if len(t) > 200 {
		t = t[:200]
	}
	return t
}
