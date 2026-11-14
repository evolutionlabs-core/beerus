package site

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/view"
)

type capture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *capture) Header() http.Header         { return c.header }
func (c *capture) Write(p []byte) (int, error) { return c.body.Write(p) }
func (c *capture) WriteHeader(status int)      { c.status = status }

func TestAccessHistoryPagesAreBounded(t *testing.T) {
	rows := []struct {
		page   string
		want   int
		ok     bool
		status int
		body   string
	}{
		{"", 1, true, 0, ""},
		{"1000", 1000, true, 0, ""},
		{"1001", 0, false, http.StatusUnprocessableEntity, "The page 1001 is too high. The access history allows pages 1 to 1000.\n"},
		{"300000000000000000", 0, false, http.StatusUnprocessableEntity, "The page 300000000000000000 is too high. The access history allows pages 1 to 1000.\n"},
		{"abc", 0, false, http.StatusUnprocessableEntity, "The page \"abc\" must be a whole number.\n"},
	}
	for _, r := range rows {
		w := &capture{header: http.Header{}}
		req := &http.Request{URL: &url.URL{Path: "/apps", RawQuery: "page=" + r.page}}
		page, ok := view.PageParam(w, req, "access history", maxHistoryPages)
		if ok != r.ok || w.status != r.status || w.body.String() != r.body || (ok && page != r.want) {
			t.Errorf("page %q gave %d, %v, status %d, body %q", r.page, page, ok, w.status, w.body.String())
		}
	}
}
