package participant

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/cbn"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type accountRequest struct {
	Settlement *int64 `json:"settlement_minor"`
	Vault      *int64 `json:"vault_minor"`
}

type step struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Done bool   `json:"done"`
}

type state struct {
	ParticipantID      string   `json:"participant_id"`
	Kind               string   `json:"kind"`
	Phase              string   `json:"phase"`
	Generation         int64    `json:"generation"`
	ObservedGeneration int64    `json:"observed_generation"`
	Steps              []step   `json:"steps"`
	Problems           []string `json:"problems"`
}

func (p *Participant) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", only(http.MethodGet, p.health))
	mux.HandleFunc("/v1/status", only(http.MethodGet, p.status))
	mux.HandleFunc("/v1/control/banks/{bank_id}/settlement-account", only(http.MethodPut, p.openAccount))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "The CBN does not serve this control call.")
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
	f, err := config.LoadCBNFile(p.dir)
	if err != nil {
		p.log.ErrorContext(r.Context(), "CBN config not read", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "config_unreadable", "The CBN config cannot be read. Start Beerus with beerus up to write it again.")
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "participant_id": config.CBNID, "world_id": f.WorldID})
}

func (p *Participant) status(w http.ResponseWriter, r *http.Request) {
	dbUp := p.store.Pool.Ping(r.Context()) == nil
	problems, err := p.books.Problems(r.Context())
	booksUp := err == nil
	if err != nil {
		problems = []string{"the CBN could not check its books: " + err.Error()}
	}
	web.WriteJSON(w, http.StatusOK, state{
		ParticipantID: config.CBNID,
		Kind:          "cbn",
		Phase:         config.PhaseActive,
		Steps:         []step{{config.StepDatabase, "Making the CBN database", dbUp}, {"schema", "Migrating the CBN ledger", booksUp}, {"settlement", "Opening the CBN settlement ledger", booksUp}},
		Problems:      problems,
	})
}

func (p *Participant) openAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("bank_id")
	var req accountRequest
	if err := web.DecodeBody(r, &req); err != nil || req.Settlement == nil || req.Vault == nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send the settlement_minor and vault_minor of the account, in kobo.")
		return
	}
	acc, err := p.books.OpenAccount(r.Context(), id, *req.Settlement, *req.Vault)
	if err != nil {
		p.failed(w, r, "account not opened", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, acc)
}

func (p *Participant) failed(w http.ResponseWriter, r *http.Request, msg string, err error) {
	var bad *cbn.InvalidError
	var conflict *cbn.ConflictError
	switch {
	case errors.As(err, &bad):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", bad.Msg)
	case errors.As(err, &conflict):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "account_conflict", conflict.Error())
	default:
		p.unavailable(w, r, msg, err)
	}
}

func (p *Participant) unavailable(w http.ResponseWriter, r *http.Request, msg string, err error) {
	p.log.ErrorContext(r.Context(), msg, "err", err)
	web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "The CBN ledger is not open yet. Try again soon.")
}
