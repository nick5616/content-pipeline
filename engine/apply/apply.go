// Package apply executes a saved plan, and nothing else. It is the only code
// that writes to a bucket. Sanitized blobs go to the private bucket and
// untouched originals to the archive, through separate writers that take
// separate types, so an original can't reach the private bucket.
package apply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/plan"
	"github.com/nick5616/canon/engine/store"
)

// Deps are apply's inputs besides the plan.
type Deps struct {
	API     gcs.API
	Private string // bucket names
	Archive string
	Working *store.Working
	Blobs   *blobstore.Store
	// StateDir holds apply.lock.
	StateDir string
}

// Status is progress (openapi ApplyStatus).
type Status struct {
	Plan          string  `json:"plan"`
	State         string  `json:"state"` // running, done, failed
	Done          int     `json:"done"`
	Total         int     `json:"total"`
	NewGeneration *int64  `json:"newGeneration"`
	Error         *string `json:"error"`
}

var (
	// ErrMoved means the remote manifest's generation isn't the plan's.
	ErrMoved = errors.New("the remote manifest changed since this plan was made; run `canon plan` again")
	// ErrStale means the working manifest was edited after planning (D37).
	ErrStale = errors.New("the working manifest was edited since this plan was made; run `canon plan` again")
	// ErrBusy means another apply holds the lock.
	ErrBusy = errors.New("another apply is running")
	// ErrCorrupt means a cache file's bytes don't match its name.
	ErrCorrupt = errors.New("cache file is corrupt")
)

// Apply checks the plan is still current, then runs its ops in order,
// reporting progress after each. It stops at the first failure; everything
// done by then is safe to keep, and the next plan lists what's left.
func Apply(ctx context.Context, p *plan.Plan, d Deps, progress func(Status)) (Status, error) {
	st := Status{Plan: p.ID, State: "running"}
	for _, o := range p.Ops {
		if o.Writes() {
			st.Total++
		}
	}
	fail := func(err error) (Status, error) {
		msg := err.Error()
		st.State, st.Error = "failed", &msg
		if progress != nil {
			progress(st)
		}
		return st, err
	}
	report := func() {
		if progress != nil {
			progress(st)
		}
	}

	unlock, err := lock(d.StateDir)
	if err != nil {
		return fail(err)
	}
	defer unlock()

	b, _, _, err := plan.Current(d.Working)
	if err != nil {
		return fail(err)
	}
	if store.Revision(b) != p.Revision {
		return fail(ErrStale)
	}
	gen := int64(0)
	switch obj, err := d.API.Attrs(ctx, d.Private, store.ManifestObject); {
	case errors.Is(err, gcs.ErrNotExist):
	case err != nil:
		return fail(err)
	default:
		gen = obj.Generation
	}
	if gen != p.Generation {
		return fail(fmt.Errorf("%w (plan: %d, now: %d)", ErrMoved, p.Generation, gen))
	}
	report()

	priv := privateWriter{d.API, d.Private}
	arch := archiveWriter{d.API, d.Archive}
	for _, op := range p.Ops {
		if !op.Writes() {
			continue
		}
		switch op.Type {
		case plan.ArchiveOriginal:
			o, err := cached(op.Path, "originals", d.Blobs.Original)
			if err != nil {
				return fail(err)
			}
			err = arch.original(ctx, o)
			if err != nil {
				return fail(err)
			}
		case plan.UploadBlob:
			bl, err := cached(op.Path, "blobs", d.Blobs.Blob)
			if err != nil {
				return fail(err)
			}
			if err := priv.blob(ctx, bl); err != nil {
				return fail(err)
			}
		case plan.WriteManifest:
			obj, err := priv.manifest(ctx, b, p.Generation)
			if errors.Is(err, gcs.ErrPrecondition) {
				return fail(ErrMoved)
			}
			if err != nil {
				return fail(err)
			}
			st.NewGeneration = &obj.Generation
			if err := d.Working.SetBase(b, obj.Generation); err != nil {
				return fail(fmt.Errorf("manifest written (generation %d), but recording it locally failed: %w", obj.Generation, err))
			}
		case plan.WriteHistory:
			if err := priv.history(ctx, op.Path, b); err != nil {
				return fail(err)
			}
		default:
			return fail(fmt.Errorf("apply: op %s is not supported until a later milestone", op.Type))
		}
		st.Done++
		report()
	}
	st.State = "done"
	report()
	return st, nil
}

// cached finds the local file a content-addressed op path names.
func cached[T any](path, kind string, find func(string) (T, bool)) (T, error) {
	var zero T
	k, sum, _, ok := plan.ParseCAS(path)
	if !ok || k != kind {
		return zero, fmt.Errorf("apply: bad %s path %q", kind, path)
	}
	v, ok := find(sum)
	if !ok {
		return zero, fmt.Errorf("apply: %s %s is no longer in the local cache", kind, sum)
	}
	return v, nil
}

// privateWriter writes the private bucket: sanitized blobs, the manifest
// and its history. It has no method that takes an original.
type privateWriter struct {
	api    gcs.API
	bucket string
}

func (w privateWriter) blob(ctx context.Context, b blobstore.Blob) error {
	// A blob whose checksum metadata is wrong is replaced (D38); versioning
	// keeps the old object.
	return putFile(ctx, w.api, w.bucket, plan.BlobPath(b.SHA256(), b.Ext()), b.Path(), b.SHA256(), contentType(b.Ext()), true)
}

func (w privateWriter) manifest(ctx context.Context, b []byte, gen int64) (gcs.Object, error) {
	return w.api.Write(ctx, w.bucket, store.ManifestObject, bytes.NewReader(b), opts(b, "application/json", gen))
}

func (w privateWriter) history(ctx context.Context, path string, b []byte) error {
	_, err := w.api.Write(ctx, w.bucket, path, bytes.NewReader(b), opts(b, "application/json", 0))
	if errors.Is(err, gcs.ErrPrecondition) {
		return alreadyThere(ctx, w.api, w.bucket, path, gcs.SHA256Hex(b))
	}
	return err
}

// archiveWriter writes the archive bucket, and only originals.
type archiveWriter struct {
	api    gcs.API
	bucket string
}

func (w archiveWriter) original(ctx context.Context, o blobstore.ArchivedOriginal) error {
	// Never replaced: the archive account can only create (D14, D38).
	return putFile(ctx, w.api, w.bucket, plan.OriginalPath(o.SHA256(), o.Ext()), o.Path(), o.SHA256(), contentType(o.Ext()), false)
}

func opts(b []byte, ct string, gen int64) gcs.WriteOpts {
	return gcs.WriteOpts{ContentType: ct, SHA256: gcs.SHA256Hex(b), CRC32C: crc32.Checksum(b, gcs.CRC32C), IfGeneration: gen}
}

// putFile uploads a cache file under its content address. The file is
// hashed first and must match its name. If the object already exists with
// the right sha256 (another apply got there), that's success; with the
// wrong one, replace says whether to overwrite it.
func putFile(ctx context.Context, api gcs.API, bucket, name, path, sum, ct string, replace bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h, c := sha256.New(), crc32.New(gcs.CRC32C)
	if _, err := io.Copy(io.MultiWriter(h, c), f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fmt.Errorf("%w: %s hashes to %s; re-ingest its original", ErrCorrupt, path, got)
	}
	o := gcs.WriteOpts{ContentType: ct, SHA256: sum, CRC32C: c.Sum32()}
	write := func() error {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err := api.Write(ctx, bucket, name, f, o)
		return err
	}
	err = write()
	if !errors.Is(err, gcs.ErrPrecondition) {
		return err
	}
	obj, err := api.Attrs(ctx, bucket, name)
	if err != nil {
		return err
	}
	if obj.SHA256 == sum {
		return nil
	}
	if !replace {
		return fmt.Errorf("%s/%s exists with sha256 %q, not %s; check it by hand", bucket, name, obj.SHA256, sum)
	}
	o.IfGeneration = obj.Generation
	return write()
}

func alreadyThere(ctx context.Context, api gcs.API, bucket, name, sum string) error {
	obj, err := api.Attrs(ctx, bucket, name)
	if err != nil {
		return err
	}
	if obj.SHA256 != sum {
		return fmt.Errorf("%s/%s exists with different content", bucket, name)
	}
	return nil
}

func contentType(ext string) string {
	for _, t := range media.Supported {
		if t.Ext == ext {
			return t.MIME
		}
	}
	return "application/octet-stream"
}

func lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "apply.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
