package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/surfaces"
)

const bucket = "priv"

func syncEnv(t *testing.T) (*Working, *gcs.Mem, Remote) {
	t.Helper()
	mem := gcs.NewMem(bucket)
	return Open(t.TempDir(), "Test Person", surfaces.MustDefault()), mem, Remote{API: mem, Bucket: bucket}
}

// remoteManifest is a valid manifest with one item titled title.
func remoteManifest(t *testing.T, title string) []byte {
	t.Helper()
	m := manifest.Empty("Test Person")
	m.Items = []manifest.Item{{ID: "01JB7Z0000000000000000000A", Slug: "sketch", Kind: manifest.KindArtwork, Title: title, Visibility: manifest.ItemVisibilityDraft}}
	b, err := manifest.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func push(mem *gcs.Mem, b []byte) int64 {
	return mem.Put(bucket, ManifestObject, b, gcs.SHA256Hex(b))
}

func edit(t *testing.T, w *Working, title string) {
	t.Helper()
	if _, err := w.Update(func(m *manifest.Manifest) error {
		if len(m.Items) == 0 {
			m.Items = append(m.Items, manifest.Item{ID: "01JB7Z0000000000000000000B", Slug: "local", Kind: manifest.KindArtwork, Visibility: manifest.ItemVisibilityDraft})
		}
		m.Items[0].Title = title
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func state(t *testing.T, w *Working, r Remote) Status {
	t.Helper()
	st, err := w.Status(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStatusStates(t *testing.T) {
	ctx := context.Background()

	// Fresh machine, empty bucket.
	w, mem, r := syncEnv(t)
	if st := state(t, w, r); st.State != InSync || *st.RemoteGeneration != 0 || st.BaseGeneration != 0 {
		t.Fatalf("fresh: %+v", st)
	}
	// Local edits, nothing remote.
	edit(t, w, "Mine")
	if st := state(t, w, r); st.State != Edited || !st.LocalEdits {
		t.Fatalf("edited: %+v", st)
	}

	// Remote exists, nothing local: behind, and Fresh pulls it.
	w, mem, r = syncEnv(t)
	gen := push(mem, remoteManifest(t, "Remote"))
	if st := state(t, w, r); st.State != Behind {
		t.Fatalf("behind: %+v", st)
	}
	st, err := w.Fresh(ctx, r)
	if err != nil || st.State != InSync || st.BaseGeneration != gen {
		t.Fatalf("fresh pull: %+v %v", st, err)
	}
	if b, _, _ := w.Bytes(); !bytes.Equal(b, remoteManifest(t, "Remote")) {
		t.Fatal("working isn't the remote after pull")
	}

	// Edit on top of base, then the remote moves: diverged.
	edit(t, w, "Mine")
	if st := state(t, w, r); st.State != Edited {
		t.Fatalf("edited on base: %+v", st)
	}
	push(mem, remoteManifest(t, "Other machine"))
	if st := state(t, w, r); st.State != Diverged {
		t.Fatalf("diverged: %+v", st)
	}
	if _, err := w.Fresh(ctx, r); !errors.Is(err, ErrDiverged) {
		t.Fatalf("Fresh should refuse, got %v", err)
	}
	if _, err := w.Pull(ctx, r, false); !errors.Is(err, ErrDiverged) {
		t.Fatalf("Pull should refuse, got %v", err)
	}
	if b, _, _ := w.Bytes(); !bytes.Contains(b, []byte("Mine")) {
		t.Fatal("refused pull touched working")
	}
	st, err = w.Pull(ctx, r, true)
	if err != nil || st.State != InSync {
		t.Fatalf("discard-local: %+v %v", st, err)
	}
	if b, _, _ := w.Bytes(); !bytes.Contains(b, []byte("Other machine")) {
		t.Fatal("discard-local didn't take the remote")
	}
}

func TestPullWithEditsKeepsThem(t *testing.T) {
	w, mem, r := syncEnv(t)
	push(mem, remoteManifest(t, "Remote"))
	if _, err := w.Pull(context.Background(), r, false); err != nil {
		t.Fatal(err)
	}
	edit(t, w, "Mine")
	st, err := w.Pull(context.Background(), r, false)
	if err != nil || st.State != Edited {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestOffline(t *testing.T) {
	w, mem, r := syncEnv(t)
	mem.Fault = func(gcs.Call) error { return fmt.Errorf("%w: dial tcp: no route", gcs.ErrOffline) }
	st := state(t, w, r)
	if st.State != Offline || st.RemoteGeneration != nil {
		t.Fatalf("%+v", st)
	}
	if _, err := w.Fresh(context.Background(), r); !errors.Is(err, gcs.ErrOffline) {
		t.Fatalf("Fresh: %v", err)
	}
	// Editing still works.
	edit(t, w, "Offline edit")
}

func TestInvalidRemoteRefused(t *testing.T) {
	w, mem, r := syncEnv(t)
	push(mem, []byte(`{"schemaVersion": 1}`))
	if _, err := w.Pull(context.Background(), r, false); !errors.Is(err, ErrRemoteInvalid) {
		t.Fatalf("want ErrRemoteInvalid, got %v", err)
	}
	if _, exists, _ := w.Bytes(); exists {
		t.Fatal("invalid remote was written locally")
	}
}

func TestTornBaseIsIgnored(t *testing.T) {
	w, _, _ := syncEnv(t)
	if err := w.SetBase(remoteManifest(t, "A"), 7); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(w.dir+"/"+baseFile, []byte("half")); err != nil {
		t.Fatal(err)
	}
	if b, gen, err := w.Base(); b != nil || gen != 0 || err != nil {
		t.Fatalf("torn base should read as none: %q %d %v", b, gen, err)
	}
}

func TestUpdateIf(t *testing.T) {
	w, _, _ := syncEnv(t)
	_, rev, _, _ := w.Load()
	newRev, err := w.UpdateIf(rev, func(m *manifest.Manifest) error {
		m.OwnedDomains = append(m.OwnedDomains, "example.com")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.UpdateIf(rev, func(*manifest.Manifest) error { return nil }); !errors.Is(err, ErrStale) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err := w.UpdateIf(newRev, func(*manifest.Manifest) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestDiff(t *testing.T) {
	a := remoteManifest(t, "A")
	m := manifest.Empty("Test Person")
	m.Items = []manifest.Item{
		{ID: "01JB7Z0000000000000000000A", Slug: "sketch", Kind: manifest.KindArtwork, Title: "Changed", Visibility: manifest.ItemVisibilityDraft},
		{ID: "01JB7Z0000000000000000000C", Slug: "new", Kind: manifest.KindArtwork, Title: "New", Visibility: manifest.ItemVisibilityDraft},
	}
	b, _ := manifest.Marshal(m)
	c, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Added) != 1 || len(c.Changed) != 1 || len(c.Removed) != 0 || c.Other {
		t.Fatalf("%+v", c)
	}
	c, _ = Diff(b, a)
	if len(c.Removed) != 1 || len(c.Changed) != 1 {
		t.Fatalf("reverse: %+v", c)
	}
	if c, _ := Diff(a, a); !c.Empty() {
		t.Fatalf("same: %+v", c)
	}
	if c, _ := Diff(nil, a); len(c.Added) != 1 || !c.Other {
		t.Fatalf("from nothing: %+v", c)
	}
}
