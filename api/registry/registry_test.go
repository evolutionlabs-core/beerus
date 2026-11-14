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

func exampleBody(t *testing.T, name string, contentType string, mt *v3.MediaType) []byte {
	t.Helper()
	if mt == nil || mt.Example == nil {
		t.Fatalf("%s: no example for %s", name, contentType)
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
		body = bytes.NewReader(exampleBody(t, o.op.OperationId, contentType, pair.Value()))
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

func buildResponse(t *testing.T, o operation, status string) *http.Response {
	t.Helper()
	r, ok := o.op.Responses.Codes.Get(status)
	if !ok {
		t.Fatalf("%s: no %s response", o.op.OperationId, status)
	}
	code, err := strconv.Atoi(status)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: code, Header: http.Header{}, Body: http.NoBody}
	for h := r.Headers.First(); h != nil; h = h.Next() {
		var value string
		if h.Value().Example == nil {
			t.Fatalf("%s %s: header %s has no example", o.op.OperationId, status, h.Key())
		}
		if err := h.Value().Example.Decode(&value); err != nil {
			t.Fatal(err)
		}
		resp.Header.Set(h.Key(), value)
	}
	if r.Content != nil && r.Content.Len() > 0 {
		pair := r.Content.First()
		resp.Header.Set("Content-Type", pair.Key())
		resp.Body = io.NopCloser(bytes.NewReader(exampleBody(t, o.op.OperationId+" "+status, pair.Key(), pair.Value())))
	}
	return resp
}

func TestOperationExamples(t *testing.T) {
	v, ops := load(t)
	const trust = "https://cbn.localhost"
	const registry = "https://nibss.localhost"
	operator := map[string]string{"Authorization": "Bearer op_2f9d41c7a0e85b36"}
	rows := []struct {
		id      string
		method  string
		target  string
		headers map[string]string
	}{
		{"enrolParticipant", http.MethodPost, trust + "/v1/trust/enrolments", nil},
		{"renewCertificate", http.MethodPost, trust + "/v1/trust/renewals", nil},
		{"revokeCertificate", http.MethodPost, trust + "/v1/trust/revocations", nil},
		{"revokeCertificateAsOperator", http.MethodPost, trust + "/v1/trust/operator/revocations", operator},
		{"listRevocations", http.MethodGet, trust + "/v1/trust/revocations?limit=100&cursor=cur_eyJzIjoxfQ", nil},
		{"createEnrolmentToken", http.MethodPost, trust + "/v1/trust/enrolment_tokens", operator},
		{"getTrustBundle", http.MethodGet, trust + "/v1/trust/bundle", nil},
		{"listParticipants", http.MethodGet, registry + "/v1/registry/participants?limit=100&role=bank&status=active", nil},
		{"createParticipant", http.MethodPost, registry + "/v1/registry/participants", operator},
		{"getParticipant", http.MethodGet, registry + "/v1/registry/participants/bank-a", nil},
		{"publishSigningKey", http.MethodPost, registry + "/v1/registry/participants/bank-a/signing_keys", nil},
		{"listSponsorships", http.MethodGet, registry + "/v1/registry/sponsorships?limit=100&sponsor_id=connect", nil},
		{"createSponsorship", http.MethodPost, registry + "/v1/registry/sponsorships", nil},
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
			req := buildRequest(t, o, row.target, row.headers)
			if ok, errs := v.ValidateHttpRequest(req); !ok {
				t.Fatalf("request: %v", errs[0])
			}
			for r := o.op.Responses.Codes.First(); r != nil; r = r.Next() {
				resp := buildResponse(t, o, r.Key())
				if ok, errs := v.ValidateHttpResponse(req, resp); !ok {
					t.Errorf("response %s: %v", r.Key(), errs[0])
				}
			}
		})
	}
	for id := range ops {
		if !slices.Contains(covered, id) {
			t.Errorf("operation %s has no row", id)
		}
	}
}
