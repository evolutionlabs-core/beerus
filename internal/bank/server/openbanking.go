package server

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const dataPingTimeout = 3 * time.Second

type Registrar interface {
	RegistryClient(id string) (*openbanking.RegistryClient, bool)
	RegistryClientByCert(cn string) (*openbanking.RegistryClient, bool)
}

type OpenConfig struct {
	Bank     config.Bank
	Books    func() *store.Books
	Registry Registrar
	Clock    *clock.Clock
}

func NewOpenBanking(c OpenConfig) (*openbanking.Server, error) {
	first := c.Books()
	if first == nil {
		return nil, errors.New("the bank ledger is not open")
	}
	b := c.Bank
	return openbanking.New(openbanking.Config{
		BankID:       b.ID,
		Issuer:       b.APIURL(),
		AuthorizeURL: b.URL() + "/authorize",
		HistoryDays:  b.HistoryDays,
		Store: &openbanking.PGStore{Pool: func() *pgxpool.Pool {
			if cur := c.Books(); cur != nil {
				return cur.Pool()
			}
			return first.Pool()
		}},
		Registry: registryView{c.Registry},
		Data:     bankData{books: c.Books, clock: c.Clock},
		Clock:    c.Clock,
		Terms:    OpenTerms(b),
	}), nil
}

func OpenTerms(b config.Bank) openbanking.Terms {
	return openbanking.Terms{
		BankName:      b.Name,
		ConsentMonths: b.API.ConsentMonths,
		RefreshDays:   b.API.RefreshDays,
		DormantDays:   b.API.DormantDays,
		AssertionLife: time.Duration(b.API.AssertionSeconds) * time.Second,
		AccessLife:    time.Duration(b.API.AccessSeconds) * time.Second,
	}
}

type registryView struct {
	src Registrar
}

func (r registryView) convert(c *openbanking.RegistryClient) (*openbanking.Client, bool) {
	pub, err := c.JWK.Public()
	if err != nil {
		return nil, false
	}
	return &openbanking.Client{ID: c.ID, Name: c.Name, LegalName: c.LegalName, RegistryID: c.RegistryID, CAC: c.CAC, Role: c.Role, CertCN: c.CertCN, Keys: []*ecdsa.PublicKey{pub}, RedirectURIs: slices.Clone(c.RedirectURIs)}, true
}

func (r registryView) Client(id string) (*openbanking.Client, bool) {
	c, ok := r.src.RegistryClient(id)
	if !ok {
		return nil, false
	}
	return r.convert(c)
}

func (r registryView) ClientByCert(cn string) (*openbanking.Client, bool) {
	c, ok := r.src.RegistryClientByCert(cn)
	if !ok {
		return nil, false
	}
	return r.convert(c)
}

type bankData struct {
	books func() *store.Books
	clock *clock.Clock
}

func (d bankData) ledger() (*store.Books, error) {
	if l := d.books(); l != nil {
		return l, nil
	}
	return nil, errors.New("the bank ledger is not open")
}

func (d bankData) find(ctx context.Context, nuban string) (ledger.Account, *store.Books, error) {
	l, err := d.ledger()
	if err != nil {
		return ledger.Account{}, nil, err
	}
	acc, err := l.Account(ctx, nuban)
	if errors.Is(err, ledger.ErrUnknownAccount) {
		return ledger.Account{}, nil, openbanking.ErrNotFound
	}
	if err != nil {
		return ledger.Account{}, nil, err
	}
	return acc, l, nil
}

func accountInfo(acc ledger.Account) openbanking.AccountInfo {
	return openbanking.AccountInfo{ID: acc.NUBAN, Name: acc.Name, Type: acc.Product, Currency: acc.Currency}
}

func (d bankData) Accounts(ctx context.Context, customer string) ([]openbanking.AccountInfo, error) {
	l, err := d.ledger()
	if err != nil {
		return nil, err
	}
	accs, err := l.Bank().CustomerAccounts(ctx, customer)
	if errors.Is(err, bank.ErrUnknownCustomer) {
		return []openbanking.AccountInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]openbanking.AccountInfo, 0, len(accs))
	for _, acc := range accs {
		out = append(out, accountInfo(acc))
	}
	return out, nil
}

func (d bankData) Account(ctx context.Context, nuban string) (openbanking.AccountInfo, error) {
	acc, _, err := d.find(ctx, nuban)
	return accountInfo(acc), err
}

func (d bankData) Balance(ctx context.Context, nuban string) (openbanking.Balance, error) {
	acc, l, err := d.find(ctx, nuban)
	if err != nil {
		return openbanking.Balance{}, err
	}
	bal, err := l.LedgerAt(ctx, nuban, d.clock.Now())
	if err != nil {
		return openbanking.Balance{}, err
	}
	return openbanking.Balance{Ledger: bal, Available: bal, Currency: acc.Currency}, nil
}

func (d bankData) BalanceOn(ctx context.Context, nuban string, day clock.Date) (int64, error) {
	_, l, err := d.find(ctx, nuban)
	if err != nil {
		return 0, err
	}
	return l.EndOfDay(ctx, nuban, day)
}

func (d bankData) Transactions(ctx context.Context, nuban string, from, to clock.Date) ([]openbanking.Txn, error) {
	_, l, err := d.find(ctx, nuban)
	if err != nil {
		return nil, err
	}
	rows, err := l.Fetch(ctx, nuban, d.clock.Now(), from)
	if err != nil {
		return nil, err
	}
	out := []openbanking.Txn{}
	for _, p := range rows {
		if p.ValueDate.Before(from) || p.ValueDate.After(to) {
			continue
		}
		t := openbanking.Txn{ID: p.Key, Amount: p.Amount, BookedAt: p.BookedAt, ValueDate: p.ValueDate, Narration: p.Narration, SessionID: p.SessionID, BankRef: p.BankRef, Channel: p.Channel, BalanceAfter: p.BalanceAfter, Seq: p.Seq}
		if p.Counterparty != nil {
			t.Counterparty = &openbanking.Counterparty{Name: p.Counterparty.Name, AccountNumber: p.Counterparty.AccountNumber, BankCode: p.Counterparty.BankCode, BankName: p.Counterparty.BankName}
		}
		out = append(out, t)
	}
	return out, nil
}

func (d bankData) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, dataPingTimeout)
	defer cancel()
	l, err := d.ledger()
	if err != nil {
		return err
	}
	return l.Ping(ctx)
}
