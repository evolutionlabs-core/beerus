package world

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var ErrUnknownAccount = errors.New("world: unknown account")

type Presented struct {
	Key          string
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

func (w *World) ensureFor(at time.Time) {
	w.ensure(clock.DateOf(at).AddDays(3))
}

func (w *World) Fetch(account string, at time.Time, from clock.Date) ([]Presented, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	acct, ok := w.accounts[account]
	if !ok {
		return nil, ErrUnknownAccount
	}
	w.ensureFor(at)
	inst := w.institutions[acct.Institution]
	muts := map[string][]Mutation{}
	for _, m := range w.ov.Mutations {
		if m.Account == account && !m.EffectiveAt.After(at) {
			muts[m.Key] = append(muts[m.Key], m)
		}
	}
	unstableAt, unstable := w.ov.UnstableAt[acct.Institution]
	unstable = unstable || !inst.StableIDs
	out := []Presented{}
	for _, l := range w.lines[account] {
		var p Presented
		switch {
		case !l.BookedAt.After(at):
			if clock.DateOf(l.BookedAt).Before(from) {
				continue
			}
			p = Presented{Key: l.Key, HadPending: l.HasPending(), Amount: l.Amount, At: l.BookedAt, BookedAt: l.BookedAt, BalanceAfter: l.BalanceAfter, Seq: l.PostedSeq}
		case l.HasPending() && !l.PendingAt.After(at):
			if clock.DateOf(l.PendingAt).Before(from) {
				continue
			}
			p = Presented{Key: l.Key, Pending: true, Amount: l.PendingAmount, At: l.PendingAt, Seq: l.PendingSeq}
		default:
			continue
		}
		p.ValueDate = l.ValueDate
		p.Narration = l.Narration
		p.SessionID = l.SessionID
		p.BankRef = l.BankRef
		p.Channel = l.Channel
		if l.Counterparty != nil {
			cp := *l.Counterparty
			p.Counterparty = &cp
		}
		if unstable && !p.Pending && at.After(unstableAt) && at.Sub(l.BookedAt) < 7*24*time.Hour {
			epoch := int(at.Sub(unstableAt) / (24 * time.Hour))
			p.BankRef = fmt.Sprintf("%s-%d", ID("r", l.Key, fmt.Sprint(epoch))[2:12], epoch)
		}
		for _, m := range muts[l.Key] {
			switch m.Field {
			case "narration":
				p.Narration = m.Value
			case "bank_ref":
				p.BankRef = m.Value
			case "counterparty_name":
				if p.Counterparty == nil {
					p.Counterparty = &Counterparty{}
				}
				p.Counterparty.Name = m.Value
			}
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (w *World) LedgerAt(account string, at time.Time) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	acct, ok := w.accounts[account]
	if !ok {
		return 0, ErrUnknownAccount
	}
	w.ensureFor(at)
	ls := w.lines[account]
	i := sort.Search(len(ls), func(i int) bool { return ls[i].BookedAt.After(at) })
	if i == 0 {
		return acct.Opening, nil
	}
	return ls[i-1].BalanceAfter, nil
}

func (w *World) PendingAt(account string, at time.Time) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.accounts[account]; !ok {
		return 0, ErrUnknownAccount
	}
	w.ensureFor(at)
	var sum int64
	for _, l := range w.lines[account] {
		if l.HasPending() && !l.PendingAt.After(at) && l.BookedAt.After(at) {
			sum += l.PendingAmount
		}
	}
	return sum, nil
}

func (w *World) EndOfDay(account string, d clock.Date) (int64, error) {
	return w.LedgerAt(account, d.End().Add(-time.Nanosecond))
}

func (w *World) Opened(account string) (clock.Date, error) {
	if _, ok := w.accounts[account]; !ok {
		return clock.Date{}, ErrUnknownAccount
	}
	return w.cfg.Genesis, nil
}

func (w *World) LinesFor(account string, through time.Time) []Line {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ensureFor(through)
	out := []Line{}
	for _, l := range w.lines[account] {
		if !l.BookedAt.After(through) {
			out = append(out, *l)
		}
	}
	return out
}
