package bank_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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
	if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	form := url.Values{}
	fields, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s: form example is not an object", name)
	}
	for k, v := range fields {
		text, ok := v.(string)
		if !ok {
			t.Fatalf("%s: form field %s is not a string", name, k)
		}
		form.Set(k, text)
	}
	return []byte(form.Encode())
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
	req, err := http.NewRequest(o.method, "https://bank-a.localhost"+target, body)
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
	bearer := map[string]string{"Authorization": "Bearer at_4be1f6a07c92d358"}
	rows := []struct {
		id      string
		method  string
		target  string
		headers map[string]string
	}{
		{"createPushedAuthorisationRequest", http.MethodPost, "/par", nil},
		{"authorise", http.MethodGet, "/authorize?client_id=connect&request_uri=urn%3Abeerus%3Apar%3A5d2f8c1e9b7a4036", nil},
		{"issueToken", http.MethodPost, "/token", nil},
		{"revokeToken", http.MethodPost, "/revoke", nil},
		{"listAccounts", http.MethodGet, "/accounts?limit=100", bearer},
		{"getAccountBalances", http.MethodGet, "/accounts/ba_01JBD2P7K3WE/balances", bearer},
		{"listAccountTransactions", http.MethodGet, "/accounts/ba_01JBD2P7K3WE/transactions?limit=100&cursor=cur_eyJzIjoxMDQyfQ", bearer},
		{"listConsentEvents", http.MethodGet, "/consent_events?limit=100", nil},
		{"getAuthorisationServerMetadata", http.MethodGet, "/.well-known/oauth-authorization-server", nil},
		{"getJWKS", http.MethodGet, "/jwks.json", nil},
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

func TestWebhookExamples(t *testing.T) {
	spec, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for w := model.Model.Webhooks.First(); w != nil; w = w.Next() {
		for o := w.Value().GetOperations().First(); o != nil; o = o.Next() {
			seen++
			for _, p := range o.Value().Parameters {
				if p.Example == nil {
					t.Errorf("%s: parameter %s has no example", o.Value().OperationId, p.Name)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("the spec has no webhooks")
	}
}
