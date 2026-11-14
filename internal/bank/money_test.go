package bank_test

import (
	"math"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
)

func TestNairaParseAndFormat(t *testing.T) {
	parsed := []struct {
		in   string
		want int64
		err  string
	}{
		{"25000", 2500000, ""},
		{" 2,500.5 ", 250050, ""},
		{"0.01", 1, ""},
		{"0", 0, `amount "0" must be a positive naira amount`},
		{"12.345", 0, `amount "12.345" must look like 25000 or 25000.50 (naira)`},
		{"abc", 0, `amount "abc" must look like 25000 or 25000.50 (naira)`},
		{"", 0, `amount "" must look like 25000 or 25000.50 (naira)`},
		{"99999999999999999999", 0, `amount "99999999999999999999" must be a positive naira amount`},
	}
	for _, c := range parsed {
		got, err := bank.ParseNaira(c.in)
		if got != c.want || (err == nil) != (c.err == "") || (err != nil && err.Error() != c.err) {
			t.Errorf("ParseNaira(%q) = %d, %v; want %d, %q", c.in, got, err, c.want, c.err)
		}
	}
	for minor, want := range map[int64]string{0: "₦0.00", 5: "₦0.05", 123456789: "₦1,234,567.89", -250050: "−₦2,500.50"} {
		if got := bank.FormatNaira(minor); got != want {
			t.Errorf("FormatNaira(%d) = %q, want %q", minor, got, want)
		}
	}
	if got := bank.Naira(-250050); got != "-₦2,500.50" {
		t.Errorf("Naira(-250050) = %q", got)
	}
}

func TestTransferLimits(t *testing.T) {
	if _, ok := bank.AddMinor(math.MaxInt64, 1); ok {
		t.Error("AddMinor did not report an overflow")
	}
	if _, ok := bank.AddMinor(math.MinInt64, -1); ok {
		t.Error("AddMinor did not report an underflow")
	}
	if got, ok := bank.AddMinor(40, 2); !ok || got != 42 {
		t.Errorf("AddMinor(40, 2) = %d, %v", got, ok)
	}
	if got, ok := bank.TotalDebit(10000, charges.Transfer{Fee: 1075, VAT: 80, StampDuty: 50}); !ok || got != 11205 {
		t.Errorf("TotalDebit = %d, %v", got, ok)
	}
	if _, ok := bank.TotalDebit(math.MaxInt64-10, charges.Transfer{Fee: 1075}); ok {
		t.Error("TotalDebit did not report an overflow")
	}
	if err := bank.CheckNarration(strings.Repeat("é", bank.MaxNarrationRunes)); err != nil {
		t.Errorf("a narration at the limit: %v", err)
	}
	if err := bank.CheckNarration(strings.Repeat("a", bank.MaxNarrationRunes+1)); err == nil || err.Error() != "narration has 101 characters; the limit is 100" {
		t.Errorf("a long narration: %v", err)
	}
	if err := bank.CheckNarration("a\nb"); err == nil || err.Error() != "narration must not contain control characters" {
		t.Errorf("a control character: %v", err)
	}
}

func TestCashRefusalMessages(t *testing.T) {
	short := &bank.InsufficientFundsError{Account: "0123456789", Balance: 500000, Amount: 750000}
	if got, want := short.Message(), "Account 0123456789 holds ₦5,000.00 and ₦7,500.00 was asked for; withdraw ₦5,000.00 or less."; got != want {
		t.Errorf("insufficient funds: %q", got)
	}
	over := &bank.InsufficientFundsError{Account: "0123456789", Balance: -100, Amount: 200}
	if got, want := over.Message(), "Account 0123456789 holds −₦1.00 and ₦2.00 was asked for; withdraw ₦0.00 or less."; got != want {
		t.Errorf("overdrawn: %q", got)
	}
	capped := &bank.DailyLimitError{Account: "0123456789", Limit: 10000000, Used: 9000000, Amount: 2000000}
	if got, want := capped.Message(), "Account 0123456789 has used ₦90,000.00 of its ₦100,000.00 daily limit and ₦20,000.00 was asked for; withdraw ₦10,000.00 or less today."; got != want {
		t.Errorf("daily limit: %q", got)
	}
	if short.Unwrap() != ledger.ErrInsufficientFunds || capped.Unwrap() != ledger.ErrDailyLimit {
		t.Error("the refusal errors do not unwrap to the ledger errors")
	}
}
