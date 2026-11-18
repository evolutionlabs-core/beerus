package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

type partnerReply struct {
	code   int
	header http.Header
	body   bytes.Buffer
}

func (r *partnerReply) Header() http.Header {
	return r.header
}

func (r *partnerReply) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *partnerReply) WriteHeader(code int) {
	r.code = code
}

func callPartner(t *testing.T, p *config.Partner, h http.HandlerFunc, method, target, body string, path map[string]string) (int, map[string]any) {
	t.Helper()
	r, err := http.NewRequestWithContext(context.WithValue(t.Context(), ctxPartner, p), method, "https://connect.localhost"+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range path {
		r.SetPathValue(k, v)
	}
	w := &partnerReply{header: http.Header{}}
	h(w, r)
	out := map[string]any{}
	if err := json.Unmarshal(w.body.Bytes(), &out); err != nil {
		t.Fatalf("reply %d is not JSON: %s", w.code, w.body.String())
	}
	return w.code, out
}

func bankReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
