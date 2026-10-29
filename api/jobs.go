package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/recipes"
	"github.com/nick5616/canon/engine/sanitize"
)

// Runner builds the job runner. It checks the private bucket for receipts
// only when buckets are configured; otherwise everything runs locally.
func (e *Engine) Runner() *jobs.Runner {
	t := e.Tools
	if t.Run == nil {
		t = recipes.NewTools()
	}
	r := &jobs.Runner{DB: e.Jobs, Derived: e.Derived, Blobs: e.Blobs, Working: e.Working,
		Tools: t, Verifier: sanitize.Verifier{Run: t.Run}, StateDir: e.StateDir}
	if e.Config.PrivateBucket != "" {
		r.Remote = func(ctx context.Context) (map[string]gcs.Object, error) {
			api, err := e.GCS(ctx)
			if err != nil {
				return nil, err
			}
			return jobs.Receipts(ctx, api, e.Config.PrivateBucket)
		}
	}
	return r
}

// runJobsForPlan is plan's first step (D45). Job failures don't stop the
// plan (they become warnings), and a run already going on this machine
// (canon serve after an ingest) is left to finish: the plan then warns
// about whatever it hasn't done yet.
func (e *Engine) runJobsForPlan(ctx context.Context) error {
	rep, err := e.Runner().Run(ctx, jobs.Options{})
	if errors.Is(err, jobs.ErrBusy) {
		return nil
	}
	if e.OnJobs != nil {
		e.OnJobs(rep)
	}
	return err
}

func (e *Engine) jobState(key string) (string, string) {
	j, err := e.Jobs.Get(key)
	if err != nil {
		return "", ""
	}
	msg := ""
	if j.LastError != nil {
		msg = *j.LastError
	}
	return string(j.State), msg
}

// jobRun is a background run (openapi JobRun).
type jobRun struct {
	State  string       `json:"state"` // running, done, failed
	Report *jobs.Report `json:"report"`
	Error  *string      `json:"error"`
}

type jobsAPI struct {
	e *Engine

	mu  sync.Mutex
	run *jobRun
}

// start begins a background run unless one is going. It reports whether
// it started one.
func (a *jobsAPI) start(o jobs.Options) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run != nil && a.run.State == "running" {
		return false
	}
	a.run = &jobRun{State: "running"}
	go func() {
		rep, err := a.e.Runner().Run(context.Background(), o)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.run.Report = &rep
		if err != nil {
			msg := err.Error()
			a.run.State, a.run.Error = "failed", &msg
			return
		}
		a.run.State = "done"
	}()
	return true
}

func (a *jobsAPI) status() jobRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run == nil {
		return jobRun{State: "done"}
	}
	return *a.run
}

// afterIngest runs jobs for what was just ingested (D45).
func (a *jobsAPI) afterIngest() {
	if a.e != nil && a.e.Jobs != nil {
		a.start(jobs.Options{})
	}
}

func (a *jobsAPI) routes(mux *http.ServeMux) {
	h := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if a.e == nil || a.e.Jobs == nil {
				problem(w, http.StatusServiceUnavailable, "Jobs unavailable", "canon serve was started without a state dir.")
				return
			}
			f(w, r)
		}
	}
	mux.HandleFunc("GET /api/jobs", h(a.list))
	mux.HandleFunc("POST /api/jobs/{id}/retry", h(a.retry))
	mux.HandleFunc("POST /api/jobs/run", h(a.runJobs))
	mux.HandleFunc("GET /api/jobs/run", h(a.runStatus))
}

func (a *jobsAPI) list(w http.ResponseWriter, r *http.Request) {
	st := jobs.State(r.URL.Query().Get("state"))
	switch st {
	case "", jobs.Pending, jobs.Running, jobs.Done, jobs.Failed:
	default:
		problem(w, http.StatusBadRequest, "Bad state", "state is pending, running, done or failed")
		return
	}
	js, err := a.e.Jobs.List(st)
	if err != nil {
		engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, js)
}

func (a *jobsAPI) retry(w http.ResponseWriter, r *http.Request) {
	j, err := a.e.Jobs.Retry(r.PathValue("id"))
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		problem(w, http.StatusNotFound, "No such job", r.PathValue("id"))
	case err != nil:
		problem(w, http.StatusConflict, "Job isn't failed", err.Error())
	default:
		writeJSON(w, http.StatusOK, j)
	}
}

// runJobs starts a run of every needed job, failed ones included (like
// canon jobs run), and answers 202 with its status; 409 if one is going.
func (a *jobsAPI) runJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o := jobs.Options{Item: q.Get("item"), Recipe: q.Get("recipe"), Failed: true, RefreshFacts: q.Get("refreshFacts") == "true"}
	if !a.start(o) {
		problem(w, http.StatusConflict, "Jobs are already running", "Poll GET /api/jobs/run until it's done.")
		return
	}
	w.Header().Set("Location", "/api/jobs/run")
	writeJSON(w, http.StatusAccepted, a.status())
}

func (a *jobsAPI) runStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.status())
}
