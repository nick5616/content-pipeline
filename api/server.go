// Package api is the local HTTP API behind `canon serve` and the UI it hosts.
// Handlers are hand-written on net/http's ServeMux; openapi.yaml (M1) is the
// contract they are tested against.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/nick5616/canon/engine/apply"
	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/ingest"
)

// Option adds an engine service to the server.
type Option func(*deps)

type deps struct {
	ingester *ingest.Ingester
	engine   *Engine
}

// WithIngester enables POST /api/ingest.
func WithIngester(in *ingest.Ingester) Option { return func(d *deps) { d.ingester = in } }

// NewServer returns the full handler: guard, API routes and the embedded UI.
// ui is the built UI (ui.Dist rooted at dist); it may hold only .gitkeep.
func NewServer(cfg config.Config, token string, ui fs.FS, opts ...Option) http.Handler {
	var d deps
	for _, o := range opts {
		o(&d)
	}
	ing := newIngestAPI(d.ingester)
	ja := &jobsAPI{e: d.engine}
	ing.after = ja.afterIngest
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ingest", ing.start)
	mux.HandleFunc("GET /api/ingest/{id}", ing.get)
	(&engineAPI{e: d.engine, applies: map[string]apply.Status{}}).routes(mux)
	ja.routes(mux)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, http.StatusNotFound, "No such endpoint", r.Method+" "+r.URL.Path)
	})
	mux.Handle("/", staticUI(ui))
	return guard(cfg.Serve.Port, token, mux)
}

// staticUI serves files from the built UI. Unknown paths without a file
// extension get index.html so client-side routes survive a reload.
func staticUI(ui fs.FS) http.Handler {
	if _, err := fs.Stat(ui, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(notBuilt))
		})
	}
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			problem(w, http.StatusMethodNotAllowed, "Method not allowed", "")
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if _, err := fs.Stat(ui, name); errors.Is(err, fs.ErrNotExist) && path.Ext(name) == "" {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}

const notBuilt = `<!doctype html>
<meta charset="utf-8">
<title>Canon: UI not built</title>
<h1>The UI is not built</h1>
<p>Run <code>task ui:build</code>, then rebuild canon. The API is still up.</p>
`

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeBody(w, v)
}

func writeBody(w http.ResponseWriter, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// problem writes an RFC 9457 problem document.
func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	p := map[string]any{"type": "about:blank", "title": title, "status": status}
	if detail != "" {
		p["detail"] = detail
	}
	_ = json.NewEncoder(w).Encode(p)
}
