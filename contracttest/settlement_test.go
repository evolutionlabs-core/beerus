package contracttest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
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
	if first.Sessions != 0 {
		t.Fatalf("a new setup has %d settlement sessions", first.Sessions)
	}
	s.pay("bank-a", s.persona("adaeze").Accounts[0], "bank-b", s.persona("ngozi").Accounts[0], "1500")
	s.advance("72h")
	later := checkSettled(t, s.app)
	if later.Sessions == 0 {
		t.Fatalf("settlement did not move on: %+v then %+v", first, later)
	}
	if c, err := s.app.VerifyBank(); err != nil || !c.OK() {
		t.Fatalf("bank ledgers: %v %v", err, c)
	}
}

func TestNIPSessionsCannotChangeOnALiveSetup(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	checkSettled(t, s.app)
	cfg := s.loadConfig()
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
	at := s.app.Clock().Now()
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
	if !strings.Contains(strings.Join(c.Problems, ";"), "but its open NIBSS sessions come to") {
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
			s.app.Step(t.Context())
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
