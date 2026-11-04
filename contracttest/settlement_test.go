package contracttest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
)

func checkSettled(t *testing.T, app *connect.App) connect.SettlementCheck {
	t.Helper()
	c, err := app.CheckSettlement()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Problems) > 0 {
		t.Fatalf("settlement: %v", c.Problems)
	}
	return c
}

func TestNIPSettlement(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	first := checkSettled(t, s.app)
	if first.Sessions == 0 || time.Since(first.SettledThrough) > 4*24*time.Hour {
		t.Fatalf("settlement after start-up: %+v", first)
	}
	w := s.app.World()
	from, to := w.AccountsAt("adaeze", "bank-a")[0], w.AccountsAt("ngozi", "bank-b")[0]
	s.ctl("POST", "/transfers", map[string]any{"from": from.Key, "to": to.Key, "amount": "1500"})
	s.advance("72h")
	later := checkSettled(t, s.app)
	if later.Sessions <= first.Sessions || !later.SettledThrough.After(first.SettledThrough) {
		t.Fatalf("settlement did not move on: %+v then %+v", first, later)
	}
	if c, err := s.app.VerifyBank(); err != nil || !c.Ledger.OK() || len(c.Mismatches) > 0 {
		t.Fatalf("bank ledgers: %v %v %v", err, c.Ledger, c.Mismatches)
	}
	snap := s.ctl("POST", "/snapshots", map[string]string{"name": "settled"})
	s.advance("48h")
	checkSettled(t, s.app)
	s.ctl("POST", "/snapshots/"+snap["id"].(string)+"/restore", nil)
	if c := checkSettled(t, s.app); c.Sessions != later.Sessions {
		t.Fatalf("after restore: %d sessions, want %d", c.Sessions, later.Sessions)
	}
}

func TestScenariosSettle(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	w := s.app.World()
	ran := 0
	for _, name := range []string{"debit_then_reversal", "late_reversal", "fee_vat_levy_lines", "identical_same_day_fees", "pending_amount_change", "own_account_transfer_across_banks"} {
		for _, inst := range []string{"bank-a", "bank-b"} {
			if name == "own_account_transfer_across_banks" && inst == "bank-b" {
				continue
			}
			acc := w.AccountsAt("adaeze", inst)[0]
			if _, err := s.ctlTry("POST", "/scenarios/"+name, map[string]any{"account_id": acc.Key}); err != nil {
				t.Fatalf("%s on %s: %v", name, inst, err)
			}
			ran++
		}
	}
	checkSettled(t, s.app)
	s.advance("72h")
	checkSettled(t, s.app)
	s.advance("240h")
	if c := checkSettled(t, s.app); ran == 0 || c.Sessions == 0 {
		t.Fatalf("ran %d scenarios, %d sessions", ran, c.Sessions)
	}
}

func TestNIPSessionsCannotChangeOnALiveSetup(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	checkSettled(t, s.app)
	cfg, err := config.Load(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.NIPSessions = 12
	if _, err := connect.New(cfg, connect.Options{}); err == nil || !strings.Contains(err.Error(), "nip_sessions must stay 4") {
		t.Fatalf("starting with a different schedule: %v", err)
	}
}

func TestSettlementCheckDoesNotTrustTheBooks(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	checkSettled(t, s.app)
	st, err := store.Open(context.Background(), s.cfg.DatabaseURL, s.cfg.Bank("bank-a").Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Now()
	_, err = ledger.New(st.Pool, nil).Post(context.Background(), ledger.Entry{
		Key: "tamper", Kind: "feed", PostingDate: clock.DateOf(at), PostedAt: at, Narration: "Tamper",
		Lines: []ledger.Line{{Account: bank.Vault, Amount: 100}, {Account: bank.NIPInward, Amount: -100}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.app.CheckSettlement()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(c.Problems, ";"), "customers moved") {
		t.Fatalf("problems: %v", c.Problems)
	}
}

func TestSettlementRunsAlongsideTheApp(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	checkSettled(t, s.app)
	s.advance("96h")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.app.Step()
		}()
		go func() {
			defer wg.Done()
			if c, err := s.app.CheckSettlement(); err != nil || len(c.Problems) > 0 {
				t.Errorf("settlement: %v %v", err, c.Problems)
			}
		}()
	}
	wg.Wait()
	checkSettled(t, s.app)
}
