package view

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; form-action 'self' http: https:; frame-ancestors %s; base-uri 'none'"

func TestPageHeaders(t *testing.T) {
	h := http.Header{}
	SetHeaders(h)
	if got, want := h.Get("Content-Security-Policy"), strings.Replace(csp, "%s", "'none'", 1); got != want || h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v, want csp %q", h, want)
	}
	AllowFrame(h, "http://localhost:3000")
	if got, want := h.Get("Content-Security-Policy"), strings.Replace(csp, "%s", "http://localhost:3000", 1); got != want || h.Get("X-Frame-Options") != "" {
		t.Errorf("framed headers %v, want csp %q", h, want)
	}
}

func TestScript(t *testing.T) {
	for _, c := range []struct {
		file   string
		status int
		kind   string
		body   string
	}{
		{"connect.js", http.StatusOK, "text/javascript; charset=utf-8", "window.BeerusConnect"},
		{"nope.js", http.StatusNotFound, "text/plain; charset=utf-8", "script not found"},
	} {
		rec := httptest.NewRecorder()
		Script(c.file).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+c.file, nil))
		if rec.Code != c.status || rec.Header().Get("Content-Type") != c.kind || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: %d %q %q", c.file, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}

func TestInitials(t *testing.T) {
	initials := Funcs()["initials"].(func(string) string)
	for in, want := range map[string]string{"Lapo Microfinance Bank": "LM", "Kuda": "K", "opay digital": "OD", "  Harbour   Bank ": "HB", "Ọ̀yọ́ Bank": "ỌB", "   ": "?"} {
		if got := initials(in); got != want {
			t.Errorf("initials(%q) = %q, want %q", in, got, want)
		}
	}
}
