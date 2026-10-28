package api

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
)

// csp allows only same-origin resources. The built UI has no inline scripts.
const csp = "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; object-src 'none'"

// guard protects a server that only this machine's browser should reach:
//
//   - Host must be exactly 127.0.0.1:<port> or localhost:<port>, which stops
//     DNS rebinding (a hostile page whose name resolves to 127.0.0.1).
//   - /api/* needs the bearer token. Static files don't: the token arrives in
//     the URL fragment, which the browser never sends.
//   - No CORS headers are ever set, and OPTIONS is refused, so no other origin
//     can read a response.
func guard(port int, token string, next http.Handler) http.Handler {
	p := strconv.Itoa(port)
	hosts := map[string]bool{"127.0.0.1:" + p: true, "localhost:" + p: true}
	want := []byte("Bearer " + token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")

		if !hosts[r.Host] {
			problem(w, http.StatusMisdirectedRequest, "Host not allowed", "This server only answers requests for 127.0.0.1 or localhost.")
			return
		}
		if r.Method == http.MethodOptions {
			h.Set("Allow", "GET, HEAD, POST, PUT, PATCH, DELETE")
			problem(w, http.StatusMethodNotAllowed, "OPTIONS not allowed", "")
			return
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			got := []byte(r.Header.Get("Authorization"))
			if subtle.ConstantTimeCompare(got, want) != 1 {
				h.Set("WWW-Authenticate", `Bearer realm="canon"`)
				problem(w, http.StatusUnauthorized, "Missing or wrong token", "Open the URL that `canon serve` printed.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
