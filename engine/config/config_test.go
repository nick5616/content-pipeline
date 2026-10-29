package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

var home = env(map[string]string{"HOME": "/home/u"})

func TestExampleIsValid(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "canon.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(b, home)
	if err != nil {
		t.Fatal(err)
	}
	if ps := c.Validate(); len(ps) != 0 {
		t.Fatalf("example config has problems: %v", ps)
	}
	if c.Serve.Port != 4747 || len(c.PublicCORSOrigins) != 5 {
		t.Fatalf("unexpected example: %+v", c)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if !os.IsNotExist(err) {
		t.Fatalf("want not-exist error, got %v", err)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	_, err := Parse([]byte("privatebucket: x\n"), home)
	if err == nil || !strings.Contains(err.Error(), "privatebucket") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}

func TestDefaults(t *testing.T) {
	tests := []struct {
		name              string
		env               map[string]string
		yaml              string
		cache, state, cfg string
	}{
		{
			name:  "home fallbacks",
			env:   map[string]string{"HOME": "/home/u"},
			cache: "/home/u/.cache/canon", state: "/home/u/.local/state/canon", cfg: "/home/u/.config/canon/config.yaml",
		},
		{
			name:  "xdg vars",
			env:   map[string]string{"HOME": "/home/u", "XDG_CACHE_HOME": "/c", "XDG_STATE_HOME": "/s", "XDG_CONFIG_HOME": "/cfg"},
			cache: "/c/canon", state: "/s/canon", cfg: "/cfg/canon/config.yaml",
		},
		{
			name:  "relative xdg ignored per spec",
			env:   map[string]string{"HOME": "/home/u", "XDG_CACHE_HOME": "rel"},
			cache: "/home/u/.cache/canon", state: "/home/u/.local/state/canon", cfg: "/home/u/.config/canon/config.yaml",
		},
		{
			name:  "explicit dirs with tilde",
			env:   map[string]string{"HOME": "/home/u"},
			yaml:  "cacheDir: ~/blobs\nstateDir: /var/canon\n",
			cache: "/home/u/blobs", state: "/var/canon", cfg: "/home/u/.config/canon/config.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse([]byte(tt.yaml), env(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			if c.CacheDir != tt.cache || c.StateDir != tt.state || c.Serve.Port != DefaultPort {
				t.Errorf("got cache=%q state=%q port=%d", c.CacheDir, c.StateDir, c.Serve.Port)
			}
			if got := DefaultPath(env(tt.env)); got != tt.cfg {
				t.Errorf("DefaultPath = %q, want %q", got, tt.cfg)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		field string // "" means valid
	}{
		{"empty is valid", "", ""},
		{"all set", "project: my-proj\nregion: us-central1\nprivateBucket: canon-priv\npublicBucket: canon-pub\narchiveBucket: canon-arch\nimpersonateServiceAccount: canon-apply@my-proj.iam.gserviceaccount.com\noperator: user:me@example.com\n", ""},
		{"bad project", "project: My_Proj\n", "project"},
		{"bucket without project", "privateBucket: canon-priv\n", "project"},
		{"bad bucket name", "project: my-proj\npublicBucket: Canon.Pub\n", "publicBucket"},
		{"reserved bucket", "project: my-proj\npublicBucket: artandmedia\n", "publicBucket"},
		{"reserved originals bucket", "project: my-proj\narchiveBucket: artandmedia-originals\n", "archiveBucket"},
		{"reserved copy bucket", "project: my-proj\nprivateBucket: artandmedia-copy\n", "privateBucket"},
		{"reserved originals copy bucket", "project: my-proj\narchiveBucket: artandmedia-originals-copy\n", "archiveBucket"},
		{"duplicate bucket", "project: my-proj\nprivateBucket: same-b\narchiveBucket: same-b\n", "archiveBucket"},
		{"bad region", "region: US Central\n", "region"},
		{"sa not email", "impersonateServiceAccount: canon-apply\n", "impersonateServiceAccount"},
		{"sa wrong project", "project: my-proj\nimpersonateServiceAccount: canon-apply@other-proj.iam.gserviceaccount.com\n", "impersonateServiceAccount"},
		{"operator missing kind", "operator: me@example.com\n", "operator"},
		{"operator bad email", "operator: user:nope\n", "operator"},
		{"cors with path", "publicCorsOrigins: [https://a.com/x]\n", "publicCorsOrigins[0]"},
		{"cors trailing slash", "publicCorsOrigins: [https://a.com/]\n", "publicCorsOrigins[0]"},
		{"cors http non-local", "publicCorsOrigins: [http://a.com]\n", "publicCorsOrigins[0]"},
		{"cors wildcard", "publicCorsOrigins: [\"*\"]\n", "publicCorsOrigins[0]"},
		{"cors localhost http ok", "publicCorsOrigins: [http://localhost:5173]\n", ""},
		{"relative cache dir", "cacheDir: cache\n", "cacheDir"},
		{"low port", "serve: {port: 80}\n", "serve.port"},
		{"tiny ingest cap", "maxIngestBytes: 10\n", "maxIngestBytes"},
		{"high port", "serve: {port: 70000}\n", "serve.port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse([]byte(tt.yaml), home)
			if err != nil {
				t.Fatal(err)
			}
			ps := c.Validate()
			if tt.field == "" {
				if len(ps) != 0 {
					t.Fatalf("want valid, got %v", ps)
				}
				return
			}
			if len(ps) != 1 || ps[0].Field != tt.field {
				t.Fatalf("want one problem on %s, got %v", tt.field, ps)
			}
		})
	}
}
