//go:build integration

package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/nick5616/canon/engine/config"
)

// Runs against fake-gcs-server from docker-compose.test.yml. `task
// test:integration` starts it and sets STORAGE_EMULATOR_HOST.
func TestFakeGCSSmoke(t *testing.T) {
	host := os.Getenv("STORAGE_EMULATOR_HOST")
	if host == "" {
		t.Fatal("STORAGE_EMULATOR_HOST is unset; run via `task test:integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	waitReady(ctx, t, "http://"+host+"/storage/v1/b?project=canon-test")

	c, err := NewClient(ctx, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	bucket := fmt.Sprintf("canon-smoke-%d", time.Now().UnixNano())
	if err := c.Bucket(bucket).Create(ctx, "canon-test", nil); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := c.BucketAttrs(ctx, bucket); err != nil {
		t.Fatalf("attrs: %v", err)
	}

	w := c.Bucket(bucket).Object("manifest/manifest.json").If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	w.ContentType = "application/json"
	if _, err := io.WriteString(w, "{}\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("write: %v", err)
	}

	var names []string
	it := c.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: "manifest/"})
	for {
		a, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		names = append(names, a.Name)
	}
	if len(names) != 1 || names[0] != "manifest/manifest.json" {
		t.Fatalf("listed %v", names)
	}
}

func waitReady(ctx context.Context, t *testing.T, url string) {
	t.Helper()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("fake-gcs-server not ready at %s", url)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
