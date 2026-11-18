package core

import (
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func acctKey(bank, nuban string) string {
	return bank + ":" + nuban
}

func splitKey(key string) (string, string) {
	bank, nuban, _ := strings.Cut(key, ":")
	return bank, nuban
}

func ts(t time.Time) string {
	return t.In(clock.Lagos).Format(time.RFC3339)
}
