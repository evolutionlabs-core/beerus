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

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

var errOverCap = errors.New("net debit cap reached")

type transferRoute struct {
	server.Route
	from, to       *ledgerSource
	sessionCounter int64
}

func (a *App) route(req server.TransferRequest, session bool) (transferRoute, error) {
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	a.mu.Lock()
	from, to := a.bankLocked(req.FromBank), a.bankLocked(req.ToBank)
	a.mu.Unlock()
	var fromPhase, toPhase string
	if from != nil {
		fromPhase = a.bankPhase(from.ID)
	}
	if to != nil {
		toPhase = a.bankPhase(to.ID)
	}
	route, err := server.NewRoute(req, from, to, fromPhase, toPhase)
	if err != nil {
		return transferRoute{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rt := transferRoute{Route: route, from: a.ledgerFor(from.ID), to: a.ledgerFor(to.ID)}
	if rt.from == nil {
		return transferRoute{}, server.Refused(http.StatusServiceUnavailable, nip.Unavailable, "bank_unavailable", fmt.Errorf("the books of %s are not open yet", from.Name))
	}
	if session && from.ID != to.ID {
		a.st.NIP.Sessions++
		rt.sessionCounter = a.st.NIP.Sessions
		a.dirty()
	}
	return rt, nil
}

func (a *App) Transfer(req server.TransferRequest) (server.TransferResult, error) {
	if err := server.CheckRequest(req); err != nil {
		return server.TransferResult{}, err
	}
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	rt, err := a.route(req, req.Key == "")
	if err != nil {
		return server.TransferResult{}, err
	}
	now := a.clock.Now()
	res := server.TransferResult{FromBank: rt.FromBank, From: req.From, ToBank: rt.ToBank, To: req.To, Amount: req.Amount, At: now}
	if rt.FromBank == rt.ToBank {
		return a.internalTransfer(rt, req, res)
	}
	return a.nipTransfer(rt, req, res)
}

func (a *App) Quote(ctx context.Context, req server.TransferRequest) (server.TransferResult, error) {
	if err := server.CheckRequest(req); err != nil {
		return server.TransferResult{}, err
	}
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	rt, err := a.route(req, false)
	if err != nil {
		return server.TransferResult{}, err
	}
	return server.Quoter{Fees: a.cfg.Fees(), Clock: a.clock, Enquirer: a}.Quote(ctx, rt.from.Bank(), rt.Route, req)
}

func (a *App) Enquire(ctx context.Context, rt server.Route, nuban string) (string, error) {
	return a.enquiredName(ctx, rt, nuban)
}

func (a *App) enquiredName(ctx context.Context, rt server.Route, nuban string) (string, error) {
	out, err := a.NameEnquiry(ctx, rt.ToCode, nuban)
	var away *server.UnavailableError
	if errors.As(err, &away) {
		return "", server.Refused(http.StatusServiceUnavailable, nip.Unavailable, "nibss_unavailable", errors.New(away.Msg))
	}
	if err != nil {
		return "", err
	}
	return enquiryResult(rt.ToName, out)
}

func (a *App) internalTransfer(rt transferRoute, req server.TransferRequest, res server.TransferResult) (server.TransferResult, error) {
	if req.From == req.To {
		return res, server.Refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_request", server.ErrSameAccount)
	}
	b := rt.from.Bank()
	ctx := context.Background()
	to, err := b.L.Account(ctx, bank.CustomerAccount(req.To))
	if err != nil {
		return res, server.AccountError("to", req.To, err)
	}
	key := req.Key
	if key == "" {
		key = web.NewID("trf")
	}
	if _, err := b.OwnTransfer(ctx, key, req.From, req.To, req.Amount, req.Narration, res.At); err != nil {
		return res, server.PostingError(req.From, err)
	}
	res.Kind, res.Status, res.ResponseCode, res.ToName, res.TotalDebit = "internal", nip.Success, nip.OK, to.Name, req.Amount
	res.BalanceAfter, err = b.BalanceAt(ctx, req.From, res.At)
	return res, err
}

func enquiryResult(name string, out server.NameEnquiry) (string, error) {
	switch out.Code {
	case nip.OK:
		return out.AccountName, nil
	case nip.Unavailable:
		return "", server.Refused(http.StatusServiceUnavailable, nip.Unavailable, "bank_unavailable", fmt.Errorf("%s is not available right now, so nothing was debited", name))
	case nip.InvalidAccount:
		return "", server.Refused(http.StatusUnprocessableEntity, nip.InvalidAccount, "invalid_account", fmt.Errorf("that is not a valid account at %s, so nothing was debited", name))
	case nip.NoRecord:
		return "", server.Refused(http.StatusUnprocessableEntity, nip.NoRecord, "no_record", fmt.Errorf("%s has no such account, so nothing was debited", name))
	}
	return "", server.Refused(http.StatusUnprocessableEntity, nip.SystemError, "unknown_bank", errors.New(out.Message))
}

func (a *App) sendLock(bankID string) *sync.Mutex {
	m, _ := a.sendLocks.LoadOrStore(bankID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func (a *App) newSessionID(rt transferRoute, req server.TransferRequest, at time.Time) string {
	seq := int(rt.sessionCounter)
	if req.Key != "" {
		sum := sha256.Sum256([]byte(rt.FromCode + "\x00" + req.Key))
		seq = -int(binary.BigEndian.Uint32(sum[:4])) - 1
	}
	return nip.SessionID(int64(a.setupKey), rt.FromCode, at, seq)
}

func (a *App) nipTransfer(rt transferRoute, req server.TransferRequest, res server.TransferResult) (server.TransferResult, error) {
	sw := a.nibssStore()
	if sw == nil {
		return res, server.Refused(http.StatusServiceUnavailable, nip.Unavailable, "nibss_unavailable", errors.New("NIBSS is not available right now, so nothing was debited"))
	}
	ctx := context.Background()
	b := rt.from.Bank()
	sender, err := b.L.Account(ctx, bank.CustomerAccount(req.From))
	if err != nil {
		return res, server.AccountError("from", req.From, err)
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
	} else if name, err = a.enquiredName(ctx, rt.Route, req.To); err != nil {
		return res, err
	}
	res.Kind, res.ToName = "nip", name
	if _, err := server.TotalDebit(req.Amount, a.cfg.Fees().ForTransfer(req.Amount)); err != nil {
		return res, err
	}
	lock := a.sendLock(rt.FromBank)
	lock.Lock()
	sess, replayed, err := a.beginSend(ctx, sw, rt, req, &res, found, sendKey, prior.SessionID, name)
	lock.Unlock()
	if err != nil {
		return res, err
	}
	return a.afterBegin(sw, rt, req, res, sess, replayed, sender.Name)
}

func (a *App) beginSend(ctx context.Context, sw *nibss.Store, rt transferRoute, req server.TransferRequest, res *server.TransferResult, found bool, sendKey, sid, name string) (nibss.Session, bool, error) {
	if !found {
		open, err := a.openPosition(sw, rt.FromCode)
		if err != nil {
			return nibss.Session{}, false, err
		}
		pos := open.netDebit()
		if need, ok := bank.AddMinor(pos, req.Amount); (!ok || need > rt.Cap) && !req.Closure {
			return nibss.Session{}, false, server.Refused(http.StatusUnprocessableEntity, nip.OverCap, "net_debit_cap_exceeded", fmt.Errorf("%s has reached its NIP net debit cap of %s. This transfer needs %s and %s is left. It clears at the next settlement session. Nothing was debited: %w", rt.FromName, bank.Naira(rt.Cap), bank.Naira(req.Amount), bank.Naira(max(rt.Cap-pos, 0)), errOverCap))
		}
		sid = a.newSessionID(rt, req, res.At)
		if sendKey == "" {
			sendKey = sid
		}
	}
	res.SessionID = sid
	b := rt.from.Bank()
	p, c, err := b.SendNIP(ctx, bank.NIPSend{Key: sendKey, From: req.From, Amount: req.Amount, SessionID: sid, To: bank.Counterparty{Name: name, AccountNumber: req.To, BankCode: rt.ToCode, BankName: rt.ToName}, At: res.At, NoCharges: req.Closure, Narration: req.Narration})
	if err != nil {
		return nibss.Session{}, false, server.PostingError(req.From, err)
	}
	res.Fee, res.VAT, res.StampDuty = c.Fee, c.VAT, c.StampDuty
	var terr error
	if res.TotalDebit, terr = server.TotalDebit(req.Amount, c); terr != nil {
		return nibss.Session{}, false, terr
	}
	x := nibss.Session{ID: sid, FromCode: rt.FromCode, ToCode: rt.ToCode, FromNUBAN: req.From, ToNUBAN: req.To, Amount: req.Amount, Narration: req.Narration, CreatedAt: res.At, Closure: req.Closure}
	if !p.Replayed {
		if held := a.takeTimeout(rt.ToBank); held != "" {
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

func (a *App) afterBegin(sw *nibss.Store, rt transferRoute, req server.TransferRequest, res server.TransferResult, sess nibss.Session, replayed bool, senderName string) (server.TransferResult, error) {
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
		if sess.Outcome == nip.OutcomeCredited {
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
		if err := sw.Resolve(context.Background(), sid, nibss.SessionOK, nip.OutcomeCredited, res.At); err != nil {
			return res, fmt.Errorf("resolve NIP session %s: %w", sid, err)
		}
		res.Status, res.ResponseCode = nip.Success, nip.OK
	}
	return a.finish(rt, req, res)
}

func (a *App) reverseFailed(sw *nibss.Store, rt transferRoute, req server.TransferRequest, res server.TransferResult, cause error) (server.TransferResult, error) {
	if rerr := a.fail(sw, rt.from, res.SessionID, nip.Unavailable, res.At); rerr != nil {
		return res, errors.Join(cause, rerr)
	}
	res.Status, res.ResponseCode = nip.Reversed, nip.Unavailable
	return a.finish(rt, req, res)
}

func (a *App) finish(rt transferRoute, req server.TransferRequest, res server.TransferResult) (server.TransferResult, error) {
	bal, err := rt.from.Bank().BalanceAt(context.Background(), req.From, res.At)
	res.BalanceAfter = bal
	return res, err
}

func (a *App) credit(rt transferRoute, req server.TransferRequest, sid, senderName string, at time.Time) error {
	if rt.to == nil {
		return fmt.Errorf("the books of %s are not open: %w", rt.ToName, nip.ErrUnavailable)
	}
	if a.bankDown(rt.ToBank, at) {
		return fmt.Errorf("%s is down: %w", rt.ToName, nip.ErrUnavailable)
	}
	_, err := rt.to.Bank().ReceiveNIP(context.Background(), sid, req.To, req.Amount, bank.Counterparty{Name: senderName, AccountNumber: req.From, BankCode: rt.FromCode, BankName: rt.FromName}, req.Narration, at)
	if err != nil {
		return fmt.Errorf("credit %s at %s: %w", req.To, rt.ToName, err)
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
	if err := sw.Resolve(ctx, sid, code, nip.OutcomeFailed, at); err != nil {
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
		if l.Inst.NIPCode == code {
			return l
		}
	}
	return nil
}

func (a *App) checkSessions(now time.Time) error {
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
	case q.Outcome == nip.OutcomeCredited:
		return sw.Resolve(ctx, x.ID, nibss.SessionOK, nip.OutcomeCredited, now)
	case q.Outcome == nip.OutcomeUnknown && q.TSQAttempts < a.cfg.TSQAttempts:
		return nil
	}
	return a.fail(sw, from, x.ID, nip.Unavailable, now)
}

func (a *App) QuerySession(id string) (nibss.Session, error) {
	x, err := a.Session(id)
	if err != nil || x.Status != nibss.SessionPending {
		return x, err
	}
	sw := a.nibssStore()
	err = a.checkSession(sw, x, a.clock.Now())
	if err != nil {
		return nibss.Session{}, fmt.Errorf("query session %s: %w", id, err)
	}
	return a.Session(id)
}

func (a *App) Session(id string) (nibss.Session, error) {
	sw := a.nibssStore()
	if sw == nil {
		return nibss.Session{}, errors.New("NIBSS is not available right now")
	}
	return sw.Session(context.Background(), strings.TrimSpace(id))
}
