package connect

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	cbnID       = "cbn"
	cbnIssuance = "ISSUANCE"
	cbnCurrency = "CURRENCY_IN_CIRCULATION"
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
	mmu     sync.Mutex
	ctx     context.Context
	cfg     *config.Config
	ctl     *ctlStore
	st      *store.Store
	l       *ledger.Ledger
	genesis time.Time
	members map[string]bool
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
	if err := ctl.update(func(s *setup.Status) (bool, error) {
		res := ensureResource(s, setup.KindSystem, setup.SystemCBN)
		if res.Phase == setup.PhaseActive {
			return false, nil
		}
		res.Phase = setup.PhaseActive
		return true, nil
	}); err != nil {
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
		return fmt.Errorf("%w: the CBN has settled with %s a day, so nip_sessions must stay %d", errSessions, plural(kept, "session", "sessions"), kept)
	}
	return nil
}

func (r *rtgs) setup() error {
	if err := r.keepSchedule(); err != nil {
		return err
	}
	r.l = ledger.New(r.st.Pool, nil)
	r.members = map[string]bool{}
	for _, acc := range []ledger.Account{
		{Code: cbnIssuance, Name: "Issuance", Kind: ledger.Asset},
		{Code: cbnCurrency, Name: "Currency in circulation", Kind: ledger.Liability},
	} {
		if _, err := r.l.OpenAccount(r.ctx, acc); err != nil {
			return err
		}
	}
	return nil
}

func (r *rtgs) member(id string, settlement, vault int64) error {
	r.mmu.Lock()
	defer r.mmu.Unlock()
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

const rtgsRetry = 10 * time.Second

func (a *App) ensureRTGS() error {
	a.mu.Lock()
	if a.rtgs != nil || a.rtgsOpening || len(a.cfg.Banks) == 0 || a.clock.Real().Sub(a.rtgsTried) < rtgsRetry {
		a.mu.Unlock()
		return nil
	}
	a.rtgsTried, a.rtgsOpening = a.clock.Real(), true
	a.mu.Unlock()
	r, err := openRTGS(context.Background(), a.cfg, a.ctl, a.cfg.Start)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rtgsOpening = false
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

func (p position) netDebit() int64 {
	return max(p.outward-p.inward, 0)
}

func (a *App) openPosition(sw *nibss.Store, code string) (position, error) {
	now := a.clock.Now()
	list, err := sw.Open(context.Background())
	if err != nil {
		return position{}, err
	}
	var p position
	for _, x := range list {
		if x.Status == nibss.SessionOK && x.ResolvedAt != nil && x.ResolvedAt.After(now) {
			continue
		}
		var ok bool
		switch code {
		case x.FromCode:
			if p.outward, ok = addMinor(p.outward, x.Amount); !ok {
				return position{}, fmt.Errorf("add open outward sessions of %s: the total is too large", code)
			}
		case x.ToCode:
			if p.inward, ok = addMinor(p.inward, x.Amount); !ok {
				return position{}, fmt.Errorf("add open inward sessions of %s: the total is too large", code)
			}
		}
	}
	return p, nil
}

func addMinor(a, b int64) (int64, bool) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, false
	}
	return a + b, true
}

func (a *App) settle(now time.Time, adhoc bool) (int, error) {
	a.gate.RLock()
	defer a.gate.RUnlock()
	return a.settleGated(now, adhoc)
}

func (a *App) settleGated(now time.Time, adhoc bool) (int, error) {
	a.mu.Lock()
	for a.settling {
		a.settleDone.Wait()
	}
	r := a.rtgs
	if r == nil || len(a.cfg.Banks) == 0 {
		a.mu.Unlock()
		return 0, nil
	}
	a.settling = true
	ledgers := slices.Clone(a.ledgers)
	banks := make([]string, 0, len(a.cfg.Banks))
	for _, b := range a.cfg.Banks {
		banks = append(banks, b.ID)
	}
	sched := a.schedule()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.settling = false
		a.settleDone.Broadcast()
		a.mu.Unlock()
	}()
	sw := a.nibssStore()
	if sw == nil {
		return 0, nil
	}
	ctx := context.Background()
	for _, id := range banks {
		if err := r.member(id, bank.GenesisSettlement, bank.GenesisVault); err != nil {
			return 0, err
		}
	}
	todo, err := sw.Unposted(ctx)
	if err != nil {
		return 0, err
	}
	upTo := now
	if !adhoc {
		upTo = sched.prev(now)
	}
	list, err := sw.Unsettled(ctx, upTo)
	if err != nil {
		return 0, err
	}
	groups := map[time.Time][]nibss.Session{}
	for _, x := range list {
		c := upTo
		if !adhoc {
			c = sched.next(x.ResolvedAt.Add(-time.Nanosecond))
		}
		groups[c] = append(groups[c], x)
	}
	for _, c := range slices.SortedFunc(maps.Keys(groups), func(x, y time.Time) int { return x.Compare(y) }) {
		ids := make([]string, 0, len(groups[c]))
		for _, x := range groups[c] {
			ids = append(ids, x.ID)
		}
		key := sched.key(c)
		if adhoc {
			key = "adhoc:" + c.In(clock.Lagos).Format(time.RFC3339Nano) + ":" + ids[0]
		}
		if err := sw.MarkSettled(ctx, ids, key, c); err != nil {
			return 0, err
		}
		todo = append(todo, nibss.Settlement{Key: key, CutoffAt: c, Sessions: groups[c]})
	}
	for _, x := range todo {
		if err := postSettlement(r, ledgers, x); err != nil {
			return 0, err
		}
		if err := sw.MarkPosted(ctx, x.Key, x.CutoffAt); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

func postSettlement(r *rtgs, ledgers []*ledgerSource, x nibss.Settlement) error {
	byCode := map[string]*ledgerSource{}
	for _, l := range ledgers {
		byCode[l.inst.NIPCode] = l
	}
	nets := map[string]*position{}
	side := func(l *ledgerSource) *position {
		p := nets[l.inst.ID]
		if p == nil {
			p = &position{}
			nets[l.inst.ID] = p
		}
		return p
	}
	for _, s := range x.Sessions {
		from, to := byCode[s.FromCode], byCode[s.ToCode]
		if from == nil || to == nil {
			return fmt.Errorf("settle %s: session %s is between %s and %s, and one of them has no open books", x.Key, s.ID, s.FromCode, s.ToCode)
		}
		side(from).outward += s.Amount
		side(to).inward += s.Amount
	}
	for _, l := range ledgers {
		p := nets[l.inst.ID]
		if p == nil {
			continue
		}
		if _, err := l.Bank().Settle(l.ctx, x.Key, p.outward, p.inward, x.CutoffAt); err != nil {
			return fmt.Errorf("settle %s at %s: %w", x.Key, l.inst.ID, err)
		}
	}
	if err := r.settle(x.Key, x.CutoffAt, nets); err != nil {
		return fmt.Errorf("settle %s at the CBN: %w", x.Key, err)
	}
	return nil
}

func (r *rtgs) settle(key string, at time.Time, nets map[string]*position) error {
	members := slices.Sorted(maps.Keys(nets))
	lines := []ledger.Line{}
	var total int64
	for _, id := range members {
		net := nets[id].inward - nets[id].outward
		if net == 0 {
			continue
		}
		total += net
		lines = append(lines, ledger.Line{Account: settlementAccount(id), Amount: -net, Narration: "NIP session " + key})
	}
	if total != 0 {
		return fmt.Errorf("the session nets to %d, not zero", total)
	}
	if len(lines) < 2 {
		return nil
	}
	_, err := r.l.Post(r.ctx, ledger.Entry{Key: "session:" + key, Kind: "session", PostingDate: clock.DateOf(at), PostedAt: at, Narration: "NIP settlement session " + key, Lines: lines, AllowOverdraft: true})
	return err
}

func (a *App) SettleNow() (int, error) {
	return a.settle(a.clock.Now(), true)
}

type SettlementCheck struct {
	Sessions int      `json:"sessions"`
	Problems []string `json:"problems"`
}

func (a *App) CheckSettlement() (SettlementCheck, error) {
	out := SettlementCheck{Problems: []string{}}
	now := a.clock.Now()
	if _, err := a.settle(now, false); err != nil {
		return out, err
	}
	a.mu.Lock()
	ledgers := slices.Clone(a.ledgers)
	unopened := a.rtgs == nil && len(a.cfg.Banks) == 0
	a.mu.Unlock()
	if unopened {
		return out, nil
	}
	books, err := a.settlementBooks()
	if err != nil {
		return out, err
	}
	ctx, r := books.ctx, books.r
	if err := r.l.Pool().QueryRow(ctx, `SELECT count(*) FROM journal_entries WHERE kind = 'session'`).Scan(&out.Sessions); err != nil {
		return out, fmt.Errorf("count CBN sessions: %w", err)
	}
	c, err := r.l.Verify(ctx)
	if err != nil {
		return out, err
	}
	if c.TrialBalance != 0 || len(c.UnbalancedEntries) > 0 {
		out.Problems = append(out.Problems, fmt.Sprintf("the CBN's books do not balance: %d", c.TrialBalance))
	}
	for _, l := range ledgers {
		problems, err := books.check(l)
		if err != nil {
			return out, err
		}
		out.Problems = append(out.Problems, problems...)
	}
	return out, nil
}

func (r *rtgs) sessionNets() (map[string]map[string]int64, error) {
	rows, err := r.l.Pool().Query(r.ctx, `SELECT substr(e.idempotency_key, length('session:') + 1), substr(a.code, length('SETTLEMENT/') + 1), sum(-jl.amount)
		FROM journal_lines jl JOIN journal_entries e ON e.id = jl.entry_id JOIN gl_accounts a ON a.id = jl.account_id
		WHERE e.kind = 'session' AND a.code LIKE 'SETTLEMENT/%' GROUP BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("read CBN sessions: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]int64{}
	for rows.Next() {
		var key, member string
		var net int64
		if err := rows.Scan(&key, &member, &net); err != nil {
			return nil, fmt.Errorf("read CBN sessions: %w", err)
		}
		if out[member] == nil {
			out[member] = map[string]int64{}
		}
		out[member][key] = net
	}
	return out, rows.Err()
}

type settlementBooks struct {
	r         *rtgs
	ctx       context.Context
	nibssNets map[string]map[string]int64
	cbnNets   map[string]map[string]int64
	open      []nibss.Session
}

func (a *App) settlementBooks() (settlementBooks, error) {
	a.mu.Lock()
	r := a.rtgs
	a.mu.Unlock()
	sw := a.nibssStore()
	if r == nil || sw == nil {
		return settlementBooks{}, errors.New("the CBN ledger or NIBSS is not open yet; try again soon")
	}
	ctx := context.Background()
	nibssNets, err := sw.SettledNets(ctx)
	if err != nil {
		return settlementBooks{}, err
	}
	cbnNets, err := r.sessionNets()
	if err != nil {
		return settlementBooks{}, err
	}
	open, err := sw.Open(ctx)
	if err != nil {
		return settlementBooks{}, err
	}
	return settlementBooks{r: r, ctx: ctx, nibssNets: nibssNets, cbnNets: cbnNets, open: open}, nil
}

func (s settlementBooks) check(l *ledgerSource) ([]string, error) {
	var out []string
	ctx := s.ctx
	code, id := l.inst.NIPCode, l.inst.ID
	b := l.Bank()
	posted, err := b.Settlements(ctx)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for k := range s.nibssNets[code] {
		keys[k] = true
	}
	for k := range posted {
		keys[k] = true
	}
	for k := range s.cbnNets[id] {
		keys[k] = true
	}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		n, p, m := s.nibssNets[code][k], posted[k], s.cbnNets[id][k]
		if n != p || n != m {
			out = append(out, fmt.Sprintf("session %s at %s: NIBSS nets %d, the bank posted %d and the CBN moved %d", k, id, n, p, m))
		}
	}
	var want position
	for _, x := range s.open {
		if x.FromCode == code {
			want.outward += x.Amount
		}
		if x.ToCode == code && (x.Status == nibss.SessionOK || x.Outcome == nibss.OutcomeCredited) {
			want.inward += x.Amount
		}
	}
	owe, err := b.L.Balance(ctx, bank.NIPOutward)
	if err != nil {
		return nil, err
	}
	due, err := b.L.Balance(ctx, bank.NIPInward)
	if err != nil {
		return nil, err
	}
	if owe != want.outward || due != want.inward {
		out = append(out, fmt.Sprintf("%s has NIP payable %d and receivable %d, but its open NIBSS sessions come to %d and %d", id, owe, due, want.outward, want.inward))
	}
	held, err := b.L.Balance(ctx, bank.Settlement)
	if err != nil {
		return nil, err
	}
	rtgsBal, err := s.r.Balance(id)
	if err != nil {
		return nil, err
	}
	if held != rtgsBal {
		out = append(out, fmt.Sprintf("%s's books show %d at the CBN, but the CBN shows %d", id, held, rtgsBal))
	}
	return out, nil
}
