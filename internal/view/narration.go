package view

import (
	"regexp"
	"strings"
)

var (
	reTradIn    = regexp.MustCompile(`^NIP TRF FRM (.+?)/`)
	reTradOut   = regexp.MustCompile(`^NIP TRF TO (.+?)/`)
	reSalary    = regexp.MustCompile(`^SALARY CREDIT (.+)$`)
	reRenarrate = regexp.MustCompile(` /RENARRATED$`)
)

func titleCase(s string) string {
	parts := strings.Fields(strings.ToLower(s))
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func Normalise(raw string) string {
	s := strings.TrimSpace(reRenarrate.ReplaceAllString(raw, ""))
	switch {
	case strings.HasPrefix(s, "REV/"):
		return "Reversal: " + Normalise(strings.TrimPrefix(s, "REV/"))
	case strings.HasPrefix(s, "Reversal: "):
		return "Reversal: " + Normalise(strings.TrimPrefix(s, "Reversal: "))
	case reTradIn.MatchString(s):
		return "Transfer from " + titleCase(reTradIn.FindStringSubmatch(s)[1])
	case reTradOut.MatchString(s):
		return "Transfer to " + titleCase(reTradOut.FindStringSubmatch(s)[1])
	case strings.HasPrefix(s, "NIP CHARGE"), s == "Transfer fee":
		return "Transfer fee"
	case strings.HasPrefix(s, "VAT ON NIP CHARGE"):
		return "VAT on transfer fee"
	case strings.HasPrefix(s, "VAT"):
		return "VAT"
	case strings.HasPrefix(s, "STAMP DUTY"), s == "Stamp duty":
		return "Stamp duty"
	case reSalary.MatchString(s):
		return "Salary from " + titleCase(reSalary.FindStringSubmatch(s)[1])
	case s == "TRF TO SAVINGS":
		return "Transfer to savings"
	case s == "TRF FRM CURRENT ACCT":
		return "Transfer from current account"
	}
	return s
}
