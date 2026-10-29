package world

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrAmbiguousAccount  = errors.New("more than one account has that number; use bank:number")
)

type TransferRequest struct {
	From      string
	To        string
	ToBank    string
	ToName    string
	Amount    int64
	Narration string
}

type TransferResult struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Kind         string `json:"kind"`
	SessionID    string `json:"session_id,omitempty"`
	Amount       int64  `json:"amount_minor"`
	Fee          int64  `json:"fee_minor"`
	VAT          int64  `json:"vat_minor"`
	StampDuty    int64  `json:"stamp_duty_minor"`
	TotalDebit   int64  `json:"total_debit_minor"`
	Credited     bool   `json:"credited"`
	BalanceAfter int64  `json:"sender_balance_after_minor"`
	ToName       string `json:"to_name"`
	ToBank       string `json:"to_bank"`
	LineKey      string `json:"-"`
}

func (w *World) FindAccount(ref string) (Account, error) {
	if a, ok := w.Account(ref); ok {
		return a, nil
	}
	var found []Account
	for _, a := range w.accounts {
		if a.NUBAN == ref {
			found = append(found, *a)
		}
	}
	switch len(found) {
	case 0:
		return Account{}, ErrUnknownAccount
	case 1:
		return found[0], nil
	}
	return Account{}, ErrAmbiguousAccount
}

func (w *World) institutionByName(name string) (Institution, bool) {
	for _, i := range w.institutions {
		if strings.EqualFold(i.ID, name) || strings.EqualFold(i.Name, name) {
			return *i, true
		}
	}
	return Institution{}, false
}

func (w *World) Quote(at time.Time, r TransferRequest) (TransferResult, error) {
	return w.transfer(at, r, false)
}

func (w *World) Transfer(at time.Time, r TransferRequest) (TransferResult, error) {
	return w.transfer(at, r, true)
}

func (w *World) transfer(at time.Time, r TransferRequest, send bool) (TransferResult, error) {
	if r.Amount <= 0 {
		return TransferResult{}, errors.New("amount must be more than zero")
	}
	from, err := w.FindAccount(r.From)
	if err != nil {
		return TransferResult{}, fmt.Errorf("from account: %w", err)
	}
	fromInst := w.institutions[from.Institution]
	var to *Account
	if a, err := w.FindAccount(r.To); err == nil {
		to = &a
	} else if !errors.Is(err, ErrUnknownAccount) {
		return TransferResult{}, fmt.Errorf("to account: %w", err)
	}
	if to != nil && to.Key == from.Key {
		return TransferResult{}, errors.New("the from and to accounts are the same")
	}
	cp := &Counterparty{AccountNumber: r.To, Name: strings.ToUpper(r.ToName)}
	var toInst Institution
	switch {
	case to != nil:
		toInst = *w.institutions[to.Institution]
		cp.AccountNumber = to.NUBAN
		cp.Name = strings.ToUpper(w.personas[to.Owner].Name)
	default:
		if len(r.To) != 10 || strings.Trim(r.To, "0123456789") != "" {
			return TransferResult{}, errors.New("to must be a known account or a 10-digit account number")
		}
		if r.ToBank == "" {
			r.ToBank = "Bank C"
		}
		if i, ok := w.institutionByName(r.ToBank); ok {
			toInst = i
		} else {
			matched := false
			for _, b := range counterpartyBanks {
				if strings.EqualFold(b.name, r.ToBank) || b.code == r.ToBank {
					toInst = Institution{ID: b.code, Name: b.name, NIPCode: b.code, Style: "traditional"}
					matched = true
				}
			}
			if !matched {
				return TransferResult{}, fmt.Errorf("unknown bank %q; use Bank A, Bank B, Bank C or Bank D Microfinance", r.ToBank)
			}
		}
		if cp.Name == "" {
			cp.Name = "BENEFICIARY " + r.To[6:]
		}
	}
	cp.BankCode = toInst.NIPCode
	cp.BankName = toInst.Name
	d := clock.DateOf(at)
	sameOwner := to != nil && to.Owner == from.Owner
	intra := toInst.ID == fromInst.ID
	res := TransferResult{From: from.Key, To: cp.AccountNumber, Amount: r.Amount}
	if intra {
		res.Kind = "intra_bank"
		res.StampDuty = charges.StampDuty(r.Amount, d, sameOwner)
	} else {
		res.Kind = "nip"
		c := charges.ForTransfer(r.Amount, d, false)
		res.Fee, res.VAT, res.StampDuty = c.Fee, c.VAT, c.StampDuty
	}
	res.TotalDebit = r.Amount + res.Fee + res.VAT + res.StampDuty
	ledger, err := w.LedgerAt(from.Key, at)
	if err != nil {
		return res, err
	}
	pending, err := w.PendingAt(from.Key, at)
	if err != nil {
		return res, err
	}
	available := ledger
	if pending < 0 {
		available += pending
	}
	if available < res.TotalDebit {
		return res, fmt.Errorf("%w: available %d, needed %d (minor units)", ErrInsufficientFunds, available, res.TotalDebit)
	}
	res.BalanceAfter = ledger - res.TotalDebit
	if to != nil {
		res.ToName = w.personas[to.Owner].Name
	} else {
		res.ToName = cp.Name
	}
	res.ToBank = toInst.Name
	if !send {
		return res, nil
	}
	s := w.newScn(fmt.Sprintf("transfer-%d", w.injectedCount()), at)
	g := &dayGen{w: w, d: d, rng: s.rng}
	ref := g.bankRef(fromInst, at)
	lines := []Line{}
	if intra {
		narr := r.Narration
		if narr == "" {
			narr = g.narr(fromInst, "out", cp.Name, cp.BankName, ref)
			if fromInst.Style != "digital" {
				narr = "TRF TO " + cp.Name + "/" + ref
			}
		}
		lines = append(lines, Line{Key: s.key(from.Key), Account: from.Key, Amount: -r.Amount, BookedAt: at, Narration: narr, Counterparty: cp, BankRef: ref, Channel: "internal"})
		if res.StampDuty > 0 {
			lines = append(lines, Line{Key: s.key(from.Key), Account: from.Key, Amount: -res.StampDuty, BookedAt: at, Narration: g.narr(fromInst, "stamp", ref), BankRef: ref, Channel: "charge"})
		}
		if to != nil {
			back := &Counterparty{Name: strings.ToUpper(w.personas[from.Owner].Name), AccountNumber: from.NUBAN, BankCode: fromInst.NIPCode, BankName: fromInst.Name}
			in := "TRF FRM " + back.Name + "/" + ref
			if fromInst.Style == "digital" {
				in = g.narr(fromInst, "in", back.Name)
			}
			lines = append(lines, Line{Key: s.key(to.Key), Account: to.Key, Amount: r.Amount, BookedAt: at, Narration: in, Counterparty: back, BankRef: ref, Channel: "internal"})
			res.Credited = true
		}
	} else {
		sid := s.sid(fromInst.NIPCode, at)
		res.SessionID = sid
		short := sid[len(sid)-12:]
		narr := r.Narration
		if narr == "" {
			narr = g.narr(fromInst, "out", cp.Name, cp.BankName, short)
		}
		lines = append(lines,
			Line{Key: s.key(from.Key), Account: from.Key, Amount: -r.Amount, BookedAt: at, Narration: narr, Counterparty: cp, SessionID: sid, BankRef: ref, Channel: "nip"},
			Line{Key: s.key(from.Key), Account: from.Key, Amount: -res.Fee, BookedAt: at, Narration: g.narr(fromInst, "fee", short), SessionID: sid, BankRef: g.bankRef(fromInst, at), Channel: "charge"},
			Line{Key: s.key(from.Key), Account: from.Key, Amount: -res.VAT, BookedAt: at, Narration: g.narr(fromInst, "vat", short), SessionID: sid, BankRef: g.bankRef(fromInst, at), Channel: "charge"})
		if res.StampDuty > 0 {
			lines = append(lines, Line{Key: s.key(from.Key), Account: from.Key, Amount: -res.StampDuty, BookedAt: at, Narration: g.narr(fromInst, "stamp", short), SessionID: sid, BankRef: g.bankRef(fromInst, at), Channel: "charge"})
		}
		if to != nil {
			back := &Counterparty{Name: strings.ToUpper(w.personas[from.Owner].Name), AccountNumber: from.NUBAN, BankCode: fromInst.NIPCode, BankName: fromInst.Name}
			lines = append(lines, Line{Key: s.key(to.Key), Account: to.Key, Amount: r.Amount, BookedAt: at, Narration: g.narr(&toInst, "in", back.Name, back.BankName, short), Counterparty: back, SessionID: sid, BankRef: g.bankRef(&toInst, at), Channel: "nip"})
			if levy := charges.EMTL(r.Amount, d); levy > 0 {
				lines = append(lines, Line{Key: s.key(to.Key), Account: to.Key, Amount: -levy, BookedAt: at, Narration: g.narr(&toInst, "emtl", short), SessionID: sid, BankRef: g.bankRef(&toInst, at), Channel: "charge"})
			}
			res.Credited = true
		}
	}
	if err := w.Inject(at, lines); err != nil {
		return res, err
	}
	res.LineKey = lines[0].Key
	res.BalanceAfter = ledger - res.TotalDebit
	return res, nil
}
