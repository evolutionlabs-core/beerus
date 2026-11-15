package web

import (
	"fmt"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

func NewSecret(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return prefix + Encode(b), nil
}

func HashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
