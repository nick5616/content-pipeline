package api

import (
	"testing"
	"time"

	"github.com/nick5616/canon/engine/jobs"
)

// The job endpoints against openapi.yaml 1.3.0: run in the background,
// poll, list, retry; then a plan uploads what ran.
func TestJobsAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	x.env.WithJobs()
	e := x.e
	e.Derived, e.Jobs = x.env.Derived, x.env.Jobs

	var js []jobs.Job
	x.call("GET", "/api/jobs", "", nil, 200, &js)
	if len(js) != 0 {
		t.Fatalf("jobs before any run: %+v", js)
	}

	var run jobRun
	x.call("POST", "/api/jobs/run", "", nil, 202, &run)
	deadline := time.Now().Add(60 * time.Second)
	for run.State == "running" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		x.call("GET", "/api/jobs/run", "", nil, 200, &run)
	}
	if run.State != "done" || run.Report == nil || run.Report.Ran != 3 || run.Report.Facts != 1 {
		t.Fatalf("run: %+v %+v", run, run.Report)
	}

	x.call("GET", "/api/jobs?state=done", "", nil, 200, &js)
	if len(js) != 3 {
		t.Fatalf("done jobs: %+v", js)
	}
	x.call("GET", "/api/jobs?state=failed", "", nil, 200, &js)
	if len(js) != 0 {
		t.Fatalf("failed jobs: %+v", js)
	}
	// Only failed jobs can be retried.
	x.call("POST", "/api/jobs/"+js0(t, x)+"/retry", "", nil, 409, nil)
	x.call("POST", "/api/jobs/"+zeros+"/retry", "", nil, 404, nil)

	var p struct {
		Summary struct{ Jobs int }
	}
	x.call("POST", "/api/plans", "", nil, 201, &p)
	if p.Summary.Jobs != 3 {
		t.Errorf("plan jobs: %+v", p.Summary)
	}
}

const zeros = "0000000000000000000000000000000000000000000000000000000000000000"

func js0(t *testing.T, x *engineHarness) string {
	var js []jobs.Job
	x.call("GET", "/api/jobs", "", nil, 200, &js)
	return js[0].Jobkey
}
