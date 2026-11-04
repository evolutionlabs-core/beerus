package connect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const nipRetention = 7 * 24 * time.Hour

type NIPFailure struct {
	SessionID   string    `json:"session_id" yaml:"session_id"`
	FromBank    string    `json:"from_bank"`
	ToCode      string    `json:"to_code"`
	Amount      int64     `json:"amount_minor"`
	Code        string    `json:"response_code"`
	Message     string    `json:"message"`
	At          time.Time `json:"at"`
	CreatedReal time.Time `json:"created_real"`
}

type NIPState struct {
	Failures map[string]*NIPFailure `json:"failures"`
	Sessions int64                  `json:"sessions"`
}

func (n *NIPState) fill() {
	if n.Failures == nil {
		n.Failures = map[string]*NIPFailure{}
	}
}

func (a *App) pruneNIPLocked(real time.Time) {
	for sid, f := range a.st.NIP.Failures {
		if real.Sub(f.CreatedReal) > nipRetention {
			delete(a.st.NIP.Failures, sid)
		}
	}
}

type reply struct {
	status   int
	body     []byte
	replayed bool
	synced   bool
	session  string
}

func jsonReply(status int, v any) reply {
	b, err := json.Marshal(v)
	if err != nil {
		return reply{status: http.StatusInternalServerError, body: []byte(`{"error":{"code":"internal_error","message":"The response could not be built."}}`)}
	}
	return reply{status: status, body: b}
}

func errReply(r *http.Request, status int, code, msg string) reply {
	return jsonReply(status, map[string]apiError{"error": {Code: code, Message: msg, RequestID: requestID(r)}})
}

func (p reply) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if p.replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.WriteHeader(p.status)
	_, _ = w.Write(p.body)
}

func (a *App) directoryLocked() *nip.Directory {
	members := []nip.Participant{}
	for _, b := range a.cfg.Banks {
		members = append(members, nip.Participant{Code: b.NIPCode, Name: b.Name, BankID: b.ID})
	}
	outside := []nip.Participant{}
	for _, o := range world.CounterpartyBanks() {
		outside = append(outside, nip.Participant{Code: o.NIPCode, Name: o.Name})
	}
	return nip.NewDirectory(members, outside)
}

type nameEnquiry struct {
	Code        string `json:"response_code"`
	Message     string `json:"message"`
	BankCode    string `json:"bank_code"`
	BankName    string `json:"bank_name,omitempty"`
	AccountNo   string `json:"account_number"`
	AccountName string `json:"account_name,omitempty"`
}

func (a *App) bankUnavailable(id string, at time.Time) bool {
	return a.world.Down(id, at) || a.world.Closed(id, at)
}

func (a *App) nameEnquiryLocked(code, nuban string, at time.Time) nameEnquiry {
	out := nameEnquiry{BankCode: code, AccountNo: nuban}
	p, ok := a.directoryLocked().Lookup(code)
	if !ok {
		out.Code, out.Message = nip.InvalidAccount, "No bank has that code."
		return out
	}
	out.BankName = p.Name
	if len(nuban) != 10 || strings.Trim(nuban, "0123456789") != "" {
		out.Code, out.Message = nip.InvalidAccount, "An account number has 10 digits."
		return out
	}
	if p.Outside {
		if !world.ValidNUBAN(p.Code, nuban) {
			out.Code, out.Message = nip.InvalidAccount, "That is not a valid account number."
			return out
		}
		out.Code, out.Message, out.AccountName = nip.OK, "Successful.", "BENEFICIARY "+nuban[6:]
		return out
	}
	if a.bankUnavailable(p.BankID, at) {
		out.Code, out.Message = nip.Unavailable, p.Name+" is not available right now."
		return out
	}
	for _, acc := range a.world.Accounts() {
		if acc.Institution == p.BankID && acc.NUBAN == nuban {
			out.Code, out.Message, out.AccountName = nip.OK, "Successful.", strings.ToUpper(acc.Name)
			return out
		}
	}
	out.Code, out.Message = nip.InvalidAccount, "No such account at "+p.Name+"."
	return out
}

func (a *App) ctlNameEnquiry(w http.ResponseWriter, r *http.Request) {
	code, nuban := r.URL.Query().Get("bank_code"), r.URL.Query().Get("account_number")
	if code == "" || nuban == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "bank_code and account_number are required.")
		return
	}
	a.mu.Lock()
	out := a.nameEnquiryLocked(code, nuban, a.clock.Now())
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (a *App) ctlNIPParticipants(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	now := a.clock.Now()
	out := []map[string]any{}
	for _, p := range a.directoryLocked().List() {
		status := "active"
		if !p.Outside && a.world.Closed(p.BankID, now) {
			status = "closed"
		} else if !p.Outside && a.world.Suspended(p.BankID, now) {
			status = "suspended"
		} else if !p.Outside && a.world.Down(p.BankID, now) {
			status = "outage"
		}
		out = append(out, map[string]any{"nip_code": p.Code, "name": p.Name, "bank_id": p.BankID, "outside": p.Outside, "status": status})
	}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (p position) netDebit() int64 {
	return max(p.outward-p.inward, 0)
}

func (a *App) positionLocked(b *config.Bank, now time.Time) (map[string]any, error) {
	open, err := a.openPositionLocked(b.ID, now, now)
	if err != nil {
		return nil, err
	}
	pos := open.netDebit()
	source := "configured"
	if b.CapIsDefault() {
		source = "simulator_default"
	}
	var collateral any
	if b.Collateral > 0 {
		collateral = b.Collateral
	}
	return map[string]any{"bank_id": b.ID, "nip_code": b.NIPCode, "net_debit_cap_minor": b.Cap(), "cap_source": source, "collateral_minor": collateral, "open_outward_minor": open.outward, "open_inward_minor": open.inward, "position_minor": pos, "headroom_minor": b.Cap() - pos, "next_session": ts(a.schedule().next(now))}, nil
}

func (a *App) ctlNIPPositions(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	now := a.clock.Now()
	out := []map[string]any{}
	var err error
	for _, b := range a.cfg.Banks {
		var row map[string]any
		if row, err = a.positionLocked(b, now); err != nil {
			break
		}
		out = append(out, row)
	}
	a.mu.Unlock()
	if err != nil {
		a.log.Error("NIP positions", "err", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The positions could not be worked out.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) ctlNIPSettlement(w http.ResponseWriter, r *http.Request) {
	c, err := a.CheckSettlement()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *App) SetNetDebitCap(id string, cap, collateral int64) error {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	b := a.cfg.Bank(id)
	if b == nil {
		return errNotFound
	}
	a.mu.Lock()
	prevCap, prevCollateral := b.NetDebitCap, b.Collateral
	b.NetDebitCap, b.Collateral = cap, collateral
	a.mu.Unlock()
	if err := a.saveBanksLocked(); err != nil {
		a.mu.Lock()
		b.NetDebitCap, b.Collateral = prevCap, prevCollateral
		a.mu.Unlock()
		return err
	}
	return nil
}

func (a *App) ctlNetDebitCap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cap        string `json:"cap"`
		Collateral string `json:"collateral"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"cap":"5000000","collateral":"5000000"}.`)
		return
	}
	capMinor, err := parseNaira(body.Cap)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "cap: "+err.Error())
		return
	}
	var collateral int64
	if body.Collateral != "" {
		if collateral, err = parseNaira(body.Collateral); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "collateral: "+err.Error())
			return
		}
	}
	id := r.PathValue("id")
	if err := a.SetNetDebitCap(id, capMinor, collateral); err != nil {
		a.bankError(w, r, err)
		return
	}
	a.mu.Lock()
	row, err := a.positionLocked(a.cfg.Bank(id), a.clock.Now())
	a.mu.Unlock()
	if err != nil {
		a.log.Error("NIP positions", "err", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The position could not be worked out.")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (a *App) ctlNIPStatus(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	a.mu.Lock()
	rep := a.statusLocked(sid)
	a.mu.Unlock()
	rep.write(w)
}

func (a *App) statusLocked(sid string) reply {
	if f := a.st.NIP.Failures[sid]; f != nil {
		return jsonReply(http.StatusOK, map[string]any{"session_id": sid, "status": nip.Failed, "response_code": f.Code, "message": f.Message, "amount_minor": f.Amount, "amount": naira(f.Amount), "from_bank": f.FromBank, "to_bank_code": f.ToCode, "initiated_at": ts(f.At)})
	}
	if nip.ValidSessionID(sid) {
		if tr := a.world.Trace(sid, a.clock.Now()); len(tr) > 0 {
			for _, t := range tr {
				if t.Line.Channel == "reversal" {
					return jsonReply(http.StatusOK, map[string]any{"session_id": sid, "status": nip.Reversed, "response_code": nip.SystemError, "message": "The transfer was reversed and the money went back to the sender.", "reversed_at": ts(t.Line.BookedAt)})
				}
			}
			return jsonReply(http.StatusOK, map[string]any{"session_id": sid, "status": nip.Success, "response_code": nip.OK, "message": "Successful."})
		}
	}
	return jsonReply(http.StatusNotFound, map[string]any{"session_id": sid, "status": nip.Failed, "response_code": nip.NoRecord, "message": "No transfer has that session ID."})
}

type transferBody struct {
	From      string `json:"from" yaml:"from"`
	To        string `json:"to" yaml:"to"`
	ToBank    string `json:"to_bank" yaml:"to_bank"`
	ToName    string `json:"to_name" yaml:"to_name"`
	Amount    string `json:"amount" yaml:"amount"`
	Narration string `json:"narration" yaml:"narration"`
	SessionID string `json:"session_id" yaml:"session_id"`
}

func (b transferBody) hash() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{b.From, b.To, b.ToBank, b.ToName, b.Amount, b.Narration}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func transferScopes(key, sid string) []string {
	out := []string{}
	if sid != "" {
		out = append(out, "control POST /transfers session "+sid)
	}
	if key != "" {
		out = append(out, "control POST /transfers key "+key)
	}
	return out
}

func (a *App) transferLocked(r *http.Request, body transferBody, amount int64, key string) reply {
	hash := body.hash()
	for _, scope := range transferScopes(key, body.SessionID) {
		if rec := a.idemGetLocked(scope); rec != nil {
			if rec.Hash != hash {
				return errReply(r, http.StatusUnprocessableEntity, "idempotency_key_reused", "That session ID or Idempotency-Key was used with a different transfer.")
			}
			return reply{status: rec.Status, body: rec.Body, replayed: true}
		}
	}
	if body.SessionID != "" && len(a.world.Trace(body.SessionID, a.clock.Now())) > 0 {
		return errReply(r, http.StatusConflict, "duplicate_session", "That session ID is already used by another transfer.")
	}
	rep := a.runTransferLocked(r, body, amount)
	if rep.status < http.StatusInternalServerError {
		for _, scope := range transferScopes(key, rep.session) {
			a.idemPutLocked(scope, hash, rep.status, "application/json", rep.body)
		}
		a.dirty()
	}
	return rep
}

func (a *App) runTransferLocked(r *http.Request, body transferBody, amount int64) reply {
	from, to := body.From, body.To
	if k, ok := a.resolveAccount(from); ok {
		from = k
	}
	if k, ok := a.resolveAccount(to); ok {
		to = k
	}
	req := world.TransferRequest{From: from, To: to, ToBank: body.ToBank, ToName: body.ToName, Amount: amount, Narration: body.Narration, SessionID: body.SessionID}
	if rt, err := a.world.Route(req); err == nil && rt.FromInst.ID != rt.ToInst.ID {
		if rep, handled := a.nipTransferLocked(r, req, rt); handled {
			return rep
		}
	}
	now := a.clock.Now()
	res, err := a.world.Transfer(now, req)
	if err != nil {
		f := transferFailure(err)
		return errReply(r, f.status, f.errCode, f.message)
	}
	rep := jsonReply(http.StatusOK, transferView(res, now))
	rep.synced, rep.session = true, body.SessionID
	return rep
}

func transferView(res world.TransferResult, at time.Time) map[string]any {
	return map[string]any{
		"result":        res,
		"amount":        naira(res.Amount),
		"charges":       naira(res.Fee + res.VAT + res.StampDuty),
		"total_debit":   naira(res.TotalDebit),
		"sender_after":  naira(res.BalanceAfter),
		"business_time": ts(at),
	}
}

type nipFailure struct {
	status  int
	code    string
	errCode string
	message string
}

func (a *App) failNIPLocked(r *http.Request, t NIPFailure, f nipFailure) reply {
	t.Code, t.Message, t.CreatedReal = f.code, f.message, a.clock.Real()
	a.st.NIP.Failures[t.SessionID] = &t
	a.dirty()
	rep := jsonReply(f.status, map[string]any{"error": apiError{Code: f.errCode, Message: f.message, RequestID: requestID(r)}, "response_code": f.code, "session_id": t.SessionID, "status": nip.Failed})
	rep.session = t.SessionID
	return rep
}

func bankFailure(err error) nipFailure {
	f := nipFailure{code: nip.Unavailable, message: transferMessage(err)}
	switch {
	case errors.Is(err, world.ErrBankSuspended):
		f.status, f.errCode = http.StatusConflict, "bank_suspended"
	case errors.Is(err, world.ErrBankClosed):
		f.status, f.errCode = http.StatusConflict, "bank_closed"
	default:
		f.status, f.errCode = http.StatusServiceUnavailable, "bank_unavailable"
	}
	return f
}

func transferFailure(err error) nipFailure {
	if errors.Is(err, world.ErrBankSuspended) || errors.Is(err, world.ErrBankClosed) || errors.Is(err, world.ErrBankUnavailable) {
		return bankFailure(err)
	}
	return nipFailure{status: http.StatusBadRequest, code: nip.SystemError, errCode: "invalid_request", message: err.Error()}
}

func (a *App) nipTransferLocked(r *http.Request, req world.TransferRequest, rt world.Route) (reply, bool) {
	now := a.clock.Now()
	sid := req.SessionID
	if sid == "" {
		a.st.NIP.Sessions++
		sid = nip.SessionID(a.cfg.Seed, rt.FromInst.NIPCode, now, int(a.st.NIP.Sessions))
	}
	t := NIPFailure{SessionID: sid, FromBank: rt.FromInst.ID, ToCode: rt.ToInst.NIPCode, Amount: req.Amount, At: now}
	if a.bankUnavailable(rt.FromInst.ID, now) {
		return reply{}, false
	}
	nuban := req.To
	if rt.To != nil {
		nuban = rt.To.NUBAN
	}
	ne := a.nameEnquiryLocked(rt.ToInst.NIPCode, nuban, now)
	switch ne.Code {
	case nip.Unavailable:
		_, err := a.world.Quote(now, req)
		if err == nil || !(errors.Is(err, world.ErrBankSuspended) || errors.Is(err, world.ErrBankClosed) || errors.Is(err, world.ErrBankUnavailable)) {
			err = fmt.Errorf("%w: %s is not available, so NIBSS rejected the transfer and nothing was debited", world.ErrBankUnavailable, rt.ToInst.Name)
		}
		return a.failNIPLocked(r, t, bankFailure(err)), true
	case nip.InvalidAccount:
		return a.failNIPLocked(r, t, nipFailure{status: http.StatusUnprocessableEntity, code: nip.InvalidAccount, errCode: "invalid_account", message: ne.Message + " Nothing was debited."}), true
	}
	if _, err := a.world.Quote(now, req); err != nil {
		return reply{}, false
	}
	if bank := a.cfg.Bank(rt.FromInst.ID); bank != nil {
		open, err := a.openPositionLocked(bank.ID, now, now)
		if err != nil {
			a.log.Error("NIP position", "bank", bank.ID, "err", err)
			return errReply(r, http.StatusInternalServerError, "internal_error", "The bank's NIP position could not be worked out."), true
		}
		if pos := open.netDebit(); pos+req.Amount > bank.Cap() {
			left := bank.Cap() - pos
			return a.failNIPLocked(r, t, nipFailure{status: http.StatusUnprocessableEntity, code: nip.OverCap, errCode: "net_debit_cap_exceeded", message: fmt.Sprintf("%s has reached its NIP net debit cap of %s. This transfer needs %s and %s is left. It clears at the next settlement session. Nothing was debited.", rt.FromInst.Name, naira(bank.Cap()), naira(req.Amount), naira(max(left, 0)))}), true
		}
	}
	req.SessionID = sid
	res, err := a.world.Transfer(now, req)
	if err != nil {
		return a.failNIPLocked(r, t, transferFailure(err)), true
	}
	out := transferView(res, now)
	out["session_id"], out["response_code"], out["status"] = sid, nip.OK, nip.Success
	rep := jsonReply(http.StatusOK, out)
	rep.synced, rep.session = true, sid
	return rep, true
}
