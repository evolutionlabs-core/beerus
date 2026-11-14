package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const BodyLimit = 1 << 20

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func WriteError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	WriteJSON(w, status, map[string]APIError{"error": {Code: code, Message: msg, RequestID: RequestID(r)}})
}

var ErrBodyTooLarge = errors.New("the request body is larger than 1 MiB")

func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, BodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > BodyLimit {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}

func DecodeBody(r *http.Request, v any) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

const SomethingWentWrong = "Something went wrong. Try again."

func Sentence(msg string) string {
	if msg == "" {
		return msg
	}
	return strings.ToUpper(msg[:1]) + strings.TrimSuffix(msg[1:], ".") + "."
}

func ListPage(data any, next string) map[string]any {
	out := map[string]any{"data": data}
	if next != "" {
		out["next_cursor"] = next
	}
	return out
}

func IdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > idempotencyKeyMax {
		WriteError(w, r, http.StatusBadRequest, "invalid_request", "The Idempotency-Key header must be 1 to 255 characters.")
		return "", false
	}
	return key, true
}
