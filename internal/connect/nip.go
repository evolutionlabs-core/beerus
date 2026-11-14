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

type NIPState struct {
	Sessions int64 `json:"sessions"`
}

type reply struct {
	status   int
	body     []byte
	replayed bool
}

func jsonReply(status int, v any) reply {
	b, err := json.Marshal(v)
	if err != nil {
		return reply{status: http.StatusInternalServerError, body: []byte(`{"error":{"code":"internal_error","message":"The response could not be built."}}`)}
	}
	return reply{status: status, body: b}
}

func errReply(r *http.Request, status int, code, msg string) reply {
	return jsonReply(status, map[string]web.APIError{"error": {Code: code, Message: msg, RequestID: web.RequestID(r)}})
}

func (p reply) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if p.replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.WriteHeader(p.status)
	_, _ = w.Write(p.body)
}

func (a *App) NameEnquiry(code, nuban string) (server.NameEnquiry, error) {
	out := server.NameEnquiry{BankCode: code, AccountNo: nuban}
	sw := a.nibssStore()
	if sw == nil {
		return out, errors.New("NIBSS is not available right now")
	}
	ctx := context.Background()
	in, err := sw.Institution(ctx, code)
	if errors.Is(err, nip.ErrUnknownInstitution) {
		out.Message = fmt.Sprintf("Bank code %s is not in the NIBSS directory.", code)
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.BankName = in.Name
	name, err := sw.NameEnquiry(ctx, code, nuban)
	switch {
	case err == nil:
		out.Code, out.Message, out.AccountName = nip.OK, "Successful.", name
	case errors.Is(err, nip.ErrUnavailable):
		out.Code, out.Message = nip.Unavailable, in.Name+" is not available right now."
	case errors.Is(err, nip.ErrInvalidAccount):
		out.Code, out.Message = nip.InvalidAccount, "That is not a valid account number at "+in.Name+"."
	case errors.Is(err, nip.ErrNoRecord):
		out.Code, out.Message = nip.NoRecord, "No such account at "+in.Name+"."
	default:
		return out, err
	}
	return out, nil
}

func (a *App) ctlNameEnquiry(w http.ResponseWriter, r *http.Request) {
	code, nuban := r.URL.Query().Get("bank_code"), r.URL.Query().Get("account_number")
	if code == "" || nuban == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "bank_code and account_number are required.")
		return
	}
	out, err := a.NameEnquiry(code, nuban)
	if err != nil {
		a.log.Error("name enquiry", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "nibss_unavailable", "NIBSS could not answer the name enquiry. Try again soon.")
		return
	}
	web.WriteJSON(w, http.StatusOK, out)
}

func (a *App) ctlNIPParticipants(w http.ResponseWriter, r *http.Request) {
	sw := a.nibssStore()
	if sw == nil {
		web.WriteError(w, r, http.StatusServiceUnavailable, "nibss_unavailable", "NIBSS is not available right now.")
		return
	}
	list, err := sw.Directory(context.Background())
	if err != nil {
		a.log.Error("NIBSS directory", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "The NIBSS directory could not be read.")
		return
	}
	now := a.clock.Now()
	a.mu.Lock()
	out := []map[string]any{}
	for _, in := range list {
		if in.Status == nip.StatusRemoved {
			continue
		}
		status := in.Status
		if status == nip.StatusActive && a.bankDownLocked(in.ID, now) {
			status = "outage"
		}
		out = append(out, map[string]any{"nip_code": in.NIPCode, "name": in.Name, "bank_id": in.ID, "status": status})
	}
	a.mu.Unlock()
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) position(b *config.Bank) (map[string]any, error) {
	sw := a.nibssStore()
	if sw == nil {
		return nil, errors.New("NIBSS is not available right now")
	}
	open, err := a.openPosition(sw, b.NIPCode)
	if err != nil {
		return nil, err
	}
	pos := open.netDebit()
	source := "configured"
	if b.CapIsDefault() {
		source = "default"
	}
	var collateral any
	if b.Collateral > 0 {
		collateral = b.Collateral
	}
	now := a.clock.Now()
	return map[string]any{"bank_id": b.ID, "nip_code": b.NIPCode, "net_debit_cap_minor": b.Cap(), "cap_source": source, "collateral_minor": collateral, "open_outward_minor": open.outward, "open_inward_minor": open.inward, "position_minor": pos, "headroom_minor": b.Cap() - pos, "next_session": ts(a.schedule().next(now))}, nil
}

func (a *App) ctlNIPPositions(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	banks := slices.Clone(a.cfg.Banks)
	a.mu.Unlock()
	out := []map[string]any{}
	for _, b := range banks {
		row, err := a.position(b)
		if err != nil {
			a.log.Error("NIP positions", "err", err)
			web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "The positions could not be worked out.")
			return
		}
		out = append(out, row)
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) ctlNIPSettlement(w http.ResponseWriter, r *http.Request) {
	c, err := a.CheckSettlement()
	if err != nil {
		a.log.Error("check settlement", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", web.SomethingWentWrong)
		return
	}
	web.WriteJSON(w, http.StatusOK, c)
}

func (a *App) ctlSettleNow(w http.ResponseWriter, r *http.Request) {
	n, err := a.SettleNow()
	if err != nil {
		a.log.Error("settle now", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", web.SomethingWentWrong)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"settled_sessions": n, "at": ts(a.clock.Now())})
}

func (a *App) ctlNIPSession(w http.ResponseWriter, r *http.Request) {
	a.writeNIPSession(w, r, a.Session)
}

func (a *App) ctlNIPSessionStatus(w http.ResponseWriter, r *http.Request) {
	a.writeNIPSession(w, r, a.QuerySession)
}

func (a *App) writeNIPSession(w http.ResponseWriter, r *http.Request, get func(string) (nibss.Session, error)) {
	x, err := get(r.PathValue("id"))
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

func (a *App) SetNetDebitCap(id string, cap, collateral int64) error {
	err := a.applyEdit(func(sp *setup.Spec) error {
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
	a.reconcileNow()
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
	if err := a.SetNetDebitCap(id, capMinor, collateral); err != nil {
		a.bankError(w, r, err)
		return
	}
	a.mu.Lock()
	b := a.cfg.Bank(id)
	a.mu.Unlock()
	row, err := a.position(b)
	if err != nil {
		a.log.Error("NIP positions", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "The position could not be worked out.")
		return
	}
	web.WriteJSON(w, http.StatusOK, row)
}

func (a *App) transferReply(r *http.Request, res server.TransferResult, err error) reply {
	var te *server.TransferError
	switch {
	case errors.As(err, &te):
		return jsonReply(te.Status, map[string]any{"error": web.APIError{Code: te.ErrCode, Message: web.Sentence(te.Error()), RequestID: web.RequestID(r)}, "response_code": te.Code, "session_id": res.SessionID})
	case err != nil:
		a.log.Error("transfer", "path", r.URL.Path, "err", err)
		return errReply(r, http.StatusInternalServerError, "internal_error", web.SomethingWentWrong)
	}
	return jsonReply(http.StatusOK, res)
}

func (a *App) bankTransfer(r *http.Request, bankID string, body server.TransferBody, key string) reply {
	amount, err := bank.ParseNaira(body.Amount)
	if err != nil {
		return errReply(r, http.StatusBadRequest, "invalid_request", err.Error())
	}
	if body.From == "" || body.To == "" {
		return errReply(r, http.StatusBadRequest, "invalid_request", "from and to are required account numbers.")
	}
	if len(key) > 255 {
		return errReply(r, http.StatusBadRequest, "invalid_request", "An Idempotency-Key must be 1 to 255 characters.")
	}
	req := server.TransferRequest{FromBank: bankID, From: body.From, ToBank: body.ToBank, To: body.To, Amount: amount, Narration: body.Narration, Key: key}
	if key == "" {
		res, err := a.Transfer(req)
		return a.transferReply(r, res, err)
	}
	scope := "control POST /banks/" + bankID + "/transfers key " + key
	hash := body.Hash(bankID)
	entry := a.idem.Acquire(scope)
	defer a.idem.Release(scope, entry)
	a.mu.Lock()
	rec := a.idemGetLocked(scope)
	a.mu.Unlock()
	if rec != nil {
		if rec.Hash != hash {
			return errReply(r, http.StatusUnprocessableEntity, "idempotency_key_reused", "That Idempotency-Key was used with a different transfer.")
		}
		return reply{status: rec.Status, body: rec.Body, replayed: true}
	}
	res, err := a.Transfer(req)
	rep := a.transferReply(r, res, err)
	if rep.status < http.StatusInternalServerError {
		a.mu.Lock()
		a.idemPutLocked(scope, hash, rep.status, "application/json", rep.body)
		a.dirty()
		a.mu.Unlock()
	}
	return rep
}

func (a *App) ctlBankQuote(w http.ResponseWriter, r *http.Request) {
	var body server.TransferBody
	if err := web.DecodeBody(r, &body); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"from":"9010000014","to_bank":"bank-b","to":"9020000011","amount":"25000"}.`)
		return
	}
	amount, err := bank.ParseNaira(body.Amount)
	if err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if body.From == "" || body.To == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "from and to are required account numbers.")
		return
	}
	res, err := a.Quote(r.Context(), server.TransferRequest{FromBank: r.PathValue("id"), From: body.From, ToBank: body.ToBank, To: body.To, Amount: amount, Narration: body.Narration})
	a.transferReply(r, res, err).write(w)
}

func (a *App) ctlBankTransfer(w http.ResponseWriter, r *http.Request) {
	var body server.TransferBody
	if err := web.DecodeBody(r, &body); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"from":"9010000014","to_bank":"bank-b","to":"9020000011","amount":"25000"}.`)
		return
	}
	rep := a.bankTransfer(r, r.PathValue("id"), body, r.Header.Get("Idempotency-Key"))
	rep.write(w)
	if rep.status == http.StatusOK {
		a.SyncAll(r.Context())
	}
}
