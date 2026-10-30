package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
)

// Desired is what should exist: the working manifest plus the local files
// it and the archive need. It's plain data, so Diff stays a pure function.
type Desired struct {
	Manifest []byte // the exact bytes apply will write
	Items    []manifest.Item
	Revision string
	// Changes since base, for the write-manifest line.
	Changes store.Changes
	// Blobs are the local cache files, by hash, for every blob the
	// manifest references that the cache has.
	Blobs map[string]LocalFile
	// Originals are every kept original in the cache (D34).
	Originals []LocalFile
	// Jobs are the derived jobs the manifest needs (M4).
	Jobs []Job
	// Public is what the public bucket should hold; nil skips it (M5).
	Public *PublicState
}

// Job is one needed job, as the plan sees it.
type Job struct {
	Key, Recipe string
	Item        string // id
	Slug        string
	// Local is set when this machine has the job's outputs.
	Local *LocalJob
	// State and Error come from the job database when Local is nil.
	State, Error string
}

// LocalJob is a finished job in the derived cache.
type LocalJob struct {
	Files int
	Bytes int64
}

// LocalFile is a file in the local cache.
type LocalFile struct {
	SHA256 string
	Ext    string
	Size   int64
}

// Diff compares desired with actual and returns the ops, in execution
// order, plus problems. utc stamps the history snapshot's name.
func Diff(d Desired, l Listing, utc string) ([]Op, []manifest.Problem) {
	var ops []Op
	var ps []manifest.Problem
	problem := func(sev manifest.Severity, code, path, format string, args ...any) {
		ps = append(ps, manifest.Problem{Code: code, Severity: sev, Path: path, Message: fmt.Sprintf(format, args...)})
	}

	// Where each blob and original is first referenced, by item order.
	type ref struct{ item, asset int }
	blobRefs := map[string]ref{}
	origRefs := map[string]ref{}
	for i, it := range d.Items {
		for j, a := range it.Assets {
			sum := strings.TrimPrefix(string(a.Blob), "sha256:")
			if _, ok := blobRefs[sum]; !ok {
				blobRefs[sum] = ref{i, j}
			}
			if a.Original != nil {
				o := strings.TrimPrefix(string(*a.Original), "sha256:")
				if _, ok := origRefs[o]; !ok {
					origRefs[o] = ref{i, j}
				}
			}
		}
	}
	itemOf := func(r ref, ok bool) (*string, string) {
		if !ok {
			return nil, ""
		}
		id := string(d.Items[r.item].ID)
		return &id, string(d.Items[r.item].Slug)
	}

	// Archive every kept original (D34). An existing one with the wrong
	// checksum is for a human to look at (D38).
	local := map[string]bool{}
	for _, o := range d.Originals {
		local[o.SHA256] = true
		path := OriginalPath(o.SHA256, o.Ext)
		r, referenced := origRefs[o.SHA256]
		item, slug := itemOf(r, referenced)
		if obj, ok := l.Originals[o.SHA256]; ok {
			if obj.SHA256 != o.SHA256 {
				problem(manifest.Error, "archive-checksum", "", "archive object %s has sha256 metadata %q; check it by hand (the archive can't be overwritten)", obj.Name, obj.SHA256)
			}
			continue
		}
		if slug == "" {
			slug = "not referenced by any item"
		}
		ops = append(ops, Op{Type: ArchiveOriginal, Path: path, Bucket: Archive, Item: item, Detail: slug, Bytes: ptr(o.Size)})
	}
	for sum, r := range origRefs {
		if _, ok := l.Originals[sum]; !ok && !local[sum] {
			problem(manifest.Warning, "original-missing", fmt.Sprintf("/items/%d/assets/%d/original", r.item, r.asset),
				"the original of %s is in neither the archive nor this machine's cache, so it won't be archived from here", d.Items[r.item].Slug)
		}
	}

	// Upload every referenced blob the private bucket lacks.
	for sum, r := range blobRefs {
		item, slug := itemOf(r, true)
		path := fmt.Sprintf("/items/%d/assets/%d/blob", r.item, r.asset)
		lf, haveLocal := d.Blobs[sum]
		obj, remote := l.Blobs[sum]
		switch {
		case remote && obj.SHA256 == sum:
		case remote && haveLocal:
			ops = append(ops, Op{Type: UploadBlob, Path: BlobPath(sum, lf.Ext), Bucket: Private, Item: item,
				Detail: slug + "; re-upload: checksum mismatch", Bytes: ptr(lf.Size)})
		case remote:
			problem(manifest.Error, "blob-checksum", path, "%s has sha256 metadata %q and the local cache doesn't have the blob to re-upload", obj.Name, obj.SHA256)
		case haveLocal:
			ops = append(ops, Op{Type: UploadBlob, Path: BlobPath(sum, lf.Ext), Bucket: Private, Item: item, Detail: slug, Bytes: ptr(lf.Size)})
		default:
			problem(manifest.Error, "blob-missing", path, "blob sha256:%s of %s is in neither the private bucket nor the local cache", sum, slug)
		}
	}

	// Upload every finished job whose receipt the bucket lacks. A receipt
	// there means the job is done, whichever machine ran it.
	want := map[string]bool{}
	for _, j := range d.Jobs {
		want[j.Key] = true
		if _, done := l.Derived[j.Key][derived.ReceiptName]; done {
			continue
		}
		id := j.Item
		switch {
		case j.Local != nil:
			ops = append(ops, Op{Type: RunJob, Path: jobs.DerivedPrefix + j.Key + "/", Bucket: Private, Item: &id,
				Detail: fmt.Sprintf("%s %s, %d files", j.Recipe, j.Slug, j.Local.Files), Bytes: ptr(j.Local.Bytes)})
		case j.State == string(jobs.Failed):
			problem(manifest.Warning, "job-failed", "", "%s on %s failed: %s (canon jobs retry %s)", j.Recipe, j.Slug, j.Error, j.Key)
		default:
			problem(manifest.Warning, "job-pending", "", "%s on %s hasn't run on this machine (canon jobs run)", j.Recipe, j.Slug)
		}
	}
	keys := make([]string, 0, len(l.Derived))
	for k := range l.Derived {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if want[k] {
			continue
		}
		var n int64
		for _, o := range l.Derived[k] {
			n += o.Size
		}
		ops = append(ops, Op{Type: Orphan, Path: jobs.DerivedPrefix + k + "/", Bucket: Private, Detail: "job no longer needed", Bytes: ptr(n)})
	}

	// Report what nothing references. Never deleted here (M8's gc does).
	for sum, obj := range l.Blobs {
		if _, ok := blobRefs[sum]; !ok {
			ops = append(ops, Op{Type: Orphan, Path: obj.Name, Bucket: Private, Detail: "not referenced by the manifest", Bytes: ptr(obj.Size)})
		}
	}
	for _, obj := range l.Strays {
		ops = append(ops, Op{Type: Orphan, Path: obj.Name, Bucket: Private, Detail: "not a content address", Bytes: ptr(obj.Size)})
	}

	if d.Public != nil {
		diffPublic(d.Public, l.Public, func(o Op) { ops = append(ops, o) })
	}

	// The manifest last, then its snapshot.
	sum := gcs.SHA256Hex(d.Manifest)
	if l.Manifest == nil || l.Manifest.SHA256 != sum {
		gen := "none"
		if l.Manifest != nil {
			gen = fmt.Sprint(l.Manifest.Generation)
		}
		ops = append(ops, Op{Type: WriteManifest, Path: store.ManifestObject, Bucket: Private,
			Detail: fmt.Sprintf("%s (generation %s -> next)", describe(d.Changes), gen), Bytes: ptr(int64(len(d.Manifest)))})
	}
	if !l.History[d.Revision] {
		ops = append(ops, Op{Type: WriteHistory, Path: HistoryPath(utc, d.Revision), Bucket: Private,
			Detail: "snapshot for rollback", Bytes: ptr(int64(len(d.Manifest)))})
	}

	sort.SliceStable(ops, func(i, j int) bool {
		if pi, pj := phase[ops[i].Type], phase[ops[j].Type]; pi != pj {
			return pi < pj
		}
		return ops[i].Path < ops[j].Path
	})
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Severity != ps[j].Severity {
			return ps[i].Severity == manifest.Error
		}
		return ps[i].Path+ps[i].Message < ps[j].Path+ps[j].Message
	})
	return ops, ps
}

// phase is the execution order (§1 of the M3 plan): everything a manifest
// points at exists before the manifest does.
// Public: every file a view or the public manifest links is copied before
// either is written, and unpublish runs after nothing links its files.
var phase = map[OpType]int{ArchiveOriginal: 0, UploadBlob: 1, RunJob: 2, PublishCopy: 3, WriteManifest: 4,
	WriteHistory: 5, WriteView: 6, WritePublicManifest: 7, Unpublish: 8, Orphan: 9}

func describe(c store.Changes) string {
	s := fmt.Sprintf("%d added, %d changed, %d removed", len(c.Added), len(c.Changed), len(c.Removed))
	if c.Other {
		s += ", plus profile/taxonomy/surfaces"
	}
	return s
}

func ptr[T any](v T) *T { return &v }
