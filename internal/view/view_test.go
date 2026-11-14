package view

import (
	"net/http"
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
