package connect

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	syncOverlapDays = 14
	syncPageSize    = 500
)

type wireMoney struct {
	Amount int64 `json:"amount_minor"`
}

type wireTxn struct {
	ID           string  `json:"id"`
	Sequence     int64   `json:"sequence"`
	Status       string  `json:"status"`
	Amount       int64   `json:"amount_minor"`
	BookedAt     *string `json:"booked_at"`
	ValueDate    string  `json:"value_date"`
	Description  string  `json:"description"`
	Counterparty *struct {
		Name          string `json:"name"`
		AccountNumber string `json:"account_number"`
		BankCode      string `json:"bank_code"`
		BankName      string `json:"bank_name"`
	} `json:"counterparty"`
	SessionID    string `json:"session_id"`
	BankRef      string `json:"bank_reference"`
	Channel      string `json:"channel"`
	BalanceAfter int64  `json:"balance_after_minor"`
	HadPending   bool   `json:"had_pending"`
}

type bankLine struct {
	Key          string
	Pending      bool
	HadPending   bool
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

func (w wireTxn) presented() (bankLine, error) {
	vd, err := clock.ParseDate(w.ValueDate)
	if err != nil {
		return bankLine{}, err
	}
	p := bankLine{Key: w.ID, Pending: w.Status == "pending", HadPending: w.HadPending, Amount: w.Amount, ValueDate: vd, Narration: w.Description, SessionID: w.SessionID, BankRef: w.BankRef, Channel: w.Channel, Seq: w.Sequence}
	if !p.Pending {
		p.BalanceAfter = w.BalanceAfter
		if w.BookedAt != nil {
			bt, err := time.Parse(time.RFC3339, *w.BookedAt)
			if err != nil {
				return bankLine{}, err
			}
			p.BookedAt = bt
		}
	}
	if w.Counterparty != nil {
		p.Counterparty = &Counterparty{Name: strp(w.Counterparty.Name), AccountNumber: strp(w.Counterparty.AccountNumber), BankCode: strp(w.Counterparty.BankCode), BankName: strp(w.Counterparty.BankName)}
	}
	return p, nil
}

type bankPull struct {
	bank    *config.Bank
	vid     string
	consent string
	nuban   string
	from    clock.Date
}

func (a *App) bankPullPlanLocked(c *Connection, acct *AccountRec) (bankPull, error) {
	b := a.cfg.Bank(c.Institution)
	if b == nil {
		return bankPull{}, &bankDown{fmt.Errorf("%s is not configured", c.Institution)}
	}
	from := acct.From
	if !(c.FullDue || !acct.HistoryDone || acct.AsAt.IsZero()) {
		if w := clock.DateOf(acct.AsAt).AddDays(-syncOverlapDays); w.After(from) {
			from = w
		}
	}
	return bankPull{bank: b, vid: c.ID, consent: c.ConsentID, nuban: acct.NUBAN, from: from}, nil
}

func (a *App) pullBank(p bankPull) (pulled, error) {
	out := pulled{from: p.from, windowed: true}
	cursor := ""
	for {
		q := url.Values{"page_size": {strconv.Itoa(syncPageSize)}, "from": {p.from.String()}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Data   []wireTxn `json:"data"`
			Cursor *string   `json:"next_cursor"`
		}
		if err := a.bankGet(p.bank, p.vid, "/accounts/"+p.nuban+"/transactions?"+q.Encode(), &page); err != nil {
			return pulled{}, err
		}
		for _, w := range page.Data {
			line, err := w.presented()
			if err != nil {
				return pulled{}, err
			}
			out.lines = append(out.lines, line)
		}
		if page.Cursor == nil || *page.Cursor == "" {
			break
		}
		cursor = *page.Cursor
	}
	var bal struct {
		Ledger    wireMoney `json:"ledger_balance"`
		Available wireMoney `json:"available_balance"`
	}
	if err := a.bankGet(p.bank, p.vid, "/accounts/"+p.nuban+"/balances", &bal); err != nil {
		return pulled{}, err
	}
	out.ledger, out.available = bal.Ledger.Amount, bal.Available.Amount
	return out, nil
}

func (a *App) bankBalanceOn(p bankPull, d clock.Date) (int64, error) {
	var out struct {
		Ledger wireMoney `json:"ledger_balance"`
	}
	if err := a.bankGet(p.bank, p.vid, "/accounts/"+p.nuban+"/balances?as_of="+d.String(), &out); err != nil {
		return 0, err
	}
	return out.Ledger.Amount, nil
}

type bankAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Currency string `json:"currency"`
}

type bankGrant struct {
	ConsentID string        `json:"consent_id"`
	From      clock.Date    `json:"transaction_from"`
	Ends      time.Time     `json:"consent_expires_at"`
	Accounts  []bankAccount `json:"data"`
}
