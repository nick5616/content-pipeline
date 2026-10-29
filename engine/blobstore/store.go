// Package blobstore is the local, content-addressed cache under the cache
// dir:
//
//	blobs/sha256/ab/cd/<sha256>.<ext>      sanitized bytes, named by their hash
//	originals/sha256/ab/cd/<sha256>.<ext>  untouched originals, for the archive (D27)
//	index/originals/<sha256>.json          which blob each original became
//	tmp/                                   staging; anything here is disposable
//
// Blobs only enter through Put, which takes a sanitize.Verified, so nothing
// unsanitized can be stored as a blob. Originals hold GPS and serials, so the
// whole tree is private to the user (0700 directories, 0600 files).
package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nick5616/canon/engine/sanitize"
)

// Store is the cache rooted at a directory.
type Store struct{ root string }

// Open creates the layout if needed and clears staging left by crashed runs.
func Open(root string) (*Store, error) {
	s := &Store{root: root}
	for _, d := range []string{"blobs/sha256", "originals/sha256", "index/originals", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, err
		}
	}
	s.sweep(24 * time.Hour)
	return s, nil
}

// sweep removes staging dirs older than age. Younger ones may belong to an
// ingest still running in another process (canon serve and the CLI).
func (s *Store) sweep(age time.Duration) {
	entries, _ := os.ReadDir(filepath.Join(s.root, "tmp"))
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > age {
			os.RemoveAll(filepath.Join(s.root, "tmp", e.Name()))
		}
	}
}

// TempDir makes a private staging directory. Callers remove it when done.
func (s *Store) TempDir() (string, error) {
	return os.MkdirTemp(filepath.Join(s.root, "tmp"), "ingest-")
}

// Original is an untouched input, staged into the cache and hashed. It is
// never modified and never uploaded except by the archive writer (M3).
type Original struct {
	path string
	sum  string
	size int64
	name string
}

func (o Original) Path() string   { return o.path }
func (o Original) SHA256() string { return o.sum }
func (o Original) Size() int64    { return o.size }
func (o Original) Name() string   { return o.name }

// ErrTooLarge means the input passed the size cap.
var ErrTooLarge = errors.New("file is larger than maxIngestBytes")

// Stage copies r into dir/in, hashing as it goes, and stops at max bytes.
// name is only a label (the uploaded or on-disk file name).
func (s *Store) Stage(dir string, r io.Reader, name string, max int64) (Original, error) {
	path := filepath.Join(dir, "in")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Original{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Original{}, err
	}
	if n > max {
		return Original{}, fmt.Errorf("%s: %w (%d bytes)", name, ErrTooLarge, max)
	}
	return Original{path: path, sum: hex.EncodeToString(h.Sum(nil)), size: n, name: name}, nil
}

func (s *Store) shard(kind, sum, ext string) string {
	return filepath.Join(s.root, kind, "sha256", sum[0:2], sum[2:4], sum+"."+ext)
}

// BlobPath is where a blob with this hash and extension lives.
func (s *Store) BlobPath(sum, ext string) string { return s.shard("blobs", sum, ext) }

// OriginalPath is where a kept original lives.
func (s *Store) OriginalPath(sum, ext string) string { return s.shard("originals", sum, ext) }

// Put moves a verified file into the blob cache under its hash. Storing the
// same bytes twice is a no-op.
func (s *Store) Put(v sanitize.Verified) (string, error) {
	dst := s.BlobPath(v.SHA256(), v.Type().Ext)
	return "sha256:" + v.SHA256(), place(v.Path(), dst)
}

// KeepOriginal moves a staged original into originals/, where the archive
// op picks it up at apply time.
func (s *Store) KeepOriginal(o Original, ext string) (string, error) {
	dst := s.OriginalPath(o.sum, ext)
	return dst, place(o.path, dst)
}

// place renames src to dst atomically, or drops src if dst already exists.
func place(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return os.Remove(src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(src, 0o600); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Different filesystem: copy to a temp name beside dst, then rename.
	tmp := dst + ".part"
	if err := copyFile(src, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// IndexEntry records what an original became, so ingesting it again is a
// no-op even if its draft was deleted.
type IndexEntry struct {
	Blob string `json:"blob"` // sha256:<hex>
	Type string `json:"type"` // media type name
	Ext  string `json:"ext"`
}

// Record writes the index entry for an original.
func (s *Store) Record(origSum string, e IndexEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	path := filepath.Join(s.root, "index", "originals", origSum+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Lookup returns the index entry for an original, if its blob still exists.
func (s *Store) Lookup(origSum string) (IndexEntry, bool) {
	b, err := os.ReadFile(filepath.Join(s.root, "index", "originals", origSum+".json"))
	if err != nil {
		return IndexEntry{}, false
	}
	var e IndexEntry
	if json.Unmarshal(b, &e) != nil || len(e.Blob) != len("sha256:")+64 {
		return IndexEntry{}, false
	}
	if _, err := os.Stat(s.BlobPath(e.Blob[len("sha256:"):], e.Ext)); err != nil {
		return IndexEntry{}, false
	}
	return e, true
}
