package view

import "testing"

func TestNormalise(t *testing.T) {
	for _, r := range []struct{ raw, want string }{
		{"NIP TRF FRM ADA OBI/rent", "Transfer from Ada Obi"},
		{"NIP TRF TO BOLA ADE/rent /RENARRATED", "Transfer to Bola Ade"},
		{"VAT 7.5%", "VAT"},
		{"STAMP DUTY", "Stamp duty"},
		{"TRF TO SAVINGS", "Transfer to savings"},
		{"Reversal: NIP TRF FRM ADA OBI/rent", "Reversal: Transfer from Ada Obi"},
		{"NIP CHARGE 123", "Transfer fee"},
		{"VAT ON NIP CHARGE 123", "VAT on transfer fee"},
		{"SALARY CREDIT ACME LTD", "Salary from Acme Ltd"},
		{"TRF FRM CURRENT ACCT", "Transfer from current account"},
		{"REV/NIP TRF TO BOLA ADE/rent", "Reversal: Transfer to Bola Ade"},
		{"CASH DEPOSIT", "CASH DEPOSIT"},
		{"", ""},
	} {
		if got := Normalise(r.raw); got != r.want {
			t.Errorf("Normalise(%q) = %q, want %q", r.raw, got, r.want)
		}
	}
}
