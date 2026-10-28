// Package schema embeds Canon's JSON Schemas (2020-12) and compiles them.
// These files are the contract: the Go validator here decides what is valid,
// and the generated Go and Zod types only exist for convenience.
package schema

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed *.schema.json facts/*.schema.json surfaces/*/*.schema.json
var FS embed.FS

// base is where the embedded files are mounted for $ref resolution. It is
// never fetched.
const base = "https://canon.invalid/schema/"

// Paths of the top-level schemas.
const (
	Manifest       = "manifest.schema.json"
	PublicManifest = "public-manifest.schema.json"
)

// PlacementPath and ViewPath locate a surface's schemas.
func PlacementPath(surface string) string { return "surfaces/" + surface + "/placement.schema.json" }
func ViewPath(surface string) string      { return "surfaces/" + surface + "/view.schema.json" }

var (
	once     sync.Once
	compiler *jsonschema.Compiler
	initErr  error
	mu       sync.Mutex
	cache    = map[string]*jsonschema.Schema{}
)

func setup() {
	compiler = jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(noLoader{})
	initErr = fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		b, err := FS.ReadFile(path)
		if err != nil {
			return err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return compiler.AddResource(base+path, doc)
	})
}

// Compile returns the compiled schema at path (relative to this directory),
// with every $ref resolved against the embedded files only.
func Compile(path string) (*jsonschema.Schema, error) {
	once.Do(setup)
	if initErr != nil {
		return nil, initErr
	}
	mu.Lock()
	defer mu.Unlock()
	if s, ok := cache[path]; ok {
		return s, nil
	}
	s, err := compiler.Compile(base + path)
	if err != nil {
		return nil, err
	}
	cache[path] = s
	return s, nil
}

// Paths lists every embedded schema file.
func Paths() []string {
	var out []string
	_ = fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".schema.json") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// noLoader refuses to fetch anything: a $ref outside the embedded files is a
// bug, not a network request.
type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema %s is not embedded", url)
}
