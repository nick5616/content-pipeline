package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nick5616/canon/engine/config"
)

const token = "s3cret-token"

var builtUI = fstest.MapFS{
	"index.html":       {Data: []byte("<!doctype html><div id=root></div>")},
	"assets/app-1.js":  {Data: []byte("console.log(1)")},
	"assets/app-1.css": {Data: []byte("body{}")},
}

func newServer(ui fstest.MapFS) http.Handler {
	var cfg config.Config
	cfg.Serve.Port = 4747
	return NewServer(cfg, token, ui)
}

func do(h http.Handler, method, host, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var bearer = map[string]string{"Authorization": "Bearer " + token}

func TestGuardHost(t *testing.T) {
	h := newServer(builtUI)
	tests := []struct {
		host string
		want int
	}{
		{"127.0.0.1:4747", 200},
		{"localhost:4747", 200},
		{"evil.com", 421},
		{"evil.com:4747", 421},
		{"127.0.0.1.evil.com", 421},
		{"127.0.0.1.evil.com:4747", 421},
		{"localhost.:4747", 421},
		{"localhost.", 421},
		{"localhost", 421},
		{"127.0.0.1", 421},
		{"127.0.0.1:4748", 421},
		{"[::1]:4747", 421},
		{"LOCALHOST:4747", 421},
		{"", 421},
	}
	for _, tt := range tests {
		for _, path := range []string{"/api/health", "/"} {
			if got := do(h, "GET", tt.host, path, bearer).Code; got != tt.want {
				t.Errorf("Host %q %s: got %d, want %d", tt.host, path, got, tt.want)
			}
		}
	}
}

func TestGuardToken(t *testing.T) {
	h := newServer(builtUI)
	tests := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"missing", nil, 401},
		{"wrong", map[string]string{"Authorization": "Bearer nope"}, 401},
		{"prefix of right", map[string]string{"Authorization": "Bearer " + token[:5]}, 401},
		{"right with suffix", map[string]string{"Authorization": "Bearer " + token + "x"}, 401},
		{"no scheme", map[string]string{"Authorization": token}, 401},
		{"basic", map[string]string{"Authorization": "Basic " + token}, 401},
		{"right", bearer, 200},
	}
	for _, tt := range tests {
		w := do(h, "GET", "127.0.0.1:4747", "/api/health", tt.hdr)
		if w.Code != tt.want {
			t.Errorf("%s: got %d, want %d", tt.name, w.Code, tt.want)
		}
		if tt.want == 401 && w.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: 401 is not problem+json", tt.name)
		}
	}
	// The bare /api path is guarded too.
	if got := do(h, "GET", "127.0.0.1:4747", "/api", nil).Code; got != 401 {
		t.Errorf("/api without token: %d", got)
	}
	// Static files need no token.
	if got := do(h, "GET", "127.0.0.1:4747", "/assets/app-1.js", nil).Code; got != 200 {
		t.Errorf("static without token: %d", got)
	}
}

func TestGuardHeaders(t *testing.T) {
	h := newServer(builtUI)
	cases := []struct{ method, host, path string }{
		{"GET", "127.0.0.1:4747", "/"},
		{"GET", "127.0.0.1:4747", "/api/health"},
		{"GET", "127.0.0.1:4747", "/api/nope"},
		{"GET", "evil.com", "/"},
		{"OPTIONS", "127.0.0.1:4747", "/api/health"},
		{"OPTIONS", "127.0.0.1:4747", "/"},
		{"POST", "127.0.0.1:4747", "/"},
	}
	for _, c := range cases {
		for _, hdr := range []map[string]string{nil, bearer, {
			"Origin": "https://evil.com", "Access-Control-Request-Method": "GET", "Authorization": "Bearer " + token,
		}} {
			w := do(h, c.method, c.host, c.path, hdr)
			for k := range w.Header() {
				if strings.HasPrefix(strings.ToLower(k), "access-control-") {
					t.Errorf("%s %s %s: sets %s", c.method, c.host, c.path, k)
				}
			}
			for k, v := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "no-referrer",
			} {
				if w.Header().Get(k) != v {
					t.Errorf("%s %s %s: %s = %q", c.method, c.host, c.path, k, w.Header().Get(k))
				}
			}
			if !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
				t.Errorf("%s %s %s: CSP %q", c.method, c.host, c.path, w.Header().Get("Content-Security-Policy"))
			}
		}
	}
}

func TestOptionsRefused(t *testing.T) {
	h := newServer(builtUI)
	for _, path := range []string{"/", "/api/health", "/assets/app-1.js"} {
		if got := do(h, "OPTIONS", "127.0.0.1:4747", path, bearer).Code; got != 405 {
			t.Errorf("OPTIONS %s: %d", path, got)
		}
	}
}

func TestEmbeddedUI(t *testing.T) {
	h := newServer(builtUI)
	w := do(h, "GET", "localhost:4747", "/", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `id=root`) {
		t.Fatalf("index: %d %q", w.Code, w.Body.String())
	}
	// Client-side routes fall back to index.html; missing assets don't.
	if w := do(h, "GET", "localhost:4747", "/items/abc", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "id=root") {
		t.Errorf("SPA fallback: %d", w.Code)
	}
	if got := do(h, "GET", "localhost:4747", "/assets/missing.js", nil).Code; got != 404 {
		t.Errorf("missing asset: %d", got)
	}
	if w := do(h, "GET", "localhost:4747", "/../../etc/passwd", nil); strings.Contains(w.Body.String(), "root:") {
		t.Errorf("traversal escaped the UI fs")
	}
}

func TestUINotBuilt(t *testing.T) {
	h := newServer(fstest.MapFS{".gitkeep": {}})
	w := do(h, "GET", "127.0.0.1:4747", "/", nil)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "UI is not built") {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	// The API still works.
	if got := do(h, "GET", "127.0.0.1:4747", "/api/health", bearer).Code; got != 200 {
		t.Errorf("health: %d", got)
	}
}

func TestUnknownAPIRoute(t *testing.T) {
	w := do(newServer(builtUI), "GET", "127.0.0.1:4747", "/api/nope", bearer)
	if w.Code != 404 || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}
