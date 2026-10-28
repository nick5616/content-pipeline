package manifest

import (
	"bytes"
	"encoding/json"
	"flag"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/nick5616/canon/engine/surfaces"
)

var update = flag.Bool("update", false, "rewrite valid/full.json in canonical form")

const testdata = "../../schema/testdata"

type expectation struct {
	File        string   `json:"file"`
	Schema      string   `json:"schema"`
	SchemaValid bool     `json:"schemaValid"`
	Codes       []string `json:"codes"`
}

func expectations(t *testing.T) []expectation {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, "expectations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var e struct{ Cases []expectation }
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	return e.Cases
}

func codes(ps []Problem) []string {
	set := map[string]bool{}
	for _, p := range ps {
		set[p.Code] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Each fixture fails with exactly the expected codes, and the schema verdict
// matches what Vitest checks through Zod.
func TestFixtures(t *testing.T) {
	cases := expectations(t)
	if len(cases) < 40 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.File, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(testdata, c.File))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			sp := SchemaProblems(c.Schema+".schema.json", doc)
			if (len(sp) == 0) != c.SchemaValid {
				t.Fatalf("schemaValid: want %v, got problems %v", c.SchemaValid, sp)
			}

			var got []string
			if c.Schema == "manifest" || len(sp) == 0 {
				_, ps, err := Load(bytes.NewReader(b))
				if err != nil {
					t.Fatal(err)
				}
				got = codes(ps)
			} else {
				got = codes(sp)
			}
			want := slices.Clone(c.Codes)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				_, ps, _ := Load(bytes.NewReader(b))
				t.Fatalf("codes: want %v, got %v\n%v", want, got, ps)
			}
		})
	}
}

// Every semantic rule has at least one fixture.
func TestEveryRuleHasAFixture(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range expectations(t) {
		for _, code := range c.Codes {
			covered[code] = true
		}
	}
	for _, code := range []string{
		CodeSchema, CodeDuplicateID, CodeDuplicateSlug, CodeAliasCollision, CodeDuplicateTaxonomy,
		CodeUnknownTaxonomy, CodeDuplicateSurface, CodeUnregisteredSurface, CodeUnknownSurface,
		CodePlacementConfig, CodePlacementKey, CodeMemberMissing, CodeMemberSelf, CodeMemberCycle,
		CodeNotOwnedDomain, CodeLinkNotOwned, CodeSharedPlatformWildcard, CodeBadDate,
	} {
		if !covered[code] {
			t.Errorf("no fixture produces %s", code)
		}
	}
}

func readFull(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, "valid/full.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func load(t *testing.T, b []byte) *Manifest {
	t.Helper()
	m, ps, err := Load(bytes.NewReader(b))
	if err != nil || HasErrors(ps) {
		t.Fatalf("load: %v %v", err, ps)
	}
	return m
}

// Load then Marshal of the canonical golden file gives the same bytes.
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(testdata, "valid/full.json")
	b := readFull(t)
	out, err := Marshal(load(t, b))
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(out, b) {
		t.Fatalf("round trip changed bytes; if full.json was edited by hand, run go test ./engine/manifest -run RoundTrip -update\n%s", firstDiff(b, out))
	}
}

// Shuffled items and reversed key order still marshal to the canonical bytes.
func TestDeterministic(t *testing.T) {
	canonical := readFull(t)
	m := load(t, canonical)

	first, _ := Marshal(m)
	second, _ := Marshal(m)
	if !bytes.Equal(first, second) {
		t.Fatal("two marshals differ")
	}

	r := rand.New(rand.NewSource(1))
	for range 5 {
		shuffled := *m
		shuffled.Items = slices.Clone(m.Items)
		r.Shuffle(len(shuffled.Items), func(i, j int) {
			shuffled.Items[i], shuffled.Items[j] = shuffled.Items[j], shuffled.Items[i]
		})
		got, _ := Marshal(&shuffled)
		if !bytes.Equal(got, canonical) {
			t.Fatal("shuffled items changed output")
		}
	}

	var generic any
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		t.Fatal(err)
	}
	var reversed bytes.Buffer
	writeReversed(&reversed, generic)
	got, err := Marshal(load(t, reversed.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, canonical) {
		t.Fatalf("reversed keys changed output\n%s", firstDiff(canonical, got))
	}
}

func TestMarshalEncoding(t *testing.T) {
	m := load(t, readFull(t))
	m.Items[0].Title = "<RPG> & Quests é 🎧"
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"title": "<RPG> & Quests é 🎧"`)) {
		t.Fatal("HTML or non-ASCII was escaped")
	}
	if !bytes.HasSuffix(out, []byte("}\n")) || bytes.HasSuffix(out, []byte("\n\n")) {
		t.Fatal("want exactly one trailing newline")
	}
	if bytes.Contains(out, []byte("null")) {
		t.Fatal("manifest output contains null")
	}
}

func TestEmptyIsValid(t *testing.T) {
	out, err := Marshal(Empty("Nicole Belovoskey"))
	if err != nil {
		t.Fatal(err)
	}
	_, ps, err := Load(bytes.NewReader(out))
	if err != nil || len(ps) > 0 {
		t.Fatalf("%v %v\n%s", err, ps, out)
	}
}

// Validate on a struct agrees with Load on its bytes.
func TestValidateStruct(t *testing.T) {
	reg := surfaces.MustDefault()
	m := load(t, readFull(t))
	if got := codes(Validate(m, reg)); !slices.Equal(got, []string{CodeLinkNotOwned}) {
		t.Fatalf("got %v", got)
	}
	m.Items[1].Slug = m.Items[0].Slug
	if got := codes(Validate(m, reg)); !slices.Contains(got, CodeDuplicateSlug) {
		t.Fatalf("got %v", got)
	}
	m.Items[1].Slug = "Not A Slug"
	if got := codes(Validate(m, reg)); !slices.Equal(got, []string{CodeSchema}) {
		t.Fatalf("got %v", got)
	}
}

func TestProblemPaths(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join(testdata, "invalid/preview-not-owned.json"))
	_, ps, _ := Load(bytes.NewReader(b))
	for _, p := range ps {
		if p.Code == CodeNotOwnedDomain {
			if p.Path != "/items/0/placements/1/config/preview/urls/0" || p.Severity != Error {
				t.Fatalf("got %+v", p)
			}
			return
		}
	}
	t.Fatal("no not-owned-domain problem")
}

func TestOwnedDomainMatching(t *testing.T) {
	c := &checker{owned: []string{"nicolebelovoskey.com", "*.saucedog.art", "rpg-quests.netlify.app"}}
	for url, want := range map[string]bool{
		"https://nicolebelovoskey.com/x":         true,
		"https://www.nicolebelovoskey.com/":      false, // exact host only
		"https://3d.saucedog.art/":               true,
		"https://a.b.saucedog.art/":              true,
		"https://saucedog.art/":                  false, // wildcard is subdomains only
		"https://evilsaucedog.art/":              false,
		"https://saucedog.art.evil.com/":         false,
		"https://RPG-Quests.netlify.app:443/":    true,
		"https://other.netlify.app/":             false,
		"https://rpg-quests.netlify.app.evil.io": false,
	} {
		if got := c.isOwned(url); got != want {
			t.Errorf("%s: got %v, want %v", url, got, want)
		}
	}
}

func TestParseDate(t *testing.T) {
	for s, ok := range map[string]bool{
		"2025": true, "2025-10": true, "2025-10-03": true, "2024-02-29": true,
		"2023-02-29": false, "2025-02-30": false, "2025-13": false, "25": false, "2025-1": false,
	} {
		if _, err := ParseDate(s); (err == nil) != ok {
			t.Errorf("%s: err=%v", s, err)
		}
	}
}

// writeReversed encodes v as JSON with every object's keys in reverse order.
func writeReversed(w *bytes.Buffer, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
		w.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				w.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			w.Write(kb)
			w.WriteByte(':')
			writeReversed(w, x[k])
		}
		w.WriteByte('}')
	case []any:
		w.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				w.WriteByte(',')
			}
			writeReversed(w, e)
		}
		w.WriteByte(']')
	default:
		b, _ := json.Marshal(x)
		w.Write(b)
	}
}

func firstDiff(a, b []byte) string {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := range min(len(al), len(bl)) {
		if al[i] != bl[i] {
			return "line " + itoa(i+1) + ":\n  want " + al[i] + "\n  got  " + bl[i]
		}
	}
	return "lengths differ"
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }
