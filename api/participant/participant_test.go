package participant_test

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

const specFile = "openapi.yaml"

var operations = []struct{ id, method, path string }{
	{"participantHealth", http.MethodGet, "/v1/health"},
	{"participantStatus", http.MethodGet, "/v1/status"},
	{"ensureParticipantStep", http.MethodPost, "/v1/steps/{step_id}/ensure"},
	{"closeParticipant", http.MethodPost, "/v1/close"},
	{"stopParticipant", http.MethodPost, "/v1/stop"},
	{"listParticipantFaults", http.MethodGet, "/v1/faults"},
	{"setParticipantFault", http.MethodPost, "/v1/faults"},
	{"clearParticipantFaults", http.MethodDelete, "/v1/faults"},
}

type found struct {
	method, path string
	op           *v3.Operation
	item         *v3.PathItem
	doc          *v3.Document
}

func load(t *testing.T) (*v3.Document, validator.Validator) {
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
	if err := n.Decode(&v); err != nil {
		t.Fatal(err)
	}
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
	target := "https://example.localhost" + path
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

func TestOperations(t *testing.T) {
	doc, v := load(t)
	ops := index(t, doc)
	listed := map[string]bool{}
	for _, row := range operations {
		listed[row.id] = true
		t.Run(row.id, func(t *testing.T) {
			f, ok := ops[row.id]
			if !ok {
				t.Fatalf("operation %s is not in the document", row.id)
			}
			if f.method != row.method || f.path != row.path {
				t.Fatalf("operation %s is %s %s, want %s %s", row.id, f.method, f.path, row.method, row.path)
			}
			req := buildRequest(t, f)
			if ok, errs := v.ValidateHttpRequest(req); !ok {
				t.Fatalf("request: %v", errs)
			}
			for status, r := range f.op.Responses.Codes.FromOldest() {
				mt := r.Content.GetOrZero("application/json")
				if mt == nil {
					t.Fatalf("%s %s: response has no JSON content", row.id, status)
				}
				for _, example := range mediaExamples(t, row.id, mt) {
					req := buildRequest(t, f)
					if ok, errs := v.ValidateHttpResponse(req, buildResponse(t, f, status, r, example)); !ok {
						t.Errorf("response %s: %v", status, errs)
					}
				}
			}
		})
	}
	for id := range ops {
		if !listed[id] {
			t.Errorf("operation %s has no test row", id)
		}
	}
}
