// Package manifest loads, validates and writes the Canon manifest. The JSON
// Schemas in schema/ decide shape; Validate adds the rules a schema can't
// express (uniqueness across items, references, owned domains, cycles).
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/schema"
)

// Severity of a Problem. Errors block plan and apply; warnings are shown in
// the plan.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Problem is one validation finding. Path is a JSON Pointer into the
// manifest; Code is stable and tested (see schema/testdata/expectations.json).
type Problem struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`
	Message  string   `json:"message"`
}

func (p Problem) String() string {
	return fmt.Sprintf("%s %s at %s: %s", p.Severity, p.Code, p.Path, p.Message)
}

// HasErrors reports whether any problem is an error.
func HasErrors(ps []Problem) bool {
	for _, p := range ps {
		if p.Severity == Error {
			return true
		}
	}
	return false
}

var (
	regOnce sync.Once
	reg     surfaces.Registry
)

func defaultRegistry() surfaces.Registry {
	regOnce.Do(func() { reg = surfaces.MustDefault() })
	return reg
}

// Load parses a manifest and validates it against the schema and the default
// surface registry. The error is for unreadable input or input that isn't
// JSON; everything else comes back as problems. When the schema check fails,
// the semantic rules are skipped (they assume a well-formed manifest) and the
// returned manifest may be nil.
func Load(r io.Reader) (*Manifest, []Problem, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, nil, fmt.Errorf("manifest is not JSON: %w", err)
	}

	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	decodeErr := dec.Decode(&m)

	if ps := SchemaProblems(schema.Manifest, doc); len(ps) > 0 {
		if decodeErr != nil {
			return nil, ps, nil
		}
		return &m, ps, nil
	}
	if decodeErr != nil {
		// The schema accepted something the generated types can't hold:
		// a codegen bug, not a user error.
		return nil, nil, fmt.Errorf("decode manifest: %w", decodeErr)
	}
	return &m, semantic(&m, defaultRegistry()), nil
}

// SchemaProblems validates a decoded JSON document (from
// jsonschema.UnmarshalJSON, or plain encoding/json) against an embedded
// schema and returns one problem per failing leaf.
func SchemaProblems(path string, doc any) []Problem {
	s, err := schema.Compile(path)
	if err != nil {
		return []Problem{{Code: "schema", Severity: Error, Path: "", Message: err.Error()}}
	}
	return schemaProblems(s, doc, "")
}

func schemaProblems(s *jsonschema.Schema, doc any, prefix string) []Problem {
	err := s.Validate(doc)
	if err == nil {
		return nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []Problem{{Code: "schema", Severity: Error, Path: prefix, Message: err.Error()}}
	}
	var out []Problem
	seen := map[string]bool{}
	for _, u := range ve.BasicOutput().Errors {
		if u.Error == nil {
			continue
		}
		msg := u.Error.String()
		// Branch failures ("allOf failed", "if-then failed") repeat their
		// causes, which are listed on their own.
		if strings.HasPrefix(msg, "allOf failed") || strings.HasPrefix(msg, "if-then failed") ||
			strings.HasPrefix(msg, "if-else failed") || strings.HasPrefix(msg, "validation failed") {
			continue
		}
		p := Problem{Code: "schema", Severity: Error, Path: prefix + u.InstanceLocation, Message: msg}
		if key := p.Path + "\x00" + p.Message; !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = append(out, Problem{Code: "schema", Severity: Error, Path: prefix, Message: ve.Error()})
	}
	return out
}
