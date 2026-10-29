package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/surfaces"
)

var reg = surfaces.MustDefault()

func addItem(slug string) func(*manifest.Manifest) error {
	return func(m *manifest.Manifest) error {
		m.Items = append(m.Items, manifest.Item{
			ID: manifest.Ulid(ids.New().String()), Slug: manifest.Slug(slug),
			Kind: manifest.KindArtwork, Visibility: manifest.ItemVisibilityDraft, Title: slug,
		})
		return nil
	}
}

func TestFirstUpdateCreatesManifest(t *testing.T) {
	dir := t.TempDir()
	w := Open(dir, "Nicole Belovoskey", reg)
	m, rev0, _, err := w.Load()
	if err != nil || len(m.Items) != 0 || m.Profile.Name != "Nicole Belovoskey" {
		t.Fatalf("%+v %v", m, err)
	}
	rev1, err := w.Update(addItem("first"))
	if err != nil {
		t.Fatal(err)
	}
	if rev1 == rev0 {
		t.Fatal("revision didn't change")
	}
	m, rev, _, _ := w.Load()
	if rev != rev1 || len(m.Items) != 1 {
		t.Fatalf("reload: %s %d", rev, len(m.Items))
	}
	if fi, _ := os.Stat(filepath.Join(dir, File)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestNoProfileName(t *testing.T) {
	if _, err := Open(t.TempDir(), "", reg).Update(addItem("x")); !errors.Is(err, ErrNoProfileName) {
		t.Fatalf("got %v", err)
	}
}

func TestInvalidUpdateWritesNothing(t *testing.T) {
	w := Open(t.TempDir(), "N", reg)
	rev, _ := w.Update(addItem("a"))
	_, err := w.Update(addItem("a")) // duplicate slug
	var ie *InvalidError
	if !errors.As(err, &ie) {
		t.Fatalf("got %v", err)
	}
	if _, now, _, _ := w.Load(); now != rev {
		t.Fatal("invalid update was written")
	}
	if _, err := w.Update(func(*manifest.Manifest) error { return errors.New("nope") }); err == nil {
		t.Fatal("fn error swallowed")
	}
}

// Concurrent writers (the CLI and canon serve) never lose an update.
func TestConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A fresh Working per writer: separate lock file handles, as
			// separate processes would have.
			_, err := Open(dir, "N", reg).Update(addItem(fmt.Sprintf("item-%d", i)))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	m, _, _, _ := Open(dir, "N", reg).Load()
	if len(m.Items) != 20 {
		t.Fatalf("lost updates: %d of 20", len(m.Items))
	}
}

// A crash mid-write leaves a temp file behind and the old manifest intact.
func TestCrashLeavesOldFile(t *testing.T) {
	dir := t.TempDir()
	w := Open(dir, "N", reg)
	rev, _ := w.Update(addItem("a"))
	os.WriteFile(filepath.Join(dir, ".working-123.json"), []byte(`{"half`), 0o600)
	if _, now, _, err := w.Load(); err != nil || now != rev {
		t.Fatalf("%v %s", err, now)
	}
}
