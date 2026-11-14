package bank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type Counterparty = ledger.Counterparty

var ErrUnknownLine = errors.New("bank: unknown statement line")

type FeedLine struct {
	Key          string
	NUBAN        string
	Amount       int64
	BookedAt     time.Time
	ValueDate    clock.Date
	Narration    string
	Counterparty *Counterparty
	SessionID    string
	BankRef      string
	Channel      string
	BalanceAfter int64
	Seq          int64
}

const feedSelect = `SELECT e.line_id, e.entry_id, a.nuban, e.amount, e.posted_at, e.value_date, e.narration, coalesce(e.session_id, ''), coalesce(jl.channel, ''), jl.counterparty, e.balance_after, e.seq
	FROM account_events e JOIN gl_accounts a ON a.id = e.account_id JOIN journal_lines jl ON jl.id = e.line_id
	WHERE a.customer AND `

func (b *Bank) lines(ctx context.Context, what, where string, args ...any) ([]FeedLine, error) {
	rows, err := b.L.Pool().Query(ctx, feedSelect+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	defer rows.Close()
	out := []FeedLine{}
	for rows.Next() {
		var l FeedLine
		var line, entry int64
		var vd time.Time
		var cp []byte
		if err := rows.Scan(&line, &entry, &l.NUBAN, &l.Amount, &l.BookedAt, &vd, &l.Narration, &l.SessionID, &l.Channel, &cp, &l.BalanceAfter, &l.Seq); err != nil {
			return nil, fmt.Errorf("read %s: %w", what, err)
		}
		l.Key = strconv.FormatInt(line, 10)
		l.BankRef = b.reference(entry)
		l.ValueDate = clock.DateOf(time.Date(vd.Year(), vd.Month(), vd.Day(), 12, 0, 0, 0, clock.Lagos))
		if len(cp) > 0 {
			l.Counterparty = &Counterparty{}
			if err := json.Unmarshal(cp, l.Counterparty); err != nil {
				return nil, fmt.Errorf("read counterparty of line %d: %w", line, err)
			}
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	return out, nil
}

func (b *Bank) Feed(ctx context.Context, nuban string, at time.Time, from clock.Date) ([]FeedLine, error) {
	return b.lines(ctx, "feed of "+nuban, `a.code = $1 AND e.posted_at <= $2 AND e.posted_at >= $3 ORDER BY e.seq`, CustomerAccount(nuban), at, from.Start())
}

func (b *Bank) Recent(ctx context.Context, nuban string, limit int, cursor string) ([]FeedLine, string, error) {
	if err := checkLimit(limit); err != nil {
		return nil, "", err
	}
	before := int64(-1)
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("%w: %q", ErrBadCursor, cursor)
		}
		before = n
	}
	out, err := b.lines(ctx, "lines of "+nuban, `a.code = $1 AND ($2 < 0 OR e.seq < $2) ORDER BY e.seq DESC LIMIT $3`, CustomerAccount(nuban), before, limit+1)
	if err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		return out[:limit], strconv.FormatInt(out[limit-1].Seq, 10), nil
	}
	return out, "", nil
}

func (b *Bank) Line(ctx context.Context, nuban, key string) (FeedLine, error) {
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil || id < 1 {
		return FeedLine{}, fmt.Errorf("%w: %q", ErrUnknownLine, key)
	}
	out, err := b.lines(ctx, "line "+key, `a.code = $1 AND e.line_id = $2`, CustomerAccount(nuban), id)
	if err != nil {
		return FeedLine{}, err
	}
	if len(out) == 0 {
		return FeedLine{}, fmt.Errorf("%w: %s on %s", ErrUnknownLine, key, nuban)
	}
	return out[0], nil
}

func (b *Bank) SessionLines(ctx context.Context, sessionID string) ([]FeedLine, error) {
	if sessionID == "" {
		return nil, invalid("session id is empty")
	}
	out, err := b.lines(ctx, "lines of session "+sessionID, `e.session_id = $1 ORDER BY e.seq LIMIT $2`, sessionID, MaxPage+1)
	if err != nil {
		return nil, err
	}
	if len(out) > MaxPage {
		return nil, fmt.Errorf("session %s has more than %d lines", sessionID, MaxPage)
	}
	return out, nil
}

type Totals struct {
	Customers int
	Accounts  int
	Deposits  int64
}

func (b *Bank) Totals(ctx context.Context) (Totals, error) {
	var t Totals
	err := b.L.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM customers WHERE status = $1),
		(SELECT count(*) FROM gl_accounts WHERE customer AND status <> 'closed'),
		(SELECT coalesce(sum(b.ledger), 0)::bigint FROM gl_accounts a JOIN balances b ON b.account_id = a.id WHERE a.customer)`, CustomerActive).Scan(&t.Customers, &t.Accounts, &t.Deposits)
	if err != nil {
		return Totals{}, fmt.Errorf("read totals of %s: %w", b.P.ID, err)
	}
	return t, nil
}

func (b *Bank) reference(entry int64) string {
	return fmt.Sprintf("%s%010d", b.P.NUBANCode, entry)
}

func (b *Bank) BalanceAt(ctx context.Context, nuban string, at time.Time) (int64, error) {
	var bal int64
	err := b.L.Pool().QueryRow(ctx, `SELECT coalesce((SELECT e.balance_after FROM account_events e WHERE e.account_id = a.id AND e.posted_at <= $2 ORDER BY e.seq DESC LIMIT 1), 0)
		FROM gl_accounts a WHERE a.code = $1 AND a.customer`, CustomerAccount(nuban), at).Scan(&bal)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ledger.ErrUnknownAccount
	}
	if err != nil {
		return 0, fmt.Errorf("read balance of %s: %w", nuban, err)
	}
	return bal, nil
}
