package bank

import (
	"context"
	"fmt"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
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
	L *ledger.Ledger
	P Profile
}

func New(l *ledger.Ledger, p Profile) *Bank {
	return &Bank{L: l, P: p}
}

func Customer(nuban string) string {
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

func (b *Bank) Genesis(ctx context.Context, settlement, vault int64, at time.Time) error {
	_, err := b.L.Post(ctx, entry("genesis", "genesis", at, "Genesis capital",
		dr(Settlement, settlement, "Genesis settlement funding"),
		dr(Vault, vault, "Genesis vault cash"),
		cr(Capital, settlement+vault, "Share capital")))
	return err
}

func (b *Bank) OpenCustomer(ctx context.Context, nuban, name, product, owner string, tier int) (ledger.Account, error) {
	return b.L.OpenAccount(ctx, ledger.Account{Code: Customer(nuban), Name: name, Kind: ledger.Liability, Customer: true, NUBAN: nuban, Product: product, Owner: owner, KYCTier: tier})
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
		cr(Customer(nuban), amount, "CASH DEPOSIT")))
}

func (b *Bank) CashWithdrawal(ctx context.Context, key, nuban string, amount int64, at time.Time) (ledger.Posted, error) {
	if err := positive(amount); err != nil {
		return ledger.Posted{}, err
	}
	e := entry(clientKey("cash_withdrawal", key), "cash_withdrawal", at, "CASH WITHDRAWAL",
		dr(Customer(nuban), amount, "CASH WITHDRAWAL"),
		cr(Vault, amount, "Cash withdrawal"))
	e.LimitedAmount = amount
	return b.L.Post(ctx, e)
}

func (b *Bank) OwnTransfer(ctx context.Context, key, from, to string, amount int64, at time.Time) (ledger.Posted, error) {
	if err := positive(amount); err != nil {
		return ledger.Posted{}, err
	}
	if from == to {
		return ledger.Posted{}, fmt.Errorf("%w: same account", ledger.ErrInvalid)
	}
	fa, err := b.L.Account(ctx, Customer(from))
	if err != nil {
		return ledger.Posted{}, err
	}
	ta, err := b.L.Account(ctx, Customer(to))
	if err != nil {
		return ledger.Posted{}, err
	}
	if fa.Owner == "" || fa.Owner != ta.Owner {
		return ledger.Posted{}, fmt.Errorf("%w: own-account transfers need the same owner", ledger.ErrInvalid)
	}
	e := entry(clientKey("own_transfer", key), "transfer", at, "OWN ACCOUNT TRANSFER",
		dr(Customer(from), amount, "TRF TO "+to),
		cr(Customer(to), amount, "TRF FRM "+from))
	e.LimitedAmount = amount
	return b.L.Post(ctx, e)
}

type NIPSend struct {
	Key         string
	From        string
	Amount      int64
	SessionID   string
	Beneficiary string
	BankName    string
	At          time.Time
}

func (b *Bank) SendNIP(ctx context.Context, s NIPSend) (ledger.Posted, charges.Transfer, error) {
	if err := positive(s.Amount); err != nil {
		return ledger.Posted{}, charges.Transfer{}, err
	}
	if len(s.SessionID) != 30 {
		return ledger.Posted{}, charges.Transfer{}, fmt.Errorf("%w: session ID must be 30 digits", ledger.ErrInvalid)
	}
	c := charges.ForTransfer(s.Amount, clock.DateOf(s.At), false)
	short := s.SessionID[18:]
	narr := fmt.Sprintf("NIP TRF TO %s/%s/%s", s.Beneficiary, s.BankName, short)
	cust := Customer(s.From)
	lines := []ledger.Line{
		dr(cust, s.Amount, narr),
		cr(NIPOutward, s.Amount, narr),
	}
	if c.Fee > 0 {
		lines = append(lines, dr(cust, c.Fee, "NIP CHARGE "+short), cr(FeeIncome, c.Fee, "NIP CHARGE "+short))
	}
	if c.VAT > 0 {
		lines = append(lines, dr(cust, c.VAT, "VAT ON NIP CHARGE "+short), cr(VATPayable, c.VAT, "VAT ON NIP CHARGE "+short))
	}
	if c.StampDuty > 0 {
		lines = append(lines, dr(cust, c.StampDuty, "STAMP DUTY "+short), cr(StampPayable, c.StampDuty, "STAMP DUTY "+short))
	}
	e := entry(clientKey("send_nip", s.Key), "transfer", s.At, narr, lines...)
	e.SessionID = s.SessionID
	e.LimitedAmount = s.Amount
	p, err := b.L.Post(ctx, e)
	return p, c, err
}

func (b *Bank) ReverseNIP(ctx context.Context, entryID int64, sessionID string, at time.Time) (ledger.Posted, error) {
	return b.L.Reverse(ctx, entryID, "nip-rev:"+sessionID, at, "")
}

func (b *Bank) ReceiveNIP(ctx context.Context, sessionID, to string, amount int64, sender, senderBank string, at time.Time) (ledger.Posted, error) {
	if err := positive(amount); err != nil {
		return ledger.Posted{}, err
	}
	if len(sessionID) != 30 {
		return ledger.Posted{}, fmt.Errorf("%w: session ID must be 30 digits", ledger.ErrInvalid)
	}
	narr := fmt.Sprintf("NIP TRF FRM %s/%s/%s", sender, senderBank, sessionID[18:])
	lines := []ledger.Line{dr(NIPInward, amount, narr), cr(Customer(to), amount, narr)}
	if levy := charges.EMTL(amount, clock.DateOf(at)); levy > 0 {
		lines = append(lines, dr(Customer(to), levy, "EMTL "+sessionID[18:]), cr(StampPayable, levy, "EMTL "+sessionID[18:]))
	}
	e := entry("nip-in:"+sessionID, "nip_inward", at, narr, lines...)
	e.SessionID = sessionID
	return b.L.Post(ctx, e)
}

func (b *Bank) Settle(ctx context.Context, sessionKey string, outward, inward int64, at time.Time) (ledger.Posted, error) {
	net := inward - outward
	lines := []ledger.Line{}
	if outward > 0 {
		lines = append(lines, dr(NIPOutward, outward, "Settlement "+sessionKey))
	}
	if inward > 0 {
		lines = append(lines, cr(NIPInward, inward, "Settlement "+sessionKey))
	}
	switch {
	case net > 0:
		lines = append(lines, dr(Settlement, net, "Settlement "+sessionKey))
	case net < 0:
		lines = append(lines, cr(Settlement, -net, "Settlement "+sessionKey))
	}
	if len(lines) < 2 {
		return ledger.Posted{}, nil
	}
	return b.L.Post(ctx, entry("settle:"+sessionKey, "settlement", at, "NIP settlement "+sessionKey, lines...))
}

func (b *Bank) RemitTaxes(ctx context.Context, key string, at time.Time) (ledger.Posted, error) {
	vat, err := b.L.Balance(ctx, VATPayable)
	if err != nil {
		return ledger.Posted{}, err
	}
	stamp, err := b.L.Balance(ctx, StampPayable)
	if err != nil {
		return ledger.Posted{}, err
	}
	lines := []ledger.Line{}
	if vat > 0 {
		lines = append(lines, dr(VATPayable, vat, "VAT remittance"))
	}
	if stamp > 0 {
		lines = append(lines, dr(StampPayable, stamp, "Stamp duty remittance"))
	}
	if vat+stamp == 0 {
		return ledger.Posted{}, nil
	}
	lines = append(lines, cr(Settlement, vat+stamp, "Tax remittance to government"))
	return b.L.Post(ctx, entry(key, "tax_remittance", at, "Monthly tax remittance", lines...))
}
