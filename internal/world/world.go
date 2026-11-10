package world

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
)

func ID(prefix string, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:])
	return prefix + "_" + strings.ToLower(enc[:20])
}

func NUBAN(bankCode string, serial uint64) string {
	return nubanCheck(bankCode, fmt.Sprintf("%09d", serial%1000000000))
}

func NormaliseBankCode(code string) (string, error) {
	if strings.Trim(code, "0123456789") != "" {
		return "", fmt.Errorf("bank code %q must be digits", code)
	}
	switch len(code) {
	case 3:
		return "000" + code, nil
	case 5:
		return "9" + code, nil
	case 6:
		return code, nil
	}
	return "", fmt.Errorf("bank code %q must have 3, 5 or 6 digits", code)
}

func ValidNUBAN(bankCode, nuban string) bool {
	if len(nuban) != 10 || strings.Trim(nuban, "0123456789") != "" {
		return false
	}
	return nubanCheck(bankCode, nuban[:9]) == nuban
}

func nubanCheck(bankCode, serial string) string {
	code, err := NormaliseBankCode(bankCode)
	if err != nil {
		return ""
	}
	digits := code + serial
	w := []int{3, 7, 3, 3, 7, 3, 3, 7, 3, 3, 7, 3, 3, 7, 3}
	sum := 0
	for i, c := range digits {
		sum += int(c-'0') * w[i]
	}
	return serial + fmt.Sprint((10-sum%10)%10)
}
