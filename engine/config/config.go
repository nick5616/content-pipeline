// Package config loads and validates Canon's YAML config file.
//
// The real config lives at $XDG_CONFIG_HOME/canon/config.yaml and stays out of
// git because it names buckets. canon.example.yaml in the repo root documents
// every field.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultPort is where `canon serve` listens unless config or --port says otherwise.
const DefaultPort = 4747

// DefaultMaxIngestBytes is the per-file ingest cap when config doesn't set one.
const DefaultMaxIngestBytes int64 = 8 << 30

// Config is the parsed config file with defaults applied.
type Config struct {
	Project string `yaml:"project"`
	Region  string `yaml:"region"`

	// PrivateBucket holds the manifest, sanitized blobs and history.
	PrivateBucket string `yaml:"privateBucket"`
	// PublicBucket holds published copies and views. It is world-readable.
	PublicBucket string `yaml:"publicBucket"`
	// ArchiveBucket holds untouched originals keyed by SHA-256 (D14). Nothing
	// public ever reads it.
	ArchiveBucket string `yaml:"archiveBucket"`

	// ImpersonateServiceAccount is the ADC impersonation target, normally
	// canon-apply@<project>.iam.gserviceaccount.com. Optional.
	ImpersonateServiceAccount string `yaml:"impersonateServiceAccount"`
	// Operator is the account provisioning grants TokenCreator on the service
	// account, for example user:you@example.com. Only provisioning reads it.
	Operator string `yaml:"operator"`
	// PublicCORSOrigins are the site origins allowed to fetch from the public
	// bucket (D16).
	PublicCORSOrigins []string `yaml:"publicCorsOrigins"`

	CacheDir string `yaml:"cacheDir"`
	StateDir string `yaml:"stateDir"`

	// ProfileName seeds profile.name when a working manifest is created from
	// scratch (the schema requires a name).
	ProfileName string `yaml:"profileName"`
	// MaxIngestBytes caps one ingested file. Default 8 GiB.
	MaxIngestBytes int64 `yaml:"maxIngestBytes"`

	Serve struct {
		Port int `yaml:"port"`
	} `yaml:"serve"`
}

// Problem is one thing wrong with a config. Field uses the YAML key name.
type Problem struct {
	Field   string
	Message string
}

func (p Problem) String() string { return p.Field + ": " + p.Message }

// Getenv looks up an environment variable. Tests swap it for a map.
type Getenv func(string) string

// DefaultPath is $XDG_CONFIG_HOME/canon/config.yaml, or ~/.config/canon/config.yaml.
func DefaultPath(getenv Getenv) string {
	return filepath.Join(xdgDir(getenv, "XDG_CONFIG_HOME", ".config"), "canon", "config.yaml")
}

// Load reads and parses the config at path, then fills defaults from the
// process environment. Unknown keys are an error, so typos don't silently
// fall back to defaults. Load does not validate; call Validate for that.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(b, os.Getenv)
}

// Parse decodes YAML config bytes and applies defaults from getenv.
func Parse(b []byte, getenv Getenv) (Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults(getenv)
	return c, nil
}

// Default is an empty config with defaults applied: no project, no buckets.
func Default(getenv Getenv) Config {
	var c Config
	c.applyDefaults(getenv)
	return c
}

func (c *Config) applyDefaults(getenv Getenv) {
	if c.CacheDir == "" {
		c.CacheDir = filepath.Join(xdgDir(getenv, "XDG_CACHE_HOME", ".cache"), "canon")
	} else {
		c.CacheDir = expandHome(getenv, c.CacheDir)
	}
	if c.StateDir == "" {
		c.StateDir = filepath.Join(xdgDir(getenv, "XDG_STATE_HOME", filepath.Join(".local", "state")), "canon")
	} else {
		c.StateDir = expandHome(getenv, c.StateDir)
	}
	if c.Serve.Port == 0 {
		c.Serve.Port = DefaultPort
	}
	if c.MaxIngestBytes == 0 {
		c.MaxIngestBytes = DefaultMaxIngestBytes
	}
}

// BucketsSet reports whether any bucket name is configured.
func (c Config) BucketsSet() bool {
	return c.PrivateBucket != "" || c.PublicBucket != "" || c.ArchiveBucket != ""
}

// Buckets returns the configured buckets by role, skipping unset ones.
func (c Config) Buckets() []Bucket {
	var out []Bucket
	for _, b := range []Bucket{
		{"private", c.PrivateBucket},
		{"public", c.PublicBucket},
		{"archive", c.ArchiveBucket},
	} {
		if b.Name != "" {
			out = append(out, b)
		}
	}
	return out
}

// Bucket is a configured bucket and its role.
type Bucket struct{ Role, Name string }

var (
	projectRe = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	regionRe  = regexp.MustCompile(`^[a-z]+(-[a-z]+[0-9]*)*[0-9]?$`)
	// Bucket names without dots: 3 to 63 chars, lowercase, digits, - and _.
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,61}[a-z0-9]$`)
	saRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$`)
)

// reservedBuckets are the import sources and their private copies. Canon
// reads them and never writes to them (D7, D42), so none may be configured as
// a Canon bucket.
var reservedBuckets = map[string]bool{
	"artandmedia": true, "artandmedia-originals": true,
	"artandmedia-copy": true, "artandmedia-originals-copy": true,
}

// Validate reports every problem with c. Unset buckets and project are not
// problems: doctor skips bucket checks until they are set.
func (c Config) Validate() []Problem {
	var ps []Problem
	add := func(field, format string, args ...any) {
		ps = append(ps, Problem{field, fmt.Sprintf(format, args...)})
	}

	if c.Project != "" && !projectRe.MatchString(c.Project) {
		add("project", "%q is not a valid GCP project ID", c.Project)
	}
	if c.Region != "" && !regionRe.MatchString(c.Region) {
		add("region", "%q is not a valid GCS location", c.Region)
	}

	seen := map[string]string{}
	for _, b := range c.Buckets() {
		field := b.Role + "Bucket"
		switch {
		case !bucketRe.MatchString(b.Name):
			add(field, "%q is not a valid bucket name (3 to 63 of a-z 0-9 - _, no dots)", b.Name)
		case reservedBuckets[b.Name]:
			add(field, "%q is an import source; Canon never writes to it", b.Name)
		case seen[b.Name] != "":
			add(field, "%q is also the %s bucket; each role needs its own bucket", b.Name, seen[b.Name])
		}
		seen[b.Name] = b.Role
	}
	if c.BucketsSet() && c.Project == "" {
		add("project", "required once any bucket is set")
	}

	if sa := c.ImpersonateServiceAccount; sa != "" {
		if !saRe.MatchString(sa) {
			add("impersonateServiceAccount", "%q is not a service account email", sa)
		} else if c.Project != "" && !strings.HasSuffix(sa, "@"+c.Project+".iam.gserviceaccount.com") {
			add("impersonateServiceAccount", "%q is not in project %q", sa, c.Project)
		}
	}
	if c.Operator != "" {
		kind, email, ok := strings.Cut(c.Operator, ":")
		if !ok || (kind != "user" && kind != "group" && kind != "serviceAccount") {
			add("operator", "%q must be user:, group: or serviceAccount: followed by an email", c.Operator)
		} else if _, err := mail.ParseAddress(email); err != nil {
			add("operator", "%q is not an email address", email)
		}
	}

	for i, o := range c.PublicCORSOrigins {
		if msg := checkOrigin(o); msg != "" {
			add(fmt.Sprintf("publicCorsOrigins[%d]", i), "%q %s", o, msg)
		}
	}

	if !filepath.IsAbs(c.CacheDir) {
		add("cacheDir", "%q must be an absolute path", c.CacheDir)
	}
	if !filepath.IsAbs(c.StateDir) {
		add("stateDir", "%q must be an absolute path", c.StateDir)
	}
	if len(c.ProfileName) > 200 {
		add("profileName", "longer than 200 characters")
	}
	if c.MaxIngestBytes < 1<<20 {
		add("maxIngestBytes", "%d is below 1 MiB", c.MaxIngestBytes)
	}
	if p := c.Serve.Port; p < 1024 || p > 65535 {
		add("serve.port", "%d is outside 1024 to 65535", p)
	}
	return ps
}

// checkOrigin returns why o is not a bare origin, or "" if it is fine. Plain
// http is only allowed for localhost dev servers.
func checkOrigin(o string) string {
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "is not an origin like https://example.com"
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "must be scheme and host only"
	}
	if strings.HasSuffix(o, "/") {
		return "must not end with /"
	}
	switch u.Scheme {
	case "https":
	case "http":
		if u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
			return "must use https (http is only for localhost)"
		}
	default:
		return "must use https"
	}
	return ""
}

func xdgDir(getenv Getenv, key, fallback string) string {
	if d := getenv(key); filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(getenv("HOME"), fallback)
}

func expandHome(getenv Getenv, p string) string {
	if p == "~" {
		return getenv("HOME")
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(getenv("HOME"), rest)
	}
	return p
}
