package connect

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

func (a *App) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "world_id": a.WorldID(), "business_time": ts(a.clock.Now())})
	})
	mux.HandleFunc("GET /clock", a.ctlClock)
	mux.HandleFunc("POST /clock/advance", a.ctlAdvance)
	mux.HandleFunc("POST /clock/pace", a.ctlPace)
	mux.HandleFunc("GET /scenarios", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": append(world.Scenarios, world.Scenario{Name: "token_refresh_failed", Description: "Connect can no longer refresh the bank token, so the connection needs reauthorisation"})})
	})
	mux.HandleFunc("POST /scenarios/{name}", a.ctlScenario)
	mux.HandleFunc("GET /personas", a.ctlPersonas)
	mux.HandleFunc("POST /transfers", a.ctlTransfer)
	mux.HandleFunc("GET /connections", a.ctlConnections)
	mux.HandleFunc("POST /connections/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := a.RevokeAtBank(r.PathValue("id")); err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "No such connection.")
			return
		}
		a.poke()
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	})
	mux.HandleFunc("GET /webhooks/deliveries", a.ctlDeliveries)
	mux.HandleFunc("POST /webhooks/deliveries/{id}/replay", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Replay(r.PathValue("id")); err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "No such delivery.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
	})
	mux.HandleFunc("GET /snapshots", a.ctlSnapshots)
	mux.HandleFunc("POST /snapshots", a.ctlTakeSnapshot)
	mux.HandleFunc("POST /snapshots/{id}/restore", a.ctlRestore)
	mux.HandleFunc("POST /sync", func(w http.ResponseWriter, r *http.Request) {
		a.SyncAll()
		writeJSON(w, http.StatusOK, map[string]string{"status": "synced"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	api := a.withRequestID(a.localOnly(mux))
	room := a.roomHandler()
	top := http.NewServeMux()
	top.Handle("/ui", room)
	top.Handle("/ui/", room)
	top.Handle("/assets/", room)
	top.Handle("/", api)
	return top
}

func (a *App) localOnly(next http.Handler) http.Handler {
	port := strconv.Itoa(a.cfg.Connect.ControlPort)
	hosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true, "[::1]:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !net.ParseIP(host).IsLoopback() {
			writeError(w, r, http.StatusForbidden, "forbidden", "The control API only accepts local connections.")
			return
		}
		if !hosts[r.Host] {
			writeError(w, r, http.StatusForbidden, "forbidden", "Unexpected Host header.")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			writeError(w, r, http.StatusForbidden, "forbidden", "Browser requests are refused.")
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(a.ctlToken)) != 1 {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "A valid control token is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) WorldID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st.WorldID
}

func (a *App) ctlClock(w http.ResponseWriter, r *http.Request) {
	st := a.clock.State()
	writeJSON(w, http.StatusOK, map[string]any{"business_time": ts(a.clock.Now()), "business_date": a.clock.Today().String(), "pace": st.Pace, "world_id": a.WorldID()})
}

var dayRe = regexp.MustCompile(`^(\d+)d$`)

func parseDuration(s string) (time.Duration, error) {
	if m := dayRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func (a *App) ctlAdvance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		By string `json:"by"`
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"by":"24h"}, {"by":"365d"} or {"to":"2027-01-01T09:00:00+01:00"}.`)
		return
	}
	a.mu.Lock()
	var err error
	switch {
	case body.By != "":
		var d time.Duration
		if d, err = parseDuration(body.By); err == nil {
			_, err = a.clock.Advance(d)
		}
	case body.To != "":
		var t time.Time
		if t, err = time.Parse(time.RFC3339, body.To); err == nil {
			_, err = a.clock.AdvanceTo(t)
		}
	default:
		err = fmt.Errorf("by or to is required")
	}
	if err != nil {
		a.mu.Unlock()
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	a.dirty()
	a.mu.Unlock()
	a.Step()
	a.ctlClock(w, r)
}

func (a *App) ctlPace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pace float64 `json:"pace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"pace":60}.`)
		return
	}
	if err := a.clock.SetPace(body.Pace); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	a.mu.Lock()
	a.dirty()
	a.mu.Unlock()
	a.ctlClock(w, r)
}

func (a *App) resolveAccount(id string) (string, bool) {
	if rec := a.st.Accounts[id]; rec != nil {
		return rec.WorldKey, true
	}
	if _, ok := a.world.Account(id); ok {
		return id, true
	}
	return "", false
}

func (a *App) ctlScenario(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		AccountID    string `json:"account_id"`
		Institution  string `json:"institution"`
		Persona      string `json:"persona"`
		ConnectionID string `json:"connection_id"`
		Duration     string `json:"duration"`
		Count        int    `json:"count"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The body must be a JSON object.")
			return
		}
	}
	switch name {
	case "consent_revoked_mid_sync", "token_refresh_failed":
		var err error
		if name == "consent_revoked_mid_sync" {
			err = a.ForceStatus(body.ConnectionID, "syncing", "")
			if err == nil {
				err = a.RevokeAtBank(body.ConnectionID)
			}
		} else {
			err = a.ForceStatus(body.ConnectionID, "reauth_required", "reauth_required")
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "connection_id must name an existing connection.")
			return
		}
		a.poke()
		writeJSON(w, http.StatusOK, map[string]string{"scenario": name, "result": "connection " + body.ConnectionID + " updated"})
		return
	}
	a.mu.Lock()
	p := world.ScenarioParams{Institution: body.Institution, Persona: body.Persona, Count: body.Count}
	if body.AccountID != "" {
		key, ok := a.resolveAccount(body.AccountID)
		if !ok {
			a.mu.Unlock()
			writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is not known.")
			return
		}
		p.Account = key
	}
	if body.Duration != "" {
		d, err := parseDuration(body.Duration)
		if err != nil {
			a.mu.Unlock()
			writeError(w, r, http.StatusBadRequest, "invalid_request", "duration must look like 2h or 3d.")
			return
		}
		p.Duration = d
	}
	result, err := a.world.RunScenario(name, a.clock.Now(), p)
	if err == nil {
		a.dirty()
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	a.poke()
	writeJSON(w, http.StatusOK, map[string]string{"scenario": name, "result": result})
}

func parseNaira(s string) (int64, error) {
	whole, frac, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), ".")
	if whole == "" || len(frac) > 2 || strings.Trim(whole, "0123456789") != "" || strings.Trim(frac, "0123456789") != "" {
		return 0, fmt.Errorf("amount %q must look like 25000 or 25000.50 (naira)", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("amount %q must be a positive naira amount", s)
	}
	return n, nil
}

func naira(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	n := strconv.FormatInt(minor/100, 10)
	out := ""
	for i, c := range n {
		if i > 0 && (len(n)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return fmt.Sprintf("%s₦%s.%02d", sign, out, minor%100)
}

func (a *App) ctlTransfer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From      string `json:"from"`
		To        string `json:"to"`
		ToBank    string `json:"to_bank"`
		ToName    string `json:"to_name"`
		Amount    string `json:"amount"`
		Narration string `json:"narration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"from":"...","to":"...","amount":"25000"}.`)
		return
	}
	amount, err := parseNaira(body.Amount)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	a.mu.Lock()
	from, to := body.From, body.To
	if key, ok := a.resolveAccount(from); ok {
		from = key
	}
	if key, ok := a.resolveAccount(to); ok {
		to = key
	}
	res, err := a.world.Transfer(a.clock.Now(), world.TransferRequest{From: from, To: to, ToBank: body.ToBank, ToName: body.ToName, Amount: amount, Narration: body.Narration})
	if err == nil {
		a.dirty()
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	a.SyncAll()
	writeJSON(w, http.StatusOK, map[string]any{
		"result":        res,
		"amount":        naira(res.Amount),
		"charges":       naira(res.Fee + res.VAT + res.StampDuty),
		"total_debit":   naira(res.TotalDebit),
		"sender_after":  naira(res.BalanceAfter),
		"business_time": ts(a.clock.Now()),
	})
}

func (a *App) ctlPersonas(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []map[string]any{}
	for _, p := range a.world.Personas() {
		accts := []map[string]any{}
		for _, k := range p.Accounts {
			acc, _ := a.world.Account(k)
			inst, _ := a.world.Institution(acc.Institution)
			accts = append(accts, map[string]any{"key": acc.Key, "institution": inst.ID, "bank": inst.Name, "nuban": acc.NUBAN, "name": acc.Name, "type": acc.Type})
		}
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "username": p.Username, "password": p.Password, "accounts": accts})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) ctlConnections(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []map[string]any{}
	for _, c := range a.st.Connections {
		m := a.connectionJSON(c)
		m["partner_id"] = c.PartnerID
		m["persona"] = c.PersonaID
		m["account_ids"] = c.AccountIDs
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["created_at"].(string) < out[j]["created_at"].(string) })
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) ctlDeliveries(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	out := []map[string]any{}
	for i := len(a.st.Deliveries) - 1; i >= 0 && len(out) < limit; i-- {
		d := a.st.Deliveries[i]
		out = append(out, map[string]any{"id": d.ID, "type": d.Type, "status": d.Status, "attempts": d.Attempts, "last_status": d.LastStatus, "last_error": d.LastError, "body": d.Body})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) SyncAll() {
	a.mu.Lock()
	now := a.clock.Now()
	for _, c := range a.st.Connections {
		if c.Status == "active" || c.Status == "syncing" || c.Status == "institution_down" {
			a.syncConnection(c, now)
		}
	}
	a.mu.Unlock()
	a.Step()
}

type snapshotMeta struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	BusinessTime string    `json:"business_time"`
	Seed         int64     `json:"seed"`
	Schema       int       `json:"schema"`
	TakenReal    time.Time `json:"taken_real"`
}

const schemaVersion = 1

type snapshotFile struct {
	Meta  snapshotMeta `json:"meta"`
	State *State       `json:"state"`
}

func (a *App) snapshotDir() string {
	return a.cfg.Path("snapshots")
}

func (a *App) TakeSnapshot(name string) (snapshotMeta, error) {
	a.Step()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.Clock = a.clock.State()
	a.st.World = a.world.Overrides()
	b, err := json.Marshal(a.st)
	if err != nil {
		return snapshotMeta{}, err
	}
	cp := &State{}
	if err := json.Unmarshal(b, cp); err != nil {
		return snapshotMeta{}, err
	}
	cp.Deliveries = nil
	cp.Idem = map[string]*IdemRec{}
	cp.WidgetSessions = map[string]*WidgetSession{}
	for _, ls := range cp.LinkSessions {
		ls.OTP = ""
	}
	meta := snapshotMeta{ID: newID("snap"), Name: name, BusinessTime: ts(a.clock.Now()), Seed: a.cfg.Seed, Schema: schemaVersion, TakenReal: a.clock.Real()}
	if err := saveJSON(filepath.Join(a.snapshotDir(), meta.ID+".json"), snapshotFile{Meta: meta, State: cp}); err != nil {
		return snapshotMeta{}, err
	}
	return meta, nil
}

func (a *App) RestoreSnapshot(id string) (string, error) {
	if strings.ContainsAny(id, `/\.`) {
		return "", errNotFound
	}
	b, err := os.ReadFile(filepath.Join(a.snapshotDir(), id+".json"))
	if err != nil {
		return "", errNotFound
	}
	var f snapshotFile
	if err := json.Unmarshal(b, &f); err != nil {
		return "", err
	}
	if f.Meta.Schema != schemaVersion || f.Meta.Seed != a.cfg.Seed {
		return "", fmt.Errorf("snapshot %s was taken with schema %d and seed %d; this setup uses schema %d and seed %d", id, f.Meta.Schema, f.Meta.Seed, schemaVersion, a.cfg.Seed)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := f.State
	st.fill()
	st.WorldID = newID("wld")
	st.Deliveries = a.st.Deliveries
	a.st = st
	a.clock.Restore(st.Clock)
	genesis, _ := a.cfg.GenesisDate()
	a.world = world.New(world.Config{Seed: a.cfg.Seed, Genesis: genesis}, st.World)
	for _, rec := range a.st.Accounts {
		if c := a.st.Connections[rec.ConnectionID]; c != nil && !rec.Removed {
			a.emit(c.PartnerID, "sync.available", map[string]any{"connection_id": c.ID, "account_id": rec.ID, "world_id": st.WorldID})
		}
	}
	a.dirty()
	a.poke()
	return st.WorldID, nil
}

func (a *App) ctlSnapshots(w http.ResponseWriter, r *http.Request) {
	entries, _ := os.ReadDir(a.snapshotDir())
	out := []snapshotMeta{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(a.snapshotDir(), e.Name()))
		if err != nil {
			continue
		}
		var f struct {
			Meta snapshotMeta `json:"meta"`
		}
		if json.Unmarshal(b, &f) == nil {
			out = append(out, f.Meta)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TakenReal.Before(out[j].TakenReal) })
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) ctlTakeSnapshot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	meta, err := a.TakeSnapshot(body.Name)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, meta)
}

func (a *App) ctlRestore(w http.ResponseWriter, r *http.Request) {
	wid, err := a.RestoreSnapshot(r.PathValue("id"))
	if err == errNotFound {
		writeError(w, r, http.StatusNotFound, "not_found", "No such snapshot.")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusConflict, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"world_id": wid, "business_time": ts(a.clock.Now()), "business_date": clock.DateOf(a.clock.Now()).String()})
}
