package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/manifest"
)

// ManifestObject is the manifest's path in the private bucket.
const ManifestObject = "manifest/manifest.json"

const (
	baseFile = "base.json"      // the remote manifest's exact bytes
	metaFile = "base.meta.json" // {generation, sha256} of base.json
)

// Remote is the private bucket, where the manifest lives.
type Remote struct {
	API    gcs.API
	Bucket string
}

// State is the sync state (openapi SyncStatus.state).
type State string

const (
	InSync   State = "in-sync"
	Behind   State = "behind"   // remote moved, no local edits
	Edited   State = "edited"   // local edits on top of base
	Diverged State = "diverged" // both
	Offline  State = "offline"
)

// Status compares working, base and remote.
type Status struct {
	State            State  `json:"state"`
	BaseGeneration   int64  `json:"baseGeneration"`
	RemoteGeneration *int64 `json:"remoteGeneration"` // nil when offline
	LocalEdits       bool   `json:"localEdits"`
	Revision         string `json:"revision"` // working revision
}

var (
	// ErrDiverged means the remote moved and there are local edits; v1 never
	// merges.
	ErrDiverged = errors.New("the remote manifest changed and you have local edits; " +
		"review them with `canon diff`, then `canon pull --discard-local` to take the remote")
	// ErrRemoteInvalid means the remote manifest doesn't load.
	ErrRemoteInvalid = errors.New("the remote manifest is invalid")
)

type baseMeta struct {
	Generation int64  `json:"generation"`
	SHA256     string `json:"sha256"`
}

// Base returns the remote manifest the working copy is based on, and its
// generation. With no base yet (nothing pulled or applied), it returns
// nil and generation 0. A base.json that doesn't match its meta (a crash
// between the two writes) is treated as missing.
func (w *Working) Base() ([]byte, int64, error) {
	mb, err := os.ReadFile(filepath.Join(w.dir, metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var meta baseMeta
	if err := json.Unmarshal(mb, &meta); err != nil {
		return nil, 0, nil
	}
	b, err := os.ReadFile(filepath.Join(w.dir, baseFile))
	if err != nil || gcs.SHA256Hex(b) != meta.SHA256 {
		return nil, 0, nil
	}
	return b, meta.Generation, nil
}

// SetBase records b as the remote manifest at generation gen. Apply calls it
// after writing the manifest; pull after reading it.
func (w *Working) SetBase(b []byte, gen int64) error {
	unlock, err := w.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return w.setBase(b, gen)
}

func (w *Working) setBase(b []byte, gen int64) error {
	if err := writeAtomic(filepath.Join(w.dir, baseFile), b); err != nil {
		return err
	}
	meta, _ := json.Marshal(baseMeta{Generation: gen, SHA256: gcs.SHA256Hex(b)})
	return writeAtomic(filepath.Join(w.dir, metaFile), meta)
}

func (w *Working) clearBase() error {
	for _, f := range []string{metaFile, baseFile} {
		if err := os.Remove(filepath.Join(w.dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// local is the local side of a status: the working bytes, base, and whether
// working differs from base.
type local struct {
	working  []byte
	exists   bool
	base     []byte
	baseGen  int64
	edits    bool
	revision string
}

func (w *Working) local() (local, error) {
	var l local
	var err error
	if l.working, l.exists, err = w.Bytes(); err != nil {
		return l, err
	}
	if l.base, l.baseGen, err = w.Base(); err != nil {
		return l, err
	}
	l.edits = l.exists && !bytes.Equal(l.working, l.base)
	if l.exists {
		l.revision = Revision(l.working)
	} else {
		_, l.revision, _, err = w.Load()
	}
	return l, err
}

// Status makes one metadata request to the remote and compares.
func (w *Working) Status(ctx context.Context, r Remote) (Status, error) {
	l, err := w.local()
	if err != nil {
		return Status{}, err
	}
	st := Status{BaseGeneration: l.baseGen, LocalEdits: l.edits, Revision: l.revision}
	obj, err := r.API.Attrs(ctx, r.Bucket, ManifestObject)
	var remoteGen int64
	var remoteSHA string
	switch {
	case errors.Is(err, gcs.ErrNotExist):
	case err != nil:
		if errors.Is(err, gcs.ErrOffline) {
			st.State = Offline
			return st, nil
		}
		return st, err
	default:
		remoteGen, remoteSHA = obj.Generation, obj.SHA256
	}
	st.RemoteGeneration = &remoteGen

	moved := remoteGen != l.baseGen
	// Working already equals the remote (say, apply wrote the manifest and
	// crashed before recording base): nothing to lose by pulling.
	if moved && l.exists && remoteSHA != "" && gcs.SHA256Hex(l.working) == remoteSHA {
		st.LocalEdits = false
	}
	switch {
	case !moved && !st.LocalEdits:
		st.State = InSync
	case !moved:
		st.State = Edited
	case !st.LocalEdits:
		st.State = Behind
	default:
		st.State = Diverged
	}
	return st, nil
}

// Pull fetches the remote manifest into base and, unless there are local
// edits, into working. With local edits it fails with ErrDiverged unless
// discardLocal is set.
func (w *Working) Pull(ctx context.Context, r Remote, discardLocal bool) (Status, error) {
	st, err := w.Status(ctx, r)
	if err != nil {
		return st, err
	}
	if st.State == Offline {
		return st, gcs.ErrOffline
	}
	if st.LocalEdits && !discardLocal {
		if st.State == Edited {
			return st, nil // nothing to pull; keep the edits
		}
		return st, ErrDiverged
	}
	b, gen, err := r.API.Read(ctx, r.Bucket, ManifestObject)
	missing := errors.Is(err, gcs.ErrNotExist)
	if err != nil && !missing {
		return st, err
	}

	unlock, err := w.lock()
	if err != nil {
		return st, err
	}
	if missing {
		err = w.clearBase()
		if err == nil {
			err = removeIfExists(w.path())
		}
	} else {
		if m, ps, lerr := manifest.Load(bytes.NewReader(b)); lerr != nil || m == nil || manifest.HasErrors(ps) {
			unlock()
			return st, fmt.Errorf("%w: %v %v", ErrRemoteInvalid, lerr, ps)
		}
		err = w.setBase(b, gen)
		if err == nil {
			err = writeAtomic(w.path(), b)
		}
	}
	unlock()
	if err != nil {
		return st, err
	}
	return w.Status(ctx, r)
}

// Fresh is what plan runs first: it pulls when the remote moved and there
// are no local edits, and refuses when both moved or GCS is unreachable.
func (w *Working) Fresh(ctx context.Context, r Remote) (Status, error) {
	st, err := w.Status(ctx, r)
	if err != nil {
		return st, err
	}
	switch st.State {
	case Offline:
		return st, gcs.ErrOffline
	case Diverged:
		return st, ErrDiverged
	case Behind:
		return w.Pull(ctx, r, false)
	}
	return st, nil
}

// Changes is what differs between two manifests, by item.
type Changes struct {
	Added, Changed, Removed []ItemRef
	// Other is true when something besides items changed (profile,
	// taxonomy, surfaces, owned domains).
	Other bool
}

// ItemRef names an item in a change list.
type ItemRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// Empty reports whether nothing changed.
func (c Changes) Empty() bool {
	return len(c.Added)+len(c.Changed)+len(c.Removed) == 0 && !c.Other
}

// Diff compares two manifests' bytes (either may be nil: no manifest).
func Diff(before, after []byte) (Changes, error) {
	load := func(b []byte) (*manifest.Manifest, error) {
		if b == nil {
			return &manifest.Manifest{}, nil
		}
		m, ps, err := manifest.Load(bytes.NewReader(b))
		if err == nil && m == nil {
			err = fmt.Errorf("invalid manifest: %v", ps)
		}
		return m, err
	}
	a, err := load(before)
	if err != nil {
		return Changes{}, err
	}
	b, err := load(after)
	if err != nil {
		return Changes{}, err
	}
	var c Changes
	old := map[manifest.Ulid]manifest.Item{}
	for _, it := range a.Items {
		old[it.ID] = it
	}
	seen := map[manifest.Ulid]bool{}
	for _, it := range b.Items {
		seen[it.ID] = true
		ref := ItemRef{string(it.ID), string(it.Slug)}
		prev, ok := old[it.ID]
		switch {
		case !ok:
			c.Added = append(c.Added, ref)
		case !sameJSON(prev, it):
			c.Changed = append(c.Changed, ref)
		}
	}
	for _, it := range a.Items {
		if !seen[it.ID] {
			c.Removed = append(c.Removed, ItemRef{string(it.ID), string(it.Slug)})
		}
	}
	a.Items, b.Items = nil, nil
	c.Other = (before == nil) != (after == nil) || !sameJSON(a, b)
	return c, nil
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
