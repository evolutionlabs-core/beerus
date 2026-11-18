package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/view"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type ImpactAccount struct {
	NUBAN     string `json:"nuban"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Owner     string `json:"owner"`
	Balance   int64  `json:"balance_minor"`
	Connected bool   `json:"connected"`
}

type ImpactCustomer struct {
	ID        string `json:"customer"`
	Name      string `json:"name"`
	Accounts  int    `json:"accounts"`
	Deposits  int64  `json:"deposits_minor"`
	Insured   int64  `json:"insured_minor"`
	Uninsured int64  `json:"uninsured_minor"`
}

type ImpactConnection struct {
	ID       string `json:"id"`
	Partner  string `json:"partner"`
	Customer string `json:"customer"`
	Status   string `json:"status"`
}

type CloseImpact struct {
	Bank         BankStatus         `json:"bank"`
	InsuredLimit int64              `json:"insured_limit_minor"`
	Deposits     int64              `json:"deposits_minor"`
	Insured      int64              `json:"insured_minor"`
	Uninsured    int64              `json:"uninsured_minor"`
	Customers    []ImpactCustomer   `json:"customers"`
	Accounts     []ImpactAccount    `json:"accounts"`
	Connections  []ImpactConnection `json:"connections"`
	Partners     []string           `json:"partners"`
	Blockers     []string           `json:"blockers"`
}

func insuredLimit(bankType string) (int64, error) {
	pack, err := countrypack.Nigeria()
	if err != nil {
		return 0, err
	}
	return pack.DepositInsurance.Limit(bankType)
}

func (a *App) CloseImpact(ctx context.Context, id string) (CloseImpact, error) {
	st, ok := a.bankStatus(id)
	banks := a.bankList()
	b := findBank(banks, id)
	if !ok || b == nil {
		return CloseImpact{}, errNotFound
	}
	limit, err := insuredLimit(b.Type)
	if err != nil {
		return CloseImpact{}, err
	}
	out := CloseImpact{Bank: st, InsuredLimit: limit, Customers: []ImpactCustomer{}, Accounts: []ImpactAccount{}, Connections: []ImpactConnection{}, Partners: []string{}, Blockers: []string{}}
	switch st.Phase {
	case PhaseProvisioning, PhaseRepairing:
		out.Blockers = append(out.Blockers, "the bank is "+st.Phase+"; wait until it is active or suspended")
	case PhaseClosed:
		out.Blockers = append(out.Blockers, "the bank is already closed")
	}
	for _, other := range banks {
		if other.SettlesThrough == id && !other.Closed() {
			out.Blockers = append(out.Blockers, other.Name+" settles through this bank; close it first")
		}
	}
	a.mu.Lock()
	connected := map[string]bool{}
	partners := map[string]bool{}
	for _, c := range a.st.Connections {
		if c.Institution != id || c.Status == "revoked" || c.Status == "expired" {
			continue
		}
		out.Connections = append(out.Connections, ImpactConnection{ID: c.ID, Partner: c.PartnerID, Customer: c.CustomerID, Status: c.Status})
		partners[c.PartnerID] = true
	}
	for _, rec := range a.st.Accounts {
		if c := a.st.Connections[rec.ConnectionID]; c != nil && c.Institution == id && c.Status != "revoked" && c.Status != "expired" {
			connected[rec.Key] = true
		}
	}
	a.mu.Unlock()
	books := a.booksOf(id)
	if books == nil && st.Phase == PhaseClosed {
		return sortedImpact(out, partners), nil
	}
	if books == nil {
		return CloseImpact{}, fmt.Errorf("the books of %s are not open", b.Name)
	}
	holders, err := books.customers(ctx)
	if err != nil {
		return CloseImpact{}, err
	}
	owned, err := accountsByOwner(ctx, books)
	if err != nil {
		return CloseImpact{}, err
	}
	for _, cust := range holders {
		cu := ImpactCustomer{ID: cust.ID, Name: cust.Name}
		for _, acc := range owned[cust.ID] {
			out.Accounts = append(out.Accounts, ImpactAccount{NUBAN: acc.NUBAN, Name: acc.Name, Type: acc.Type, Owner: cust.ID, Balance: acc.Balance, Connected: connected[acctKey(id, acc.NUBAN)]})
			cu.Accounts++
			if acc.Balance > 0 {
				cu.Deposits += acc.Balance
			}
		}
		cu.Insured = min(cu.Deposits, out.InsuredLimit)
		cu.Uninsured = cu.Deposits - cu.Insured
		out.Deposits += cu.Deposits
		out.Insured += cu.Insured
		out.Uninsured += cu.Uninsured
		out.Customers = append(out.Customers, cu)
	}
	return sortedImpact(out, partners), nil
}

func sortedImpact(out CloseImpact, partners map[string]bool) CloseImpact {
	for p := range partners {
		out.Partners = append(out.Partners, p)
	}
	sort.SliceStable(out.Customers, func(i, j int) bool { return out.Customers[i].Deposits > out.Customers[j].Deposits })
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].NUBAN < out.Accounts[j].NUBAN })
	sort.Slice(out.Connections, func(i, j int) bool { return out.Connections[i].ID < out.Connections[j].ID })
	sort.Strings(out.Partners)
	return out
}

func accountsByOwner(ctx context.Context, books bankBooks) (map[string][]server.AccountJSON, error) {
	out := map[string][]server.AccountJSON{}
	cursor := ""
	for {
		page, next, err := books.accounts(ctx, "", bank.MaxPage, cursor)
		if err != nil {
			return nil, err
		}
		for _, acc := range page {
			out[acc.Customer] = append(out[acc.Customer], acc)
		}
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

func accountsOf(ctx context.Context, books bankBooks, customer string) ([]server.AccountJSON, error) {
	out := []server.AccountJSON{}
	cursor := ""
	for {
		page, next, err := books.accounts(ctx, customer, bank.MaxPage, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

func (a *App) ctlCloseImpact(w http.ResponseWriter, r *http.Request) {
	imp, err := a.CloseImpact(r.Context(), r.PathValue("id"))
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, imp)
}

func (a *App) bankList() []*config.Bank {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.cfg.Banks)
}

func findBank(banks []*config.Bank, id string) *config.Bank {
	for _, b := range banks {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (a *App) bankByID(id string) *config.Bank {
	return findBank(a.bankList(), id)
}

func (a *App) banksLocked() []*config.Bank {
	return a.cfg.Banks
}

func (a *App) bankLocked(id string) *config.Bank {
	for _, b := range a.banksLocked() {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (a *App) openInstitutions() []*config.Bank {
	return server.OpenBanks(a.banksLocked())
}

type CloseRequest struct {
	Money     string            `json:"money"`
	To        string            `json:"to"`
	Customers map[string]string `json:"customers"`
}

type Move struct {
	From   string `json:"from"`
	To     string `json:"to"`
	ToBank string `json:"to_bank"`
	Amount int64  `json:"amount_minor"`
	Payout bool   `json:"payout"`
}

type CloseResult struct {
	Bank    BankStatus `json:"bank"`
	Moved   int64      `json:"moved_minor"`
	PaidOut int64      `json:"paid_out_minor"`
	Claims  int64      `json:"claims_minor"`
	Left    int64      `json:"left_minor"`
	Moves   []Move     `json:"moves"`
	Revoked []string   `json:"revoked_connections"`

	ClosedAt time.Time           `json:"closed_at"`
	Lines    []setup.ClosureLine `json:"lines"`
}

func (a *App) CloseBank(ctx context.Context, id string, req CloseRequest) (setup.Resource, error) {
	err := a.applyEdit(ctx, func(sp *setup.Spec) error {
		i := slices.IndexFunc(sp.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
		if i < 0 {
			return errNotFound
		}
		b := &sp.Spec.Banks[i]
		if b.State == setup.StateClosed {
			return server.Invalid("%s is already closed", b.Name)
		}
		b.State = setup.StateClosed
		if req.Money != "" || req.To != "" || len(req.Customers) > 0 {
			b.OnRemove = &setup.Removal{Money: req.Money, To: req.To, Customers: req.Customers}
		}
		return nil
	}, ApplyOptions{})
	if err != nil {
		return setup.Resource{}, err
	}
	return a.Resource(setup.KindBank, id)
}

type closeRun struct {
	running bool
	at      time.Time
	name    string
	cond    *setup.Condition
	res     CloseResult
	err     error

	generation int64
	money      *setup.Removal
}

func sameRemoval(a, b *setup.Removal) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Money == b.Money && a.To == b.To && maps.Equal(a.Customers, b.Customers)
}

func (a *App) closeMovesDue(ctx context.Context) {
	st, err := a.ctl.status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	for _, r := range st.Resources {
		if r.Kind != setup.KindBank || (r.Phase != config.PhaseClosing && r.Phase != config.PhaseTerminating) || (r.NextAttemptAt != nil && now.Before(*r.NextAttemptAt)) {
			continue
		}
		if nextFinalizer(r) != setup.FinalizerDecideMoney {
			continue
		}
		if b := a.bankByID(r.ID); b == nil || b.Closed() {
			continue
		}
		if !a.startCloseRun(r.ID) {
			continue
		}
		run := a.closeMoves(ctx, r)
		a.finishCloseRun(r.ID, run)
	}
	a.sweepCloseRuns()
}

func (a *App) startCloseRun(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closeRuns[id] != nil && a.closeRuns[id].running {
		return false
	}
	if a.closeRuns == nil {
		a.closeRuns = map[string]*closeRun{}
	}
	a.closeRuns[id] = &closeRun{running: true, at: a.clock.Real()}
	return true
}

func (a *App) finishCloseRun(id string, run closeRun) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run.at = a.clock.Real()
	a.closeRuns[id] = &run
}

func (a *App) takeCloseRun(id string) (closeRun, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run := a.closeRuns[id]
	if run == nil || run.running {
		return closeRun{}, false
	}
	delete(a.closeRuns, id)
	return *run, true
}

func (a *App) sweepCloseRuns() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Real()
	for id, run := range a.closeRuns {
		if !run.running && now.Sub(run.at) > finalizerReadTTL {
			delete(a.closeRuns, id)
		}
	}
}

func (a *App) closeMoves(ctx context.Context, r setup.Resource) closeRun {
	run := a.closeMovesFor(ctx, r)
	run.generation, run.money = r.Generation, r.Money
	return run
}

func (a *App) closeMovesFor(ctx context.Context, r setup.Resource) closeRun {
	imp, err := a.CloseImpact(ctx, r.ID)
	if err != nil {
		return closeRun{err: err}
	}
	choice, to, cond := a.closeChoices(ctx, r, imp)
	if cond != nil {
		return closeRun{cond: cond}
	}
	res, err := a.closeBooks(ctx, r.ID, imp, choice, to)
	if err != nil {
		return closeRun{err: err}
	}
	return closeRun{name: imp.Bank.Name, res: res}
}

func (a *App) decideMoney(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	b := a.bankByID(id)
	if b == nil {
		return nil, errNotFound
	}
	if b.Closed() {
		return nil, nil
	}
	run, ok := a.takeCloseRun(id)
	if !ok || run.generation != r.Generation || !sameRemoval(run.money, r.Money) {
		return a.blocked(setup.ReasonSettlementPending, "The money moves of %s are being worked out. Try again soon.", b.Name), nil
	}
	if run.err != nil {
		return nil, run.err
	}
	if run.cond != nil {
		return run.cond, nil
	}
	res := run.res
	res.Bank, _ = a.bankStatus(id)
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("encode the close result: %w", err)
	}
	if err := a.ctl.update(func(st *setup.Status) (bool, error) {
		cur := findResource(st, setup.KindBank, id)
		if cur == nil {
			return false, errNotFound
		}
		cur.Result = raw
		keepClosure(st, setup.Closure{Bank: id, Name: run.name, ClosedAt: res.ClosedAt, Lines: res.Lines})
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("record the close result: %w", err)
	}
	a.lifeMu.Lock()
	a.mu.Lock()
	b.State = setup.StateClosed
	a.mu.Unlock()
	a.lifeMu.Unlock()
	if _, err := a.ensureBankDir(id, false); err != nil {
		return nil, err
	}
	return nil, nil
}

func (a *App) blocked(reason, format string, args ...any) *setup.Condition {
	return &setup.Condition{Type: setup.ConditionBlocked, Status: "True", Reason: reason, Message: fmt.Sprintf(format, args...), ChangedAt: a.clock.Real()}
}

func (a *App) closeChoices(ctx context.Context, r setup.Resource, imp CloseImpact) (map[string]string, string, *setup.Condition) {
	choice := map[string]string{}
	rm := r.Money
	held := 0
	var total int64
	for _, cu := range imp.Customers {
		if cu.Deposits > 0 {
			held++
			total += cu.Deposits
		}
	}
	if rm == nil {
		if total == 0 {
			return choice, "", nil
		}
		return nil, "", a.blocked(setup.ReasonMoneyDecisionNeeded, "%s holds %s for %s. Choose --money transfer --to <bank>, --money payout or --money leave. Nothing moves until you do.", imp.Bank.Name, bank.Naira(total), view.Plural(held, "customer", "customers"))
	}
	for _, cu := range imp.Customers {
		choice[cu.ID] = rm.Money
	}
	needTarget := rm.Money == setup.MoneyTransfer
	for cid, c := range rm.Customers {
		if _, ok := choice[cid]; !ok {
			return nil, "", a.blocked(setup.ReasonMoneyDecisionNeeded, "%s is not a customer of %s. Remove it from on_remove.customers.", cid, imp.Bank.Name)
		}
		choice[cid] = c
		needTarget = needTarget || c == setup.MoneyTransfer
	}
	if !needTarget {
		return choice, "", nil
	}
	to := a.bankByID(rm.To)
	faulty, err := a.faultBanks(ctx)
	if err != nil {
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "Every bank must be asked about its faults before money moves, and one could not be reached: %v. Check that the banks are running, then try again.", err)
	}
	down := a.downBanks()
	suspended, changing, err := a.targetChange(rm.To)
	if err != nil {
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "The state of %s could not be read: %v. Try again soon.", rm.To, err)
	}
	switch {
	case to == nil:
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s is not a bank in this setup, so money cannot move there. Choose another bank.", rm.To)
	case a.bankPhase(rm.To) != PhaseActive || down[rm.To] || suspended:
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s is suspended or down, so money cannot move there. Resume it or choose another bank.", to.Name)
	case changing:
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s has a change that NIBSS has not taken yet, so money cannot move there. Try again soon.", to.Name)
	case faulty[rm.To]:
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s has a fault, so the money cannot be moved safely; clear it with beerus fault clear --bank %s.", to.Name, rm.To)
	case faulty[r.ID]:
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s has a fault, so the money cannot be moved safely; clear it with beerus fault clear --bank %s.", imp.Bank.Name, r.ID)
	}
	return choice, rm.To, nil
}

func (a *App) targetChange(id string) (suspended, changing bool, err error) {
	st, err := a.ctl.status()
	if err != nil {
		return false, false, fmt.Errorf("read control.json: %w", err)
	}
	if sb, ok := bankIn(st.Applied, id); ok && sb.State == setup.StateSuspended {
		suspended = true
	}
	if r := findResource(&st, setup.KindBank, id); r != nil && r.Generation != r.ObservedGeneration {
		changing = true
	}
	return suspended, changing, nil
}

func (a *App) closeBooks(ctx context.Context, id string, imp CloseImpact, choice map[string]string, to string) (CloseResult, error) {
	from, dest := a.booksOf(id), a.booksOf(to)
	if from == nil {
		return CloseResult{}, fmt.Errorf("the books of %s are not open", id)
	}
	payouts, err := from.payouts(ctx)
	if err != nil {
		return CloseResult{}, err
	}
	paid := map[string]int64{}
	for _, acc := range imp.Accounts {
		paid[acc.Owner] += payouts[acc.NUBAN]
	}
	holders := map[string]bank.Customer{}
	if to != "" {
		all, err := from.customers(ctx)
		if err != nil {
			return CloseResult{}, err
		}
		for _, c := range all {
			holders[c.ID] = c
		}
	}
	for _, acc := range imp.Accounts {
		c := choice[acc.Owner]
		live, err := from.account(ctx, acc.NUBAN)
		if err != nil {
			return CloseResult{}, err
		}
		bal := live.Balance
		if c == setup.MoneyLeave || bal <= 0 {
			continue
		}
		key := "close:" + id + ":" + acc.NUBAN
		if c == setup.MoneyPayout {
			amount := min(bal, imp.InsuredLimit-paid[acc.Owner])
			if amount <= 0 {
				continue
			}
			if err := from.payout(ctx, key, acc.NUBAN, amount); err != nil {
				return CloseResult{}, fmt.Errorf("pay out %s from %s: %w", bank.Naira(amount), acc.NUBAN, err)
			}
			paid[acc.Owner] += amount
			continue
		}
		if dest == nil {
			return CloseResult{}, fmt.Errorf("the books of %s are not open", to)
		}
		holder, err := holderOf(holders, acc.Owner)
		if err != nil {
			return CloseResult{}, err
		}
		nuban, err := a.closureAccount(ctx, id, dest, a.bankByID(to), holder, acc)
		if err != nil {
			return CloseResult{}, err
		}
		moveKey, err := a.closureKey(ctx, id, acc.NUBAN, key)
		if err != nil {
			return CloseResult{}, err
		}
		tr, err := a.transfer(ctx, server.TransferRequest{FromBank: id, From: acc.NUBAN, ToBank: to, To: nuban, Amount: bal, Narration: "Closure of " + imp.Bank.Name, Key: moveKey, Closure: true})
		if err != nil {
			return CloseResult{}, fmt.Errorf("move %s from %s: %w", bank.Naira(bal), acc.NUBAN, err)
		}
		if tr.Status != nip.Success {
			return CloseResult{}, fmt.Errorf("move %s from %s: session %s is %s", bank.Naira(bal), acc.NUBAN, tr.SessionID, tr.Status)
		}
	}
	for _, acc := range imp.Accounts {
		if choice[acc.Owner] != setup.MoneyPayout {
			continue
		}
		live, err := from.account(ctx, acc.NUBAN)
		if err != nil {
			return CloseResult{}, err
		}
		if live.Balance > 0 {
			if err := from.freeze(ctx, "close:"+id+":"+acc.NUBAN+":freeze", acc.NUBAN); err != nil {
				return CloseResult{}, err
			}
		}
	}
	return a.closeOutcome(ctx, id, imp, choice, from)
}

func holderOf(holders map[string]bank.Customer, owner string) (bank.Customer, error) {
	c, ok := holders[owner]
	if !ok {
		return bank.Customer{}, fmt.Errorf("read customer %s: %w", owner, bank.ErrUnknownCustomer)
	}
	return c, nil
}

func (a *App) closureKey(ctx context.Context, id, nuban, key string) (string, error) {
	all, err := a.nibssCtl.closureSessions(ctx, id)
	if err != nil {
		return "", fmt.Errorf("count failed closure moves: %w", err)
	}
	failed := 0
	for _, x := range all {
		if x.FromAccount == nuban && x.Status != nibss.SessionOK && x.Status != nibss.SessionPending {
			failed++
		}
	}
	if failed == 0 {
		return key, nil
	}
	return fmt.Sprintf("%s:%d", key, failed), nil
}

func (a *App) closeOutcome(ctx context.Context, id string, imp CloseImpact, choice map[string]string, from bankBooks) (CloseResult, error) {
	all, err := a.nibssCtl.closureSessions(ctx, id)
	if err != nil {
		return CloseResult{}, fmt.Errorf("read back closure moves: %w", err)
	}
	sent := slices.DeleteFunc(all, func(x nibss.ClosureSession) bool {
		return x.Status != nibss.SessionOK && x.Status != nibss.SessionPending
	})
	payouts, err := from.payouts(ctx)
	if err != nil {
		return CloseResult{}, err
	}
	owner := map[string]string{}
	for _, acc := range imp.Accounts {
		owner[acc.NUBAN] = acc.Owner
	}
	targets := map[string]*config.Bank{}
	for _, other := range a.bankList() {
		targets[other.NIPCode] = other
	}
	holder := map[string]string{}
	for _, cu := range imp.Customers {
		holder[cu.ID] = cu.Name
	}
	line := func(nuban, outcome string, amount int64) setup.ClosureLine {
		return setup.ClosureLine{Customer: owner[nuban], Name: holder[owner[nuban]], NUBAN: nuban, Outcome: outcome, AmountMinor: amount}
	}
	res := CloseResult{Moves: []Move{}, Lines: []setup.ClosureLine{}, ClosedAt: a.clock.Now(), Revoked: a.revokedConnections(func(c *Connection) bool { return c.Institution == id })}
	for _, x := range sent {
		if _, ok := owner[x.FromAccount]; !ok {
			continue
		}
		if x.Status != nibss.SessionOK {
			return CloseResult{}, fmt.Errorf("closure session %s from %s is %s, not %s", x.ID, x.FromAccount, x.Status, nibss.SessionOK)
		}
		dest := targets[x.ToBankCode]
		if dest == nil {
			dest = &config.Bank{Name: x.ToBankCode}
		}
		res.Moves = append(res.Moves, Move{From: x.FromAccount, To: x.ToAccount, ToBank: dest.ID, Amount: x.Amount})
		moved := line(x.FromAccount, setup.OutcomeTransferred, x.Amount)
		moved.ToBank, moved.ToBankName, moved.ToNUBAN = dest.ID, dest.Name, x.ToAccount
		res.Lines = append(res.Lines, moved)
		res.Moved += x.Amount
	}
	paid := map[string]int64{}
	for _, acc := range imp.Accounts {
		if amount := payouts[acc.NUBAN]; amount > 0 {
			res.Moves = append(res.Moves, Move{From: acc.NUBAN, Amount: amount, Payout: true})
			res.Lines = append(res.Lines, line(acc.NUBAN, setup.OutcomePaidOut, amount))
			res.PaidOut += amount
			paid[acc.Owner] += amount
		}
	}
	held := map[string]int64{}
	for _, acc := range imp.Accounts {
		live, err := from.account(ctx, acc.NUBAN)
		if err != nil {
			return CloseResult{}, fmt.Errorf("read back the balance of %s: %w", acc.NUBAN, err)
		}
		bal := live.Balance
		if bal <= 0 {
			continue
		}
		held[acc.Owner] += bal
		switch choice[acc.Owner] {
		case setup.MoneyLeave:
			res.Lines = append(res.Lines, line(acc.NUBAN, setup.OutcomeLeft, bal))
			res.Left += bal
		case setup.MoneyPayout:
			if live.Status != "frozen" {
				return CloseResult{}, fmt.Errorf("%s keeps %s as an NDIC claim but is %s, not frozen", acc.NUBAN, bank.Naira(bal), live.Status)
			}
			res.Lines = append(res.Lines, line(acc.NUBAN, setup.OutcomeClaim, bal))
			res.Claims += bal
		default:
			return CloseResult{}, fmt.Errorf("%s still holds %s after its money was moved", acc.NUBAN, bank.Naira(bal))
		}
	}
	for o, c := range choice {
		if c == setup.MoneyPayout {
			if want := min(paid[o]+held[o], imp.InsuredLimit); paid[o] != want {
				return CloseResult{}, fmt.Errorf("customer %s was paid out %s, but the insured amount is %s", o, bank.Naira(paid[o]), bank.Naira(want))
			}
		}
	}
	return res, nil
}

func (a *App) closureAccount(ctx context.Context, fromID string, dest bankBooks, to *config.Bank, cust bank.Customer, acc ImpactAccount) (string, error) {
	found, err := dest.customersByBVN(ctx, cust.BVN)
	if err != nil {
		return "", fmt.Errorf("find %s at %s: %w", cust.Name, to.Name, err)
	}
	holder := ""
	if i := slices.IndexFunc(found, func(c bank.Customer) bool { return c.Status == bank.CustomerActive }); i >= 0 {
		holder = found[i].ID
	} else {
		created, err := dest.createCustomer(ctx, "close:"+fromID+":"+cust.ID, cust)
		if err != nil {
			return "", fmt.Errorf("find %s at %s: %w", cust.Name, to.Name, err)
		}
		holder = created.ID
	}
	have, err := accountsOf(ctx, dest, holder)
	if err != nil {
		return "", err
	}
	for _, x := range have {
		if x.Type == acc.Type && x.Status != "closed" {
			return x.NUBAN, nil
		}
	}
	opened, err := dest.openAccount(ctx, "close:"+fromID+":"+acc.NUBAN, holder, acc.Type)
	if err != nil {
		return "", fmt.Errorf("open an account for %s at %s: %w", cust.Name, to.Name, err)
	}
	return opened.NUBAN, nil
}

func (a *App) ctlCloseBank(w http.ResponseWriter, r *http.Request) {
	var req CloseRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"money":"transfer","to":"bank-a"}.`)
		return
	}
	res, err := a.CloseBank(r.Context(), r.PathValue("id"), req)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusAccepted, res)
}

func keepClosure(st *setup.Status, c setup.Closure) {
	st.Closures = slices.DeleteFunc(st.Closures, func(x setup.Closure) bool { return x.Bank == c.Bank })
	st.Closures = append(st.Closures, c)
}

func (a *App) Closure(id string) (setup.Closure, error) {
	st, err := a.ctl.durableStatus()
	if err != nil {
		return setup.Closure{}, err
	}
	for _, c := range st.Closures {
		if c.Bank == id {
			return c, nil
		}
	}
	return setup.Closure{}, errNotFound
}

func (a *App) ctlClosure(w http.ResponseWriter, r *http.Request) {
	c, err := a.Closure(r.PathValue("id"))
	if err != nil {
		a.applyFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, c)
}
