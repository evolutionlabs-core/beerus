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
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	PhaseProvisioning  = setup.PhaseProvisioning
	PhaseActive        = setup.PhaseActive
	PhaseSuspended     = setup.PhaseSuspended
	PhaseRepairing     = setup.PhaseRepairing
	PhaseClosing       = setup.PhaseClosing
	PhaseClosed        = setup.PhaseClosed
	PhaseTerminating   = setup.PhaseTerminating
	driftEvery         = 5 * time.Second
	defaultMaxBanks    = 6
	connectionsPerBank = 8
)

var (
	typeHistoryDays = map[string]int{"commercial": 730, "digital": 180, "microfinance": 365}
	ErrBankInvalid  = errors.New("invalid bank")
	slugRe          = regexp.MustCompile(`[^a-z0-9]+`)
	idRe            = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	databaseRe      = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
)

type BankStep struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Done  bool       `json:"done"`
	Error string     `json:"error,omitempty"`
	At    *time.Time `json:"at,omitempty"`
}

type BankStatus struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Phase  string     `json:"phase"`
	Reason string     `json:"reason,omitempty"`
	URL    string     `json:"online_banking"`
	Steps  []BankStep `json:"steps"`

	Attempts      int        `json:"attempts,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
}

type siteServer struct {
	srv *http.Server
	api *http.Server
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
	if len(a.cfg.Banks) == 0 {
		a.lifeMu.Unlock()
		return nil, invalid("this setup runs the built-in banks without ledgers; create a new setup with beerus init to manage banks")
	}
	b, err := a.admit(req, 0, "")
	if err != nil {
		a.lifeMu.Unlock()
		return nil, err
	}
	a.mu.Lock()
	a.cfg.Banks = append(a.cfg.Banks, b)
	a.mu.Unlock()
	st, v, err := a.recordBanksLocked()
	if err != nil {
		a.mu.Lock()
		a.cfg.Banks = a.cfg.Banks[:len(a.cfg.Banks)-1]
		a.mu.Unlock()
		a.lifeMu.Unlock()
		return nil, err
	}
	a.status[b.ID] = newStatus(b, false)
	a.lifeMu.Unlock()
	if err := a.ctl.flush(st, v); err != nil {
		return nil, err
	}
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
	days, ok := typeHistoryDays[typ]
	if !ok {
		return nil, invalid("type must be commercial, digital or microfinance")
	}
	if req.HistoryDays > 0 {
		if req.HistoryDays > world.MaxHistoryDays {
			return nil, invalid("history_days must be at most %d", world.MaxHistoryDays)
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
	markNUBAN := func(code string) {
		if n, err := world.NormaliseBankCode(code); err == nil {
			usedNUBAN[n] = true
		}
	}
	for _, p := range a.world.PurgedInstitutions() {
		if strings.EqualFold(p.Name, name) {
			return nil, invalid("%q was the name of a purged bank, and it stays reserved", name)
		}
		if p.ID == id {
			return nil, invalid("%q was the id of a purged bank, and it stays reserved", id)
		}
		usedNIP[p.NIPCode] = true
		markNUBAN(p.NUBANCode)
	}
	usedPort := map[int]bool{a.cfg.Connect.PartnerPort: true, a.cfg.Connect.WidgetPort: true, a.cfg.Connect.ControlPort: true, a.cfg.Connect.HealthPort: true}
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
		usedNIP[b.NIPCode], usedPort[b.Port], usedPort[b.APIPort] = true, true, true
		markNUBAN(b.NUBANCode)
		if b.Port > maxPort {
			maxPort = b.Port
		}
	}
	pack, err := countrypack.Nigeria()
	if err != nil {
		return nil, fmt.Errorf("load simulator code ranges: %w", err)
	}
	codes := pack.SimulatorCodes
	nip := req.NIPCode
	if nip == "" {
		for n := codes.NIP.First; n <= codes.NIP.Last; n++ {
			c := strconv.Itoa(n)
			if !usedNIP[c] {
				nip = c
				break
			}
		}
		if nip == "" {
			return nil, invalid("all NIP codes from %d to %d are taken. Set nip_code by hand, or purge a bank", codes.NIP.First, codes.NIP.Last)
		}
	} else if !regexp.MustCompile(`^\d{6}$`).MatchString(nip) || usedNIP[nip] {
		return nil, invalid("nip_code %q must be 6 digits and not already used", nip)
	}
	nuban := req.NUBANCode
	if nuban == "" {
		for n := codes.NUBAN.First; n <= codes.NUBAN.Last; n++ {
			c := strconv.Itoa(n)
			if norm, _ := world.NormaliseBankCode(c); !usedNUBAN[norm] {
				nuban = c
				break
			}
		}
		if nuban == "" {
			return nil, invalid("all NUBAN codes from %d to %d are taken. Set nuban_code by hand, or purge a bank", codes.NUBAN.First, codes.NUBAN.Last)
		}
	} else if norm, err := world.NormaliseBankCode(nuban); err != nil || (len(nuban) == 6 && nuban[0] != '9') || usedNUBAN[norm] {
		return nil, invalid("nuban_code %q must be 3 digits, or 5 or 6 digits starting with 9, and not already used", nuban)
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
		var err error
		if port, err = a.cfg.AllocatePort(maxPort+1, usedPort, portFree); err != nil {
			return nil, invalid("online banking: %v", err)
		}
	}
	usedPort[port] = true
	apiPort, err := a.cfg.AllocatePort(port+a.cfg.APIPortOffset(), usedPort, portFree)
	if err != nil {
		return nil, invalid("bank API: %v", err)
	}
	if database == "" {
		database = config.DatabaseName(a.cfg.Dir, id)
	} else if !databaseRe.MatchString(database) {
		return nil, invalid("database %q must be lowercase letters, digits and underscores", database)
	}
	if err := a.postgresRoom(context.Background()); err != nil {
		return nil, err
	}
	return &config.Bank{ID: id, Name: name, Type: typ, NIPCode: nip, NUBANCode: nuban, HistoryDays: days, Port: port, APIPort: apiPort, Database: database, SettlesThrough: req.SettlesThrough}, nil
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
	saved, v, err := a.recordBanksLocked()
	if err != nil {
		b.State, st.Phase = prev, prevPhase
		a.lifeMu.Unlock()
		return err
	}
	a.lifeMu.Unlock()
	if err := a.ctl.flush(saved, v); err != nil {
		return err
	}
	a.mu.Lock()
	a.alignSuspensionLocked(b)
	a.mu.Unlock()
	a.poke()
	a.lifeMu.Lock()
	phase := st.Phase
	a.lifeMu.Unlock()
	if phase == PhaseActive || phase == PhaseSuspended {
		a.recordPhase(id, phase)
	}
	return a.setMemberStatus(id, memberStatus(b))
}

func (a *App) startBankSite(b *config.Bank) error {
	a.lifeMu.Lock()
	serving := a.serving
	_, running := a.sites[b.ID]
	a.lifeMu.Unlock()
	if !serving || running || a.role == RoleSim {
		return nil
	}
	a.forgetOpenBanking(b.ID)
	h := a.BankHandler(b.ID)
	if h == nil {
		return fmt.Errorf("no ledger for %s", b.ID)
	}
	ls, err := listenLocal(b.Port)
	if err != nil {
		return err
	}
	api, err := a.startBankAPI(b)
	if err != nil {
		for _, l := range ls {
			l.Close()
		}
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	for _, l := range ls {
		go func(l net.Listener) { _ = srv.Serve(l) }(l)
	}
	a.lifeMu.Lock()
	a.sites[b.ID] = &siteServer{srv: srv, api: api}
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
		if sites[id].api != nil {
			_ = sites[id].api.Shutdown(ctx)
		}
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
			ports = append(ports, b.Port, b.APIPort)
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
		return fmt.Errorf("port %d is already in use by another program; free it or change it in local.yaml", p)
	}
	return nil
}

func (a *App) siteProbeURL(b *config.Bank) string {
	if a.role == RoleSim {
		return b.URL() + "/health"
	}
	return fmt.Sprintf("http://127.0.0.1:%d/login", b.Port)
}
