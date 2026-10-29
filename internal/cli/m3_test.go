package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/internal/testenv"
	"github.com/nick5616/canon/internal/testmedia"
)

// cliEnv writes a config file for a temp cache and state dir and points
// the GCS client at a fake.
func cliEnv(t *testing.T) (run func(args ...string) (string, error), ok func(args ...string) string, mem *gcs.Mem) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := fmt.Sprintf("project: canon-test\nprivateBucket: %s\narchiveBucket: %s\ncacheDir: %s\nstateDir: %s\nprofileName: Test Person\n",
		testenv.Private, testenv.Archive, filepath.Join(dir, "cache"), filepath.Join(dir, "state"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	mem = testenv.Mem()
	old := newGCS
	newGCS = func(context.Context, config.Config) (gcs.API, error) { return mem, nil }
	t.Cleanup(func() { newGCS = old })
	run = func(args ...string) (string, error) {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader(""))
		root.SetArgs(append([]string{"--config", cfgPath}, args...))
		err := root.Execute()
		return out.String(), err
	}
	ok = func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("canon %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	return run, ok, mem
}

var planID = regexp.MustCompile(`canon apply ([0-9A-Z]{26})`)

// The done criterion: ingest, plan, apply, plan again -> "No changes."
func TestIngestPlanApplyPlan(t *testing.T) {
	run, ok, mem := cliEnv(t)
	media := testmedia.Dir(t)
	ok("ingest", filepath.Join(media, "png.png"), filepath.Join(media, "jpeg.jpg"))

	out := ok("status")
	if !strings.Contains(out, "local edits") {
		t.Fatalf("status: %s", out)
	}
	out = ok("plan")
	if !strings.Contains(out, "2 to archive, 2 to upload") {
		t.Fatalf("plan: %s", out)
	}
	id := planID.FindStringSubmatch(out)[1]
	ok("plan", "show", id)

	out = ok("apply", id)
	if !strings.Contains(out, "[6/6]") || !strings.Contains(out, "now generation") {
		t.Fatalf("apply: %s", out)
	}
	out = ok("plan")
	if !strings.HasPrefix(out, "No changes.") {
		t.Fatalf("second plan: %s", out)
	}
	if len(mem.Names(testenv.Archive)) != 2 {
		t.Fatalf("archive: %v", mem.Names(testenv.Archive))
	}

	// Applying the same plan again: the remote moved on.
	if _, err := run("apply", id); err == nil || !strings.Contains(err.Error(), "remote manifest changed") {
		t.Fatalf("reapply: %v", err)
	}
}

func TestItemCommands(t *testing.T) {
	run, ok, _ := cliEnv(t)
	ok("item", "new", "project", "rpg-quests", "RPG Quests", "summary=Quests from journal entries.")
	ok("taxonomy", "set", "tags", "journaling", "label=Journaling")
	ok("item", "set", "rpg-quests", `tags=["journaling"]`, "date.value=2025-06", "aliases=[\"videogamequest\"]")

	out := ok("show", "videogamequest")
	for _, want := range []string{`"title": "RPG Quests"`, `"journaling"`, `"value": "2025-06"`} {
		if !strings.Contains(out, want) {
			t.Errorf("show is missing %s:\n%s", want, out)
		}
	}
	out = ok("ls", "--tag", "journaling")
	if !strings.Contains(out, "rpg-quests") {
		t.Fatalf("ls: %s", out)
	}
	if out := ok("taxonomy", "ls", "tags"); !strings.Contains(out, "Journaling") {
		t.Fatalf("taxonomy ls: %s", out)
	}

	// An invalid edit changes nothing and says why.
	out, err := run("item", "set", "rpg-quests", "tags=[\"nope\"]")
	if err == nil || !strings.Contains(out, "nope") {
		t.Fatalf("invalid edit: %v\n%s", err, out)
	}

	out = ok("diff")
	if !strings.Contains(out, "+ ") || !strings.Contains(out, "rpg-quests") {
		t.Fatalf("diff: %s", out)
	}
	ok("item", "rm", "rpg-quests")
	if out := ok("ls"); strings.Contains(out, "rpg-quests") {
		t.Fatalf("still listed: %s", out)
	}
}

func TestPullDiscardLocal(t *testing.T) {
	run, ok, mem := cliEnv(t)
	ok("item", "new", "writing", "essay", "Essay")
	m := `{"items":[],"ownedDomains":[],"profile":{"name":"Other"},"schemaVersion":1,"surfaces":[],"taxonomy":{"categories":[],"mediums":[],"tags":[],"tech":[],"viewports":[]}}`
	mem.Put(testenv.Private, "manifest/manifest.json", []byte(m), gcs.SHA256Hex([]byte(m)))
	if out, err := run("plan"); err == nil || !strings.Contains(err.Error(), "local edits") {
		t.Fatalf("plan should refuse when diverged: %v\n%s", err, out)
	}
	if _, err := run("pull"); err == nil {
		t.Fatal("pull should refuse")
	}
	ok("pull", "--discard-local")
	if out := ok("ls"); strings.Contains(out, "essay") {
		t.Fatalf("local edit survived discard: %s", out)
	}
}
