package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

func NewSecret(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + Encode(b)
}

func HashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
