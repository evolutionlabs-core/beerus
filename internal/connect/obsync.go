package connect

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
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

func (w wireTxn) presented() (world.Presented, error) {
	vd, err := clock.ParseDate(w.ValueDate)
	if err != nil {
		return world.Presented{}, err
	}
	p := world.Presented{Key: w.ID, Pending: w.Status == "pending", HadPending: w.HadPending, Amount: w.Amount, ValueDate: vd, Narration: w.Description, SessionID: w.SessionID, BankRef: w.BankRef, Channel: w.Channel, Seq: w.Sequence}
	if !p.Pending {
		p.BalanceAfter = w.BalanceAfter
		if w.BookedAt != nil {
			bt, err := time.Parse(time.RFC3339, *w.BookedAt)
			if err != nil {
				return world.Presented{}, err
			}
			p.BookedAt, p.At = bt, bt
		}
	}
	if w.Counterparty != nil {
		p.Counterparty = &world.Counterparty{Name: w.Counterparty.Name, AccountNumber: w.Counterparty.AccountNumber, BankCode: w.Counterparty.BankCode, BankName: w.Counterparty.BankName}
	}
	return p, nil
}

func (a *App) nubanOf(acct *AccountRec) string {
	if acct.NUBAN != "" {
		return acct.NUBAN
	}
	wa, _ := a.world.Account(acct.WorldKey)
	return wa.NUBAN
}

func fingerprint(lines []world.Presented) {
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Seq < lines[j].Seq })
	seen := map[string]int{}
	for i := range lines {
		p := &lines[i]
		dir := "cr"
		amt := p.Amount
		if amt < 0 {
			dir, amt = "dr", -amt
		}
		parts := []string{p.ValueDate.String(), dir, strconv.FormatInt(amt, 10), Normalise(p.Narration)}
		if !p.Pending {
			parts = append(parts, strconv.FormatInt(p.BalanceAfter, 10))
		}
		base := strings.Join(parts, "|")
		n := seen[base]
		seen[base]++
		p.Key = world.ID("fp", base, strconv.Itoa(n))
		p.HadPending = false
	}
}

func (a *App) pullBank(c *Connection, acct *AccountRec, at time.Time) (pulled, error) {
	b := a.cfg.Bank(c.Institution)
	if b == nil {
		return pulled{}, &bankDown{fmt.Errorf("%s is not configured", c.Institution)}
	}
	meta, err := a.bankMetaFor(b)
	if err != nil {
		return pulled{}, err
	}
	nuban := a.nubanOf(acct)
	from := acct.From
	if !(c.FullDue || !acct.HistoryDone || acct.AsAt.IsZero()) {
		if w := clock.DateOf(acct.AsAt).AddDays(-syncOverlapDays); w.After(from) {
			from = w
		}
	}
	out := pulled{from: from, windowed: true}
	cursor := ""
	for {
		q := url.Values{"page_size": {strconv.Itoa(syncPageSize)}, "from": {from.String()}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Data   []wireTxn `json:"data"`
			Cursor *string   `json:"next_cursor"`
		}
		if err := a.bankGet(b, c.ID, "/accounts/"+nuban+"/transactions?"+q.Encode(), &page); err != nil {
			return pulled{}, err
		}
		for _, w := range page.Data {
			p, err := w.presented()
			if err != nil {
				return pulled{}, err
			}
			out.lines = append(out.lines, p)
		}
		if page.Cursor == nil || *page.Cursor == "" {
			break
		}
		cursor = *page.Cursor
	}
	if !meta.StableIDs {
		fingerprint(out.lines)
	}
	var bal struct {
		Ledger    wireMoney `json:"ledger_balance"`
		Available wireMoney `json:"available_balance"`
	}
	if err := a.bankGet(b, c.ID, "/accounts/"+nuban+"/balances", &bal); err != nil {
		return pulled{}, err
	}
	out.ledger, out.available = bal.Ledger.Amount, bal.Available.Amount
	return out, nil
}

func (a *App) bankBalanceOn(c *Connection, acct *AccountRec, d clock.Date) (int64, error) {
	b := a.cfg.Bank(c.Institution)
	if b == nil {
		return 0, &bankDown{fmt.Errorf("%s is not configured", c.Institution)}
	}
	var out struct {
		Ledger wireMoney `json:"ledger_balance"`
	}
	if err := a.bankGet(b, c.ID, "/accounts/"+a.nubanOf(acct)+"/balances?as_of="+d.String(), &out); err != nil {
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
