package bank

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func ParseNaira(s string) (int64, error) {
	whole, frac, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), ".")
	if whole == "" || len(frac) > 2 || strings.Trim(whole, "0123456789") != "" || strings.Trim(frac, "0123456789") != "" {
		return 0, fmt.Errorf("amount %q must look like 25000 or 25000.50 (naira)", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("amount %q must be a positive naira amount", s)
	}
	return n, nil
}

func FormatNaira(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "−", -minor
	}
	n := strconv.FormatInt(minor/100, 10)
	out := make([]byte, 0, len(n)+len(n)/3)
	for i := range n {
		if i > 0 && (len(n)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, n[i])
	}
	return fmt.Sprintf("%s₦%s.%02d", sign, out, minor%100)
}

func Naira(minor int64) string {
	return strings.Replace(FormatNaira(minor), "−", "-", 1)
}

func AddMinor(a, b int64) (int64, bool) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, false
	}
	return a + b, true
}
