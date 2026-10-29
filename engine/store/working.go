// Package store holds the local working manifest (D6): working.json (your
// edits) and base.json (the remote manifest at the generation they're based
// on) in the state dir, never the repo. sync.go compares them with the
// remote and pulls.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/surfaces"
)

// File is the working manifest's name in the state dir.
const File = "working.json"

// Working is the working manifest on disk.
type Working struct {
	dir         string
	profileName string
	reg         surfaces.Registry
}

// Open returns the working manifest in stateDir. profileName seeds
// profile.name when there is no working manifest yet.
func Open(stateDir, profileName string, reg surfaces.Registry) *Working {
	return &Working{dir: stateDir, profileName: profileName, reg: reg}
}

// ErrNoProfileName means a manifest has to be created and config has no
// profileName for it.
var ErrNoProfileName = errors.New("no working manifest yet, and config has no profileName to start one with")

// InvalidError is returned when an update would leave the manifest invalid.
type InvalidError struct{ Problems []manifest.Problem }

func (e *InvalidError) Error() string {
	var errs []string
	for _, p := range e.Problems {
		if p.Severity == manifest.Error {
			errs = append(errs, p.String())
		}
	}
	return fmt.Sprintf("the change would make the manifest invalid: %v", errs)
}

// Revision identifies a working manifest's exact bytes.
func Revision(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:8])
}

func (w *Working) path() string { return filepath.Join(w.dir, File) }

// Load reads the working manifest, or an empty one if none exists yet.
func (w *Working) Load() (*manifest.Manifest, string, []manifest.Problem, error) {
	b, err := os.ReadFile(w.path())
	if errors.Is(err, os.ErrNotExist) {
		if w.profileName == "" {
			return nil, "", nil, ErrNoProfileName
		}
		m := manifest.Empty(w.profileName)
		out, err := manifest.Marshal(m)
		if err != nil {
			return nil, "", nil, err
		}
		return m, Revision(out), nil, nil
	}
	if err != nil {
		return nil, "", nil, err
	}
	m, ps, err := manifest.Load(bytes.NewReader(b))
	if err != nil {
		return nil, "", nil, err
	}
	if m == nil {
		return nil, "", ps, &InvalidError{Problems: ps}
	}
	return m, Revision(b), ps, nil
}

// ErrStale means the working manifest changed since the revision the caller
// based its edit on (If-Match).
var ErrStale = errors.New("the working manifest changed since you loaded it")

// Update applies fn to the manifest under an exclusive lock and writes the
// result atomically. If fn fails, or the result has errors, nothing is
// written. It returns the new revision.
func (w *Working) Update(fn func(*manifest.Manifest) error) (string, error) {
	return w.UpdateIf("", fn)
}

// UpdateIf is Update, but fails with ErrStale unless the current revision is
// rev. An empty rev skips the check.
func (w *Working) UpdateIf(rev string, fn func(*manifest.Manifest) error) (string, error) {
	unlock, err := w.lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	m, cur, _, err := w.Load()
	if err != nil {
		return "", err
	}
	if rev != "" && rev != cur {
		return "", ErrStale
	}
	if err := fn(m); err != nil {
		return "", err
	}
	if ps := manifest.Validate(m, w.reg); manifest.HasErrors(ps) {
		return "", &InvalidError{Problems: ps}
	}
	out, err := manifest.Marshal(m)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(w.path(), out); err != nil {
		return "", err
	}
	return Revision(out), nil
}

// lock takes the state dir's exclusive lock, shared by every writer of
// working.json and base.json (CLI and canon serve alike).
func (w *Working) lock() (func(), error) {
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(w.dir, "working.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// Bytes returns working.json exactly as stored, and whether it exists.
func (w *Working) Bytes() ([]byte, bool, error) {
	b, err := os.ReadFile(w.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return b, err == nil, err
}

// writeAtomic writes via a synced temp file and a rename, so a crash leaves
// either the old file or the new one, never half of one.
func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".working-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
