// Package doctor checks that this machine can run Canon: tools, config,
// credentials, buckets and local dirs. Every dependency comes in through Env,
// so tests run without touching the network or PATH.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/toolchain"
)

type Status string

const (
	OK      Status = "ok"
	Warn    Status = "warn"
	Fail    Status = "fail"
	Skipped Status = "skipped"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	Checks []Check          `json:"checks"`
	Tools  []toolchain.Tool `json:"tools"`
}

// Failed reports whether any check failed.
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// Env is everything doctor touches. NewEnv fills it with the real thing.
type Env struct {
	Config     config.Config
	ConfigPath string
	// ConfigErr is the error from loading ConfigPath, if any. A missing file
	// is a warning (defaults apply); anything else fails.
	ConfigErr error

	LookupTool  func(ctx context.Context, name string) (toolchain.Tool, error)
	Credentials func(ctx context.Context) error // mints one token
	// BucketReachable lists at most one object. Canon's service account has
	// object roles only, so reading the bucket's own settings would be denied.
	BucketReachable func(ctx context.Context, bucket string) error
	Writable        func(dir string) error
	Docker          func(ctx context.Context) error
}

// StateFile is where doctor records tool versions between runs.
const StateFile = "doctor.json"

// Run performs every check. It never returns early: one report shows all
// problems at once.
func Run(ctx context.Context, env Env) Report {
	var r Report
	add := func(name string, s Status, format string, args ...any) {
		r.Checks = append(r.Checks, Check{name, s, fmt.Sprintf(format, args...)})
	}

	// Config.
	cfg := env.Config
	switch {
	case errors.Is(env.ConfigErr, fs.ErrNotExist):
		add("config", Warn, "no config at %s; using defaults (copy canon.example.yaml)", env.ConfigPath)
	case env.ConfigErr != nil:
		add("config", Fail, "%v", env.ConfigErr)
	default:
		if ps := cfg.Validate(); len(ps) > 0 {
			add("config", Fail, "%s: %v", env.ConfigPath, ps)
		} else {
			add("config", OK, "%s", env.ConfigPath)
		}
	}
	configOK := env.ConfigErr == nil || errors.Is(env.ConfigErr, fs.ErrNotExist)
	configOK = configOK && len(cfg.Validate()) == 0

	// Local dirs, before tools, since the version record lives in StateDir.
	stateWritable := false
	for _, d := range []struct{ name, dir string }{{"cache dir", cfg.CacheDir}, {"state dir", cfg.StateDir}} {
		if err := env.Writable(d.dir); err != nil {
			add(d.name, Fail, "%s: %v", d.dir, err)
			continue
		}
		add(d.name, OK, "%s", d.dir)
		stateWritable = stateWritable || d.name == "state dir"
	}

	// Tools, compared with the versions recorded last run.
	prev := readTools(filepath.Join(cfg.StateDir, StateFile))
	for _, name := range toolchain.Required {
		t, err := env.LookupTool(ctx, name)
		if err != nil {
			add(name, Fail, "%v", err)
			continue
		}
		r.Tools = append(r.Tools, t)
		if old, ok := prev[name]; ok && old != t.Version {
			add(name, Warn, "%s at %s (was %s last run; derived output may change)", t.Version, t.Path, old)
		} else {
			add(name, OK, "%s at %s", t.Version, t.Path)
		}
	}
	if stateWritable {
		if err := writeTools(filepath.Join(cfg.StateDir, StateFile), prev, r.Tools); err != nil {
			add("tool record", Warn, "%v", err)
		}
	}

	// Credentials, then buckets.
	credsOK := false
	if err := env.Credentials(ctx); err != nil {
		add("credentials", Fail, "%v", err)
	} else {
		credsOK = true
		if sa := cfg.ImpersonateServiceAccount; sa != "" {
			add("credentials", OK, "ADC can mint a token as %s", sa)
		} else {
			add("credentials", OK, "ADC can mint a token (no impersonation configured)")
		}
	}
	switch {
	case !configOK:
		add("buckets", Skipped, "config is invalid")
	case !cfg.BucketsSet():
		add("buckets", Skipped, "bucket names are unset in config")
	case !credsOK:
		add("buckets", Skipped, "credentials failed")
	default:
		for _, b := range cfg.Buckets() {
			name := b.Role + " bucket"
			if err := env.BucketReachable(ctx, b.Name); err != nil {
				add(name, Fail, "gs://%s: %v", b.Name, err)
			} else {
				add(name, OK, "gs://%s reachable", b.Name)
			}
		}
	}

	if err := env.Docker(ctx); err != nil {
		add("docker", Warn, "not reachable, so integration tests can't run: %v", err)
	} else {
		add("docker", OK, "daemon reachable")
	}
	return r
}

type record struct {
	Tools []toolchain.Tool `json:"tools"`
}

func readTools(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var rec record
	if json.Unmarshal(b, &rec) == nil {
		for _, t := range rec.Tools {
			out[t.Name] = t.Version
		}
	}
	return out
}

// writeTools records the current versions. A tool missing this run keeps its
// last recorded version, so reinstalling the same version is not a change.
func writeTools(path string, prev map[string]string, now []toolchain.Tool) error {
	found := map[string]bool{}
	rec := record{Tools: append([]toolchain.Tool{}, now...)}
	for _, t := range now {
		found[t.Name] = true
	}
	for _, name := range toolchain.Required {
		if v, ok := prev[name]; ok && !found[name] {
			rec.Tools = append(rec.Tools, toolchain.Tool{Name: name, Version: v})
		}
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NewEnv wires the real implementations for cfg, as loaded from path.
func NewEnv(cfg config.Config, path string, loadErr error) Env {
	return Env{
		Config:     cfg,
		ConfigPath: path,
		ConfigErr:  loadErr,
		LookupTool: toolchain.Lookup,
		Credentials: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			ts, err := gcs.TokenSource(ctx, cfg)
			if err != nil {
				return err
			}
			_, err = ts.Token()
			return err
		},
		BucketReachable: func(ctx context.Context, bucket string) error {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			c, err := gcs.NewClient(ctx, cfg)
			if err != nil {
				return err
			}
			defer c.Close()
			it := c.Bucket(bucket).Objects(ctx, &storage.Query{})
			it.PageInfo().MaxSize = 1
			if _, err := it.Next(); err != nil && !errors.Is(err, iterator.Done) {
				return err
			}
			return nil
		},
		Writable: writable,
		Docker: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if _, err := exec.LookPath("docker"); err != nil {
				return err
			}
			return exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run()
		},
	}
}

// writable creates dir if needed and proves a file can be written in it.
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
