package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

//go:embed migrations/*.sql
var migrations embed.FS

var (
	ErrUnbalanced        = errors.New("ledger: entry does not balance")
	ErrOverflow          = errors.New("ledger: amount overflow")
	ErrUnknownAccount    = errors.New("ledger: unknown account")
	ErrCurrency          = errors.New("ledger: currency mismatch")
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")
	ErrAccountClosed     = errors.New("ledger: account is closed")
	ErrAccountFrozen     = errors.New("ledger: account is frozen")
	ErrPostNoDebit       = errors.New("ledger: account is post-no-debit")
	ErrDailyLimit        = errors.New("ledger: daily limit exceeded")
	ErrIdempotency       = errors.New("ledger: idempotency key reused with a different entry")
	ErrAlreadyReversed   = errors.New("ledger: entry already reversed")
	ErrInvalid           = errors.New("ledger: invalid entry")
)

type Kind string

const (
	Asset     Kind = "asset"
	Liability Kind = "liability"
	Income    Kind = "income"
	Expense   Kind = "expense"
	Equity    Kind = "equity"
)

func (k Kind) sign() int64 {
	if k == Asset || k == Expense {
		return 1
	}
	return -1
}

type Account struct {
	ID             int64
	Code           string
	Name           string
	Kind           Kind
	Currency       string
	Customer       bool
	NUBAN          string
	Product        string
	Owner          string
	Status         string
	KYCTier        int
	OverdraftLimit int64
}

type Line struct {
	Account   string `json:"account"`
	Amount    int64  `json:"amount"`
	Narration string `json:"narration"`
}

type Entry struct {
	Key            string     `json:"key"`
	Kind           string     `json:"kind"`
	PostingDate    clock.Date `json:"posting_date"`
	ValueDate      clock.Date `json:"value_date"`
	PostedAt       time.Time  `json:"posted_at"`
	Narration      string     `json:"narration"`
	SessionID      string     `json:"session_id,omitempty"`
	Lines          []Line     `json:"lines"`
	AllowOverdraft bool       `json:"allow_overdraft,omitempty"`
	LimitedAmount  int64      `json:"limited_amount,omitempty"`
	reverses       int64
}

type Posted struct {
	EntryID  int64
	Replayed bool
}

type Limits map[int]int64

var DefaultLimits = Limits{1: 5000000, 2: 20000000, 3: 500000000}

type Ledger struct {
	pool   *pgxpool.Pool
	limits Limits
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return migrate(ctx, db)
}

func migrate(ctx context.Context, db *sql.DB) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

func New(pool *pgxpool.Pool, limits Limits) *Ledger {
	if limits == nil {
		limits = DefaultLimits
	}
	return &Ledger{pool: pool, limits: limits}
}

func (l *Ledger) Pool() *pgxpool.Pool {
	return l.pool
}

func (l *Ledger) OpenAccount(ctx context.Context, a Account) (Account, error) {
	if a.Currency == "" {
		a.Currency = "NGN"
	}
	if a.Status == "" {
		a.Status = "active"
	}
	if a.KYCTier == 0 {
		a.KYCTier = 3
	}
	var nuban, owner *string
	if a.NUBAN != "" {
		nuban = &a.NUBAN
	}
	if a.Owner != "" {
		owner = &a.Owner
	}
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return a, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO gl_accounts (code, name, kind, currency, customer, nuban, product, owner, status, kyc_tier, overdraft_limit)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (code) DO NOTHING RETURNING id`,
		a.Code, a.Name, string(a.Kind), a.Currency, a.Customer, nuban, a.Product, owner, a.Status, a.KYCTier, a.OverdraftLimit).Scan(&a.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return l.Account(ctx, a.Code)
	}
	if err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO balances (account_id) VALUES ($1)`, a.ID); err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	var kind string
	var nuban, product, owner *string
	err := row.Scan(&a.ID, &a.Code, &a.Name, &kind, &a.Currency, &a.Customer, &nuban, &product, &owner, &a.Status, &a.KYCTier, &a.OverdraftLimit)
	if owner != nil {
		a.Owner = *owner
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrUnknownAccount
	}
	a.Kind = Kind(kind)
	if nuban != nil {
		a.NUBAN = *nuban
	}
	if product != nil {
		a.Product = *product
	}
	return a, err
}

const accountCols = `id, code, name, kind, currency, customer, nuban, product, owner, status, kyc_tier, overdraft_limit`

func (l *Ledger) Account(ctx context.Context, code string) (Account, error) {
	return scanAccount(l.pool.QueryRow(ctx, `SELECT `+accountCols+` FROM gl_accounts WHERE code = $1`, code))
}

func (l *Ledger) AccountByNUBAN(ctx context.Context, nuban string) (Account, error) {
	return scanAccount(l.pool.QueryRow(ctx, `SELECT `+accountCols+` FROM gl_accounts WHERE nuban = $1`, nuban))
}

func (l *Ledger) SetStatus(ctx context.Context, code, status string) error {
	ct, err := l.pool.Exec(ctx, `UPDATE gl_accounts SET status = $2 WHERE code = $1`, code, status)
	if err == nil && ct.RowsAffected() == 0 {
		return ErrUnknownAccount
	}
	return err
}

func (l *Ledger) Balance(ctx context.Context, code string) (int64, error) {
	var b int64
	err := l.pool.QueryRow(ctx, `SELECT CASE WHEN a.customer THEN coalesce((SELECT ledger FROM balances WHERE account_id = a.id), 0)
		ELSE coalesce((SELECT sum(amount) FROM journal_lines WHERE account_id = a.id), 0) * (CASE WHEN a.kind IN ('asset', 'expense') THEN 1 ELSE -1 END) END
		FROM gl_accounts a WHERE a.code = $1`, code).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUnknownAccount
	}
	return b, err
}

func add(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

func (e Entry) hash() string {
	b, _ := json.Marshal(struct {
		Key            string `json:"key"`
		Kind           string `json:"kind"`
		Narration      string `json:"narration"`
		SessionID      string `json:"session_id"`
		Lines          []Line `json:"lines"`
		AllowOverdraft bool   `json:"allow_overdraft"`
		LimitedAmount  int64  `json:"limited_amount"`
		Reverses       int64  `json:"reverses"`
	}{e.Key, e.Kind, e.Narration, e.SessionID, e.Lines, e.AllowOverdraft, e.LimitedAmount, e.reverses})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (e Entry) validate() error {
	if e.Key == "" || e.Kind == "" || len(e.Lines) < 2 || e.PostingDate.IsZero() || e.PostedAt.IsZero() {
		return ErrInvalid
	}
	var total int64
	var err error
	for _, ln := range e.Lines {
		if ln.Amount == 0 || ln.Account == "" || ln.Amount == math.MinInt64 {
			return ErrInvalid
		}
		if total, err = add(total, ln.Amount); err != nil {
			return err
		}
	}
	if total != 0 {
		return ErrUnbalanced
	}
	return nil
}

func (l *Ledger) Post(ctx context.Context, e Entry) (Posted, error) {
	if e.ValueDate.IsZero() {
		e.ValueDate = e.PostingDate
	}
	if err := e.validate(); err != nil {
		return Posted{}, err
	}
	hash := e.hash()
	for attempt := 0; ; attempt++ {
		p, err := l.post(ctx, e, hash)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001") && attempt < 5 {
			continue
		}
		return p, err
	}
}

func (l *Ledger) existing(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key, hash string) (Posted, error) {
	var id int64
	var h string
	if err := q.QueryRow(ctx, `SELECT id, request_hash FROM journal_entries WHERE idempotency_key = $1`, key).Scan(&id, &h); err != nil {
		return Posted{}, err
	}
	if h != hash {
		return Posted{}, ErrIdempotency
	}
	return Posted{EntryID: id, Replayed: true}, nil
}

type lockedAccount struct {
	Account
	ledger int64
	events int64
}

func (l *Ledger) post(ctx context.Context, e Entry, hash string) (Posted, error) {
	if p, err := l.existing(ctx, l.pool, e.Key, hash); err == nil || errors.Is(err, ErrIdempotency) {
		return p, err
	}
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return Posted{}, err
	}
	defer tx.Rollback(ctx)
	codes := map[string]bool{}
	for _, ln := range e.Lines {
		codes[ln.Account] = true
	}
	list := make([]string, 0, len(codes))
	for c := range codes {
		list = append(list, c)
	}
	rows, err := tx.Query(ctx, `SELECT `+accountCols+` FROM gl_accounts WHERE code = ANY($1) ORDER BY id FOR SHARE`, list)
	if err != nil {
		return Posted{}, err
	}
	accts := map[string]*lockedAccount{}
	ids := []int64{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			rows.Close()
			return Posted{}, err
		}
		accts[a.Code] = &lockedAccount{Account: a}
		if a.Customer {
			ids = append(ids, a.ID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Posted{}, err
	}
	if len(accts) != len(codes) {
		return Posted{}, ErrUnknownAccount
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	byID := map[int64]*lockedAccount{}
	for _, a := range accts {
		byID[a.ID] = a
	}
	brows, err := tx.Query(ctx, `SELECT account_id, ledger, events FROM balances WHERE account_id = ANY($1) ORDER BY account_id FOR UPDATE`, ids)
	if err != nil {
		return Posted{}, err
	}
	for brows.Next() {
		var id, ledger, events int64
		if err := brows.Scan(&id, &ledger, &events); err != nil {
			brows.Close()
			return Posted{}, err
		}
		byID[id].ledger, byID[id].events = ledger, events
	}
	brows.Close()
	if err := brows.Err(); err != nil {
		return Posted{}, err
	}
	if p, err := l.existing(ctx, tx, e.Key, hash); err == nil || errors.Is(err, ErrIdempotency) {
		return p, err
	}
	currency := ""
	delta := map[string]int64{}
	for _, ln := range e.Lines {
		a := accts[ln.Account]
		if currency == "" {
			currency = a.Currency
		}
		if a.Currency != currency {
			return Posted{}, ErrCurrency
		}
		d, err := add(delta[ln.Account], ln.Amount*a.Kind.sign())
		if err != nil {
			return Posted{}, err
		}
		delta[ln.Account] = d
	}
	for code, d := range delta {
		a := accts[code]
		switch a.Status {
		case "closed":
			return Posted{}, ErrAccountClosed
		case "frozen":
			if e.reverses == 0 {
				return Posted{}, ErrAccountFrozen
			}
		case "pnd":
			if a.Customer && d < 0 && e.reverses == 0 && e.Kind != "charge" {
				return Posted{}, ErrPostNoDebit
			}
		}
		if !a.Customer {
			continue
		}
		next, err := add(a.ledger, d)
		if err != nil {
			return Posted{}, err
		}
		if d < 0 && next < -a.OverdraftLimit && !e.AllowOverdraft && e.reverses == 0 {
			return Posted{}, ErrInsufficientFunds
		}
	}
	var limitedAccount *int64
	if e.LimitedAmount > 0 {
		for _, ln := range e.Lines {
			a := accts[ln.Account]
			if a.Customer && delta[ln.Account] < 0 {
				limitedAccount = &a.ID
				break
			}
		}
		if limitedAccount != nil {
			var spent int64
			err := tx.QueryRow(ctx, `SELECT coalesce(sum(je.limited_amount), 0) FROM journal_entries je
				WHERE je.limited_account = $1 AND je.posting_date = $2
				AND NOT EXISTS (SELECT 1 FROM journal_entries r WHERE r.reverses = je.id)`, *limitedAccount, e.PostingDate.Start()).Scan(&spent)
			if err != nil {
				return Posted{}, err
			}
			tier := byID[*limitedAccount].KYCTier
			total, err := add(spent, e.LimitedAmount)
			if err != nil {
				return Posted{}, err
			}
			if limit, ok := l.limits[tier]; ok && total > limit {
				return Posted{}, ErrDailyLimit
			}
		}
	}
	var session *string
	if e.SessionID != "" {
		session = &e.SessionID
	}
	var reverses *int64
	if e.reverses != 0 {
		reverses = &e.reverses
	}
	var entryID int64
	limited := int64(0)
	if limitedAccount != nil {
		limited = e.LimitedAmount
	}
	err = tx.QueryRow(ctx, `INSERT INTO journal_entries (idempotency_key, request_hash, kind, posting_date, value_date, posted_at, narration, session_id, reverses, limited_account, limited_amount)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (idempotency_key) DO NOTHING RETURNING id`,
		e.Key, hash, e.Kind, e.PostingDate.Start(), e.ValueDate.Start(), e.PostedAt, e.Narration, session, reverses, limitedAccount, limited).Scan(&entryID)
	if errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx)
		return l.existing(ctx, l.pool, e.Key, hash)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "journal_entries_one_reversal" {
		return Posted{}, ErrAlreadyReversed
	}
	if err != nil {
		return Posted{}, err
	}
	for i, ln := range e.Lines {
		a := accts[ln.Account]
		var lineID int64
		narr := ln.Narration
		if narr == "" {
			narr = e.Narration
		}
		if err := tx.QueryRow(ctx, `INSERT INTO journal_lines (entry_id, line_no, account_id, amount, currency, narration) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			entryID, i+1, a.ID, ln.Amount, a.Currency, narr).Scan(&lineID); err != nil {
			return Posted{}, err
		}
		if a.Customer {
			if a.ledger, err = add(a.ledger, ln.Amount*a.Kind.sign()); err != nil {
				return Posted{}, err
			}
			a.events++
			if _, err := tx.Exec(ctx, `INSERT INTO account_events (account_id, seq, entry_id, line_id, amount, balance_after, posted_at, value_date, narration, session_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				a.ID, a.events, entryID, lineID, ln.Amount*a.Kind.sign(), a.ledger, e.PostedAt, e.ValueDate.Start(), narr, session); err != nil {
				return Posted{}, err
			}
		}
	}
	for _, a := range accts {
		if !a.Customer {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE balances SET ledger = $2, events = $3 WHERE account_id = $1`, a.ID, a.ledger, a.events); err != nil {
			return Posted{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "journal_entries_one_reversal" {
			return Posted{}, ErrAlreadyReversed
		}
		return Posted{}, err
	}
	return Posted{EntryID: entryID}, nil
}

func (l *Ledger) Reverse(ctx context.Context, entryID int64, key string, at time.Time, narration string) (Posted, error) {
	var prior int64
	var priorKey string
	err := l.pool.QueryRow(ctx, `SELECT id, idempotency_key FROM journal_entries WHERE reverses = $1`, entryID).Scan(&prior, &priorKey)
	if err == nil {
		if priorKey == key {
			return Posted{EntryID: prior, Replayed: true}, nil
		}
		return Posted{}, ErrAlreadyReversed
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Posted{}, err
	}
	var kind, narr string
	var session *string
	var valueDate time.Time
	err = l.pool.QueryRow(ctx, `SELECT kind, narration, session_id, value_date FROM journal_entries WHERE id = $1`, entryID).Scan(&kind, &narr, &session, &valueDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return Posted{}, ErrInvalid
	}
	if err != nil {
		return Posted{}, err
	}
	if kind == "reversal" {
		return Posted{}, ErrInvalid
	}
	rows, err := l.pool.Query(ctx, `SELECT a.code, jl.amount, jl.narration FROM journal_lines jl JOIN gl_accounts a ON a.id = jl.account_id WHERE jl.entry_id = $1 ORDER BY jl.line_no`, entryID)
	if err != nil {
		return Posted{}, err
	}
	lines := []Line{}
	for rows.Next() {
		var ln Line
		if err := rows.Scan(&ln.Account, &ln.Amount, &ln.Narration); err != nil {
			rows.Close()
			return Posted{}, err
		}
		ln.Amount = -ln.Amount
		ln.Narration = "REV/" + ln.Narration
		lines = append(lines, ln)
	}
	rows.Close()
	if narration == "" {
		narration = "REV/" + narr
	}
	e := Entry{Key: key, Kind: "reversal", PostingDate: clock.DateOf(at), ValueDate: clock.DateOf(valueDate), PostedAt: at, Narration: narration, reverses: entryID, Lines: lines}
	if session != nil {
		e.SessionID = *session
	}
	return l.Post(ctx, e)
}

type Event struct {
	Seq          int64
	EntryID      int64
	Amount       int64
	BalanceAfter int64
	PostedAt     time.Time
	ValueDate    clock.Date
	Narration    string
	SessionID    string
}

func (l *Ledger) Events(ctx context.Context, code string, after int64, limit int) ([]Event, error) {
	rows, err := l.pool.Query(ctx, `SELECT e.seq, e.entry_id, e.amount, e.balance_after, e.posted_at, e.value_date, e.narration, coalesce(e.session_id, '')
		FROM account_events e JOIN gl_accounts a ON a.id = e.account_id WHERE a.code = $1 AND e.seq > $2 ORDER BY e.seq LIMIT $3`, code, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var ev Event
		var vd time.Time
		if err := rows.Scan(&ev.Seq, &ev.EntryID, &ev.Amount, &ev.BalanceAfter, &ev.PostedAt, &vd, &ev.Narration, &ev.SessionID); err != nil {
			return nil, err
		}
		ev.ValueDate = clock.DateOf(time.Date(vd.Year(), vd.Month(), vd.Day(), 12, 0, 0, 0, clock.Lagos))
		out = append(out, ev)
	}
	return out, rows.Err()
}

type Check struct {
	TrialBalance      int64
	BalanceMismatch   []string
	EventMismatch     []string
	UnbalancedEntries []int64
}

func (c Check) OK() bool {
	return c.TrialBalance == 0 && len(c.BalanceMismatch) == 0 && len(c.EventMismatch) == 0 && len(c.UnbalancedEntries) == 0
}

func (c Check) String() string {
	return fmt.Sprintf("trial balance %d, balance mismatches %v, event mismatches %v, unbalanced entries %v", c.TrialBalance, c.BalanceMismatch, c.EventMismatch, c.UnbalancedEntries)
}

func (l *Ledger) Verify(ctx context.Context) (Check, error) {
	var c Check
	if err := l.pool.QueryRow(ctx, `SELECT coalesce(sum(amount), 0) FROM journal_lines`).Scan(&c.TrialBalance); err != nil {
		return c, err
	}
	rows, err := l.pool.Query(ctx, `SELECT a.code FROM gl_accounts a JOIN balances b ON b.account_id = a.id
		LEFT JOIN (SELECT account_id, sum(amount) AS s FROM journal_lines GROUP BY account_id) t ON t.account_id = a.id
		WHERE a.customer AND b.ledger <> coalesce(t.s, 0) * (CASE WHEN a.kind IN ('asset', 'expense') THEN 1 ELSE -1 END)`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return c, err
		}
		c.BalanceMismatch = append(c.BalanceMismatch, code)
	}
	rows.Close()
	rows, err = l.pool.Query(ctx, `SELECT a.code FROM gl_accounts a JOIN balances b ON b.account_id = a.id
		WHERE a.customer AND (
			b.events <> (SELECT count(*) FROM account_events e WHERE e.account_id = a.id)
			OR b.ledger <> coalesce((SELECT e.balance_after FROM account_events e WHERE e.account_id = a.id ORDER BY seq DESC LIMIT 1), 0)
			OR EXISTS (SELECT 1 FROM (SELECT balance_after - amount - coalesce(lag(balance_after) OVER (ORDER BY seq), 0) AS d FROM account_events e WHERE e.account_id = a.id) x WHERE x.d <> 0))`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return c, err
		}
		c.EventMismatch = append(c.EventMismatch, code)
	}
	rows.Close()
	rows, err = l.pool.Query(ctx, `SELECT entry_id FROM journal_lines GROUP BY entry_id HAVING sum(amount) <> 0 OR count(*) < 2`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return c, err
		}
		c.UnbalancedEntries = append(c.UnbalancedEntries, id)
	}
	rows.Close()
	return c, nil
}
