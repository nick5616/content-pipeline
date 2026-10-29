package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	validator "github.com/pb33f/libopenapi-validator"

	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/internal/testenv"
)

type engineHarness struct {
	t   *testing.T
	h   http.Handler
	v   validator.Validator
	env *testenv.Env
	mem *gcs.Mem
	e   *Engine
}

func newEngineHarness(t *testing.T, fixtures ...string) *engineHarness {
	t.Helper()
	mem := testenv.Mem()
	env := testenv.Seeded(t, mem, fixtures...)
	var cfg config.Config
	cfg.Serve.Port = 4747
	cfg.PrivateBucket, cfg.ArchiveBucket = testenv.Private, testenv.Archive
	e := &Engine{Config: cfg, Working: env.Working, Blobs: env.Blobs, StateDir: env.StateDir, Now: testenv.Clock,
		GCS: func(context.Context) (gcs.API, error) { return mem, nil }}
	v, errs := validator.NewValidator(loadSpec(t))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return &engineHarness{t: t, h: NewServer(cfg, token, builtUI, WithEngine(e)), v: v, env: env, mem: mem, e: e}
}

// call sends a request, validates the request (when it's meant to be valid)
// and always the response against openapi.yaml, and decodes the body.
func (x *engineHarness) call(method, path, body string, hdr map[string]string, want int, out any) http.Header {
	x.t.Helper()
	mk := func() *http.Request {
		req := httptest.NewRequest(method, "http://127.0.0.1:4747"+path, strings.NewReader(body))
		req.Host = "127.0.0.1:4747"
		req.Header.Set("Authorization", "Bearer "+token)
		switch {
		case method == "PATCH":
			req.Header.Set("Content-Type", "application/merge-patch+json")
		case body != "":
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		return req
	}
	if want < 400 {
		if ok, errs := x.v.ValidateHttpRequest(mk()); !ok {
			x.t.Fatalf("request %s %s doesn't match openapi.yaml:\n%s", method, path, describe(errs))
		}
	}
	resp, b := serve(x.t, x.h, x.v, mk())
	if resp.StatusCode != want {
		x.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			x.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.Header
}

func match(etag string) map[string]string { return map[string]string{"If-Match": etag} }

func TestItemsAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png")

	var list struct {
		Revision string            `json:"revision"`
		Items    []json.RawMessage `json:"items"`
	}
	hdr := x.call("GET", "/api/items?kind=artwork", "", nil, 200, &list)
	if len(list.Items) != 1 || hdr.Get("ETag") != list.Revision {
		t.Fatalf("list: %+v, etag %q", list, hdr.Get("ETag"))
	}
	rev := list.Revision

	var it struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	hdr = x.call("POST", "/api/items", `{"slug":"rpg-quests","kind":"project","title":"RPG Quests"}`, match(rev), 201, &it)
	if hdr.Get("Location") != "/api/items/"+it.ID {
		t.Fatalf("location %q", hdr.Get("Location"))
	}
	stale := rev
	rev = hdr.Get("ETag")

	x.call("PATCH", "/api/items/rpg-quests", `{"title":"Stale"}`, match(stale), 412, nil)
	hdr = x.call("PATCH", "/api/items/rpg-quests", `{"title":"RPG Quests!"}`, match(rev), 200, &it)
	if it.Title != "RPG Quests!" {
		t.Fatalf("%+v", it)
	}
	rev = hdr.Get("ETag")
	x.call("PATCH", "/api/items/rpg-quests", `{"tags":["not-declared"]}`, match(rev), 422, nil)
	x.call("GET", "/api/items/"+it.ID, "", nil, 200, nil)
	x.call("GET", "/api/items/nope", "", nil, 404, nil)

	var tax struct {
		Tags []json.RawMessage `json:"tags"`
	}
	hdr = x.call("PUT", "/api/taxonomy/tags/journaling", `{"slug":"journaling","label":"Journaling"}`, match(rev), 200, &tax)
	if len(tax.Tags) != 1 {
		t.Fatalf("%+v", tax)
	}
	rev = hdr.Get("ETag")
	x.call("GET", "/api/taxonomy", "", nil, 200, nil)

	x.call("DELETE", "/api/items/rpg-quests", "", match(rev), 204, nil)
	x.call("DELETE", "/api/items/rpg-quests", "", map[string]string{"If-Match": `"0000000000000000"`}, 412, nil)
}

func TestPlanApplyAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png", "jpeg.jpg")

	var st struct {
		State string `json:"state"`
	}
	x.call("GET", "/api/sync/status", "", nil, 200, &st)
	if st.State != "edited" {
		t.Fatalf("state %s", st.State)
	}

	var p struct {
		ID      string `json:"id"`
		Summary struct{ Archive, Upload int }
		Ops     []json.RawMessage `json:"ops"`
	}
	hdr := x.call("POST", "/api/plans", "", nil, 201, &p)
	if hdr.Get("Location") != "/api/plans/"+p.ID || p.Summary.Upload != 2 || p.Summary.Archive != 2 {
		t.Fatalf("plan %+v", p)
	}
	x.call("GET", "/api/plans/"+p.ID, "", nil, 200, nil)
	x.call("GET", "/api/plans/01JB7Z00000000000000000000", "", nil, 404, nil)
	x.call("GET", "/api/plans/"+p.ID+"/apply", "", nil, 404, nil)

	var as struct {
		State string `json:"state"`
		Done  int    `json:"done"`
		Total int    `json:"total"`
	}
	x.call("POST", "/api/plans/"+p.ID+"/apply", "", nil, 202, &as)
	deadline := time.Now().Add(10 * time.Second)
	for as.State != "done" {
		if as.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("apply: %+v", as)
		}
		time.Sleep(10 * time.Millisecond)
		x.call("GET", "/api/plans/"+p.ID+"/apply", "", nil, 200, &as)
	}
	if as.Done != as.Total || as.Total != len(p.Ops) {
		t.Fatalf("%+v vs %d ops", as, len(p.Ops))
	}

	x.call("GET", "/api/sync/status", "", nil, 200, &st)
	if st.State != "in-sync" {
		t.Fatalf("after apply: %s", st.State)
	}
	x.call("POST", "/api/plans", "", nil, 201, &p)
	if len(p.Ops) != 0 {
		t.Fatalf("second plan not empty: %d ops", len(p.Ops))
	}
	x.call("POST", "/api/sync/pull", "", nil, 200, nil)
}

func TestApplyAPIConflicts(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	var p struct{ ID string }
	x.call("POST", "/api/plans", "", nil, 201, &p)
	b, _ := json.Marshal(map[string]any{})
	x.mem.Put(testenv.Private, store.ManifestObject, b, "") // another machine applied
	x.call("POST", "/api/plans/"+p.ID+"/apply", "", nil, 409, nil)

	// Remote moved and local edits: diverged, both for plan and pull.
	x.call("POST", "/api/plans", "", nil, 409, nil)
	x.call("POST", "/api/sync/pull", "", nil, 409, nil)
}

func TestEngineOffline(t *testing.T) {
	x := newEngineHarness(t)
	x.mem.Fault = func(gcs.Call) error { return fmt.Errorf("%w: dial tcp", gcs.ErrOffline) }
	var st struct{ State string }
	x.call("GET", "/api/sync/status", "", nil, 200, &st)
	if st.State != "offline" {
		t.Fatalf("%+v", st)
	}
	x.call("POST", "/api/plans", "", nil, 503, nil)
	x.call("POST", "/api/sync/pull", "", nil, 503, nil)
	// Editing still works offline.
	var list struct{ Revision string }
	x.call("GET", "/api/items", "", nil, 200, &list)
	x.call("POST", "/api/items", `{"slug":"offline","kind":"writing","title":"Offline"}`, match(list.Revision), 201, nil)
}

func TestEngineWithoutBuckets(t *testing.T) {
	var cfg config.Config
	cfg.Serve.Port = 4747
	env := testenv.New(t, testenv.Mem())
	e := &Engine{Config: cfg, Working: env.Working, Blobs: env.Blobs, StateDir: env.StateDir,
		GCS: func(context.Context) (gcs.API, error) { return nil, errors.New("must not be called") }}
	v, _ := validator.NewValidator(loadSpec(t))
	x := &engineHarness{t: t, h: NewServer(cfg, token, builtUI, WithEngine(e)), v: v}
	x.call("POST", "/api/plans", "", nil, 503, nil)
	x.call("GET", "/api/items", "", nil, 200, nil)
}

// Every route answers 503, not 404, when serve has no engine.
func TestEngineRoutesWithoutEngine(t *testing.T) {
	var cfg config.Config
	cfg.Serve.Port = 4747
	h := NewServer(cfg, token, builtUI)
	for _, path := range []string{"/api/items", "/api/sync/status", "/api/taxonomy"} {
		w := do(h, "GET", "127.0.0.1:4747", path, bearer)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}
