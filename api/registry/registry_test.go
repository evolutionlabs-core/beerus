package registry_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

const specFile = "openapi.yaml"

type operation struct {
	path   string
	method string
	op     *v3.Operation
}

func load(t *testing.T) (validator.Validator, map[string]operation) {
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
	ops := map[string]operation{}
	for p := model.Model.Paths.PathItems.First(); p != nil; p = p.Next() {
		for o := p.Value().GetOperations().First(); o != nil; o = o.Next() {
			ops[o.Value().OperationId] = operation{path: p.Key(), method: strings.ToUpper(o.Key()), op: o.Value()}
		}
	}
	return v, ops
}

func exampleBody(t *testing.T, contentType string, mt *v3.MediaType) []byte {
	t.Helper()
	if mt == nil || mt.Example == nil {
		t.Fatalf("no example for %s", contentType)
	}
	var value any
	if err := mt.Example.Decode(&value); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func buildRequest(t *testing.T, o operation, target string, headers map[string]string) *http.Request {
	t.Helper()
	var body io.Reader
	contentType := ""
	if o.op.RequestBody != nil {
		pair := o.op.RequestBody.Content.First()
		contentType = pair.Key()
		body = bytes.NewReader(exampleBody(t, contentType, pair.Value()))
	}
	req, err := http.NewRequest(o.method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func buildResponse(t *testing.T, o operation, status int) *http.Response {
	t.Helper()
	r, ok := o.op.Responses.Codes.Get(strconv.Itoa(status))
	if !ok {
		t.Fatalf("no %d response", status)
	}
	resp := &http.Response{StatusCode: status, Header: http.Header{}, Body: http.NoBody}
	for h := r.Headers.First(); h != nil; h = h.Next() {
		var value string
		if err := h.Value().Example.Decode(&value); err != nil {
			t.Fatal(err)
		}
		resp.Header.Set(h.Key(), value)
	}
	if r.Content != nil && r.Content.Len() > 0 {
		pair := r.Content.First()
		resp.Header.Set("Content-Type", pair.Key())
		resp.Body = io.NopCloser(bytes.NewReader(exampleBody(t, pair.Key(), pair.Value())))
	}
	return resp
}

func TestOperationExamples(t *testing.T) {
	v, ops := load(t)
	const trust = "https://cbn.localhost"
	const registry = "https://nibss.localhost"
	rows := []struct {
		id     string
		method string
		target string
		status int
	}{
		{"enrolParticipant", http.MethodPost, trust + "/v1/trust/enrolments", 201},
		{"renewCertificate", http.MethodPost, trust + "/v1/trust/renewals", 201},
		{"revokeCertificate", http.MethodPost, trust + "/v1/trust/revocations", 200},
		{"listRevocations", http.MethodGet, trust + "/v1/trust/revocations?limit=100&cursor=cur_eyJzIjoxfQ", 200},
		{"getTrustBundle", http.MethodGet, trust + "/v1/trust/bundle", 200},
		{"listParticipants", http.MethodGet, registry + "/v1/registry/participants?limit=100&role=bank&status=active", 200},
		{"getParticipant", http.MethodGet, registry + "/v1/registry/participants/bank-a", 200},
		{"listSponsorships", http.MethodGet, registry + "/v1/registry/sponsorships?limit=100&sponsor_id=connect", 200},
	}
	covered := []string{}
	for _, row := range rows {
		covered = append(covered, row.id)
		t.Run(row.id, func(t *testing.T) {
			o, ok := ops[row.id]
			if !ok {
				t.Fatalf("operation %s is not in the spec", row.id)
			}
			if o.method != row.method {
				t.Fatalf("method = %s, want %s", o.method, row.method)
			}
			req := buildRequest(t, o, row.target, nil)
			if ok, errs := v.ValidateHttpRequest(req); !ok {
				t.Fatalf("request: %v", errs[0])
			}
			resp := buildResponse(t, o, row.status)
			if ok, errs := v.ValidateHttpResponse(req, resp); !ok {
				t.Fatalf("response: %v", errs[0])
			}
		})
	}
	for id := range ops {
		if !slices.Contains(covered, id) {
			t.Errorf("operation %s has no row", id)
		}
	}
}
