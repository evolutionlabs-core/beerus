package bank_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var (
	dob = clock.Date{Y: 1990, M: time.May, D: 14}
	ctx = context.Background()
)

func chk(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func cheapHash(t testing.TB, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	chk(t, err)
	return string(h)
}

func mustCustomer(t testing.TB, b *bank.Bank, key, name string) bank.Customer {
	t.Helper()
	c, err := b.CreateCustomer(ctx, key, name, "+2348030000001", dob, "22912345678", "", "", t0)
	chk(t, err)
	return c
}

func mustAccount(t testing.TB, b *bank.Bank, key, customer string) ledger.Account {
	t.Helper()
	a, err := b.OpenAccount(ctx, key, customer, bank.ProductCurrent, t0)
	chk(t, err)
	return a
}

func wantErr(t testing.TB, err, target error, msg string) {
	t.Helper()
	if !errors.Is(err, target) || err.Error() != msg {
		t.Fatalf("got %v, want %v: %q", err, target, msg)
	}
}

func lifecycle(t *testing.T, a *testBank) {
	k := t.Name() + ":"
	ca := mustCustomer(t, a.Bank, k+"cli1", "Adaeze Okafor")
	acct := mustAccount(t, a.Bank, k+"open1", ca.ID)
	if ca.Status != bank.CustomerActive || !ca.CreatedAt.Equal(t0) || !world.ValidNUBAN("901", acct.NUBAN) || acct.Owner != ca.ID || acct.Status != "active" || !acct.Customer {
		t.Fatalf("customer %+v account %+v", ca, acct)
	}
	_, err := a.CashDeposit(ctx, k+"dep1", acct.NUBAN, 500000, t0.Add(time.Hour))
	chk(t, err)
	_, err = a.CashWithdrawal(ctx, k+"wd1", acct.NUBAN, 200000, t0.Add(2*time.Hour))
	chk(t, err)
	at := t0.Add(3 * time.Hour)
	wantErr(t, a.CloseAccount(ctx, acct.NUBAN, at), bank.ErrBalanceNotZero, fmt.Sprintf("bank: account balance is not zero: account %s holds ₦3,000.00; move it out by transfer or cash first", acct.NUBAN))
	wantErr(t, a.CloseCustomer(ctx, ca.ID, at), bank.ErrOpenAccounts, fmt.Sprintf("bank: customer has open accounts: customer %s still has 1 open account; close them first", ca.ID))
	mustAccount(t, a.Bank, k+"open2", ca.ID)
	wantErr(t, a.CloseCustomer(ctx, ca.ID, at), bank.ErrOpenAccounts, fmt.Sprintf("bank: customer has open accounts: customer %s still has 2 open accounts; close them first", ca.ID))
	_, err = a.CashWithdrawal(ctx, k+"wd2", acct.NUBAN, 300000, at)
	chk(t, err)
	accts, _, err := a.Accounts(ctx, ca.ID, 10, "")
	chk(t, err)
	for _, ac := range accts {
		chk(t, a.CloseAccount(ctx, ac.NUBAN, t0.Add(4*time.Hour)))
	}
	chk(t, a.CloseAccount(ctx, acct.NUBAN, t0.Add(5*time.Hour)))
	if _, err := a.CashDeposit(ctx, k+"depc", acct.NUBAN, 100, at); !errors.Is(err, ledger.ErrAccountClosed) {
		t.Fatalf("deposit to a closed account: %v", err)
	}
	chk(t, a.CloseCustomer(ctx, ca.ID, t0.Add(5*time.Hour)))
	chk(t, a.CloseCustomer(ctx, ca.ID, t0.Add(6*time.Hour)))
	_, err = a.OpenAccount(ctx, k+"open3", ca.ID, bank.ProductSavings, t0.Add(6*time.Hour))
	wantErr(t, err, bank.ErrCustomerClosed, "bank: customer is closed: "+ca.ID)
	cs, _, err := a.Customers(ctx, bank.MaxPage, "")
	i := slices.IndexFunc(cs, func(c bank.Customer) bool { return c.ID == ca.ID })
	if err != nil || i < 0 || cs[i].Status != bank.CustomerClosed || cs[i].ClosedAt == nil || !cs[i].ClosedAt.Equal(t0.Add(5*time.Hour)) {
		t.Fatalf("customers %+v %v", cs, err)
	}
	wantErr(t, a.CloseCustomer(ctx, "cus_missing", t0), bank.ErrUnknownCustomer, "bank: unknown customer: cus_missing")
	wantErr(t, a.CloseAccount(ctx, "9010000000", t0), ledger.ErrUnknownAccount, "close account 9010000000: ledger: unknown account")
	over := mustAccount(t, a.Bank, k+"overa", mustCustomer(t, a.Bank, k+"cli2", "Chidi Eze").ID)
	_, err = a.L.Post(ctx, ledger.Entry{Key: "od", Kind: "charge", PostingDate: clock.DateOf(t0), PostedAt: t0, Narration: "FEE", AllowOverdraft: true,
		Lines: []ledger.Line{{Account: bank.CustomerAccount(over.NUBAN), Amount: 12345}, {Account: bank.FeeIncome, Amount: -12345}}})
	chk(t, err)
	wantErr(t, a.CloseAccount(ctx, over.NUBAN, t0), bank.ErrBalanceNotZero, fmt.Sprintf("bank: account balance is not zero: account %s holds -₦123.45; move it out by transfer or cash first", over.NUBAN))
}

func neverReused(t *testing.T, a *testBank) {
	k := t.Name() + ":"
	c1, c2 := mustCustomer(t, a.Bank, k+"cli1", "Ngozi Ade"), mustCustomer(t, a.Bank, k+"cli2", "Ngozi Ade")
	if c1.ID == c2.ID || mustCustomer(t, a.Bank, k+"cli1", "Ngozi Ade").ID != c1.ID {
		t.Fatalf("customer IDs %s and %s, or a replay gave another", c1.ID, c2.ID)
	}
	_, err := a.CreateCustomer(ctx, k+"cli1", "Someone Else", "+2348030000001", dob, "22912345678", "", "", t0)
	wantErr(t, err, ledger.ErrIdempotency, "create customer "+c1.ID+": ledger: idempotency key reused with a different entry")
	if _, err := a.L.Pool().Exec(ctx, `DELETE FROM customers WHERE id = $1`, c1.ID); err == nil {
		t.Fatal("a customer row was deleted")
	}
	first := mustAccount(t, a.Bank, k+"open1", c1.ID)
	serial := uint64(1)
	for world.NUBAN("901", serial) != first.NUBAN {
		serial++
	}
	taken := world.NUBAN("901", serial+1)
	_, err = a.L.OpenAccount(ctx, ledger.Account{Code: bank.CustomerAccount(taken), Name: "Legacy", Kind: ledger.Liability, Customer: true, NUBAN: taken, Product: "current", Owner: "legacy", KYCTier: 3})
	chk(t, err)
	second := mustAccount(t, a.Bank, k+"open2", c1.ID)
	chk(t, a.CloseAccount(ctx, first.NUBAN, t0))
	third := mustAccount(t, a.Bank, k+"open3", c2.ID)
	if second.NUBAN != world.NUBAN("901", serial+2) || third.NUBAN != world.NUBAN("901", serial+3) {
		t.Fatalf("NUBANs %s and %s reused or skipped after serial %d", second.NUBAN, third.NUBAN, serial)
	}
	if _, err := a.L.Pool().Exec(ctx, `DELETE FROM gl_accounts WHERE nuban = $1`, first.NUBAN); err == nil {
		t.Fatal("an account row was deleted")
	}
	_, err = a.L.Pool().Exec(ctx, `SELECT setval('nuban_serial', $1)`, bank.MaxNUBANSerial)
	chk(t, err)
	_, err = a.OpenAccount(ctx, k+"last", c2.ID, bank.ProductCurrent, t0)
	wantErr(t, err, bank.ErrNUBANsUsed, "bank: no NUBANs left: all 999999999 serials are used")
}

func idempotentOpen(t *testing.T, a *testBank) {
	k := t.Name() + ":"
	c := mustCustomer(t, a.Bank, k+"cli1", "Bola Adeyemi")
	var wg sync.WaitGroup
	got, errs := make([]string, 8), make([]error, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acct, err := a.OpenAccount(ctx, k+"race", c.ID, bank.ProductSavings, t0)
			got[i], errs[i] = acct.NUBAN, err
		}()
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil || got[i] != got[0] {
			t.Fatalf("concurrent open %d gave %s, %v; want %s", i, got[i], errs[i], got[0])
		}
	}
	accts, _, err := a.Accounts(ctx, c.ID, 10, "")
	if err != nil || len(accts) != 1 {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	_, err = a.OpenAccount(ctx, k+"race", c.ID, bank.ProductCurrent, t0)
	wantErr(t, err, ledger.ErrIdempotency, "open account "+got[0]+": ledger: idempotency key reused with a different entry")
	_, err = a.OpenAccount(ctx, k+"bad", c.ID, "fixed", t0)
	wantErr(t, err, ledger.ErrInvalid, `ledger: invalid entry: account type "fixed" must be current or savings`)
	_, err = a.OpenAccount(ctx, k+"nobody", "cus_nobody", bank.ProductCurrent, t0)
	wantErr(t, err, bank.ErrUnknownCustomer, "bank: unknown customer: cus_nobody")
}

func passwords(t *testing.T, a *testBank) {
	k := t.Name() + ":"
	hash := cheapHash(t, "correct horse")
	create := func(key, name, user, h string) (bank.Customer, error) {
		return a.CreateCustomer(ctx, k+key, name, "08030000001", dob, "22912345678", user, h, t0)
	}
	c, err := create("cli1", "Adaeze Okafor", "adaeze", hash)
	chk(t, err)
	replay, err := create("cli1", "Adaeze Okafor", "adaeze", cheapHash(t, "correct horse"))
	got, err2 := a.Authenticate(ctx, "adaeze", "correct horse")
	if err != nil || replay.ID != c.ID || err2 != nil || got.ID != c.ID || got.Username != "adaeze" {
		t.Fatalf("replay %+v %v, sign-in %+v %v", replay, err, got, err2)
	}
	for _, tc := range []struct{ user, pass string }{{"adaeze", "wrong"}, {"nobody", "correct horse"}, {"", ""}} {
		_, err := a.Authenticate(ctx, tc.user, tc.pass)
		wantErr(t, err, bank.ErrBadLogin, "bank: wrong username or password")
	}
	_, err = create("cli2", "Another", "adaeze", hash)
	wantErr(t, err, bank.ErrUsernameTaken, "bank: username is taken: adaeze")
	chk(t, a.CloseCustomer(ctx, c.ID, t0.Add(time.Hour)))
	_, err = a.Authenticate(ctx, "adaeze", "correct horse")
	wantErr(t, err, bank.ErrBadLogin, "bank: wrong username or password")
	_, err = bank.HashPassword("")
	wantErr(t, err, ledger.ErrInvalid, "ledger: invalid entry: password is empty")
}

func rejectedCustomers(t *testing.T, a *testBank) {
	before, _, err := a.Customers(ctx, bank.MaxPage, "")
	chk(t, err)
	hash := cheapHash(t, "pw")
	future := clock.Date{Y: 2026, M: time.March, D: 3}
	for _, tc := range []struct {
		key, name, phone string
		dob              clock.Date
		bvn, user, hash  string
		msg              string
	}{
		{"", "A", "080", dob, "229", "", "", "idempotency key is empty"},
		{"k", "A", "080-1", dob, "229", "", "", `phone "080-1" must be digits with an optional leading +`},
		{"k", "A", "080", future, "229", "", "", "date of birth 2026-03-03 is after today, 2026-03-02"},
		{"k", "A", "080", dob, "229", "", hash, "a password needs a username"},
		{"k", "A", "080", dob, "229", "Ada", hash, `username "Ada" may only use a-z, 0-9, dot, dash and underscore`},
		{"k", "A", "080", dob, "229", "ada", "", `username "ada" needs a password`},
		{"k", "A", "080", dob, "229", "ada", "plaintext", "password hash is not a bcrypt hash"},
	} {
		_, err := a.CreateCustomer(ctx, tc.key, tc.name, tc.phone, tc.dob, tc.bvn, tc.user, tc.hash, t0)
		wantErr(t, err, ledger.ErrInvalid, "ledger: invalid entry: "+tc.msg)
	}
	after, _, err := a.Customers(ctx, bank.MaxPage, "")
	if err != nil || len(after) != len(before) {
		t.Fatalf("bad input created customers: %d before and %d after, %v", len(before), len(after), err)
	}
}

func paging(t *testing.T, a *testBank) {
	k := t.Name() + ":"
	var ids, seen []string
	for i := 0; i < 5; i++ {
		c := mustCustomer(t, a.Bank, fmt.Sprintf("%scli%d", k, i), "Customer")
		ids = append(ids, c.ID)
		mustAccount(t, a.Bank, fmt.Sprintf("%sopen%d", k, i), c.ID)
	}
	pages := 0
	for cursor := "x"; cursor != ""; pages++ {
		if cursor == "x" {
			cursor = ""
		}
		page, next, err := a.Customers(ctx, 2, cursor)
		chk(t, err)
		for _, c := range page {
			seen = append(seen, c.ID)
		}
		cursor = next
	}
	first, next, err := a.Accounts(ctx, "", 3, "")
	rest, end, err2 := a.Accounts(ctx, "", 3, next)
	one, _, err3 := a.Accounts(ctx, ids[3], 10, "")
	if pages != 3 || fmt.Sprint(seen) != fmt.Sprint(ids) || err != nil || err2 != nil || err3 != nil || len(first) != 3 || next != first[2].NUBAN || len(rest) != 2 || end != "" || len(one) != 1 || one[0].Owner != ids[3] {
		t.Fatalf("%d pages of %v, want %v; accounts %+v then %+v, of one customer %+v", pages, seen, ids, first, rest, one)
	}
	_, _, err = a.Customers(ctx, bank.MaxPage+1, "")
	wantErr(t, err, ledger.ErrInvalid, "ledger: invalid entry: limit must be from 1 to 1000")
	_, _, err = a.Customers(ctx, 2, "cus_nope")
	wantErr(t, err, bank.ErrBadCursor, `bank: invalid cursor: "cus_nope"`)
	_, _, err = a.Accounts(ctx, "cus_nope", 2, "")
	wantErr(t, err, bank.ErrUnknownCustomer, "bank: unknown customer: cus_nope")
}

func statements(t *testing.T, a *testBank) {
	k := t.Name() + ":"
	n := mustAccount(t, a.Bank, k+"open1", mustCustomer(t, a.Bank, k+"cli1", "Tolu Bello").ID).NUBAN
	for i, at := range []time.Time{
		time.Date(2026, 2, 27, 23, 59, 0, 0, clock.Lagos), time.Date(2026, 2, 28, 0, 0, 0, 0, clock.Lagos),
		time.Date(2026, 3, 1, 12, 0, 0, 0, clock.Lagos), time.Date(2026, 3, 2, 0, 0, 0, 0, clock.Lagos),
	} {
		_, err := a.CashDeposit(ctx, fmt.Sprintf("%sdep%d", k, i), n, int64(100*(i+1)), at)
		chk(t, err)
	}
	feb28, mar1, none := clock.Date{Y: 2026, M: time.February, D: 28}, clock.Date{Y: 2026, M: time.March, D: 1}, clock.Date{}
	window, next, err := a.Statement(ctx, n, feb28, mar1, 10, "")
	if err != nil || next != "" || len(window) != 2 || window[0].Amount != 200 || window[1].Amount != 300 || window[1].BalanceAfter != 600 {
		t.Fatalf("Feb 28 to Mar 1: %+v %q %v", window, next, err)
	}
	page, next, err := a.Statement(ctx, n, none, none, 3, "")
	last, end, err2 := a.Statement(ctx, n, none, none, 3, next)
	if err != nil || err2 != nil || len(page) != 3 || next != "3" || len(last) != 1 || last[0].BalanceAfter != 1000 || end != "" {
		t.Fatalf("pages %+v %q and %+v %q, %v %v", page, next, last, end, err, err2)
	}
	_, _, err = a.Statement(ctx, n, mar1, feb28, 10, "")
	wantErr(t, err, ledger.ErrInvalid, "ledger: invalid entry: from 2026-03-01 is after to 2026-02-28")
	_, _, err = a.Statement(ctx, n, none, none, 10, "x")
	wantErr(t, err, bank.ErrBadCursor, `bank: invalid cursor: "x"`)
	_, _, err = a.Statement(ctx, "9019999999", none, none, 10, "")
	wantErr(t, err, ledger.ErrUnknownAccount, "statement 9019999999: ledger: unknown account")
}

func nameEnquiry(t *testing.T, a *testBank) {
	k := t.Name() + ":"
	n := mustAccount(t, a.Bank, k+"open1", mustCustomer(t, a.Bank, k+"cli1", "Adaeze Okafor").ID).NUBAN
	name, err := a.NameEnquiry(ctx, n)
	if err != nil || name != "Adaeze Okafor" {
		t.Fatalf("name %q %v", name, err)
	}
	bad, unknown := n[:9]+fmt.Sprint((int(n[9]-'0')+1)%10), world.NUBAN("901", 777)
	_, err = a.NameEnquiry(ctx, bad)
	wantErr(t, err, nip.ErrInvalidAccount, "name enquiry "+bad+": 07 invalid account")
	_, err = a.NameEnquiry(ctx, unknown)
	wantErr(t, err, nip.ErrNoRecord, "name enquiry "+unknown+": 25 no record")
	chk(t, a.CloseAccount(ctx, n, t0))
	_, err = a.NameEnquiry(ctx, n)
	wantErr(t, err, nip.ErrInvalidAccount, "name enquiry "+n+": account is closed: 07 invalid account")
}

func TestCustomers(t *testing.T) {
	t.Parallel()
	a := newBank(t, "bank-a", "999001", "901", 0)
	for _, s := range []struct {
		name string
		run  func(*testing.T, *testBank)
	}{
		{"paging", paging}, {"lifecycle", lifecycle}, {"rejected input", rejectedCustomers}, {"idempotent open", idempotentOpen},
		{"passwords", passwords}, {"statements", statements}, {"name enquiry", nameEnquiry}, {"numbers never reused", neverReused},
	} {
		t.Run(s.name, func(t *testing.T) { s.run(t, a) })
	}
	mustOK(t, a.L)
}
