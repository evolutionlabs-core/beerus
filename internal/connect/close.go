package connect

import (
	"net/http"
	"sort"
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
	if st.Phase == PhaseProvisioning || st.Phase == PhaseRepairing {
		out.Blockers = append(out.Blockers, "the bank is "+st.Phase+"; wait until it is active or suspended")
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
