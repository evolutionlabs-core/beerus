package connect

import (
	"context"
	"errors"
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

type localBooks struct {
	b   *bank.Bank
	clk *clock.Clock
}

func (a *App) booksOf(id string) bankBooks {
	if a.role == RoleCore {
		if !a.booksReady(id) {
			return nil
		}
		c, err := a.adminOf(id)
		if err != nil {
			return nil
		}
		return c
	}
	a.mu.Lock()
	ls := a.ledgerFor(id)
	a.mu.Unlock()
	if ls == nil {
		return nil
	}
	return localBooks{b: ls.Bank(), clk: a.clock}
}

func (a *App) booksReady(id string) bool {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	st := a.status[id]
	return st != nil && st.Phase != PhaseClosed && st.Steps[stepIndex(config.StepOpenBankingSchema)].Done
}

func (l localBooks) customers(ctx context.Context) ([]bank.Customer, error) {
	out := []bank.Customer{}
	cursor := ""
	for {
		page, next, err := l.b.Customers(ctx, bank.MaxPage, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

func (l localBooks) customersByBVN(ctx context.Context, bvn string) ([]bank.Customer, error) {
	c, err := l.b.CustomerByBVN(ctx, bvn)
	if errors.Is(err, bank.ErrUnknownCustomer) {
		return []bank.Customer{}, nil
	}
	if err != nil {
		return nil, err
	}
	return []bank.Customer{c}, nil
}

func (l localBooks) createCustomer(ctx context.Context, key string, c bank.Customer) (bank.Customer, error) {
	return l.b.CreateCustomer(ctx, key, c.Name, c.Phone, c.DOB, c.BVN, "", "", l.clk.Now())
}

func (l localBooks) accountView(ctx context.Context, acc ledger.Account) (server.AccountJSON, error) {
	bal, err := l.b.L.Balance(ctx, acc.Code)
	if err != nil {
		return server.AccountJSON{}, fmt.Errorf("read balance of %s: %w", acc.NUBAN, err)
	}
	return server.AccountJSON{NUBAN: acc.NUBAN, Name: acc.Name, Customer: acc.Owner, Type: acc.Product, Status: acc.Status, Currency: acc.Currency, Balance: bal}, nil
}

func (l localBooks) accounts(ctx context.Context, customer string, limit int, cursor string) ([]server.AccountJSON, string, error) {
	page, next, err := l.b.Accounts(ctx, customer, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([]server.AccountJSON, 0, len(page))
	for _, acc := range page {
		v, err := l.accountView(ctx, acc)
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	return out, next, nil
}

func (l localBooks) account(ctx context.Context, nuban string) (server.AccountJSON, error) {
	acc, err := l.b.L.Account(ctx, bank.CustomerAccount(nuban))
	if err != nil {
		return server.AccountJSON{}, err
	}
	if !acc.Customer {
		return server.AccountJSON{}, ledger.ErrUnknownAccount
	}
	return l.accountView(ctx, acc)
}

func (l localBooks) openAccount(ctx context.Context, key, customer, product string) (server.AccountJSON, error) {
	acc, err := l.b.OpenAccount(ctx, key, customer, product, l.clk.Now())
	if err != nil {
		return server.AccountJSON{}, err
	}
	return l.accountView(ctx, acc)
}

func (l localBooks) payouts(ctx context.Context) (map[string]int64, error) {
	return l.b.ClosurePayouts(ctx)
}

func (l localBooks) payout(ctx context.Context, key, nuban string, amount int64) error {
	_, err := l.b.ClosurePayout(ctx, key, nuban, amount, l.clk.Now())
	return err
}

func (l localBooks) freeze(ctx context.Context, _, nuban string) error {
	return l.b.FreezeAccount(ctx, nuban)
}

func (l localBooks) totals(ctx context.Context) (bank.Totals, error) {
	return l.b.Totals(ctx)
}

func (l localBooks) recent(ctx context.Context, nuban string, limit int, cursor string) ([]bank.FeedLine, string, error) {
	return l.b.Recent(ctx, nuban, limit, cursor)
}

func (l localBooks) line(ctx context.Context, nuban, id string) (bank.FeedLine, error) {
	return l.b.Line(ctx, nuban, id)
}

func (l localBooks) sessionLines(ctx context.Context, sessionID string) ([]bank.FeedLine, error) {
	return l.b.SessionLines(ctx, sessionID)
}

func (l localBooks) login(ctx context.Context, username, password string) (string, error) {
	c, err := l.b.Authenticate(ctx, username, password)
	return c.ID, err
}

func (l localBooks) check(ctx context.Context) (ledger.Check, error) {
	return l.b.L.Verify(ctx)
}
