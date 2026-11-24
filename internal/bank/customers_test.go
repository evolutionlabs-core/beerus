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

var dob = clock.Date{Y: 1990, M: time.May, D: 14}

func cheapHash(t testing.TB, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func mustCustomer(t testing.TB, b *bank.Bank, key, name string) bank.Customer {
	t.Helper()
	c, err := b.CreateCustomer(context.Background(), key, name, "+2348030000001", dob, "22912345678", "", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustAccount(t testing.TB, b *bank.Bank, key, customer string) ledger.Account {
	t.Helper()
	a, err := b.OpenAccount(context.Background(), key, customer, bank.ProductCurrent, t0)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func wantErr(t testing.TB, err, target error, msg string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got %v, want %v", err, target)
	}
	if msg != "" && err.Error() != msg {
		t.Fatalf("message %q, want %q", err.Error(), msg)
	}
}

func lifecycle(t *testing.T, a *testBank) {
	ctx := context.Background()
	ca := mustCustomer(t, a.Bank, t.Name()+":cli:1", "Adaeze Okafor")
	if ca.BVN != "22912345678" || ca.Status != bank.CustomerActive || !ca.CreatedAt.Equal(t0) || ca.DOB != dob {
		t.Fatalf("customer %+v", ca)
	}
	acct := mustAccount(t, a.Bank, t.Name()+":open:1", ca.ID)
	if !world.ValidNUBAN("901", acct.NUBAN) || acct.Owner != ca.ID || acct.Name != "Adaeze Okafor" || acct.Product != bank.ProductCurrent || acct.Status != "active" || !acct.Customer {
		t.Fatalf("account %+v", acct)
	}
	if _, err := a.CashDeposit(ctx, t.Name()+":dep:1", acct.NUBAN, 500000, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CashWithdrawal(ctx, t.Name()+":wd:1", acct.NUBAN, 200000, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	err := a.CloseAccount(ctx, acct.NUBAN, t0.Add(3*time.Hour))
	wantErr(t, err, bank.ErrBalanceNotZero, fmt.Sprintf("bank: account balance is not zero: account %s holds ₦3,000.00; move it out by transfer or cash first", acct.NUBAN))
	err = a.CloseCustomer(ctx, ca.ID, t0.Add(3*time.Hour))
	wantErr(t, err, bank.ErrOpenAccounts, fmt.Sprintf("bank: customer has open accounts: customer %s still has 1 open account; close them first", ca.ID))
	mustAccount(t, a.Bank, t.Name()+":open:2", ca.ID)
	err = a.CloseCustomer(ctx, ca.ID, t0.Add(3*time.Hour))
	wantErr(t, err, bank.ErrOpenAccounts, fmt.Sprintf("bank: customer has open accounts: customer %s still has 2 open accounts; close them first", ca.ID))
	if _, err := a.CashWithdrawal(ctx, t.Name()+":wd:2", acct.NUBAN, 300000, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	accts, _, err := a.Accounts(ctx, ca.ID, 10, "")
	if err != nil || len(accts) != 2 {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	for _, ac := range accts {
		if err := a.CloseAccount(ctx, ac.NUBAN, t0.Add(4*time.Hour)); err != nil {
			t.Fatalf("close at ₦0: %v", err)
		}
	}
	if err := a.CloseAccount(ctx, acct.NUBAN, t0.Add(5*time.Hour)); err != nil {
		t.Fatalf("closing a closed account again: %v", err)
	}
	if _, err := a.CashDeposit(ctx, t.Name()+":dep:closed", acct.NUBAN, 100, t0.Add(5*time.Hour)); !errors.Is(err, ledger.ErrAccountClosed) {
		t.Fatalf("deposit to a closed account: %v", err)
	}
	if err := a.CloseCustomer(ctx, ca.ID, t0.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := a.CloseCustomer(ctx, ca.ID, t0.Add(6*time.Hour)); err != nil {
		t.Fatalf("closing a closed customer again: %v", err)
	}
	_, err = a.OpenAccount(ctx, t.Name()+":open:3", ca.ID, bank.ProductSavings, t0.Add(6*time.Hour))
	wantErr(t, err, bank.ErrCustomerClosed, "bank: customer is closed: "+ca.ID)
	cs, _, err := a.Customers(ctx, bank.MaxPage, "")
	closed := slices.IndexFunc(cs, func(c bank.Customer) bool { return c.ID == ca.ID })
	if err != nil || closed < 0 || cs[closed].Status != bank.CustomerClosed || cs[closed].ClosedAt == nil || !cs[closed].ClosedAt.Equal(t0.Add(5*time.Hour)) {
		t.Fatalf("customers %+v %v", cs, err)
	}
	err = a.CloseCustomer(ctx, "cus_missing", t0)
	wantErr(t, err, bank.ErrUnknownCustomer, "bank: unknown customer: cus_missing")
	err = a.CloseAccount(ctx, "9010000000", t0)
	wantErr(t, err, ledger.ErrUnknownAccount, "close account 9010000000: ledger: unknown account")
	over := mustAccount(t, a.Bank, t.Name()+":open:over", mustCustomer(t, a.Bank, t.Name()+":cli:2", "Chidi Eze").ID)
	if _, err := a.L.Post(ctx, ledger.Entry{Key: "od", Kind: "charge", PostingDate: clock.DateOf(t0), PostedAt: t0, Narration: "FEE", AllowOverdraft: true,
		Lines: []ledger.Line{{Account: bank.CustomerAccount(over.NUBAN), Amount: 12345}, {Account: bank.FeeIncome, Amount: -12345}}}); err != nil {
		t.Fatal(err)
	}
	err = a.CloseAccount(ctx, over.NUBAN, t0)
	wantErr(t, err, bank.ErrBalanceNotZero, fmt.Sprintf("bank: account balance is not zero: account %s holds -₦123.45; move it out by transfer or cash first", over.NUBAN))
	mustOK(t, a.L)
}

func TestNUBANsAndCustomerIDsAreNeverReused(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	c1 := mustCustomer(t, a.Bank, t.Name()+":cli:1", "Ngozi Ade")
	c2 := mustCustomer(t, a.Bank, t.Name()+":cli:2", "Ngozi Ade")
	if c1.ID == c2.ID || c1.ID[:4] != "cus_" {
		t.Fatalf("customer IDs %s and %s", c1.ID, c2.ID)
	}
	again := mustCustomer(t, a.Bank, t.Name()+":cli:1", "Ngozi Ade")
	if again.ID != c1.ID {
		t.Fatalf("replay gave %s, want %s", again.ID, c1.ID)
	}
	_, err := a.CreateCustomer(ctx, t.Name()+":cli:1", "Someone Else", "+2348030000001", dob, "22912345678", "", "", t0)
	wantErr(t, err, ledger.ErrIdempotency, "create customer "+c1.ID+": ledger: idempotency key reused with a different entry")
	if _, err := a.L.Pool().Exec(ctx, `DELETE FROM customers WHERE id = $1`, c1.ID); err == nil {
		t.Fatal("a customer row was deleted")
	}
	first := mustAccount(t, a.Bank, t.Name()+":open:1", c1.ID)
	if _, err := a.L.OpenAccount(ctx, ledger.Account{Code: bank.CustomerAccount(world.NUBAN("901", 2)), Name: "Legacy", Kind: ledger.Liability, Customer: true, NUBAN: world.NUBAN("901", 2), Product: "current", Owner: "legacy", KYCTier: 3}); err != nil {
		t.Fatal(err)
	}
	second := mustAccount(t, a.Bank, t.Name()+":open:2", c1.ID)
	if second.NUBAN != world.NUBAN("901", 3) {
		t.Fatalf("second NUBAN %s, want serial 3 after the taken serial 2: %s", second.NUBAN, world.NUBAN("901", 3))
	}
	if err := a.CloseAccount(ctx, first.NUBAN, t0); err != nil {
		t.Fatal(err)
	}
	third := mustAccount(t, a.Bank, t.Name()+":open:3", c2.ID)
	if third.NUBAN == first.NUBAN || third.NUBAN != world.NUBAN("901", 4) {
		t.Fatalf("third NUBAN %s reused or skipped", third.NUBAN)
	}
	if _, err := a.L.Pool().Exec(ctx, `DELETE FROM gl_accounts WHERE nuban = $1`, first.NUBAN); err == nil {
		t.Fatal("an account row was deleted")
	}
	if _, err := a.L.Pool().Exec(ctx, `SELECT setval('nuban_serial', $1)`, bank.MaxNUBANSerial); err != nil {
		t.Fatal(err)
	}
	_, err = a.OpenAccount(ctx, t.Name()+":open:last", c2.ID, bank.ProductCurrent, t0)
	wantErr(t, err, bank.ErrNUBANsUsed, "bank: no NUBANs left: all 999999999 serials are used")
}

func idempotentOpen(t *testing.T, a *testBank) {
	ctx := context.Background()
	c := mustCustomer(t, a.Bank, t.Name()+":cli:1", "Bola Adeyemi")
	var wg sync.WaitGroup
	got := make([]string, 8)
	errs := make([]error, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acct, err := a.OpenAccount(ctx, t.Name()+":open:race", c.ID, bank.ProductSavings, t0)
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
	_, err = a.OpenAccount(ctx, t.Name()+":open:race", c.ID, bank.ProductCurrent, t0)
	wantErr(t, err, ledger.ErrIdempotency, "open account "+got[0]+": ledger: idempotency key reused with a different entry")
	_, err = a.OpenAccount(ctx, t.Name()+":open:bad", c.ID, "fixed", t0)
	wantErr(t, err, ledger.ErrInvalid, `ledger: invalid entry: account type "fixed" must be current or savings`)
	_, err = a.OpenAccount(ctx, t.Name()+":open:nobody", "cus_nobody", bank.ProductCurrent, t0)
	wantErr(t, err, bank.ErrUnknownCustomer, "bank: unknown customer: cus_nobody")
}

func passwords(t *testing.T, a *testBank) {
	ctx := context.Background()
	hash := cheapHash(t, "correct horse")
	c, err := a.CreateCustomer(ctx, t.Name()+":cli:1", "Adaeze Okafor", "08030000001", dob, "22912345678", "adaeze", hash, t0)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := a.CreateCustomer(ctx, t.Name()+":cli:1", "Adaeze Okafor", "08030000001", dob, "22912345678", "adaeze", cheapHash(t, "correct horse"), t0)
	if err != nil || replay.ID != c.ID {
		t.Fatalf("replay with a username gave %+v %v", replay, err)
	}
	got, err := a.Authenticate(ctx, "adaeze", "correct horse")
	if err != nil || got.ID != c.ID || got.Username != "adaeze" {
		t.Fatalf("sign-in gave %+v %v", got, err)
	}
	var stored string
	if err := a.L.Pool().QueryRow(ctx, `SELECT password_hash FROM customers WHERE id = $1`, c.ID).Scan(&stored); err != nil || stored != hash {
		t.Fatalf("stored %q %v, want the bcrypt hash", stored, err)
	}
	for _, tc := range []struct{ user, pass string }{{"adaeze", "wrong"}, {"adaeze", ""}, {"nobody", "correct horse"}, {"", ""}} {
		_, err := a.Authenticate(ctx, tc.user, tc.pass)
		wantErr(t, err, bank.ErrBadLogin, "bank: wrong username or password")
	}
	_, err = a.CreateCustomer(ctx, t.Name()+":cli:2", "Another", "08030000002", dob, "22912345679", "adaeze", hash, t0)
	wantErr(t, err, bank.ErrUsernameTaken, "bank: username is taken: adaeze")
	mustCustomer(t, a.Bank, t.Name()+":cli:3", "No Login")
	if err := a.CloseCustomer(ctx, c.ID, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, err = a.Authenticate(ctx, "adaeze", "correct horse")
	wantErr(t, err, bank.ErrBadLogin, "bank: wrong username or password")
	_, err = bank.HashPassword("")
	wantErr(t, err, ledger.ErrInvalid, "ledger: invalid entry: password is empty")
}

func rejectedCustomers(t *testing.T, a *testBank) {
	ctx := context.Background()
	before, _, err := a.Customers(ctx, bank.MaxPage, "")
	if err != nil {
		t.Fatal(err)
	}
	hash := cheapHash(t, "pw")
	for _, tc := range []struct {
		key, name, phone string
		dob              clock.Date
		bvn, user, hash  string
		msg              string
	}{
		{"", "A", "080", dob, "229", "", "", "idempotency key is empty"},
		{"k", " ", "080", dob, "229", "", "", "name is empty"},
		{"k", "A", "080-1", dob, "229", "", "", `phone "080-1" must be digits with an optional leading +`},
		{"k", "A", "080", clock.Date{}, "229", "", "", "date of birth is empty"},
		{"k", "A", "080", clock.Date{Y: 2026, M: time.March, D: 3}, "229", "", "", "date of birth 2026-03-03 is after today, 2026-03-02"},
		{"k", "A", "080", dob, "22a", "", "", `BVN "22a" must be digits`},
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
	ctx := context.Background()
	var ids []string
	for i := 0; i < 5; i++ {
		c := mustCustomer(t, a.Bank, fmt.Sprintf("%s:cli:%d", t.Name(), i), fmt.Sprintf("Customer %d", i))
		ids = append(ids, c.ID)
		mustAccount(t, a.Bank, fmt.Sprintf("%s:open:%d", t.Name(), i), c.ID)
	}
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		page, next, err := a.Customers(ctx, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page {
			seen = append(seen, c.ID)
		}
		if next == "" {
			if pages != 2 {
				t.Fatalf("%d pages, want 3", pages+1)
			}
			break
		}
		cursor = next
	}
	if fmt.Sprint(seen) != fmt.Sprint(ids) {
		t.Fatalf("paged %v, want %v", seen, ids)
	}
	first, next, err := a.Accounts(ctx, "", 3, "")
	if err != nil || len(first) != 3 || next != first[2].NUBAN {
		t.Fatalf("accounts page %+v next %q %v", first, next, err)
	}
	rest, next, err := a.Accounts(ctx, "", 3, next)
	if err != nil || len(rest) != 2 || next != "" {
		t.Fatalf("accounts page %+v next %q %v", rest, next, err)
	}
	one, _, err := a.Accounts(ctx, ids[3], 10, "")
	if err != nil || len(one) != 1 || one[0].Owner != ids[3] {
		t.Fatalf("accounts of %s: %+v %v", ids[3], one, err)
	}
	for _, limit := range []int{0, bank.MaxPage + 1} {
		_, _, err := a.Customers(ctx, limit, "")
		wantErr(t, err, ledger.ErrInvalid, "ledger: invalid entry: limit must be from 1 to 1000")
	}
	_, _, err = a.Customers(ctx, 2, "cus_nope")
	wantErr(t, err, bank.ErrBadCursor, `bank: invalid cursor: "cus_nope"`)
	_, _, err = a.Accounts(ctx, "", 2, "0000000000")
	wantErr(t, err, bank.ErrBadCursor, `bank: invalid cursor: "0000000000"`)
	_, _, err = a.Accounts(ctx, "cus_nope", 2, "")
	wantErr(t, err, bank.ErrUnknownCustomer, "bank: unknown customer: cus_nope")
}

func statements(t *testing.T, a *testBank) {
	ctx := context.Background()
	c := mustCustomer(t, a.Bank, t.Name()+":cli:1", "Tolu Bello")
	n := mustAccount(t, a.Bank, t.Name()+":open:1", c.ID).NUBAN
	days := []time.Time{
		time.Date(2026, 2, 27, 23, 59, 0, 0, clock.Lagos),
		time.Date(2026, 2, 28, 0, 0, 0, 0, clock.Lagos),
		time.Date(2026, 3, 1, 12, 0, 0, 0, clock.Lagos),
		time.Date(2026, 3, 2, 0, 0, 0, 0, clock.Lagos),
	}
	for i, at := range days {
		if _, err := a.CashDeposit(ctx, fmt.Sprintf(t.Name()+":dep:%d", i), n, int64(100*(i+1)), at); err != nil {
			t.Fatal(err)
		}
	}
	feb28, mar1 := clock.Date{Y: 2026, M: time.February, D: 28}, clock.Date{Y: 2026, M: time.March, D: 1}
	got, next, err := a.Statement(ctx, n, feb28, mar1, 10, "")
	if err != nil || next != "" || len(got) != 2 || got[0].Amount != 200 || got[1].Amount != 300 || got[1].BalanceAfter != 600 {
		t.Fatalf("Feb 28 to Mar 1: %+v %q %v", got, next, err)
	}
	page, next, err := a.Statement(ctx, n, clock.Date{}, clock.Date{}, 3, "")
	if err != nil || len(page) != 3 || next != "3" {
		t.Fatalf("first page %+v %q %v", page, next, err)
	}
	page, next, err = a.Statement(ctx, n, clock.Date{}, clock.Date{}, 3, next)
	if err != nil || len(page) != 1 || page[0].BalanceAfter != 1000 || next != "" {
		t.Fatalf("second page %+v %q %v", page, next, err)
	}
	_, _, err = a.Statement(ctx, n, mar1, feb28, 10, "")
	wantErr(t, err, ledger.ErrInvalid, "ledger: invalid entry: from 2026-03-01 is after to 2026-02-28")
	_, _, err = a.Statement(ctx, n, clock.Date{}, clock.Date{}, 10, "x")
	wantErr(t, err, bank.ErrBadCursor, `bank: invalid cursor: "x"`)
	_, _, err = a.Statement(ctx, "9019999999", clock.Date{}, clock.Date{}, 10, "")
	wantErr(t, err, ledger.ErrUnknownAccount, "statement 9019999999: ledger: unknown account")
}

func nameEnquiry(t *testing.T, a *testBank) {
	ctx := context.Background()
	c := mustCustomer(t, a.Bank, t.Name()+":cli:1", "Adaeze Okafor")
	n := mustAccount(t, a.Bank, t.Name()+":open:1", c.ID).NUBAN
	name, err := a.NameEnquiry(ctx, n)
	if err != nil || name != "Adaeze Okafor" {
		t.Fatalf("name %q %v", name, err)
	}
	bad := n[:9] + fmt.Sprint((int(n[9]-'0')+1)%10)
	_, err = a.NameEnquiry(ctx, bad)
	wantErr(t, err, nip.ErrInvalidAccount, "name enquiry "+bad+": 07 invalid account")
	unknown := world.NUBAN("901", 777)
	_, err = a.NameEnquiry(ctx, unknown)
	wantErr(t, err, nip.ErrNoRecord, "name enquiry "+unknown+": 25 no record")
	if err := a.CloseAccount(ctx, n, t0); err != nil {
		t.Fatal(err)
	}
	_, err = a.NameEnquiry(ctx, n)
	wantErr(t, err, nip.ErrInvalidAccount, "name enquiry "+n+": account is closed: 07 invalid account")
}

func TestCustomers(t *testing.T) {
	a := newBank(t, "bank-a", "999001", "901", 0)
	for _, s := range []struct {
		name string
		run  func(*testing.T, *testBank)
	}{
		{"paging", paging},
		{"lifecycle", lifecycle},
		{"rejected input", rejectedCustomers},
		{"idempotent open", idempotentOpen},
		{"passwords", passwords},
		{"statements", statements},
		{"name enquiry", nameEnquiry},
	} {
		t.Run(s.name, func(t *testing.T) { s.run(t, a) })
	}
	mustOK(t, a.L)
}
