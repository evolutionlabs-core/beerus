package bank_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
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

func TestCustomersAndAccountsAtBankLevel(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	b := newBank(t, "bank-b", "999002", "902", 0)
	ca := mustCustomer(t, a.Bank, "cli:1", "Adaeze Okafor")
	cb := mustCustomer(t, b.Bank, "cli:1", "Adaeze Okafor")
	if ca.BVN != "22912345678" || cb.BVN != ca.BVN || ca.Status != bank.CustomerActive || !ca.CreatedAt.Equal(t0) || ca.DOB != dob {
		t.Fatalf("customers %+v and %+v", ca, cb)
	}
	acct := mustAccount(t, a.Bank, "open:1", ca.ID)
	if !world.ValidNUBAN("901", acct.NUBAN) || acct.Owner != ca.ID || acct.Name != "Adaeze Okafor" || acct.Product != bank.ProductCurrent || acct.Status != "active" || !acct.Customer {
		t.Fatalf("account %+v", acct)
	}
	if acct.NUBAN != world.NUBAN("901", 1) {
		t.Fatalf("first NUBAN %s, want serial 1: %s", acct.NUBAN, world.NUBAN("901", 1))
	}
	if _, err := a.CashDeposit(ctx, "dep:1", acct.NUBAN, 500000, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CashWithdrawal(ctx, "wd:1", acct.NUBAN, 200000, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	lines, next, err := a.Statement(ctx, acct.NUBAN, clock.Date{}, clock.Date{}, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "" || len(lines) != 2 || lines[0].Amount != 500000 || lines[0].BalanceAfter != 500000 || lines[1].Amount != -200000 || lines[1].BalanceAfter != 300000 {
		t.Fatalf("statement %+v next %q", lines, next)
	}
	err = a.CloseAccount(ctx, acct.NUBAN, t0.Add(3*time.Hour))
	wantErr(t, err, bank.ErrBalanceNotZero, fmt.Sprintf("bank: account balance is not zero: account %s holds ₦3,000.00; move it out by transfer or cash first", acct.NUBAN))
	err = a.CloseCustomer(ctx, ca.ID, t0.Add(3*time.Hour))
	wantErr(t, err, bank.ErrOpenAccounts, fmt.Sprintf("bank: customer has open accounts: customer %s still has 1 open account; close them first", ca.ID))
	mustAccount(t, a.Bank, "open:2", ca.ID)
	err = a.CloseCustomer(ctx, ca.ID, t0.Add(3*time.Hour))
	wantErr(t, err, bank.ErrOpenAccounts, fmt.Sprintf("bank: customer has open accounts: customer %s still has 2 open accounts; close them first", ca.ID))
	if _, err := a.CashWithdrawal(ctx, "wd:2", acct.NUBAN, 300000, t0.Add(3*time.Hour)); err != nil {
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
	if _, err := a.CashDeposit(ctx, "dep:closed", acct.NUBAN, 100, t0.Add(5*time.Hour)); !errors.Is(err, ledger.ErrAccountClosed) {
		t.Fatalf("deposit to a closed account: %v", err)
	}
	if err := a.CloseCustomer(ctx, ca.ID, t0.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := a.CloseCustomer(ctx, ca.ID, t0.Add(6*time.Hour)); err != nil {
		t.Fatalf("closing a closed customer again: %v", err)
	}
	_, err = a.OpenAccount(ctx, "open:3", ca.ID, bank.ProductSavings, t0.Add(6*time.Hour))
	wantErr(t, err, bank.ErrCustomerClosed, "bank: customer is closed: "+ca.ID)
	cs, _, err := a.Customers(ctx, 10, "")
	if err != nil || len(cs) != 1 || cs[0].Status != bank.CustomerClosed || cs[0].ClosedAt == nil || !cs[0].ClosedAt.Equal(t0.Add(5*time.Hour)) {
		t.Fatalf("customers %+v %v", cs, err)
	}
	err = a.CloseCustomer(ctx, "cus_missing", t0)
	wantErr(t, err, bank.ErrUnknownCustomer, "bank: unknown customer: cus_missing")
	err = a.CloseAccount(ctx, "9010000000", t0)
	wantErr(t, err, ledger.ErrUnknownAccount, "close account 9010000000: ledger: unknown account")
	mustOK(t, a.L)
}

func TestCloseAccountWithOverdrawnBalanceIsRefused(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	c := mustCustomer(t, a.Bank, "cli:1", "Chidi Eze")
	acct := mustAccount(t, a.Bank, "open:1", c.ID)
	if _, err := a.L.Post(ctx, ledger.Entry{Key: "od", Kind: "charge", PostingDate: clock.DateOf(t0), PostedAt: t0, Narration: "FEE", AllowOverdraft: true,
		Lines: []ledger.Line{{Account: bank.CustomerAccount(acct.NUBAN), Amount: 12345}, {Account: bank.FeeIncome, Amount: -12345}}}); err != nil {
		t.Fatal(err)
	}
	err := a.CloseAccount(ctx, acct.NUBAN, t0)
	wantErr(t, err, bank.ErrBalanceNotZero, fmt.Sprintf("bank: account balance is not zero: account %s holds -₦123.45; move it out by transfer or cash first", acct.NUBAN))
}

func TestNUBANsAndCustomerIDsAreNeverReused(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	c1 := mustCustomer(t, a.Bank, "cli:1", "Ngozi Ade")
	c2 := mustCustomer(t, a.Bank, "cli:2", "Ngozi Ade")
	if c1.ID == c2.ID || c1.ID[:4] != "cus_" {
		t.Fatalf("customer IDs %s and %s", c1.ID, c2.ID)
	}
	again := mustCustomer(t, a.Bank, "cli:1", "Ngozi Ade")
	if again.ID != c1.ID {
		t.Fatalf("replay gave %s, want %s", again.ID, c1.ID)
	}
	_, err := a.CreateCustomer(ctx, "cli:1", "Someone Else", "+2348030000001", dob, "22912345678", "", "", t0)
	wantErr(t, err, ledger.ErrIdempotency, "create customer "+c1.ID+": ledger: idempotency key reused with a different entry")
	if _, err := a.L.Pool().Exec(ctx, `DELETE FROM customers WHERE id = $1`, c1.ID); err == nil {
		t.Fatal("a customer row was deleted")
	}
	first := mustAccount(t, a.Bank, "open:1", c1.ID)
	if _, err := a.OpenCustomer(ctx, world.NUBAN("901", 2), "Legacy", "current", "legacy", 3); err != nil {
		t.Fatal(err)
	}
	second := mustAccount(t, a.Bank, "open:2", c1.ID)
	if second.NUBAN != world.NUBAN("901", 3) {
		t.Fatalf("second NUBAN %s, want serial 3 after the taken serial 2: %s", second.NUBAN, world.NUBAN("901", 3))
	}
	if err := a.CloseAccount(ctx, first.NUBAN, t0); err != nil {
		t.Fatal(err)
	}
	third := mustAccount(t, a.Bank, "open:3", c2.ID)
	if third.NUBAN == first.NUBAN || third.NUBAN != world.NUBAN("901", 4) {
		t.Fatalf("third NUBAN %s reused or skipped", third.NUBAN)
	}
	if _, err := a.L.Pool().Exec(ctx, `DELETE FROM gl_accounts WHERE nuban = $1`, first.NUBAN); err == nil {
		t.Fatal("an account row was deleted")
	}
	if _, err := a.L.Pool().Exec(ctx, `SELECT setval('nuban_serial', $1)`, bank.MaxNUBANSerial); err != nil {
		t.Fatal(err)
	}
	_, err = a.OpenAccount(ctx, "open:last", c2.ID, bank.ProductCurrent, t0)
	wantErr(t, err, bank.ErrNUBANsUsed, "bank: no NUBANs left: all 999999999 serials are used")
}

func TestOpenAccountIsIdempotent(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	c := mustCustomer(t, a.Bank, "cli:1", "Bola Adeyemi")
	var wg sync.WaitGroup
	got := make([]string, 8)
	errs := make([]error, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acct, err := a.OpenAccount(ctx, "open:race", c.ID, bank.ProductSavings, t0)
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
	_, err = a.OpenAccount(ctx, "open:race", c.ID, bank.ProductCurrent, t0)
	wantErr(t, err, ledger.ErrIdempotency, "open account "+got[0]+": ledger: idempotency key reused with a different entry")
	_, err = a.OpenAccount(ctx, "open:bad", c.ID, "fixed", t0)
	wantErr(t, err, ledger.ErrInvalid, `ledger: invalid entry: account type "fixed" must be current or savings`)
	_, err = a.OpenAccount(ctx, "open:nobody", "cus_nobody", bank.ProductCurrent, t0)
	wantErr(t, err, bank.ErrUnknownCustomer, "bank: unknown customer: cus_nobody")
}

func TestReverseNIPPostsEachLineAgainstTheSession(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 1)
	from := a.custs[0]
	before := bal(t, a.Bank, bank.CustomerAccount(from))
	sid := "999001260302100000000000000042"
	sent, _, err := a.SendNIP(ctx, bank.NIPSend{Key: sid, From: from, Amount: 10000000, SessionID: sid, Beneficiary: "CHIDI OKAFOR", BankName: "BANK B", At: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.ReverseNIP(ctx, sent.EntryID, "999001260302100000000000000043", t0.Add(2*time.Hour))
	wantErr(t, err, ledger.ErrInvalid, fmt.Sprintf("ledger: invalid entry: entry %d is not the NIP send for session 999001260302100000000000000043", sent.EntryID))
	dep, err := a.CashDeposit(ctx, "dep", from, 100, t0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.ReverseNIP(ctx, dep.EntryID, sid, t0.Add(2*time.Hour))
	wantErr(t, err, ledger.ErrInvalid, fmt.Sprintf("ledger: invalid entry: entry %d is not the NIP send for session %s", dep.EntryID, sid))
	rev, err := a.ReverseNIP(ctx, sent.EntryID, sid, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.ReverseNIP(ctx, sent.EntryID, sid, t0.Add(3*time.Hour))
	if err != nil || !again.Replayed || again.EntryID != rev.EntryID {
		t.Fatalf("second reversal %+v %v", again, err)
	}
	if got := bal(t, a.Bank, bank.CustomerAccount(from)); got != before+100 {
		t.Fatalf("balance %d after reversal, want %d", got, before+100)
	}
	lines, _, err := a.Statement(ctx, from, clock.Date{}, clock.Date{}, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	var credits []ledger.Event
	for _, ln := range lines {
		if ln.EntryID == rev.EntryID {
			credits = append(credits, ln)
		}
	}
	want := []struct {
		amount    int64
		narration string
	}{
		{10000000, "REV/NIP TRF TO CHIDI OKAFOR/BANK B/000000000042"},
		{5000, "REV/NIP CHARGE 000000000042"},
		{375, "REV/VAT ON NIP CHARGE 000000000042"},
		{5000, "REV/STAMP DUTY 000000000042"},
	}
	if len(credits) != len(want) {
		t.Fatalf("reversal has %d customer lines, want %d: %+v", len(credits), len(want), credits)
	}
	for i, w := range want {
		if credits[i].Amount != w.amount || credits[i].Narration != w.narration || credits[i].SessionID != sid {
			t.Fatalf("reversal line %d is %+v, want %d %q for session %s", i, credits[i], w.amount, w.narration, sid)
		}
	}
	if bal(t, a.Bank, bank.FeeIncome) != 0 || bal(t, a.Bank, bank.VATPayable) != 0 || bal(t, a.Bank, bank.NIPOutward) != 0 {
		t.Fatal("reversal left fee, VAT or payable behind")
	}
	mustOK(t, a.L)
}

func TestNoChargesSendsThePrincipalOnly(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 1)
	from := a.custs[0]
	before := bal(t, a.Bank, bank.CustomerAccount(from))
	sid := "999001260302100000000000000050"
	_, c, err := a.SendNIP(ctx, bank.NIPSend{Key: "close:bank-a:" + from, From: from, Amount: 2000000, SessionID: sid, Beneficiary: "X", BankName: "BANK B", At: t0.Add(time.Hour), NoCharges: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Total() != 0 {
		t.Fatalf("charges %+v, want none", c)
	}
	if got := before - bal(t, a.Bank, bank.CustomerAccount(from)); got != 2000000 {
		t.Fatalf("debited %d, want 2000000", got)
	}
	if bal(t, a.Bank, bank.FeeIncome)+bal(t, a.Bank, bank.VATPayable)+bal(t, a.Bank, bank.StampPayable) != 0 {
		t.Fatal("a no-charges send posted a charge")
	}
	mustOK(t, a.L)
}

func TestClosureReceiptSkipsEMTL(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 2)
	at := time.Date(2025, 6, 2, 10, 0, 0, 0, clock.Lagos)
	if _, err := a.ReceiveNIP(ctx, "999002250602100000000000000001", a.custs[0], 2000000, "X", "BANK B", false, at); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReceiveNIP(ctx, "999002250602100000000000000002", a.custs[1], 2000000, "X", "BANK B", true, at); err != nil {
		t.Fatal(err)
	}
	if got := bal(t, a.Bank, bank.CustomerAccount(a.custs[0])); got != 50000000+2000000-5000 {
		t.Fatalf("ordinary receipt balance %d, want EMTL taken", got)
	}
	if got := bal(t, a.Bank, bank.CustomerAccount(a.custs[1])); got != 50000000+2000000 {
		t.Fatalf("closure receipt balance %d, want the whole amount", got)
	}
	mustOK(t, a.L)
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	hash := cheapHash(t, "correct horse")
	c, err := a.CreateCustomer(ctx, "cli:1", "Adaeze Okafor", "08030000001", dob, "22912345678", "adaeze", hash, t0)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := a.CreateCustomer(ctx, "cli:1", "Adaeze Okafor", "08030000001", dob, "22912345678", "adaeze", hash, t0)
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
	_, err = a.CreateCustomer(ctx, "cli:2", "Another", "08030000002", dob, "22912345679", "adaeze", hash, t0)
	wantErr(t, err, bank.ErrUsernameTaken, "bank: username is taken: adaeze")
	mustCustomer(t, a.Bank, "cli:3", "No Login")
	if err := a.CloseCustomer(ctx, c.ID, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, err = a.Authenticate(ctx, "adaeze", "correct horse")
	wantErr(t, err, bank.ErrBadLogin, "bank: wrong username or password")
}

func TestHashPasswordIsBcrypt(t *testing.T) {
	h, err := bank.HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if cost, err := bcrypt.Cost([]byte(h)); err != nil || cost != bcrypt.DefaultCost {
		t.Fatalf("cost %d %v", cost, err)
	}
	if bcrypt.CompareHashAndPassword([]byte(h), []byte("s3cret")) != nil {
		t.Fatal("hash does not match its password")
	}
	_, err = bank.HashPassword("")
	wantErr(t, err, ledger.ErrInvalid, "ledger: invalid entry: password is empty")
}

func TestCreateCustomerRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
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
	cs, _, err := a.Customers(ctx, 10, "")
	if err != nil || len(cs) != 0 {
		t.Fatalf("bad input created customers: %+v %v", cs, err)
	}
}

func TestPaging(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	var ids []string
	for i := 0; i < 5; i++ {
		c := mustCustomer(t, a.Bank, fmt.Sprintf("cli:%d", i), fmt.Sprintf("Customer %d", i))
		ids = append(ids, c.ID)
		mustAccount(t, a.Bank, fmt.Sprintf("open:%d", i), c.ID)
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

func TestStatementDatesAndCursor(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	c := mustCustomer(t, a.Bank, "cli:1", "Tolu Bello")
	n := mustAccount(t, a.Bank, "open:1", c.ID).NUBAN
	days := []time.Time{
		time.Date(2026, 2, 27, 23, 59, 0, 0, clock.Lagos),
		time.Date(2026, 2, 28, 0, 0, 0, 0, clock.Lagos),
		time.Date(2026, 3, 1, 12, 0, 0, 0, clock.Lagos),
		time.Date(2026, 3, 2, 0, 0, 0, 0, clock.Lagos),
	}
	for i, at := range days {
		if _, err := a.CashDeposit(ctx, fmt.Sprintf("dep:%d", i), n, int64(100*(i+1)), at); err != nil {
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

func TestNameEnquiry(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 0)
	c := mustCustomer(t, a.Bank, "cli:1", "Adaeze Okafor")
	n := mustAccount(t, a.Bank, "open:1", c.ID).NUBAN
	name, err := a.NameEnquiry(ctx, n)
	if err != nil || name != "Adaeze Okafor" {
		t.Fatalf("name %q %v", name, err)
	}
	bad := n[:9] + fmt.Sprint((int(n[9]-'0')+1)%10)
	_, err = a.NameEnquiry(ctx, bad)
	wantErr(t, err, nibss.ErrInvalidAccount, "name enquiry "+bad+": 07 invalid account")
	unknown := world.NUBAN("901", 777)
	_, err = a.NameEnquiry(ctx, unknown)
	wantErr(t, err, nibss.ErrNoRecord, "name enquiry "+unknown+": 25 no record")
	if err := a.CloseAccount(ctx, n, t0); err != nil {
		t.Fatal(err)
	}
	_, err = a.NameEnquiry(ctx, n)
	wantErr(t, err, nibss.ErrInvalidAccount, "name enquiry "+n+": account is closed: 07 invalid account")
}
