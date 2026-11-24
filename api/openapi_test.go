package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

var specs = []string{"nip", "participant", "settlement"}

type found struct {
	method, path string
	op           *v3.Operation
	item         *v3.PathItem
	doc          *v3.Document
}

func load(t *testing.T, specFile string) (*v3.Document, validator.Validator) {
	t.Helper()
	spec, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		t.Fatalf("validator: %v", errs)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatal(err)
	}
	return &model.Model, v
}

func index(t *testing.T, doc *v3.Document) map[string]found {
	t.Helper()
	out := map[string]found{}
	for path, item := range doc.Paths.PathItems.FromOldest() {
		for method, op := range item.GetOperations().FromOldest() {
			if _, dup := out[op.OperationId]; dup {
				t.Fatalf("operation ID %s is used twice", op.OperationId)
			}
			out[op.OperationId] = found{strings.ToUpper(method), path, op, item, doc}
		}
	}
	return out
}

func exampleJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decode(t *testing.T, n interface{ Decode(any) error }) any {
	t.Helper()
	var v any
	must(t, n.Decode(&v))
	return v
}

func buildRequest(t *testing.T, f found) *http.Request {
	t.Helper()
	path := f.path
	query := url.Values{}
	header := http.Header{}
	params := append([]*v3.Parameter{}, f.item.Parameters...)
	params = append(params, f.op.Parameters...)
	for _, p := range params {
		if p.Example == nil {
			t.Fatalf("%s: parameter %s has no example", f.op.OperationId, p.Name)
		}
		val, ok := decode(t, p.Example).(string)
		if !ok {
			val = string(exampleJSON(t, decode(t, p.Example)))
		}
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", val)
		case "query":
			query.Set(p.Name, val)
		case "header":
			header.Set(p.Name, val)
		}
	}
	var body io.Reader
	if f.op.RequestBody != nil {
		mt := f.op.RequestBody.Content.GetOrZero("application/json")
		if mt == nil {
			t.Fatalf("%s: request body has no JSON content", f.op.OperationId)
		}
		body = bytes.NewReader(exampleJSON(t, mediaExamples(t, f.op.OperationId, mt)[0]))
		header.Set("Content-Type", "application/json")
	}
	target := "http://example.localhost" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for _, sec := range f.op.Security {
		if _, ok := sec.Requirements.Get("bearerAuth"); ok {
			header.Set("Authorization", "Bearer control-token")
		}
	}
	if len(f.op.Security) == 0 {
		for _, sec := range f.doc.Security {
			if _, ok := sec.Requirements.Get("bearerAuth"); ok {
				header.Set("Authorization", "Bearer control-token")
			}
			if _, ok := sec.Requirements.Get("callerHeader"); ok {
				header.Set("Beerus-Caller", "bank-a")
			}
		}
	}
	req, err := http.NewRequest(f.method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header
	return req
}

func mediaExamples(t *testing.T, id string, mt *v3.MediaType) []any {
	t.Helper()
	var out []any
	if mt.Example != nil {
		out = append(out, decode(t, mt.Example))
	}
	for _, ex := range mt.Examples.FromOldest() {
		out = append(out, decode(t, ex.Value))
	}
	if len(out) == 0 {
		t.Fatalf("%s: media type has no example", id)
	}
	return out
}

func buildResponse(t *testing.T, f found, status string, r *v3.Response, example any) *http.Response {
	t.Helper()
	code := 0
	for _, c := range status {
		code = code*10 + int(c-'0')
	}
	resp := &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(exampleJSON(t, example))),
	}
	for name, h := range r.Headers.FromOldest() {
		if h.Schema != nil {
			resp.Header.Set(name, "true")
		}
	}
	return resp
}

func TestOpenAPI(t *testing.T) {
	for _, name := range specs {
		t.Run(name, func(t *testing.T) {
			doc, v := load(t, name+"/openapi.yaml")
			for id, f := range index(t, doc) {
				req := buildRequest(t, f)
				if ok, errs := v.ValidateHttpRequest(req); !ok {
					t.Errorf("%s request: %v", id, errs)
				}
				for status, r := range f.op.Responses.Codes.FromOldest() {
					mt := r.Content.GetOrZero("application/json")
					if mt == nil {
						t.Fatalf("%s %s: response has no JSON content", id, status)
					}
					for _, example := range mediaExamples(t, id, mt) {
						if ok, errs := v.ValidateHttpResponse(buildRequest(t, f), buildResponse(t, f, status, r, example)); !ok {
							t.Errorf("%s response %s: %v", id, status, errs)
						}
					}
				}
			}
		})
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
