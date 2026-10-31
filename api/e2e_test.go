package api

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/internal/testenv"
	"github.com/nick5616/canon/ui"
)

// TestUISmoke runs ui/e2e/smoke.mjs in headless Chrome against the built UI
// and a real engine on in-memory buckets: library, edit, plan, apply. It
// needs `task ui:build` (or npm run build in ui/), node, and Chrome (it
// looks in ~/.cache/puppeteer; CANON_CHROME overrides).
func TestUISmoke(t *testing.T) {
	chrome := os.Getenv("CANON_CHROME")
	if chrome == "" {
		matches, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".cache/puppeteer/chrome/*/chrome-linux64/chrome"))
		if len(matches) > 0 {
			chrome = matches[len(matches)-1]
		}
	}
	if _, err := exec.LookPath("node"); err != nil || chrome == "" {
		t.Skip("needs node and Chrome (set CANON_CHROME)")
	}
	if _, err := os.Stat(filepath.Join("..", "ui", "node_modules", "puppeteer-core")); err != nil {
		t.Skip("ui/node_modules has no puppeteer-core (npm ci in ui/)")
	}
	if b, err := ui.Dist.Open("index.html"); err != nil {
		t.Skip("the UI isn't built")
	} else {
		b.Close()
	}

	x := newEngineHarness(t, "png.png", "jpeg.jpg")
	x.env.WithPublic()
	x.e.Derived, x.e.Jobs = x.env.Derived, x.env.Jobs
	x.e.Config.PublicBucket = testenv.Public
	if _, err := x.env.Working.Update(func(m *manifest.Manifest) error {
		m.Surfaces = []manifest.Surface{{ID: "art-portfolio", Label: "Art"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Thumbnails exist only once their jobs have run.
	if _, err := x.env.Runner().Run(t.Context(), jobs.Options{}); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := x.e.Config
	cfg.Serve.Port = ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: NewServer(cfg, token, ui.Dist, WithEngine(x.e))}
	go srv.Serve(ln)
	defer srv.Close()

	url := "http://127.0.0.1:" + strconv.Itoa(cfg.Serve.Port) + "/#token=" + token
	cmd := exec.Command("node", "e2e/smoke.mjs", url, chrome)
	cmd.Dir = filepath.Join("..", "ui")
	out, err := cmd.CombinedOutput()
	t.Logf("\n%s", out)
	if err != nil {
		t.Fatal(err)
	}

	// The edit reached the working manifest, and apply reached the bucket.
	m, _, _, _ := x.env.Working.Load()
	found := false
	for _, it := range m.Items {
		found = found || it.Title == "Edited in the browser"
	}
	if !found {
		t.Error("the edit isn't in the working manifest")
	}
	b, _, _, ok := x.mem.Get(testenv.Private, "manifest/manifest.json")
	if !ok || !strings.Contains(string(b), "Edited in the browser") {
		t.Error("apply didn't write the edited manifest")
	}
}
