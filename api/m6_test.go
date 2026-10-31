package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nick5616/canon/engine/doctor"
	"github.com/nick5616/canon/engine/jobs"
)

func TestDoctorAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	x.call("GET", "/api/doctor", "", nil, 503, nil)
	x.e.Doctor = func(context.Context) doctor.Report {
		return doctor.Report{Checks: []doctor.Check{{Name: "ffmpeg", Status: doctor.OK, Detail: "6.1"}}}
	}
	var r struct {
		Checks []struct{ Name, Status string }
	}
	x.call("GET", "/api/doctor", "", nil, 200, &r)
	if len(r.Checks) != 1 || r.Checks[0].Status != "ok" {
		t.Fatalf("%+v", r)
	}
}

func TestLocalCacheAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	x.env.WithJobs()
	x.e.Derived, x.e.Jobs = x.env.Derived, x.env.Jobs
	if _, err := x.env.Runner().Run(t.Context(), jobs.Options{}); err != nil {
		t.Fatal(err)
	}
	m, _, _, _ := x.env.Working.Load()
	sum := strings.TrimPrefix(string(m.Items[0].Assets[0].Blob), "sha256:")

	h := x.call("GET", "/api/local/blobs/"+sum, "", nil, 200, nil)
	if h.Get("Content-Type") != "image/png" || !strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Errorf("blob headers: %v", h)
	}
	x.call("GET", "/api/local/blobs/"+strings.Repeat("0", 64), "", nil, 404, nil)

	specs, _, _ := x.env.Runner().Specs(jobs.Options{Recipe: "thumb"})
	d, ok := x.env.Derived.Lookup(specs[0].Key)
	if !ok {
		t.Fatal("no thumb outputs")
	}
	name := d.Receipt().Files[0].Name
	h = x.call("GET", "/api/local/derived/"+specs[0].Key+"/"+name, "", nil, 200, nil)
	if h.Get("Content-Type") != "image/webp" {
		t.Errorf("derived type %q", h.Get("Content-Type"))
	}
	// The thumbnail endpoint picks the nearest size (the fixture has one).
	h = x.call("GET", "/api/local/thumbnails/"+sum+"?w=48", "", nil, 200, nil)
	if h.Get("Content-Type") != "image/webp" {
		t.Errorf("thumbnail type %q", h.Get("Content-Type"))
	}
	x.call("GET", "/api/local/thumbnails/"+strings.Repeat("0", 64), "", nil, 404, nil)

	// The receipt and anything it doesn't list aren't served.
	x.call("GET", "/api/local/derived/"+specs[0].Key+"/job.json", "", nil, 404, nil)
	x.call("GET", "/api/local/derived/"+specs[0].Key+"/..%2Fjob.json", "", nil, 404, nil)
}

// The stream delivers a sync event after an edit.
func TestEventsAPI(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	srv := httptest.NewServer(x.h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.Host = "127.0.0.1:4747"
	req.Header.Set("Authorization", "Bearer "+token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := bufio.NewScanner(resp.Body)
	lines.Scan() // ": canon events"

	// Edit an item through the API.
	var items struct {
		Items []struct{ ID string }
	}
	h := x.call("GET", "/api/items", "", nil, 200, &items)
	x.call("PATCH", "/api/items/"+items.Items[0].ID, `{"title":"Renamed"}`, match(h.Get("ETag")), 200, nil)

	for lines.Scan() {
		data, ok := strings.CutPrefix(lines.Text(), "data: ")
		if !ok {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "sync" {
			if b, _ := json.Marshal(e.Data); !strings.Contains(string(b), `"localEdits":true`) {
				t.Errorf("sync event: %s", b)
			}
			return
		}
	}
	t.Fatalf("no sync event: %v", lines.Err())
}

func TestEventsNeedToken(t *testing.T) {
	x := newEngineHarness(t, "png.png")
	req := httptest.NewRequest("GET", "http://127.0.0.1:4747/api/events", nil)
	req.Host = "127.0.0.1:4747"
	w := httptest.NewRecorder()
	x.h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("no token: %d", w.Code)
	}
}
