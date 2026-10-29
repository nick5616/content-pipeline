// Package plan computes what apply would change: the desired state (working
// manifest plus local cache) against the actual state (bucket listings),
// through the pure Diff. Plans are saved, reviewed, and applied as written.
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/canonjson"
	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/recipes"
	"github.com/nick5616/canon/engine/store"
)

// OpType is one kind of plan step (openapi OpType).
type OpType string

const (
	ArchiveOriginal OpType = "archive-original"
	UploadBlob      OpType = "upload-blob"
	RunJob          OpType = "run-job" // upload a finished job's outputs (D50)
	WriteManifest   OpType = "write-manifest"
	WriteHistory    OpType = "write-history"
	Orphan          OpType = "orphan" // reported, never executed
)

// Bucket is a bucket role.
type Bucket string

const (
	Private Bucket = "private"
	Public  Bucket = "public"
	Archive Bucket = "archive"
)

// Op is one step.
type Op struct {
	Type   OpType  `json:"type"`
	Path   string  `json:"path"`
	Bucket Bucket  `json:"bucket"`
	Item   *string `json:"item"`
	Detail string  `json:"detail"`
	Bytes  *int64  `json:"bytes"`
}

// Writes reports whether the op changes anything.
func (o Op) Writes() bool { return o.Type != Orphan }

// Summary counts ops by kind. publish and unpublish stay 0 until M5.
type Summary struct {
	Archive   int `json:"archive"`
	Upload    int `json:"upload"`
	Jobs      int `json:"jobs"`
	Publish   int `json:"publish"`
	Unpublish int `json:"unpublish"`
	Orphaned  int `json:"orphaned"`
}

// Plan is a saved plan (openapi Plan).
type Plan struct {
	ID string `json:"id"`
	// Generation is the remote manifest's generation the plan was computed
	// against (0: no manifest yet). Apply refuses if it moved.
	Generation int64 `json:"generation"`
	// Revision is the working manifest's; apply refuses if it changed (D37).
	Revision string             `json:"revision"`
	Summary  Summary            `json:"summary"`
	Ops      []Op               `json:"ops"`
	Problems []manifest.Problem `json:"problems"`
}

// Empty reports whether applying would write nothing.
func (p *Plan) Empty() bool {
	for _, o := range p.Ops {
		if o.Writes() {
			return false
		}
	}
	return true
}

// Env is everything Make needs.
type Env struct {
	Working       *store.Working
	Blobs         *blobstore.Store
	Remote        store.Remote // the private bucket
	ArchiveBucket string
	Now           func() time.Time
	IDs           *ids.Generator // nil: ids.New
	// Derived is the local job output cache; nil plans no jobs.
	Derived *derived.Store
	// RunJobs runs pending jobs before planning (D45); nil skips that.
	RunJobs func(ctx context.Context) error
	// JobState reports a job's local state, for the plan's warnings.
	JobState func(key string) (state, lastError string)
}

// Current returns the working manifest's bytes as apply would write them:
// working.json, or the empty manifest when there is none yet.
func Current(w *store.Working) ([]byte, *manifest.Manifest, []manifest.Problem, error) {
	b, exists, err := w.Bytes()
	if err != nil {
		return nil, nil, nil, err
	}
	if !exists {
		m, _, ps, err := w.Load()
		if err != nil {
			return nil, nil, nil, err
		}
		b, err = manifest.Marshal(m)
		return b, m, ps, err
	}
	m, ps, err := manifest.Load(bytes.NewReader(b))
	if err == nil && m == nil {
		err = &store.InvalidError{Problems: ps}
	}
	return b, m, ps, err
}

// Make syncs with the remote (pulling when it's safe), lists both buckets
// and diffs. A manifest with errors, or a diff with errors, gives no plan.
func Make(ctx context.Context, env Env) (*Plan, error) {
	if _, err := env.Working.Fresh(ctx, env.Remote); err != nil {
		return nil, err
	}
	if env.RunJobs != nil {
		if err := env.RunJobs(ctx); err != nil {
			return nil, fmt.Errorf("running jobs: %w", err)
		}
	}
	b, m, ps, err := Current(env.Working)
	if err != nil {
		return nil, err
	}
	if manifest.HasErrors(ps) {
		return nil, &store.InvalidError{Problems: ps}
	}
	base, _, err := env.Working.Base()
	if err != nil {
		return nil, err
	}
	changes, err := store.Diff(base, b)
	if err != nil {
		return nil, err
	}
	d, err := Gather(b, m, changes, env.Blobs)
	if err != nil {
		return nil, err
	}
	l, err := List(ctx, env.Remote.API, env.Remote.Bucket, env.ArchiveBucket)
	if err != nil {
		return nil, err
	}
	if env.Derived != nil {
		d.Jobs = GatherJobs(m, env.Blobs, env.Derived, l, env.JobState)
	}

	now := time.Now
	if env.Now != nil {
		now = env.Now
	}
	ops, diffPs := Diff(d, l, now().UTC().Format("20060102T150405Z"))
	ps = append(ps, diffPs...)
	if manifest.HasErrors(ps) {
		return nil, &store.InvalidError{Problems: ps}
	}

	id := ids.New()
	if env.IDs != nil {
		id = env.IDs.New()
	}
	p := &Plan{ID: id.String(), Revision: d.Revision, Ops: ops, Problems: ps}
	if l.Manifest != nil {
		p.Generation = l.Manifest.Generation
	}
	if p.Ops == nil {
		p.Ops = []Op{}
	}
	if p.Problems == nil {
		p.Problems = []manifest.Problem{}
	}
	for _, o := range ops {
		switch o.Type {
		case ArchiveOriginal:
			p.Summary.Archive++
		case UploadBlob:
			p.Summary.Upload++
		case RunJob:
			p.Summary.Jobs++
		case Orphan:
			p.Summary.Orphaned++
		}
	}
	return p, nil
}

// Gather resolves the local files the manifest and archive need.
func Gather(b []byte, m *manifest.Manifest, changes store.Changes, blobs *blobstore.Store) (Desired, error) {
	d := Desired{Manifest: b, Items: m.Items, Revision: store.Revision(b), Changes: changes, Blobs: map[string]LocalFile{}}
	for _, it := range m.Items {
		for _, a := range it.Assets {
			sum := strings.TrimPrefix(string(a.Blob), "sha256:")
			if _, done := d.Blobs[sum]; done {
				continue
			}
			if bl, ok := blobs.Blob(sum); ok {
				fi, err := os.Stat(bl.Path())
				if err != nil {
					return d, err
				}
				d.Blobs[sum] = LocalFile{SHA256: sum, Ext: bl.Ext(), Size: fi.Size()}
			}
		}
	}
	origs, err := blobs.Originals()
	if err != nil {
		return d, err
	}
	for _, o := range origs {
		fi, err := os.Stat(o.Path())
		if err != nil {
			return d, err
		}
		d.Originals = append(d.Originals, LocalFile{SHA256: o.SHA256(), Ext: o.Ext(), Size: fi.Size()})
	}
	return d, nil
}

// GatherJobs lists the jobs the manifest needs. A blob's type comes from
// the local cache, or from its object name when only the bucket has it.
func GatherJobs(m *manifest.Manifest, blobs *blobstore.Store, der *derived.Store, l Listing, state func(string) (string, string)) []Job {
	typeOf := func(sum string) (media.Type, bool) {
		if b, ok := blobs.Blob(sum); ok {
			return jobs.ByExt(b.Ext())
		}
		if o, ok := l.Blobs[sum]; ok {
			if _, _, ext, ok := ParseCAS(o.Name); ok {
				return jobs.ByExt(ext)
			}
		}
		return media.Type{}, false
	}
	var out []Job
	for _, s := range jobs.Desired(m, recipes.All(), typeOf) {
		j := Job{Key: s.Key, Recipe: s.Recipe.ID(), Item: string(s.Item), Slug: s.Slug}
		if dn, ok := der.Lookup(s.Key); ok {
			lj := &LocalJob{Files: len(dn.Receipt().Files) + 1, Bytes: int64(len(dn.ReceiptBytes()))}
			for _, f := range dn.Receipt().Files {
				lj.Bytes += f.Size
			}
			j.Local = lj
		} else if state != nil {
			j.State, j.Error = state(s.Key)
		}
		out = append(out, j)
	}
	return out
}

// Plans are kept in <state>/plans/<id>.json.
const plansDir = "plans"

var idRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// Save writes p to <stateDir>/plans/<id>.json (0600: plans name drafts).
func Save(stateDir string, p *Plan) (string, error) {
	dir := filepath.Join(stateDir, plansDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, p.ID+".json")
	return path, WriteFile(path, p)
}

// WriteFile writes p as canonical JSON.
func WriteFile(path string, p *Plan) error {
	b, err := canonjson.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// ErrNotFound means no saved plan has that id.
var ErrNotFound = errors.New("no such plan")

// Open loads a plan by id (from the state dir) or by file path.
func Open(stateDir, idOrPath string) (*Plan, error) {
	path := idOrPath
	if idRe.MatchString(idOrPath) {
		path = filepath.Join(stateDir, plansDir, idOrPath+".json")
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, idOrPath)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var p Plan
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("read plan %s: %w", idOrPath, err)
	}
	if !idRe.MatchString(p.ID) {
		return nil, fmt.Errorf("read plan %s: bad id %q", idOrPath, p.ID)
	}
	return &p, nil
}

// Render prints the plan the way the design doc shows it.
func Render(w io.Writer, p *Plan) error {
	if p.Empty() {
		fmt.Fprintf(w, "No changes. The buckets match the working manifest (generation %d).\n", p.Generation)
	} else {
		fmt.Fprintf(w, "Plan %s: %d to archive, %d to upload, %d jobs, %d orphaned (against generation %d)\n",
			p.ID, p.Summary.Archive, p.Summary.Upload, p.Summary.Jobs, p.Summary.Orphaned, p.Generation)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, o := range p.Ops {
		sym, verb := opLabel[o.Type][0], opLabel[o.Type][1]
		var extra []string
		if o.Detail != "" && o.Type != WriteManifest {
			extra = append(extra, o.Detail)
		}
		if o.Bytes != nil && o.Type != WriteManifest && o.Type != WriteHistory {
			extra = append(extra, humanBytes(*o.Bytes))
		}
		line := fmt.Sprintf("  %s %s\t%s", sym, verb, short(o.Path))
		switch {
		case o.Type == WriteManifest:
			line += "\t" + o.Detail
		case len(extra) > 0:
			line += "\t(" + strings.Join(extra, ", ") + ")"
		}
		fmt.Fprintln(tw, line)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, pr := range p.Problems {
		fmt.Fprintf(w, "  ! %s\n", pr.String())
	}
	return nil
}

var opLabel = map[OpType][2]string{
	ArchiveOriginal: {"+", "archive"},
	UploadBlob:      {"+", "upload"},
	RunJob:          {"+", "job"},
	WriteManifest:   {"~", "manifest"},
	WriteHistory:    {"+", "history"},
	Orphan:          {"?", "orphan"},
}

// short abbreviates the hash in a content-addressed path.
func short(p string) string {
	if key, ok := strings.CutPrefix(p, jobs.DerivedPrefix); ok && len(key) == 65 {
		return jobs.DerivedPrefix + key[:12] + "…/"
	}
	if kind, sum, ext, ok := ParseCAS(p); ok {
		return fmt.Sprintf("%s/sha256/%s/%s/%s….%s", kind, sum[0:2], sum[2:4], sum[:12], ext)
	}
	return p
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
