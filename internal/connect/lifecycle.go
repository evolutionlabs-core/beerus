package connect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	PhaseProvisioning  = "provisioning"
	PhaseActive        = "active"
	PhaseSuspended     = "suspended"
	PhaseRepairing     = "repairing"
	PhaseClosed        = "closed"
	driftEvery         = 5 * time.Second
	defaultMaxBanks    = 6
	connectionsPerBank = 8
)

var (
	ErrBankInvalid = errors.New("invalid bank")
	slugRe         = regexp.MustCompile(`[^a-z0-9]+`)
	idRe           = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	databaseRe     = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	stepNames      = []string{"Record in manifest", "Register the bank", "Create database and ledger", "Start online banking", "Health check"}
	bankTypes      = map[string]int{"commercial": 730, "digital": 180, "microfinance": 365}
)

type BankStep struct {
	Name  string    `json:"name"`
	Done  bool      `json:"done"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at,omitempty"`
}

type BankStatus struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Phase  string     `json:"phase"`
	Reason string     `json:"reason,omitempty"`
	URL    string     `json:"online_banking"`
	Steps  []BankStep `json:"steps"`
}

type siteServer struct {
	srv *http.Server
}

func newStatus(b *config.Bank, done bool) *BankStatus {
	st := &BankStatus{ID: b.ID, Name: b.Name, Phase: PhaseProvisioning, URL: b.URL()}
	for _, n := range stepNames {
		st.Steps = append(st.Steps, BankStep{Name: n, Done: done})
	}
	if done {
		st.Phase = PhaseActive
		if b.Suspended() {
			st.Phase = PhaseSuspended
		}
	}
	if b.Closed() {
		st.Phase = PhaseClosed
	}
	return st
}

func (a *App) initLifecycle() {
	if raw, err := os.ReadFile(a.cfg.Path("beerus.yaml")); err == nil {
		a.cfgSum = config.HashKey(string(raw))
	}
	a.status = map[string]*BankStatus{}
	a.sites = map[string]*siteServer{}
	a.strikes = map[string]int{}
	for _, b := range a.cfg.Banks {
		a.status[b.ID] = newStatus(b, a.worldOnly || a.ledgerFor(b.ID) != nil)
		a.alignSuspensionLocked(b)
	}
}

func (a *App) alignSuspensionLocked(b *config.Bank) {
	if b.Closed() {
		return
	}
	now := a.clock.Now()
	changed := false
	if b.Suspended() {
		changed = a.world.Suspend(b.ID, now)
	} else {
		changed = a.world.Resume(b.ID, now)
	}
	if changed {
		a.dirty()
	}
}

func (a *App) BankStatuses() []BankStatus {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	out := []BankStatus{}
	for _, b := range a.cfg.Banks {
		if st := a.status[b.ID]; st != nil {
			cp := *st
			cp.Steps = append([]BankStep(nil), st.Steps...)
			out = append(out, cp)
		}
	}
	return out
}

func (a *App) bankPhase(id string) string {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	if st := a.status[id]; st != nil {
		return st.Phase
	}
	if a.cfg.Bank(id) == nil {
		return PhaseActive
	}
	return PhaseProvisioning
}

func (a *App) bankReady(id string) bool {
	return a.bankPhase(id) != PhaseProvisioning
}

func (a *App) BankWarnings() []string {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	return append([]string(nil), a.warnings...)
}

func (a *App) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	a.warnings = append(a.warnings, msg)
	a.log.Warn(msg)
}

func (a *App) saveBanksLocked() error {
	disk, err := config.Load(a.cfg.Dir)
	if err != nil {
		disk = a.cfg
	} else {
		disk.Banks = a.cfg.Banks
	}
	if err := disk.Save(); err != nil {
		return err
	}
	if raw, err := os.ReadFile(a.cfg.Path("beerus.yaml")); err == nil {
		a.cfgSum = config.HashKey(string(raw))
	}
	return nil
}

type BankRequest struct {
	Name           string `json:"name"`
	ID             string `json:"id"`
	Type           string `json:"type"`
	NIPCode        string `json:"nip_code"`
	NUBANCode      string `json:"nuban_code"`
	HistoryDays    int    `json:"history_days"`
	SettlesThrough string `json:"settles_through"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBankInvalid, fmt.Sprintf(format, args...))
}

func portFree(p int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err != nil {
			if host == "::1" {
				continue
			}
			return false
		}
		l.Close()
	}
	return true
}

func (a *App) postgresRoom(ctx context.Context) error {
	a.mu.Lock()
	if len(a.ledgers) == 0 {
		a.mu.Unlock()
		return nil
	}
	pool := a.ledgers[0].st.Pool
	a.mu.Unlock()
	var max, used int
	if err := pool.QueryRow(ctx, `SELECT setting::int FROM pg_settings WHERE name = 'max_connections'`).Scan(&max); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity`).Scan(&used); err != nil {
		return err
	}
	if used+connectionsPerBank+5 > max {
		return invalid("Postgres has %d of %d connections in use; another bank needs up to %d", used, max, connectionsPerBank)
	}
	return nil
}

func (a *App) CreateBank(req BankRequest) (*config.Bank, error) {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	if len(a.cfg.Banks) == 0 {
		return nil, invalid("this setup runs the built-in banks without ledgers; create a new setup with beerus init to manage banks")
	}
	b, err := a.admit(req, 0, "")
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cfg.Banks = append(a.cfg.Banks, b)
	a.mu.Unlock()
	if err := a.saveBanksLocked(); err != nil {
		a.mu.Lock()
		a.cfg.Banks = a.cfg.Banks[:len(a.cfg.Banks)-1]
		a.mu.Unlock()
		return nil, err
	}
	a.status[b.ID] = newStatus(b, false)
	a.poke()
	return b, nil
}

func (a *App) admit(req BankRequest, port int, database string) (*config.Bank, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 40 {
		return nil, invalid("name must be 1 to 40 characters")
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	}
	if !idRe.MatchString(id) {
		return nil, invalid("id %q must be lowercase letters, digits and dashes", id)
	}
	if id == cbnID {
		return nil, invalid("id %q is reserved for the CBN", id)
	}
	if reservedIDs[id] {
		return nil, invalid("id %q is reserved for NIBSS", id)
	}
	if database != "" && database == config.DatabaseName(a.cfg.Dir, cbnID) {
		return nil, invalid("database %q belongs to the CBN", database)
	}
	typ := req.Type
	if typ == "" {
		typ = "commercial"
	}
	days, ok := bankTypes[typ]
	if !ok {
		return nil, invalid("type must be commercial, digital or microfinance")
	}
	if req.HistoryDays > 0 {
		if req.HistoryDays > 730 {
			return nil, invalid("history_days must be at most 730")
		}
		days = req.HistoryDays
	}
	max := a.cfg.MaxBanks
	if max == 0 {
		max = defaultMaxBanks
	}
	if len(a.cfg.Banks) >= max {
		return nil, invalid("this setup already has %d banks, the limit set by max_banks", max)
	}
	usedNIP, usedNUBAN := map[string]bool{}, map[string]bool{}
	for _, p := range a.world.PurgedInstitutions() {
		if strings.EqualFold(p.Name, name) {
			return nil, invalid("%q was the name of a purged bank, and it stays reserved", name)
		}
		if p.ID == id {
			return nil, invalid("%q was the id of a purged bank, and it stays reserved", id)
		}
		usedNIP[p.NIPCode], usedNUBAN[p.NUBANCode] = true, true
	}
	declared := map[string]bool{}
	for _, b := range a.cfg.Banks {
		declared[b.NIPCode] = true
	}
	reservedNIP := map[string]bool{}
	for _, o := range world.CounterpartyBanks() {
		if declared[o.NIPCode] {
			continue
		}
		reservedNIP[o.NIPCode] = true
		if strings.EqualFold(o.Name, name) || strings.Trim(slugRe.ReplaceAllString(strings.ToLower(o.Name), "-"), "-") == id {
			return nil, invalid("%q is the name of an outside bank that transfers already use", o.Name)
		}
	}
	usedPort := map[int]bool{a.cfg.Connect.PartnerPort: true, a.cfg.Connect.WidgetPort: true, a.cfg.Connect.ControlPort: true}
	maxPort := 0
	for _, b := range a.cfg.Banks {
		if strings.EqualFold(b.Name, name) {
			return nil, invalid("a bank named %q already exists", name)
		}
		if b.ID == id {
			return nil, invalid("a bank with id %q already exists", id)
		}
		if database != "" && b.Database == database {
			return nil, invalid("database %q is already used by %s", database, b.ID)
		}
		usedNIP[b.NIPCode], usedNUBAN[b.NUBANCode], usedPort[b.Port] = true, true, true
		if b.Port > maxPort {
			maxPort = b.Port
		}
	}
	nip := req.NIPCode
	if nip == "" {
		for n := 999005; n < 999999; n++ {
			c := strconv.Itoa(n)
			if !usedNIP[c] && !reservedNIP[c] {
				nip = c
				break
			}
		}
	} else if !regexp.MustCompile(`^\d{6}$`).MatchString(nip) || usedNIP[nip] || reservedNIP[nip] {
		return nil, invalid("nip_code %q must be 6 digits and not already used", nip)
	}
	nuban := req.NUBANCode
	if nuban == "" {
		for n := 903; n < 999; n++ {
			c := strconv.Itoa(n)
			if !usedNUBAN[c] {
				nuban = c
				break
			}
		}
	} else if !regexp.MustCompile(`^\d{3}$`).MatchString(nuban) || usedNUBAN[nuban] {
		return nil, invalid("nuban_code %q must be 3 digits and not already used", nuban)
	}
	if req.SettlesThrough != "" {
		via := a.cfg.Bank(req.SettlesThrough)
		if via == nil || via.Type == "microfinance" || via.Closed() {
			return nil, invalid("settles_through must name an open commercial or digital bank")
		}
	} else if typ == "microfinance" {
		for _, b := range a.cfg.Banks {
			if b.Type == "commercial" {
				req.SettlesThrough = b.ID
				break
			}
		}
	}
	if port != 0 {
		if port < 1024 || port > 65535 || usedPort[port] {
			return nil, invalid("port %d is out of range or already used", port)
		}
		if !portFree(port) {
			return nil, invalid("port %d is taken by another program", port)
		}
	} else {
		for p := maxPort + 1; p < maxPort+60; p++ {
			if !usedPort[p] && portFree(p) {
				port = p
				break
			}
		}
		if port == 0 {
			return nil, invalid("no free port near %d for online banking", maxPort)
		}
	}
	if database == "" {
		database = config.DatabaseName(a.cfg.Dir, id)
	} else if !databaseRe.MatchString(database) {
		return nil, invalid("database %q must be lowercase letters, digits and underscores", database)
	}
	if err := a.postgresRoom(context.Background()); err != nil {
		return nil, err
	}
	return &config.Bank{ID: id, Name: name, Type: typ, NIPCode: nip, NUBANCode: nuban, HistoryDays: days, Port: port, Database: database, SettlesThrough: req.SettlesThrough}, nil
}

func (a *App) SetBankSuspended(id string, suspended bool) error {
	a.lifeMu.Lock()
	b := a.cfg.Bank(id)
	st := a.status[id]
	if b == nil || st == nil {
		a.lifeMu.Unlock()
		return errNotFound
	}
	if b.Closed() {
		a.lifeMu.Unlock()
		return invalid("%s is closed; a closed bank cannot be suspended or resumed", b.Name)
	}
	prev, prevPhase := b.State, st.Phase
	b.State = ""
	if suspended {
		b.State = "suspended"
	}
	if st.Phase == PhaseActive || st.Phase == PhaseSuspended {
		st.Phase = PhaseActive
		if suspended {
			st.Phase = PhaseSuspended
		}
	}
	if err := a.saveBanksLocked(); err != nil {
		b.State, st.Phase = prev, prevPhase
		a.lifeMu.Unlock()
		return err
	}
	a.lifeMu.Unlock()
	a.mu.Lock()
	a.alignSuspensionLocked(b)
	a.mu.Unlock()
	a.poke()
	return nil
}

func (a *App) step(st *BankStatus, i int, run func() error) bool {
	a.lifeMu.Lock()
	done := st.Steps[i].Done
	a.lifeMu.Unlock()
	if done {
		return true
	}
	err := run()
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	st.Steps[i].At = a.clock.Real()
	if err != nil {
		st.Steps[i].Error = err.Error()
		return false
	}
	st.Steps[i].Done, st.Steps[i].Error = true, ""
	return true
}

func (a *App) reconcileBanks() {
	if a.worldOnly {
		return
	}
	a.watchConfig()
	a.checkDrift()
	a.lifeMu.Lock()
	pending := []*config.Bank{}
	for _, b := range a.cfg.Banks {
		if st := a.status[b.ID]; st != nil && (st.Phase == PhaseProvisioning || st.Phase == PhaseRepairing) {
			pending = append(pending, b)
		}
	}
	a.lifeMu.Unlock()
	for _, b := range pending {
		a.provision(b)
	}
}

func (a *App) provision(b *config.Bank) {
	a.lifeMu.Lock()
	st := a.status[b.ID]
	a.lifeMu.Unlock()
	ok := a.step(st, 0, func() error {
		if err := a.cfg.Claim("database", b.Database, b.ID); err != nil {
			return err
		}
		return a.cfg.Claim("port", strconv.Itoa(b.Port), b.ID)
	}) && a.step(st, 1, func() error {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.world.AddInstitution(world.Institution{ID: b.ID, Name: b.Name, Type: b.Type, NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, HistoryDays: b.HistoryDays, StableIDs: true, Style: b.Style()})
	}) && a.step(st, 2, func() error {
		a.mu.Lock()
		have := a.ledgerFor(b.ID) != nil
		a.mu.Unlock()
		if have {
			return nil
		}
		ls, err := openLedgerSource(context.Background(), a.cfg, b, a.world)
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.ledgers = append(a.ledgers, ls)
		a.mu.Unlock()
		return nil
	}) && a.step(st, 3, func() error {
		return a.startBankSite(b)
	}) && a.step(st, 4, func() error {
		a.lifeMu.Lock()
		serving := a.serving
		a.lifeMu.Unlock()
		if !serving {
			return nil
		}
		c := &http.Client{Timeout: 5 * time.Second}
		resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/login", b.Port))
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK && !(b.Suspended() && resp.StatusCode == http.StatusServiceUnavailable) {
			return fmt.Errorf("online banking answered %d", resp.StatusCode)
		}
		return nil
	})
	if ok {
		a.lifeMu.Lock()
		st.Phase, st.Reason = PhaseActive, ""
		if b.Suspended() {
			st.Phase = PhaseSuspended
		}
		a.lifeMu.Unlock()
		a.mu.Lock()
		a.alignSuspensionLocked(b)
		a.mu.Unlock()
	}
}

func (a *App) startBankSite(b *config.Bank) error {
	a.lifeMu.Lock()
	serving := a.serving
	_, running := a.sites[b.ID]
	a.lifeMu.Unlock()
	if !serving || running {
		return nil
	}
	h := a.BankHandler(b.ID)
	if h == nil {
		return fmt.Errorf("no ledger for %s", b.ID)
	}
	ls, err := listenLocal(b.Port)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	for _, l := range ls {
		go func(l net.Listener) { _ = srv.Serve(l) }(l)
	}
	a.lifeMu.Lock()
	a.sites[b.ID] = &siteServer{srv: srv}
	a.lifeMu.Unlock()
	return nil
}

func (a *App) stopBankSites(ctx context.Context) {
	a.lifeMu.Lock()
	sites := a.sites
	a.sites = map[string]*siteServer{}
	a.serving = false
	a.lifeMu.Unlock()
	ids := make([]string, 0, len(sites))
	for id := range sites {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		_ = sites[id].srv.Shutdown(ctx)
	}
}

func (a *App) bankError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrBankInvalid):
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_request", strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": "))
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "No such bank.")
	default:
		a.log.Error("bank lifecycle", "err", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (a *App) bankStatus(id string) (BankStatus, bool) {
	for _, st := range a.BankStatuses() {
		if st.ID == id {
			return st, true
		}
	}
	return BankStatus{}, false
}

func (a *App) ctlCreateBank(w http.ResponseWriter, r *http.Request) {
	var req BankRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"name":"...","type":"commercial"}.`)
		return
	}
	b, err := a.CreateBank(req)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	st, _ := a.bankStatus(b.ID)
	writeJSON(w, http.StatusAccepted, st)
}

func (a *App) ctlSuspend(w http.ResponseWriter, r *http.Request, suspended bool) {
	id := r.PathValue("id")
	if err := a.SetBankSuspended(id, suspended); err != nil {
		a.bankError(w, r, err)
		return
	}
	st, _ := a.bankStatus(id)
	writeJSON(w, http.StatusOK, st)
}

func (a *App) watchConfig() {
	if a.cfg.Dir == "" || time.Since(a.watched) < time.Second {
		return
	}
	a.watched = time.Now()
	raw, err := os.ReadFile(a.cfg.Path("beerus.yaml"))
	if err != nil {
		return
	}
	sum := config.HashKey(string(raw))
	a.lifeMu.Lock()
	same := sum == a.cfgSum
	a.lifeMu.Unlock()
	if !same {
		a.applyConfig(sum)
	}
}

func (a *App) applyConfig(sum string) {
	disk, err := config.Load(a.cfg.Dir)
	a.lifeMu.Lock()
	a.cfgSum = sum
	a.warnings = nil
	if err != nil {
		a.warn("beerus.yaml was not applied: %v", err)
		a.lifeMu.Unlock()
		return
	}
	if len(a.cfg.Banks) == 0 {
		a.lifeMu.Unlock()
		return
	}
	a.cfg.MaxBanks = disk.MaxBanks
	known := map[string]*config.Bank{}
	for _, b := range a.cfg.Banks {
		known[b.ID] = b
	}
	seen := map[string]bool{}
	suspend := map[string]bool{}
	rewrite := false
	for _, fb := range disk.Banks {
		seen[fb.ID] = true
		cur := known[fb.ID]
		if cur == nil {
			b, err := a.admit(BankRequest{Name: fb.Name, ID: fb.ID, Type: fb.Type, NIPCode: fb.NIPCode, NUBANCode: fb.NUBANCode, HistoryDays: fb.HistoryDays, SettlesThrough: fb.SettlesThrough}, fb.Port, fb.Database)
			if err != nil {
				a.warn("%s in beerus.yaml was not added: %s", fb.ID, strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": "))
				rewrite = true
				continue
			}
			b.State = fb.State
			a.mu.Lock()
			a.cfg.Banks = append(a.cfg.Banks, b)
			a.mu.Unlock()
			a.status[b.ID] = newStatus(b, false)
			rewrite = true
			continue
		}
		switch {
		case cur.Closed() && !fb.Closed():
			a.warn("%s is closed and cannot be reopened; the edit was undone", fb.ID)
			rewrite = true
		case !cur.Closed() && fb.Closed():
			a.warn("%s cannot be closed from beerus.yaml; use beerus bank close, which decides what happens to its customers' money", fb.ID)
			rewrite = true
		case cur.Suspended() != fb.Suspended():
			suspend[fb.ID] = fb.Suspended()
		}
		if fb.Name != cur.Name || fb.Type != cur.Type || fb.NIPCode != cur.NIPCode || fb.NUBANCode != cur.NUBANCode || fb.HistoryDays != cur.HistoryDays || fb.Port != cur.Port || fb.Database != cur.Database || fb.SettlesThrough != cur.SettlesThrough {
			a.warn("only the state of %s can be changed in beerus.yaml; other edits were undone", fb.ID)
			rewrite = true
		}
	}
	for _, b := range a.cfg.Banks {
		if !seen[b.ID] {
			a.warn("%s was removed from beerus.yaml, but banks are removed with beerus bank purge; it was put back", b.ID)
			rewrite = true
		}
	}
	if rewrite {
		if err := a.saveBanksLocked(); err != nil {
			a.warn("beerus.yaml could not be saved: %v", err)
		}
	}
	a.lifeMu.Unlock()
	for id, s := range suspend {
		if err := a.SetBankSuspended(id, s); err != nil {
			a.lifeMu.Lock()
			a.warn("%s: %v", id, err)
			a.lifeMu.Unlock()
		}
	}
	a.poke()
}

func (a *App) checkDrift() {
	if time.Since(a.driftAt) < driftEvery && !a.driftDue.Swap(false) {
		return
	}
	a.driftAt = time.Now()
	a.lifeMu.Lock()
	serving := a.serving
	banks := []*config.Bank{}
	for _, b := range a.cfg.Banks {
		if st := a.status[b.ID]; st != nil && (st.Phase == PhaseActive || st.Phase == PhaseSuspended) {
			banks = append(banks, b)
		}
	}
	a.lifeMu.Unlock()
	if !serving {
		return
	}
	for _, b := range banks {
		reason, redo := a.probe(b)
		if reason == "" {
			delete(a.strikes, b.ID)
			continue
		}
		if a.strikes[b.ID]++; a.strikes[b.ID] < 2 {
			a.driftDue.Store(true)
			continue
		}
		delete(a.strikes, b.ID)
		a.detach(b, redo)
		a.log.Warn("bank drifted; repairing", "bank", b.ID, "reason", reason)
		a.lifeMu.Lock()
		st := a.status[b.ID]
		st.Phase, st.Reason = PhaseRepairing, reason
		for _, i := range redo {
			st.Steps[i].Done, st.Steps[i].Error = false, ""
		}
		a.lifeMu.Unlock()
	}
}

func (a *App) probe(b *config.Bank) (string, []int) {
	a.mu.Lock()
	ls := a.ledgerFor(b.ID)
	a.mu.Unlock()
	if ls == nil {
		return "its ledger is not open", []int{2, 4}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ls.Ping(ctx); err != nil {
		return "its database cannot be reached: " + err.Error(), []int{2, 4}
	}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/login", b.Port))
	if err == nil {
		resp.Body.Close()
		return "", nil
	}
	return "online banking is not answering", []int{3, 4}
}

func (a *App) detach(b *config.Bank, redo []int) {
	if redo[0] == 2 {
		a.mu.Lock()
		var ls *ledgerSource
		for i, l := range a.ledgers {
			if l.inst.ID == b.ID {
				ls = l
				a.ledgers = append(a.ledgers[:i:i], a.ledgers[i+1:]...)
				break
			}
		}
		a.mu.Unlock()
		if ls != nil {
			ls.Close()
		}
		return
	}
	a.lifeMu.Lock()
	site := a.sites[b.ID]
	delete(a.sites, b.ID)
	a.lifeMu.Unlock()
	if site != nil {
		_ = site.srv.Close()
	}
}

func (a *App) OpenAccount(bankID, persona, typ string) (world.Account, error) {
	if _, ok := a.world.Institution(bankID); !ok || a.world.Purged(bankID) {
		return world.Account{}, errNotFound
	}
	if phase := a.bankPhase(bankID); phase == PhaseProvisioning || phase == PhaseRepairing {
		return world.Account{}, invalid("the bank is %s; try again when it is active", phase)
	}
	if typ == "" {
		typ = "current"
	}
	a.mu.Lock()
	acc, err := a.world.OpenAccount(persona, bankID, typ, a.clock.Now())
	if err == nil {
		a.dirty()
	}
	a.mu.Unlock()
	if err != nil {
		return world.Account{}, invalid("%s", transferMessage(err))
	}
	a.poke()
	return acc, nil
}

func (a *App) ctlOpenAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Persona string `json:"persona"`
		Type    string `json:"type"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"persona":"adaeze","type":"current"}.`)
		return
	}
	acc, err := a.OpenAccount(r.PathValue("id"), body.Persona, body.Type)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	inst, _ := a.world.Institution(acc.Institution)
	writeJSON(w, http.StatusCreated, map[string]any{"key": acc.Key, "bank": inst.Name, "nuban": acc.NUBAN, "name": acc.Name, "type": acc.Type, "owner": acc.Owner, "opened_at": ts(acc.OpenedAt)})
}

func CheckPorts(c *config.Config) error {
	ports := []int{c.Connect.PartnerPort, c.Connect.WidgetPort, c.Connect.ControlPort}
	for _, b := range c.Banks {
		if !b.Closed() {
			ports = append(ports, b.Port)
		}
	}
	_, err := os.Stat(c.Path("running"))
	running := err == nil
	for _, p := range ports {
		if portFree(p) {
			continue
		}
		if running {
			return fmt.Errorf("beerus is already running for %s; stop it (Ctrl+C in its terminal) before starting it again", c.Dir)
		}
		return fmt.Errorf("port %d is already in use by another program; free it or change it in beerus.yaml", p)
	}
	return nil
}
