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

func (a *App) stopRouting(r setup.Resource) (*setup.Condition, error) {
	return nil, a.setMemberStatus(r.ID, nibss.StatusTerminating)
}

func (a *App) decideMoney(r setup.Resource) (*setup.Condition, error) {
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
	res, err := a.closeBooks(id, imp, choice, to)
	if err != nil {
		return nil, err
	}
	a.lifeMu.Lock()
	a.mu.Lock()
	b.State = setup.StateClosed
	a.mu.Unlock()
	a.lifeMu.Unlock()
	a.detach(b, setup.StepSite)
	res.Bank, _ = a.bankStatus(id)
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("encode the close result: %w", err)
	}
	a.recordBank(id, func(r *setup.Resource) bool {
		r.Result = raw
		return true
	})
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
		return nil, "", a.blocked(setup.ReasonMoneyDecisionNeeded, "%s holds %s for %s. Run beerus apply --prune --money transfer --to <bank>.", imp.Bank.Name, naira(total), plural(held, "customer", "customers"))
	}
	for _, cu := range imp.Customers {
		choice[cu.ID] = rm.Money
	}
	needTarget := rm.Money != setup.MoneyLeave
	for cid, c := range rm.Customers {
		if _, ok := choice[cid]; !ok {
			return nil, "", a.blocked(setup.ReasonMoneyDecisionNeeded, "%s is not a customer of %s. Remove it from on_remove.customers.", cid, imp.Bank.Name)
		}
		choice[cid] = c
		needTarget = needTarget || c != setup.MoneyLeave
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

func (a *App) closeBooks(id string, imp CloseImpact, choice map[string]string, to string) (CloseResult, error) {
	now := a.clock.Now()
	res := CloseResult{Moves: []Move{}, Revoked: []string{}}
	a.mu.Lock()
	from, dest := a.ledgerFor(id), a.ledgerFor(to)
	a.mu.Unlock()
	if from == nil {
		return CloseResult{}, fmt.Errorf("the books of %s are not open", id)
	}
	paid, err := a.alreadyMoved(id, imp.Accounts)
	if err != nil {
		return CloseResult{}, err
	}
	for _, acc := range imp.Accounts {
		c := choice[acc.Owner]
		bal, err := from.Bank().BalanceAt(from.ctx, acc.NUBAN, now)
		if err != nil {
			return res, err
		}
		if c == "leave" || bal <= 0 {
			if bal > 0 {
				res.Left += bal
			}
			continue
		}
		amount := bal
		if c == "payout" {
			amount = min(amount, imp.InsuredLimit-paid[acc.Owner])
			paid[acc.Owner] += amount
			res.Claims += bal - amount
			if amount <= 0 {
				continue
			}
		}
		if dest == nil {
			return res, fmt.Errorf("the books of %s are not open", to)
		}
		nuban, err := a.closureAccount(from, dest, acc, now)
		if err != nil {
			return res, err
		}
		narr := "Closure of " + imp.Bank.Name
		if c == "payout" {
			narr = "NDIC payout, closure of " + imp.Bank.Name
		}
		tr, err := a.transfer(TransferRequest{FromBank: id, From: acc.NUBAN, ToBank: to, To: nuban, Amount: amount, Narration: narr, Key: "closure:" + id + ":" + acc.NUBAN, Closure: true})
		if err != nil {
			return res, fmt.Errorf("move %s from %s: %w", naira(amount), acc.NUBAN, err)
		}
		if tr.Status != nip.Success {
			return res, fmt.Errorf("move %s from %s: session %s is %s", naira(amount), acc.NUBAN, tr.SessionID, tr.Status)
		}
		res.Moves = append(res.Moves, Move{From: acc.NUBAN, To: nuban, ToBank: to, Amount: amount, Payout: c == "payout"})
		if c == "payout" {
			res.PaidOut += amount
		} else {
			res.Moved += amount
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.st.Connections))
	for cid := range a.st.Connections {
		ids = append(ids, cid)
	}
	sort.Strings(ids)
	for _, cid := range ids {
		c := a.st.Connections[cid]
		if c.Institution != id || c.Status == "revoked" || c.Status == "expired" {
			continue
		}
		for _, acct := range c.AccountIDs {
			a.emit(c.PartnerID, "account.removed", map[string]any{"connection_id": c.ID, "account_id": acct})
		}
		a.setStatus(c, "revoked", "connection_revoked")
		res.Revoked = append(res.Revoked, c.ID)
	}
	a.dirty()
	return res, nil
}

func (a *App) alreadyMoved(id string, accounts []ImpactAccount) (map[string]int64, error) {
	b := a.bankByID(id)
	sw := a.nibssStore()
	if b == nil || sw == nil {
		return nil, errors.New("NIBSS is not available right now, so earlier closure moves cannot be checked; try again soon")
	}
	sent, err := sw.ClosureSent(context.Background(), b.NIPCode)
	if err != nil {
		return nil, fmt.Errorf("check earlier closure moves: %w", err)
	}
	owner := map[string]string{}
	for _, acc := range accounts {
		owner[acc.NUBAN] = acc.Owner
	}
	paid := map[string]int64{}
	for _, x := range sent {
		if o, ok := owner[x.FromNUBAN]; ok {
			paid[o] += x.Amount
		}
	}
	return paid, nil
}

func (a *App) closureAccount(from, dest *ledgerSource, acc ImpactAccount, at time.Time) (string, error) {
	cust, err := from.Bank().Customer(from.ctx, acc.Owner)
	if err != nil {
		return "", err
	}
	db := dest.Bank()
	holder, err := db.CustomerByBVN(dest.ctx, cust.BVN)
	if errors.Is(err, bank.ErrUnknownCustomer) {
		holder, err = db.CreateCustomer(dest.ctx, "closure:"+from.inst.ID+":"+cust.ID, cust.Name, cust.Phone, cust.DOB, cust.BVN, "", "", at)
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
	opened, err := db.OpenAccount(dest.ctx, "closure:"+from.inst.ID+":"+acc.NUBAN, holder.ID, acc.Type, at)
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
