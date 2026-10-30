// Package testenv builds a throwaway Canon environment for tests: a cache,
// a state dir, a working manifest, fake buckets (or real ones on
// fake-gcs-server), and an ingester for the synthetic media fixtures.
package testenv

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nick5616/canon/engine/apply"
	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/ingest"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/plan"
	"github.com/nick5616/canon/engine/recipes"
	"github.com/nick5616/canon/engine/sanitize"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/internal/testmedia"
)

// Bucket names used by every test environment.
const (
	Private = "canon-test-private"
	Archive = "canon-test-archive"
	Public  = "canon-test-public"
	// PublicBase is where views link the public bucket.
	PublicBase = "https://storage.googleapis.com/" + Public
)

// Clock is the fixed time every test plan is made at, so history names are
// stable.
var Clock = func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }

// Env is one machine's worth of Canon state.
type Env struct {
	T        *testing.T
	API      gcs.API
	StateDir string
	CacheDir string
	Blobs    *blobstore.Store
	Working  *store.Working
	Ingester *ingest.Ingester
	// Derived and Jobs are set by WithJobs.
	Derived *derived.Store
	Jobs    *jobs.DB
	// Publish is set by WithPublic.
	Publish bool
}

// WithPublic turns on publishing (M5) to the Public bucket, and jobs.
func (e *Env) WithPublic() *Env {
	if e.Jobs == nil {
		e.WithJobs()
	}
	e.Publish = true
	return e
}

// WithJobs turns on derived jobs (M4): plans run them first, applies
// upload them. Jobs run with the real tools.
func (e *Env) WithJobs() *Env {
	e.T.Helper()
	var err error
	if e.Derived, err = derived.Open(e.CacheDir); err != nil {
		e.T.Fatal(err)
	}
	if e.Jobs, err = jobs.OpenDB(e.StateDir); err != nil {
		e.T.Fatal(err)
	}
	e.T.Cleanup(func() { e.Jobs.Close() })
	return e
}

// Runner is the job runner for this env, checking its bucket for receipts.
func (e *Env) Runner() *jobs.Runner {
	return &jobs.Runner{DB: e.Jobs, Derived: e.Derived, Blobs: e.Blobs, Working: e.Working,
		Tools: recipes.NewTools(), Verifier: sanitize.NewVerifier(), StateDir: e.StateDir,
		Remote: func(ctx context.Context) (map[string]gcs.Object, error) { return jobs.Receipts(ctx, e.API, Private) }}
}

// New makes an environment on api (a *gcs.Mem, or a real client).
func New(t *testing.T, api gcs.API) *Env {
	t.Helper()
	e := &Env{T: t, API: api, StateDir: t.TempDir(), CacheDir: t.TempDir()}
	var err error
	if e.Blobs, err = blobstore.Open(e.CacheDir); err != nil {
		t.Fatal(err)
	}
	e.Working = store.Open(e.StateDir, "Test Person", surfaces.MustDefault())
	e.Ingester = ingest.New(e.Blobs, e.Working, 1<<30)
	return e
}

// Mem returns a fake with the test buckets.
func Mem() *gcs.Mem { return gcs.NewMem(Private, Archive, Public) }

// Remote is the private bucket as the sync code sees it.
func (e *Env) Remote() store.Remote { return store.Remote{API: e.API, Bucket: Private} }

// PlanEnv is what plan.Make needs.
func (e *Env) PlanEnv() plan.Env {
	env := plan.Env{Working: e.Working, Blobs: e.Blobs, Remote: e.Remote(), ArchiveBucket: Archive, Now: Clock}
	if e.Jobs != nil {
		env.Derived = e.Derived
		env.RunJobs = func(ctx context.Context) error {
			rep, err := e.Runner().Run(ctx, jobs.Options{})
			if err == nil && rep.Failed > 0 {
				err = fmt.Errorf("jobs failed: %v", rep.Errors)
			}
			return err
		}
	}
	if e.Publish {
		env.PublicBucket, env.PublicBase = Public, PublicBase
	}
	return env
}

// Deps is what apply.Apply needs.
func (e *Env) Deps() apply.Deps {
	return apply.Deps{API: e.API, Private: Private, Archive: Archive, Working: e.Working, Blobs: e.Blobs, Derived: e.Derived, Public: publicIf(e.Publish), StateDir: e.StateDir}
}

func publicIf(on bool) string {
	if on {
		return Public
	}
	return ""
}

// seeds caches ingested state by fixture list: ingest runs real tools and
// is slow, and many tests start from the same few files.
var seeds sync.Map // key -> *seed

type seed struct {
	once         sync.Once
	cache, state string
	err          error
}

// Seeded is New plus the named fixtures already ingested. Each fixture set
// is ingested once per test binary and copied in, so every Seeded
// environment with the same names starts byte-identical (same item ids).
func Seeded(t *testing.T, api gcs.API, names ...string) *Env {
	t.Helper()
	v, _ := seeds.LoadOrStore(strings.Join(names, "\x00"), &seed{})
	s := v.(*seed)
	s.once.Do(func() {
		s.cache, s.err = os.MkdirTemp("", "canon-seed-cache-")
		if s.err != nil {
			return
		}
		s.state, s.err = os.MkdirTemp("", "canon-seed-state-")
		if s.err != nil {
			return
		}
		e := &Env{T: t, CacheDir: s.cache, StateDir: s.state}
		if e.Blobs, s.err = blobstore.Open(s.cache); s.err != nil {
			return
		}
		e.Working = store.Open(s.state, "Test Person", surfaces.MustDefault())
		e.Ingester = ingest.New(e.Blobs, e.Working, 1<<30)
		e.Ingest(names...)
	})
	if s.err != nil {
		t.Fatal(s.err)
	}
	e := New(t, api)
	for src, dst := range map[string]string{s.cache: e.CacheDir, s.state: e.StateDir} {
		if err := copyTree(src, dst); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
}

// Ingest runs the named media fixtures (relative to the fixtures dir)
// through ingest and fails the test if any doesn't make it.
func (e *Env) Ingest(names ...string) {
	e.T.Helper()
	dir := testmedia.Dir(e.T)
	for _, n := range names {
		o, err := e.Ingester.StagePath(filepath.Join(dir, n))
		if err != nil {
			e.T.Fatal(err)
		}
		r := e.Ingester.Process(context.Background(), o, ingest.Options{}, nil)
		if r.Stage != ingest.Done {
			e.T.Fatalf("ingest %s: %v", n, r.Error)
		}
	}
}

// Plan makes a plan and fails the test on error.
func (e *Env) Plan() *plan.Plan {
	e.T.Helper()
	p, err := plan.Make(context.Background(), e.PlanEnv())
	if err != nil {
		e.T.Fatalf("plan: %v", err)
	}
	return p
}

// Apply applies p and fails the test on error.
func (e *Env) Apply(p *plan.Plan) apply.Status {
	e.T.Helper()
	st, err := apply.Apply(context.Background(), p, e.Deps(), nil)
	if err != nil {
		e.T.Fatalf("apply: %v", err)
	}
	return st
}

// Converge plans and applies, then asserts the next plan writes nothing:
// the idempotency test, run at the end of every scenario.
func (e *Env) Converge() {
	e.T.Helper()
	if p := e.Plan(); !p.Empty() {
		e.Apply(p)
	}
	e.AssertEmpty()
}

// AssertEmpty asserts that a fresh plan writes nothing.
func (e *Env) AssertEmpty() {
	e.T.Helper()
	if p := e.Plan(); !p.Empty() {
		e.T.Fatalf("second plan is not empty: %+v", p.Ops)
	}
}

// Cleanup removes the seed directories. Call it from TestMain after m.Run.
func Cleanup() {
	seeds.Range(func(_, v any) bool {
		s := v.(*seed)
		os.RemoveAll(s.cache)
		os.RemoveAll(s.state)
		return true
	})
}
