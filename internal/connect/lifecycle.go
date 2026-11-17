package connect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
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
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Done      bool       `json:"done"`
	Error     string     `json:"error,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	At        *time.Time `json:"at,omitempty"`
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
		return invalid("Postgres has %d of %s in use; another bank needs up to %s", used, plural(max, "connection", "connections"), plural(connectionsPerBank, "connection", "connections"))
	}
	return nil
}

func (a *App) removedBanks(ctx context.Context) ([]nibss.Institution, error) {
	s := a.nibssStore()
	if s == nil {
		return nil, errors.New("the NIBSS directory is not open yet, so the codes of removed banks cannot be checked; try again soon")
	}
	all, err := s.Directory(ctx)
	if err != nil {
		return nil, err
	}
	out := []nibss.Institution{}
	for _, in := range all {
		if in.Status == nibss.StatusRemoved {
			out = append(out, in)
		}
	}
	return out, nil
}

func (a *App) CreateBank(ctx context.Context, req BankRequest) (BankStatus, error) {
	if req.NIPCode != "" || req.NUBANCode != "" {
		return BankStatus{}, invalid("nip_code and nuban_code are assigned by Beerus; leave them out")
	}
	if req.HistoryDays != 0 {
		return BankStatus{}, invalid("history_days can no longer be set; it follows the bank type")
	}
	name := strings.TrimSpace(req.Name)
	id := strings.TrimSpace(req.ID)
	slug := ""
	var removed []nibss.Institution
	if id == "" {
		slug = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	}
	if slug != "" {
		var err error
		if removed, err = a.removedBanks(ctx); err != nil {
			return BankStatus{}, err
		}
	}
	stored, err := a.ctl.status()
	if err != nil {
		return BankStatus{}, fmt.Errorf("read control.json: %w", err)
	}
	if r := findResource(&stored, setup.KindBank, id); r != nil && (r.Phase == PhaseClosing || r.Phase == PhaseTerminating) {
		return BankStatus{}, applyFail(http.StatusConflict, codeBeingRemoved, "banks[%s] is being removed, and its id is never reused. Choose another id.", id)
	}
	err = a.applyEdit(func(sp *setup.Spec) error {
		if slug != "" {
			taken := map[string]bool{}
			for _, b := range sp.Spec.Banks {
				taken[b.ID] = true
			}
			for _, in := range removed {
				taken[in.ID] = true
			}
			for _, r := range stored.Resources {
				if r.Kind == setup.KindBank {
					taken[r.ID] = true
				}
			}
			for _, b := range a.bankList() {
				taken[b.ID] = true
			}
			id = slug
			for n := 2; taken[id]; n++ {
				id = fmt.Sprintf("%s-%d", slug, n)
			}
		}
		nb := setup.Bank{ID: id, Name: name, Type: orDefault(req.Type, "commercial"), SettlesThrough: req.SettlesThrough}
		if nb.SettlesThrough == "" && nb.Type == "microfinance" {
			for _, b := range sp.Spec.Banks {
				if b.Type == "commercial" && b.State != setup.StateClosed {
					nb.SettlesThrough = b.ID
					break
				}
			}
		}
		sp.Spec.Banks = append(sp.Spec.Banks, nb)
		return nil
	}, ApplyOptions{})
	if err != nil {
		return BankStatus{}, err
	}
	st, _ := a.bankStatus(id)
	return st, nil
}

func (a *App) SetBankSuspended(id string, suspended bool) error {
	err := a.applyEdit(func(sp *setup.Spec) error {
		i := slices.IndexFunc(sp.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
		if i < 0 {
			return errNotFound
		}
		b := &sp.Spec.Banks[i]
		if b.State == setup.StateClosed {
			return invalid("%s is closed; a closed bank cannot be suspended or resumed", b.Name)
		}
		b.State = ""
		if suspended {
			b.State = setup.StateSuspended
		}
		return nil
	}, ApplyOptions{})
	if err != nil {
		return err
	}
	a.reconcileNow()
	return nil
}

func (a *App) startBankSite(b *config.Bank) error {
	a.lifeMu.Lock()
	serving := a.serving
	_, running := a.sites[b.ID]
	a.lifeMu.Unlock()
	if !serving || running || a.role == RoleCore {
		return nil
	}
	a.forgetOpenBanking(b.ID)
	h := a.BankHandler(b.ID)
	if h == nil {
		return fmt.Errorf("no ledger for %s", b.ID)
	}
	ls, err := a.listenLocal(b.Port)
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

const NoBanksMessage = "there are no banks yet. Add one first with beerus bank create --name NAME"

func (a *App) noSuchBank(w http.ResponseWriter, r *http.Request) {
	if len(a.bankList()) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", NoBanksMessage)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "No such bank.")
}

func (a *App) bankError(w http.ResponseWriter, r *http.Request, err error) {
	if a.writeApplyError(w, r, err) {
		return
	}
	switch {
	case errors.Is(err, ErrBankInvalid):
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_request", strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": "))
	case errors.Is(err, errNotFound):
		a.noSuchBank(w, r)
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
	st, err := a.CreateBank(r.Context(), req)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
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
	if a.role == RoleCore {
		return b.URL() + "/health"
	}
	return fmt.Sprintf("http://127.0.0.1:%d/login", b.Port)
}
