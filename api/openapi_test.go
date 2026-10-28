package api

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	verrors "github.com/pb33f/libopenapi-validator/errors"
	"github.com/pb33f/libopenapi/datamodel"
)

// loadSpec parses openapi.yaml with its $refs into ../schema resolved.
func loadSpec(t *testing.T) libopenapi.Document {
	t.Helper()
	b, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocumentWithConfiguration(b, &datamodel.DocumentConfiguration{
		BasePath:            ".",
		SpecFilePath:        "openapi.yaml",
		AllowFileReferences: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSpecIsValid31(t *testing.T) {
	doc := loadSpec(t)
	if v := doc.GetVersion(); v != "3.1.0" {
		t.Fatalf("version %s", v)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("build model: %v", err)
	}
	if n := model.Model.Paths.PathItems.Len(); n < 15 {
		t.Fatalf("only %d paths", n)
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		t.Fatalf("validator: %v", errs)
	}
	if ok, verrs := v.ValidateDocument(); !ok {
		for _, e := range verrs {
			t.Errorf("%s: %s", e.Message, e.Reason)
			for _, se := range e.SchemaValidationErrors {
				t.Errorf("  %s at %s (line %d)", se.Reason, se.FieldPath, se.Line)
			}
		}
	}
}

// Every request and response the handler tests exercise matches the spec.
func TestContract(t *testing.T) {
	v, errs := validator.NewValidator(loadSpec(t))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	h := newServer(builtUI)
	cases := []struct {
		name string
		hdr  map[string]string
		code int
	}{
		{"health ok", bearer, 200},
		{"health without token", nil, 401},
		{"health wrong token", map[string]string{"Authorization": "Bearer nope"}, 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://127.0.0.1:4747/api/health", nil)
			req.Host = "127.0.0.1:4747"
			for k, val := range c.hdr {
				req.Header.Set(k, val)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			resp := w.Result()
			if resp.StatusCode != c.code {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if c.code == 200 {
				if ok, verrs := v.ValidateHttpRequestResponse(req, resp); !ok {
					t.Fatal(describe(verrs))
				}
			} else if ok, verrs := v.ValidateHttpResponse(req, resp); !ok {
				t.Fatal(describe(verrs))
			}
		})
	}
}

func describe(errs []*verrors.ValidationError) string {
	var b strings.Builder
	for _, e := range errs {
		fmt.Fprintf(&b, "%s: %s\n", e.Message, e.Reason)
		for _, se := range e.SchemaValidationErrors {
			fmt.Fprintf(&b, "  %s at %s\n", se.Reason, se.FieldPath)
		}
	}
	return b.String()
}
