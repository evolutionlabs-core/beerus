package view

import "testing"

func TestNormalise(t *testing.T) {
	rows := []struct{ raw, want string }{
		{"NIP TRF FRM ADA OBI/rent", "Transfer from Ada Obi"},
		{"NIP TRF TO BOLA ADE/rent", "Transfer to Bola Ade"},
		{"NIP CHARGE 123", "Transfer fee"},
		{"Transfer fee", "Transfer fee"},
		{"VAT ON NIP CHARGE 123", "VAT on transfer fee"},
		{"VAT 7.5%", "VAT"},
		{"STAMP DUTY", "Stamp duty"},
		{"SALARY CREDIT ACME LTD", "Salary from Acme Ltd"},
		{"TRF TO SAVINGS", "Transfer to savings"},
		{"TRF FRM CURRENT ACCT", "Transfer from current account"},
		{"REV/NIP TRF TO BOLA ADE/rent", "Reversal: Transfer to Bola Ade"},
		{"Reversal: NIP TRF FRM ADA OBI/rent", "Reversal: Transfer from Ada Obi"},
		{"NIP TRF TO BOLA ADE/rent /RENARRATED", "Transfer to Bola Ade"},
		{"CASH DEPOSIT", "CASH DEPOSIT"},
		{"", ""},
	}
	for _, r := range rows {
		if got := Normalise(r.raw); got != r.want {
			t.Errorf("Normalise(%q) = %q, want %q", r.raw, got, r.want)
		}
	}
}
