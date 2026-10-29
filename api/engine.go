package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nick5616/canon/engine/apply"
	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/items"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/plan"
	"github.com/nick5616/canon/engine/recipes"
	"github.com/nick5616/canon/engine/store"
)

// Engine is what the sync, item, taxonomy and plan handlers need.
type Engine struct {
	Config   config.Config
	Working  *store.Working
	Blobs    *blobstore.Store
	StateDir string
	// GCS returns the client, made on first use: canon serve starts (and
	// edits work) without credentials.
	GCS func(context.Context) (gcs.API, error)
	Now func() time.Time

	// Derived and Jobs enable derived jobs (M4); nil Jobs disables them.
	Derived *derived.Store
	Jobs    *jobs.DB
	Tools   recipes.Tools
	// OnJobs, if set, sees the report of the jobs plan runs first.
	OnJobs func(jobs.Report)
}

// WithEngine enables the sync, item, taxonomy and plan endpoints.
func WithEngine(e *Engine) Option { return func(d *deps) { d.engine = e } }

// ErrNoBuckets means config lacks the buckets plan and apply need.
var ErrNoBuckets = errors.New("plan and apply need privateBucket and archiveBucket in the config file")

func (e *Engine) remote(ctx context.Context) (store.Remote, error) {
	if e.Config.PrivateBucket == "" || e.Config.ArchiveBucket == "" {
		return store.Remote{}, ErrNoBuckets
	}
	api, err := e.GCS(ctx)
	if err != nil {
		return store.Remote{}, err
	}
	return store.Remote{API: api, Bucket: e.Config.PrivateBucket}, nil
}

// PlanEnv is what plan.Make needs.
func (e *Engine) PlanEnv(ctx context.Context) (plan.Env, error) {
	r, err := e.remote(ctx)
	env := plan.Env{Working: e.Working, Blobs: e.Blobs, Remote: r, ArchiveBucket: e.Config.ArchiveBucket, Now: e.Now}
	if e.Jobs != nil {
		env.Derived, env.RunJobs, env.JobState = e.Derived, e.runJobsForPlan, e.jobState
	}
	return env, err
}

// Deps is what apply.Apply needs.
func (e *Engine) Deps(ctx context.Context) (apply.Deps, error) {
	r, err := e.remote(ctx)
	return apply.Deps{API: r.API, Private: e.Config.PrivateBucket, Archive: e.Config.ArchiveBucket,
		Working: e.Working, Blobs: e.Blobs, Derived: e.Derived, StateDir: e.StateDir}, err
}

type engineAPI struct {
	e *Engine

	mu      sync.Mutex
	applies map[string]apply.Status
}

func (a *engineAPI) routes(mux *http.ServeMux) {
	h := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if a.e == nil {
				problem(w, http.StatusServiceUnavailable, "Engine unavailable", "canon serve was started without a state dir.")
				return
			}
			f(w, r)
		}
	}
	mux.HandleFunc("GET /api/sync/status", h(a.syncStatus))
	mux.HandleFunc("POST /api/sync/pull", h(a.pull))
	mux.HandleFunc("GET /api/items", h(a.listItems))
	mux.HandleFunc("POST /api/items", h(a.createItem))
	mux.HandleFunc("GET /api/items/{id}", h(a.getItem))
	mux.HandleFunc("PATCH /api/items/{id}", h(a.updateItem))
	mux.HandleFunc("DELETE /api/items/{id}", h(a.deleteItem))
	mux.HandleFunc("GET /api/taxonomy", h(a.getTaxonomy))
	mux.HandleFunc("PUT /api/taxonomy/{vocab}/{slug}", h(a.putTerm))
	mux.HandleFunc("POST /api/plans", h(a.createPlan))
	mux.HandleFunc("GET /api/plans/{id}", h(a.getPlan))
	mux.HandleFunc("POST /api/plans/{id}/apply", h(a.applyPlan))
	mux.HandleFunc("GET /api/plans/{id}/apply", h(a.applyStatus))
}

// engineError maps engine errors onto the spec's responses.
func engineError(w http.ResponseWriter, err error) {
	var inv *store.InvalidError
	switch {
	case errors.As(err, &inv):
		problemWith(w, http.StatusUnprocessableEntity, "Invalid", "The result would not validate.", inv.Problems)
	case errors.Is(err, items.ErrBadRequest):
		problem(w, http.StatusUnprocessableEntity, "Invalid", err.Error())
	case errors.Is(err, store.ErrStale):
		problem(w, http.StatusPreconditionFailed, "Working manifest changed", err.Error())
	case errors.Is(err, items.ErrNotFound), errors.Is(err, plan.ErrNotFound):
		problem(w, http.StatusNotFound, "Not found", err.Error())
	case errors.Is(err, items.ErrPublished), errors.Is(err, store.ErrDiverged),
		errors.Is(err, apply.ErrMoved), errors.Is(err, apply.ErrStale), errors.Is(err, apply.ErrBusy):
		problem(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, gcs.ErrOffline), errors.Is(err, ErrNoBuckets):
		problem(w, http.StatusServiceUnavailable, "GCS unavailable", err.Error())
	default:
		problem(w, http.StatusInternalServerError, "Internal error", err.Error())
	}
}

func etag(rev string) string { return `"` + rev + `"` }

// ifMatch returns the revision from If-Match. The spec requires the header;
// without one the edit is refused rather than applied blind.
func ifMatch(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := r.Header.Get("If-Match")
	rev := strings.Trim(v, `"`)
	if v == "" || rev == "" {
		problem(w, http.StatusPreconditionFailed, "If-Match required", "Send the working manifest revision you edited from.")
		return "", false
	}
	return rev, true
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "Invalid", err.Error())
		return nil, false
	}
	return b, true
}

func (a *engineAPI) load(w http.ResponseWriter) (*manifest.Manifest, string, bool) {
	m, rev, _, err := a.e.Working.Load()
	if err != nil {
		engineError(w, err)
		return nil, "", false
	}
	return m, rev, true
}

type syncStatus struct {
	State            store.State `json:"state"`
	BaseGeneration   int64       `json:"baseGeneration"`
	RemoteGeneration *int64      `json:"remoteGeneration"`
	LocalEdits       bool        `json:"localEdits"`
	Revision         string      `json:"revision"`
}

func toSync(s store.Status) syncStatus {
	return syncStatus{s.State, s.BaseGeneration, s.RemoteGeneration, s.LocalEdits, etag(s.Revision)}
}

func (a *engineAPI) syncStatus(w http.ResponseWriter, r *http.Request) {
	rem, err := a.e.remote(r.Context())
	if err != nil {
		engineError(w, err)
		return
	}
	st, err := a.e.Working.Status(r.Context(), rem)
	if err != nil {
		engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSync(st))
}

func (a *engineAPI) pull(w http.ResponseWriter, r *http.Request) {
	rem, err := a.e.remote(r.Context())
	if err != nil {
		engineError(w, err)
		return
	}
	discard, _ := strconv.ParseBool(r.URL.Query().Get("discardLocal"))
	st, err := a.e.Working.Pull(r.Context(), rem, discard)
	if err != nil {
		engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSync(st))
}

func (a *engineAPI) listItems(w http.ResponseWriter, r *http.Request) {
	m, rev, ok := a.load(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	list := items.List(m, items.Filter{
		Kind: manifest.Kind(q.Get("kind")), Surface: q.Get("surface"),
		Visibility: manifest.ItemVisibility(q.Get("visibility")), Tag: q.Get("tag"), Query: q.Get("q"),
	})
	w.Header().Set("ETag", etag(rev))
	writeJSON(w, http.StatusOK, map[string]any{"revision": etag(rev), "items": list})
}

func (a *engineAPI) getItem(w http.ResponseWriter, r *http.Request) {
	m, rev, ok := a.load(w)
	if !ok {
		return
	}
	i, err := items.Find(m, r.PathValue("id"))
	if err != nil {
		engineError(w, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	writeJSON(w, http.StatusOK, m.Items[i])
}

func (a *engineAPI) createItem(w http.ResponseWriter, r *http.Request) {
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	it, newRev, err := items.New(a.e.Working, rev, body)
	if err != nil {
		engineError(w, err)
		return
	}
	w.Header().Set("ETag", etag(newRev))
	w.Header().Set("Location", "/api/items/"+string(it.ID))
	writeJSON(w, http.StatusCreated, it)
}

func (a *engineAPI) updateItem(w http.ResponseWriter, r *http.Request) {
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	it, newRev, err := items.Patch(a.e.Working, rev, r.PathValue("id"), body)
	if err != nil {
		engineError(w, err)
		return
	}
	w.Header().Set("ETag", etag(newRev))
	writeJSON(w, http.StatusOK, it)
}

func (a *engineAPI) deleteItem(w http.ResponseWriter, r *http.Request) {
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	newRev, err := items.Remove(a.e.Working, rev, r.PathValue("id"))
	if err != nil {
		engineError(w, err)
		return
	}
	w.Header().Set("ETag", etag(newRev))
	w.WriteHeader(http.StatusNoContent)
}

func (a *engineAPI) getTaxonomy(w http.ResponseWriter, r *http.Request) {
	m, rev, ok := a.load(w)
	if !ok {
		return
	}
	w.Header().Set("ETag", etag(rev))
	writeJSON(w, http.StatusOK, m.Taxonomy)
}

func (a *engineAPI) putTerm(w http.ResponseWriter, r *http.Request) {
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	tax, newRev, err := items.SetTerm(a.e.Working, rev, r.PathValue("vocab"), r.PathValue("slug"), body)
	if err != nil {
		engineError(w, err)
		return
	}
	w.Header().Set("ETag", etag(newRev))
	writeJSON(w, http.StatusOK, tax)
}

func (a *engineAPI) createPlan(w http.ResponseWriter, r *http.Request) {
	env, err := a.e.PlanEnv(r.Context())
	if err != nil {
		engineError(w, err)
		return
	}
	p, err := plan.Make(r.Context(), env)
	if err != nil {
		engineError(w, err)
		return
	}
	if _, err := plan.Save(a.e.StateDir, p); err != nil {
		engineError(w, err)
		return
	}
	w.Header().Set("Location", "/api/plans/"+p.ID)
	writeJSON(w, http.StatusCreated, p)
}

func (a *engineAPI) getPlan(w http.ResponseWriter, r *http.Request) {
	p, err := plan.Open(a.e.StateDir, r.PathValue("id"))
	if err != nil {
		engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// applyPlan runs the checks synchronously, so a moved remote or an edited
// working manifest gets its 409 here, then applies in the background.
func (a *engineAPI) applyPlan(w http.ResponseWriter, r *http.Request) {
	p, err := plan.Open(a.e.StateDir, r.PathValue("id"))
	if err != nil {
		engineError(w, err)
		return
	}
	deps, err := a.e.Deps(r.Context())
	if err != nil {
		engineError(w, err)
		return
	}
	first := make(chan apply.Status, 1)
	result := make(chan error, 1)
	go func() {
		var once sync.Once
		_, err := apply.Apply(context.Background(), p, deps, func(s apply.Status) {
			a.mu.Lock()
			a.applies[p.ID] = s
			a.mu.Unlock()
			once.Do(func() { first <- s })
		})
		result <- err
	}()
	// The first status comes after the checks: failed means nothing was
	// written, so it's an error response rather than a 202.
	st := <-first
	if st.State == "failed" {
		engineError(w, <-result)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (a *engineAPI) applyStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	st, ok := a.applies[r.PathValue("id")]
	a.mu.Unlock()
	if !ok {
		problem(w, http.StatusNotFound, "No apply for this plan", "Statuses are kept until canon serve restarts.")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// problemWith is problem plus the validation problems that caused it.
func problemWith(w http.ResponseWriter, status int, title, detail string, ps []manifest.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	writeBody(w, map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail, "problems": ps})
}
