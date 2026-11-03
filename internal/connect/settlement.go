package connect

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	defaultNIPSessions = 4
	cbnID              = "cbn"
	cbnIssuance        = "ISSUANCE"
	cbnCurrency        = "CURRENCY_IN_CIRCULATION"
	outsideMember      = "OUTSIDE"
	outsideFunding     = 10 * bank.GenesisSettlement
)

var reservedIDs = map[string]bool{cbnID: true, "nibss": true}

type schedule struct {
	per int
}

func (s schedule) every() time.Duration {
	return 24 * time.Hour / time.Duration(s.per)
}

func businessDay(d clock.Date) bool {
	return d.Weekday() != time.Saturday && d.Weekday() != time.Sunday
}

func (s schedule) next(after time.Time) time.Time {
	d := clock.DateOf(after)
	k := int(after.Sub(d.Start())/s.every()) + 1
	for {
		if businessDay(d) && k <= s.per {
			return d.Start().Add(time.Duration(k) * s.every())
		}
		d, k = d.AddDays(1), 1
	}
}

func (s schedule) key(cutoff time.Time) string {
	return cutoff.In(clock.Lagos).Format("2006-01-02T15:04")
}

func (a *App) schedule() schedule {
	n := a.cfg.NIPSessions
	if n <= 0 || 24*time.Hour%time.Duration(n) != 0 {
		n = defaultNIPSessions
	}
	return schedule{per: n}
}

func settlementAccount(member string) string {
	return "SETTLEMENT/" + member
}

type rtgs struct {
	ctx     context.Context
	cfg     *config.Config
	st      *store.Store
	l       *ledger.Ledger
	genesis time.Time
	members map[string]bool
	through time.Time
}

func openRTGS(ctx context.Context, cfg *config.Config, genesis time.Time) (*rtgs, error) {
	name := config.DatabaseName(cfg.Dir, cbnID)
	if err := cfg.Claim("database", name, ""); err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL, name)
	if err != nil {
		return nil, err
	}
	r := &rtgs{ctx: ctx, cfg: cfg, st: st, genesis: genesis}
	if err := r.setup(); err != nil {
		st.Close()
		return nil, err
	}
	return r, nil
}

func (r *rtgs) setup() error {
	r.l = ledger.New(r.st.Pool, nil)
	r.members = map[string]bool{}
	r.through = time.Time{}
	for _, acc := range []ledger.Account{
		{Code: cbnIssuance, Name: "Issuance", Kind: ledger.Asset},
		{Code: cbnCurrency, Name: "Currency in circulation", Kind: ledger.Liability},
	} {
		if _, err := r.l.OpenAccount(r.ctx, acc); err != nil {
			return err
		}
	}
	return r.member(outsideMember, outsideFunding, 0)
}

func (r *rtgs) member(id string, settlement, vault int64) error {
	if r.members[id] {
		return nil
	}
	if _, err := r.l.OpenAccount(r.ctx, ledger.Account{Code: settlementAccount(id), Name: "Settlement account of " + id, Kind: ledger.Liability}); err != nil {
		return err
	}
	lines := []ledger.Line{{Account: cbnIssuance, Amount: settlement + vault, Narration: "Issuance"}, {Account: settlementAccount(id), Amount: -settlement, Narration: "Genesis settlement funding"}}
	if vault > 0 {
		lines = append(lines, ledger.Line{Account: cbnCurrency, Amount: -vault, Narration: "Vault cash issued"})
	}
	at := r.genesis.Add(-time.Hour)
	if _, err := r.l.Post(r.ctx, ledger.Entry{Key: "genesis:" + id, Kind: "genesis", PostingDate: clock.DateOf(at), PostedAt: at, Narration: "Genesis funding of " + id, Lines: lines}); err != nil {
		return err
	}
	r.members[id] = true
	return nil
}

func lastPosted(ctx context.Context, l *ledger.Ledger, kind string, floor time.Time) (time.Time, error) {
	var t sql.NullTime
	if err := l.Pool().QueryRow(ctx, `SELECT max(posted_at) FROM journal_entries WHERE kind = $1`, kind).Scan(&t); err != nil {
		return time.Time{}, err
	}
	if !t.Valid || t.Time.Before(floor) {
		return floor, nil
	}
	return t.Time, nil
}

func (r *rtgs) settledThrough() (time.Time, error) {
	if r.through.IsZero() {
		t, err := lastPosted(r.ctx, r.l, "session", r.genesis)
		if err != nil {
			return t, err
		}
		r.through = t
	}
	return r.through, nil
}

func (s *ledgerSource) settledThrough(floor time.Time) (time.Time, error) {
	if s.settled.IsZero() {
		t, err := lastPosted(s.ctx, s.b.L, "settlement", floor)
		if err != nil {
			return t, err
		}
		s.settled = t
	}
	return s.settled, nil
}

func (r *rtgs) Balance(member string) (int64, error) {
	return r.l.Balance(r.ctx, settlementAccount(member))
}

func (r *rtgs) Snapshot(id string) error {
	name := r.st.Name() + "_snap_" + snapID(id)
	if err := r.cfg.Claim("database_snapshot", name, ""); err != nil {
		return err
	}
	if err := r.st.Snapshot(r.ctx, snapID(id)); err != nil {
		return err
	}
	r.l = ledger.New(r.st.Pool, nil)
	return nil
}

func (r *rtgs) Restore(id string) error {
	ok, err := r.st.HasSnapshot(r.ctx, snapID(id))
	if err != nil {
		return err
	}
	if ok {
		if err := r.st.Restore(r.ctx, snapID(id)); err != nil {
			return err
		}
		return r.setup()
	}
	return r.rebuild()
}

func (r *rtgs) rebuild() error {
	r.st.Close()
	if err := store.DropDatabases(r.ctx, r.st.URL(), []string{r.st.Name()}); err != nil {
		return err
	}
	st, err := store.Open(r.ctx, r.cfg.DatabaseURL, r.st.Name())
	if err != nil {
		return err
	}
	r.st = st
	return r.setup()
}

func (r *rtgs) DropSnapshot(id string) error {
	name := r.st.Name() + "_snap_" + snapID(id)
	if err := store.DropDatabases(r.ctx, r.st.URL(), []string{name}); err != nil {
		return err
	}
	return r.cfg.Release("database_snapshot", name)
}

func (r *rtgs) Drop() error {
	if err := r.st.Drop(r.ctx); err != nil {
		return err
	}
	return r.cfg.Release("database", r.st.Name())
}

func (r *rtgs) Close() {
	r.st.Close()
}

func (a *App) ensureRTGS() {
	if a.rtgs != nil || a.worldOnly || len(a.cfg.Banks) == 0 || time.Since(a.rtgsTried) < 10*time.Second {
		return
	}
	a.rtgsTried = time.Now()
	r, err := openRTGS(context.Background(), a.cfg, a.world.Config().Genesis.Start())
	if err != nil {
		a.log.Error("CBN ledger not ready; retrying", "err", err)
		return
	}
	a.rtgs = r
}

type position struct {
	outward, inward int64
}

func (a *App) settleLocked(now time.Time) error {
	if a.rtgs == nil || len(a.cfg.Banks) == 0 {
		return nil
	}
	sched := a.schedule()
	genesis := a.world.Config().Genesis.Start()
	from, err := a.rtgs.settledThrough()
	if err != nil {
		return err
	}
	for _, l := range a.ledgers {
		t, err := l.settledThrough(genesis)
		if err != nil {
			return err
		}
		if t.Before(from) {
			from = t
		}
	}
	last := time.Time{}
	for c := sched.next(from); !c.After(now); c = sched.next(c) {
		last = c
	}
	if last.IsZero() {
		return nil
	}
	sessions := map[time.Time]map[string]*position{}
	for _, b := range a.cfg.Banks {
		if err := a.rtgs.member(b.ID, bank.GenesisSettlement, bank.GenesisVault); err != nil {
			return err
		}
		for _, acc := range a.world.Accounts() {
			if acc.Institution != b.ID {
				continue
			}
			vs, err := a.world.Versions(acc.Key, from, last)
			if err != nil {
				return err
			}
			for _, v := range vs {
				out, in := bank.NIPAmounts(bank.FeedLine{Key: v.Key, Pending: v.Pending, Amount: v.Amount, Narration: v.Narration, Channel: v.Channel})
				if out == 0 && in == 0 {
					continue
				}
				c := sched.next(v.BookedAt.Add(-time.Nanosecond))
				if sessions[c] == nil {
					sessions[c] = map[string]*position{}
				}
				p := sessions[c][b.ID]
				if p == nil {
					p = &position{}
					sessions[c][b.ID] = p
				}
				p.outward += out
				p.inward += in
			}
		}
	}
	cutoffs := make([]time.Time, 0, len(sessions))
	for c := range sessions {
		cutoffs = append(cutoffs, c)
	}
	sort.Slice(cutoffs, func(i, j int) bool { return cutoffs[i].Before(cutoffs[j]) })
	for _, c := range cutoffs {
		key := sched.key(c)
		ps := sessions[c]
		if c.After(a.rtgs.through) {
			if err := a.rtgs.settle(key, c, ps); err != nil {
				return err
			}
		}
		for _, l := range a.ledgers {
			p := ps[l.inst.ID]
			if p == nil || !c.After(l.settled) {
				continue
			}
			l.mu.Lock()
			_, err := l.b.Settle(l.ctx, key, p.outward, p.inward, c)
			l.mu.Unlock()
			if err != nil {
				return fmt.Errorf("settling %s at %s: %w", key, l.inst.ID, err)
			}
		}
	}
	a.rtgs.through = last
	for _, l := range a.ledgers {
		l.settled = last
	}
	return nil
}

func (r *rtgs) settle(key string, cutoff time.Time, ps map[string]*position) error {
	members := make([]string, 0, len(ps))
	for id := range ps {
		members = append(members, id)
	}
	sort.Strings(members)
	lines := []ledger.Line{}
	var total int64
	for _, id := range members {
		net := ps[id].inward - ps[id].outward
		if net == 0 {
			continue
		}
		total += net
		lines = append(lines, ledger.Line{Account: settlementAccount(id), Amount: -net, Narration: "NIP session " + key})
	}
	if total != 0 {
		lines = append(lines, ledger.Line{Account: settlementAccount(outsideMember), Amount: total, Narration: "NIP session " + key})
	}
	if len(lines) < 2 {
		return nil
	}
	_, err := r.l.Post(r.ctx, ledger.Entry{Key: "session:" + key, Kind: "session", PostingDate: clock.DateOf(cutoff), PostedAt: cutoff, Narration: "NIP settlement session " + key, Lines: lines, AllowOverdraft: true})
	return err
}

type SettlementCheck struct {
	SettledThrough time.Time `json:"settled_through"`
	Sessions       int       `json:"sessions"`
	Problems       []string  `json:"problems"`
}

func (a *App) CheckSettlement() (SettlementCheck, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := SettlementCheck{Problems: []string{}}
	if a.rtgs == nil {
		return out, nil
	}
	now := a.clock.Now()
	for _, l := range a.ledgers {
		if err := l.Sync(now); err != nil {
			return out, err
		}
	}
	if err := a.settleLocked(now); err != nil {
		return out, err
	}
	sched := a.schedule()
	last := a.rtgs.through
	out.SettledThrough = last
	if err := a.rtgs.l.Pool().QueryRow(a.rtgs.ctx, `SELECT count(*) FROM journal_entries WHERE kind = 'session'`).Scan(&out.Sessions); err != nil {
		return out, err
	}
	c, err := a.rtgs.l.Verify(a.rtgs.ctx)
	if err != nil {
		return out, err
	}
	if c.TrialBalance != 0 || len(c.UnbalancedEntries) > 0 {
		out.Problems = append(out.Problems, fmt.Sprintf("the CBN's books do not balance: %d", c.TrialBalance))
	}
	for _, l := range a.ledgers {
		var open position
		for _, acc := range a.world.Accounts() {
			if acc.Institution != l.inst.ID {
				continue
			}
			vs, err := a.world.Versions(acc.Key, last.Add(-24*time.Hour), now)
			if err != nil {
				return out, err
			}
			for _, v := range vs {
				o, i := bank.NIPAmounts(bank.FeedLine{Key: v.Key, Pending: v.Pending, Amount: v.Amount, Narration: v.Narration, Channel: v.Channel})
				if sched.next(v.BookedAt.Add(-time.Nanosecond)).After(last) {
					open.outward += o
					open.inward += i
				}
			}
		}
		owe, err := l.b.L.Balance(l.ctx, bank.NIPOutward)
		if err != nil {
			return out, err
		}
		due, err := l.b.L.Balance(l.ctx, bank.NIPInward)
		if err != nil {
			return out, err
		}
		if owe != open.outward || due != open.inward {
			out.Problems = append(out.Problems, fmt.Sprintf("%s has NIP payable %d and receivable %d, but the unsettled transfers come to %d and %d", l.inst.ID, owe, due, open.outward, open.inward))
		}
		held, err := l.b.L.Balance(l.ctx, bank.Settlement)
		if err != nil {
			return out, err
		}
		rtgsBal, err := a.rtgs.Balance(l.inst.ID)
		if err != nil {
			return out, err
		}
		if held != rtgsBal {
			out.Problems = append(out.Problems, fmt.Sprintf("%s's books show %d at the CBN, but the CBN shows %d", l.inst.ID, held, rtgsBal))
		}
	}
	return out, nil
}
