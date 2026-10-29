package apply_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/apply"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/plan"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/internal/testenv"
)

// A mix of families: exiftool image, Go PNG, ffmpeg audio, and a pair that
// sanitizes to one blob (D34: both originals still get archived).
var fixtures = []string{"jpeg.jpg", "png.png", "mp3.mp3", "jpeg-named.HEIC"}

func TestApplyThenPlanIsEmpty(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, fixtures...)

	p := e.Plan()
	if p.Generation != 0 || p.Summary.Upload != 3 || p.Summary.Archive != 3 {
		t.Fatalf("first plan: gen %d, %+v", p.Generation, p.Summary)
	}
	st := e.Apply(p)
	if st.State != "done" || st.Done != st.Total || st.NewGeneration == nil {
		t.Fatalf("status %+v", st)
	}
	e.AssertEmpty()

	// The remote manifest is the working one, and base records it.
	working, _, _ := e.Working.Bytes()
	remote, gen, err := mem.Read(context.Background(), testenv.Private, store.ManifestObject)
	if err != nil || !bytes.Equal(remote, working) || gen != *st.NewGeneration {
		t.Fatalf("remote manifest differs (gen %d, err %v)", gen, err)
	}
	if base, bgen, _ := e.Working.Base(); !bytes.Equal(base, working) || bgen != gen {
		t.Fatal("base not recorded")
	}
	s, _ := e.Working.Status(context.Background(), e.Remote())
	if s.State != store.InSync {
		t.Fatalf("state %s", s.State)
	}
	assertIntegrity(t, mem)
	assertArchiveBoundary(t, e, mem)
}

// Every object carries sha256 metadata that matches its bytes.
func assertIntegrity(t *testing.T, mem *gcs.Mem) {
	t.Helper()
	for _, b := range []string{testenv.Private, testenv.Archive} {
		objs, _ := mem.List(context.Background(), b, "")
		for _, o := range objs {
			data, _, _, _ := mem.Get(b, o.Name)
			if gcs.SHA256Hex(data) != o.SHA256 {
				t.Errorf("%s/%s: sha256 metadata %q doesn't match its bytes", b, o.Name, o.SHA256)
			}
			if _, sum, _, ok := plan.ParseCAS(o.Name); ok && sum != o.SHA256 {
				t.Errorf("%s/%s: bytes don't match the name", b, o.Name)
			}
		}
	}
}

// D14: no private object holds an original's bytes, and the archive holds
// only originals.
func assertArchiveBoundary(t *testing.T, e *testenv.Env, mem *gcs.Mem) {
	t.Helper()
	origs, _ := e.Blobs.Originals()
	isOrig := map[string]bool{}
	for _, o := range origs {
		isOrig[o.SHA256()] = true
	}
	for _, name := range mem.Names(testenv.Private) {
		data, _, _, _ := mem.Get(testenv.Private, name)
		if isOrig[gcs.SHA256Hex(data)] {
			t.Errorf("private bucket received an original: %s", name)
		}
	}
	archived := mem.Names(testenv.Archive)
	if len(archived) != len(origs) {
		t.Errorf("archive has %d objects, cache has %d originals", len(archived), len(origs))
	}
	for _, name := range archived {
		data, _, _, _ := mem.Get(testenv.Archive, name)
		if !isOrig[gcs.SHA256Hex(data)] || !strings.HasPrefix(name, "originals/") {
			t.Errorf("archive holds something that isn't an original: %s", name)
		}
	}
}

func TestContentTypes(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	e.Converge()
	for _, name := range mem.Names(testenv.Private) {
		_, ct, _, _ := mem.Get(testenv.Private, name)
		want := map[bool]string{true: "image/png", false: "application/json"}[strings.HasPrefix(name, "blobs/")]
		if ct != want {
			t.Errorf("%s: content type %q, want %q", name, ct, want)
		}
	}
}

func writes(mem *gcs.Mem) int {
	n := 0
	for _, c := range mem.Calls {
		if c.Method == "write" {
			n++
		}
	}
	return n
}

func TestRemoteMovedRefuses(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	p := e.Plan()
	mem.Put(testenv.Private, store.ManifestObject, []byte("{}"), "") // another machine
	before := writes(mem)
	_, err := apply.Apply(context.Background(), p, e.Deps(), nil)
	if !errors.Is(err, apply.ErrMoved) {
		t.Fatalf("want ErrMoved, got %v", err)
	}
	if writes(mem) != before {
		t.Fatal("apply wrote something after refusing")
	}
}

func TestWorkingEditedRefuses(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	p := e.Plan()
	if _, err := e.Working.Update(func(m *manifest.Manifest) error {
		m.Items[0].Title = "Edited after planning"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := apply.Apply(context.Background(), p, e.Deps(), nil); !errors.Is(err, apply.ErrStale) {
		t.Fatalf("want ErrStale, got %v", err)
	}
	if writes(mem) != 0 {
		t.Fatal("apply wrote something after refusing")
	}
}

// cleanRun is the end state of an uninterrupted run, to compare against.
func cleanRun(t *testing.T) map[string]string {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, fixtures...)
	e.Converge()
	return snapshot(t, e, mem)
}

// snapshot maps bucket/name to the sha256 of its bytes. Item ids are random
// per ingest, so the manifest and its history are checked against this
// environment's own working manifest and then stood in for by placeholders.
func snapshot(t *testing.T, e *testenv.Env, mem *gcs.Mem) map[string]string {
	t.Helper()
	working, _, _ := e.Working.Bytes()
	out := map[string]string{}
	for _, b := range []string{testenv.Private, testenv.Archive} {
		for _, n := range mem.Names(b) {
			data, _, _, _ := mem.Get(b, n)
			switch {
			case n == store.ManifestObject || strings.HasPrefix(n, "manifest/history/"):
				if !bytes.Equal(data, working) {
					t.Fatalf("%s isn't the working manifest", n)
				}
				if n != store.ManifestObject {
					n = "manifest/history/<snapshot>"
				}
				out[b+"/"+n] = "working"
			default:
				out[b+"/"+n] = gcs.SHA256Hex(data)
			}
		}
	}
	return out
}

func sameState(t *testing.T, want, got map[string]string) {
	t.Helper()
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Fatalf("end state differs from a clean run:\nwant %v\ngot  %v", want, got)
	}
}

// Fail the Nth write for every N, then rerun: the end state is a clean
// run's, and the plan after that is empty.
func TestFailAtEveryWrite(t *testing.T) {
	want := cleanRun(t)
	for n := 1; ; n++ {
		mem := testenv.Mem()
		e := testenv.Seeded(t, mem, fixtures...)
		p := e.Plan()
		if n > p.Summary.Archive+p.Summary.Upload+2 {
			break
		}
		count := 0
		mem.Fault = func(c gcs.Call) error {
			if c.Method == "write" {
				count++
				if count == n {
					return errors.New("injected 503")
				}
			}
			return nil
		}
		st, err := apply.Apply(context.Background(), p, e.Deps(), nil)
		if err == nil || st.State != "failed" || st.Error == nil {
			t.Fatalf("n=%d: apply should fail, got %+v", n, st)
		}
		mem.Fault = nil
		e.Converge()
		sameState(t, want, snapshot(t, e, mem))
	}
}

// An upload cut off mid-stream leaves no object, and a rerun converges.
func TestCutUpload(t *testing.T) {
	want := cleanRun(t)
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, fixtures...)
	cut := false
	mem.Fault = func(c gcs.Call) error {
		if c.Method == "write" && strings.HasPrefix(c.Name, "blobs/") && !cut {
			cut = true
			return &gcs.Cut{After: 100, Err: errors.New("connection reset")}
		}
		return nil
	}
	if _, err := apply.Apply(context.Background(), e.Plan(), e.Deps(), nil); err == nil {
		t.Fatal("apply should fail")
	}
	for _, n := range mem.Names(testenv.Private) {
		if strings.HasPrefix(n, "blobs/") {
			t.Fatalf("partial upload left %s", n)
		}
	}
	mem.Fault = nil
	e.Converge()
	sameState(t, want, snapshot(t, e, mem))
}

// A crash between writing the manifest and its history leaves one op.
func TestCrashBeforeHistory(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	mem.Fault = func(c gcs.Call) error {
		if c.Method == "write" && strings.HasPrefix(c.Name, "manifest/history/") {
			return errors.New("killed")
		}
		return nil
	}
	if _, err := apply.Apply(context.Background(), e.Plan(), e.Deps(), nil); err == nil {
		t.Fatal("apply should fail")
	}
	mem.Fault = nil
	p := e.Plan()
	if len(p.Ops) != 1 || p.Ops[0].Type != plan.WriteHistory {
		t.Fatalf("want only write-history, got %+v", p.Ops)
	}
	e.Converge()
}

// A crash after the manifest write but before base is recorded: the next
// plan sees the remote equals working and pulls instead of diverging.
func TestCrashBeforeBaseRecorded(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	e.Converge()
	base, gen, _ := e.Working.Base()
	e.Ingest("jpeg.jpg")
	e.Apply(e.Plan())
	if err := e.Working.SetBase(base, gen); err != nil { // as if SetBase never ran
		t.Fatal(err)
	}
	s, _ := e.Working.Status(context.Background(), e.Remote())
	if s.State != store.Behind {
		t.Fatalf("state %s, want behind", s.State)
	}
	e.AssertEmpty()
}

func TestTransientListErrors(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	mem.Fault = func(c gcs.Call) error {
		if c.Method == "list" {
			return errors.New("googleapi: Error 503: backend error")
		}
		return nil
	}
	if _, err := plan.Make(context.Background(), e.PlanEnv()); err == nil {
		t.Fatal("plan should fail while listing fails")
	}
	mem.Fault = nil
	e.Converge()
}

func TestCanceledContext(t *testing.T) {
	want := cleanRun(t)
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, fixtures...)
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	mem.Fault = func(c gcs.Call) error {
		if c.Method == "write" {
			if n++; n == 2 {
				cancel()
			}
		}
		return ctx.Err()
	}
	if _, err := apply.Apply(ctx, e.Plan(), e.Deps(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	mem.Fault = nil
	e.Converge()
	sameState(t, want, snapshot(t, e, mem))
}

func TestCorruptCacheStopsBeforeUpload(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	p := e.Plan()
	for _, op := range p.Ops {
		if op.Type == plan.UploadBlob {
			_, sum, _, _ := plan.ParseCAS(op.Path)
			b, _ := e.Blobs.Blob(sum)
			if err := os.WriteFile(b.Path(), []byte("not the png"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err := apply.Apply(context.Background(), p, e.Deps(), nil)
	if !errors.Is(err, apply.ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
	for _, n := range mem.Names(testenv.Private) {
		if strings.HasPrefix(n, "blobs/") || n == store.ManifestObject {
			t.Fatalf("uploaded %s despite corruption", n)
		}
	}
}

// D38: a private blob with wrong checksum metadata is re-uploaded.
func TestPrivateChecksumMismatchReuploads(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	e.Converge()
	var blob string
	for _, n := range mem.Names(testenv.Private) {
		if strings.HasPrefix(n, "blobs/") {
			blob = n
		}
	}
	mem.Put(testenv.Private, blob, []byte("garbage"), "")
	p := e.Plan()
	if p.Summary.Upload != 1 || !strings.Contains(p.Ops[0].Detail, "checksum mismatch") {
		t.Fatalf("want one re-upload, got %+v", p.Ops)
	}
	e.Apply(p)
	e.AssertEmpty()
	assertIntegrity(t, mem)
}

// D38: an archive original with wrong metadata is a plan error.
func TestArchiveChecksumMismatchIsAnError(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	origs, _ := e.Blobs.Originals()
	o := origs[0]
	mem.Put(testenv.Archive, plan.OriginalPath(o.SHA256(), o.Ext()), []byte("x"), "wrong")
	_, err := plan.Make(context.Background(), e.PlanEnv())
	var inv *store.InvalidError
	if !errors.As(err, &inv) || !strings.Contains(err.Error(), "archive-checksum") {
		t.Fatalf("want archive-checksum error, got %v", err)
	}
}

func TestSecondApplyIsBusy(t *testing.T) {
	mem := testenv.Mem()
	e := testenv.Seeded(t, mem, "png.png")
	p := e.Plan()
	release := make(chan struct{})
	started := make(chan struct{})
	mem.Fault = func(c gcs.Call) error {
		if c.Method == "write" {
			select {
			case <-started:
			default:
				close(started)
				<-release
			}
		}
		return nil
	}
	done := make(chan error)
	go func() { _, err := apply.Apply(context.Background(), p, e.Deps(), nil); done <- err }()
	<-started
	if _, err := apply.Apply(context.Background(), p, e.Deps(), nil); !errors.Is(err, apply.ErrBusy) {
		t.Errorf("want ErrBusy, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
