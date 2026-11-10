package connect

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/setup"
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
		writeJSON(w, http.StatusOK, map[string]any{"data": scenarios})
	})
	mux.HandleFunc("POST /scenarios/run", a.ctlScenarioRun)
	mux.HandleFunc("POST /scenarios/{name}", a.ctlScenario)
	mux.HandleFunc("GET /nip/name-enquiry", a.ctlNameEnquiry)
	mux.HandleFunc("GET /nip/participants", a.ctlNIPParticipants)
	mux.HandleFunc("GET /nip/positions", a.ctlNIPPositions)
	mux.HandleFunc("GET /nip/settlement", a.ctlNIPSettlement)
	mux.HandleFunc("POST /nip/settle-now", a.ctlSettleNow)
	mux.HandleFunc("GET /nip/sessions/{id}", a.ctlNIPSession)
	mux.HandleFunc("GET /faults", a.ctlFaults)
	mux.HandleFunc("POST /faults", a.ctlAddFault)
	mux.HandleFunc("DELETE /faults", a.ctlClearFaults)
	mux.HandleFunc("GET /internal/replica", a.ctlReplica)
	mux.HandleFunc("POST /internal/consents", a.ctlConsentRecord)
	mux.HandleFunc("POST /banks/{id}/net-debit-cap", a.ctlNetDebitCap)
	mux.HandleFunc("GET /connections", a.ctlConnections)
	mux.HandleFunc("POST /connections/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := a.RevokeAtBank(r.PathValue("id")); err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "No such connection.")
			return
		}
		a.poke()
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	})
	mux.HandleFunc("POST /plan", a.ctlPlan)
	mux.HandleFunc("POST /apply", a.ctlApply)
	mux.HandleFunc("GET /resources", a.ctlResources)
	mux.HandleFunc("GET /resources/{kind}/{id}", a.ctlResource)
	mux.HandleFunc("POST /partners", a.ctlAddPartner)
	mux.HandleFunc("POST /partners/{id}/rotate-key", a.ctlRotatePartner)
	mux.HandleFunc("DELETE /partners/{id}", a.ctlRemovePartner)
	mux.HandleFunc("GET /registry/clients", a.ctlRegistryClients)
	mux.HandleFunc("POST /registry/clients", a.ctlRegistryAdd)
	mux.HandleFunc("GET /registry/consents", a.ctlConsentLedger)
	mux.HandleFunc("GET /webhooks/deliveries", a.ctlDeliveries)
	mux.HandleFunc("POST /webhooks/deliveries/{id}/replay", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Replay(r.PathValue("id")); err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "No such delivery.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
	})
	mux.HandleFunc("GET /banks", func(w http.ResponseWriter, r *http.Request) {
		data := []BankStatus{}
		for _, st := range a.BankStatuses() {
			if state := r.URL.Query().Get("state"); state == "" || st.Phase == state {
				data = append(data, st)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "warnings": a.BankWarnings()})
	})
	mux.HandleFunc("POST /banks", a.ctlCreateBank)
	mux.HandleFunc("GET /banks/{id}/customers", a.ctlCustomers)
	mux.HandleFunc("POST /banks/{id}/customers", a.ctlCreateCustomer)
	mux.HandleFunc("POST /banks/{id}/customers/{customer}/close", a.ctlCloseCustomer)
	mux.HandleFunc("GET /banks/{id}/accounts", a.ctlAccounts)
	mux.HandleFunc("POST /banks/{id}/accounts", a.ctlOpenAccount)
	mux.HandleFunc("POST /banks/{id}/accounts/{nuban}/close", a.ctlCloseAccount)
	mux.HandleFunc("GET /banks/{id}/accounts/{nuban}/statement", a.ctlStatement)
	mux.HandleFunc("POST /banks/{id}/deposits", a.ctlCash("deposit"))
	mux.HandleFunc("POST /banks/{id}/withdrawals", a.ctlCash("withdrawal"))
	mux.HandleFunc("POST /banks/{id}/transfers", a.ctlBankTransfer)
	mux.HandleFunc("GET /banks/{id}/close", a.ctlCloseImpact)
	mux.HandleFunc("POST /banks/{id}/close", a.ctlCloseBank)
	mux.HandleFunc("GET /banks/{id}/purge", a.ctlPurgeImpact)
	mux.HandleFunc("POST /banks/{id}/purge", a.ctlPurgeBank)
	mux.HandleFunc("POST /banks/{id}/suspend", func(w http.ResponseWriter, r *http.Request) { a.ctlSuspend(w, r, true) })
	mux.HandleFunc("POST /banks/{id}/resume", func(w http.ResponseWriter, r *http.Request) { a.ctlSuspend(w, r, false) })
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
	if u, err := url.Parse(a.coreURL); err == nil && multiProcess() && u.Host != "" {
		hosts[u.Host] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); !remotePeers() && (err != nil || !net.ParseIP(host).IsLoopback()) {
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

func (a *App) advanceClock(by, to string) error {
	a.mu.Lock()
	var err error
	switch {
	case by != "":
		var d time.Duration
		if d, err = parseDuration(by); err == nil {
			_, err = a.clock.Advance(d)
		}
	case to != "":
		var t time.Time
		if t, err = time.Parse(time.RFC3339, to); err == nil {
			_, err = a.clock.AdvanceTo(t)
		}
	default:
		err = fmt.Errorf("by or to is required")
	}
	if err != nil {
		a.mu.Unlock()
		return err
	}
	a.dirty()
	a.mu.Unlock()
	a.Step()
	return nil
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
	if err := a.advanceClock(body.By, body.To); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
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

type scenario struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

var scenarios = []scenario{
	{"consent_revoked_mid_sync", "The customer revokes consent at the bank"},
	{"token_refresh_failed", "Connect can no longer refresh the bank token, so the connection needs reauthorisation"},
}

type scenarioParams struct {
	ConnectionID string `json:"connection_id"`
}

func (a *App) runScenario(name string, in scenarioParams) (string, error) {
	var err error
	switch name {
	case "consent_revoked_mid_sync":
		err = a.ForceStatus(in.ConnectionID, "syncing", "")
		if err == nil {
			err = a.RevokeAtBank(in.ConnectionID)
		}
	case "token_refresh_failed":
		err = a.ForceStatus(in.ConnectionID, "reauth_required", "reauth_required")
	default:
		return "", fmt.Errorf("unknown scenario %q; run beerus scenarios to list them", name)
	}
	if errors.Is(err, errNotFound) {
		return "", errors.New("connection_id must name an existing connection")
	}
	if err != nil {
		return "", fmt.Errorf("run scenario %s: %w", name, err)
	}
	a.poke()
	return "connection " + in.ConnectionID + " updated", nil
}

func (a *App) ctlScenario(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body scenarioParams
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The body must be a JSON object.")
			return
		}
	}
	result, err := a.runScenario(name, body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
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

func (a *App) ctlConnections(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []map[string]any{}
	for _, c := range a.st.Connections {
		m := a.connectionJSON(c)
		m["partner_id"] = c.PartnerID
		m["customer_id"] = c.CustomerID
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
	if a.role == RoleBank {
		return
	}
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
	SetupKey     uint64    `json:"setup_key,string"`
	Schema       int       `json:"schema"`
	TakenReal    time.Time `json:"taken_real"`
	Banks        []string  `json:"banks,omitempty"`
	Closed       []string  `json:"closed,omitempty"`
	CBN          bool      `json:"cbn,omitempty"`
	NIBSS        bool      `json:"nibss,omitempty"`
}

const schemaVersion = 2

type snapshotFile struct {
	Meta  snapshotMeta `json:"meta"`
	State *State       `json:"state"`
}

func (a *App) snapshotDir() string {
	return a.cfg.Path("snapshots")
}

func (a *App) TakeSnapshot(name string) (meta snapshotMeta, err error) {
	a.Step()
	resume, err := a.pauseBanks()
	if err != nil {
		return snapshotMeta{}, err
	}
	defer func() {
		if rerr := resume(); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}()
	a.gate.Lock()
	defer a.gate.Unlock()
	a.mu.Lock()
	a.st.Clock = a.clock.State()
	a.regMu.Lock()
	b, err := json.Marshal(a.st)
	a.regMu.Unlock()
	if err != nil {
		a.mu.Unlock()
		return snapshotMeta{}, err
	}
	cp := &State{}
	if err := json.Unmarshal(b, cp); err != nil {
		a.mu.Unlock()
		return snapshotMeta{}, err
	}
	cp.Deliveries = nil
	cp.Vault = nil
	cp.Registry = nil
	cp.Revokes = nil
	cp.ConsentLedger = nil
	cp.Idem = map[string]*IdemRec{}
	cp.WidgetSessions = map[string]*WidgetSession{}
	for _, ls := range cp.LinkSessions {
		ls.OTP, ls.Verifier, ls.BankState = "", "", ""
	}
	meta = snapshotMeta{ID: newID("snap"), Name: name, BusinessTime: ts(a.clock.Now()), SetupKey: a.setupKey, Schema: schemaVersion, TakenReal: a.clock.Real()}
	for _, b := range a.cfg.Banks {
		if b.Closed() {
			meta.Closed = append(meta.Closed, b.ID)
		}
	}
	ledgers, r, sw := slices.Clone(a.ledgers), a.rtgs, a.nibss
	a.mu.Unlock()
	snapID := meta.ID
	defer func() {
		if err != nil {
			err = errors.Join(err, a.discardSnapshot(snapID, ledgers, r))
		}
	}()
	for _, l := range ledgers {
		if err := l.Snapshot(meta.ID); err != nil {
			return snapshotMeta{}, err
		}
		meta.Banks = append(meta.Banks, l.inst.ID)
	}
	if r != nil {
		if err := r.Snapshot(meta.ID); err != nil {
			return snapshotMeta{}, err
		}
		meta.CBN = true
	}
	if err := a.snapshotNIBSS(sw, meta.ID); err != nil {
		return snapshotMeta{}, err
	}
	meta.NIBSS = true
	if err := saveJSON(filepath.Join(a.snapshotDir(), meta.ID+".json"), snapshotFile{Meta: meta, State: cp}); err != nil {
		return snapshotMeta{}, err
	}
	return meta, nil
}

func (a *App) discardSnapshot(id string, ledgers []*ledgerSource, r *rtgs) error {
	errs := []error{}
	for _, l := range ledgers {
		errs = append(errs, l.DropSnapshot(id))
	}
	if r != nil {
		errs = append(errs, r.DropSnapshot(id))
	}
	errs = append(errs, a.dropNIBSSSnapshot(id))
	return errors.Join(errs...)
}

func (a *App) RestoreSnapshot(id string) (result string, err error) {
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
	if f.Meta.SetupKey != a.setupKey {
		return "", fmt.Errorf("snapshot %s was taken in another setup and cannot be restored here", id)
	}
	if f.Meta.Schema != schemaVersion {
		return "", fmt.Errorf("snapshot %s was taken with schema %d; this setup uses schema %d", id, f.Meta.Schema, schemaVersion)
	}
	if !f.Meta.NIBSS {
		return "", fmt.Errorf("snapshot %s has no copy of NIBSS", id)
	}
	stored, err := a.ctl.status()
	if err != nil {
		return "", fmt.Errorf("read control.json: %w", err)
	}
	for _, r := range stored.Resources {
		if r.Phase != setup.PhaseClosing && r.Phase != setup.PhaseTerminating {
			continue
		}
		name := r.ID
		if b := a.bankByID(r.ID); b != nil && r.Kind == setup.KindBank {
			name = b.Name
		} else if r.Kind == setup.KindPartner {
			name = "partner " + r.ID
		}
		return "", fmt.Errorf("%s is %s, so a snapshot cannot be restored until that finishes", name, r.Phase)
	}
	a.mu.Lock()
	for _, bid := range f.Meta.Banks {
		if a.cfg.Bank(bid) == nil {
			a.mu.Unlock()
			return "", fmt.Errorf("snapshot %s was taken before %s was purged, and purging a bank cannot be undone", id, bid)
		}
	}
	for _, b := range a.cfg.Banks {
		if b.Closed() && !slices.Contains(f.Meta.Closed, b.ID) {
			a.mu.Unlock()
			return "", fmt.Errorf("snapshot %s was taken before %s closed, and closing a bank cannot be undone", id, b.Name)
		}
	}
	a.mu.Unlock()
	resume, err := a.pauseBanks()
	if err != nil {
		return "", err
	}
	defer func() {
		if rerr := resume(); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}()
	wid, err := a.restoreAll(id, f)
	if err != nil {
		return "", err
	}
	if _, err := a.settle(a.clock.Now(), false); err != nil {
		return "", fmt.Errorf("restored, but settlement failed: %w", err)
	}
	return wid, nil
}

func (a *App) restoreAll(id string, f snapshotFile) (string, error) {
	a.gate.Lock()
	defer a.gate.Unlock()
	a.mu.Lock()
	ledgers, r, sw := slices.Clone(a.ledgers), a.rtgs, a.nibss
	a.mu.Unlock()
	taken := map[string]bool{}
	for _, id := range f.Meta.Banks {
		taken[id] = true
	}
	restore := map[string]bool{}
	for _, l := range ledgers {
		ok, err := l.HasSnapshot(id)
		if err != nil {
			return "", err
		}
		switch {
		case ok:
			restore[l.inst.ID] = true
		case taken[l.inst.ID] || f.Meta.Banks == nil:
			return "", fmt.Errorf("snapshot %s has no copy of %s", id, l.inst.Name)
		}
	}
	for _, l := range ledgers {
		if restore[l.inst.ID] {
			if err := l.Restore(id); err != nil {
				return "", err
			}
		}
	}
	if r != nil {
		if err := r.Restore(id); err != nil {
			return "", err
		}
	}
	if err := a.restoreNIBSS(sw, id); err != nil {
		return "", err
	}
	a.mu.Lock()
	st := f.State
	st.fill()
	st.WorldID = newID("wld")
	st.Deliveries = a.st.Deliveries
	st.Registry = a.st.Registry
	st.Revokes = a.st.Revokes
	st.ConsentLedger = a.st.ConsentLedger
	prior := a.st
	a.st = st
	if err := a.ensureRegistryLocked(); err != nil {
		a.st = prior
		a.mu.Unlock()
		return "", err
	}
	for _, c := range st.Connections {
		switch c.Status {
		case "revoked", "expired", "reauth_required":
		default:
			a.setStatus(c, "reauth_required", "reauth_required")
		}
	}
	a.clock.Restore(st.Clock)
	a.mu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, rec := range a.st.Accounts {
		if c := a.st.Connections[rec.ConnectionID]; c != nil && !rec.Removed && c.Status != "reauth_required" {
			a.emit(c.PartnerID, "sync.available", map[string]any{"connection_id": c.ID, "account_id": rec.ID, "world_id": st.WorldID})
		}
	}
	a.dirty()
	a.poke()
	return st.WorldID, nil
}

func (a *App) snapshotMetas() ([]snapshotMeta, error) {
	entries, err := os.ReadDir(a.snapshotDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []snapshotMeta{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	out := []snapshotMeta{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(a.snapshotDir(), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read snapshot %s: %w", e.Name(), err)
		}
		var f struct {
			Meta snapshotMeta `json:"meta"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("decode snapshot %s: %w", e.Name(), err)
		}
		out = append(out, f.Meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TakenReal.Before(out[j].TakenReal) })
	return out, nil
}

func (a *App) ctlSnapshots(w http.ResponseWriter, r *http.Request) {
	out, err := a.snapshotMetas()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
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
