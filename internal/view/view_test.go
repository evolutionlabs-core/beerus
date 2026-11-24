package view

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageHeaders(t *testing.T) {
	h := http.Header{}
	SetHeaders(h)
	want := map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Cache-Control":           "no-store",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; form-action 'self' http: https:; frame-ancestors 'none'; base-uri 'none'",
		"Referrer-Policy":         "same-origin",
	}
	if len(h) != len(want) {
		t.Errorf("set %d headers, want %d: %v", len(h), len(want), h)
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s is %q, want %q", k, got, v)
		}
	}
}

func TestAllowFrame(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{"local app", "http://localhost:3000", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; form-action 'self' http: https:; frame-ancestors http://localhost:3000; base-uri 'none'"},
		{"https app", "https://app.example.com", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; form-action 'self' http: https:; frame-ancestors https://app.example.com; base-uri 'none'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			SetHeaders(h)
			AllowFrame(h, tc.origin)
			if got := h.Get("Content-Security-Policy"); got != tc.want {
				t.Errorf("Content-Security-Policy is %q, want %q", got, tc.want)
			}
			if got := h.Get("X-Frame-Options"); got != "" {
				t.Errorf("X-Frame-Options is %q, want none", got)
			}
		})
	}
}

func TestScript(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		status int
		kind   string
		body   string
	}{
		{"connect script", "connect.js", http.StatusOK, "text/javascript; charset=utf-8", "window.BeerusConnect"},
		{"missing script", "nope.js", http.StatusNotFound, "text/plain; charset=utf-8", "script not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Script(tc.file).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+tc.file, nil))
			if rec.Code != tc.status {
				t.Fatalf("status is %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.kind {
				t.Errorf("Content-Type is %q, want %q", got, tc.kind)
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Errorf("body %q does not contain %q", rec.Body.String(), tc.body)
			}
		})
	}
}
