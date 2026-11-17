package participant

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const stepSchema = "schema"

type step struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Done bool   `json:"done"`
}

type state struct {
	ParticipantID      string `json:"participant_id"`
	Kind               string `json:"kind"`
	Phase              string `json:"phase"`
	Generation         int64  `json:"generation"`
	ObservedGeneration int64  `json:"observed_generation"`
	Steps              []step `json:"steps"`
}

type registerRequest struct {
	NIPCode             string `json:"nip_code"`
	NUBANCode           string `json:"nuban_code"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	CreditURL           string `json:"credit_url"`
	CertificateIdentity string `json:"certificate_identity"`
	NetDebitCap         *int64 `json:"net_debit_cap_minor"`
	Collateral          *int64 `json:"collateral_minor"`
}

type statusRequest struct {
	Status string `json:"status"`
}

func (p *Participant) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", only(http.MethodGet, p.health))
	mux.HandleFunc("/v1/status", only(http.MethodGet, p.status))
	mux.HandleFunc("/v1/control/name-enquiries", only(http.MethodGet, p.controlNameEnquiry))
	mux.HandleFunc("/v1/control/banks/{bank_id}", only(http.MethodPut, p.register))
	mux.HandleFunc("/v1/control/banks/{bank_id}/status", only(http.MethodPut, p.setStatus))
	mux.HandleFunc("/v1/control/registry/clients", only(http.MethodGet, p.listClients))
	mux.HandleFunc("/v1/control/registry/clients/{client_id}", only(http.MethodPut, p.ensureClient))
	mux.HandleFunc("/v1/control/consents", only(http.MethodGet, p.listConsents))
	mux.HandleFunc("/v1/control/settlement-sessions", only(http.MethodPost, p.settlementSession))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "NIBSS does not serve this control call.")
	})
	return web.WithRequestID(p.guard(mux))
}

func only(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			web.WriteError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Use "+method+" for this path.")
			return
		}
		next(w, r)
	}
}

func (p *Participant) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.allowed(r) {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", "The control API only accepts local connections.")
			return
		}
		if r.Header.Get("Origin") != "" {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", "Browser requests are refused.")
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(tok), []byte(p.token)) != 1 {
			web.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "A valid control token is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (p *Participant) allowed(r *http.Request) bool {
	if ip := net.ParseIP(p.bind); p.bind != "" && !(ip != nil && ip.IsLoopback()) {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}

func (p *Participant) health(w http.ResponseWriter, r *http.Request) {
	f, err := config.LoadNIBSSFile(p.dir)
	if err != nil {
		p.log.ErrorContext(r.Context(), "NIBSS config not read", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "config_unreadable", "The NIBSS config cannot be read. Start Beerus with beerus up to write it again.")
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "participant_id": config.NIBSSID, "world_id": f.WorldID})
}

func (p *Participant) status(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, state{
		ParticipantID: config.NIBSSID,
		Kind:          kind,
		Phase:         phaseActive,
		Steps:         []step{{config.StepDatabase, "Making the NIBSS database", true}, {stepSchema, "Migrating the NIBSS database", true}},
	})
}

func (p *Participant) register(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("bank_id")
	var req registerRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send the nip_code, nuban_code, name, type, credit_url, certificate_identity, net_debit_cap_minor and collateral_minor of the bank.")
		return
	}
	if req.NetDebitCap == nil || req.Collateral == nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send the net_debit_cap_minor and the collateral_minor of the bank, in kobo.")
		return
	}
	if u, err := url.Parse(req.CreditURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || req.CertificateIdentity == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send a credit_url that starts with http:// or https:// and a certificate_identity.")
		return
	}
	m := nibss.Member{
		Institution:         nip.Institution{ID: id, Name: req.Name, NIPCode: req.NIPCode, NUBANCode: req.NUBANCode, Type: req.Type},
		CreditURL:           req.CreditURL,
		CertificateIdentity: req.CertificateIdentity,
		Limits:              nibss.Limits{NetDebitCap: *req.NetDebitCap, Collateral: *req.Collateral},
	}
	if err := p.store.Register(r.Context(), m); err != nil {
		p.registerFailed(w, r, err)
		return
	}
	p.reply(w, r, id)
}

func (p *Participant) registerFailed(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *nibss.RegistrationConflictError
	var bad *nibss.InvalidError
	switch {
	case errors.As(err, &conflict):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "registration_conflict", web.Sentence(err.Error())+" Send the registered details.")
	case errors.Is(err, nibss.ErrCodeTaken):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "registration_conflict", web.Sentence(err.Error())+" Send other codes or another name.")
	case errors.As(err, &bad):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", web.Sentence(err.Error()))
	default:
		p.unavailable(w, r, "bank not registered", err)
	}
}

func (p *Participant) setStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("bank_id")
	var req statusRequest
	if err := web.DecodeBody(r, &req); err != nil || req.Status == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"status":"active"} with the status the bank should have.`)
		return
	}
	err := p.store.SetStatus(r.Context(), id, req.Status)
	var bad *nibss.InvalidError
	switch {
	case err == nil:
		p.reply(w, r, id)
	case errors.As(err, &bad):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The status must be active, suspended, terminating or removed.")
	case errors.Is(err, nip.ErrUnknownInstitution):
		web.WriteError(w, r, http.StatusNotFound, "not_found", fmt.Sprintf("Bank %s is not in the NIBSS directory. Register it first.", id))
	case errors.Is(err, nibss.ErrTransition):
		p.refused(w, r, id, req.Status)
	default:
		p.unavailable(w, r, "bank status not changed", err)
	}
}

func (p *Participant) refused(w http.ResponseWriter, r *http.Request, id, want string) {
	m, err := p.store.Member(r.Context(), id)
	if err != nil {
		p.unavailable(w, r, "bank status not read", err)
		return
	}
	next := nibss.Transitions(m.Status)
	hint := "A " + m.Status + " bank stays " + m.Status + "."
	if len(next) > 0 {
		hint = "It can become " + strings.Join(next, " or ") + "."
	}
	web.WriteError(w, r, http.StatusUnprocessableEntity, "status_not_allowed", fmt.Sprintf("Bank %s is %s and cannot become %s. %s", id, m.Status, want, hint))
}

func (p *Participant) reply(w http.ResponseWriter, r *http.Request, id string) {
	m, err := p.store.Member(r.Context(), id)
	if err != nil {
		p.unavailable(w, r, "bank not read", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, m.Institution)
}

func (p *Participant) unavailable(w http.ResponseWriter, r *http.Request, msg string, err error) {
	p.log.ErrorContext(r.Context(), msg, "err", err)
	web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "NIBSS cannot answer now. Try again soon.")
}
