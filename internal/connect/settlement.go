package connect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	cbnID          = "cbn"
	cbnIssuance    = "ISSUANCE"
	cbnCurrency    = "CURRENCY_IN_CIRCULATION"
	outsideMember  = "OUTSIDE"
	outsideFunding = 10 * bank.GenesisSettlement
)

var reservedIDs = map[string]bool{cbnID: true, "nibss": true}

type schedule struct {
	per int
	cal *clock.Calendar
}

func (s schedule) every() time.Duration {
	return 24 * time.Hour / time.Duration(s.per)
}

func (s schedule) next(after time.Time) time.Time {
	d := clock.DateOf(after)
	k := int(after.Sub(d.Start())/s.every()) + 1
	for {
		if s.cal.IsBusinessDay(d) && k <= s.per {
			return d.Start().Add(time.Duration(k) * s.every())
		}
		d, k = d.AddDays(1), 1
	}
}

func (s schedule) prev(at time.Time) time.Time {
	d := clock.DateOf(at)
	k := int(at.Sub(d.Start()) / s.every())
	for {
		if s.cal.IsBusinessDay(d) && k >= 1 {
			return d.Start().Add(time.Duration(k) * s.every())
		}
		d, k = d.AddDays(-1), s.per
	}
}

func (s schedule) key(cutoff time.Time) string {
	return cutoff.In(clock.Lagos).Format("2006-01-02T15:04")
}

func (a *App) schedule() schedule {
	return schedule{per: a.cfg.Sessions(), cal: a.cfg.Calendar()}
}

func settlementAccount(member string) string {
	return "SETTLEMENT/" + member
}

type rtgs struct {
	ctx     context.Context
	cfg     *config.Config
	ctl     *ctlStore
	st      *store.Store
	l       *ledger.Ledger
	genesis time.Time
	members map[string]bool
	through time.Time
}

func openRTGS(ctx context.Context, cfg *config.Config, ctl *ctlStore, genesis time.Time) (*rtgs, error) {
	name := config.DatabaseName(cfg.Dir, cbnID)
	if err := ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: ownedDatabase, Name: name}); err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL, name)
	if err != nil {
		return nil, err
	}
	r := &rtgs{ctx: ctx, cfg: cfg, ctl: ctl, st: st, genesis: genesis}
	if err := r.setup(); err != nil {
		st.Close()
		return nil, err
	}
	return r, nil
}

var errSessions = errors.New("nip_sessions changed")

func (r *rtgs) keepSchedule() error {
	pool := r.st.Pool
	if _, err := pool.Exec(r.ctx, `CREATE TABLE IF NOT EXISTS nip_schedule (only_row boolean PRIMARY KEY DEFAULT true CHECK (only_row), sessions integer NOT NULL)`); err != nil {
		return err
	}
	if _, err := pool.Exec(r.ctx, `INSERT INTO nip_schedule (sessions) VALUES ($1) ON CONFLICT DO NOTHING`, r.cfg.Sessions()); err != nil {
		return err
	}
	var kept int
	if err := pool.QueryRow(r.ctx, `SELECT sessions FROM nip_schedule`).Scan(&kept); err != nil {
		return err
	}
	if kept != r.cfg.Sessions() {
		return fmt.Errorf("%w: the CBN has settled with %d sessions a day, so nip_sessions must stay %d", errSessions, kept, kept)
	}
	return nil
}

func (r *rtgs) setup() error {
	if err := r.keepSchedule(); err != nil {
		return err
	}
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
	if err := r.ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: ownedSnapshot, Name: name}); err != nil {
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
	return r.ctl.Release(setup.Owned{Kind: ownedSnapshot, Name: name})
}

func (r *rtgs) Drop() error {
	if err := r.st.Drop(r.ctx); err != nil {
		return err
	}
	return r.ctl.Release(setup.Owned{Kind: ownedDatabase, Name: r.st.Name()})
}

func (r *rtgs) Close() {
	r.st.Close()
}

func (a *App) ensureRTGS() error {
	if a.rtgs != nil || a.worldOnly || len(a.cfg.Banks) == 0 || time.Since(a.rtgsTried) < 10*time.Second {
		return nil
	}
	a.rtgsTried = time.Now()
	r, err := openRTGS(context.Background(), a.cfg, a.ctl, a.world.Config().Genesis.Start())
	if errors.Is(err, errSessions) {
		return err
	}
	if err != nil {
		a.log.Error("CBN ledger not ready; retrying", "err", err)
		return nil
	}
	a.rtgs = r
	return nil
}

type position struct {
	outward, inward int64
}

func nipAmounts(v world.Presented) (int64, int64, error) {
	return bank.NIPAmounts(bank.FeedLine{Key: v.Key, Pending: v.Pending, Amount: v.Amount, Channel: v.Channel, Category: v.Category})
}

func (a *App) accountsByBank() map[string][]world.Account {
	out := map[string][]world.Account{}
	for _, acc := range a.world.Accounts() {
		out[acc.Institution] = append(out[acc.Institution], acc)
	}
	return out
}

func (a *App) openPositionLocked(bankID string, after, now time.Time) (position, error) {
	sched := a.schedule()
	since := sched.prev(after)
	var open position
	for _, acc := range a.world.Accounts() {
		if acc.Institution != bankID {
			continue
		}
		vs, err := a.world.Versions(acc.Key, since, now)
		if err != nil {
			return open, err
		}
		for _, v := range vs {
			o, i, err := nipAmounts(v)
			if err != nil {
				return open, err
			}
			if (o != 0 || i != 0) && sched.next(v.BookedAt.Add(-time.Nanosecond)).After(after) {
				open.outward += o
				open.inward += i
			}
		}
	}
	return open, nil
}

func (a *App) settleLocked(now time.Time) error {
	for a.settling {
		a.settleDone.Wait()
	}
	if a.rtgs == nil || len(a.cfg.Banks) == 0 {
		return nil
	}
	a.settling = true
	defer func() {
		a.settling = false
		a.settleDone.Broadcast()
	}()
	r := a.rtgs
	ledgers := slices.Clone(a.ledgers)
	banks := make([]string, 0, len(a.cfg.Banks))
	for _, b := range a.cfg.Banks {
		banks = append(banks, b.ID)
	}
	sched := a.schedule()
	genesis := a.world.Config().Genesis.Start()
	a.mu.Unlock()
	from, err := r.settledThrough()
	for _, l := range ledgers {
		if err != nil {
			break
		}
		var t time.Time
		if t, err = l.settledThrough(genesis); err == nil && t.Before(from) {
			from = t
		}
	}
	if err == nil {
		for _, id := range banks {
			if err = r.member(id, bank.GenesisSettlement, bank.GenesisVault); err != nil {
				break
			}
		}
	}
	a.mu.Lock()
	if err != nil {
		return err
	}
	last := time.Time{}
	for c := sched.next(from); !c.After(now); c = sched.next(c) {
		last = c
	}
	if last.IsZero() {
		return nil
	}
	sessions, err := a.sessionPositions(sched, banks, from, last)
	if err != nil {
		return err
	}
	a.mu.Unlock()
	err = settleSessions(r, ledgers, sched, sessions)
	a.mu.Lock()
	if err != nil {
		return err
	}
	r.through = last
	for _, l := range ledgers {
		l.settled = last
	}
	return nil
}

func (a *App) sessionPositions(sched schedule, banks []string, from, last time.Time) (map[time.Time]map[string]*position, error) {
	sessions := map[time.Time]map[string]*position{}
	accounts := a.accountsByBank()
	for _, id := range banks {
		for _, acc := range accounts[id] {
			vs, err := a.world.Versions(acc.Key, from, last)
			if err != nil {
				return nil, err
			}
			for _, v := range vs {
				out, in, err := nipAmounts(v)
				if err != nil {
					return nil, err
				}
				if out == 0 && in == 0 {
					continue
				}
				c := sched.next(v.BookedAt.Add(-time.Nanosecond))
				if sessions[c] == nil {
					sessions[c] = map[string]*position{}
				}
				p := sessions[c][id]
				if p == nil {
					p = &position{}
					sessions[c][id] = p
				}
				p.outward += out
				p.inward += in
			}
		}
	}
	return sessions, nil
}

func settleSessions(r *rtgs, ledgers []*ledgerSource, sched schedule, sessions map[time.Time]map[string]*position) error {
	cutoffs := make([]time.Time, 0, len(sessions))
	for c := range sessions {
		cutoffs = append(cutoffs, c)
	}
	sort.Slice(cutoffs, func(i, j int) bool { return cutoffs[i].Before(cutoffs[j]) })
	for _, c := range cutoffs {
		key := sched.key(c)
		ps := sessions[c]
		if c.After(r.through) {
			if err := r.settle(key, c, ps); err != nil {
				return err
			}
		}
		for _, l := range ledgers {
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
	accounts := a.accountsByBank()
	genesis := a.world.Config().Genesis.Start()
	for _, l := range a.ledgers {
		open, err := a.openPositionLocked(l.inst.ID, last, now)
		if err != nil {
			return out, err
		}
		var worldNet int64
		for _, acc := range accounts[l.inst.ID] {
			vs, err := a.world.Versions(acc.Key, genesis.Add(-time.Nanosecond), now)
			if err != nil {
				return out, err
			}
			for _, v := range vs {
				if !v.Pending && (v.Channel == "nip" || (v.Channel == "reversal" && v.Category == "")) {
					worldNet += v.Amount
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
		var settledNet int64
		if err := l.b.L.Pool().QueryRow(l.ctx, `SELECT coalesce(sum(jl.amount), 0) FROM journal_lines jl JOIN journal_entries e ON e.id = jl.entry_id JOIN gl_accounts a ON a.id = jl.account_id WHERE e.kind = 'settlement' AND a.code = $1`, bank.Settlement).Scan(&settledNet); err != nil {
			return out, err
		}
		if booked := settledNet + due - owe; booked != worldNet {
			out.Problems = append(out.Problems, fmt.Sprintf("%s's customers moved %d through NIP, but its books settled and owe %d", l.inst.ID, worldNet, booked))
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
