package web

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"strings"
)

type ctxKey int

const ctxRequestID ctxKey = iota

const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

func Encode(b []byte) string {
	return strings.ToLower(base32.NewEncoding(idAlphabet).WithPadding(base32.NoPadding).EncodeToString(b))
}

func NewID(prefix string) string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + Encode(b)
}

func RequestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if v, ok := r.Context().Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := NewID("req")
		w.Header().Set("Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}
