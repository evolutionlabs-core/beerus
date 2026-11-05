package connect

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/world"
)

var insuredLimit = map[string]int64{"commercial": 500_000_000, "digital": 500_000_000, "microfinance": 200_000_000}

type ImpactAccount struct {
	Key       string `json:"key"`
	NUBAN     string `json:"nuban"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Owner     string `json:"owner"`
	Balance   int64  `json:"balance_minor"`
	Pending   int64  `json:"pending_minor"`
	Connected bool   `json:"connected"`
}

type ImpactCustomer struct {
	Persona   string `json:"persona"`
	Name      string `json:"name"`
	Accounts  int    `json:"accounts"`
	Deposits  int64  `json:"deposits_minor"`
	Insured   int64  `json:"insured_minor"`
	Uninsured int64  `json:"uninsured_minor"`
}

type ImpactConnection struct {
	ID      string `json:"id"`
	Partner string `json:"partner"`
	Persona string `json:"persona"`
	Status  string `json:"status"`
}

type CloseImpact struct {
	Bank         BankStatus         `json:"bank"`
	InsuredLimit int64              `json:"insured_limit_minor"`
	Deposits     int64              `json:"deposits_minor"`
	Insured      int64              `json:"insured_minor"`
	Uninsured    int64              `json:"uninsured_minor"`
	Pending      int64              `json:"pending_minor"`
	Customers    []ImpactCustomer   `json:"customers"`
	Accounts     []ImpactAccount    `json:"accounts"`
	Connections  []ImpactConnection `json:"connections"`
	Partners     []string           `json:"partners"`
	Blockers     []string           `json:"blockers"`
}

func (a *App) CloseImpact(id string) (CloseImpact, error) {
	st, ok := a.bankStatus(id)
	b := a.cfg.Bank(id)
	if !ok || b == nil {
		return CloseImpact{}, errNotFound
	}
	out := CloseImpact{Bank: st, InsuredLimit: insuredLimit[b.Type], Customers: []ImpactCustomer{}, Accounts: []ImpactAccount{}, Connections: []ImpactConnection{}, Partners: []string{}, Blockers: []string{}}
	switch st.Phase {
	case PhaseProvisioning, PhaseRepairing:
		out.Blockers = append(out.Blockers, "the bank is "+st.Phase+"; wait until it is active or suspended")
	case PhaseClosed:
		out.Blockers = append(out.Blockers, "the bank is already closed")
	}
	for _, other := range a.cfg.Banks {
		if other.SettlesThrough == id && !other.Closed() {
			out.Blockers = append(out.Blockers, other.Name+" settles through this bank; close it first")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	connected := map[string]bool{}
	partners := map[string]bool{}
	for _, c := range a.st.Connections {
		if c.Institution != id || c.Status == "revoked" || c.Status == "expired" {
			continue
		}
		out.Connections = append(out.Connections, ImpactConnection{ID: c.ID, Partner: c.PartnerID, Persona: c.PersonaID, Status: c.Status})
		partners[c.PartnerID] = true
	}
	for _, rec := range a.st.Accounts {
		if c := a.st.Connections[rec.ConnectionID]; c != nil && c.Institution == id && c.Status != "revoked" && c.Status != "expired" {
			connected[rec.WorldKey] = true
		}
	}
	customers := map[string]*ImpactCustomer{}
	for _, acc := range a.world.Accounts() {
		if acc.Institution != id || (!acc.OpenedAt.IsZero() && acc.OpenedAt.After(now)) {
			continue
		}
		bal, _ := a.world.LedgerAt(acc.Key, now)
		pend, _ := a.world.PendingAt(acc.Key, now)
		out.Accounts = append(out.Accounts, ImpactAccount{Key: acc.Key, NUBAN: acc.NUBAN, Name: acc.Name, Type: acc.Type, Owner: acc.Owner, Balance: bal, Pending: pend, Connected: connected[acc.Key]})
		out.Pending += pend
		cu := customers[acc.Owner]
		if cu == nil {
			cu = &ImpactCustomer{Persona: acc.Owner}
			if p, ok := a.world.Persona(acc.Owner); ok {
				cu.Name = p.Name
			}
			customers[acc.Owner] = cu
		}
		cu.Accounts++
		if bal > 0 {
			cu.Deposits += bal
		}
	}
	for _, cu := range customers {
		cu.Insured = min(cu.Deposits, out.InsuredLimit)
		cu.Uninsured = cu.Deposits - cu.Insured
		out.Deposits += cu.Deposits
		out.Insured += cu.Insured
		out.Uninsured += cu.Uninsured
		out.Customers = append(out.Customers, *cu)
	}
	for p := range partners {
		out.Partners = append(out.Partners, p)
	}
	sort.Slice(out.Customers, func(i, j int) bool { return out.Customers[i].Deposits > out.Customers[j].Deposits })
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].Key < out.Accounts[j].Key })
	sort.Slice(out.Connections, func(i, j int) bool { return out.Connections[i].ID < out.Connections[j].ID })
	sort.Strings(out.Partners)
	return out, nil
}

func (a *App) ctlCloseImpact(w http.ResponseWriter, r *http.Request) {
	imp, err := a.CloseImpact(r.PathValue("id"))
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, imp)
}

func (a *App) openInstitutions() []world.Institution {
	now := a.clock.Now()
	out := []world.Institution{}
	for _, inst := range a.world.Institutions() {
		if !a.world.Closed(inst.ID, now) {
			out = append(out, inst)
		}
	}
	return out
}

var moneyChoices = map[string]bool{"transfer": true, "payout": true, "leave": true}

type CloseRequest struct {
	Money     string            `json:"money"`
	To        string            `json:"to"`
	Customers map[string]string `json:"customers"`
}

type CloseResult struct {
	Bank    BankStatus   `json:"bank"`
	Moved   int64        `json:"moved_minor"`
	PaidOut int64        `json:"paid_out_minor"`
	Claims  int64        `json:"claims_minor"`
	Left    int64        `json:"left_minor"`
	Moves   []world.Move `json:"moves"`
	Revoked []string     `json:"revoked_connections"`
}

func (a *App) CloseBank(id string, req CloseRequest) (CloseResult, error) {
	a.recMu.Lock()
	defer a.recMu.Unlock()
	imp, err := a.CloseImpact(id)
	if err != nil {
		return CloseResult{}, err
	}
	if len(imp.Blockers) > 0 {
		return CloseResult{}, invalid("%s cannot be closed: %s", imp.Bank.Name, strings.Join(imp.Blockers, "; "))
	}
	if req.Money == "" {
		req.Money = "transfer"
	}
	if !moneyChoices[req.Money] {
		return CloseResult{}, invalid("money must be transfer, payout or leave")
	}
	choice := map[string]string{}
	for _, cu := range imp.Customers {
		choice[cu.Persona] = req.Money
	}
	for p, c := range req.Customers {
		if _, ok := choice[p]; !ok {
			return CloseResult{}, invalid("%s is not a customer of %s", p, imp.Bank.Name)
		}
		if !moneyChoices[c] {
			return CloseResult{}, invalid("the choice for %s must be transfer, payout or leave", p)
		}
		choice[p] = c
	}
	needTarget := false
	for _, c := range choice {
		needTarget = needTarget || c != "leave"
	}
	if needTarget {
		to := a.cfg.Bank(req.To)
		switch {
		case req.To == "":
			return CloseResult{}, invalid("choose the bank that receives the money with to")
		case to == nil:
			return CloseResult{}, invalid("unknown bank %q", req.To)
		case req.To == id:
			return CloseResult{}, invalid("the money cannot go to the bank that is closing")
		case a.bankPhase(req.To) != PhaseActive || a.world.Down(req.To, a.clock.Now()):
			return CloseResult{}, invalid("%s is not active, so it cannot receive the money", to.Name)
		}
	}
	a.lifeMu.Lock()
	b := a.cfg.Bank(id)
	st := a.status[id]
	prev, prevPhase := b.State, st.Phase
	b.State, st.Phase, st.Reason = "closed", PhaseClosed, ""
	saved, v, err := a.recordBanksLocked()
	if err != nil {
		b.State, st.Phase = prev, prevPhase
		a.lifeMu.Unlock()
		return CloseResult{}, err
	}
	a.lifeMu.Unlock()
	if err := a.ctl.flush(saved, v); err != nil {
		return CloseResult{}, err
	}
	res, err := a.closeWorld(id, imp, choice, req.To)
	if err != nil {
		a.lifeMu.Lock()
		b.State, st.Phase = prev, prevPhase
		saved, v, rerr := a.recordBanksLocked()
		a.lifeMu.Unlock()
		if rerr == nil {
			rerr = a.ctl.flush(saved, v)
		}
		return CloseResult{}, errors.Join(err, rerr)
	}
	a.detach(b, []int{3})
	res.Bank, _ = a.bankStatus(id)
	a.poke()
	return res, nil
}

func (a *App) closeWorld(id string, imp CloseImpact, choice map[string]string, to string) (CloseResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	res := CloseResult{Moves: []world.Move{}, Revoked: []string{}}
	paid := map[string]int64{}
	for _, acc := range imp.Accounts {
		c := choice[acc.Owner]
		bal, err := a.world.LedgerAt(acc.Key, now)
		if err != nil {
			return CloseResult{}, err
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
		dest, err := a.destination(acc.Owner, acc.Type, to, now)
		if err != nil {
			return CloseResult{}, err
		}
		res.Moves = append(res.Moves, world.Move{From: acc.Key, To: dest, Amount: amount, Payout: c == "payout"})
		if c == "payout" {
			res.PaidOut += amount
		} else {
			res.Moved += amount
		}
	}
	if err := a.world.Close(id, now, res.Moves); err != nil {
		return CloseResult{}, err
	}
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

func (a *App) destination(persona, typ, bank string, now time.Time) (string, error) {
	have := a.world.AccountsAt(persona, bank)
	for _, acc := range have {
		if acc.Type == typ {
			return acc.Key, nil
		}
	}
	if len(have) > 0 {
		return have[0].Key, nil
	}
	acc, err := a.world.OpenAccount(persona, bank, "current", now)
	if err != nil {
		return "", invalid("could not open an account for %s at %s: %s", persona, bank, transferMessage(err))
	}
	return acc.Key, nil
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
	writeJSON(w, http.StatusOK, res)
}
