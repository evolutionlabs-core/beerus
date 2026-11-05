package world

import (
	"cmp"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	retailMemberPercent            = 50
	retailMemberOf                 = 100
	retailBusinessInflows          = 4.5
	retailBusinessOutflows         = 2.6
	retailPersonalOutflow          = 1.05
	retailQuietDayDivisor          = 3
	retailSettleLaterShare         = 0.08
	retailReversedShare            = 0.12
	retailSettleMinSeconds         = 2 * 3600
	retailSettleSpanSeconds        = 18 * 3600
	retailReverseMinSeconds        = 300
	retailReverseSpanSeconds       = 30 * 3600
	retailSweepDivisor             = 10
	retailInflowMinNaira           = 2000
	retailInflowMaxNaira           = 250000
	retailInflowStepNaira          = 50
	retailOutflowMinNaira          = 1000
	retailOutflowMaxNaira          = 400000
	retailOutflowStepNaira         = 100
	retailInflowFromHour           = 7
	retailInflowToHour             = 22
	retailOutflowFromHour          = 7
	retailOutflowToHour            = 23
	retailSweepFromHour            = 18
	retailSweepToHour              = 19
	retailSessionSpace             = 1000000000000
	retailPayDay                   = 25
	retailSalary             int64 = 45000000
	retailEmployer                 = "EKO LOGISTICS"
	retailMinTransfer        int64 = 100000
	retailFitStep            int64 = 5000
	retailSweepStep          int64 = 100000
	retailPhonePrefix              = "080"
	retailPhoneSpace               = 100000000
	retailDOBSpanDays              = 14610
	retailMonthEndHour             = 23
)

var retailDOBEarliest = clock.Date{Y: 1960, M: time.January, D: 1}

var knownTemplates = []string{TemplateRetail}

type rosterAccount struct {
	typ     string
	name    string
	opening int64
}

type rosterPersona struct {
	id       string
	name     string
	username string
	password string
	phone    string
	dob      clock.Date
	business bool
	rate     float64
	accounts []rosterAccount
}

func retailRoster(value int64) []rosterPersona {
	w := New(Config{Seed: value, Institutions: DefaultInstitutions()}, Overrides{})
	phoneBase := hash64("phone", fmt.Sprint(value)) % retailPhoneSpace
	out := []rosterPersona{}
	for i, id := range w.order {
		p := w.personas[id]
		rp := rosterPersona{
			id:       p.ID,
			name:     p.Name,
			username: p.Username,
			password: p.Password,
			phone:    fmt.Sprintf("%s%08d", retailPhonePrefix, (phoneBase+uint64(i))%retailPhoneSpace),
			dob:      retailDOBEarliest.AddDays(int(hash64("dob", fmt.Sprint(value), p.ID) % retailDOBSpanDays)),
			business: p.Business,
			rate:     p.rate,
		}
		for _, k := range p.Accounts {
			a := w.accounts[k]
			if slices.ContainsFunc(rp.accounts, func(r rosterAccount) bool { return r.typ == a.Type }) {
				continue
			}
			rp.accounts = append(rp.accounts, rosterAccount{typ: a.Type, name: a.Name, opening: a.Opening})
		}
		out = append(out, rp)
	}
	return out
}

func retailMember(value int64, persona, bank string) bool {
	return hash64("member", fmt.Sprint(value), persona, bank)%retailMemberOf < retailMemberPercent
}

func accountName(inst *Institution, name string) string {
	if inst.Style == "digital" {
		return titleCase(name)
	}
	return strings.ToUpper(name)
}

func payday(d clock.Date, cal *clock.Calendar) bool {
	p := clock.Date{Y: d.Y, M: d.M, D: retailPayDay}
	for !cal.IsBusinessDay(p) {
		p = p.AddDays(-1)
	}
	return p == d
}

type seedAcct struct {
	key  string
	typ  string
	name string
	st   *genState
}

type seedRun struct {
	out    *Template
	bank   *Institution
	banks  []Institution
	p      rosterPersona
	seed   uint64
	now    time.Time
	cal    *clock.Calendar
	prefix string
	accts  []*seedAcct
	rng    *rand.Rand
	d      clock.Date
	n      int
}

func generateRetail(roster []rosterPersona, value int64, bank Institution, banks []Institution, historyDays int, now time.Time, cal *clock.Calendar) Template {
	var out Template
	today := clock.DateOf(now)
	start := today.AddDays(-historyDays)
	for _, p := range roster {
		if !retailMember(value, p.id, bank.ID) {
			continue
		}
		prefix := fmt.Sprintf("seed:%s:%d:%s:%s", TemplateRetail, value, bank.ID, p.id)
		out.Customers = append(out.Customers, SeedCustomer{Key: prefix, Bank: bank.ID, Name: p.name, Phone: p.phone, DOB: p.dob, Username: p.username, Password: p.password})
		r := &seedRun{out: &out, bank: &bank, banks: banks, p: p, seed: hash64(TemplateRetail, fmt.Sprint(value)), now: now, cal: cal, prefix: prefix}
		for _, a := range p.accounts {
			key := prefix + ":" + a.typ
			name := accountName(&bank, a.name)
			out.Accounts = append(out.Accounts, SeedAccount{Key: key, Bank: bank.ID, Customer: prefix, Product: a.typ, Name: name, Opening: a.opening, OpenedAt: start.Start()})
			r.accts = append(r.accts, &seedAcct{key: key, typ: a.typ, name: name, st: &genState{projected: a.opening}})
		}
		for d := start; d.Before(today); d = d.AddDays(1) {
			r.day(d)
		}
	}
	slices.SortStableFunc(out.Postings, func(a, b SeedPosting) int {
		return cmp.Or(a.PostedAt.Compare(b.PostedAt), strings.Compare(a.Key, b.Key))
	})
	return out
}

func (r *seedRun) day(d clock.Date) {
	r.d, r.n = d, 0
	r.rng = rand.New(rand.NewPCG(r.seed, hash64(r.bank.ID, r.p.id, d.String())))
	for _, a := range r.accts {
		a.st.prune(d.Start())
	}
	scale := r.p.rate
	if !r.cal.IsBusinessDay(d) {
		scale /= retailQuietDayDivisor
	}
	trading := []*seedAcct{}
	for _, a := range r.accts {
		if a.typ == "current" || !r.p.business {
			trading = append(trading, a)
		}
	}
	isPayday := payday(d, r.cal)
	switch {
	case len(trading) == 0:
	case r.p.business:
		nIn := int(r.rng.Float64() * retailBusinessInflows * scale)
		for range nIn {
			r.inflow(trading[r.rng.IntN(len(trading))], r.amount(retailInflowMinNaira, retailInflowMaxNaira, retailInflowStepNaira), false)
		}
		nOut := int(r.rng.Float64() * retailBusinessOutflows * scale)
		for range nOut {
			r.outflow(trading[r.rng.IntN(len(trading))])
		}
		if isPayday {
			r.sweep()
		}
	default:
		if isPayday {
			r.inflow(trading[0], retailSalary, true)
		}
		if r.rng.Float64() < retailPersonalOutflow*scale {
			r.outflow(trading[0])
		}
	}
	if d.AddDays(1).D == 1 {
		r.monthEnd()
	}
}

func (r *seedRun) at(minHour, maxHour int) time.Time {
	span := (maxHour - minHour) * 3600
	return r.d.Start().Add(time.Duration(minHour*3600+r.rng.IntN(span)) * time.Second)
}

func (r *seedRun) amount(lo, hi, step int) int64 {
	v := lo + r.rng.IntN(hi-lo+1)
	v = max(v/step*step, lo)
	return int64(v) * 100
}

func (r *seedRun) counterpartyName() string {
	return counterpartyNames[r.rng.IntN(len(counterpartyNames))]
}

func (r *seedRun) counterpartyBank(key string) *Institution {
	var best *Institution
	var bestScore uint64
	for i := range r.banks {
		b := &r.banks[i]
		score := hash64("counterparty", key, b.ID)
		if best == nil || score > bestScore || (score == bestScore && b.ID < best.ID) {
			best, bestScore = b, score
		}
	}
	return best
}

func (r *seedRun) transfer(dir, name string, cpBank *Institution, short string) (string, string) {
	if cpBank.ID != r.bank.ID {
		return r.narr(dir, name, cpBank.Name, short), "nip"
	}
	if r.bank.Style == "digital" {
		return r.narr(dir, name), "internal"
	}
	if dir == "in" {
		return "TRF FRM " + strings.ToUpper(name), "internal"
	}
	return "TRF TO " + strings.ToUpper(name), "internal"
}

func (r *seedRun) nextKey(a *seedAcct, kind string) string {
	return fmt.Sprintf("%s:%s:%03d:%s", a.key, r.d, r.n+1, kind)
}

func (r *seedRun) sessionRef() string {
	return fmt.Sprintf("%012d", r.rng.Uint64N(retailSessionSpace))
}

func (r *seedRun) line(a *seedAcct, kind string, amount int64, at time.Time, narration, channel, category, cp, cpBank string) SeedPosting {
	r.n++
	return SeedPosting{
		Key:              fmt.Sprintf("%s:%s:%03d:%s", a.key, r.d, r.n, kind),
		Bank:             r.bank.ID,
		Account:          a.key,
		PostedAt:         at,
		ValueDate:        clock.DateOf(at),
		Amount:           amount,
		Narration:        narration,
		Channel:          channel,
		Category:         category,
		Counterparty:     cp,
		CounterpartyBank: cpBank,
	}
}

func (r *seedRun) post(a *seedAcct, l SeedPosting) {
	r.out.Postings = append(r.out.Postings, l)
	a.st.projected += l.Amount
	if l.Amount > 0 {
		a.st.credits = append(a.st.credits, credit{l.PostedAt, l.Amount})
	}
	if l.Channel != "charge" {
		a.st.alerts++
	}
	if l.Amount < 0 && (l.Channel == "nip" || l.Channel == "internal") {
		a.st.debitTurn += -l.Amount
	}
}

func (r *seedRun) narr(kind string, args ...string) string {
	return (&dayGen{}).narr(r.bank, kind, args...)
}

func (r *seedRun) inflow(a *seedAcct, amt int64, salary bool) {
	t := r.at(retailInflowFromHour, retailInflowToHour)
	cp := r.counterpartyName()
	short := r.sessionRef()
	if salary {
		cpBank := r.counterpartyBank(r.nextKey(a, "salary"))
		_, channel := r.transfer("in", retailEmployer, cpBank, short)
		r.post(a, r.line(a, "salary", amt, t, r.narr("salary", retailEmployer), channel, "", retailEmployer, cpBank.NIPCode))
		return
	}
	cpBank := r.counterpartyBank(r.nextKey(a, "in"))
	narration, channel := r.transfer("in", cp, cpBank, short)
	r.post(a, r.line(a, "in", amt, t, narration, channel, "", cp, cpBank.NIPCode))
	if levy := charges.EMTL(amt, r.d); levy > 0 {
		r.post(a, r.line(a, "emtl", -levy, t.Add(time.Second), r.narr("emtl", short), "charge", charges.CategoryLevy, "", ""))
	}
}

func (r *seedRun) fit(a *seedAcct, amt int64, at time.Time) int64 {
	avail := a.st.projected - a.st.laterCredits(at)
	total := charges.ForTransfer(amt, r.d, false).Total()
	if avail-amt-total >= buffer {
		return amt
	}
	amt = (avail - buffer - total) / retailFitStep * retailFitStep
	if amt < retailMinTransfer {
		return 0
	}
	return amt
}

func (r *seedRun) outflow(a *seedAcct) {
	t := r.at(retailOutflowFromHour, retailOutflowToHour)
	amt := r.fit(a, r.amount(retailOutflowMinNaira, retailOutflowMaxNaira, retailOutflowStepNaira), t)
	if amt == 0 {
		return
	}
	cp := r.counterpartyName()
	roll := r.rng.Float64()
	short := r.sessionRef()
	tr := charges.ForTransfer(amt, r.d, false)
	cpBank := r.counterpartyBank(r.nextKey(a, "out"))
	narration, channel := r.transfer("out", cp, cpBank, short)
	lines := []SeedPosting{
		r.line(a, "out", -amt, t, narration, channel, "", cp, cpBank.NIPCode),
		r.line(a, "fee", -tr.Fee, t.Add(time.Second), r.narr("fee", short), "charge", charges.CategoryFee, "", ""),
		r.line(a, "vat", -tr.VAT, t.Add(2*time.Second), r.narr("vat", short), "charge", charges.CategoryVAT, "", ""),
	}
	if tr.StampDuty > 0 {
		lines = append(lines, r.line(a, "stamp", -tr.StampDuty, t.Add(3*time.Second), r.narr("stamp", short), "charge", charges.CategoryStamp, "", ""))
	}
	last := time.Duration(len(lines)-1) * time.Second
	switch {
	case roll < retailSettleLaterShare:
		settled := t.Add(time.Duration(retailSettleMinSeconds+r.rng.IntN(retailSettleSpanSeconds)) * time.Second)
		if settled.Add(last).Before(r.now) {
			for i := range lines {
				lines[i].PostedAt = settled.Add(time.Duration(i) * time.Second)
				lines[i].ValueDate = clock.DateOf(t)
			}
		}
		r.postAll(a, lines)
	case roll < retailReversedShare:
		r.postAll(a, lines)
		back := t.Add(time.Duration(retailReverseMinSeconds+r.rng.IntN(retailReverseSpanSeconds)) * time.Second)
		if !back.Add(last).Before(r.now) {
			return
		}
		for i, l := range lines {
			rev := l
			rev.Key = l.Key + ":rev"
			rev.Amount = -l.Amount
			rev.PostedAt = back.Add(time.Duration(i) * time.Second)
			rev.ValueDate = clock.DateOf(t)
			rev.Narration = r.narr("rev", l.Narration)
			rev.Channel = "reversal"
			r.post(a, rev)
		}
	default:
		r.postAll(a, lines)
	}
}

func (r *seedRun) postAll(a *seedAcct, lines []SeedPosting) {
	for _, l := range lines {
		r.post(a, l)
	}
}

func (r *seedRun) account(typ string) *seedAcct {
	for _, a := range r.accts {
		if a.typ == typ {
			return a
		}
	}
	return nil
}

func (r *seedRun) sweep() {
	from, to := r.account("current"), r.account("savings")
	if from == nil || to == nil {
		return
	}
	t := r.at(retailSweepFromHour, retailSweepToHour)
	avail := from.st.projected - from.st.laterCredits(t)
	amt := avail / retailSweepDivisor / retailSweepStep * retailSweepStep
	if amt < retailMinTransfer || avail-amt < buffer {
		return
	}
	r.post(from, r.line(from, "sweep", -amt, t, r.narr("sweep"), "internal", "", to.name, r.bank.NIPCode))
	r.post(to, r.line(to, "sweepin", amt, t.Add(time.Second), r.narr("sweepin"), "internal", "", from.name, r.bank.NIPCode))
}

func (r *seedRun) monthEnd() {
	month := r.d.Start().Format("Jan 2006")
	for i, a := range r.accts {
		st := a.st
		t := r.d.Start().Add(retailMonthEndHour*time.Hour + time.Duration(i*10)*time.Minute)
		if st.alerts > 0 {
			sms := min(int64(st.alerts)*charges.SMSAlertFee, charges.SMSMonthlyCap)
			if st.projected-st.laterCredits(t) >= sms+charges.VAT(sms) {
				r.post(a, r.line(a, "sms", -sms, t, r.narr("sms", month), "charge", charges.CategoryFee, "", ""))
				r.post(a, r.line(a, "smsvat", -charges.VAT(sms), t.Add(time.Second), r.narr("smsvat", month), "charge", charges.CategoryVAT, "", ""))
			}
		}
		if a.typ == "current" && st.debitTurn > 0 {
			fee := (st.debitTurn*charges.MaintenancePerMille + 500) / 1000
			room := (st.projected - st.laterCredits(t)) * 10000 / (10000 + charges.VATRateBasisPoints)
			fee = min(fee, room)
			if fee > 0 {
				r.post(a, r.line(a, "maint", -fee, t.Add(2*time.Second), r.narr("maint", month), "charge", charges.CategoryFee, "", ""))
				r.post(a, r.line(a, "maintvat", -charges.VAT(fee), t.Add(3*time.Second), r.narr("maintvat", month), "charge", charges.CategoryVAT, "", ""))
			}
		}
		st.alerts = 0
		st.debitTurn = 0
	}
}
