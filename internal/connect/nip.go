package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (a *App) NameEnquiry(ctx context.Context, code, nuban string) (nip.NameEnquiry, error) {
	if a.nibssCtl == nil {
		return nip.NameEnquiry{}, &server.UnavailableError{Msg: nibssAway}
	}
	return a.nibssCtl.enquire(ctx, code, nuban)
}

func (a *App) ctlNameEnquiry(w http.ResponseWriter, r *http.Request) {
	code, nuban := r.URL.Query().Get("bank_code"), r.URL.Query().Get("account_number")
	if code == "" || nuban == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "bank_code and account_number are required.")
		return
	}
	out, err := a.NameEnquiry(r.Context(), code, nuban)
	var refused *server.PeerError
	if errors.As(err, &refused) && refused.Status == http.StatusBadRequest {
		web.WriteError(w, r, http.StatusBadRequest, refused.Code, refused.Message)
		return
	}
	if err != nil {
		a.log.Error("name enquiry", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "nibss_unavailable", "NIBSS could not answer the name enquiry. Try again soon.")
		return
	}
	web.WriteJSON(w, http.StatusOK, out)
}

func (a *App) ctlNIPParticipants(w http.ResponseWriter, r *http.Request) {
	list, err := a.nibssCtl.directory(r.Context())
	var away *nibssDownError
	if errors.As(err, &away) {
		a.log.Warn("NIBSS directory", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "nibss_unavailable", "NIBSS is not available right now.")
		return
	}
	if err != nil {
		a.log.Error("NIBSS directory", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "The NIBSS directory could not be read.")
		return
	}
	down := a.downBanks()
	out := []map[string]any{}
	for _, in := range list {
		if in.Status == nip.StatusRemoved {
			continue
		}
		status := in.Status
		if status == nip.StatusActive && down[in.ID] {
			status = "outage"
		}
		out = append(out, map[string]any{"nip_code": in.NIPCode, "name": in.Name, "bank_id": in.ID, "status": status})
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) position(ctx context.Context, b *config.Bank) (map[string]any, error) {
	sessions, err := a.nibssCtl.openSessions(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	inward, ok := sessions.inwardAll()
	if !ok {
		return nil, fmt.Errorf("the open sessions of %s add up to more than 64 bits", b.ID)
	}
	open := nibss.Position{Outward: sessions.Outward, Inward: inward}
	pos := open.NetDebit()
	source := "configured"
	if b.CapIsDefault() {
		source = "default"
	}
	var collateral any
	if b.Collateral > 0 {
		collateral = b.Collateral
	}
	next, err := a.nibssCtl.nextCutoff(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bank_id": b.ID, "nip_code": b.NIPCode, "net_debit_cap_minor": b.Cap(), "cap_source": source, "collateral_minor": collateral, "open_outward_minor": open.Outward, "open_inward_minor": open.Inward, "position_minor": pos, "headroom_minor": b.Cap() - pos, "next_session": ts(next)}, nil
}

func (a *App) ctlNIPPositions(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	banks := slices.Clone(a.cfg.Banks)
	a.mu.Unlock()
	out := []map[string]any{}
	for _, b := range banks {
		row, err := a.position(r.Context(), b)
		if err != nil {
			a.settlementFailed(w, r, "NIP positions", err)
			return
		}
		out = append(out, row)
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) ctlNIPSettlement(w http.ResponseWriter, r *http.Request) {
	c, err := a.CheckSettlement(r.Context())
	if err != nil {
		a.settlementFailed(w, r, "check settlement", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, c)
}

func (a *App) settlementFailed(w http.ResponseWriter, r *http.Request, msg string, err error) {
	var cbnDown *cbnDownError
	var nibssDown *nibssDownError
	var peer *server.PeerError
	var unchecked *uncheckedError
	var away *server.UnavailableError
	switch {
	case errors.As(err, &unchecked):
		a.log.Warn(msg, "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", web.Sentence(unchecked.Name+" "+unchecked.Why)+" Try again soon.")
		return
	case errors.As(err, &away):
		a.log.Warn(msg, "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", away.Msg)
		return
	case errors.As(err, &cbnDown):
		a.log.Warn(msg, "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", cbnAway)
		return
	case errors.As(err, &nibssDown):
		a.log.Warn(msg, "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", nibssAway)
		return
	case errors.As(err, &peer) && peer.Status == http.StatusServiceUnavailable:
		a.log.Warn(msg, "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", peer.Message)
		return
	}
	a.log.Error(msg, "err", err)
	web.WriteError(w, r, http.StatusInternalServerError, "internal_error", web.SomethingWentWrong)
}

func (a *App) ctlSettleNow(w http.ResponseWriter, r *http.Request) {
	n, err := a.SettleNow(r.Context())
	if err != nil {
		a.settlementFailed(w, r, "settle now", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"settled_sessions": n, "at": ts(a.clock.Now())})
}

func (a *App) ctlNIPSession(w http.ResponseWriter, r *http.Request) {
	x, err := a.Session(r.Context(), r.PathValue("id"))
	a.writeNIPSession(w, r, x, err)
}

func (a *App) ctlNIPSessionStatus(w http.ResponseWriter, r *http.Request) {
	x, err := a.QuerySession(r.Context(), r.PathValue("id"))
	a.writeNIPSession(w, r, x, err)
}

func (a *App) writeNIPSession(w http.ResponseWriter, r *http.Request, x any, err error) {
	switch {
	case errors.Is(err, nip.ErrNoRecord):
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No NIP session has that ID.")
	case err != nil:
		a.log.Error("NIP session", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "nibss_unavailable", "NIBSS could not be asked about the session. Try again soon.")
	default:
		web.WriteJSON(w, http.StatusOK, x)
	}
}

func (a *App) SetNetDebitCap(ctx context.Context, id string, cap, collateral int64) error {
	err := a.applyEdit(ctx, func(sp *setup.Spec) error {
		i := slices.IndexFunc(sp.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
		if i < 0 {
			return errNotFound
		}
		sp.Spec.Banks[i].NetDebitCap, sp.Spec.Banks[i].Collateral = cap, collateral
		return nil
	}, ApplyOptions{})
	if err != nil {
		return err
	}
	a.reconcileNow(ctx)
	return nil
}

func (a *App) ctlNetDebitCap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cap        string `json:"cap"`
		Collateral string `json:"collateral"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"cap":"5000000","collateral":"5000000"}.`)
		return
	}
	capMinor, err := bank.ParseNaira(body.Cap)
	if err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "cap: "+err.Error())
		return
	}
	var collateral int64
	if body.Collateral != "" {
		if collateral, err = bank.ParseNaira(body.Collateral); err != nil {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "collateral: "+err.Error())
			return
		}
	}
	id := r.PathValue("id")
	if err := a.SetNetDebitCap(r.Context(), id, capMinor, collateral); err != nil {
		a.bankError(w, r, err)
		return
	}
	a.mu.Lock()
	b := a.cfg.Bank(id)
	a.mu.Unlock()
	row, err := a.position(r.Context(), b)
	if err != nil {
		a.settlementFailed(w, r, "NIP position", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, row)
}
