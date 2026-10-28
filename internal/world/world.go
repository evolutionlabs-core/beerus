package world

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type Institution struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	NIPCode     string `json:"nip_code"`
	NUBANCode   string `json:"nuban_code"`
	HistoryDays int    `json:"history_days"`
	StableIDs   bool   `json:"stable_ids"`
	Style       string `json:"style"`
}

type Account struct {
	Key         string `json:"key"`
	Institution string `json:"institution"`
	NUBAN       string `json:"nuban"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Currency    string `json:"currency"`
	Opening     int64  `json:"opening_minor"`
	Owner       string `json:"owner"`
}

type Persona struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	Name     string   `json:"name"`
	Business bool     `json:"business"`
	Accounts []string `json:"accounts"`
	rate     float64
}

type Counterparty struct {
	Name          string `json:"name"`
	AccountNumber string `json:"account_number"`
	BankCode      string `json:"bank_code"`
	BankName      string `json:"bank_name"`
}

type Line struct {
	Key           string        `json:"key"`
	Account       string        `json:"account"`
	Amount        int64         `json:"amount_minor"`
	PendingAmount int64         `json:"pending_amount_minor"`
	PendingAt     time.Time     `json:"pending_at"`
	BookedAt      time.Time     `json:"booked_at"`
	ValueDate     clock.Date    `json:"value_date"`
	Narration     string        `json:"narration"`
	Counterparty  *Counterparty `json:"counterparty"`
	SessionID     string        `json:"session_id"`
	BankRef       string        `json:"bank_ref"`
	Channel       string        `json:"channel"`
	order         int
	BalanceAfter  int64 `json:"-"`
	PendingSeq    int64 `json:"-"`
	PostedSeq     int64 `json:"-"`
}

func (l *Line) HasPending() bool {
	return !l.PendingAt.IsZero()
}

type Mutation struct {
	Key         string    `json:"key"`
	Account     string    `json:"account"`
	Field       string    `json:"field"`
	Value       string    `json:"value"`
	EffectiveAt time.Time `json:"effective_at"`
}

type Outage struct {
	Institution string    `json:"institution"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
}

type Overrides struct {
	Injected   []Line               `json:"injected"`
	Mutations  []Mutation           `json:"mutations"`
	Outages    []Outage             `json:"outages"`
	UnstableAt map[string]time.Time `json:"unstable_at"`
}

type Config struct {
	Seed    int64      `json:"seed"`
	Genesis clock.Date `json:"genesis"`
}

type World struct {
	mu           sync.Mutex
	cfg          Config
	institutions map[string]*Institution
	accounts     map[string]*Account
	personas     map[string]*Persona
	order        []string
	lines        map[string][]*Line
	through      clock.Date
	state        map[string]*genState
	ov           Overrides
	injectedSet  map[string]bool
}

type credit struct {
	at     time.Time
	amount int64
}

type genState struct {
	projected int64
	alerts    int
	debitTurn int64
	credits   []credit
}

func (st *genState) laterCredits(t time.Time) int64 {
	var sum int64
	for _, c := range st.credits {
		if c.at.After(t) {
			sum += c.amount
		}
	}
	return sum
}

func (st *genState) prune(before time.Time) {
	keep := st.credits[:0]
	for _, c := range st.credits {
		if c.at.After(before) {
			keep = append(keep, c)
		}
	}
	st.credits = keep
}

func ID(prefix string, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:])
	return prefix + "_" + strings.ToLower(enc[:20])
}

func hash64(parts ...string) uint64 {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return binary.BigEndian.Uint64(h[:8])
}

func NUBAN(bankCode string, serial uint64) string {
	s := fmt.Sprintf("%09d", serial%1000000000)
	digits := bankCode + s
	w := []int{3, 7, 3, 3, 7, 3, 3, 7, 3, 3, 7, 3}
	sum := 0
	for i, c := range digits {
		sum += int(c-'0') * w[i]
	}
	check := (10 - sum%10) % 10
	return s + fmt.Sprint(check)
}

func ValidNUBAN(bankCode, nuban string) bool {
	if len(nuban) != 10 {
		return false
	}
	return nubanCheck(bankCode, nuban[:9]) == nuban
}

func nubanCheck(bankCode, serial string) string {
	digits := bankCode + serial
	w := []int{3, 7, 3, 3, 7, 3, 3, 7, 3, 3, 7, 3}
	sum := 0
	for i, c := range digits {
		sum += int(c-'0') * w[i]
	}
	return serial + fmt.Sprint((10-sum%10)%10)
}

func New(cfg Config, ov Overrides) *World {
	w := &World{
		cfg:          cfg,
		institutions: map[string]*Institution{},
		accounts:     map[string]*Account{},
		personas:     map[string]*Persona{},
		lines:        map[string][]*Line{},
		state:        map[string]*genState{},
		ov:           ov,
	}
	if w.ov.UnstableAt == nil {
		w.ov.UnstableAt = map[string]time.Time{}
	}
	w.institutions["bank-a"] = &Institution{ID: "bank-a", Name: "Bank A", NIPCode: "999001", NUBANCode: "901", HistoryDays: 730, StableIDs: true, Style: "traditional"}
	w.institutions["bank-b"] = &Institution{ID: "bank-b", Name: "Bank B", NIPCode: "999002", NUBANCode: "902", HistoryDays: 180, StableIDs: true, Style: "digital"}
	add := func(p *Persona, accts ...Account) {
		for _, a := range accts {
			inst := w.institutions[a.Institution]
			a.NUBAN = NUBAN(inst.NUBANCode, hash64(fmt.Sprint(cfg.Seed), p.ID, a.Institution, a.Type))
			a.Key = a.Institution + ":" + a.NUBAN
			a.Currency = "NGN"
			a.Owner = p.ID
			acct := a
			w.accounts[a.Key] = &acct
			p.Accounts = append(p.Accounts, a.Key)
		}
		p.Password = "beerus-" + ID("pw", fmt.Sprint(cfg.Seed), p.ID)[3:11]
		w.personas[p.ID] = p
		w.order = append(w.order, p.ID)
	}
	add(&Persona{ID: "adaeze", Username: "adaeze", Name: "Adaeze's Bakery Ltd", Business: true, rate: 1},
		Account{Institution: "bank-a", Name: "ADAEZE'S BAKERY LTD", Type: "current", Opening: 125000000},
		Account{Institution: "bank-a", Name: "ADAEZE'S BAKERY LTD SAVINGS", Type: "savings", Opening: 300000000},
		Account{Institution: "bank-b", Name: "Adaeze's Bakery Ltd", Type: "current", Opening: 45000000})
	add(&Persona{ID: "tunde", Username: "tunde", Name: "Tunde Motors Ltd", Business: true, rate: 0.7},
		Account{Institution: "bank-a", Name: "TUNDE MOTORS LTD", Type: "current", Opening: 480000000})
	add(&Persona{ID: "ngozi", Username: "ngozi", Name: "Ngozi Fabrics Ventures", Business: true, rate: 0.8},
		Account{Institution: "bank-b", Name: "Ngozi Fabrics Ventures", Type: "current", Opening: 32000000})
	add(&Persona{ID: "chidi", Username: "chidi", Name: "Chidi Okafor", Business: false, rate: 0.4},
		Account{Institution: "bank-b", Name: "Chidi Okafor", Type: "savings", Opening: 8500000})
	for k, a := range w.accounts {
		w.state[k] = &genState{projected: a.Opening}
	}
	w.injectedSet = map[string]bool{}
	for _, l := range w.ov.Injected {
		w.injectedSet[l.Key] = true
	}
	w.through = cfg.Genesis.AddDays(-1)
	return w
}

func (w *World) Config() Config {
	return w.cfg
}

func (w *World) Institutions() []Institution {
	out := []Institution{}
	for _, id := range []string{"bank-a", "bank-b"} {
		out = append(out, *w.institutions[id])
	}
	return out
}

func (w *World) Institution(id string) (Institution, bool) {
	i, ok := w.institutions[id]
	if !ok {
		return Institution{}, false
	}
	return *i, true
}

func (w *World) Account(key string) (Account, bool) {
	a, ok := w.accounts[key]
	if !ok {
		return Account{}, false
	}
	return *a, true
}

func (w *World) Personas() []Persona {
	out := []Persona{}
	for _, id := range w.order {
		out = append(out, *w.personas[id])
	}
	return out
}

func (w *World) Persona(id string) (Persona, bool) {
	p, ok := w.personas[id]
	if !ok {
		return Persona{}, false
	}
	return *p, true
}

func (w *World) PersonaByUsername(u string) (Persona, bool) {
	for _, p := range w.personas {
		if p.Username == u {
			return *p, true
		}
	}
	return Persona{}, false
}

func (w *World) AccountsAt(personaID, institution string) []Account {
	p, ok := w.personas[personaID]
	if !ok {
		return nil
	}
	out := []Account{}
	for _, k := range p.Accounts {
		if a := w.accounts[k]; a.Institution == institution {
			out = append(out, *a)
		}
	}
	return out
}

func (w *World) Overrides() Overrides {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := Overrides{UnstableAt: map[string]time.Time{}}
	cp.Injected = append(cp.Injected, w.ov.Injected...)
	cp.Mutations = append(cp.Mutations, w.ov.Mutations...)
	cp.Outages = append(cp.Outages, w.ov.Outages...)
	for k, v := range w.ov.UnstableAt {
		cp.UnstableAt[k] = v
	}
	return cp
}

func (w *World) Down(institution string, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, o := range w.ov.Outages {
		if o.Institution == institution && !at.Before(o.From) && at.Before(o.To) {
			return true
		}
	}
	return false
}

func (w *World) AddOutage(o Outage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ov.Outages = append(w.ov.Outages, o)
}

func (w *World) SetUnstable(institution string, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.ov.UnstableAt[institution]; !ok {
		w.ov.UnstableAt[institution] = at
	}
}

func (w *World) AddMutation(m Mutation) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ov.Mutations = append(w.ov.Mutations, m)
}

func (w *World) Inject(at time.Time, lines []Line) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range lines {
		l := lines[i]
		if w.injectedSet[l.Key] {
			return fmt.Errorf("world: line %s already injected", l.Key)
		}
		if _, ok := w.accounts[l.Account]; !ok {
			return fmt.Errorf("world: unknown account %s", l.Account)
		}
	}
	for i := range lines {
		l := lines[i]
		w.injectedSet[l.Key] = true
		w.ov.Injected = append(w.ov.Injected, l)
		if !DateOf(l.firstAppear()).After(w.through) {
			cp := l
			cp.order = 1 << 30
			w.lines[l.Account] = append(w.lines[l.Account], &cp)
		}
	}
	w.finish()
	return nil
}

func DateOf(t time.Time) clock.Date {
	return clock.DateOf(t)
}

func (w *World) ensure(through clock.Date) {
	if !w.through.Before(through) {
		return
	}
	for d := w.through.AddDays(1); !d.After(through); d = d.AddDays(1) {
		w.generateDay(d)
		for _, l := range w.ov.Injected {
			if DateOf(l.firstAppear()) == d {
				cp := l
				cp.order = 1 << 30
				w.lines[l.Account] = append(w.lines[l.Account], &cp)
			}
		}
	}
	w.through = through
	w.finish()
}

func (l Line) firstAppear() time.Time {
	if l.HasPending() {
		return l.PendingAt
	}
	return l.BookedAt
}

func (w *World) finish() {
	for key, ls := range w.lines {
		acct := w.accounts[key]
		sort.SliceStable(ls, func(i, j int) bool {
			if !ls[i].BookedAt.Equal(ls[j].BookedAt) {
				return ls[i].BookedAt.Before(ls[j].BookedAt)
			}
			return ls[i].order < ls[j].order
		})
		bal := acct.Opening
		for _, l := range ls {
			bal += l.Amount
			l.BalanceAfter = bal
		}
		type ver struct {
			at    time.Time
			order int
			l     *Line
			pend  bool
		}
		vs := make([]ver, 0, len(ls)+8)
		for _, l := range ls {
			if l.HasPending() {
				vs = append(vs, ver{l.PendingAt, l.order, l, true})
			}
			vs = append(vs, ver{l.BookedAt, l.order, l, false})
		}
		sort.SliceStable(vs, func(i, j int) bool {
			if !vs[i].at.Equal(vs[j].at) {
				return vs[i].at.Before(vs[j].at)
			}
			if vs[i].order != vs[j].order {
				return vs[i].order < vs[j].order
			}
			return vs[i].pend && !vs[j].pend
		})
		for i, v := range vs {
			if v.pend {
				v.l.PendingSeq = int64(i + 1)
			} else {
				v.l.PostedSeq = int64(i + 1)
			}
		}
		w.lines[key] = ls
	}
}
