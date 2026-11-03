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

	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

type NIPTransfer struct {
	SessionID string          `json:"session_id"`
	Key       string          `json:"key,omitempty"`
	Hash      string          `json:"hash"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	FromBank  string          `json:"from_bank"`
	ToBank    string          `json:"to_bank"`
	ToCode    string          `json:"to_code"`
	Amount    int64           `json:"amount_minor"`
	Status    string          `json:"status"`
	Code      string          `json:"response_code"`
	Message   string          `json:"message"`
	At        time.Time       `json:"at"`
	HTTP      int             `json:"http_status"`
	Response  json.RawMessage `json:"response"`
}

type NIPState struct {
	Transfers map[string]*NIPTransfer `json:"transfers"`
	Keys      map[string]string       `json:"keys"`
}

func (n *NIPState) fill() {
	if n.Transfers == nil {
		n.Transfers = map[string]*NIPTransfer{}
	}
	if n.Keys == nil {
		n.Keys = map[string]string{}
	}
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
		out.Code, out.Message, out.AccountName = nip.OK, "Successful.", "BENEFICIARY "+nuban[6:]
		return out
	}
	if a.bankUnavailable(p.BankID, at) {
		out.Code, out.Message = nip.Unavailable, p.Name+" is not available right now."
		return out
	}
	for _, acc := range a.world.Accounts() {
		if acc.Institution == p.BankID && acc.NUBAN == nuban {
			pr, _ := a.world.Persona(acc.Owner)
			out.Code, out.Message, out.AccountName = nip.OK, "Successful.", strings.ToUpper(pr.Name)
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
	defer a.mu.Unlock()
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
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) openCutoff(t *NIPTransfer) time.Time {
	return a.schedule().next(t.At.Add(-time.Nanosecond))
}

func (a *App) netDebitLocked(bankID string, now time.Time) int64 {
	var net int64
	for _, t := range a.st.NIP.Transfers {
		if t.Status == nip.Failed || !a.openCutoff(t).After(now) {
			continue
		}
		if t.FromBank == bankID {
			net += t.Amount
		}
		if t.ToBank == bankID && t.Status == nip.Success {
			net -= t.Amount
		}
	}
	if net < 0 {
		return 0
	}
	return net
}

func (a *App) ctlNIPPositions(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	out := []map[string]any{}
	for _, b := range a.cfg.Banks {
		pos := a.netDebitLocked(b.ID, now)
		out = append(out, map[string]any{"bank_id": b.ID, "nip_code": b.NIPCode, "net_debit_cap_minor": b.Cap(), "collateral_minor": b.Cap(), "position_minor": pos, "headroom_minor": b.Cap() - pos, "next_session": ts(a.schedule().next(now))})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func nipView(t *NIPTransfer) map[string]any {
	return map[string]any{"session_id": t.SessionID, "status": t.Status, "response_code": t.Code, "message": t.Message, "amount_minor": t.Amount, "amount": naira(t.Amount), "from_bank": t.FromBank, "to_bank_code": t.ToCode, "initiated_at": ts(t.At)}
}

func (a *App) ctlNIPStatus(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	a.mu.Lock()
	defer a.mu.Unlock()
	if t := a.st.NIP.Transfers[sid]; t != nil {
		writeJSON(w, http.StatusOK, nipView(t))
		return
	}
	if nip.ValidSessionID(sid) {
		if tr := a.world.Trace(sid, a.clock.Now()); len(tr) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "status": nip.Success, "response_code": nip.OK, "message": "Successful."})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"session_id": sid, "status": nip.Failed, "response_code": nip.NoRecord, "message": "No transfer has that session ID."})
}

type transferBody struct {
	From      string `json:"from"`
	To        string `json:"to"`
	ToBank    string `json:"to_bank"`
	ToName    string `json:"to_name"`
	Amount    string `json:"amount"`
	Narration string `json:"narration"`
	SessionID string `json:"session_id"`
}

func (b transferBody) hash() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{b.From, b.To, b.ToBank, b.ToName, b.Amount, b.Narration}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (a *App) replayLocked(w http.ResponseWriter, r *http.Request, key string, b transferBody) bool {
	var t *NIPTransfer
	if b.SessionID != "" {
		t = a.st.NIP.Transfers[b.SessionID]
	}
	if t == nil && key != "" {
		t = a.st.NIP.Transfers[a.st.NIP.Keys[key]]
	}
	if t == nil {
		return false
	}
	if t.Hash != b.hash() {
		writeError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", "That session ID or Idempotency-Key was used with a different transfer.")
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(t.HTTP)
	_, _ = w.Write(t.Response)
	return true
}

type nipFailure struct {
	status  int
	code    string
	errCode string
	message string
}

func (a *App) recordLocked(t *NIPTransfer, status int, body any) {
	b, _ := json.Marshal(body)
	a.st.NIP.Transfers[t.SessionID] = t
	t.HTTP, t.Response = status, b
	if t.Key != "" {
		a.st.NIP.Keys[t.Key] = t.SessionID
	}
	a.dirty()
}

func (a *App) failNIPLocked(w http.ResponseWriter, r *http.Request, t *NIPTransfer, f nipFailure) {
	t.Status, t.Code, t.Message = nip.Failed, f.code, f.message
	body := map[string]any{"error": apiError{Code: f.errCode, Message: f.message, RequestID: requestID(r)}, "response_code": f.code, "session_id": t.SessionID, "status": nip.Failed}
	a.recordLocked(t, f.status, body)
	writeJSON(w, f.status, body)
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

func (a *App) nipTransferLocked(w http.ResponseWriter, r *http.Request, body transferBody, req world.TransferRequest, rt world.Route, key string) string {
	now := a.clock.Now()
	sid := body.SessionID
	if sid == "" {
		sid = nip.SessionID(a.cfg.Seed, rt.FromInst.NIPCode, now, len(a.st.NIP.Transfers))
	}
	t := &NIPTransfer{SessionID: sid, Key: key, Hash: body.hash(), From: rt.From.Key, To: req.To, FromBank: rt.FromInst.ID, ToCode: rt.ToInst.NIPCode, Amount: req.Amount, Status: nip.Pending, At: now}
	if p, ok := a.directoryLocked().Lookup(rt.ToInst.NIPCode); ok {
		t.ToBank = p.BankID
	}
	if a.bankUnavailable(rt.FromInst.ID, now) {
		return ""
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
		a.failNIPLocked(w, r, t, bankFailure(err))
		return nip.Failed
	case nip.InvalidAccount:
		a.failNIPLocked(w, r, t, nipFailure{status: http.StatusUnprocessableEntity, code: nip.InvalidAccount, errCode: "invalid_account", message: ne.Message + " Nothing was debited."})
		return nip.Failed
	}
	if _, err := a.world.Quote(now, req); err != nil {
		return ""
	}
	bank := a.cfg.Bank(rt.FromInst.ID)
	pos := a.netDebitLocked(rt.FromInst.ID, now)
	if bank != nil && pos+req.Amount > bank.Cap() {
		left := bank.Cap() - pos
		a.failNIPLocked(w, r, t, nipFailure{status: http.StatusUnprocessableEntity, code: nip.OverCap, errCode: "net_debit_cap_exceeded", message: fmt.Sprintf("%s has reached its NIP net debit cap of %s. This transfer needs %s and %s is left. It clears at the next settlement session. Nothing was debited.", rt.FromInst.Name, naira(bank.Cap()), naira(req.Amount), naira(max(left, 0)))})
		return nip.Failed
	}
	a.st.NIP.Transfers[sid] = t
	req.SessionID = sid
	res, err := a.world.Transfer(now, req)
	if err != nil {
		a.failNIPLocked(w, r, t, nipFailure{status: http.StatusBadRequest, code: nip.SystemError, errCode: "invalid_request", message: err.Error()})
		return nip.Failed
	}
	t.Status, t.Code, t.Message = nip.Success, nip.OK, "Successful."
	out := map[string]any{
		"result":        res,
		"amount":        naira(res.Amount),
		"charges":       naira(res.Fee + res.VAT + res.StampDuty),
		"total_debit":   naira(res.TotalDebit),
		"sender_after":  naira(res.BalanceAfter),
		"session_id":    sid,
		"response_code": nip.OK,
		"status":        nip.Success,
		"business_time": ts(now),
	}
	a.recordLocked(t, http.StatusOK, out)
	writeJSON(w, http.StatusOK, out)
	return nip.Success
}
