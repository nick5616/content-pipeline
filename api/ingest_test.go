package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	validator "github.com/pb33f/libopenapi-validator"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/ingest"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/internal/testmedia"
)

func ingestServer(t *testing.T, maxBytes int64) (http.Handler, *ingest.Ingester) {
	t.Helper()
	blobs, err := blobstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := ingest.New(blobs, store.Open(t.TempDir(), "N", surfaces.MustDefault()), maxBytes)
	var cfg config.Config
	cfg.Serve.Port = 4747
	return NewServer(cfg, token, builtUI, WithIngester(in)), in
}

func upload(t *testing.T, files ...string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, f := range files {
		w, _ := mw.CreateFormFile("files", filepath.Base(f))
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "http://127.0.0.1:4747/api/ingest", &body)
	req.Host = "127.0.0.1:4747"
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// serve runs req and returns a response whose body can be read twice
// (once by the validator, once by the test).
func serve(t *testing.T, h http.Handler, v validator.Validator, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if ok, errs := v.ValidateHttpResponse(req, resp); !ok {
		t.Fatalf("%s %s -> %d does not match openapi.yaml:\n%s\n%s", req.Method, req.URL.Path, resp.StatusCode, describe(errs), body)
	}
	return resp, body
}

func TestIngestAPI(t *testing.T) {
	v, _ := validator.NewValidator(loadSpec(t))
	h, _ := ingestServer(t, 64<<20)
	dir := testmedia.Dir(t)

	resp, body := serve(t, h, v, upload(t, filepath.Join(dir, "png.png"), filepath.Join(dir, "hostile", "playlist.mp4")))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var b batch
	json.Unmarshal(body, &b)
	if loc := resp.Header.Get("Location"); loc != "/api/ingest/"+b.ID || len(b.Files) != 2 {
		t.Fatalf("location %q, %d files", loc, len(b.Files))
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		req := httptest.NewRequest("GET", "http://127.0.0.1:4747/api/ingest/"+b.ID, nil)
		req.Host = "127.0.0.1:4747"
		req.Header.Set("Authorization", "Bearer "+token)
		_, body := serve(t, h, v, req)
		json.Unmarshal(body, &b)
		if (b.Files[0].Stage == ingest.Done || b.Files[0].Stage == ingest.Failed) &&
			(b.Files[1].Stage == ingest.Done || b.Files[1].Stage == ingest.Failed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("batch never finished: %s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b.Files[0].Stage != ingest.Done || b.Files[0].Item == nil {
		t.Errorf("png: %+v", b.Files[0])
	}
	if b.Files[1].Stage != ingest.Failed || !strings.Contains(*b.Files[1].Error, "HLS playlist") {
		t.Errorf("playlist: %+v", b.Files[1])
	}
}

func TestIngestAPIErrors(t *testing.T) {
	v, _ := validator.NewValidator(loadSpec(t))
	h, _ := ingestServer(t, 1000)
	resp, body := serve(t, h, v, upload(t, filepath.Join(testmedia.Dir(t), "jpeg.jpg")))
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "maxIngestBytes") {
		t.Fatalf("oversize: %d %s", resp.StatusCode, body)
	}

	req := httptest.NewRequest("GET", "http://127.0.0.1:4747/api/ingest/01JB7Z00000000000000000001", nil)
	req.Host = "127.0.0.1:4747"
	req.Header.Set("Authorization", "Bearer "+token)
	if resp, _ := serve(t, h, v, req); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown batch: %d", resp.StatusCode)
	}

	// Without an ingester (e.g. no cache dir) the endpoint says so.
	plain := newServer(builtUI)
	w := httptest.NewRecorder()
	plain.ServeHTTP(w, upload(t, filepath.Join(testmedia.Dir(t), "png.png")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no ingester: %d", w.Code)
	}
}
