package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/recipes"
	"github.com/nick5616/canon/engine/sanitize"
	"github.com/nick5616/canon/engine/store"
)

// DerivedPrefix is where job outputs live in the private bucket:
// derived/<jobkey>/<file>, with derived/<jobkey>/job.json written last.
const DerivedPrefix = "derived/"

// ReceiptPath is a job's receipt in the bucket.
func ReceiptPath(key string) string { return DerivedPrefix + key + "/" + derived.ReceiptName }

// FilePath is one of a job's outputs in the bucket.
func FilePath(key, name string) string { return DerivedPrefix + key + "/" + name }

// Receipts lists the jobs whose receipt is in the private bucket.
func Receipts(ctx context.Context, api gcs.API, bucket string) (map[string]gcs.Object, error) {
	objs, err := api.List(ctx, bucket, DerivedPrefix)
	if err != nil {
		return nil, err
	}
	out := map[string]gcs.Object{}
	for _, o := range objs {
		if key, name, ok := splitDerived(o.Name); ok && name == derived.ReceiptName {
			out[key] = o
		}
	}
	return out, nil
}

// splitDerived parses derived/<jobkey>/<name>.
func splitDerived(name string) (key, file string, ok bool) {
	rest, found := strings.CutPrefix(name, DerivedPrefix)
	if !found {
		return "", "", false
	}
	key, file, found = strings.Cut(rest, "/")
	if !found || len(key) != 64 || file == "" || strings.Contains(file, "/") {
		return "", "", false
	}
	return key, file, true
}

// SplitDerived is splitDerived for the planner.
func SplitDerived(name string) (key, file string, ok bool) { return splitDerived(name) }

// Runner runs jobs locally.
type Runner struct {
	DB       *DB
	Derived  *derived.Store
	Blobs    *blobstore.Store
	Working  *store.Working
	Recipes  []recipes.Recipe // nil: recipes.All()
	Tools    recipes.Tools
	Verifier sanitize.Verifier
	// Remote lists receipts in the private bucket; nil skips the check
	// (no buckets configured, or offline by choice).
	Remote func(ctx context.Context) (map[string]gcs.Object, error)
	// Workers defaults to the CPU count; FFmpeg caps concurrent ffmpeg
	// recipes (default 2).
	Workers, FFmpeg int
	StateDir        string // holds jobs.lock
}

// Options narrow one run.
type Options struct {
	Item   string // id or slug
	Recipe string // name
	// Failed reruns failed jobs too (canon jobs run does; plan doesn't).
	Failed bool
	// RefreshFacts overwrites facts the recipes own (D48).
	RefreshFacts bool
	Progress     func(Job)
}

// Report sums up a run.
type Report struct {
	Ran     int      `json:"ran"`
	Cached  int      `json:"cached"` // already in the local cache
	Remote  int      `json:"remote"` // receipt already in the bucket
	Failed  int      `json:"failed"`
	Skipped int      `json:"skipped"` // failed before; not retried this run
	Facts   int      `json:"facts"`   // items whose facts were filled
	Errors  []string `json:"errors,omitempty"`
}

// ErrBusy means another runner on this machine holds the lock.
var ErrBusy = errors.New("jobs are already running on this machine")

// Specs returns the jobs the working manifest needs from blobs in the local
// cache, filtered by o.
func (r *Runner) Specs(o Options) ([]Spec, *manifest.Manifest, error) {
	m, _, _, err := r.Working.Load()
	if err != nil {
		return nil, nil, err
	}
	reg := r.Recipes
	if reg == nil {
		reg = recipes.All()
	}
	all := Desired(m, reg, r.localType)
	var out []Spec
	for _, s := range all {
		if o.Item != "" && string(s.Item) != o.Item && s.Slug != o.Item {
			continue
		}
		if o.Recipe != "" && s.Recipe.Name != o.Recipe {
			continue
		}
		out = append(out, s)
	}
	return out, m, nil
}

func (r *Runner) localType(sum string) (media.Type, bool) {
	b, ok := r.Blobs.Blob(sum)
	if !ok {
		return media.Type{}, false
	}
	return ByExt(b.Ext())
}

// Run brings every needed job to done: cached here, receipt in the bucket,
// or run now. Facts from done jobs fill the items' empty facts.
func (r *Runner) Run(ctx context.Context, o Options) (Report, error) {
	var rep Report
	unlock, err := lockFile(filepath.Join(r.StateDir, "jobs.lock"))
	if err != nil {
		return rep, err
	}
	defer unlock()

	specs, _, err := r.Specs(o)
	if err != nil {
		return rep, err
	}
	progress := func(key string) {
		if o.Progress == nil {
			return
		}
		if j, err := r.DB.Get(key); err == nil {
			o.Progress(j)
		}
	}

	var remote map[string]gcs.Object
	facts := map[manifest.Ulid]map[string]any{}
	owned := map[manifest.Ulid]map[string]bool{}
	addFacts := func(s Spec, f map[string]any) {
		if len(f) == 0 {
			return
		}
		if facts[s.Item] == nil {
			facts[s.Item], owned[s.Item] = map[string]any{}, map[string]bool{}
		}
		for _, k := range s.Recipe.Facts {
			if v, ok := f[k]; ok {
				facts[s.Item][k] = v
				owned[s.Item][k] = true
			}
		}
	}

	var queue []Spec
	for _, s := range specs {
		if err := r.DB.ensure(s); err != nil {
			return rep, err
		}
		// A cached job's receipt carries its facts, so even a refresh
		// needn't rerun it.
		if d, ok := r.Derived.Lookup(s.Key); ok {
			rep.Cached++
			addFacts(s, d.Receipt().Facts)
			if err := r.DB.set(s.Key, Done, nil, false); err != nil {
				return rep, err
			}
			progress(s.Key)
			continue
		}
		if remote == nil && r.Remote != nil {
			if remote, err = r.Remote(ctx); err != nil {
				return rep, fmt.Errorf("checking the bucket for finished jobs: %w", err)
			}
		}
		// A remote receipt means another machine ran it. Facts recipes
		// still run here when asked to refresh.
		if _, ok := remote[s.Key]; ok && !(o.RefreshFacts && len(s.Recipe.Facts) > 0) {
			rep.Remote++
			if err := r.DB.set(s.Key, Done, nil, false); err != nil {
				return rep, err
			}
			progress(s.Key)
			continue
		}
		if j, err := r.DB.Get(s.Key); err == nil && j.State == Failed && !o.Failed {
			rep.Skipped++
			continue
		}
		queue = append(queue, s)
	}

	workers := r.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	capFF := r.FFmpeg
	if capFF <= 0 {
		capFF = 2
	}
	ff := make(chan struct{}, capFF)
	slots := make(chan struct{}, workers)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range queue {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(s Spec) {
			defer wg.Done()
			defer func() { <-slots }()
			if s.Recipe.FFmpeg {
				ff <- struct{}{}
				defer func() { <-ff }()
			}
			if ctx.Err() != nil {
				return
			}
			_ = r.DB.set(s.Key, Running, nil, true)
			progress(s.Key)
			d, err := r.runOne(ctx, s)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil && ctx.Err() != nil:
				_ = r.DB.set(s.Key, Pending, nil, false)
			case err != nil:
				msg := err.Error()
				var te *sanitize.ToolError
				if errors.As(err, &te) {
					msg = te.Error()
				}
				_ = r.DB.set(s.Key, Failed, &msg, false)
				rep.Failed++
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s on %s: %s", s.Recipe.ID(), s.Slug, firstLine(msg)))
			default:
				_ = r.DB.set(s.Key, Done, nil, false)
				rep.Ran++
				addFacts(s, d.Receipt().Facts)
			}
			progress(s.Key)
		}(s)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	n, err := r.fill(facts, owned, o.RefreshFacts)
	rep.Facts = n
	sort.Strings(rep.Errors)
	return rep, err
}

// runOne runs a recipe into scratch, verifies every media output, and
// commits the job dir with its receipt.
func (r *Runner) runOne(ctx context.Context, s Spec) (derived.Done, error) {
	b, ok := r.Blobs.Blob(s.Source)
	if !ok {
		return derived.Done{}, fmt.Errorf("blob sha256:%s isn't in the local cache", s.Source)
	}
	scratch, err := r.Derived.TempDir()
	if err != nil {
		return derived.Done{}, err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(scratch)
		}
	}()
	// Tools read the input by extension, so link the blob in under its own.
	in := filepath.Join(scratch, "in-source."+b.Ext())
	if err := os.Symlink(b.Path(), in); err != nil {
		return derived.Done{}, err
	}
	src := recipes.Source{Path: in, Type: s.Type}
	res, err := s.Recipe.Run(ctx, r.Tools, src, scratch)
	if err != nil {
		return derived.Done{}, err
	}
	os.Remove(in)
	var outs []derived.Output
	for _, f := range res.Files {
		if f.Type == nil {
			o, err := derived.JSON(scratch, f.Name, f.JSON)
			if err != nil {
				return derived.Done{}, err
			}
			outs = append(outs, o)
			continue
		}
		v, err := r.Verifier.Verify(ctx, *f.Type, filepath.Join(scratch, f.Name))
		if err != nil {
			return derived.Done{}, fmt.Errorf("%s: %w", f.Name, err)
		}
		outs = append(outs, derived.Media(f.Name, v))
	}
	rc := derived.Receipt{Jobkey: s.Key, Recipe: s.Recipe.ID(), Source: "sha256:" + s.Source, Facts: res.Facts}
	d, err := r.Derived.Commit(scratch, rc, outs)
	if err == nil {
		keep = true // it's the job dir now
	}
	return d, err
}

// fill writes facts into items: only where empty, unless refresh (D48).
// Values that are already equal change nothing, so a rerun writes nothing.
func (r *Runner) fill(facts map[manifest.Ulid]map[string]any, owned map[manifest.Ulid]map[string]bool, refresh bool) (int, error) {
	if len(facts) == 0 {
		return 0, nil
	}
	m, _, _, err := r.Working.Load()
	if err != nil {
		return 0, err
	}
	if !needsFill(m, facts, refresh) {
		return 0, nil
	}
	n := 0
	_, err = r.Working.Update(func(m *manifest.Manifest) error {
		n = 0
		for i := range m.Items {
			it := &m.Items[i]
			f, ok := facts[it.ID]
			if !ok {
				continue
			}
			changed := false
			for k, v := range f {
				if !owned[it.ID][k] {
					continue
				}
				cur, has := it.Facts[k]
				if has && (!refresh || reflect.DeepEqual(cur, v)) {
					continue
				}
				if it.Facts == nil {
					it.Facts = manifest.ItemFacts{}
				}
				it.Facts[k] = v
				changed = true
			}
			if changed {
				n++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err // nothing was written
	}
	return n, nil
}

func needsFill(m *manifest.Manifest, facts map[manifest.Ulid]map[string]any, refresh bool) bool {
	for _, it := range m.Items {
		for k, v := range facts[it.ID] {
			cur, has := it.Facts[k]
			if !has || (refresh && !reflect.DeepEqual(cur, v)) {
				return true
			}
		}
	}
	return false
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

func lockFile(p string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
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
