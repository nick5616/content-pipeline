package canonjson

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMarshal(t *testing.T) {
	type inner struct {
		Z string `json:"z"`
		A string `json:"a"`
	}
	v := map[string]any{
		"b":     []any{inner{"<tag> & é", "🎧"}, 1.5},
		"a":     map[string]any{"y": 1, "x": nil},
		"big":   12345678901234567,
		"empty": []any{},
	}
	want := `{
  "a": {
    "x": null,
    "y": 1
  },
  "b": [
    {
      "a": "🎧",
      "z": "<tag> & é"
    },
    1.5
  ],
  "big": 12345678901234567,
  "empty": []
}
`
	got, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	again, _ := Marshal(v)
	if !bytes.Equal(got, again) {
		t.Fatal("not deterministic")
	}
	if bytes.Contains(got, []byte("\\u003c")) || bytes.Contains(got, []byte("\\u00e9")) {
		t.Fatal("escaped HTML or non-ASCII")
	}
}

func TestMarshalIsIdempotent(t *testing.T) {
	first, _ := Marshal(map[string]any{"k": []any{"v", 2, true}})
	var generic any
	if err := decodeNumber(first, &generic); err != nil {
		t.Fatal(err)
	}
	second, _ := Marshal(generic)
	if !bytes.Equal(first, second) {
		t.Fatalf("%s != %s", first, second)
	}
}

func decodeNumber(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}
