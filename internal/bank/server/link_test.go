package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestPeerAnswerOverTheLimitFailsClearly(t *testing.T) {
	big := bytes.Repeat([]byte(" "), MaxPeerBody+1)
	l := Link{Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(big)), Header: http.Header{}}, nil
	})}}
	var out struct{ Data []string }
	if _, err := l.Do(context.Background(), "http://core.invalid", http.MethodGet, "/v1/registry/clients", nil, &out); err == nil || !strings.Contains(err.Error(), "larger than 16 MiB") {
		t.Fatalf("an oversized answer: %v", err)
	}
	l.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(errors.New("connection reset"))), Header: http.Header{}}, nil
	})}
	if _, err := l.Do(context.Background(), "http://core.invalid", http.MethodGet, "/v1/registry/clients", nil, &out); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("a broken answer: %v", err)
	}
}

func stubReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}
}
