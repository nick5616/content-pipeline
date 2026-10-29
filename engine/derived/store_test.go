package derived

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var key = strings.Repeat("a", 64)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCommitAndLookup(t *testing.T) {
	s := open(t)
	scratch, _ := s.TempDir()
	o, err := JSON(scratch, "peaks.json", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(scratch, "intermediate.png"), []byte("x"), 0o600)
	d, err := s.Commit(scratch, Receipt{Jobkey: key, Recipe: "peaks@v1", Source: "sha256:" + key}, []Output{o})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Path("intermediate.png")); err == nil {
		t.Error("an unlisted file was kept")
	}
	got, ok := s.Lookup(key)
	if !ok || len(got.Receipt().Files) != 1 || got.Receipt().Files[0].Size != 7 || got.ReceiptSHA256() != d.ReceiptSHA256() {
		t.Fatalf("lookup: %+v %v", got.Receipt(), ok)
	}
	// A missing file makes the job not done.
	os.Remove(d.Path("peaks.json"))
	if _, ok := s.Lookup(key); ok {
		t.Error("incomplete job dir counted as done")
	}
}

func TestEmptyFilesIsAList(t *testing.T) {
	s := open(t)
	scratch, _ := s.TempDir()
	d, err := s.Commit(scratch, Receipt{Jobkey: key, Recipe: "palette@v1", Facts: map[string]any{"palette": []string{"#000000"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(d.ReceiptBytes(), &raw)
	if fs, ok := raw["files"].([]any); !ok || len(fs) != 0 {
		t.Errorf("files: %v", raw["files"])
	}
}

func TestCommitRefuses(t *testing.T) {
	s := open(t)
	other := t.TempDir()
	scratch, _ := s.TempDir()
	outside, _ := JSON(other, "a.json", 1)
	inside, _ := JSON(scratch, "a.json", 1)
	receipt, _ := JSON(scratch, ReceiptName, 1)
	bad, _ := JSON(scratch, "Bad Name.json", 1)
	for name, c := range map[string]struct {
		key  string
		outs []Output
	}{
		"bad key":       {"nope", []Output{inside}},
		"outside":       {key, []Output{outside}},
		"twice":         {key, []Output{inside, inside}},
		"receipt name":  {key, []Output{receipt}},
		"bad file name": {key, []Output{bad}},
	} {
		if _, err := s.Commit(scratch, Receipt{Jobkey: c.key}, c.outs); err == nil {
			t.Errorf("%s: committed", name)
		}
	}
}
