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

func exampleBody(t *testing.T, contentType string, mt *v3.MediaType) []byte {
	t.Helper()
	if mt == nil || mt.Example == nil {
		t.Fatalf("no example for %s", contentType)
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
	for k, v := range value.(map[string]any) {
		form.Set(k, v.(string))
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
		body = bytes.NewReader(exampleBody(t, contentType, pair.Value()))
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
	bearer := map[string]string{"Authorization": "Bearer at_4be1f6a07c92d358"}
	rows := []struct {
		id      string
		method  string
		target  string
		headers map[string]string
		status  int
	}{
		{"createPushedAuthorisationRequest", http.MethodPost, "/par", nil, 201},
		{"authorise", http.MethodGet, "/authorize?client_id=connect&request_uri=urn%3Abeerus%3Apar%3A5d2f8c1e9b7a4036", nil, 302},
		{"issueToken", http.MethodPost, "/token", nil, 200},
		{"revokeToken", http.MethodPost, "/revoke", nil, 200},
		{"listAccounts", http.MethodGet, "/accounts?limit=100", bearer, 200},
		{"getAccountBalances", http.MethodGet, "/accounts/ba_01JBD2P7K3WE/balances", bearer, 200},
		{"listAccountTransactions", http.MethodGet, "/accounts/ba_01JBD2P7K3WE/transactions?limit=100&cursor=cur_eyJzIjoxMDQyfQ", bearer, 200},
		{"listConsentEvents", http.MethodGet, "/consent_events?limit=100", nil, 200},
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
