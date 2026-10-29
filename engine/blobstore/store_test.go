package blobstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/sanitize"
	"github.com/nick5616/canon/internal/testmedia"
)

// verified sanitizes the JPEG fixture into dir and verifies it.
func verified(t *testing.T, dir string) sanitize.Verified {
	t.Helper()
	out := filepath.Join(dir, "out.jpg")
	in := filepath.Join(testmedia.Dir(t), "jpeg.jpg")
	if err := sanitize.NewTools().Sanitize(context.Background(), media.JPEG, in, out); err != nil {
		t.Fatal(err)
	}
	v, err := sanitize.NewVerifier().Verify(context.Background(), media.JPEG, out)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPutIsContentAddressedAndIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for range 2 {
		dir, _ := s.TempDir()
		v := verified(t, dir)
		ref, err := s.Put(v)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
		if _, err := os.Stat(v.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Error("staged file left behind")
		}
	}
	if refs[0] != refs[1] || !strings.HasPrefix(refs[0], "sha256:") {
		t.Fatalf("refs %v", refs)
	}
	sum := refs[0][len("sha256:"):]
	path := s.BlobPath(sum, "jpg")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("blob mode %v", fi.Mode().Perm())
	}
	if want := filepath.Join("blobs", "sha256", sum[:2], sum[2:4], sum+".jpg"); !strings.HasSuffix(path, want) {
		t.Errorf("path %s", path)
	}
}

func TestStage(t *testing.T) {
	s, _ := Open(t.TempDir())
	dir, _ := s.TempDir()
	o, err := s.Stage(dir, bytes.NewReader([]byte("hello")), "a.txt", 5)
	if err != nil || o.Size() != 5 || o.SHA256() != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("%+v %v", o, err)
	}
	dir2, _ := s.TempDir()
	if _, err := s.Stage(dir2, bytes.NewReader([]byte("hello!")), "b.txt", 5); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	kept, err := s.KeepOriginal(o, "txt")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kept); string(b) != "hello" {
		t.Fatal("kept original differs")
	}
}

func TestIndex(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, ok := s.Lookup("ab"); ok {
		t.Fatal("found nothing")
	}
	dir, _ := s.TempDir()
	ref, _ := s.Put(verified(t, dir))
	orig := strings.Repeat("c", 64)
	if err := s.Record(orig, IndexEntry{Blob: ref, Type: "jpeg", Ext: "jpg"}); err != nil {
		t.Fatal(err)
	}
	if e, ok := s.Lookup(orig); !ok || e.Blob != ref {
		t.Fatalf("lookup %+v %v", e, ok)
	}
	// An entry whose blob was evicted doesn't count.
	os.Remove(s.BlobPath(ref[len("sha256:"):], "jpg"))
	if _, ok := s.Lookup(orig); ok {
		t.Fatal("stale index entry returned")
	}
}

func TestPrivatePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	if _, err := Open(root); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"blobs/sha256", "originals/sha256", "tmp"} {
		fi, _ := os.Stat(filepath.Join(root, d))
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v", d, fi.Mode().Perm())
		}
	}
}
