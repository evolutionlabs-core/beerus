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
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	Settlement   = "SETTLEMENT_CBN"
	Vault        = "VAULT_CASH"
	Capital      = "CAPITAL"
	NIPOutward   = "NIP_OUTWARD_PAYABLE"
	NIPInward    = "NIP_INWARD_RECEIVABLE"
	FeeIncome    = "FEE_INCOME"
	VATPayable   = "VAT_PAYABLE"
	StampPayable = "STAMP_DUTY_PAYABLE"
	Suspense     = "SUSPENSE"
)

type Profile struct {
	ID        string
	Name      string
	NIPCode   string
	NUBANCode string
}

type Bank struct {
	L    *ledger.Ledger
	P    Profile
	fees *charges.Schedule
}

func New(l *ledger.Ledger, p Profile, fees *charges.Schedule) *Bank {
	return &Bank{L: l, P: p, fees: fees}
}

func CustomerAccount(nuban string) string {
	return "CUST:" + nuban
}

func (b *Bank) Setup(ctx context.Context) error {
	for _, a := range []ledger.Account{
		{Code: Settlement, Name: "Settlement account at the CBN", Kind: ledger.Asset},
		{Code: Vault, Name: "Vault cash", Kind: ledger.Asset},
		{Code: Capital, Name: "Share capital", Kind: ledger.Equity},
		{Code: NIPOutward, Name: "NIP outward payable", Kind: ledger.Liability},
		{Code: NIPInward, Name: "NIP inward receivable", Kind: ledger.Asset},
		{Code: FeeIncome, Name: "Fee income", Kind: ledger.Income},
		{Code: VATPayable, Name: "VAT payable", Kind: ledger.Liability},
		{Code: StampPayable, Name: "Stamp duty payable", Kind: ledger.Liability},
		{Code: Suspense, Name: "Settlement suspense", Kind: ledger.Asset},
	} {
		if _, err := b.L.OpenAccount(ctx, a); err != nil {
			return err
		}
	}
	return nil
}

func entry(key, kind string, at time.Time, narration string, lines ...ledger.Line) ledger.Entry {
	d := clock.DateOf(at)
	return ledger.Entry{Key: key, Kind: kind, PostingDate: d, ValueDate: d, PostedAt: at, Narration: narration, Lines: lines}
}

func dr(account string, amount int64, narration string) ledger.Line {
	return ledger.Line{Account: account, Amount: amount, Narration: narration}
}

func cr(account string, amount int64, narration string) ledger.Line {
	return ledger.Line{Account: account, Amount: -amount, Narration: narration}
}

func via(l ledger.Line, channel string, cp *Counterparty) ledger.Line {
	l.Channel, l.Counterparty = channel, cp
	return l
}

const (
	GenesisSettlement int64 = 1_000_000_000_000
	GenesisVault      int64 = 200_000_000_000
)

func (b *Bank) Genesis(ctx context.Context, settlement, vault int64, at time.Time) error {
	_, err := b.L.Post(ctx, entry("genesis", "genesis", at, "Genesis capital",
		dr(Settlement, settlement, "Genesis settlement funding"),
		dr(Vault, vault, "Genesis vault cash"),
		cr(Capital, settlement+vault, "Share capital")))
	return err
}

func clientKey(op, key string) string {
	return "client:" + op + ":" + key
}

func positive(amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("%w: amount must be positive", ledger.ErrInvalid)
	}
	return nil
}

func (b *Bank) CashDeposit(ctx context.Context, key, nuban string, amount int64, at time.Time) (ledger.Posted, error) {
	if err := positive(amount); err != nil {
		return ledger.Posted{}, err
	}
	return b.L.Post(ctx, entry(clientKey("cash_deposit", key), "cash_deposit", at, "CASH DEPOSIT",
		dr(Vault, amount, "Cash deposit"),
		via(cr(CustomerAccount(nuban), amount, "CASH DEPOSIT"), "branch", nil)))
}

func (b *Bank) CashWithdrawal(ctx context.Context, key, nuban string, amount int64, at time.Time) (ledger.Posted, error) {
	if err := positive(amount); err != nil {
		return ledger.Posted{}, err
	}
	e := entry(clientKey("cash_withdrawal", key), "cash_withdrawal", at, "CASH WITHDRAWAL",
		via(dr(CustomerAccount(nuban), amount, "CASH WITHDRAWAL"), "branch", nil),
		cr(Vault, amount, "Cash withdrawal"))
	e.LimitedAmount = amount
	return b.L.Post(ctx, e)
}

func (b *Bank) ClosurePayout(ctx context.Context, key, nuban string, amount int64, at time.Time) (ledger.Posted, error) {
	if err := positive(amount); err != nil {
		return ledger.Posted{}, err
	}
	e := entry(clientKey("closure_payout", key), "closure_payout", at, "NDIC PAYOUT",
		via(dr(CustomerAccount(nuban), amount, "NDIC PAYOUT"), "branch", nil),
		cr(Vault, amount, "NDIC payout in cash"))
	e.AllowOverdraft = true
	return b.L.Post(ctx, e)
}

func (b *Bank) ClosurePayouts(ctx context.Context) (map[string]int64, error) {
	rows, err := b.L.Pool().Query(ctx, `SELECT a.nuban, sum(jl.amount)::bigint FROM journal_lines jl
		JOIN journal_entries je ON je.id = jl.entry_id JOIN gl_accounts a ON a.id = jl.account_id
		WHERE je.kind = 'closure_payout' AND a.customer GROUP BY a.nuban`)
	if err != nil {
		return nil, fmt.Errorf("read closure payouts: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var nuban string
		var paid int64
		if err := rows.Scan(&nuban, &paid); err != nil {
			return nil, fmt.Errorf("read closure payouts: %w", err)
		}
		out[nuban] = paid
	}
	return out, rows.Err()
}

func (b *Bank) FreezeAccount(ctx context.Context, nuban string) error {
	if err := b.L.SetStatus(ctx, CustomerAccount(nuban), "frozen"); err != nil {
		return fmt.Errorf("freeze account %s: %w", nuban, err)
	}
	return nil
}

func (b *Bank) AccountStatus(ctx context.Context, nuban string) (string, error) {
	a, err := b.L.AccountByNUBAN(ctx, nuban)
	if err != nil {
		return "", fmt.Errorf("read account %s: %w", nuban, err)
	}
	return a.Status, nil
}

func (b *Bank) OwnTransfer(ctx context.Context, key, from, to string, amount int64, narration string, at time.Time) (ledger.Posted, error) {
	if err := positive(amount); err != nil {
		return ledger.Posted{}, err
	}
	if from == to {
		return ledger.Posted{}, fmt.Errorf("%w: same account", ledger.ErrInvalid)
	}
	fa, err := b.L.Account(ctx, CustomerAccount(from))
	if err != nil {
		return ledger.Posted{}, err
	}
	ta, err := b.L.Account(ctx, CustomerAccount(to))
	if err != nil {
		return ledger.Posted{}, err
	}
	e := entry(clientKey("own_transfer", key), "transfer", at, "TRANSFER",
		via(dr(CustomerAccount(from), amount, withNarration("TRF TO "+ta.Name+"/"+to, narration)), "internal", b.party(ta)),
		via(cr(CustomerAccount(to), amount, withNarration("TRF FRM "+fa.Name+"/"+from, narration)), "internal", b.party(fa)))
	e.LimitedAmount = amount
	return b.L.Post(ctx, e)
}

func (b *Bank) party(a ledger.Account) *Counterparty {
	return &Counterparty{Name: a.Name, AccountNumber: a.NUBAN, BankCode: b.P.NIPCode, BankName: b.P.Name}
}

func withNarration(base, narration string) string {
	if narration == "" {
		return base
	}
	return base + "/" + narration
}

type NIPSend struct {
	Key       string
	From      string
	Amount    int64
	SessionID string
	To        Counterparty
	At        time.Time
	NoCharges bool
	Narration string
}

func (b *Bank) SendNIP(ctx context.Context, s NIPSend) (ledger.Posted, charges.Transfer, error) {
	if err := positive(s.Amount); err != nil {
		return ledger.Posted{}, charges.Transfer{}, err
	}
	if len(s.SessionID) != 30 {
		return ledger.Posted{}, charges.Transfer{}, fmt.Errorf("%w: session ID must be 30 digits", ledger.ErrInvalid)
	}
	c := charges.Transfer{}
	if !s.NoCharges {
		c = b.fees.ForTransfer(s.Amount)
	}
	short := s.SessionID[18:]
	to := s.To
	narr := withNarration(fmt.Sprintf("NIP TRF TO %s/%s/%s", to.Name, to.BankName, short), s.Narration)
	cust := CustomerAccount(s.From)
	lines := []ledger.Line{
		via(dr(cust, s.Amount, narr), "nip", &to),
		cr(NIPOutward, s.Amount, narr),
	}
	if c.Fee > 0 {
		lines = append(lines, via(dr(cust, c.Fee, "NIP CHARGE "+short), "charge", nil), cr(FeeIncome, c.Fee, "NIP CHARGE "+short))
	}
	if c.VAT > 0 {
		lines = append(lines, via(dr(cust, c.VAT, "VAT ON NIP CHARGE "+short), "charge", nil), cr(VATPayable, c.VAT, "VAT ON NIP CHARGE "+short))
	}
	if c.StampDuty > 0 {
		lines = append(lines, via(dr(cust, c.StampDuty, "STAMP DUTY "+short), "charge", nil), cr(StampPayable, c.StampDuty, "STAMP DUTY "+short))
	}
	e := entry(clientKey("send_nip", s.Key), "transfer", s.At, narr, lines...)
	e.SessionID = s.SessionID
	if !s.NoCharges {
		e.LimitedAmount = s.Amount
	}
	p, err := b.L.Post(ctx, e)
	return p, c, err
}

type PriorSend struct {
	SessionID string
	At        time.Time
	To        Counterparty
}

func (b *Bank) PriorNIPSend(ctx context.Context, key string) (PriorSend, bool, error) {
	var p PriorSend
	var cp []byte
	err := b.L.Pool().QueryRow(ctx, `SELECT je.session_id, je.posted_at, jl.counterparty FROM journal_entries je JOIN journal_lines jl ON jl.entry_id = je.id AND jl.line_no = 1
		WHERE je.idempotency_key = $1 AND je.session_id IS NOT NULL`, clientKey("send_nip", key)).Scan(&p.SessionID, &p.At, &cp)
	if errors.Is(err, pgx.ErrNoRows) {
		return PriorSend{}, false, nil
	}
	if err != nil {
		return PriorSend{}, false, fmt.Errorf("find NIP send for key %s: %w", key, err)
	}
	if err := json.Unmarshal(cp, &p.To); err != nil {
		return PriorSend{}, false, fmt.Errorf("read counterparty of NIP send for key %s: %w", key, err)
	}
	return p, true, nil
}

func (b *Bank) SentNIP(ctx context.Context, sessionID string) (int64, error) {
	var id int64
	err := b.L.Pool().QueryRow(ctx, `SELECT id FROM journal_entries WHERE session_id = $1 AND idempotency_key LIKE $2 || '%' ORDER BY id LIMIT 1`, sessionID, clientKey("send_nip", "")).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: no NIP send for session %s", ledger.ErrInvalid, sessionID)
	}
	if err != nil {
		return 0, fmt.Errorf("find NIP send %s: %w", sessionID, err)
	}
	return id, nil
}

func (b *Bank) ReverseNIP(ctx context.Context, entryID int64, sessionID string, at time.Time) (ledger.Posted, error) {
	var key string
	var session *string
	err := b.L.Pool().QueryRow(ctx, `SELECT idempotency_key, session_id FROM journal_entries WHERE id = $1`, entryID).Scan(&key, &session)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Posted{}, fmt.Errorf("%w: entry %d does not exist", ledger.ErrInvalid, entryID)
	}
	if err != nil {
		return ledger.Posted{}, fmt.Errorf("read NIP entry: %w", err)
	}
	if !strings.HasPrefix(key, clientKey("send_nip", "")) || session == nil || *session != sessionID {
		return ledger.Posted{}, fmt.Errorf("%w: entry %d is not the NIP send for session %s", ledger.ErrInvalid, entryID, sessionID)
	}
	p, err := b.L.Reverse(ctx, entryID, "nip-rev:"+sessionID, at, "")
	if err != nil {
		return p, fmt.Errorf("reverse NIP session %s: %w", sessionID, err)
	}
	return p, nil
}

func (b *Bank) ReceiveNIP(ctx context.Context, sessionID, to string, amount int64, from Counterparty, narration string, at time.Time) (CreditAnswer, error) {
	ans, err := b.Credit(ctx, NIPCredit{SessionID: sessionID, FromCode: from.BankCode, FromBankName: from.BankName, FromNUBAN: from.AccountNumber, FromName: from.Name, ToNUBAN: to, Amount: amount, Narration: narration, CreatedAt: at}, at)
	if err != nil {
		return CreditAnswer{}, err
	}
	switch ans.Code {
	case nip.OK:
		return ans, nil
	case nip.InvalidAccount:
		return ans, fmt.Errorf("credit session %s: %w", sessionID, nip.ErrInvalidAccount)
	case nip.NoRecord:
		return ans, fmt.Errorf("credit session %s: %w", sessionID, nip.ErrNoRecord)
	}
	return ans, fmt.Errorf("credit session %s refused with code %s", sessionID, ans.Code)
}

func nipInEntry(sessionID, to string, amount int64, from Counterparty, narration string, at time.Time) (ledger.Entry, error) {
	if err := positive(amount); err != nil {
		return ledger.Entry{}, err
	}
	if len(sessionID) != 30 {
		return ledger.Entry{}, fmt.Errorf("%w: session ID must be 30 digits", ledger.ErrInvalid)
	}
	narr := withNarration(fmt.Sprintf("NIP TRF FRM %s/%s/%s", from.Name, from.BankName, sessionID[18:]), narration)
	lines := []ledger.Line{dr(NIPInward, amount, narr), via(cr(CustomerAccount(to), amount, narr), "nip", &from)}
	e := entry(nipInKey(sessionID), "nip_inward", at, narr, lines...)
	e.SessionID = sessionID
	return e, nil
}

func nipInKey(sessionID string) string {
	return "nip-in:" + sessionID
}

func (b *Bank) Settle(ctx context.Context, sessionKey string, outward, inward int64, at time.Time) (ledger.Posted, error) {
	narr := "Settlement " + sessionKey
	lines := []ledger.Line{}
	for _, ln := range []ledger.Line{{Account: NIPOutward, Amount: outward}, {Account: NIPInward, Amount: -inward}, {Account: Settlement, Amount: inward - outward}} {
		if ln.Amount != 0 {
			ln.Narration = narr
			lines = append(lines, ln)
		}
	}
	if len(lines) < 2 {
		return ledger.Posted{}, nil
	}
	return b.L.Post(ctx, entry("settle:"+sessionKey, "settlement", at, "NIP settlement "+sessionKey, lines...))
}

func (b *Bank) Settlements(ctx context.Context) (map[string]int64, error) {
	rows, err := b.L.Pool().Query(ctx, `SELECT substr(e.idempotency_key, length('settle:') + 1), coalesce(sum(jl.amount) FILTER (WHERE a.code = $1), 0)::bigint
		FROM journal_entries e JOIN journal_lines jl ON jl.entry_id = e.id JOIN gl_accounts a ON a.id = jl.account_id
		WHERE e.kind = 'settlement' GROUP BY 1`, Settlement)
	if err != nil {
		return nil, fmt.Errorf("read settlements: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var key string
		var net int64
		if err := rows.Scan(&key, &net); err != nil {
			return nil, fmt.Errorf("read settlements: %w", err)
		}
		out[key] = net
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlements: %w", err)
	}
	return out, nil
}
