package bank_test

import (
	"math"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/money"
)

func TestNairaParseAndFormat(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		err  string
	}{
		{"25000", 2500000, ""},
		{" 2,500.5 ", 250050, ""},
		{"0.01", 1, ""},
		{"0", 0, `amount "0" must be a positive naira amount`},
		{"12.345", 0, `amount "12.345" must look like 25000 or 25000.50 (naira)`},
		{"99999999999999999999", 0, `amount "99999999999999999999" is too large; the largest amount is ₦92,233,720,368,547,758.07`},
	} {
		got, err := money.ParseNaira(c.in)
		if got != c.want || (err == nil) != (c.err == "") || (err != nil && err.Error() != c.err) {
			t.Errorf("ParseNaira(%q) = %d, %v; want %d, %q", c.in, got, err, c.want, c.err)
		}
	}
	for minor, want := range map[int64]string{0: "₦0.00", 5: "₦0.05", 123456789: "₦1,234,567.89", -250050: "−₦2,500.50"} {
		if got := money.FormatNaira(minor); got != want {
			t.Errorf("FormatNaira(%d) = %q, want %q", minor, got, want)
		}
	}
}

func TestTransferLimits(t *testing.T) {
	_, over := money.AddMinor(math.MaxInt64, 1)
	_, under := money.AddMinor(math.MinInt64, -1)
	sum, ok := money.AddMinor(40, 2)
	total, okTotal := bank.TotalDebit(10000, charges.Transfer{Fee: 1075, VAT: 80, StampDuty: 50})
	_, overTotal := bank.TotalDebit(math.MaxInt64-10, charges.Transfer{Fee: 1075})
	if over || under || !ok || sum != 42 || !okTotal || total != 11205 || overTotal {
		t.Errorf("AddMinor and TotalDebit: %v %v %v %d %v %d %v", over, under, ok, sum, okTotal, total, overTotal)
	}
	for _, c := range []struct{ narration, want string }{
		{strings.Repeat("é", bank.MaxNarrationRunes), ""},
		{strings.Repeat("a", bank.MaxNarrationRunes+1), "narration has 101 characters; the limit is 100"},
		{"a\nb", "narration must not contain control characters"},
	} {
		if err := bank.CheckNarration(c.narration); (err == nil) != (c.want == "") || (err != nil && err.Error() != c.want) {
			t.Errorf("CheckNarration: %v, want %q", err, c.want)
		}
	}
}

func TestCashRefusalMessages(t *testing.T) {
	short := &bank.InsufficientFundsError{Account: "0123456789", Balance: 500000, Amount: 750000}
	over := &bank.InsufficientFundsError{Account: "0123456789", Balance: -100, Amount: 200}
	capped := &bank.DailyLimitError{Account: "0123456789", Limit: 10000000, Used: 9000000, Amount: 2000000}
	for got, want := range map[string]string{
		short.Message():  "Account 0123456789 holds ₦5,000.00 and ₦7,500.00 was asked for; withdraw ₦5,000.00 or less.",
		over.Message():   "Account 0123456789 holds −₦1.00 and ₦2.00 was asked for; withdraw ₦0.00 or less.",
		capped.Message(): "Account 0123456789 has used ₦90,000.00 of its ₦100,000.00 daily limit and ₦20,000.00 was asked for; withdraw ₦10,000.00 or less today.",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if short.Unwrap() != ledger.ErrInsufficientFunds || capped.Unwrap() != ledger.ErrDailyLimit {
		t.Error("the refusal errors do not unwrap to the ledger errors")
	}
}
