// Package ingest turns dropped files into sanitized blobs and draft items,
// entirely locally:
//
//	stage -> dedupe -> detect -> sanitize -> verify -> store -> facts -> draft
//
// Originals on disk are only ever read. Every step is idempotent: ingesting
// the same file again reports the existing item and changes nothing.
package ingest

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/sanitize"
	"github.com/nick5616/canon/engine/store"
)

// Stage names match IngestBatch.files[].stage in api/openapi.yaml.
type Stage string

const (
	Queued   Stage = "queued"
	Detect   Stage = "detect"
	Sanitize Stage = "sanitize"
	Verify   Stage = "verify"
	Hash     Stage = "hash"
	Draft    Stage = "draft"
	Done     Stage = "done"
	Failed   Stage = "failed"
)

// FileResult is one file's progress and outcome.
type FileResult struct {
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	DetectedType *string `json:"detectedType"`
	Stage        Stage   `json:"stage"`
	Blob         *string `json:"blob"`
	Item         *string `json:"item"`
	Error        *string `json:"error"`
	// Note explains a no-op ("already ingested as rpg-quests").
	Note *string `json:"note"`
	// Slug is for the CLI's output; the API reports the item id.
	Slug string `json:"-"`
}

// Options change one ingest.
type Options struct {
	Kind   manifest.Kind // "" picks by type (D29)
	DryRun bool          // detect, sanitize and verify only; store nothing
}

// Ingester holds what ingest needs. NewIngester wires the real tools.
type Ingester struct {
	Blobs     *blobstore.Store
	Working   *store.Working
	Sanitizer sanitize.Sanitizer
	Verifier  sanitize.Verifier
	Run       sanitize.Runner // exiftool and ffprobe reads
	IDs       *ids.Generator
	MaxBytes  int64
}

// New returns an Ingester using the real tools.
func New(blobs *blobstore.Store, working *store.Working, maxBytes int64) *Ingester {
	tools := sanitize.NewTools()
	return &Ingester{
		Blobs: blobs, Working: working, Sanitizer: tools, Verifier: sanitize.Verifier{Run: tools.Run},
		Run: tools.Run, MaxBytes: maxBytes,
	}
}

// StagePath copies a file on disk into staging. The original is opened
// read-only and never touched again.
func (in *Ingester) StagePath(path string) (blobstore.Original, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return blobstore.Original{}, err
	}
	if !fi.Mode().IsRegular() {
		return blobstore.Original{}, fmt.Errorf("%s: not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return blobstore.Original{}, err
	}
	defer f.Close()
	return in.StageReader(f, filepath.Base(path))
}

// StageReader stages an upload or any other stream.
func (in *Ingester) StageReader(r io.Reader, name string) (blobstore.Original, error) {
	dir, err := in.Blobs.TempDir()
	if err != nil {
		return blobstore.Original{}, err
	}
	o, err := in.Blobs.Stage(dir, r, name, in.MaxBytes)
	if err != nil {
		os.RemoveAll(dir)
	}
	return o, err
}

// Discard drops a staged original that won't be processed.
func Discard(o blobstore.Original) { os.RemoveAll(filepath.Dir(o.Path())) }

// Process runs one staged original through the pipeline. progress, if set,
// sees every stage change. The staging dir is removed when it returns.
func (in *Ingester) Process(ctx context.Context, o blobstore.Original, opts Options, progress func(FileResult)) FileResult {
	defer Discard(o)
	r := FileResult{Name: o.Name(), Size: o.Size(), Stage: Queued}
	step := func(s Stage) {
		r.Stage = s
		if progress != nil {
			progress(r)
		}
	}
	fail := func(err error) FileResult {
		msg := err.Error()
		r.Error = &msg
		step(Failed)
		return r
	}
	origRef := "sha256:" + o.SHA256()

	// Already in the manifest? (The same photo ingested on another machine
	// has the same original hash, even if its sanitized bytes differ.)
	m, _, _, err := in.Working.Load()
	if err != nil {
		return fail(err)
	}
	if it := findOriginal(m, origRef); it != nil {
		return existing(&r, it, "already ingested as "+string(it.Slug), step)
	}

	step(Detect)
	f, err := os.Open(o.Path())
	if err != nil {
		return fail(err)
	}
	typ, err := media.Detect(f)
	f.Close()
	if err != nil {
		return fail(err)
	}
	r.DetectedType = &typ.MIME

	kind, err := pickKind(typ, opts.Kind)
	if err != nil {
		return fail(err)
	}
	origMeta, err := in.originalMeta(ctx, o.Path())
	if err != nil {
		return fail(err)
	}
	if opts.Kind == "" && typ.Kind == media.Image && origMeta.camera {
		kind = manifest.KindPhoto
	}

	blobRef, verified, err := in.sanitizeAndVerify(ctx, o, typ, opts.DryRun, step)
	if err != nil {
		return fail(err)
	}
	if opts.DryRun {
		r.Blob = &blobRef
		step(Done)
		return r
	}

	step(Hash)
	if verified != nil {
		if blobRef, err = in.Blobs.Put(*verified); err != nil {
			return fail(err)
		}
		if err := in.Blobs.Record(o.SHA256(), blobstore.IndexEntry{Blob: blobRef, Type: typ.Name, Ext: typ.Ext}); err != nil {
			return fail(err)
		}
	}
	r.Blob = &blobRef
	if _, err := in.Blobs.KeepOriginal(o, typ.Ext); err != nil {
		return fail(err)
	}

	facts, err := in.facts(ctx, typ, kind, in.Blobs.BlobPath(strings.TrimPrefix(blobRef, "sha256:"), typ.Ext))
	if err != nil {
		return fail(err)
	}

	step(Draft)
	var made *manifest.Item
	var same *manifest.Item
	_, err = in.Working.Update(func(m *manifest.Manifest) error {
		// Re-check under the lock: another process may have just drafted it.
		if it := findOriginal(m, origRef); it != nil {
			same = it
			return nil
		}
		if it := findBlob(m, blobRef); it != nil {
			same = it
			return nil
		}
		it := draft(m, in.newID(), o.Name(), typ, kind, facts, origMeta.date, blobRef, origRef)
		m.Items = append(m.Items, it)
		made = &it
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if same != nil {
		return existing(&r, same, "same content as "+string(same.Slug), step)
	}
	id := string(made.ID)
	r.Item, r.Slug = &id, string(made.Slug)
	step(Done)
	return r
}

// existing reports a no-op: the file is already in the manifest.
func existing(r *FileResult, it *manifest.Item, note string, step func(Stage)) FileResult {
	id := string(it.ID)
	r.Item, r.Slug, r.Note = &id, string(it.Slug), &note
	if len(it.Assets) > 0 {
		b := string(it.Assets[0].Blob)
		r.Blob = &b
	}
	step(Done)
	return *r
}

// sanitizeAndVerify returns the blob ref, plus the Verified file when it
// still has to be stored. A known original whose blob is cached skips both
// steps.
func (in *Ingester) sanitizeAndVerify(ctx context.Context, o blobstore.Original, typ media.Type, dry bool, step func(Stage)) (string, *sanitize.Verified, error) {
	if e, ok := in.Blobs.Lookup(o.SHA256()); ok && e.Type == typ.Name && !dry {
		return e.Blob, nil, nil
	}
	step(Sanitize)
	out := filepath.Join(filepath.Dir(o.Path()), "out."+typ.Ext)
	if err := in.Sanitizer.Sanitize(ctx, typ, o.Path(), out); err != nil {
		return "", nil, err
	}
	step(Verify)
	v, err := in.Verifier.Verify(ctx, typ, out)
	if err != nil {
		return "", nil, err
	}
	return "sha256:" + v.SHA256(), &v, nil
}

func (in *Ingester) newID() manifest.Ulid {
	if in.IDs != nil {
		return manifest.Ulid(in.IDs.New().String())
	}
	return manifest.Ulid(ids.New().String())
}

// kindsFor are the item kinds each kind of file may become.
var kindsFor = map[media.Kind][]manifest.Kind{
	media.Image:    {manifest.KindArtwork, manifest.KindPhoto},
	media.Video:    {manifest.KindVideo},
	media.Audio:    {manifest.KindTrack},
	media.Document: {manifest.KindWriting},
}

// pickKind applies an override, or the default for the file's kind.
func pickKind(t media.Type, override manifest.Kind) (manifest.Kind, error) {
	allowed := kindsFor[t.Kind]
	if override == "" {
		return allowed[0], nil
	}
	for _, k := range allowed {
		if k == override {
			return k, nil
		}
	}
	return "", fmt.Errorf("a %s file can't be a %s item", t.Kind, override)
}

func findOriginal(m *manifest.Manifest, ref string) *manifest.Item {
	for i := range m.Items {
		for _, a := range m.Items[i].Assets {
			if a.Original != nil && string(*a.Original) == ref {
				return &m.Items[i]
			}
		}
	}
	return nil
}

func findBlob(m *manifest.Manifest, ref string) *manifest.Item {
	for i := range m.Items {
		for _, a := range m.Items[i].Assets {
			if string(a.Blob) == ref {
				return &m.Items[i]
			}
		}
	}
	return nil
}
