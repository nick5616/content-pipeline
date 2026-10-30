package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/ingest"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/recipes"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/internal/testmedia"
)

// fake recipes: no tools, JSON outputs only, counting their runs.
type counter struct{ n atomic.Int32 }

func jsonRecipe(name string, c *counter, facts map[string]any, fail error) recipes.Recipe {
	r := recipes.Recipe{
		Name: name, Version: 1, Params: map[string]any{"n": 1},
		Applies: func(s recipes.Source) bool { return s.Type.Kind == media.Image },
		Run: func(ctx context.Context, _ recipes.Tools, s recipes.Source, scratch string) (recipes.Result, error) {
			c.n.Add(1)
			if fail != nil {
				return recipes.Result{}, fail
			}
			// Leave an intermediate behind: Commit must drop it.
			_ = os.WriteFile(filepath.Join(scratch, "tmp.bin"), []byte("x"), 0o600)
			return recipes.Result{Files: []recipes.File{{Name: "out.json", JSON: map[string]string{"from": filepath.Base(s.Path)}}}, Facts: facts}, nil
		},
	}
	for k := range facts {
		r.Facts = append(r.Facts, k)
	}
	return r
}

type env struct {
	r    *Runner
	w    *store.Working
	slug string
}

func newEnv(t *testing.T, reg ...recipes.Recipe) env {
	t.Helper()
	cache, state := t.TempDir(), t.TempDir()
	blobs, err := blobstore.Open(cache)
	if err != nil {
		t.Fatal(err)
	}
	der, err := derived.Open(cache)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	w := store.Open(state, "Test Person", surfaces.MustDefault())
	in := ingest.New(blobs, w, 64<<20)
	o, err := in.StagePath(filepath.Join(testmedia.Dir(t), "png.png"))
	if err != nil {
		t.Fatal(err)
	}
	res := in.Process(context.Background(), o, ingest.Options{}, nil)
	if res.Stage != ingest.Done {
		t.Fatalf("ingest: %v", res.Error)
	}
	return env{&Runner{DB: db, Derived: der, Blobs: blobs, Working: w, Recipes: reg, StateDir: state, Workers: 4}, w, res.Slug}
}

func (e env) item(t *testing.T) manifest.Item {
	m, _, _, err := e.w.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range m.Items {
		if string(it.Slug) == e.slug {
			return it
		}
	}
	t.Fatal("item gone")
	return manifest.Item{}
}

func TestKey(t *testing.T) {
	r := recipes.Recipe{Name: "thumb", Version: 1, Params: map[string]any{"a": 1, "b": []int{2, 3}}}
	same := recipes.Recipe{Name: "thumb", Version: 1, Params: map[string]any{"b": []int{2, 3}, "a": 1}}
	src := strings.Repeat("a", 64)
	k := Key(src, r)
	if k != Key(src, same) {
		t.Error("param order changed the key")
	}
	for name, other := range map[string]string{
		"source":  Key(strings.Repeat("b", 64), r),
		"version": Key(src, recipes.Recipe{Name: "thumb", Version: 2, Params: r.Params}),
		"params":  Key(src, recipes.Recipe{Name: "thumb", Version: 1, Params: map[string]any{"a": 2, "b": []int{2, 3}}}),
		"name":    Key(src, recipes.Recipe{Name: "lqip", Version: 1, Params: r.Params}),
	} {
		if other == k {
			t.Errorf("changing the %s kept the key", name)
		}
	}
	if len(k) != 64 {
		t.Errorf("key %q", k)
	}
}

func TestRegistryKeysAreStable(t *testing.T) {
	// Every real recipe's params encode, and its key doesn't depend on map
	// iteration order.
	src := strings.Repeat("c", 64)
	for _, r := range recipes.All() {
		k := Key(src, r)
		for i := 0; i < 20; i++ {
			if Key(src, r) != k {
				t.Fatalf("%s: key changed between calls", r.ID())
			}
		}
	}
}

func TestRunThenRerun(t *testing.T) {
	var c counter
	e := newEnv(t, jsonRecipe("fake", &c, nil, nil))
	ctx := context.Background()
	rep, err := e.r.Run(ctx, Options{})
	if err != nil || rep.Ran != 1 || c.n.Load() != 1 {
		t.Fatalf("first run: %+v %v (ran %d)", rep, err, c.n.Load())
	}
	specs, _, _ := e.r.Specs(Options{})
	d, ok := e.r.Derived.Lookup(specs[0].Key)
	if !ok {
		t.Fatal("no committed job dir")
	}
	if fs := d.Receipt().Files; len(fs) != 1 || fs[0].Name != "out.json" {
		t.Errorf("receipt files: %+v", fs)
	}
	if _, err := os.Stat(d.Path("tmp.bin")); err == nil {
		t.Error("an intermediate survived the commit")
	}
	rep, err = e.r.Run(ctx, Options{})
	if err != nil || rep.Ran != 0 || rep.Cached != 1 || c.n.Load() != 1 {
		t.Fatalf("rerun: %+v %v", rep, err)
	}
	j, err := e.r.DB.Get(specs[0].Key)
	if err != nil || j.State != Done || j.Attempts != 1 {
		t.Errorf("db: %+v %v", j, err)
	}
}

func TestRemoteReceiptSkips(t *testing.T) {
	var c counter
	e := newEnv(t, jsonRecipe("fake", &c, nil, nil))
	specs, _, _ := e.r.Specs(Options{})
	mem := gcs.NewMem("priv")
	mem.Put("priv", ReceiptPath(specs[0].Key), []byte("{}"), "x")
	e.r.Remote = func(ctx context.Context) (map[string]gcs.Object, error) { return Receipts(ctx, mem, "priv") }
	rep, err := e.r.Run(context.Background(), Options{})
	if err != nil || rep.Remote != 1 || c.n.Load() != 0 {
		t.Fatalf("%+v %v (ran %d)", rep, err, c.n.Load())
	}
}

func TestFailureAndRetry(t *testing.T) {
	var c counter
	boom := &os.PathError{Op: "vips", Path: "x", Err: errors.New("no such file")}
	e := newEnv(t, jsonRecipe("fake", &c, nil, boom))
	ctx := context.Background()
	rep, err := e.r.Run(ctx, Options{})
	if err != nil || rep.Failed != 1 || len(rep.Errors) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	specs, _, _ := e.r.Specs(Options{})
	if _, ok := e.r.Derived.Lookup(specs[0].Key); ok {
		t.Error("a failed job left outputs")
	}
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(e.r.Blobs.BlobPath(strings.Repeat("0", 64), "x")), "..", "..", "..", "..", "derived", "tmp"))
	if len(entries) != 0 {
		t.Errorf("scratch left behind: %d entries", len(entries))
	}
	// Plan's run doesn't retry it; canon jobs run does.
	if rep, _ := e.r.Run(ctx, Options{}); rep.Skipped != 1 || c.n.Load() != 1 {
		t.Errorf("plan rerun: %+v", rep)
	}
	if rep, _ := e.r.Run(ctx, Options{Failed: true}); rep.Failed != 1 || c.n.Load() != 2 {
		t.Errorf("jobs run: %+v", rep)
	}
	j, err := e.r.DB.Retry(specs[0].Key)
	if err != nil || j.State != Pending || j.Attempts != 2 || j.LastError == nil {
		t.Errorf("retry: %+v %v", j, err)
	}
	if _, err := e.r.DB.Retry(strings.Repeat("f", 64)); !errors.Is(err, ErrNotFound) {
		t.Errorf("retry unknown: %v", err)
	}
}

func TestFactsFillOnlyEmpty(t *testing.T) {
	var c counter
	e := newEnv(t, jsonRecipe("fake", &c, map[string]any{"palette": []any{"#112233"}}, nil))
	ctx := context.Background()
	// A hand-set palette stays.
	if _, err := e.w.Update(func(m *manifest.Manifest) error {
		for i := range m.Items {
			m.Items[i].Facts["palette"] = []any{"#abcdef"}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, rev, _, _ := e.w.Load()
	rep, err := e.r.Run(ctx, Options{})
	if err != nil || rep.Facts != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, rev2, _, _ := e.w.Load(); rev2 != rev {
		t.Error("filling facts touched a set value")
	}
	// --refresh-facts overwrites it, from the cached receipt.
	rep, err = e.r.Run(ctx, Options{RefreshFacts: true})
	if err != nil || rep.Facts != 1 || c.n.Load() != 1 {
		t.Fatalf("refresh: %+v %v", rep, err)
	}
	if p := e.item(t).Facts["palette"].([]any); p[0] != "#112233" {
		t.Errorf("palette %v", p)
	}
	// Cleared, it comes back on the next run without rerunning.
	if _, err := e.w.Update(func(m *manifest.Manifest) error {
		for i := range m.Items {
			delete(m.Items[i].Facts, "palette")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := e.r.Run(ctx, Options{}); rep.Facts != 1 || c.n.Load() != 1 {
		t.Errorf("refill: %+v", rep)
	}
}

func TestFFmpegCap(t *testing.T) {
	var running, peak atomic.Int32
	var mu sync.Mutex
	mk := func(name string) recipes.Recipe {
		return recipes.Recipe{
			Name: name, Version: 1, Params: map[string]any{}, FFmpeg: true,
			Applies: func(recipes.Source) bool { return true },
			Run: func(ctx context.Context, _ recipes.Tools, _ recipes.Source, scratch string) (recipes.Result, error) {
				n := running.Add(1)
				mu.Lock()
				if n > peak.Load() {
					peak.Store(n)
				}
				mu.Unlock()
				time.Sleep(150 * time.Millisecond)
				running.Add(-1)
				return recipes.Result{}, nil
			},
		}
	}
	e := newEnv(t, mk("a"), mk("b"), mk("c"), mk("d"), mk("e"))
	e.r.Workers = 8
	rep, err := e.r.Run(context.Background(), Options{})
	if err != nil || rep.Ran != 5 {
		t.Fatalf("%+v %v", rep, err)
	}
	if p := peak.Load(); p > 2 || p < 2 {
		t.Errorf("peak concurrent ffmpeg jobs %d, want 2", p)
	}
}

func TestOneRunnerAtATime(t *testing.T) {
	var c counter
	e := newEnv(t, jsonRecipe("fake", &c, nil, nil))
	unlock, err := lockFile(filepath.Join(e.r.StateDir, "jobs.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := e.r.Run(context.Background(), Options{}); !errors.Is(err, ErrBusy) {
		t.Errorf("got %v, want ErrBusy", err)
	}
}

func TestCancelLeavesPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := recipes.Recipe{
		Name: "slow", Version: 1, Params: map[string]any{},
		Applies: func(recipes.Source) bool { return true },
		Run: func(ctx context.Context, _ recipes.Tools, _ recipes.Source, scratch string) (recipes.Result, error) {
			cancel()
			<-ctx.Done()
			return recipes.Result{}, ctx.Err()
		},
	}
	e := newEnv(t, r)
	if _, err := e.r.Run(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	specs, _, _ := e.r.Specs(Options{})
	if j, _ := e.r.DB.Get(specs[0].Key); j.State != Pending {
		t.Errorf("cancelled job is %s, want pending", j.State)
	}
}

func TestDesiredDedupesAndFirstImage(t *testing.T) {
	img := func(id, slug string, blobs ...string) manifest.Item {
		it := manifest.Item{ID: manifest.Ulid(id), Slug: manifest.Slug(slug), Kind: manifest.KindArtwork}
		for _, b := range blobs {
			it.Assets = append(it.Assets, manifest.AssetRef{Blob: manifest.BlobRef("sha256:" + b), Role: manifest.AssetRoleOriginal})
		}
		return it
	}
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	m := &manifest.Manifest{Items: []manifest.Item{img("01A", "one", a, b), img("01B", "two", a)}}
	typeOf := func(string) (media.Type, bool) { return media.PNG, true }
	var names []string
	for _, s := range Desired(m, recipes.All(), typeOf) {
		names = append(names, s.Recipe.Name+":"+s.Source[:1]+":"+s.Slug)
	}
	want := "lqip:a:one,palette:a:one,thumb:a:one,lqip:b:one,thumb:b:one"
	if strings.Join(names, ",") != want {
		t.Errorf("got %s\nwant %s", strings.Join(names, ","), want)
	}
}

// A fact the item's kind doesn't allow: the update is refused, nothing is
// written, and the report doesn't claim it filled anything.
func TestFactsThatDontValidate(t *testing.T) {
	var c counter
	e := newEnv(t, jsonRecipe("fake", &c, map[string]any{"bpm": 120.0}, nil)) // png is an artwork
	_, rev, _, _ := e.w.Load()
	rep, err := e.r.Run(context.Background(), Options{})
	if err == nil || rep.Facts != 0 {
		t.Fatalf("got %+v, %v; want an error and no facts", rep, err)
	}
	if _, rev2, _, _ := e.w.Load(); rev2 != rev {
		t.Error("an invalid fill was written")
	}
}
