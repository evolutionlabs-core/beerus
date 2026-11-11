package connect

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

type TransferRequest struct {
	FromBank  string `json:"from_bank"`
	From      string `json:"from"`
	ToBank    string `json:"to_bank"`
	To        string `json:"to"`
	Amount    int64  `json:"amount_minor"`
	Narration string `json:"narration"`
	Key       string `json:"-"`
	Closure   bool   `json:"-"`
}

type TransferResult struct {
	Kind         string     `json:"kind"`
	FromBank     string     `json:"from_bank"`
	From         string     `json:"from"`
	ToBank       string     `json:"to_bank"`
	To           string     `json:"to"`
	ToName       string     `json:"to_name"`
	SessionID    string     `json:"session_id,omitempty"`
	Status       string     `json:"status"`
	ResponseCode string     `json:"response_code"`
	Amount       int64      `json:"amount_minor"`
	Fee          int64      `json:"fee_minor"`
	VAT          int64      `json:"vat_minor"`
	StampDuty    int64      `json:"stamp_duty_minor"`
	TotalDebit   int64      `json:"total_debit_minor"`
	BalanceAfter int64      `json:"sender_balance_after_minor"`
	NextCheckAt  *time.Time `json:"next_check_at,omitempty"`
	At           time.Time  `json:"business_time"`
}

var (
	errSameAccount   = errors.New("the from and to accounts are the same")
	errBankNotActive = errors.New("bank is not active")
	errOverCap       = errors.New("net debit cap reached")
)

type notActiveError struct {
	name  string
	phase string
}

func (e notActiveError) Error() string {
	return fmt.Sprintf("%s is %s, so it cannot do this now", e.name, e.phase)
}

func (notActiveError) Is(target error) bool {
	return target == errBankNotActive
}

type transferError struct {
	code    string
	errCode string
	status  int
	err     error
}

func (e *transferError) Error() string {
	return e.err.Error()
}

func (e *transferError) Unwrap() error {
	return e.err
}

func refused(status int, code, errCode string, err error) error {
	return &transferError{code: code, errCode: errCode, status: status, err: err}
}

type transferRoute struct {
	from, to       *ledgerSource
	fromBank       string
	toBank         string
	fromName       string
	toName         string
	fromCode       string
	toCode         string
	cap            int64
	sessionCounter int64
}

func (a *App) route(req TransferRequest, session bool) (transferRoute, error) {
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	a.mu.Lock()
	from, to := a.bankLocked(req.FromBank), a.bankLocked(req.ToBank)
	a.mu.Unlock()
	if from == nil {
		return transferRoute{}, refused(http.StatusNotFound, nip.SystemError, "not_found", fmt.Errorf("no bank %q", req.FromBank))
	}
	if to == nil {
		return transferRoute{}, refused(http.StatusUnprocessableEntity, nip.SystemError, "unknown_bank", fmt.Errorf("find bank %q: %w", req.ToBank, nibss.ErrUnknownInstitution))
	}
	if phase := a.bankPhase(from.ID); phase != PhaseActive && !req.Closure {
		return transferRoute{}, refused(http.StatusConflict, nip.Unavailable, "bank_not_active", notActiveError{from.Name, phase})
	}
	if a.bankPhase(to.ID) == PhaseClosed {
		return transferRoute{}, refused(http.StatusUnprocessableEntity, nip.Unavailable, "bank_closed", fmt.Errorf("%s is closed and takes no more transfers. Nothing was debited", to.Name))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rt := transferRoute{from: a.ledgerFor(from.ID), to: a.ledgerFor(to.ID), fromBank: from.ID, toBank: to.ID, fromName: from.Name, toName: to.Name, fromCode: from.NIPCode, toCode: to.NIPCode, cap: from.Cap()}
	if rt.from == nil {
		return transferRoute{}, refused(http.StatusServiceUnavailable, nip.Unavailable, "bank_unavailable", fmt.Errorf("the books of %s are not open yet", from.Name))
	}
	if session && from.ID != to.ID {
		a.st.NIP.Sessions++
		rt.sessionCounter = a.st.NIP.Sessions
		a.dirty()
	}
	return rt, nil
}

func (a *App) Transfer(req TransferRequest) (TransferResult, error) {
	a.gate.RLock()
	defer a.gate.RUnlock()
	return a.transfer(req)
}

const maxNarrationRunes = 100

func checkNarration(n string) error {
	if count := utf8.RuneCountInString(n); count > maxNarrationRunes {
		return fmt.Errorf("narration has %d characters; the limit is %d", count, maxNarrationRunes)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return errors.New("narration must not contain control characters")
		}
	}
	return nil
}

func (a *App) transfer(req TransferRequest) (TransferResult, error) {
	if req.Amount <= 0 {
		return TransferResult{}, refused(http.StatusBadRequest, nip.SystemError, "invalid_request", errors.New("amount must be above zero"))
	}
	if err := checkNarration(req.Narration); err != nil {
		return TransferResult{}, refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_request", err)
	}
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	rt, err := a.route(req, req.Key == "")
	if err != nil {
		return TransferResult{}, err
	}
	now := a.clock.Now()
	res := TransferResult{FromBank: rt.fromBank, From: req.From, ToBank: rt.toBank, To: req.To, Amount: req.Amount, At: now}
	if rt.fromBank == rt.toBank {
		return a.internalTransfer(rt, req, res)
	}
	return a.nipTransfer(rt, req, res)
}

func (a *App) Quote(req TransferRequest) (TransferResult, error) {
	if req.Amount <= 0 {
		return TransferResult{}, refused(http.StatusBadRequest, nip.SystemError, "invalid_request", errors.New("amount must be above zero"))
	}
	if err := checkNarration(req.Narration); err != nil {
		return TransferResult{}, refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_request", err)
	}
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	rt, err := a.route(req, false)
	if err != nil {
		return TransferResult{}, err
	}
	ctx := context.Background()
	b := rt.from.Bank()
	now := a.clock.Now()
	res := TransferResult{FromBank: rt.fromBank, From: req.From, ToBank: rt.toBank, To: req.To, Amount: req.Amount, At: now}
	sender, err := b.L.Account(ctx, bank.CustomerAccount(req.From))
	if err != nil {
		return res, accountError("from", req.From, err)
	}
	if rt.fromBank == rt.toBank {
		if req.From == req.To {
			return res, refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_request", errSameAccount)
		}
		to, err := b.L.Account(ctx, bank.CustomerAccount(req.To))
		if err != nil {
			return res, accountError("to", req.To, err)
		}
		res.Kind, res.ToName, res.TotalDebit = "internal", to.Name, req.Amount
	} else {
		name, err := a.enquire(ctx, rt, req.To)
		if err != nil {
			return res, err
		}
		c := charges.ForTransfer(req.Amount, clock.DateOf(now), false)
		res.Kind, res.ToName = "nip", name
		res.Fee, res.VAT, res.StampDuty = c.Fee, c.VAT, c.StampDuty
		if res.TotalDebit, err = totalDebit(req.Amount, c); err != nil {
			return res, err
		}
	}
	bal, err := b.BalanceAt(ctx, req.From, now)
	if err != nil {
		return res, fmt.Errorf("read balance of %s: %w", req.From, err)
	}
	if room, ok := addMinor(bal, sender.OverdraftLimit); ok && room < res.TotalDebit {
		return res, postingError(req.From, ledger.ErrInsufficientFunds)
	}
	res.BalanceAfter = bal - res.TotalDebit
	return res, nil
}

func (a *App) enquire(ctx context.Context, rt transferRoute, nuban string) (string, error) {
	if a.role == RoleBank {
		return a.remoteEnquiry(rt.toCode, nuban)
	}
	sw := a.nibssStore()
	if sw == nil {
		return "", refused(http.StatusServiceUnavailable, nip.Unavailable, "nibss_unavailable", errors.New("NIBSS is not available right now"))
	}
	name, err := sw.NameEnquiry(ctx, rt.toCode, nuban)
	if err != nil {
		return "", enquiryError(rt.toName, err)
	}
	return name, nil
}

func (a *App) internalTransfer(rt transferRoute, req TransferRequest, res TransferResult) (TransferResult, error) {
	if req.From == req.To {
		return res, refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_request", errSameAccount)
	}
	b := rt.from.Bank()
	ctx := context.Background()
	to, err := b.L.Account(ctx, bank.CustomerAccount(req.To))
	if err != nil {
		return res, accountError("to", req.To, err)
	}
	key := req.Key
	if key == "" {
		key = newID("trf")
	}
	if _, err := b.OwnTransfer(ctx, key, req.From, req.To, req.Amount, req.Narration, res.At); err != nil {
		return res, postingError(req.From, err)
	}
	res.Kind, res.Status, res.ResponseCode, res.ToName, res.TotalDebit = "internal", nip.Success, nip.OK, to.Name, req.Amount
	res.BalanceAfter, err = b.BalanceAt(ctx, req.From, res.At)
	return res, err
}

func accountError(side, nuban string, err error) error {
	if errors.Is(err, ledger.ErrUnknownAccount) {
		return refused(http.StatusUnprocessableEntity, nip.InvalidAccount, "invalid_account", fmt.Errorf("the %s account %s does not exist", side, nuban))
	}
	return err
}

func postingError(nuban string, err error) error {
	switch {
	case errors.Is(err, ledger.ErrUnknownAccount):
		return accountError("from", nuban, err)
	case errors.Is(err, ledger.ErrInsufficientFunds):
		return refused(http.StatusUnprocessableEntity, nip.NoFunds, "insufficient_funds", fmt.Errorf("account %s does not have enough money for this transfer and its charges", nuban))
	case errors.Is(err, ledger.ErrAccountClosed), errors.Is(err, ledger.ErrAccountFrozen), errors.Is(err, ledger.ErrPostNoDebit):
		return refused(http.StatusUnprocessableEntity, nip.Restricted, "account_restricted", fmt.Errorf("account %s cannot be debited: %w", nuban, err))
	case errors.Is(err, ledger.ErrDailyLimit):
		return refused(http.StatusUnprocessableEntity, nip.OverCap, "daily_limit", fmt.Errorf("account %s has reached its daily limit", nuban))
	case errors.Is(err, ledger.ErrIdempotency):
		return refused(http.StatusUnprocessableEntity, nip.SystemError, "idempotency_key_reused", errors.New("that idempotency key was used for a different transfer"))
	}
	return err
}

func enquiryError(name string, err error) error {
	switch {
	case errors.Is(err, nibss.ErrUnavailable):
		return refused(http.StatusServiceUnavailable, nip.Unavailable, "bank_unavailable", fmt.Errorf("%s is not available right now, so nothing was debited", name))
	case errors.Is(err, nibss.ErrInvalidAccount):
		return refused(http.StatusUnprocessableEntity, nip.InvalidAccount, "invalid_account", fmt.Errorf("that is not a valid account at %s, so nothing was debited", name))
	case errors.Is(err, nibss.ErrNoRecord):
		return refused(http.StatusUnprocessableEntity, nip.NoRecord, "no_record", fmt.Errorf("%s has no such account, so nothing was debited", name))
	case errors.Is(err, nibss.ErrUnknownInstitution):
		return refused(http.StatusUnprocessableEntity, nip.SystemError, "unknown_bank", err)
	}
	return err
}

func (a *App) sendLock(bankID string) *sync.Mutex {
	m, _ := a.sendLocks.LoadOrStore(bankID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func totalDebit(amount int64, c charges.Transfer) (int64, error) {
	total, ok := addMinor(amount, c.Fee)
	if ok {
		total, ok = addMinor(total, c.VAT)
	}
	if ok {
		total, ok = addMinor(total, c.StampDuty)
	}
	if !ok {
		return 0, refused(http.StatusUnprocessableEntity, nip.SystemError, "amount_too_large", fmt.Errorf("%s is too large to add the transfer charges to. Nothing was debited", naira(amount)))
	}
	return total, nil
}

func (a *App) newSessionID(rt transferRoute, req TransferRequest, at time.Time) string {
	seq := int(rt.sessionCounter)
	if req.Key != "" {
		sum := sha256.Sum256([]byte(rt.fromCode + "\x00" + req.Key))
		seq = -int(binary.BigEndian.Uint32(sum[:4])) - 1
	}
	return nip.SessionID(int64(a.setupKey), rt.fromCode, at, seq)
}

func (a *App) nipTransfer(rt transferRoute, req TransferRequest, res TransferResult) (TransferResult, error) {
	sw := a.nibssStore()
	if sw == nil {
		return res, refused(http.StatusServiceUnavailable, nip.Unavailable, "nibss_unavailable", errors.New("NIBSS is not available right now, so nothing was debited"))
	}
	ctx := context.Background()
	b := rt.from.Bank()
	sender, err := b.L.Account(ctx, bank.CustomerAccount(req.From))
	if err != nil {
		return res, accountError("from", req.From, err)
	}
	sendKey := ""
	var prior bank.PriorSend
	found := false
	if req.Key != "" {
		sendKey = "k:" + req.Key
		if prior, found, err = b.PriorNIPSend(ctx, sendKey); err != nil {
			return res, err
		}
	}
	name := prior.To.Name
	if found {
		res.At = prior.At.In(clock.Lagos)
	} else if name, err = sw.NameEnquiry(ctx, rt.toCode, req.To); err != nil {
		return res, enquiryError(rt.toName, err)
	}
	res.Kind, res.ToName = "nip", name
	if _, err := totalDebit(req.Amount, charges.ForTransfer(req.Amount, clock.DateOf(res.At), false)); err != nil {
		return res, err
	}
	lock := a.sendLock(rt.fromBank)
	lock.Lock()
	sess, replayed, err := a.beginSend(ctx, sw, rt, req, &res, found, sendKey, prior.SessionID, name)
	lock.Unlock()
	if err != nil {
		return res, err
	}
	return a.afterBegin(sw, rt, req, res, sess, replayed, sender.Name)
}

func (a *App) beginSend(ctx context.Context, sw *nibss.Store, rt transferRoute, req TransferRequest, res *TransferResult, found bool, sendKey, sid, name string) (nibss.Session, bool, error) {
	if !found {
		open, err := a.openPosition(sw, rt.fromCode)
		if err != nil {
			return nibss.Session{}, false, err
		}
		pos := open.netDebit()
		if need, ok := addMinor(pos, req.Amount); (!ok || need > rt.cap) && !req.Closure {
			return nibss.Session{}, false, refused(http.StatusUnprocessableEntity, nip.OverCap, "net_debit_cap_exceeded", fmt.Errorf("%s has reached its NIP net debit cap of %s. This transfer needs %s and %s is left. It clears at the next settlement session. Nothing was debited: %w", rt.fromName, naira(rt.cap), naira(req.Amount), naira(max(rt.cap-pos, 0)), errOverCap))
		}
		sid = a.newSessionID(rt, req, res.At)
		if sendKey == "" {
			sendKey = sid
		}
	}
	res.SessionID = sid
	b := rt.from.Bank()
	p, c, err := b.SendNIP(ctx, bank.NIPSend{Key: sendKey, From: req.From, Amount: req.Amount, SessionID: sid, To: bank.Counterparty{Name: name, AccountNumber: req.To, BankCode: rt.toCode, BankName: rt.toName}, At: res.At, NoCharges: req.Closure, Narration: req.Narration})
	if err != nil {
		return nibss.Session{}, false, postingError(req.From, err)
	}
	res.Fee, res.VAT, res.StampDuty = c.Fee, c.VAT, c.StampDuty
	var terr error
	if res.TotalDebit, terr = totalDebit(req.Amount, c); terr != nil {
		return nibss.Session{}, false, terr
	}
	x := nibss.Session{ID: sid, FromCode: rt.fromCode, ToCode: rt.toCode, FromNUBAN: req.From, ToNUBAN: req.To, Amount: req.Amount, Narration: req.Narration, CreatedAt: res.At, Closure: req.Closure}
	if !p.Replayed {
		if held := a.takeTimeout(rt.toBank); held != "" {
			next := res.At.Add(a.cfg.TSQAfter)
			x.Outcome, x.NextTSQAt = held, &next
		}
	}
	sess, err := sw.Begin(ctx, x)
	if err != nil && p.Replayed {
		return nibss.Session{}, false, fmt.Errorf("begin NIP session %s: %w", sid, err)
	}
	if err != nil {
		if rerr := a.reverseSend(rt.from, sid, res.At); rerr != nil {
			return nibss.Session{}, false, errors.Join(fmt.Errorf("begin NIP session %s: %w", sid, err), rerr)
		}
		return nibss.Session{ID: sid, Status: nip.Unavailable, Outcome: nibss.OutcomeReversed}, false, nil
	}
	return sess, p.Replayed, nil
}

func (a *App) afterBegin(sw *nibss.Store, rt transferRoute, req TransferRequest, res TransferResult, sess nibss.Session, replayed bool, senderName string) (TransferResult, error) {
	sid := sess.ID
	switch {
	case sess.Status == nibss.SessionOK:
		res.Status, res.ResponseCode = nip.Success, nip.OK
	case sess.Status != nibss.SessionPending:
		if sess.Outcome != nibss.OutcomeReversed {
			if err := a.fail(sw, rt.from, sid, sess.Status, res.At); err != nil {
				return res, err
			}
		}
		res.Status, res.ResponseCode = nip.Reversed, sess.Status
	case sess.Outcome != "":
		if sess.Outcome == nibss.OutcomeCredited {
			if err := a.credit(rt, req, sid, senderName, res.At); err != nil {
				a.log.Warn("NIP credit failed; reversing", "session", sid, "err", err)
				if rerr := sw.RevokeCredit(context.Background(), sid); rerr != nil {
					return res, errors.Join(err, rerr)
				}
				return a.reverseFailed(sw, rt, req, res, err)
			}
		}
		res.Status, res.ResponseCode, res.NextCheckAt = nip.Pending, nibss.SessionPending, sess.NextTSQAt
	default:
		if err := a.credit(rt, req, sid, senderName, res.At); err != nil {
			a.log.Warn("NIP credit failed; reversing", "session", sid, "err", err)
			return a.reverseFailed(sw, rt, req, res, err)
		}
		if err := sw.Resolve(context.Background(), sid, nibss.SessionOK, nibss.OutcomeCredited, res.At); err != nil {
			return res, fmt.Errorf("resolve NIP session %s: %w", sid, err)
		}
		res.Status, res.ResponseCode = nip.Success, nip.OK
	}
	return a.finish(rt, req, res)
}

func (a *App) reverseFailed(sw *nibss.Store, rt transferRoute, req TransferRequest, res TransferResult, cause error) (TransferResult, error) {
	if rerr := a.fail(sw, rt.from, res.SessionID, nip.Unavailable, res.At); rerr != nil {
		return res, errors.Join(cause, rerr)
	}
	res.Status, res.ResponseCode = nip.Reversed, nip.Unavailable
	return a.finish(rt, req, res)
}

func (a *App) finish(rt transferRoute, req TransferRequest, res TransferResult) (TransferResult, error) {
	bal, err := rt.from.Bank().BalanceAt(context.Background(), req.From, res.At)
	res.BalanceAfter = bal
	return res, err
}

func (a *App) credit(rt transferRoute, req TransferRequest, sid, senderName string, at time.Time) error {
	if rt.to == nil {
		return fmt.Errorf("the books of %s are not open: %w", rt.toName, nibss.ErrUnavailable)
	}
	if a.bankDown(rt.toBank, at) {
		return fmt.Errorf("%s is down: %w", rt.toName, nibss.ErrUnavailable)
	}
	_, err := rt.to.Bank().ReceiveNIP(context.Background(), sid, req.To, req.Amount, bank.Counterparty{Name: senderName, AccountNumber: req.From, BankCode: rt.fromCode, BankName: rt.fromName}, req.Narration, req.Closure, at)
	if err != nil {
		return fmt.Errorf("credit %s at %s: %w", req.To, rt.toName, err)
	}
	return nil
}

func (a *App) reverseSend(from *ledgerSource, sid string, at time.Time) error {
	b := from.Bank()
	entry, err := b.SentNIP(context.Background(), sid)
	if err != nil {
		return err
	}
	if _, err := b.ReverseNIP(context.Background(), entry, sid, at); err != nil && !errors.Is(err, ledger.ErrAlreadyReversed) {
		return err
	}
	return nil
}

func (a *App) fail(sw *nibss.Store, from *ledgerSource, sid, code string, at time.Time) error {
	ctx := context.Background()
	if err := sw.Resolve(ctx, sid, code, nibss.OutcomeFailed, at); err != nil {
		return fmt.Errorf("fail NIP session %s: %w", sid, err)
	}
	if err := a.reverseSend(from, sid, at); err != nil {
		return fmt.Errorf("reverse NIP session %s: %w", sid, err)
	}
	if err := sw.Resolve(ctx, sid, code, nibss.OutcomeReversed, at); err != nil {
		return fmt.Errorf("mark NIP session %s reversed: %w", sid, err)
	}
	return nil
}

func (a *App) bankByCode(code string) *ledgerSource {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, l := range a.ledgers {
		if l.inst.NIPCode == code {
			return l
		}
	}
	return nil
}

func (a *App) checkSessions(now time.Time) error {
	a.gate.RLock()
	defer a.gate.RUnlock()
	sw := a.nibssStore()
	if sw == nil {
		return nil
	}
	ctx := context.Background()
	due, err := sw.Due(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	for _, x := range due {
		if err := a.checkSession(sw, x, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *App) checkSession(sw *nibss.Store, x nibss.Session, now time.Time) error {
	ctx := context.Background()
	from := a.bankByCode(x.FromCode)
	if from == nil {
		return fmt.Errorf("session %s: the sending bank %s is not open", x.ID, x.FromCode)
	}
	q, err := sw.TSQ(ctx, x.ID, now.Add(a.cfg.TSQAfter))
	if err != nil {
		return err
	}
	switch {
	case q.Outcome == nibss.OutcomeCredited:
		return sw.Resolve(ctx, x.ID, nibss.SessionOK, nibss.OutcomeCredited, now)
	case q.Outcome == nibss.OutcomeUnknown && q.TSQAttempts < a.cfg.TSQAttempts:
		return nil
	}
	return a.fail(sw, from, x.ID, nip.Unavailable, now)
}

func (a *App) Session(id string) (nibss.Session, error) {
	sw := a.nibssStore()
	if sw == nil {
		return nibss.Session{}, errors.New("NIBSS is not available right now")
	}
	return sw.Session(context.Background(), strings.TrimSpace(id))
}

func (a *App) QuerySession(id string) (nibss.Session, error) {
	x, err := a.Session(id)
	if err != nil || x.Status != nibss.SessionPending {
		return x, err
	}
	a.gate.RLock()
	sw := a.nibssStore()
	if sw == nil {
		a.gate.RUnlock()
		return nibss.Session{}, errors.New("NIBSS is not available right now")
	}
	err = a.checkSession(sw, x, a.clock.Now())
	a.gate.RUnlock()
	if err != nil {
		return nibss.Session{}, err
	}
	return a.Session(id)
}
