package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/toolchain"
)

// fakeEnv is a machine where everything works.
func fakeEnv(t *testing.T) Env {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default(func(k string) string {
		return map[string]string{"HOME": dir}[k]
	})
	cfg.Project = "my-proj"
	cfg.PrivateBucket, cfg.PublicBucket, cfg.ArchiveBucket = "canon-priv", "canon-pub", "canon-arch"
	return Env{
		Config:     cfg,
		ConfigPath: "/cfg/config.yaml",
		LookupTool: func(_ context.Context, name string) (toolchain.Tool, error) {
			return toolchain.Tool{Name: name, Path: "/usr/bin/" + name, Version: "1.0"}, nil
		},
		Credentials: func(context.Context) error { return nil },
		BucketAttrs: func(context.Context, string) error { return nil },
		Writable:    writable,
		Docker:      func(context.Context) error { return nil },
	}
}

func statuses(r Report) map[string]Status {
	m := map[string]Status{}
	for _, c := range r.Checks {
		m[c.Name] = c.Status
	}
	return m
}

func expect(t *testing.T, r Report, want map[string]Status) {
	t.Helper()
	got := statuses(r)
	for name, s := range want {
		if got[name] != s {
			t.Errorf("%s: got %q, want %q (report: %+v)", name, got[name], s, r.Checks)
		}
	}
}

func TestAllOK(t *testing.T) {
	r := Run(context.Background(), fakeEnv(t))
	for _, c := range r.Checks {
		if c.Status != OK {
			t.Errorf("%s: %s %s", c.Name, c.Status, c.Detail)
		}
	}
	if len(r.Tools) != len(toolchain.Required) || r.Failed() {
		t.Fatalf("tools %v failed=%v", r.Tools, r.Failed())
	}
	if len(r.Checks) != 1+2+len(toolchain.Required)+1+3+1 {
		t.Errorf("unexpected checks: %+v", r.Checks)
	}
}

func TestToolMissingFails(t *testing.T) {
	env := fakeEnv(t)
	inner := env.LookupTool
	env.LookupTool = func(ctx context.Context, name string) (toolchain.Tool, error) {
		if name == "qpdf" {
			return toolchain.Tool{}, errors.New("qpdf: not found on PATH")
		}
		return inner(ctx, name)
	}
	r := Run(context.Background(), env)
	expect(t, r, map[string]Status{"qpdf": Fail, "ffmpeg": OK})
	if !r.Failed() {
		t.Fatal("report should fail")
	}
}

func TestVersionChangeWarns(t *testing.T) {
	env := fakeEnv(t)
	Run(context.Background(), env) // records 1.0 for everything

	inner := env.LookupTool
	env.LookupTool = func(ctx context.Context, name string) (toolchain.Tool, error) {
		tool, err := inner(ctx, name)
		if name == "exiftool" {
			tool.Version = "13.0"
		}
		return tool, err
	}
	r := Run(context.Background(), env)
	expect(t, r, map[string]Status{"exiftool": Warn, "ffmpeg": OK})
	if r.Failed() {
		t.Fatal("a version change warns, it doesn't fail")
	}

	// The new version is now the baseline.
	r = Run(context.Background(), env)
	expect(t, r, map[string]Status{"exiftool": OK})
}

func TestMissingToolKeepsRecordedVersion(t *testing.T) {
	env := fakeEnv(t)
	Run(context.Background(), env)
	inner := env.LookupTool
	env.LookupTool = func(ctx context.Context, name string) (toolchain.Tool, error) {
		if name == "qpdf" {
			return toolchain.Tool{}, errors.New("gone")
		}
		return inner(ctx, name)
	}
	Run(context.Background(), env)
	env.LookupTool = inner
	r := Run(context.Background(), env)
	expect(t, r, map[string]Status{"qpdf": OK})
}

func TestBucketsUnsetSkipped(t *testing.T) {
	env := fakeEnv(t)
	env.Config.PrivateBucket, env.Config.PublicBucket, env.Config.ArchiveBucket = "", "", ""
	env.BucketAttrs = func(context.Context, string) error { t.Fatal("must not call GCS"); return nil }
	r := Run(context.Background(), env)
	expect(t, r, map[string]Status{"buckets": Skipped})
	if r.Failed() {
		t.Fatal("unset buckets shouldn't fail")
	}
}

func TestBucketUnreachableFails(t *testing.T) {
	env := fakeEnv(t)
	env.BucketAttrs = func(_ context.Context, b string) error {
		if b == "canon-pub" {
			return errors.New("storage: bucket doesn't exist")
		}
		return nil
	}
	expect(t, Run(context.Background(), env), map[string]Status{"public bucket": Fail, "private bucket": OK})
}

func TestNoCredentialsFailsAndSkipsBuckets(t *testing.T) {
	env := fakeEnv(t)
	env.Credentials = func(context.Context) error { return errors.New("no ADC") }
	expect(t, Run(context.Background(), env), map[string]Status{"credentials": Fail, "buckets": Skipped})
}

func TestUnwritableCacheFails(t *testing.T) {
	env := fakeEnv(t)
	env.Writable = func(dir string) error {
		if dir == env.Config.CacheDir {
			return fs.ErrPermission
		}
		return writable(dir)
	}
	r := Run(context.Background(), env)
	expect(t, r, map[string]Status{"cache dir": Fail, "state dir": OK})
	if !r.Failed() {
		t.Fatal("report should fail")
	}
}

func TestUnwritableStateSkipsRecord(t *testing.T) {
	env := fakeEnv(t)
	env.Writable = func(string) error { return fs.ErrPermission }
	Run(context.Background(), env)
	if _, err := os.Stat(filepath.Join(env.Config.StateDir, StateFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("doctor.json should not be written: %v", err)
	}
}

func TestConfig(t *testing.T) {
	env := fakeEnv(t)
	env.ConfigErr = fmt.Errorf("open: %w", fs.ErrNotExist)
	expect(t, Run(context.Background(), env), map[string]Status{"config": Warn})

	env = fakeEnv(t)
	env.ConfigErr = errors.New("parse config: yaml: line 3")
	expect(t, Run(context.Background(), env), map[string]Status{"config": Fail})

	env = fakeEnv(t)
	env.Config.PublicBucket = "artandmedia"
	env.BucketAttrs = func(context.Context, string) error { t.Fatal("must not call GCS"); return nil }
	expect(t, Run(context.Background(), env), map[string]Status{"config": Fail, "buckets": Skipped})
}

func TestDockerOnlyWarns(t *testing.T) {
	env := fakeEnv(t)
	env.Docker = func(context.Context) error { return errors.New("cannot connect") }
	r := Run(context.Background(), env)
	expect(t, r, map[string]Status{"docker": Warn})
	if r.Failed() {
		t.Fatal("docker is dev-only")
	}
}
