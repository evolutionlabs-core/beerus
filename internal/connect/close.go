package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
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

func (a *App) CloseImpact(id string) (CloseImpact, error) {
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
	ls := a.ledgerFor(id)
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
	if ls == nil {
		return CloseImpact{}, fmt.Errorf("the books of %s are not open", b.Name)
	}
	holders, err := allCustomers(ls)
	if err != nil {
		return CloseImpact{}, err
	}
	now := a.clock.Now()
	bk := ls.Bank()
	for _, cust := range holders {
		accs, err := bk.CustomerAccounts(ls.ctx, cust.ID)
		if err != nil {
			return CloseImpact{}, err
		}
		cu := ImpactCustomer{ID: cust.ID, Name: cust.Name}
		for _, acc := range accs {
			bal, err := bk.BalanceAt(ls.ctx, acc.NUBAN, now)
			if err != nil {
				return CloseImpact{}, err
			}
			out.Accounts = append(out.Accounts, ImpactAccount{NUBAN: acc.NUBAN, Name: acc.Name, Type: acc.Product, Owner: cust.ID, Balance: bal, Connected: connected[acctKey(id, acc.NUBAN)]})
			cu.Accounts++
			if bal > 0 {
				cu.Deposits += bal
			}
		}
		cu.Insured = min(cu.Deposits, out.InsuredLimit)
		cu.Uninsured = cu.Deposits - cu.Insured
		out.Deposits += cu.Deposits
		out.Insured += cu.Insured
		out.Uninsured += cu.Uninsured
		out.Customers = append(out.Customers, cu)
	}
	for p := range partners {
		out.Partners = append(out.Partners, p)
	}
	sort.SliceStable(out.Customers, func(i, j int) bool { return out.Customers[i].Deposits > out.Customers[j].Deposits })
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].NUBAN < out.Accounts[j].NUBAN })
	sort.Slice(out.Connections, func(i, j int) bool { return out.Connections[i].ID < out.Connections[j].ID })
	sort.Strings(out.Partners)
	return out, nil
}

func allCustomers(ls *ledgerSource) ([]bank.Customer, error) {
	out := []bank.Customer{}
	cursor := ""
	for {
		page, next, err := ls.Bank().Customers(ls.ctx, bank.MaxPage, cursor)
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
	imp, err := a.CloseImpact(r.PathValue("id"))
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, imp)
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
	if a.role == RoleBank && a.peerBanks != nil {
		return a.peerBanks
	}
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
	out := []*config.Bank{}
	for _, b := range a.banksLocked() {
		if !b.Closed() {
			out = append(out, b)
		}
	}
	return out
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

func (a *App) CloseBank(id string, req CloseRequest) (setup.Resource, error) {
	err := a.applyEdit(func(sp *setup.Spec) error {
		i := slices.IndexFunc(sp.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
		if i < 0 {
			return errNotFound
		}
		b := &sp.Spec.Banks[i]
		if b.State == setup.StateClosed {
			return invalid("%s is already closed", b.Name)
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

func (a *App) decideMoney(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	b := a.bankByID(id)
	if b == nil {
		return nil, errNotFound
	}
	if b.Closed() {
		return nil, nil
	}
	imp, err := a.CloseImpact(id)
	if err != nil {
		return nil, err
	}
	choice, to, cond := a.closeChoices(r, imp)
	if cond != nil {
		return cond, nil
	}
	res, err := a.closeBooks(ctx, id, imp, choice, to)
	if err != nil {
		return nil, err
	}
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
		keepClosure(st, setup.Closure{Bank: id, Name: imp.Bank.Name, ClosedAt: res.ClosedAt, Lines: res.Lines})
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("record the close result: %w", err)
	}
	a.lifeMu.Lock()
	a.mu.Lock()
	b.State = setup.StateClosed
	a.mu.Unlock()
	a.lifeMu.Unlock()
	a.detach(b, setup.StepSite)
	return nil, nil
}

func (a *App) blocked(reason, format string, args ...any) *setup.Condition {
	return &setup.Condition{Type: setup.ConditionBlocked, Status: "True", Reason: reason, Message: fmt.Sprintf(format, args...), ChangedAt: a.clock.Real()}
}

func (a *App) closeChoices(r setup.Resource, imp CloseImpact) (map[string]string, string, *setup.Condition) {
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
		return nil, "", a.blocked(setup.ReasonMoneyDecisionNeeded, "%s holds %s for %s. Choose --money transfer --to <bank>, --money payout or --money leave. Nothing moves until you do.", imp.Bank.Name, naira(total), plural(held, "customer", "customers"))
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
	now := a.clock.Now()
	switch {
	case to == nil:
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s is not a bank in this setup, so money cannot move there. Choose another bank.", rm.To)
	case a.bankPhase(rm.To) != PhaseActive || a.bankDown(rm.To, now):
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s is suspended or down, so money cannot move there. Resume it or choose another bank.", to.Name)
	case a.bankFaulty(rm.To, now):
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s has a fault, so the money cannot be moved safely; clear it with beerus fault clear --bank %s.", to.Name, rm.To)
	case a.bankFaulty(r.ID, now):
		return nil, "", a.blocked(setup.ReasonTargetUnavailable, "%s has a fault, so the money cannot be moved safely; clear it with beerus fault clear --bank %s.", imp.Bank.Name, r.ID)
	}
	return choice, rm.To, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (a *App) closeBooks(ctx context.Context, id string, imp CloseImpact, choice map[string]string, to string) (CloseResult, error) {
	now := a.clock.Now()
	a.mu.Lock()
	from, dest := a.ledgerFor(id), a.ledgerFor(to)
	a.mu.Unlock()
	if from == nil {
		return CloseResult{}, fmt.Errorf("the books of %s are not open", id)
	}
	bk := from.Bank()
	payouts, err := bk.ClosurePayouts(from.ctx)
	if err != nil {
		return CloseResult{}, err
	}
	paid := map[string]int64{}
	for _, acc := range imp.Accounts {
		paid[acc.Owner] += payouts[acc.NUBAN]
	}
	for _, acc := range imp.Accounts {
		c := choice[acc.Owner]
		bal, err := bk.BalanceAt(from.ctx, acc.NUBAN, now)
		if err != nil {
			return CloseResult{}, err
		}
		if c == setup.MoneyLeave || bal <= 0 {
			continue
		}
		key := "close:" + id + ":" + acc.NUBAN
		if c == setup.MoneyPayout {
			amount := min(bal, imp.InsuredLimit-paid[acc.Owner])
			if amount <= 0 {
				continue
			}
			if _, err := bk.ClosurePayout(from.ctx, key, acc.NUBAN, amount, now); err != nil {
				return CloseResult{}, fmt.Errorf("pay out %s from %s: %w", naira(amount), acc.NUBAN, err)
			}
			paid[acc.Owner] += amount
			continue
		}
		if dest == nil {
			return CloseResult{}, fmt.Errorf("the books of %s are not open", to)
		}
		nuban, err := a.closureAccount(from, dest, acc, now)
		if err != nil {
			return CloseResult{}, err
		}
		tr, err := a.transfer(TransferRequest{FromBank: id, From: acc.NUBAN, ToBank: to, To: nuban, Amount: bal, Narration: "Closure of " + imp.Bank.Name, Key: key, Closure: true})
		if err != nil {
			return CloseResult{}, fmt.Errorf("move %s from %s: %w", naira(bal), acc.NUBAN, err)
		}
		if tr.Status != nip.Success {
			return CloseResult{}, fmt.Errorf("move %s from %s: session %s is %s", naira(bal), acc.NUBAN, tr.SessionID, tr.Status)
		}
	}
	for _, acc := range imp.Accounts {
		if choice[acc.Owner] != setup.MoneyPayout {
			continue
		}
		bal, err := bk.BalanceAt(from.ctx, acc.NUBAN, now)
		if err != nil {
			return CloseResult{}, err
		}
		if bal > 0 {
			if err := bk.FreezeAccount(from.ctx, acc.NUBAN); err != nil {
				return CloseResult{}, err
			}
		}
	}
	return a.closeOutcome(ctx, id, imp, choice, from)
}

func (a *App) closeOutcome(ctx context.Context, id string, imp CloseImpact, choice map[string]string, from *ledgerSource) (CloseResult, error) {
	b := a.bankByID(id)
	sw := a.nibssStore()
	if b == nil || sw == nil {
		return CloseResult{}, errors.New("NIBSS is not available right now, so the closure moves cannot be read back; try again soon")
	}
	sent, err := sw.ClosureSent(ctx, b.NIPCode)
	if err != nil {
		return CloseResult{}, fmt.Errorf("read back closure moves: %w", err)
	}
	bk := from.Bank()
	payouts, err := bk.ClosurePayouts(from.ctx)
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
		if _, ok := owner[x.FromNUBAN]; !ok {
			continue
		}
		if x.Status != nibss.SessionOK {
			return CloseResult{}, fmt.Errorf("closure session %s from %s is %s, not %s", x.ID, x.FromNUBAN, x.Status, nibss.SessionOK)
		}
		dest := targets[x.ToCode]
		if dest == nil {
			dest = &config.Bank{Name: x.ToCode}
		}
		res.Moves = append(res.Moves, Move{From: x.FromNUBAN, To: x.ToNUBAN, ToBank: dest.ID, Amount: x.Amount})
		moved := line(x.FromNUBAN, setup.OutcomeTransferred, x.Amount)
		moved.ToBank, moved.ToBankName, moved.ToNUBAN = dest.ID, dest.Name, x.ToNUBAN
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
	readAt := a.clock.Now()
	for _, acc := range imp.Accounts {
		bal, err := bk.BalanceAt(from.ctx, acc.NUBAN, readAt)
		if err != nil {
			return CloseResult{}, fmt.Errorf("read back the balance of %s: %w", acc.NUBAN, err)
		}
		if bal <= 0 {
			continue
		}
		held[acc.Owner] += bal
		switch choice[acc.Owner] {
		case setup.MoneyLeave:
			res.Lines = append(res.Lines, line(acc.NUBAN, setup.OutcomeLeft, bal))
			res.Left += bal
		case setup.MoneyPayout:
			status, err := bk.AccountStatus(from.ctx, acc.NUBAN)
			if err != nil {
				return CloseResult{}, err
			}
			if status != "frozen" {
				return CloseResult{}, fmt.Errorf("%s keeps %s as an NDIC claim but is %s, not frozen", acc.NUBAN, naira(bal), status)
			}
			res.Lines = append(res.Lines, line(acc.NUBAN, setup.OutcomeClaim, bal))
			res.Claims += bal
		default:
			return CloseResult{}, fmt.Errorf("%s still holds %s after its money was moved", acc.NUBAN, naira(bal))
		}
	}
	for o, c := range choice {
		if c == setup.MoneyPayout {
			if want := min(paid[o]+held[o], imp.InsuredLimit); paid[o] != want {
				return CloseResult{}, fmt.Errorf("customer %s was paid out %s, but the insured amount is %s", o, naira(paid[o]), naira(want))
			}
		}
	}
	return res, nil
}

func (a *App) closureAccount(from, dest *ledgerSource, acc ImpactAccount, at time.Time) (string, error) {
	cust, err := from.Bank().Customer(from.ctx, acc.Owner)
	if err != nil {
		return "", err
	}
	db := dest.Bank()
	holder, err := db.CustomerByBVN(dest.ctx, cust.BVN)
	if errors.Is(err, bank.ErrUnknownCustomer) {
		holder, err = db.CreateCustomer(dest.ctx, "close:"+from.inst.ID+":"+cust.ID, cust.Name, cust.Phone, cust.DOB, cust.BVN, "", "", at)
	}
	if err != nil {
		return "", fmt.Errorf("find %s at %s: %w", cust.Name, dest.inst.Name, err)
	}
	have, err := db.CustomerAccounts(dest.ctx, holder.ID)
	if err != nil {
		return "", err
	}
	for _, x := range have {
		if x.Product == acc.Type && x.Status != "closed" {
			return x.NUBAN, nil
		}
	}
	opened, err := db.OpenAccount(dest.ctx, "close:"+from.inst.ID+":"+acc.NUBAN, holder.ID, acc.Type, at)
	if err != nil {
		return "", fmt.Errorf("open an account for %s at %s: %w", cust.Name, dest.inst.Name, err)
	}
	return opened.NUBAN, nil
}

func (a *App) ctlCloseBank(w http.ResponseWriter, r *http.Request) {
	var req CloseRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"money":"transfer","to":"bank-a"}.`)
		return
	}
	res, err := a.CloseBank(r.PathValue("id"), req)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
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
	if findResource(&st, setup.KindBank, id) == nil {
		if c, ok := st.PurgedClosure(id); ok {
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
	writeJSON(w, http.StatusOK, c)
}
