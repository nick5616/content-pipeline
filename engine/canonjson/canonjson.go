// Package canonjson writes JSON in Canon's one canonical form, so the same
// value always produces the same bytes: object keys sorted, 2-space indent,
// a trailing newline, no HTML escaping, and raw UTF-8.
package canonjson

import (
	"bytes"
	"encoding/json"
)

// Marshal encodes v canonically. Numbers keep their exact decimal form.
func Marshal(v any) ([]byte, error) {
	raw, err := encode(v, "")
	if err != nil {
		return nil, err
	}
	// Round-trip through a generic value: encoding/json sorts map keys, which
	// also puts struct fields in key order.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return encode(generic, "  ")
}

func encode(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // Encode ends with exactly one '\n'
}
