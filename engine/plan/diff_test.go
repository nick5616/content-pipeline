package plan

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/canonjson"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

func hash(c byte) string { return strings.Repeat(string(c), 64) }

var (
	blobA, blobB, origA, origB = hash('a'), hash('b'), hash('c'), hash('d')
	manifestBytes              = []byte("{\"fake\":\"manifest\"}\n")
	rev                        = store.Revision(manifestBytes)
)

func item(id, slug string, assets ...manifest.AssetRef) manifest.Item {
	return manifest.Item{ID: manifest.Ulid(id), Slug: manifest.Slug(slug), Assets: assets}
}

func asset(blob, orig string) manifest.AssetRef {
	a := manifest.AssetRef{Blob: manifest.BlobRef("sha256:" + blob), Role: manifest.AssetRoleOriginal}
	if orig != "" {
		o := manifest.BlobRef("sha256:" + orig)
		a.Original = &o
	}
	return a
}

const idA, idB = "01JB7Z0000000000000000000A", "01JB7Z0000000000000000000B"

// desired: two items; A has blobA from origA; B shares blobA (from origB)
// and adds blobB with no original.
func desired() Desired {
	return Desired{
		Manifest: manifestBytes,
		Revision: rev,
		Items: []manifest.Item{
			item(idA, "sketch", asset(blobA, origA)),
			item(idB, "study", asset(blobA, origB), asset(blobB, "")),
		},
		Changes:   store.Changes{Added: []store.ItemRef{{ID: idA, Slug: "sketch"}, {ID: idB, Slug: "study"}}},
		Blobs:     map[string]LocalFile{blobA: {blobA, "png", 1000}, blobB: {blobB, "jpg", 2 << 20}},
		Originals: []LocalFile{{origA, "png", 3000}, {origB, "png", 3100}},
	}
}

func obj(name, sum string, gen int64) gcs.Object {
	return gcs.Object{Name: name, SHA256: sum, Generation: gen, Size: 10}
}

// everything is a listing where every desired object already exists.
func everything() Listing {
	return Listing{
		Blobs:     map[string]gcs.Object{blobA: obj(BlobPath(blobA, "png"), blobA, 1), blobB: obj(BlobPath(blobB, "jpg"), blobB, 2)},
		Originals: map[string]gcs.Object{origA: obj(OriginalPath(origA, "png"), origA, 3), origB: obj(OriginalPath(origB, "png"), origB, 4)},
		Manifest:  &gcs.Object{Name: store.ManifestObject, SHA256: gcs.SHA256Hex(manifestBytes), Generation: 41},
		History:   map[string]bool{rev: true},
	}
}

func empty() Listing {
	return Listing{Blobs: map[string]gcs.Object{}, Originals: map[string]gcs.Object{}, History: map[string]bool{}}
}

type opSpec struct {
	typ  OpType
	path string
}

func specs(ops []Op) []opSpec {
	var out []opSpec
	for _, o := range ops {
		out = append(out, opSpec{o.Type, o.Path})
	}
	return out
}

func codes(ps []manifest.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, string(p.Severity)+":"+p.Code)
	}
	return out
}

const utc = "20261028T120000Z"

func TestDiff(t *testing.T) {
	tests := []struct {
		name    string
		desired func(*Desired)
		listing func(*Listing)
		ops     []opSpec
		codes   []string
	}{
		{
			name:    "empty buckets: archive both, upload both (shared blob once), then manifest and history",
			listing: func(l *Listing) { *l = empty() },
			ops: []opSpec{
				{ArchiveOriginal, OriginalPath(origA, "png")},
				{ArchiveOriginal, OriginalPath(origB, "png")},
				{UploadBlob, BlobPath(blobA, "png")},
				{UploadBlob, BlobPath(blobB, "jpg")},
				{WriteManifest, store.ManifestObject},
				{WriteHistory, HistoryPath(utc, rev)},
			},
		},
		{name: "everything present: nothing to do"},
		{
			name:    "blob present remotely but evicted locally: fine",
			desired: func(d *Desired) { delete(d.Blobs, blobB) },
		},
		{
			name:    "blob in neither place: error",
			desired: func(d *Desired) { delete(d.Blobs, blobB) },
			listing: func(l *Listing) { delete(l.Blobs, blobB) },
			codes:   []string{"error:blob-missing"},
		},
		{
			name:    "private checksum mismatch with local copy: re-upload",
			listing: func(l *Listing) { l.Blobs[blobB] = obj(BlobPath(blobB, "jpg"), "", 2) },
			ops:     []opSpec{{UploadBlob, BlobPath(blobB, "jpg")}},
		},
		{
			name:    "private checksum mismatch without local copy: error",
			desired: func(d *Desired) { delete(d.Blobs, blobB) },
			listing: func(l *Listing) { l.Blobs[blobB] = obj(BlobPath(blobB, "jpg"), "nope", 2) },
			codes:   []string{"error:blob-checksum"},
		},
		{
			name:    "archive checksum mismatch: error, never an overwrite",
			listing: func(l *Listing) { l.Originals[origA] = obj(OriginalPath(origA, "png"), "", 3) },
			codes:   []string{"error:archive-checksum"},
		},
		{
			name:    "original nowhere: warning only",
			desired: func(d *Desired) { d.Originals = d.Originals[1:] },
			listing: func(l *Listing) { delete(l.Originals, origA) },
			codes:   []string{"warning:original-missing"},
		},
		{
			name:    "unreferenced original in the cache still gets archived (D34)",
			desired: func(d *Desired) { d.Originals = append(d.Originals, LocalFile{hash('e'), "jpg", 5}) },
			ops:     []opSpec{{ArchiveOriginal, OriginalPath(hash('e'), "jpg")}},
		},
		{
			name: "orphans are reported, not written",
			listing: func(l *Listing) {
				l.Blobs[hash('f')] = obj(BlobPath(hash('f'), "gif"), hash('f'), 9)
				l.Strays = []gcs.Object{obj("blobs/stray.txt", "", 10)}
			},
			ops: []opSpec{{Orphan, BlobPath(hash('f'), "gif")}, {Orphan, "blobs/stray.txt"}},
		},
		{
			name:    "manifest changed: write it and snapshot it",
			listing: func(l *Listing) { l.Manifest.SHA256 = "old"; l.History = map[string]bool{} },
			ops:     []opSpec{{WriteManifest, store.ManifestObject}, {WriteHistory, HistoryPath(utc, rev)}},
		},
		{
			name:    "history missing for the current manifest (crash after the manifest write)",
			listing: func(l *Listing) { l.History = map[string]bool{} },
			ops:     []opSpec{{WriteHistory, HistoryPath(utc, rev)}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, l := desired(), everything()
			if tt.desired != nil {
				tt.desired(&d)
			}
			if tt.listing != nil {
				tt.listing(&l)
			}
			ops, ps := Diff(d, l, utc)
			if got, want := specs(ops), tt.ops; len(got) != len(want) || (len(want) > 0 && !equal(got, want)) {
				t.Errorf("ops:\n got %v\nwant %v", got, want)
			}
			if got := codes(ps); strings.Join(got, ",") != strings.Join(tt.codes, ",") {
				t.Errorf("problems: got %v (%v), want %v", got, ps, tt.codes)
			}
		})
	}
}

func equal(a, b []opSpec) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The same inputs give the same ops in the same order, every time.
func TestDiffDeterministic(t *testing.T) {
	first, _ := Diff(desired(), empty(), utc)
	want, _ := canonjson.Marshal(first)
	for range 50 {
		ops, _ := Diff(desired(), empty(), utc)
		if got, _ := canonjson.Marshal(ops); !bytes.Equal(got, want) {
			t.Fatal("diff output changed between runs")
		}
	}
}

func goldenPlan() *Plan {
	ops, ps := Diff(desired(), empty(), utc)
	l := everything()
	l.Blobs[hash('f')] = obj(BlobPath(hash('f'), "gif"), hash('f'), 9)
	orphans, _ := Diff(desired(), l, utc)
	ops = append(ops, orphans...)
	return &Plan{ID: "01JB7Z00000000000000000P1N", Generation: 0, Revision: rev, Ops: ops, Problems: append(ps, manifest.Problem{
		Code: "link-not-owned", Severity: manifest.Warning, Path: "/items/0/links/0", Message: "example.com is not an owned domain",
	}), Summary: Summary{Archive: 2, Upload: 2, Orphaned: 1}}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (run with -update if intended):\n%s", name, got)
	}
}

func TestRenderGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, goldenPlan()); err != nil {
		t.Fatal(err)
	}
	golden(t, "plan.txt", buf.Bytes())

	buf.Reset()
	if err := Render(&buf, &Plan{Generation: 42, Ops: []Op{{Type: Orphan, Path: BlobPath(hash('f'), "gif"), Bucket: Private, Bytes: ptr(int64(10))}}}); err != nil {
		t.Fatal(err)
	}
	golden(t, "plan-empty.txt", buf.Bytes())
}

func TestPlanJSONGolden(t *testing.T) {
	b, err := canonjson.Marshal(goldenPlan())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "plan.json", b)
}

func TestSaveOpen(t *testing.T) {
	dir := t.TempDir()
	p := goldenPlan()
	path, err := Save(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("plan file mode %v, want 0600 (plans name drafts)", fi.Mode().Perm())
	}
	for _, ref := range []string{p.ID, path} {
		got, err := Open(dir, ref)
		if err != nil || got.ID != p.ID || len(got.Ops) != len(p.Ops) {
			t.Fatalf("Open(%s): %+v, %v", ref, got, err)
		}
	}
	if _, err := Open(dir, "01JB7Z00000000000000000XXX"); err == nil {
		t.Fatal("want not found")
	}
}

func TestParseCAS(t *testing.T) {
	if _, sum, ext, ok := ParseCAS(BlobPath(blobA, "png")); !ok || sum != blobA || ext != "png" {
		t.Fatal("round trip failed")
	}
	for _, bad := range []string{
		"blobs/sha256/ab/cd/" + blobA + ".png", // shards don't match the hash
		"blobs/sha256/aa/aa/" + blobA,          // no extension
		"blobs/" + blobA + ".png",
		"originals/sha256/aa/aa/" + blobA + ".PNG",
	} {
		if _, _, _, ok := ParseCAS(bad); ok {
			t.Errorf("accepted %s", bad)
		}
	}
}
