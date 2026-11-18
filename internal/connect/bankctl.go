package connect

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"
	"net/url"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	bankAway      = "%s is not running or cannot be reached. Run beerus get banks to see its state, then try again."
	bankPage      = control.MaxPage
	bankAccounts  = "/v1/accounts"
	bankCustomers = "/v1/customers"
	bankPayouts   = "/v1/closure-payouts"
	bankBookRoot  = "/v1/books/"
)

type bankNotReadyError struct {
	Name string
}

func (e *bankNotReadyError) Error() string {
	return e.Name + " is not ready, so its books cannot be checked. Try again soon."
}

type bankDownError struct {
	ID   string
	Name string
	Err  error
}

func (e *bankDownError) Error() string {
	return fmt.Sprintf("%s is not answering: %v", e.ID, e.Err)
}

func (e *bankDownError) Unwrap() error {
	return e.Err
}

type bankBooks interface {
	customers(ctx context.Context) ([]Customer, error)
	customersByBVN(ctx context.Context, bvn string) ([]Customer, error)
	createCustomer(ctx context.Context, key string, c Customer) (Customer, error)
	accounts(ctx context.Context, customer string, limit int, cursor string) ([]control.AccountJSON, string, error)
	account(ctx context.Context, nuban string) (control.AccountJSON, error)
	openAccount(ctx context.Context, key, customer, product string) (control.AccountJSON, error)
	payouts(ctx context.Context) (map[string]int64, error)
	payout(ctx context.Context, key, nuban string, amount int64) error
	freeze(ctx context.Context, key, nuban string) error
	totals(ctx context.Context) (Totals, error)
	recent(ctx context.Context, nuban string, limit int, cursor string) ([]FeedLine, string, error)
	line(ctx context.Context, nuban, id string) (FeedLine, error)
	sessionLines(ctx context.Context, sessionID string) ([]FeedLine, error)
	check(ctx context.Context) (Check, error)
}

type bankAdmin struct {
	link control.Link
	id   string
	name string
	base string
}

type pageOf[T any] struct {
	Data []T    `json:"data"`
	Next string `json:"next_cursor"`
}

func (a *App) adminOf(id string) (*bankAdmin, error) {
	b := a.bankByID(id)
	if b == nil {
		return nil, errNotFound
	}
	return &bankAdmin{link: a.link, id: b.ID, name: b.Name, base: b.URL()}, nil
}

func (c *bankAdmin) call(ctx context.Context, method, path string, key string, in, out any) error {
	var header http.Header
	if key != "" {
		header = http.Header{"Idempotency-Key": {key}}
	}
	_, err := c.link.Request(ctx, c.base, method, path, header, in, out)
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusServiceUnavailable {
		return &bankDownError{ID: c.id, Name: c.name, Err: err}
	}
	if err == nil || errors.As(err, &peer) || ctx.Err() != nil {
		return err
	}
	if control.Unreachable(err) {
		return &bankDownError{ID: c.id, Name: c.name, Err: err}
	}
	return fmt.Errorf("call %s: %w", c.id, err)
}

func listAll[T any](ctx context.Context, c *bankAdmin, path string, q url.Values) ([]T, error) {
	out := []T{}
	cursor := ""
	for {
		q.Set("limit", strconv.Itoa(bankPage))
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page pageOf[T]
		if err := c.call(ctx, http.MethodGet, path+"?"+q.Encode(), "", nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Data...)
		if page.Next == "" {
			return out, nil
		}
		cursor = page.Next
	}
}

func (c *bankAdmin) state(ctx context.Context) (control.State, error) {
	var out control.State
	err := c.call(ctx, http.MethodGet, "/v1/status", "", nil, &out)
	return out, err
}

func (c *bankAdmin) customers(ctx context.Context) ([]Customer, error) {
	return listAll[Customer](ctx, c, bankCustomers, url.Values{})
}

func (c *bankAdmin) customersByBVN(ctx context.Context, bvn string) ([]Customer, error) {
	return listAll[Customer](ctx, c, bankCustomers, url.Values{"bvn": {bvn}})
}

func (c *bankAdmin) createCustomer(ctx context.Context, key string, cust Customer) (Customer, error) {
	var out Customer
	req := control.CustomerRequest{Name: cust.Name, Phone: cust.Phone, DOB: cust.DOB.String()}
	err := c.call(ctx, http.MethodPost, bankCustomers, key, req, &out)
	return out, err
}

func (c *bankAdmin) accounts(ctx context.Context, customer string, limit int, cursor string) ([]control.AccountJSON, string, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if customer != "" {
		q.Set("customer", customer)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var page pageOf[control.AccountJSON]
	err := c.call(ctx, http.MethodGet, bankAccounts+"?"+q.Encode(), "", nil, &page)
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Code == "invalid_cursor" {
		return nil, "", fmt.Errorf("%w: %q", errBankCursor, cursor)
	}
	return page.Data, page.Next, err
}

func (c *bankAdmin) account(ctx context.Context, nuban string) (control.AccountJSON, error) {
	var out control.AccountJSON
	err := c.call(ctx, http.MethodGet, bankAccounts+"/"+url.PathEscape(nuban), "", nil, &out)
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusNotFound {
		return out, fmt.Errorf("%w: %s", errUnknownAccount, nuban)
	}
	return out, err
}

func (c *bankAdmin) openAccount(ctx context.Context, key, customer, product string) (control.AccountJSON, error) {
	var out control.AccountJSON
	err := c.call(ctx, http.MethodPost, bankAccounts, key, control.AccountRequest{Customer: customer, Type: product}, &out)
	return out, err
}

func (c *bankAdmin) payouts(ctx context.Context) (map[string]int64, error) {
	paid, err := listAll[control.ClosurePaid](ctx, c, bankPayouts, url.Values{})
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, p := range paid {
		out[p.Account] = p.Paid
	}
	return out, nil
}

func (c *bankAdmin) payout(ctx context.Context, key, nuban string, amount int64) error {
	req := control.ClosurePayoutRequest{Account: nuban, Amount: amount}
	return c.call(ctx, http.MethodPost, bankPayouts, key, req, nil)
}

func (c *bankAdmin) freeze(ctx context.Context, key, nuban string) error {
	return c.call(ctx, http.MethodPost, bankAccounts+"/"+url.PathEscape(nuban)+"/freeze", key, nil, nil)
}

func (c *bankAdmin) totals(ctx context.Context) (Totals, error) {
	var out control.Totals
	err := c.call(ctx, http.MethodGet, bankBookRoot+"totals", "", nil, &out)
	return Totals{Customers: out.Customers, Accounts: out.Accounts, Deposits: out.Deposits}, err
}

func (c *bankAdmin) recent(ctx context.Context, nuban string, limit int, cursor string) ([]FeedLine, string, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var page pageOf[control.Entry]
	err := c.call(ctx, http.MethodGet, bankBookRoot+"accounts/"+url.PathEscape(nuban)+"/entries?"+q.Encode(), "", nil, &page)
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Code == "invalid_cursor" {
		return nil, "", fmt.Errorf("%w: %q", errBankCursor, cursor)
	}
	if err != nil {
		return nil, "", err
	}
	lines, err := feedLines(page.Data)
	return lines, page.Next, err
}

func (c *bankAdmin) line(ctx context.Context, nuban, id string) (FeedLine, error) {
	var out control.Entry
	err := c.call(ctx, http.MethodGet, bankBookRoot+"accounts/"+url.PathEscape(nuban)+"/entries/"+url.PathEscape(id), "", nil, &out)
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusNotFound {
		return FeedLine{}, fmt.Errorf("%w: %s", errUnknownLine, id)
	}
	if err != nil {
		return FeedLine{}, err
	}
	lines, err := feedLines([]control.Entry{out})
	if err != nil {
		return FeedLine{}, err
	}
	return lines[0], nil
}

func (c *bankAdmin) sessionLines(ctx context.Context, sessionID string) ([]FeedLine, error) {
	var out struct {
		Data []control.Entry `json:"data"`
	}
	if err := c.call(ctx, http.MethodGet, bankBookRoot+"sessions/"+url.PathEscape(sessionID), "", nil, &out); err != nil {
		return nil, err
	}
	return feedLines(out.Data)
}

func (c *bankAdmin) check(ctx context.Context) (Check, error) {
	var out control.LedgerCheck
	if err := c.call(ctx, http.MethodGet, "/v1/ledger/check", "", nil, &out); err != nil {
		return Check{}, err
	}
	return Check{TrialBalance: out.TrialBalance, BalanceMismatch: out.BalanceMismatches, EventMismatch: out.EventMismatches, UnbalancedEntries: out.UnbalancedEntries}, nil
}

func feedLines(entries []control.Entry) ([]FeedLine, error) {
	out := make([]FeedLine, 0, len(entries))
	for _, e := range entries {
		value, err := clock.ParseDate(e.ValueDate)
		if err != nil {
			return nil, fmt.Errorf("read entry %s: %w", e.ID, err)
		}
		out = append(out, FeedLine{Key: e.ID, NUBAN: e.Account, Amount: e.Amount, BookedAt: e.PostedAt, ValueDate: value, Narration: e.Narration, Counterparty: e.Counterparty, SessionID: e.SessionID, BankRef: e.Reference, Channel: e.Channel, BalanceAfter: e.BalanceAfter, Seq: e.Seq})
	}
	return out, nil
}
