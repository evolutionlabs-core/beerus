package bank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const TransferClearing = "TRANSFER_CLEARING"

type Counterparty struct {
	Name          string `json:"name"`
	AccountNumber string `json:"account_number"`
	BankCode      string `json:"bank_code"`
	BankName      string `json:"bank_name"`
}

type FeedLine struct {
	Key          string
	NUBAN        string
	Pending      bool
	HadPending   bool
	Amount       int64
	At           time.Time
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

func (b *Bank) EnsureCustomer(ctx context.Context, nuban, name, product, owner string, opening int64, at time.Time) error {
	if _, err := b.OpenCustomer(ctx, nuban, name, product, owner, 3); err != nil {
		return err
	}
	if opening <= 0 {
		return nil
	}
	_, err := b.L.Post(ctx, entry("opening:"+nuban, "opening", at, "OPENING BALANCE",
		dr(Vault, opening, "Opening balance"),
		cr(Customer(nuban), opening, "OPENING BALANCE")))
	return err
}

func counterpart(l FeedLine) string {
	n := strings.ToUpper(l.Narration)
	n = strings.TrimPrefix(n, "REV/")
	n = strings.TrimPrefix(n, "REVERSAL: ")
	switch {
	case strings.Contains(n, "VAT"):
		return VATPayable
	case strings.Contains(n, "STAMP DUTY"), strings.HasPrefix(n, "EMTL"), strings.Contains(n, "ELECTRONIC MONEY TRANSFER LEVY"):
		return StampPayable
	case strings.Contains(n, "NIP CHARGE"), strings.Contains(n, "TRANSFER FEE"), strings.Contains(n, "SMS"), strings.Contains(n, "MAINT"):
		return FeeIncome
	case l.Channel == "internal", strings.HasPrefix(n, "TRF TO"), strings.HasPrefix(n, "TRF FRM"), strings.Contains(n, "SAVINGS"), strings.Contains(n, "CURRENT ACCOUNT"):
		return TransferClearing
	}
	original := l.Amount
	if l.Channel == "reversal" {
		original = -l.Amount
	}
	if original < 0 {
		return NIPOutward
	}
	return NIPInward
}

func kindOf(l FeedLine) string {
	switch l.Channel {
	case "nip", "reversal", "charge", "internal":
		return l.Channel
	}
	return "feed"
}

func (b *Bank) nextSeq(ctx context.Context, tx pgx.Tx, accountID int64) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO feed_heads (account_id) VALUES ($1) ON CONFLICT DO NOTHING`, accountID); err != nil {
		return 0, err
	}
	var seq int64
	err := tx.QueryRow(ctx, `UPDATE feed_heads SET seq = seq + 1 WHERE account_id = $1 RETURNING seq`, accountID).Scan(&seq)
	return seq, err
}

func (b *Bank) feedExists(ctx context.Context, accountID int64, key string, pending bool) (bool, error) {
	var ok bool
	err := b.L.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM account_feed WHERE account_id = $1 AND line_key = $2 AND pending = $3)`, accountID, key, pending).Scan(&ok)
	return ok, err
}

func (b *Bank) insertFeed(ctx context.Context, accountID int64, l FeedLine, entryID *int64, balance *int64) error {
	tx, err := b.L.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM account_feed WHERE account_id = $1 AND line_key = $2 AND pending = $3)`, accountID, l.Key, l.Pending).Scan(&exists); err != nil || exists {
		return err
	}
	seq, err := b.nextSeq(ctx, tx, accountID)
	if err != nil {
		return err
	}
	var cp []byte
	if l.Counterparty != nil {
		cp, _ = json.Marshal(l.Counterparty)
	}
	var booked *time.Time
	if !l.Pending {
		t := l.BookedAt
		booked = &t
	}
	null := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO account_feed (account_id, seq, line_key, pending, amount, appeared_at, booked_at, value_date, narration, counterparty, session_id, bank_ref, channel, balance_after, entry_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		accountID, seq, l.Key, l.Pending, l.Amount, l.At, booked, l.ValueDate.Start(), l.Narration, cp, null(l.SessionID), null(l.BankRef), l.Channel, balance, entryID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (b *Bank) RecordLine(ctx context.Context, l FeedLine) error {
	acct, err := b.L.Account(ctx, Customer(l.NUBAN))
	if err != nil {
		return err
	}
	if done, err := b.feedExists(ctx, acct.ID, l.Key, l.Pending); err != nil || done {
		return err
	}
	if l.Pending {
		return b.insertFeed(ctx, acct.ID, l, nil, nil)
	}
	if l.Amount == 0 {
		return fmt.Errorf("bank: line %s has no amount", l.Key)
	}
	amt := l.Amount
	if amt < 0 {
		amt = -amt
	}
	var lines []ledger.Line
	if l.Amount > 0 {
		lines = []ledger.Line{dr(counterpart(l), amt, l.Narration), cr(Customer(l.NUBAN), amt, l.Narration)}
	} else {
		lines = []ledger.Line{dr(Customer(l.NUBAN), amt, l.Narration), cr(counterpart(l), amt, l.Narration)}
	}
	e := ledger.Entry{Key: "line:" + l.Key, Kind: kindOf(l), PostingDate: clock.DateOf(l.BookedAt), ValueDate: l.ValueDate, PostedAt: l.BookedAt, Narration: l.Narration, SessionID: l.SessionID, Lines: lines, AllowOverdraft: true}
	p, err := b.L.Post(ctx, e)
	if err != nil {
		return err
	}
	var bal int64
	if err := b.L.Pool().QueryRow(ctx, `SELECT balance_after FROM account_events WHERE account_id = $1 AND entry_id = $2 ORDER BY seq DESC LIMIT 1`, acct.ID, p.EntryID).Scan(&bal); err != nil {
		return err
	}
	return b.insertFeed(ctx, acct.ID, l, &p.EntryID, &bal)
}

func (b *Bank) Watermark(ctx context.Context, nuban string) (time.Time, error) {
	var wm *time.Time
	err := b.L.Pool().QueryRow(ctx, `SELECT h.watermark FROM feed_heads h JOIN gl_accounts a ON a.id = h.account_id WHERE a.code = $1`, Customer(nuban)).Scan(&wm)
	if errors.Is(err, pgx.ErrNoRows) || wm == nil {
		return time.Time{}, nil
	}
	return *wm, err
}

func (b *Bank) SetWatermark(ctx context.Context, nuban string, at time.Time) error {
	acct, err := b.L.Account(ctx, Customer(nuban))
	if err != nil {
		return err
	}
	_, err = b.L.Pool().Exec(ctx, `INSERT INTO feed_heads (account_id, watermark) VALUES ($1, $2) ON CONFLICT (account_id) DO UPDATE SET watermark = GREATEST(feed_heads.watermark, EXCLUDED.watermark)`, acct.ID, at)
	return err
}

func (b *Bank) Feed(ctx context.Context, nuban string, at time.Time, from clock.Date) ([]FeedLine, error) {
	rows, err := b.L.Pool().Query(ctx, `SELECT f.line_key, f.pending, f.amount, f.appeared_at, f.booked_at, f.value_date, f.narration, f.counterparty, coalesce(f.session_id, ''), coalesce(f.bank_ref, ''), f.channel, coalesce(f.balance_after, 0), f.seq,
			EXISTS (SELECT 1 FROM account_feed p WHERE p.account_id = f.account_id AND p.line_key = f.line_key AND p.pending)
		FROM account_feed f JOIN gl_accounts a ON a.id = f.account_id
		WHERE a.code = $1 AND (
			(NOT f.pending AND f.booked_at <= $2 AND f.booked_at >= $3)
			OR (f.pending AND f.appeared_at <= $2 AND f.appeared_at >= $3 AND NOT EXISTS (
				SELECT 1 FROM account_feed q WHERE q.account_id = f.account_id AND q.line_key = f.line_key AND NOT q.pending AND q.booked_at <= $2)))
		ORDER BY f.seq`, Customer(nuban), at, from.Start())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeedLine{}
	for rows.Next() {
		var l FeedLine
		var booked *time.Time
		var vd time.Time
		var cp []byte
		if err := rows.Scan(&l.Key, &l.Pending, &l.Amount, &l.At, &booked, &vd, &l.Narration, &cp, &l.SessionID, &l.BankRef, &l.Channel, &l.BalanceAfter, &l.Seq, &l.HadPending); err != nil {
			return nil, err
		}
		if booked != nil {
			l.BookedAt = *booked
		}
		l.ValueDate = clock.DateOf(time.Date(vd.Year(), vd.Month(), vd.Day(), 12, 0, 0, 0, clock.Lagos))
		if len(cp) > 0 {
			l.Counterparty = &Counterparty{}
			_ = json.Unmarshal(cp, l.Counterparty)
		}
		if l.Pending {
			l.HadPending = false
		}
		l.NUBAN = nuban
		out = append(out, l)
	}
	return out, rows.Err()
}

func (b *Bank) BalanceAt(ctx context.Context, nuban string, at time.Time) (int64, error) {
	var bal *int64
	err := b.L.Pool().QueryRow(ctx, `SELECT f.balance_after FROM account_feed f JOIN gl_accounts a ON a.id = f.account_id
		WHERE a.code = $1 AND NOT f.pending AND f.booked_at <= $2 ORDER BY f.seq DESC LIMIT 1`, Customer(nuban), at).Scan(&bal)
	if err == nil && bal != nil {
		return *bal, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var opening int64
	err = b.L.Pool().QueryRow(ctx, `SELECT coalesce(sum(-jl.amount), 0) FROM journal_lines jl JOIN journal_entries je ON je.id = jl.entry_id JOIN gl_accounts a ON a.id = jl.account_id
		WHERE a.code = $1 AND je.kind = 'opening'`, Customer(nuban)).Scan(&opening)
	return opening, err
}

func (b *Bank) PendingAt(ctx context.Context, nuban string, at time.Time) (int64, error) {
	var sum int64
	err := b.L.Pool().QueryRow(ctx, `SELECT coalesce(sum(f.amount), 0) FROM account_feed f JOIN gl_accounts a ON a.id = f.account_id
		WHERE a.code = $1 AND f.pending AND f.appeared_at <= $2 AND NOT EXISTS (
			SELECT 1 FROM account_feed q WHERE q.account_id = f.account_id AND q.line_key = f.line_key AND NOT q.pending AND q.booked_at <= $2)`, Customer(nuban), at).Scan(&sum)
	return sum, err
}
