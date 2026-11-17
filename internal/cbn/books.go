package cbn

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	issuance         = "ISSUANCE"
	currencyInCirc   = "CURRENCY_IN_CIRCULATION"
	settlementPrefix = "SETTLEMENT/"
	sessionKind      = "session"
	genesisKind      = "genesis"
	sessionPrefix    = "session:"
	genesisPrefix    = "genesis:"
	MaxPage          = 500
	DefaultPage      = 50
)

var (
	ErrNoAccount = errors.New("no settlement account at the CBN")
	ErrCursor    = errors.New("cursor is not valid")
)

type InvalidError struct {
	Msg string
}

func (e *InvalidError) Error() string {
	return e.Msg
}

type Net struct {
	BankID string `json:"bank_id"`
	Net    int64  `json:"net_minor"`
}

type Posting struct {
	Key       string    `json:"key"`
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id,omitempty"`
	Narration string    `json:"narration"`
	PostedAt  time.Time `json:"posted_at"`
	Nets      []Net     `json:"nets"`
}

type Account struct {
	BankID     string    `json:"bank_id"`
	Settlement int64     `json:"settlement_minor"`
	Vault      int64     `json:"vault_minor"`
	OpenedAt   time.Time `json:"opened_at"`
}

type ConflictError struct {
	Stored Account
	Asked  Account
}

func (e *ConflictError) Error() string {
	var diff []string
	if e.Stored.Settlement != e.Asked.Settlement {
		diff = append(diff, fmt.Sprintf("settlement_minor %d", e.Asked.Settlement))
	}
	if e.Stored.Vault != e.Asked.Vault {
		diff = append(diff, fmt.Sprintf("vault_minor %d", e.Asked.Vault))
	}
	return fmt.Sprintf("%s opened with settlement_minor %d and vault_minor %d, and this request has %s. Send the amounts it opened with.", e.Stored.BankID, e.Stored.Settlement, e.Stored.Vault, strings.Join(diff, " and "))
}

type ReusedError struct {
	ID    string
	First []Net
	Now   []Net
}

func (e *ReusedError) Error() string {
	return fmt.Sprintf("Session %s was posted with %s, and this request has %s. Send a new session_id, or repeat the first nets.", e.ID, listNets(e.First), listNets(e.Now))
}

func listNets(nets []Net) string {
	parts := make([]string, 0, len(nets))
	for _, n := range nets {
		parts = append(parts, fmt.Sprintf("%s %d", n.BankID, n.Net))
	}
	switch len(parts) {
	case 0:
		return "no nets"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

type UnbalancedError struct {
	Sum int64
}

func (e *UnbalancedError) Error() string {
	return fmt.Sprintf("The session nets to %d, not zero. Send nets that sum to zero.", e.Sum)
}

type TooLargeError struct {
	ID      string
	BankID  string
	Balance int64
	Net     int64
}

func (e *TooLargeError) Error() string {
	if e.BankID != "" {
		return fmt.Sprintf("Session %s would take %s's settlement balance past the largest amount: %d + %d. Send smaller nets.", e.ID, e.BankID, e.Balance, e.Net)
	}
	return fmt.Sprintf("The nets of session %s are too large to add up. The largest net is %d kobo. Send smaller nets.", e.ID, int64(math.MaxInt64))
}

type DuplicateError struct {
	ID     string
	BankID string
	Times  int
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("%s appears %d times in session %s. Send one net per bank.", e.BankID, e.Times, e.ID)
}

type UnknownBankError struct {
	BankID string
}

func (e *UnknownBankError) Error() string {
	return fmt.Sprintf("%s has no settlement account at the CBN. Open it first.", e.BankID)
}

type Books struct {
	l     *ledger.Ledger
	clock *clock.Clock
}

func settlementAccount(bank string) string {
	return settlementPrefix + bank
}

func New(ctx context.Context, pool *pgxpool.Pool, clk *clock.Clock) (*Books, error) {
	b := &Books{l: ledger.New(pool, nil), clock: clk}
	for _, acc := range []ledger.Account{
		{Code: issuance, Name: "Issuance", Kind: ledger.Asset},
		{Code: currencyInCirc, Name: "Currency in circulation", Kind: ledger.Liability},
	} {
		if _, err := b.l.OpenAccount(ctx, acc); err != nil {
			return nil, fmt.Errorf("open account %s: %w", acc.Code, err)
		}
	}
	return b, nil
}

func (b *Books) account(ctx context.Context, bank string) (Account, error) {
	out := Account{BankID: bank}
	err := b.l.Pool().QueryRow(ctx, `SELECT e.posted_at,
			coalesce(sum(-jl.amount) FILTER (WHERE a.code = $2), 0)::bigint,
			coalesce(sum(-jl.amount) FILTER (WHERE a.code = $3), 0)::bigint
		FROM journal_entries e JOIN journal_lines jl ON jl.entry_id = e.id JOIN gl_accounts a ON a.id = jl.account_id
		WHERE e.idempotency_key = $1 GROUP BY e.id`, genesisPrefix+bank, settlementAccount(bank), currencyInCirc).Scan(&out.OpenedAt, &out.Settlement, &out.Vault)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, fmt.Errorf("read account of %s: %w", bank, ErrNoAccount)
	}
	if err != nil {
		return Account{}, fmt.Errorf("read account of %s: %w", bank, err)
	}
	return out, nil
}

func (b *Books) OpenAccount(ctx context.Context, bank string, settlement, vault int64) (Account, error) {
	asked := Account{BankID: bank, Settlement: settlement, Vault: vault}
	if bank == "" || settlement < 0 || vault < 0 || settlement > math.MaxInt64-vault || settlement+vault == 0 {
		return Account{}, &InvalidError{Msg: "Send a settlement_minor and a vault_minor of zero or more, with one of them above zero, that fit together."}
	}
	stored, err := b.account(ctx, bank)
	if err == nil {
		if stored.Settlement != settlement || stored.Vault != vault {
			return Account{}, &ConflictError{Stored: stored, Asked: asked}
		}
		return stored, nil
	}
	if !errors.Is(err, ErrNoAccount) {
		return Account{}, err
	}
	acc := ledger.Account{Code: settlementAccount(bank), Name: "Settlement account of " + bank, Kind: ledger.Liability}
	if _, err := b.l.OpenAccount(ctx, acc); err != nil {
		return Account{}, fmt.Errorf("open account %s: %w", acc.Code, err)
	}
	lines := []ledger.Line{{Account: issuance, Amount: settlement + vault, Narration: "Issuance"}}
	if settlement > 0 {
		lines = append(lines, ledger.Line{Account: acc.Code, Amount: -settlement, Narration: "Genesis settlement funding"})
	}
	if vault > 0 {
		lines = append(lines, ledger.Line{Account: currencyInCirc, Amount: -vault, Narration: "Vault cash issued"})
	}
	at := b.clock.Now()
	if _, err := b.l.Post(ctx, ledger.Entry{Key: genesisPrefix + bank, Kind: genesisKind, PostingDate: clock.DateOf(at), PostedAt: at, Narration: "Genesis funding of " + bank, Lines: lines}); err != nil {
		return Account{}, fmt.Errorf("post genesis of %s: %w", bank, err)
	}
	return b.account(ctx, bank)
}

func (b *Books) Balance(ctx context.Context, bank string) (int64, error) {
	bal, err := b.l.Balance(ctx, settlementAccount(bank))
	if errors.Is(err, ledger.ErrUnknownAccount) {
		return 0, fmt.Errorf("read balance of %s: %w", bank, ErrNoAccount)
	}
	if err != nil {
		return 0, fmt.Errorf("read balance of %s: %w", bank, err)
	}
	return bal, nil
}

func (b *Books) Post(ctx context.Context, id string, cutoff time.Time, nets []Net) (Posting, bool, error) {
	if id == "" || cutoff.IsZero() || len(nets) < 2 {
		return Posting{}, false, &InvalidError{Msg: "Send a session_id, a cutoff_at and at least 2 nets."}
	}
	sorted := slices.SortedFunc(slices.Values(nets), func(x, y Net) int { return strings.Compare(x.BankID, y.BankID) })
	stored, found, err := b.stored(ctx, id)
	if err != nil {
		return Posting{}, false, err
	}
	if found {
		if !slices.Equal(nonZero(sorted), stored.Nets) {
			return Posting{}, false, &ReusedError{ID: id, First: stored.Nets, Now: nonZero(sorted)}
		}
		return stored, true, nil
	}
	lines, err := b.lines(ctx, id, sorted)
	if err != nil {
		return Posting{}, false, err
	}
	at := b.clock.Now()
	p := Posting{Key: sessionPrefix + id, Kind: sessionKind, SessionID: id, Narration: "NIP settlement session " + id, PostedAt: at, Nets: nonZero(sorted)}
	if len(lines) == 0 {
		return p, false, nil
	}
	e := ledger.Entry{Key: p.Key, Kind: sessionKind, PostingDate: clock.DateOf(at), PostedAt: at, Narration: p.Narration, Lines: lines, AllowOverdraft: true}
	posted, err := b.l.Post(ctx, e)
	if err != nil {
		return Posting{}, false, fmt.Errorf("post session %s: %w", id, err)
	}
	return p, posted.Replayed, nil
}

func nonZero(nets []Net) []Net {
	out := make([]Net, 0, len(nets))
	for _, n := range nets {
		if n.Net != 0 {
			out = append(out, n)
		}
	}
	return out
}

func (b *Books) lines(ctx context.Context, id string, sorted []Net) ([]ledger.Line, error) {
	for i := 1; i < len(sorted); i++ {
		if sorted[i].BankID != sorted[i-1].BankID {
			continue
		}
		times := 2
		for j := i + 1; j < len(sorted) && sorted[j].BankID == sorted[i].BankID; j++ {
			times++
		}
		return nil, &DuplicateError{ID: id, BankID: sorted[i].BankID, Times: times}
	}
	var total int64
	for _, n := range sorted {
		if (n.Net > 0 && total > math.MaxInt64-n.Net) || (n.Net < 0 && total < math.MinInt64-n.Net) || n.Net == math.MinInt64 {
			return nil, &TooLargeError{ID: id}
		}
		total += n.Net
	}
	if total != 0 {
		return nil, &UnbalancedError{Sum: total}
	}
	lines := []ledger.Line{}
	for _, n := range sorted {
		if _, err := b.l.Account(ctx, settlementAccount(n.BankID)); errors.Is(err, ledger.ErrUnknownAccount) {
			return nil, &UnknownBankError{BankID: n.BankID}
		} else if err != nil {
			return nil, fmt.Errorf("read account of %s: %w", n.BankID, err)
		}
		if n.Net == 0 {
			continue
		}
		bal, err := b.Balance(ctx, n.BankID)
		if err != nil {
			return nil, err
		}
		if (n.Net > 0 && bal > math.MaxInt64-n.Net) || (n.Net < 0 && bal < math.MinInt64-n.Net) {
			return nil, &TooLargeError{ID: id, BankID: n.BankID, Balance: bal, Net: n.Net}
		}
		lines = append(lines, ledger.Line{Account: settlementAccount(n.BankID), Amount: -n.Net, Narration: "NIP session " + id})
	}
	return lines, nil
}

func (b *Books) stored(ctx context.Context, id string) (Posting, bool, error) {
	list, _, err := b.page(ctx, "", sessionPrefix+id, 1, true)
	if err != nil {
		return Posting{}, false, err
	}
	if len(list) == 0 {
		return Posting{}, false, nil
	}
	return list[0], true, nil
}

func (b *Books) Postings(ctx context.Context, bank string, limit int, cursor string) ([]Posting, string, error) {
	return b.page(ctx, bank, cursor, limit, false)
}

func (b *Books) page(ctx context.Context, bank, key string, limit int, exact bool) ([]Posting, string, error) {
	where, args := `e.kind IN ('session', 'genesis')`, []any{}
	if exact {
		args = append(args, key)
		where += ` AND e.idempotency_key = $1`
	} else if key != "" {
		var at time.Time
		var id int64
		err := b.l.Pool().QueryRow(ctx, `SELECT posted_at, id FROM journal_entries WHERE idempotency_key = $1 AND kind IN ('session', 'genesis')`, key).Scan(&at, &id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrCursor
		}
		if err != nil {
			return nil, "", fmt.Errorf("read cursor: %w", err)
		}
		args = append(args, at, id)
		where += ` AND (e.posted_at, e.id) < ($1, $2)`
	}
	if bank != "" {
		args = append(args, settlementAccount(bank))
		where += fmt.Sprintf(` AND EXISTS (SELECT 1 FROM journal_lines x JOIN gl_accounts xa ON xa.id = x.account_id WHERE x.entry_id = e.id AND xa.code = $%d)`, len(args))
	}
	args = append(args, limit+1)
	rows, err := b.l.Pool().Query(ctx, `SELECT e.id, e.idempotency_key, e.kind, e.narration, e.posted_at
		FROM journal_entries e WHERE `+where+fmt.Sprintf(` ORDER BY e.posted_at DESC, e.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, "", fmt.Errorf("list postings: %w", err)
	}
	type head struct {
		id int64
		p  Posting
	}
	heads := []head{}
	for rows.Next() {
		var h head
		if err := rows.Scan(&h.id, &h.p.Key, &h.p.Kind, &h.p.Narration, &h.p.PostedAt); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("read posting: %w", err)
		}
		heads = append(heads, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list postings: %w", err)
	}
	next := ""
	if len(heads) > limit {
		heads = heads[:limit]
		next = heads[limit-1].p.Key
	}
	out := make([]Posting, 0, len(heads))
	for _, h := range heads {
		if h.p.Nets, err = b.nets(ctx, h.id); err != nil {
			return nil, "", err
		}
		if id, ok := strings.CutPrefix(h.p.Key, sessionPrefix); ok {
			h.p.SessionID = id
		}
		out = append(out, h.p)
	}
	return out, next, nil
}

func (b *Books) nets(ctx context.Context, entry int64) ([]Net, error) {
	rows, err := b.l.Pool().Query(ctx, `SELECT substr(a.code, length($2::text) + 1), -jl.amount
		FROM journal_lines jl JOIN gl_accounts a ON a.id = jl.account_id
		WHERE jl.entry_id = $1 AND a.code LIKE $2::text || '%'`, entry, settlementPrefix)
	if err != nil {
		return nil, fmt.Errorf("read nets: %w", err)
	}
	defer rows.Close()
	out := []Net{}
	for rows.Next() {
		var n Net
		if err := rows.Scan(&n.BankID, &n.Net); err != nil {
			return nil, fmt.Errorf("read net: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read nets: %w", err)
	}
	slices.SortFunc(out, func(x, y Net) int { return strings.Compare(x.BankID, y.BankID) })
	return out, nil
}

func (b *Books) Problems(ctx context.Context) ([]string, error) {
	c, err := b.l.Verify(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify CBN books: %w", err)
	}
	if c.TrialBalance != 0 || len(c.UnbalancedEntries) > 0 {
		return []string{fmt.Sprintf("the CBN's books do not balance: %d", c.TrialBalance)}, nil
	}
	return []string{}, nil
}
