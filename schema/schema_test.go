package schema

import (
	"testing"
)

// Every schema compiles under the 2020-12 metaschema and every $ref resolves
// within the embedded files.
func TestAllSchemasCompile(t *testing.T) {
	paths := Paths()
	if len(paths) != 4+8+14 {
		t.Errorf("expected 26 schema files, found %d: %v", len(paths), paths)
	}
	for _, p := range paths {
		if _, err := Compile(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}
