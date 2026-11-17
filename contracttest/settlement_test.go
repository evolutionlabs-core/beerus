package contracttest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
)

func checkSettled(t *testing.T, app *connect.App) connect.SettlementCheck {
	t.Helper()
	c, err := app.CheckSettlement(t.Context())
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
	if c, err := s.app.VerifyBank(context.Background()); err != nil || !c.OK() {
		t.Fatalf("bank ledgers: %v %v", err, c)
	}
}

func TestNIBSSPostsACutoffOnceAndTheCBNReplaysARetry(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.pay("bank-a", s.persona("adaeze").Accounts[0], "bank-b", s.persona("ngozi").Accounts[0], "1500")
	s.advance("72h")
	first := checkSettled(t, s.app)
	if first.Sessions == 0 {
		t.Fatalf("no cutoff was posted: %+v", first)
	}
	cfg, err := pgxpool.ParseConfig(s.cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = config.DatabaseName(s.cfg.Dir, "nibss")
	db, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tag, err := db.Exec(t.Context(), `UPDATE settlement_sessions SET posted_at = NULL`)
	if err != nil || int(tag.RowsAffected()) != first.Sessions {
		t.Fatalf("made %d of %d cutoffs unposted: %v", tag.RowsAffected(), first.Sessions, err)
	}
	if again := checkSettled(t, s.app); again.Sessions != first.Sessions {
		t.Fatalf("a retry posted %d cutoffs, want the same %d", again.Sessions, first.Sessions)
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
	if _, err := connect.New(t.Context(), cfg, connect.Options{}); err == nil || !strings.Contains(err.Error(), "nip_sessions must stay 4") {
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
	at := clock.New(nil).Now()
	_, err = ledger.New(st.Pool, nil).Post(context.Background(), ledger.Entry{
		Key: "tamper", Kind: "feed", PostingDate: clock.DateOf(at), PostedAt: at, Narration: "Tamper",
		Lines: []ledger.Line{{Account: bank.Vault, Amount: 100}, {Account: bank.NIPInward, Amount: -100}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.app.CheckSettlement(t.Context())
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
			if c, err := s.app.CheckSettlement(t.Context()); err != nil || len(c.Problems) > 0 {
				t.Errorf("settlement: %v %v", err, c.Problems)
			}
		}()
	}
	wg.Wait()
	checkSettled(t, s.app)
}
