// Package derived is the local cache of job outputs, under the cache dir:
//
//	derived/<jobkey>/<file>      a job's outputs
//	derived/<jobkey>/job.json    its receipt: every file's sha256, plus facts
//	derived/tmp/                 a running job's scratch; disposable
//
// A job dir only appears complete: outputs are written to a temp dir,
// verified, and renamed into place with the receipt already inside. Files
// only enter as Outputs, which come from a sanitize.Verified (media) or from
// Go-encoded JSON, so nothing the verifier hasn't passed is ever cached for
// upload.
package derived

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/nick5616/canon/engine/sanitize"
)

// ReceiptName is the receipt's file name in every job dir, local and remote.
const ReceiptName = "job.json"

// Store is the derived cache.
type Store struct{ root string }

// Open creates <cacheDir>/derived and clears scratch older than a day.
func Open(cacheDir string) (*Store, error) {
	s := &Store{root: filepath.Join(cacheDir, "derived")}
	if err := os.MkdirAll(filepath.Join(s.root, "tmp"), 0o700); err != nil {
		return nil, err
	}
	entries, _ := os.ReadDir(filepath.Join(s.root, "tmp"))
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			os.RemoveAll(filepath.Join(s.root, "tmp", e.Name()))
		}
	}
	return s, nil
}

// TempDir is scratch for one job run. Callers remove it.
func (s *Store) TempDir() (string, error) {
	return os.MkdirTemp(filepath.Join(s.root, "tmp"), "job-")
}

// Receipt describes a finished job.
type Receipt struct {
	Jobkey string `json:"jobkey"`
	Recipe string `json:"recipe"` // name@vN
	Source string `json:"source"` // sha256:<hex>
	Files  []File `json:"files"`
	// Facts the recipe computed, so a run elsewhere can fill them too.
	Facts map[string]any `json:"facts,omitempty"`
}

// File is one output.
type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Type   string `json:"type"` // MIME type
	// Width and Height are set for images (views need them).
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

// Output is a file ready to commit. Make one with Media or JSON.
type Output struct {
	name, path, sum, mime string
	size                  int64
	width, height         int
}

var fileName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Media wraps a verified media file as an output called name.
func Media(name string, v sanitize.Verified) Output {
	o := Output{name: name, path: v.Path(), sum: v.SHA256(), mime: v.Type().MIME, size: v.Size()}
	// exiftool -n reports "W H" for every image it reads.
	if sz, ok := v.Tags()["Composite:ImageSize"].(string); ok {
		fmt.Sscanf(sz, "%d %d", &o.width, &o.height)
	}
	return o
}

// JSON writes v as name in dir (Go-encoded, so it carries nothing but v).
func JSON(dir, name string, v any) (Output, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Output{}, err
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return Output{}, err
	}
	sum := sha256.Sum256(b)
	return Output{name: name, path: p, sum: hex.EncodeToString(sum[:]), mime: "application/json", size: int64(len(b))}, nil
}

// Commit moves outputs into <jobkey>/ with their receipt. The outputs must
// sit in one scratch dir (TempDir), which becomes the job dir. An existing
// job dir wins: another runner got there first.
func (s *Store) Commit(scratch string, rc Receipt, outs []Output) (Done, error) {
	if !keyRe.MatchString(rc.Jobkey) {
		return Done{}, fmt.Errorf("derived: bad jobkey %q", rc.Jobkey)
	}
	rc.Files = []File{}
	seen := map[string]bool{}
	for _, o := range outs {
		switch {
		case !fileName.MatchString(o.name) || o.name == ReceiptName:
			return Done{}, fmt.Errorf("derived: bad output name %q", o.name)
		case seen[o.name]:
			return Done{}, fmt.Errorf("derived: output %q twice", o.name)
		case filepath.Dir(o.path) != filepath.Clean(scratch) || filepath.Base(o.path) != o.name:
			return Done{}, fmt.Errorf("derived: output %q isn't %s in the job's scratch dir", o.path, o.name)
		}
		seen[o.name] = true
		rc.Files = append(rc.Files, File{Name: o.name, SHA256: o.sum, Size: o.size, Type: o.mime, Width: o.width, Height: o.height})
	}
	sort.Slice(rc.Files, func(i, j int) bool { return rc.Files[i].Name < rc.Files[j].Name })
	// Anything else in scratch (tool intermediates) is not an output.
	entries, err := os.ReadDir(scratch)
	if err != nil {
		return Done{}, err
	}
	for _, e := range entries {
		if !seen[e.Name()] {
			if err := os.RemoveAll(filepath.Join(scratch, e.Name())); err != nil {
				return Done{}, err
			}
		}
	}
	b, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return Done{}, err
	}
	if err := os.WriteFile(filepath.Join(scratch, ReceiptName), append(b, '\n'), 0o600); err != nil {
		return Done{}, err
	}
	dst := filepath.Join(s.root, rc.Jobkey)
	if err := os.Rename(scratch, dst); err != nil {
		if d, ok := s.Lookup(rc.Jobkey); ok {
			os.RemoveAll(scratch)
			return d, nil
		}
		return Done{}, err
	}
	d, ok := s.Lookup(rc.Jobkey)
	if !ok {
		return Done{}, fmt.Errorf("derived: %s vanished after commit", rc.Jobkey)
	}
	return d, nil
}

var keyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Done is a committed job dir. Only Lookup and Commit make one.
type Done struct {
	dir     string
	receipt Receipt
	raw     []byte
}

func (d Done) Receipt() Receipt { return d.receipt }

// ReceiptBytes is job.json exactly as stored; it uploads last.
func (d Done) ReceiptBytes() []byte { return d.raw }

// Path is an output's file.
func (d Done) Path(name string) string { return filepath.Join(d.dir, name) }

// ErrIncomplete means a job dir is missing files its receipt lists.
var ErrIncomplete = errors.New("derived: job dir is incomplete")

// Lookup finds a committed job. A dir whose receipt is unreadable or whose
// files are missing doesn't count.
func (s *Store) Lookup(jobkey string) (Done, bool) {
	if !keyRe.MatchString(jobkey) {
		return Done{}, false
	}
	dir := filepath.Join(s.root, jobkey)
	raw, err := os.ReadFile(filepath.Join(dir, ReceiptName))
	if err != nil {
		return Done{}, false
	}
	var rc Receipt
	if json.Unmarshal(raw, &rc) != nil || rc.Jobkey != jobkey {
		return Done{}, false
	}
	for _, f := range rc.Files {
		if fi, err := os.Stat(filepath.Join(dir, f.Name)); err != nil || fi.Size() != f.Size {
			return Done{}, false
		}
	}
	return Done{dir: dir, receipt: rc, raw: raw}, true
}

// ReceiptSHA256 is the receipt's hash, as uploaded.
func (d Done) ReceiptSHA256() string {
	s := sha256.Sum256(d.raw)
	return hex.EncodeToString(s[:])
}
