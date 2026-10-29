package plan

import (
	"context"
	"path"
	"regexp"
	"strings"

	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/store"
)

// Listing is the actual state: what's in the private and archive buckets,
// from object listings only (no downloads).
type Listing struct {
	Blobs     map[string]gcs.Object // private blobs/, by hash
	Originals map[string]gcs.Object // archive originals/, by hash
	// Strays are objects under blobs/ whose names aren't content addresses.
	Strays   []gcs.Object
	Manifest *gcs.Object // nil when the bucket has no manifest yet
	// History holds the revisions that have a snapshot in manifest/history/.
	History map[string]bool
}

const (
	historyPrefix = "manifest/history/"
)

// casName matches <kind>/sha256/ab/cd/<hash>.<ext> with consistent shards.
var casName = regexp.MustCompile(`^(blobs|originals)/sha256/([0-9a-f]{2})/([0-9a-f]{2})/([0-9a-f]{64})\.([a-z0-9]+)$`)

// BlobPath is a sanitized blob's object name (both buckets use it).
func BlobPath(sum, ext string) string { return casPath("blobs", sum, ext) }

// OriginalPath is an original's object name in the archive.
func OriginalPath(sum, ext string) string { return casPath("originals", sum, ext) }

func casPath(kind, sum, ext string) string {
	return path.Join(kind, "sha256", sum[0:2], sum[2:4], sum+"."+ext)
}

// ParseCAS returns the hash in a content-addressed object name.
func ParseCAS(name string) (kind, sum, ext string, ok bool) {
	m := casName.FindStringSubmatch(name)
	if m == nil || m[2] != m[4][0:2] || m[3] != m[4][2:4] {
		return "", "", "", false
	}
	return m[1], m[4], m[5], true
}

// HistoryPath is a manifest snapshot's name: <utc>-<revision>.json.
func HistoryPath(utc, revision string) string {
	return historyPrefix + utc + "-" + revision + ".json"
}

var historyName = regexp.MustCompile(`^manifest/history/[0-9TZ]+-([0-9a-f]{16})\.json$`)

// List reads both buckets: three prefix listings and nothing else.
func List(ctx context.Context, api gcs.API, private, archive string) (Listing, error) {
	l := Listing{Blobs: map[string]gcs.Object{}, Originals: map[string]gcs.Object{}, History: map[string]bool{}}
	blobs, err := api.List(ctx, private, "blobs/")
	if err != nil {
		return l, err
	}
	for _, o := range blobs {
		if kind, sum, _, ok := ParseCAS(o.Name); ok && kind == "blobs" {
			l.Blobs[sum] = o
		} else {
			l.Strays = append(l.Strays, o)
		}
	}
	mf, err := api.List(ctx, private, "manifest/")
	if err != nil {
		return l, err
	}
	for _, o := range mf {
		switch {
		case o.Name == store.ManifestObject:
			o := o
			l.Manifest = &o
		case strings.HasPrefix(o.Name, historyPrefix):
			if m := historyName.FindStringSubmatch(o.Name); m != nil {
				l.History[m[1]] = true
			}
		}
	}
	origs, err := api.List(ctx, archive, "originals/")
	if err != nil {
		return l, err
	}
	for _, o := range origs {
		if kind, sum, _, ok := ParseCAS(o.Name); ok && kind == "originals" {
			l.Originals[sum] = o
		}
	}
	return l, nil
}
