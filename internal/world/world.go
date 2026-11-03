package world

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
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
	Type        string `json:"type"`
	HistoryDays int    `json:"history_days"`
	StableIDs   bool   `json:"stable_ids"`
	Style       string `json:"style"`
}

type Account struct {
	Key         string    `json:"key"`
	Institution string    `json:"institution"`
	NUBAN       string    `json:"nuban"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Currency    string    `json:"currency"`
	Opening     int64     `json:"opening_minor"`
	Owner       string    `json:"owner"`
	OpenedAt    time.Time `json:"opened_at,omitempty"`
}

type OpenedAccount struct {
	Persona     string    `json:"persona"`
	Institution string    `json:"institution"`
	Type        string    `json:"type"`
	At          time.Time `json:"at"`
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

type Closure struct {
	Institution string    `json:"institution"`
	At          time.Time `json:"at"`
}

type Move struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount_minor"`
	Payout bool   `json:"payout,omitempty"`
}

type Overrides struct {
	Injected   []Line               `json:"injected"`
	Mutations  []Mutation           `json:"mutations"`
	Outages    []Outage             `json:"outages"`
	Suspended  []Outage             `json:"suspended,omitempty"`
	Closed     []Closure            `json:"closed,omitempty"`
	Opened     []OpenedAccount      `json:"opened,omitempty"`
	Purged     []Institution        `json:"purged,omitempty"`
	UnstableAt map[string]time.Time `json:"unstable_at"`
}

type Config struct {
	Seed         int64           `json:"seed"`
	Genesis      clock.Date      `json:"genesis"`
	Institutions []Institution   `json:"-"`
	Calendar     *clock.Calendar `json:"-"`
}

func DefaultInstitutions() []Institution {
	return []Institution{
		{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "999001", NUBANCode: "901", HistoryDays: 730, StableIDs: true, Style: "traditional"},
		{ID: "bank-b", Name: "Bank B", Type: "digital", NIPCode: "999002", NUBANCode: "902", HistoryDays: 180, StableIDs: true, Style: "digital"},
	}
}

type World struct {
	mu           sync.Mutex
	cfg          Config
	institutions map[string]*Institution
	instOrder    []string
	purged       map[string]bool
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
	return nubanCheck(bankCode, fmt.Sprintf("%09d", serial%1000000000))
}

func ValidNUBAN(bankCode, nuban string) bool {
	if len(nuban) != 10 {
		return false
	}
	return nubanCheck(bankCode, nuban[:9]) == nuban
}

func nubanCheck(bankCode, serial string) string {
	switch len(bankCode) {
	case 3:
		bankCode = "000" + bankCode
	case 5:
		bankCode = "9" + bankCode
	}
	digits := bankCode + serial
	w := []int{3, 7, 3, 3, 7, 3, 3, 7, 3, 3, 7, 3, 3, 7, 3}
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
		purged:       map[string]bool{},
		accounts:     map[string]*Account{},
		personas:     map[string]*Persona{},
		lines:        map[string][]*Line{},
		state:        map[string]*genState{},
		ov:           ov,
	}
	if w.ov.UnstableAt == nil {
		w.ov.UnstableAt = map[string]time.Time{}
	}
	insts := cfg.Institutions
	if len(insts) == 0 && len(ov.Purged) == 0 {
		insts = DefaultInstitutions()
	}
	for _, in := range insts {
		inst := in
		w.institutions[inst.ID] = &inst
		w.instOrder = append(w.instOrder, inst.ID)
	}
	for _, in := range ov.Purged {
		if _, ok := w.institutions[in.ID]; ok {
			continue
		}
		inst := in
		w.institutions[inst.ID] = &inst
		w.purged[inst.ID] = true
	}
	add := func(p *Persona, accts ...Account) {
		for _, a := range accts {
			inst, ok := w.institutions[a.Institution]
			if !ok {
				continue
			}
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
	add(&Persona{ID: "kemi", Username: "kemi", Name: "Kemi Beauty Lounge", Business: true, rate: 0.5},
		Account{Institution: "bank-b", Name: "Kemi Beauty Lounge", Type: "current", Opening: 18000000})
	add(&Persona{ID: "emeka", Username: "emeka", Name: "Emeka Auto Parts Nig Ltd", Business: true, rate: 0.7},
		Account{Institution: "bank-a", Name: "EMEKA AUTO PARTS NIG LTD", Type: "current", Opening: 260000000},
		Account{Institution: "bank-a", Name: "EMEKA AUTO PARTS NIG LTD SAVINGS", Type: "savings", Opening: 150000000})
	add(&Persona{ID: "fatima", Username: "fatima", Name: "Fatima Spices and Grains", Business: true, rate: 0.4},
		Account{Institution: "bank-a", Name: "FATIMA SPICES AND GRAINS", Type: "current", Opening: 54000000})
	add(&Persona{ID: "bola", Username: "bola", Name: "Bola Print Works", Business: true, rate: 0.5},
		Account{Institution: "bank-a", Name: "BOLA PRINT WORKS", Type: "current", Opening: 96000000},
		Account{Institution: "bank-b", Name: "Bola Print Works", Type: "current", Opening: 22000000})
	add(&Persona{ID: "ifeanyi", Username: "ifeanyi", Name: "Ifeanyi Pharmacy Ltd", Business: true, rate: 0.6},
		Account{Institution: "bank-a", Name: "IFEANYI PHARMACY LTD", Type: "current", Opening: 175000000})
	add(&Persona{ID: "sade", Username: "sade", Name: "Sade Event Planners", Business: true, rate: 0.4},
		Account{Institution: "bank-b", Name: "Sade Event Planners", Type: "current", Opening: 27000000})
	add(&Persona{ID: "musa", Username: "musa", Name: "Musa Cattle Traders", Business: true, rate: 0.45},
		Account{Institution: "bank-a", Name: "MUSA CATTLE TRADERS", Type: "current", Opening: 310000000})
	add(&Persona{ID: "nkechi", Username: "nkechi", Name: "Nkechi Cosmetics Hub", Business: true, rate: 0.4},
		Account{Institution: "bank-b", Name: "Nkechi Cosmetics Hub", Type: "current", Opening: 15000000})
	add(&Persona{ID: "yemi", Username: "yemi", Name: "Yemi Tech Solutions Ltd", Business: true, rate: 0.7},
		Account{Institution: "bank-a", Name: "YEMI TECH SOLUTIONS LTD", Type: "current", Opening: 420000000},
		Account{Institution: "bank-a", Name: "YEMI TECH SOLUTIONS LTD SAVINGS", Type: "savings", Opening: 500000000},
		Account{Institution: "bank-b", Name: "Yemi Tech Solutions Ltd", Type: "current", Opening: 80000000})
	add(&Persona{ID: "hauwa", Username: "hauwa", Name: "Hauwa Fresh Farms", Business: true, rate: 0.4},
		Account{Institution: "bank-a", Name: "HAUWA FRESH FARMS", Type: "current", Opening: 66000000})
	add(&Persona{ID: "obinna", Username: "obinna", Name: "Obinna Eze", Business: false, rate: 0.2},
		Account{Institution: "bank-a", Name: "OBINNA EZE", Type: "savings", Opening: 12000000})
	add(&Persona{ID: "amina", Username: "amina", Name: "Amina Sule", Business: false, rate: 0.15},
		Account{Institution: "bank-b", Name: "Amina Sule", Type: "savings", Opening: 6500000})
	add(&Persona{ID: "segun", Username: "segun", Name: "Segun Adebisi", Business: false, rate: 0.25},
		Account{Institution: "bank-a", Name: "SEGUN ADEBISI", Type: "current", Opening: 9500000})
	add(&Persona{ID: "ruth", Username: "ruth", Name: "Ruth Akpan", Business: false, rate: 0.15},
		Account{Institution: "bank-b", Name: "Ruth Akpan", Type: "savings", Opening: 4200000})
	add(&Persona{ID: "kunle", Username: "kunle", Name: "Kunle Balogun", Business: false, rate: 0.2},
		Account{Institution: "bank-b", Name: "Kunle Balogun", Type: "savings", Opening: 15500000})
	add(&Persona{ID: "ifeoma", Username: "ifeoma", Name: "Ifeoma Nwachukwu", Business: false, rate: 0.15},
		Account{Institution: "bank-a", Name: "IFEOMA NWACHUKWU", Type: "savings", Opening: 7800000})
	for _, o := range w.ov.Opened {
		_, _ = w.openLocked(o)
	}
	for k, a := range w.accounts {
		w.state[k] = &genState{projected: a.Opening}
	}
	w.injectedSet = map[string]bool{}
	for i := range w.ov.Injected {
		if w.ov.Injected[i].ValueDate.IsZero() {
			w.ov.Injected[i].ValueDate = clock.DateOf(w.ov.Injected[i].BookedAt)
		}
		w.injectedSet[w.ov.Injected[i].Key] = true
	}
	w.through = cfg.Genesis.AddDays(-1)
	return w
}

func (w *World) Config() Config {
	return w.cfg
}

func (w *World) Institutions() []Institution {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []Institution{}
	for _, id := range w.instOrder {
		if !w.purged[id] {
			out = append(out, *w.institutions[id])
		}
	}
	return out
}

func (w *World) Purged(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.purged[id]
}

func (w *World) PurgedInstitutions() []Institution {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Institution{}, w.ov.Purged...)
}

func (w *World) Purge(institution string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	inst := w.institutions[institution]
	switch {
	case inst == nil:
		return fmt.Errorf("unknown bank %q", institution)
	case w.purged[institution] || slices.ContainsFunc(w.ov.Purged, func(p Institution) bool { return p.ID == institution }):
		return fmt.Errorf("%s is already purged", inst.Name)
	}
	if _, ok := w.closedSinceLocked(institution); !ok {
		return fmt.Errorf("%s is not closed; close it before purging it", inst.Name)
	}
	next := make(map[string]bool, len(w.purged)+1)
	for k, v := range w.purged {
		next[k] = v
	}
	next[institution] = true
	w.purged = next
	order := []string{}
	for _, id := range w.instOrder {
		if id != institution {
			order = append(order, id)
		}
	}
	w.instOrder = order
	w.ov.Purged = append(w.ov.Purged, *inst)
	return nil
}

func (w *World) Institution(id string) (Institution, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
		out = append(out, w.visible(w.personas[id]))
	}
	return out
}

func (w *World) Persona(id string) (Persona, bool) {
	p, ok := w.personas[id]
	if !ok {
		return Persona{}, false
	}
	return w.visible(p), true
}

func (w *World) visible(p *Persona) Persona {
	w.mu.Lock()
	purged := w.purged
	w.mu.Unlock()
	cp := *p
	if len(purged) == 0 {
		return cp
	}
	cp.Accounts = []string{}
	for _, k := range p.Accounts {
		if !purged[w.accounts[k].Institution] {
			cp.Accounts = append(cp.Accounts, k)
		}
	}
	return cp
}

func (w *World) PersonaByUsername(u string) (Persona, bool) {
	for _, p := range w.personas {
		if p.Username == u {
			return w.visible(p), true
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
	if w.Purged(institution) {
		return out
	}
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
	cp.Suspended = append(cp.Suspended, w.ov.Suspended...)
	cp.Closed = append(cp.Closed, w.ov.Closed...)
	cp.Opened = append(cp.Opened, w.ov.Opened...)
	cp.Purged = append(cp.Purged, w.ov.Purged...)
	for k, v := range w.ov.UnstableAt {
		cp.UnstableAt[k] = v
	}
	return cp
}

func (w *World) injectedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.ov.Injected)
}

func (w *World) AddInstitution(inst Institution) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.institutions[inst.ID]; ok {
		return nil
	}
	for _, other := range w.institutions {
		if other.NIPCode == inst.NIPCode || other.NUBANCode == inst.NUBANCode || other.Name == inst.Name {
			return fmt.Errorf("world: %s clashes with %s", inst.ID, other.ID)
		}
	}
	cp := inst
	next := make(map[string]*Institution, len(w.institutions)+1)
	for k, v := range w.institutions {
		next[k] = v
	}
	next[inst.ID] = &cp
	w.institutions = next
	w.instOrder = append(w.instOrder[:len(w.instOrder):len(w.instOrder)], inst.ID)
	return nil
}

var (
	ErrBankSuspended   = errors.New("bank suspended")
	ErrBankClosed      = errors.New("bank closed")
	ErrBankUnavailable = errors.New("bank unavailable")
)

func (w *World) Suspend(institution string, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.suspendedAt(institution, at) {
		return false
	}
	w.ov.Suspended = append(w.ov.Suspended, Outage{Institution: institution, From: at})
	w.reset()
	return true
}

func (w *World) Resume(institution string, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := false
	for i := range w.ov.Suspended {
		s := &w.ov.Suspended[i]
		if s.Institution == institution && !at.Before(s.From) && (s.To.IsZero() || at.Before(s.To)) {
			s.To = at
			changed = true
		}
	}
	if changed {
		w.reset()
	}
	return changed
}

func (w *World) Suspended(institution string, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.suspendedAt(institution, at)
}

func (w *World) OutageEnd(institution string, at time.Time) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.outageAt(institution, at)
}

func (w *World) Closed(institution string, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closedAt(institution, at)
}

func (w *World) ClosedSince(institution string) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closedSinceLocked(institution)
}

func (w *World) closedAt(institution string, at time.Time) bool {
	for _, c := range w.ov.Closed {
		if c.Institution == institution && !at.Before(c.At) {
			return true
		}
	}
	return false
}

func (w *World) Close(institution string, at time.Time, moves []Move) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	inst := w.institutions[institution]
	if inst == nil {
		return fmt.Errorf("unknown bank %q", institution)
	}
	if _, ok := w.closedSinceLocked(institution); ok {
		return fmt.Errorf("%w: %s is already closed", ErrBankClosed, inst.Name)
	}
	lines := []Line{}
	for i, m := range moves {
		from, to := w.accounts[m.From], w.accounts[m.To]
		if from == nil || to == nil || from.Institution != institution || to.Institution == institution || m.Amount <= 0 {
			return fmt.Errorf("world: bad closing move %+v", m)
		}
		toInst := w.institutions[to.Institution]
		owner := strings.ToUpper(w.personas[from.Owner].Name)
		outText, inText := closingText(inst, toInst, owner, m.Payout)
		out := Line{Key: fmt.Sprintf("close:%s:%d:out", institution, i), Account: from.Key, Amount: -m.Amount, BookedAt: at, Channel: "nip", Narration: outText,
			Counterparty: &Counterparty{Name: owner, AccountNumber: to.NUBAN, BankCode: toInst.NIPCode, BankName: toInst.Name}}
		in := Line{Key: fmt.Sprintf("close:%s:%d:in", institution, i), Account: to.Key, Amount: m.Amount, BookedAt: at, Channel: "nip", Narration: inText,
			Counterparty: &Counterparty{Name: owner, AccountNumber: from.NUBAN, BankCode: inst.NIPCode, BankName: inst.Name}}
		if m.Payout {
			out.Counterparty.Name = "NIGERIA DEPOSIT INSURANCE CORPORATION"
			in.Counterparty.Name = "NIGERIA DEPOSIT INSURANCE CORPORATION"
		}
		lines = append(lines, out, in)
	}
	w.ov.Closed = append(w.ov.Closed, Closure{Institution: institution, At: at})
	w.reset()
	return w.injectLocked(lines)
}

func closingText(from, to *Institution, owner string, payout bool) (string, string) {
	out, in := "NIP/BANK CLOSURE/TO "+strings.ToUpper(to.Name)+"/"+owner, "NIP/BANK CLOSURE/FRM "+strings.ToUpper(from.Name)+"/"+owner
	if payout {
		out, in = "NDIC INSURED DEPOSIT PAYOUT", "NDIC PAYOUT/"+strings.ToUpper(from.Name)+" INSURED DEPOSIT"
	}
	if from.Style == "digital" {
		out = "Bank closure transfer to " + to.Name
		if payout {
			out = "NDIC insured deposit payout"
		}
	}
	if to.Style == "digital" {
		in = "Transfer from " + from.Name + " (bank closure)"
		if payout {
			in = "NDIC payout for your " + from.Name + " deposit"
		}
	}
	return out, in
}

func (w *World) closedSinceLocked(institution string) (time.Time, bool) {
	for _, c := range w.ov.Closed {
		if c.Institution == institution {
			return c.At, true
		}
	}
	return time.Time{}, false
}

func (w *World) suspendedAt(institution string, at time.Time) bool {
	if w.closedAt(institution, at) {
		return true
	}
	for _, s := range w.ov.Suspended {
		if s.Institution == institution && !at.Before(s.From) && (s.To.IsZero() || at.Before(s.To)) {
			return true
		}
	}
	return false
}

func (w *World) outageAt(institution string, at time.Time) (time.Time, bool) {
	end, down := time.Time{}, false
	for _, o := range w.ov.Outages {
		if o.Institution == institution && !at.Before(o.From) && at.Before(o.To) && o.To.After(end) {
			end, down = o.To, true
		}
	}
	return end, down
}

func (w *World) OpenAccount(persona, institution, typ string, at time.Time) (Account, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closedAt(institution, at) {
		return Account{}, fmt.Errorf("%w: the bank is closed, so it cannot open accounts", ErrBankClosed)
	}
	if w.suspendedAt(institution, at) {
		return Account{}, fmt.Errorf("%w: the bank is suspended by the CBN, so it cannot open accounts", ErrBankSuspended)
	}
	o := OpenedAccount{Persona: persona, Institution: institution, Type: typ, At: at}
	a, err := w.openLocked(o)
	if err != nil {
		return Account{}, err
	}
	w.ov.Opened = append(w.ov.Opened, o)
	w.reset()
	return a, nil
}

func (w *World) openLocked(o OpenedAccount) (Account, error) {
	p := w.personas[o.Persona]
	if p == nil {
		return Account{}, fmt.Errorf("unknown persona %q", o.Persona)
	}
	inst := w.institutions[o.Institution]
	if inst == nil {
		return Account{}, fmt.Errorf("unknown bank %q", o.Institution)
	}
	if o.Type != "current" && o.Type != "savings" {
		return Account{}, errors.New("type must be current or savings")
	}
	for _, k := range p.Accounts {
		if a := w.accounts[k]; a.Institution == inst.ID && a.Type == o.Type {
			return Account{}, fmt.Errorf("%s already has a %s account at %s", p.Name, o.Type, inst.Name)
		}
	}
	name := p.Name
	if inst.Style != "digital" {
		name = strings.ToUpper(name)
		if o.Type == "savings" {
			name += " SAVINGS"
		}
	}
	nuban := NUBAN(inst.NUBANCode, hash64(fmt.Sprint(w.cfg.Seed), p.ID, inst.ID, o.Type))
	a := Account{Key: inst.ID + ":" + nuban, Institution: inst.ID, NUBAN: nuban, Name: name, Type: o.Type, Currency: "NGN", Owner: p.ID, OpenedAt: o.At}
	if _, taken := w.accounts[a.Key]; taken {
		return Account{}, fmt.Errorf("account number %s is already taken", nuban)
	}
	w.accounts[a.Key] = &a
	p.Accounts = append(p.Accounts, a.Key)
	w.state[a.Key] = &genState{}
	return a, nil
}

func (w *World) reset() {
	w.lines = map[string][]*Line{}
	w.state = map[string]*genState{}
	for k, a := range w.accounts {
		w.state[k] = &genState{projected: a.Opening}
	}
	w.through = w.cfg.Genesis.AddDays(-1)
}

func (w *World) Down(institution string, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.suspendedAt(institution, at) {
		return true
	}
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
	w.reset()
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
	return w.injectLocked(lines)
}

func (w *World) injectLocked(lines []Line) error {
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
		if l.ValueDate.IsZero() {
			l.ValueDate = clock.DateOf(l.BookedAt)
		}
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
