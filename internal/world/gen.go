package world

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const buffer = 2000000

var counterpartyNames = []string{
	"CHIDI OKAFOR", "FOLAKE ADEYEMI", "IBRAHIM MUSA", "BLESSING EZE", "OLUWASEUN BELLO",
	"HALIMA YUSUF", "EMEKA NWOSU", "TOLU ADEBAYO", "AISHA ABDULLAHI", "KELECHI OBI",
	"GRACE OKON", "YAKUBU DANJUMA", "FUNMI ALADE", "UCHE NNAMDI", "ZAINAB BAKARE",
	"MAMA NKECHI PROVISIONS", "LAGOS FLOUR MILLS DISTRIBUTORS", "ABUJA PACKAGING SUPPLIES", "SUNRISE GAS DEPOT", "EKO LOGISTICS",
}

type cpBank struct {
	code string
	name string
}

var counterpartyBanks = []cpBank{
	{"999001", "Bank A"}, {"999002", "Bank B"}, {"999003", "Bank C"}, {"999004", "Bank D Microfinance"},
}

func NIPFee(amount int64) int64 {
	return charges.NIPFee(amount)
}

func VAT(base int64) int64 {
	return charges.VAT(base)
}

func StampDuty(amount int64, d clock.Date, exempt bool) int64 {
	return charges.StampDuty(amount, d, exempt)
}

func EMTL(amount int64, d clock.Date) int64 {
	return charges.EMTL(amount, d)
}

type dayGen struct {
	w     *World
	d     clock.Date
	rng   *rand.Rand
	p     *Persona
	order int
	n     int
}

func (w *World) generateDay(d clock.Date) {
	base := clock.DaysBetween(w.cfg.Genesis, d) * 100000
	for _, st := range w.state {
		st.prune(d.Start())
	}
	for i, pid := range w.order {
		p := w.personas[pid]
		g := &dayGen{
			w:     w,
			d:     d,
			rng:   rand.New(rand.NewPCG(uint64(w.cfg.Seed), hash64("day", pid, d.String()))),
			p:     p,
			order: base + i*10000,
		}
		g.run()
	}
}

func (g *dayGen) next() int {
	g.order++
	return g.order
}

func (g *dayGen) key(kind string) string {
	g.n++
	return fmt.Sprintf("%s:%s:%03d:%s", g.p.ID, g.d, g.n, kind)
}

func (g *dayGen) at(minHour, maxHour int) time.Time {
	span := (maxHour - minHour) * 3600
	return g.d.Start().Add(time.Duration(minHour*3600+g.rng.IntN(span)) * time.Second)
}

func (g *dayGen) accountOf(inst, typ string) *Account {
	for _, k := range g.p.Accounts {
		a := g.w.accounts[k]
		if (inst == "" || a.Institution == inst) && a.Type == typ {
			return a
		}
	}
	return nil
}

func (g *dayGen) trading() []*Account {
	out := []*Account{}
	for _, k := range g.p.Accounts {
		a := g.w.accounts[k]
		if a.Type == "current" || !g.p.Business {
			out = append(out, a)
		}
	}
	return out
}

func (g *dayGen) add(l *Line) {
	l.order = g.next()
	if l.ValueDate.IsZero() {
		l.ValueDate = clock.DateOf(l.BookedAt)
	}
	g.w.lines[l.Account] = append(g.w.lines[l.Account], l)
	st := g.w.state[l.Account]
	st.projected += l.Amount
	if l.Amount > 0 {
		st.credits = append(st.credits, credit{l.BookedAt, l.Amount})
	}
	st.alerts++
	if l.Amount < 0 && (l.Channel == "nip" || l.Channel == "internal") {
		st.debitTurn += -l.Amount
	}
}

func (g *dayGen) sessionID(sender string, t time.Time) string {
	return sender + t.In(clock.Lagos).Format("060102150405") + fmt.Sprintf("%012d", g.rng.Uint64N(1000000000000))
}

func (g *dayGen) bankRef(inst *Institution, t time.Time) string {
	if inst.Style == "digital" {
		return fmt.Sprintf("%016x", g.rng.Uint64())
	}
	const alnum = "ABCDEFGHJKLMNPQRSTUVWXYZ0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = alnum[g.rng.IntN(len(alnum))]
	}
	return fmt.Sprintf("FT%s%03d%s", t.Format("06"), t.YearDay(), b)
}

func (g *dayGen) counterparty(exclude string) *Counterparty {
	name := counterpartyNames[g.rng.IntN(len(counterpartyNames))]
	bank := counterpartyBanks[g.rng.IntN(len(counterpartyBanks))]
	return &Counterparty{
		Name:          name,
		AccountNumber: fmt.Sprintf("%010d", g.rng.Uint64N(10000000000)),
		BankCode:      bank.code,
		BankName:      bank.name,
	}
}

func (g *dayGen) amount(min, max, step int) int64 {
	v := min + g.rng.IntN(max-min+1)
	v = v / step * step
	if v < min {
		v = min
	}
	return int64(v) * 100
}

func (g *dayGen) run() {
	weekend := g.d.Weekday() == time.Saturday || g.d.Weekday() == time.Sunday
	scale := g.p.rate
	if weekend {
		scale /= 3
	}
	accts := g.trading()
	if len(accts) == 0 {
		return
	}
	if g.p.Business {
		nIn := int(g.rng.Float64() * 4.5 * scale)
		for i := 0; i < nIn; i++ {
			g.inflow(accts[g.rng.IntN(len(accts))], g.amount(2000, 250000, 50), "")
		}
		nOut := int(g.rng.Float64() * 2.6 * scale)
		for i := 0; i < nOut; i++ {
			g.outflow(accts[g.rng.IntN(len(accts))])
		}
		if g.d.Weekday() == time.Friday {
			g.ownTransfer()
		}
		if g.d.D == 25 {
			g.sweepToSavings()
		}
	} else {
		a := accts[0]
		if g.d.D == 25 {
			g.inflow(a, 45000000, "salary")
		}
		if g.rng.Float64() < 0.35*scale*3 {
			g.outflow(a)
		}
	}
	if g.d.AddDays(1).D == 1 {
		g.monthEnd()
	}
}

func (g *dayGen) narr(inst *Institution, kind string, args ...string) string {
	a := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	if inst.Style == "digital" {
		switch kind {
		case "in":
			return "Transfer from " + titleCase(a(0))
		case "out":
			return "Transfer to " + titleCase(a(0))
		case "fee":
			return "Transfer fee"
		case "vat":
			return "VAT"
		case "stamp":
			return "Stamp duty"
		case "emtl":
			return "Electronic money transfer levy"
		case "rev":
			return "Reversal: " + a(0)
		case "sms":
			return "SMS notification fee " + a(0)
		case "maint":
			return "Account maintenance fee " + a(0)
		case "smsvat":
			return "VAT on SMS notification fee"
		case "maintvat":
			return "VAT on account maintenance fee"
		case "salary":
			return "Salary from " + titleCase(a(0))
		case "sweep":
			return "Transfer to savings"
		case "sweepin":
			return "Transfer from current account"
		}
	}
	switch kind {
	case "in":
		return fmt.Sprintf("NIP TRF FRM %s/%s/%s", a(0), strings.ToUpper(a(1)), a(2))
	case "out":
		return fmt.Sprintf("NIP TRF TO %s/%s/%s", a(0), strings.ToUpper(a(1)), a(2))
	case "fee":
		return "NIP CHARGE " + a(0)
	case "vat":
		return "VAT ON NIP CHARGE " + a(0)
	case "stamp":
		return "STAMP DUTY " + a(0)
	case "emtl":
		return "EMTL " + a(0)
	case "rev":
		return "REV/" + a(0)
	case "sms":
		return "SMS ALERT CHARGES " + strings.ToUpper(a(0))
	case "maint":
		return "ACCOUNT MAINT FEE " + strings.ToUpper(a(0))
	case "smsvat":
		return "VAT ON SMS ALERT CHARGES " + strings.ToUpper(a(0))
	case "maintvat":
		return "VAT ON ACCOUNT MAINT FEE " + strings.ToUpper(a(0))
	case "salary":
		return "SALARY CREDIT " + a(0)
	case "sweep":
		return "TRF TO SAVINGS"
	case "sweepin":
		return "TRF FRM CURRENT ACCT"
	}
	return kind
}

func titleCase(s string) string {
	parts := strings.Fields(strings.ToLower(s))
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func (g *dayGen) inflow(a *Account, amt int64, kind string) {
	inst := g.w.institutions[a.Institution]
	t := g.at(7, 22)
	cp := g.counterparty(inst.NIPCode)
	sid := g.sessionID(cp.BankCode, t)
	ref := g.bankRef(inst, t)
	narr := g.narr(inst, "in", cp.Name, cp.BankName, sid[len(sid)-12:])
	if kind == "salary" {
		cp.Name = "EKO LOGISTICS"
		narr = g.narr(inst, "salary", cp.Name)
	}
	g.add(&Line{Key: g.key("in"), Account: a.Key, Amount: amt, BookedAt: t, Narration: narr, Counterparty: cp, SessionID: sid, BankRef: ref, Channel: "nip"})
	if levy := EMTL(amt, g.d); levy > 0 && kind != "salary" {
		g.add(&Line{Key: g.key("emtl"), Account: a.Key, Amount: -levy, BookedAt: t.Add(time.Second), Narration: g.narr(inst, "emtl", sid[len(sid)-12:]), SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "charge"})
	}
}

func (g *dayGen) charges(amount int64, exempt bool) (fee, vat, stamp int64) {
	t := charges.ForTransfer(amount, g.d, exempt)
	return t.Fee, t.VAT, t.StampDuty
}

func (g *dayGen) fit(a *Account, amt int64, at time.Time, exempt bool) int64 {
	st := g.w.state[a.Key]
	avail := st.projected - st.laterCredits(at)
	for amt >= 100000 {
		fee, vat, stamp := g.charges(amt, exempt)
		if avail-amt-fee-vat-stamp >= buffer {
			return amt
		}
		amt = (avail - buffer - 5000 - VAT(5000) - 5000) / 5000 * 5000
	}
	return 0
}

func (g *dayGen) debitWithCharges(a *Account, amt int64, cp *Counterparty, t time.Time, kind string, exempt bool) (string, []*Line) {
	inst := g.w.institutions[a.Institution]
	sid := g.sessionID(inst.NIPCode, t)
	short := sid[len(sid)-12:]
	fee, vat, stamp := g.charges(amt, exempt)
	main := &Line{Key: g.key(kind), Account: a.Key, Amount: -amt, BookedAt: t, Narration: g.narr(inst, "out", cp.Name, cp.BankName, short), Counterparty: cp, SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "nip"}
	lines := []*Line{main}
	lines = append(lines, &Line{Key: g.key("fee"), Account: a.Key, Amount: -fee, BookedAt: t.Add(time.Second), Narration: g.narr(inst, "fee", short), SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "charge"})
	lines = append(lines, &Line{Key: g.key("vat"), Account: a.Key, Amount: -vat, BookedAt: t.Add(2 * time.Second), Narration: g.narr(inst, "vat", short), SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "charge"})
	if stamp > 0 {
		lines = append(lines, &Line{Key: g.key("stamp"), Account: a.Key, Amount: -stamp, BookedAt: t.Add(3 * time.Second), Narration: g.narr(inst, "stamp", short), SessionID: sid, BankRef: g.bankRef(inst, t), Channel: "charge"})
	}
	return sid, lines
}

func (g *dayGen) outflow(a *Account) {
	t := g.at(7, 23)
	amt := g.fit(a, g.amount(1000, 400000, 100), t, false)
	if amt == 0 {
		return
	}
	inst := g.w.institutions[a.Institution]
	cp := g.counterparty(inst.NIPCode)
	roll := g.rng.Float64()
	_, lines := g.debitWithCharges(a, amt, cp, t, "out", false)
	switch {
	case roll < 0.08:
		post := t.Add(time.Duration(2*3600+g.rng.IntN(18*3600)) * time.Second)
		lines[0].PendingAt = t
		lines[0].PendingAmount = lines[0].Amount
		if g.rng.Float64() < 0.25 {
			lines[0].PendingAmount = lines[0].Amount - int64(1+g.rng.IntN(5))*10000
		}
		for i, l := range lines {
			l.BookedAt = post.Add(time.Duration(i) * time.Second)
			l.ValueDate = clock.DateOf(t)
		}
		for _, l := range lines {
			g.add(l)
		}
	case roll < 0.12:
		for _, l := range lines {
			g.add(l)
		}
		back := t.Add(time.Duration(300+g.rng.IntN(30*3600)) * time.Second)
		for i, l := range lines {
			g.add(&Line{
				Key:          l.Key + ":rev",
				Account:      a.Key,
				Amount:       -l.Amount,
				BookedAt:     back.Add(time.Duration(i) * time.Second),
				ValueDate:    clock.DateOf(t),
				Narration:    g.narr(inst, "rev", l.Narration),
				Counterparty: l.Counterparty,
				SessionID:    l.SessionID,
				BankRef:      g.bankRef(inst, back),
				Channel:      "reversal",
			})
		}
	default:
		for _, l := range lines {
			g.add(l)
		}
	}
}

func (g *dayGen) ownTransfer() {
	from := g.accountOf("bank-a", "current")
	to := g.accountOf("bank-b", "current")
	if from == nil || to == nil {
		return
	}
	t := g.at(15, 18)
	amt := g.fit(from, g.amount(50000, 300000, 5000), t, false)
	if amt == 0 {
		return
	}
	toInst := g.w.institutions[to.Institution]
	fromInst := g.w.institutions[from.Institution]
	cp := &Counterparty{Name: strings.ToUpper(g.p.Name), AccountNumber: to.NUBAN, BankCode: toInst.NIPCode, BankName: toInst.Name}
	sid, lines := g.debitWithCharges(from, amt, cp, t, "own", false)
	for _, l := range lines {
		g.add(l)
	}
	back := &Counterparty{Name: strings.ToUpper(g.p.Name), AccountNumber: from.NUBAN, BankCode: fromInst.NIPCode, BankName: fromInst.Name}
	g.add(&Line{Key: g.key("ownin"), Account: to.Key, Amount: amt, BookedAt: t.Add(4 * time.Second), Narration: g.narr(toInst, "in", back.Name, back.BankName, sid[len(sid)-12:]), Counterparty: back, SessionID: sid, BankRef: g.bankRef(toInst, t), Channel: "nip"})
}

func (g *dayGen) sweepToSavings() {
	from := g.accountOf("bank-a", "current")
	to := g.accountOf("bank-a", "savings")
	if from == nil || to == nil {
		return
	}
	st := g.w.state[from.Key]
	t := g.at(18, 19)
	avail := st.projected - st.laterCredits(t)
	amt := avail / 10 / 100000 * 100000
	if amt < 100000 || avail-amt < buffer {
		return
	}
	inst := g.w.institutions[from.Institution]
	ref := g.bankRef(inst, t)
	g.add(&Line{Key: g.key("sweep"), Account: from.Key, Amount: -amt, BookedAt: t, Narration: g.narr(inst, "sweep"), Counterparty: &Counterparty{Name: to.Name, AccountNumber: to.NUBAN, BankCode: inst.NIPCode, BankName: inst.Name}, BankRef: ref, Channel: "internal"})
	g.add(&Line{Key: g.key("sweepin"), Account: to.Key, Amount: amt, BookedAt: t.Add(time.Second), Narration: g.narr(inst, "sweepin"), Counterparty: &Counterparty{Name: from.Name, AccountNumber: from.NUBAN, BankCode: inst.NIPCode, BankName: inst.Name}, BankRef: ref, Channel: "internal"})
}

func (g *dayGen) monthEnd() {
	month := g.d.Start().Format("Jan 2006")
	for i, k := range g.p.Accounts {
		a := g.w.accounts[k]
		inst := g.w.institutions[a.Institution]
		st := g.w.state[k]
		t := g.d.Start().Add(23*time.Hour + time.Duration(i*10)*time.Minute)
		if st.alerts > 0 && st.projected-st.laterCredits(t) > 60000 {
			sms := int64(st.alerts) * 400
			if sms > 50000 {
				sms = 50000
			}
			g.add(&Line{Key: g.key("sms"), Account: k, Amount: -sms, BookedAt: t, Narration: g.narr(inst, "sms", month), BankRef: g.bankRef(inst, t), Channel: "charge"})
			g.add(&Line{Key: g.key("smsvat"), Account: k, Amount: -VAT(sms), BookedAt: t.Add(time.Second), Narration: g.narr(inst, "smsvat", month), BankRef: g.bankRef(inst, t), Channel: "charge"})
		}
		if a.Type == "current" && st.debitTurn > 0 {
			fee := (st.debitTurn + 500) / 1000
			room := (st.projected - st.laterCredits(t)) * 1000 / 1075
			if fee > room {
				fee = room
			}
			if fee > 0 {
				g.add(&Line{Key: g.key("maint"), Account: k, Amount: -fee, BookedAt: t.Add(2 * time.Second), Narration: g.narr(inst, "maint", month), BankRef: g.bankRef(inst, t), Channel: "charge"})
				g.add(&Line{Key: g.key("maintvat"), Account: k, Amount: -VAT(fee), BookedAt: t.Add(3 * time.Second), Narration: g.narr(inst, "maintvat", month), BankRef: g.bankRef(inst, t), Channel: "charge"})
			}
		}
		st.alerts = 0
		st.debitTurn = 0
	}
}
