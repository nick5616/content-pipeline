//go:build integration

package apply_test

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nick5616/canon/engine/apply"
	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/internal/testenv"
)

// realClient connects to fake-gcs-server (task test:integration) and makes
// fresh test buckets, so runs don't see each other.
func realClient(t *testing.T) gcs.Client {
	t.Helper()
	if os.Getenv("STORAGE_EMULATOR_HOST") == "" {
		t.Fatal("STORAGE_EMULATOR_HOST is unset; run via `task test:integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := gcs.NewClient(ctx, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	for _, b := range []string{testenv.Private, testenv.Archive} {
		// fake-gcs-server keeps buckets for the container's life; empty
		// them so every test starts clean.
		if err := c.Bucket(b).Create(ctx, "canon-test", nil); err != nil && !strings.Contains(err.Error(), "409") {
			t.Fatalf("create %s: %v", b, err)
		}
		objs, err := c.List(ctx, b, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			if err := c.Bucket(b).Object(o.Name).Delete(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	return c
}

func TestIntegrationConverges(t *testing.T) {
	c := realClient(t)
	e := testenv.Seeded(t, c, fixtures...)
	p := e.Plan()
	if p.Summary.Upload != 3 || p.Summary.Archive != 3 {
		t.Fatalf("%+v", p.Summary)
	}
	e.Apply(p)
	e.AssertEmpty()

	ctx := context.Background()
	for _, b := range []string{testenv.Private, testenv.Archive} {
		objs, err := c.List(ctx, b, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			data, _, err := c.Read(ctx, b, o.Name)
			if err != nil {
				t.Fatal(err)
			}
			if gcs.SHA256Hex(data) != o.SHA256 {
				t.Errorf("%s/%s: sha256 metadata %q doesn't match", b, o.Name, o.SHA256)
			}
		}
	}
	working, _, _ := e.Working.Bytes()
	remote, _, err := c.Read(ctx, testenv.Private, store.ManifestObject)
	if err != nil || !bytes.Equal(remote, working) {
		t.Fatalf("remote manifest differs: %v", err)
	}
}

// The generation precondition holds on the real API, not just the fake.
func TestIntegrationRemoteMovedRefuses(t *testing.T) {
	c := realClient(t)
	e := testenv.Seeded(t, c, "png.png")
	e.Converge()
	_, gen, _ := e.Working.Base()

	e.Ingest("jpeg.jpg")
	p := e.Plan()
	// Another machine applies in between.
	b := []byte(`{}`)
	if _, err := c.Write(context.Background(), testenv.Private, store.ManifestObject, bytes.NewReader(b),
		gcs.WriteOpts{SHA256: gcs.SHA256Hex(b), CRC32C: crc(b), IfGeneration: gen}); err != nil {
		t.Fatal(err)
	}
	_, err := apply.Apply(context.Background(), p, e.Deps(), nil)
	if !errors.Is(err, apply.ErrMoved) {
		t.Fatalf("want ErrMoved, got %v", err)
	}
}

func TestIntegrationPreconditions(t *testing.T) {
	c := realClient(t)
	ctx := context.Background()
	b := []byte("x")
	o := gcs.WriteOpts{SHA256: gcs.SHA256Hex(b), CRC32C: crc(b)}
	first, err := c.Write(ctx, testenv.Private, "t/obj", bytes.NewReader(b), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(ctx, testenv.Private, "t/obj", bytes.NewReader(b), o); !errors.Is(err, gcs.ErrPrecondition) {
		t.Fatalf("DoesNotExist: want ErrPrecondition, got %v", err)
	}
	o.IfGeneration = first.Generation + 1000
	if _, err := c.Write(ctx, testenv.Private, "t/obj", bytes.NewReader(b), o); !errors.Is(err, gcs.ErrPrecondition) {
		t.Fatalf("GenerationMatch: want ErrPrecondition, got %v", err)
	}
	o.IfGeneration = first.Generation
	if _, err := c.Write(ctx, testenv.Private, "t/obj", bytes.NewReader(b), o); err != nil {
		t.Fatalf("matching generation: %v", err)
	}
	if _, err := c.Attrs(ctx, testenv.Private, "t/none"); !errors.Is(err, gcs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	bad := gcs.WriteOpts{SHA256: o.SHA256, CRC32C: crc(b) + 1}
	if _, err := c.Write(ctx, testenv.Private, "t/badcrc", bytes.NewReader(b), bad); err == nil {
		t.Fatal("an upload with the wrong CRC32C was accepted")
	}
	if _, err := c.Attrs(ctx, testenv.Private, "t/badcrc"); !errors.Is(err, gcs.ErrNotExist) {
		t.Fatalf("a rejected upload left an object: %v", err)
	}
}

func crc(b []byte) uint32 { return crc32.Checksum(b, gcs.CRC32C) }
