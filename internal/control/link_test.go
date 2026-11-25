package control

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

func TestPeerAnswerFailsClearly(t *testing.T) {
	for _, c := range []struct {
		name, want string
		body       io.Reader
	}{
		{"over the limit", "larger than 16 MiB", bytes.NewReader(bytes.Repeat([]byte(" "), MaxPeerBody+1))},
		{"broken", "connection reset", iotest.ErrReader(errors.New("connection reset"))},
	} {
		l := Link{Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(c.body), Header: http.Header{}}, nil
		})}}
		var out struct{ Data []string }
		if _, err := l.Do(context.Background(), "http://core.invalid", http.MethodGet, "/v1/registry/clients", nil, &out); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
