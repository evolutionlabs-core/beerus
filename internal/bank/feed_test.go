package bank_test

import (
	"context"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/charges"
)

func TestCustomerNarrationDoesNotChangeSettlement(t *testing.T) {
	for _, narration := range []string{"PRIVATE LESSONS", "SMS ALERT", "MAINTENANCE TO PLUMBER", "SAVINGS CLUB", "CURRENT ACCOUNT TOP UP", "TRF TO MUM", "TRF FRM DAD", "NIP CHARGE REFUND", "STAMP DUTY GIFT"} {
		out, in, err := bank.NIPAmounts(bank.FeedLine{Key: "a", Amount: -5000, Channel: "nip", Narration: narration})
		if err != nil || out != 5000 || in != 0 {
			t.Errorf("%q out: %d %d %v", narration, out, in, err)
		}
		out, in, err = bank.NIPAmounts(bank.FeedLine{Key: "b", Amount: 7000, Channel: "nip", Narration: narration})
		if err != nil || out != 0 || in != 7000 {
			t.Errorf("%q in: %d %d %v", narration, out, in, err)
		}
	}
}

func TestChargesAreClassifiedByCategory(t *testing.T) {
	for _, c := range []struct {
		line    bank.FeedLine
		out, in int64
		fails   bool
	}{
		{line: bank.FeedLine{Amount: -5000, Channel: "charge", Category: charges.CategoryVAT, Narration: "NIP TRANSFER"}},
		{line: bank.FeedLine{Amount: -5000, Channel: "charge", Category: charges.CategoryFee, Narration: "NIP TRANSFER"}},
		{line: bank.FeedLine{Amount: -5000, Channel: "charge", Narration: "VAT"}, fails: true},
		{line: bank.FeedLine{Amount: 5000, Channel: "reversal", Category: charges.CategoryVAT, Narration: "REV/NIP TRANSFER"}},
		{line: bank.FeedLine{Amount: 5000, Channel: "reversal", Narration: "REV/VAT"}, in: 0, out: -5000},
		{line: bank.FeedLine{Amount: -5000, Channel: "internal", Narration: "NIP TRANSFER"}},
	} {
		out, in, err := bank.NIPAmounts(c.line)
		if (err != nil) != c.fails || out != c.out || in != c.in {
			t.Errorf("%+v: %d %d %v", c.line, out, in, err)
		}
	}
}

func TestChargeWithoutCategoryIsRefused(t *testing.T) {
	b := newBank(t, "bank-x", "999011", "011", 1)
	err := b.RecordLine(context.Background(), bank.FeedLine{Key: "k", NUBAN: b.custs[0], Amount: -5000, BookedAt: t0, Channel: "charge", Narration: "VAT"})
	if err == nil {
		t.Fatal("a charge with no category was posted")
	}
}
