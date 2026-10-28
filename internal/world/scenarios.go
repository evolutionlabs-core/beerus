package world

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type Scenario struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

var Scenarios = []Scenario{
	{"debit_then_reversal", "A failed transfer as a debit, then a reversal credit two hours later"},
	{"late_reversal", "A transfer reversed about 30 hours later after a dispute"},
	{"fee_vat_levy_lines", "A transfer with separate fee, VAT and stamp duty lines"},
	{"identical_same_day_fees", "Two identical ₦50 fee lines on one day"},
	{"pending_amount_change", "A pending line that posts three hours later with a different amount"},
	{"own_account_transfer_across_banks", "A transfer between the same customer's accounts at Bank A and Bank B"},
	{"renarration", "The bank changes the narration of recent posted lines"},
	{"unstable_ids", "The bank's references change every business day from now on"},
	{"bank_outage", "The bank is unreachable for a period (default 2 hours)"},
	{"consent_revoked_mid_sync", "The customer revokes consent at the bank"},
}

type ScenarioParams struct {
	Account     string
	Institution string
	Persona     string
	Duration    time.Duration
	Count       int
}

type scn struct {
	w    *World
	name string
	at   time.Time
	rng  *rand.Rand
	n    int
}

func (w *World) newScn(name string, at time.Time) *scn {
	return &scn{w: w, name: name, at: at, rng: rand.New(rand.NewPCG(uint64(w.cfg.Seed), hash64("scn", name, at.UTC().Format(time.RFC3339Nano))))}
}

func (s *scn) key(acct string) string {
	s.n++
	return fmt.Sprintf("scn:%s:%s:%d:%s", s.name, s.at.UTC().Format("20060102T150405.000"), s.n, acct)
}

func (s *scn) sid(code string, t time.Time) string {
	return code + t.In(clock.Lagos).Format("060102150405") + fmt.Sprintf("%012d", s.rng.Uint64N(1000000000000))
}

func (s *scn) transfer(a Account, amt int64, t time.Time, cp *Counterparty, kind string) []Line {
	inst := s.w.institutions[a.Institution]
	g := &dayGen{w: s.w, d: clock.DateOf(t), rng: s.rng}
	sid := s.sid(inst.NIPCode, t)
	short := sid[len(sid)-12:]
	fee, vat := NIPFee(amt), VAT(NIPFee(amt))
	stamp := StampDuty(amt, clock.DateOf(t), false)
	out := []Line{{Key: s.key(a.Key), Account: a.Key, Amount: -amt, BookedAt: t, Narration: g.narr(inst, "out", cp.Name, cp.BankName, short), Counterparty: cp, SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "nip"}}
	out = append(out, Line{Key: s.key(a.Key), Account: a.Key, Amount: -fee, BookedAt: t.Add(time.Second), Narration: g.narr(inst, "fee", short), SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "charge"})
	out = append(out, Line{Key: s.key(a.Key), Account: a.Key, Amount: -vat, BookedAt: t.Add(2 * time.Second), Narration: g.narr(inst, "vat", short), SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "charge"})
	if stamp > 0 {
		out = append(out, Line{Key: s.key(a.Key), Account: a.Key, Amount: -stamp, BookedAt: t.Add(3 * time.Second), Narration: g.narr(inst, "stamp", short), SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "charge"})
	}
	return out
}

func (s *scn) reversal(a Account, lines []Line, t time.Time) []Line {
	inst := s.w.institutions[a.Institution]
	g := &dayGen{w: s.w, d: clock.DateOf(t), rng: s.rng}
	out := []Line{}
	for i, l := range lines {
		out = append(out, Line{Key: s.key(a.Key), Account: a.Key, Amount: -l.Amount, BookedAt: t.Add(time.Duration(i) * time.Second), ValueDate: clock.DateOf(l.BookedAt), Narration: g.narr(inst, "rev", l.Narration), Counterparty: l.Counterparty, SessionID: l.SessionID, BankRef: g.bankRef(inst, t), Channel: "reversal"})
	}
	return out
}

func (s *scn) cp() *Counterparty {
	g := &dayGen{rng: s.rng}
	return g.counterparty("")
}

func (w *World) RunScenario(name string, at time.Time, p ScenarioParams) (string, error) {
	s := w.newScn(name, at)
	acct, hasAcct := w.Account(p.Account)
	needAcct := func() error {
		if !hasAcct {
			return fmt.Errorf("scenario %s needs an account", name)
		}
		return nil
	}
	switch name {
	case "debit_then_reversal", "late_reversal":
		if err := needAcct(); err != nil {
			return "", err
		}
		amt := int64(2500000)
		debit := s.transfer(acct, amt, at, s.cp(), "out")
		gap := 2 * time.Hour
		if name == "late_reversal" {
			gap = 30 * time.Hour
		}
		all := append(debit, s.reversal(acct, debit, at.Add(gap))...)
		return fmt.Sprintf("debit at %s, reversal at %s", at.Format(time.RFC3339), at.Add(gap).Format(time.RFC3339)), w.Inject(at, all)
	case "fee_vat_levy_lines":
		if err := needAcct(); err != nil {
			return "", err
		}
		return "transfer of ₦75,000 with fee, VAT and stamp duty", w.Inject(at, s.transfer(acct, 7500000, at, s.cp(), "out"))
	case "identical_same_day_fees":
		if err := needAcct(); err != nil {
			return "", err
		}
		inst := w.institutions[acct.Institution]
		g := &dayGen{w: w, d: clock.DateOf(at), rng: s.rng}
		narr := g.narr(inst, "fee", "")
		lines := []Line{
			{Key: s.key(acct.Key), Account: acct.Key, Amount: -5000, BookedAt: at, Narration: strings.TrimSpace(narr), BankRef: "", Channel: "charge"},
			{Key: s.key(acct.Key), Account: acct.Key, Amount: -5000, BookedAt: at.Add(time.Minute), Narration: strings.TrimSpace(narr), BankRef: "", Channel: "charge"},
		}
		return "two identical ₦50 fee lines", w.Inject(at, lines)
	case "pending_amount_change":
		if err := needAcct(); err != nil {
			return "", err
		}
		lines := s.transfer(acct, 1500000, at.Add(3*time.Hour), s.cp(), "out")
		lines[0].PendingAt = at
		lines[0].PendingAmount = -1450000
		lines[0].ValueDate = clock.DateOf(at)
		return "pending ₦14,500 now, posts as ₦15,000 in 3 hours", w.Inject(at, lines)
	case "own_account_transfer_across_banks":
		pid := p.Persona
		if pid == "" && hasAcct {
			pid = acct.Owner
		}
		if pid == "" {
			pid = "adaeze"
		}
		from, to := Account{}, Account{}
		for _, a := range w.AccountsAt(pid, "bank-a") {
			if a.Type == "current" {
				from = a
			}
		}
		for _, a := range w.AccountsAt(pid, "bank-b") {
			if a.Type == "current" {
				to = a
			}
		}
		if from.Key == "" || to.Key == "" {
			return "", fmt.Errorf("persona %s needs current accounts at both banks", pid)
		}
		toInst := w.institutions["bank-b"]
		fromInst := w.institutions["bank-a"]
		cp := &Counterparty{Name: strings.ToUpper(w.personas[pid].Name), AccountNumber: to.NUBAN, BankCode: toInst.NIPCode, BankName: toInst.Name}
		lines := s.transfer(from, 10000000, at, cp, "own")
		g := &dayGen{w: w, d: clock.DateOf(at), rng: s.rng}
		sid := lines[0].SessionID
		back := &Counterparty{Name: cp.Name, AccountNumber: from.NUBAN, BankCode: fromInst.NIPCode, BankName: fromInst.Name}
		lines = append(lines, Line{Key: s.key(to.Key), Account: to.Key, Amount: 10000000, BookedAt: at.Add(4 * time.Second), Narration: g.narr(toInst, "in", back.Name, back.BankName, sid[len(sid)-12:]), Counterparty: back, SessionID: sid, BankRef: g.bankRef(toInst, at), Channel: "nip"})
		return "₦100,000 from Bank A to Bank B, session " + sid, w.Inject(at, lines)
	case "renarration":
		if err := needAcct(); err != nil {
			return "", err
		}
		n := p.Count
		if n <= 0 {
			n = 3
		}
		ls, err := w.Fetch(acct.Key, at, clock.DateOf(at).AddDays(-30))
		if err != nil {
			return "", err
		}
		done := 0
		for i := len(ls) - 1; i >= 0 && done < n; i-- {
			if ls[i].Pending {
				continue
			}
			w.AddMutation(Mutation{Key: ls[i].Key, Account: acct.Key, Field: "narration", Value: ls[i].Narration + " /RENARRATED", EffectiveAt: at})
			done++
		}
		return fmt.Sprintf("renarrated %d lines", done), nil
	case "unstable_ids":
		inst := p.Institution
		if inst == "" && hasAcct {
			inst = acct.Institution
		}
		if _, ok := w.institutions[inst]; !ok {
			return "", fmt.Errorf("scenario %s needs an institution", name)
		}
		w.SetUnstable(inst, at)
		return inst + " references now change every business day", nil
	case "bank_outage":
		inst := p.Institution
		if inst == "" && hasAcct {
			inst = acct.Institution
		}
		if _, ok := w.institutions[inst]; !ok {
			return "", fmt.Errorf("scenario %s needs an institution", name)
		}
		d := p.Duration
		if d <= 0 {
			d = 2 * time.Hour
		}
		w.AddOutage(Outage{Institution: inst, From: at, To: at.Add(d)})
		return fmt.Sprintf("%s down until %s", inst, at.Add(d).Format(time.RFC3339)), nil
	}
	return "", fmt.Errorf("unknown scenario %q", name)
}
