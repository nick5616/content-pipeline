package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/internal/testenv"
	"github.com/nick5616/canon/internal/testmedia"
)

// Import a bucket, rerun it, then plan: the drafts flow into the same
// archive-and-upload plan as dropped files, and the source is never written.
func TestImportBucket(t *testing.T) {
	_, ok, _ := cliEnv(t)
	mem := gcs.NewMem(testenv.Private, testenv.Archive, "old-art")
	old := newGCS
	newGCS = func(context.Context, config.Config) (gcs.API, error) { return mem, nil }
	t.Cleanup(func() { newGCS = old })

	media := testmedia.Dir(t)
	read := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join(media, n))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	mem.PutMeta("old-art", "paintings/", nil, nil)
	mem.PutMeta("old-art", "paintings/sun.png", read("png.png"), map[string]string{"art-name": "Sun", "art-date": "Circa 2022"})
	mem.PutMeta("old-art", "sketches/cat.gif", read("gif.gif"), map[string]string{"art-date": "3"})

	out := ok("import", "bucket", "gs://old-art/")
	for _, want := range []string{"draft sun in paintings", "draft cat in sketches", `art-date "3" isn't a date`,
		"2 imported, 0 already there, 1 skipped, 0 failed; 1 with warnings", "imports/old-art.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("import output lacks %q:\n%s", want, out)
		}
	}
	report := strings.TrimSpace(strings.SplitN(strings.SplitN(out, "Report: ", 2)[1], "\n", 2)[0])
	var rep struct {
		Results []struct{ Object, Item string }
	}
	b, err := os.ReadFile(report)
	if err != nil || json.Unmarshal(b, &rep) != nil || len(rep.Results) != 3 {
		t.Fatalf("report %s: %v %s", report, err, b)
	}

	if out := ok("import", "bucket", "gs://old-art"); !strings.Contains(out, "0 imported, 2 already there") {
		t.Errorf("rerun: %s", out)
	}
	out = ok("plan")
	if !strings.Contains(out, "2 to archive, 2 to upload") {
		t.Errorf("plan: %s", out)
	}
	for _, c := range mem.Calls {
		if c.Bucket == "old-art" && c.Method != "list" && c.Method != "read" {
			t.Errorf("source bucket got a %s call", c.Method)
		}
	}
}

func TestParseBucketURL(t *testing.T) {
	for in, want := range map[string][2]string{
		"gs://a-b": {"a-b", ""}, "gs://a-b/": {"a-b", ""}, "gs://a-b/dj-crate/": {"a-b", "dj-crate/"},
	} {
		if b, p, err := parseBucketURL(in); err != nil || b != want[0] || p != want[1] {
			t.Errorf("%s: %q %q %v", in, b, p, err)
		}
	}
	for _, bad := range []string{"a-b", "gs://", "https://storage.googleapis.com/a-b"} {
		if _, _, err := parseBucketURL(bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}
