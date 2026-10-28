package connect

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var (
	reTradIn    = regexp.MustCompile(`^NIP TRF FRM (.+?)/`)
	reTradOut   = regexp.MustCompile(`^NIP TRF TO (.+?)/`)
	reSMS       = regexp.MustCompile(`^SMS ALERT CHARGES (.+)$`)
	reMaint     = regexp.MustCompile(`^ACCOUNT MAINT FEE (.+)$`)
	reSalary    = regexp.MustCompile(`^SALARY CREDIT (.+)$`)
	reRenarrate = regexp.MustCompile(` /RENARRATED$`)
)

func titleCase(s string) string {
	parts := strings.Fields(strings.ToLower(s))
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func Normalise(raw string) string {
	s := strings.TrimSpace(reRenarrate.ReplaceAllString(raw, ""))
	switch {
	case strings.HasPrefix(s, "REV/"):
		return "Reversal: " + Normalise(strings.TrimPrefix(s, "REV/"))
	case strings.HasPrefix(s, "Reversal: "):
		return "Reversal: " + Normalise(strings.TrimPrefix(s, "Reversal: "))
	case reTradIn.MatchString(s):
		return "Transfer from " + titleCase(reTradIn.FindStringSubmatch(s)[1])
	case reTradOut.MatchString(s):
		return "Transfer to " + titleCase(reTradOut.FindStringSubmatch(s)[1])
	case strings.HasPrefix(s, "NIP CHARGE"), s == "Transfer fee":
		return "Transfer fee"
	case strings.HasPrefix(s, "VAT ON NIP CHARGE"):
		return "VAT on transfer fee"
	case strings.HasPrefix(s, "VAT ON SMS ALERT CHARGES"), s == "VAT on SMS notification fee":
		return "VAT on SMS alert charges"
	case strings.HasPrefix(s, "VAT ON ACCOUNT MAINT FEE"), s == "VAT on account maintenance fee":
		return "VAT on account maintenance fee"
	case strings.HasPrefix(s, "VAT"):
		return "VAT"
	case strings.HasPrefix(s, "STAMP DUTY"), s == "Stamp duty":
		return "Stamp duty"
	case strings.HasPrefix(s, "EMTL"), s == "Electronic money transfer levy":
		return "Electronic money transfer levy"
	case strings.HasPrefix(s, "SMS notification fee "):
		return "SMS alert charges " + strings.TrimPrefix(s, "SMS notification fee ")
	case reSMS.MatchString(s):
		return "SMS alert charges " + titleCase(reSMS.FindStringSubmatch(s)[1])
	case reMaint.MatchString(s):
		return "Account maintenance fee " + titleCase(reMaint.FindStringSubmatch(s)[1])
	case reSalary.MatchString(s):
		return "Salary from " + titleCase(reSalary.FindStringSubmatch(s)[1])
	case s == "TRF TO SAVINGS":
		return "Transfer to savings"
	case s == "TRF FRM CURRENT ACCT":
		return "Transfer from current account"
	}
	return s
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func txnID(partner, accountKey, lineKey string, pending bool) string {
	if pending {
		return world.ID("txn", partner, accountKey, lineKey, "pending")
	}
	return world.ID("txn", partner, accountKey, lineKey)
}

func AccountID(partner, institution, nuban string) string {
	return world.ID("acc", partner, institution, nuban)
}

func toTxn(partner string, acct *AccountRec, p world.Presented) Txn {
	t := Txn{
		ID:             txnID(partner, acct.WorldKey, p.Key, p.Pending),
		AccountID:      acct.ID,
		Sequence:       p.Seq,
		AmountMinor:    p.Amount,
		Currency:       "NGN",
		Status:         "posted",
		ValueDate:      p.ValueDate.String(),
		DescriptionRaw: p.Narration,
		Description:    Normalise(p.Narration),
		References:     References{SessionID: strp(p.SessionID), BankReference: strp(p.BankRef)},
		Channel:        p.Channel,
	}
	if p.Pending {
		t.Status = "pending"
	} else {
		b := p.BookedAt.In(clock.Lagos).Format(time.RFC3339)
		t.BookedAt = &b
		bal := p.BalanceAfter
		t.BalanceAfterMinor = &bal
		if p.HadPending {
			id := txnID(partner, acct.WorldKey, p.Key, true)
			t.PendingTransactionID = &id
		}
	}
	if p.Counterparty != nil {
		t.Counterparty = &Counterparty{
			Name:          strp(p.Counterparty.Name),
			AccountNumber: strp(p.Counterparty.AccountNumber),
			BankCode:      strp(p.Counterparty.BankCode),
			BankName:      strp(p.Counterparty.BankName),
		}
	}
	return t
}

func sp(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func cpEqual(a, b *Counterparty) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return sp(a.Name) == sp(b.Name) && sp(a.AccountNumber) == sp(b.AccountNumber) && sp(a.BankCode) == sp(b.BankCode) && sp(a.BankName) == sp(b.BankName)
}

func changeReason(old, cur Txn) string {
	switch {
	case cur.Status == "pending" && (old.AmountMinor != cur.AmountMinor || old.ValueDate != cur.ValueDate):
		return "pending_updated"
	case old.DescriptionRaw != cur.DescriptionRaw || old.Description != cur.Description:
		return "description_changed"
	case sp(old.References.BankReference) != sp(cur.References.BankReference) || sp(old.References.SessionID) != sp(cur.References.SessionID):
		return "bank_reference_changed"
	case !cpEqual(old.Counterparty, cur.Counterparty):
		return "counterparty_changed"
	case cur.Status == "pending" && old.Sequence != cur.Sequence:
		return "pending_updated"
	}
	return ""
}

type fetchResult struct {
	added, modified, removed int
}

func (r fetchResult) changed() bool {
	return r.added+r.modified+r.removed > 0
}

func (a *App) fetchAccount(partner string, acct *AccountRec, at time.Time) (fetchResult, error) {
	var res fetchResult
	lines, err := a.world.Fetch(acct.WorldKey, at, acct.From)
	if err != nil {
		return res, err
	}
	feed := a.st.Feeds[acct.ID]
	if feed == nil {
		feed = &Feed{Lines: map[string]Txn{}, Gone: map[string]bool{}}
		a.st.Feeds[acct.ID] = feed
	}
	seen := map[string]bool{}
	var added, modified []LogEntry
	for _, p := range lines {
		t := toTxn(partner, acct, p)
		seen[t.ID] = true
		if feed.Gone[t.ID] {
			continue
		}
		old, ok := feed.Lines[t.ID]
		if !ok {
			added = append(added, LogEntry{Kind: "added", Txn: t})
			feed.Lines[t.ID] = t
			continue
		}
		if old.Status == "posted" && old.AmountMinor != t.AmountMinor {
			t.AmountMinor = old.AmountMinor
		}
		if reason := changeReason(old, t); reason != "" {
			modified = append(modified, LogEntry{Kind: "modified", Txn: t, Reason: reason})
			feed.Lines[t.ID] = t
		}
	}
	var removed []LogEntry
	ids := make([]string, 0, len(feed.Lines))
	for id := range feed.Lines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := feed.Lines[id]
		if t.Status == "pending" && !seen[id] {
			removed = append(removed, LogEntry{Kind: "removed", Txn: Txn{ID: id}})
			delete(feed.Lines, id)
			feed.Gone[id] = true
		}
	}
	sort.SliceStable(added, func(i, j int) bool { return added[i].Txn.Sequence < added[j].Txn.Sequence })
	sort.SliceStable(modified, func(i, j int) bool { return modified[i].Txn.Sequence < modified[j].Txn.Sequence })
	for _, group := range [][]LogEntry{added, modified, removed} {
		for _, e := range group {
			a.st.Seq++
			e.Seq = a.st.Seq
			feed.Log = append(feed.Log, e)
		}
	}
	res.added, res.modified, res.removed = len(added), len(modified), len(removed)
	ledger, err := a.world.LedgerAt(acct.WorldKey, at)
	if err != nil {
		return res, err
	}
	pend, err := a.world.PendingAt(acct.WorldKey, at)
	if err != nil {
		return res, err
	}
	acct.Ledger = ledger
	acct.Available = ledger
	if pend < 0 {
		acct.Available = ledger + pend
	}
	acct.AsAt = at
	a.dirty()
	return res, nil
}

type cursor struct {
	World   string `json:"w"`
	Account string `json:"a"`
	Seq     int64  `json:"s"`
}

var errBadCursor = errors.New("bad cursor")

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return "cur_" + base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	if !strings.HasPrefix(s, "cur_") {
		return c, errBadCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "cur_"))
	if err != nil {
		return c, errBadCursor
	}
	if err := json.Unmarshal(b, &c); err != nil || c.World == "" || c.Account == "" || c.Seq < 0 {
		return c, errBadCursor
	}
	return c, nil
}
