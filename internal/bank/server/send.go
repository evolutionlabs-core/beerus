package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	sendPoll      = time.Second
	sendBatch     = 50
	sendGrace     = 15 * time.Second
	sessionOK     = "00"
	sessionOpen   = "pending"
	transfersPath = "/v1/transfers"
)

var ErrNoSend = errors.New("no such send")

type NIPSession struct {
	ID          string     `json:"session_id"`
	FromCode    string     `json:"from_code"`
	ToCode      string     `json:"to_code"`
	FromNUBAN   string     `json:"from_nuban"`
	ToNUBAN     string     `json:"to_nuban"`
	Amount      int64      `json:"amount_minor"`
	Narration   string     `json:"narration"`
	Status      string     `json:"status"`
	Outcome     string     `json:"outcome,omitempty"`
	TSQAttempts int        `json:"tsq_attempts"`
	NextTSQAt   *time.Time `json:"next_tsq_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Settlement  string     `json:"settlement_session,omitempty"`
	Closure     bool       `json:"closure,omitempty"`
}

type nibssTransfer struct {
	SessionID string `json:"session_id"`
	FromCode  string `json:"from_code"`
	ToCode    string `json:"to_code"`
	FromNUBAN string `json:"from_nuban"`
	ToNUBAN   string `json:"to_nuban"`
	FromName  string `json:"from_name"`
	Amount    int64  `json:"amount_minor"`
	Narration string `json:"narration"`
	Closure   bool   `json:"closure"`
}

type SenderConfig struct {
	Own      func() config.Bank
	Books    func() *bank.Bank
	Fees     *charges.Schedule
	Clock    *clock.Clock
	Log      *slog.Logger
	SetupKey uint64
	Phase    func(id string) string
	Lookup   func(id string) *config.Bank
	Refresh  func(ctx context.Context) error
	NIBSS    func() (NIBSSClient, error)
}

type Sender struct {
	SenderConfig
	sendLock sync.Mutex
}

func NewSender(c SenderConfig) *Sender {
	return &Sender{SenderConfig: c}
}

func (s *Sender) route(ctx context.Context, req TransferRequest) (Route, error) {
	if s.Refresh != nil {
		if err := s.Refresh(ctx); err != nil {
			return Route{}, err
		}
	}
	from, to := s.Lookup(req.FromBank), s.Lookup(req.ToBank)
	var fromPhase, toPhase string
	if from != nil {
		fromPhase = s.Phase(from.ID)
	}
	if to != nil {
		toPhase = s.Phase(to.ID)
	}
	rt, err := NewRoute(req, from, to, fromPhase, toPhase)
	if err != nil {
		return Route{}, err
	}
	if rt.FromBank != s.Own().ID || s.Books() == nil {
		return Route{}, Refused(http.StatusServiceUnavailable, nip.Unavailable, "bank_unavailable", fmt.Errorf("the books of %s are not open yet", rt.FromName))
	}
	return rt, nil
}

func (s *Sender) Quote(ctx context.Context, req TransferRequest) (TransferResult, error) {
	if err := CheckRequest(req); err != nil {
		return TransferResult{}, err
	}
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	rt, err := s.route(ctx, req)
	if err != nil {
		return TransferResult{}, err
	}
	return Quoter{Fees: s.Fees, Clock: s.Clock, Enquirer: quoteEnquirer{s}}.Quote(ctx, s.Books(), rt, req)
}

type quoteEnquirer struct {
	s *Sender
}

func (q quoteEnquirer) Enquire(ctx context.Context, rt Route, nuban string) (string, error) {
	return q.s.enquire(ctx, rt, nuban, "")
}

func (s *Sender) Transfer(ctx context.Context, req TransferRequest) (TransferResult, error) {
	if err := CheckRequest(req); err != nil {
		return TransferResult{}, err
	}
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	rt, err := s.route(ctx, req)
	if err != nil {
		return TransferResult{}, err
	}
	res := TransferResult{FromBank: rt.FromBank, From: req.From, ToBank: rt.ToBank, To: req.To, Amount: req.Amount, At: s.Clock.Now()}
	if rt.FromBank == rt.ToBank {
		return s.internal(ctx, rt, req, res)
	}
	return s.external(ctx, rt, req, res)
}

func (s *Sender) internal(ctx context.Context, rt Route, req TransferRequest, res TransferResult) (TransferResult, error) {
	if req.From == req.To {
		return res, Refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_request", ErrSameAccount)
	}
	b := s.Books()
	to, err := b.L.Account(ctx, bank.CustomerAccount(req.To))
	if err != nil {
		return res, AccountError("to", req.To, err)
	}
	key := req.Key
	if key == "" {
		key = web.NewID("trf")
	}
	if _, err := b.OwnTransfer(ctx, key, req.From, req.To, req.Amount, req.Narration, res.At); err != nil {
		return res, PostingError(req.From, err)
	}
	res.Kind, res.Status, res.ResponseCode, res.ToName, res.TotalDebit = "internal", nip.Success, nip.OK, to.Name, req.Amount
	res.BalanceAfter, err = b.BalanceAt(ctx, req.From, res.At)
	return res, err
}

func (s *Sender) enquire(ctx context.Context, rt Route, nuban, tail string) (string, error) {
	c, err := s.NIBSS()
	if err != nil {
		return "", err
	}
	out, err := c.Enquire(ctx, rt.ToCode, nuban)
	var away *UnavailableError
	if errors.As(err, &away) {
		return "", Refused(http.StatusServiceUnavailable, nip.Unavailable, "nibss_unavailable", errors.New("NIBSS is not available right now"+tail))
	}
	var refused *PeerError
	if errors.As(err, &refused) {
		return "", Refused(http.StatusBadGateway, nip.SystemError, "bad_gateway", errors.New("NIBSS refused the name enquiry"))
	}
	if err != nil {
		return "", err
	}
	return enquiryResult(rt.ToName, out)
}

func enquiryResult(name string, out nip.NameEnquiry) (string, error) {
	switch out.Code {
	case nip.OK:
		return out.AccountName, nil
	case nip.Unavailable:
		return "", Refused(http.StatusServiceUnavailable, nip.Unavailable, "bank_unavailable", fmt.Errorf("%s is not available right now, so nothing was debited", name))
	case nip.InvalidAccount:
		return "", Refused(http.StatusUnprocessableEntity, nip.InvalidAccount, "invalid_account", fmt.Errorf("that is not a valid account at %s, so nothing was debited", name))
	case nip.NoRecord:
		return "", Refused(http.StatusUnprocessableEntity, nip.NoRecord, "no_record", fmt.Errorf("%s has no such account, so nothing was debited", name))
	}
	return "", Refused(http.StatusUnprocessableEntity, nip.SystemError, "unknown_bank", errors.New(out.Message))
}

func (s *Sender) external(ctx context.Context, rt Route, req TransferRequest, res TransferResult) (TransferResult, error) {
	b := s.Books()
	sender, err := b.L.Account(ctx, bank.CustomerAccount(req.From))
	if err != nil {
		return res, AccountError("from", req.From, err)
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
	} else if name, err = s.enquire(ctx, rt, req.To, ", so nothing was debited"); err != nil {
		return res, err
	}
	res.Kind, res.ToName = "nip", name
	if _, err := TotalDebit(req.Amount, s.Fees.ForTransfer(req.Amount)); err != nil {
		return res, err
	}
	body := nibssTransfer{FromCode: rt.FromCode, ToCode: rt.ToCode, FromNUBAN: req.From, ToNUBAN: req.To, FromName: sender.Name, Amount: req.Amount, Narration: req.Narration, Closure: req.Closure}
	send := bank.NIPSend{Key: sendKey, From: req.From, Amount: req.Amount, SessionID: prior.SessionID, To: bank.Counterparty{Name: name, AccountNumber: req.To, BankCode: rt.ToCode, BankName: rt.ToName}, At: res.At, NoCharges: req.Closure, Narration: req.Narration}
	c, err := s.debit(ctx, &send, &body, req.Key)
	if err != nil {
		return res, PostingError(req.From, err)
	}
	res.SessionID = send.SessionID
	res.Fee, res.VAT, res.StampDuty = c.Fee, c.VAT, c.StampDuty
	if res.TotalDebit, err = TotalDebit(req.Amount, c); err != nil {
		return res, err
	}
	if err := s.submit(ctx, rt, send.SessionID, body, &res); err != nil {
		return res, err
	}
	res.BalanceAfter, err = b.BalanceAt(ctx, req.From, s.Clock.Now())
	return res, err
}

func (s *Sender) debit(ctx context.Context, send *bank.NIPSend, body *nibssTransfer, key string) (charges.Transfer, error) {
	s.sendLock.Lock()
	defer s.sendLock.Unlock()
	b := s.Books()
	if send.SessionID == "" {
		sid, err := s.sessionID(ctx, b, body.FromCode, key, send.At)
		if err != nil {
			return charges.Transfer{}, err
		}
		send.SessionID = sid
		if send.Key == "" {
			send.Key = sid
		}
	}
	body.SessionID = send.SessionID
	raw, err := json.Marshal(body)
	if err != nil {
		return charges.Transfer{}, fmt.Errorf("encode send %s: %w", send.SessionID, err)
	}
	send.Request, send.CheckAt = raw, s.Clock.Real().Add(sendGrace)
	_, c, err := b.SendNIP(ctx, *send)
	return c, err
}

func (s *Sender) sessionID(ctx context.Context, b *bank.Bank, code, key string, at time.Time) (string, error) {
	seq := 0
	if key != "" {
		sum := sha256.Sum256([]byte(code + "\x00" + key))
		seq = -int(binary.BigEndian.Uint32(sum[:4])) - 1
	} else {
		n, err := b.NextSendSeq(ctx)
		if err != nil {
			return "", err
		}
		seq = int(n)
	}
	return nip.SessionID(int64(s.SetupKey), code, at, seq), nil
}

func (s *Sender) submit(ctx context.Context, rt Route, sid string, body nibssTransfer, res *TransferResult) error {
	b := s.Books()
	var sess NIPSession
	c, err := s.NIBSS()
	if err == nil {
		_, err = c.Do(ctx, http.MethodPost, transfersPath, nil, body, &sess)
	}
	var refused *PeerError
	switch {
	case errors.As(err, &refused) && refused.Status < http.StatusInternalServerError:
		if rerr := s.undo(ctx, b, sid); rerr != nil {
			return errors.Join(err, rerr)
		}
		return Refused(refused.Status, nip.SystemError, refused.Code, errors.New(refused.Message+". Nothing was debited"))
	case err != nil:
		s.Log.Warn("NIBSS gave no answer to a transfer; it will be checked", "bank", rt.FromBank, "session", sid, "err", err)
		return s.later(ctx, b, sid, s.Clock.Real().Add(clock.Backoff(1)), res)
	}
	return s.conclude(ctx, b, sid, sess, res)
}

func (s *Sender) later(ctx context.Context, b *bank.Bank, sid string, next time.Time, res *TransferResult) error {
	if err := b.RescheduleSend(ctx, sid, next); err != nil {
		return err
	}
	at := next.In(clock.Lagos)
	res.Status, res.ResponseCode, res.NextCheckAt = nip.Pending, sessionOpen, &at
	return nil
}

func (s *Sender) conclude(ctx context.Context, b *bank.Bank, sid string, sess NIPSession, res *TransferResult) error {
	switch sess.Status {
	case sessionOK:
		res.Status, res.ResponseCode = nip.Success, nip.OK
		return b.FinishSend(ctx, sid)
	case sessionOpen:
		next := s.Clock.Real().Add(clock.Backoff(1))
		if sess.NextTSQAt != nil {
			next = *sess.NextTSQAt
		}
		return s.later(ctx, b, sid, next, res)
	}
	if err := s.undo(ctx, b, sid); err != nil {
		return err
	}
	res.Status, res.ResponseCode = nip.Reversed, sess.Status
	return nil
}

func (s *Sender) undo(ctx context.Context, b *bank.Bank, sid string) error {
	entry, err := b.SentNIP(ctx, sid)
	if err != nil {
		return err
	}
	if _, err := b.ReverseNIP(ctx, entry, sid, s.Clock.Now()); err != nil && !errors.Is(err, ledger.ErrAlreadyReversed) {
		return err
	}
	return b.FinishSend(ctx, sid)
}

func (s *Sender) Run(ctx context.Context) {
	tick := time.NewTicker(sendPoll)
	defer tick.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("sends not checked", "bank", s.Own().ID, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Sender) Tick(ctx context.Context) error {
	b := s.Books()
	if b == nil {
		return nil
	}
	due, err := b.DueSends(ctx, s.Clock.Real(), sendBatch)
	if err != nil {
		return err
	}
	for _, row := range due {
		if _, err := s.check(ctx, b, row); err != nil && ctx.Err() == nil {
			s.Log.Warn("send not checked; retrying", "bank", s.Own().ID, "session", row.SessionID, "err", err)
		}
	}
	return nil
}

func (s *Sender) check(ctx context.Context, b *bank.Bank, row bank.Send) (NIPSession, error) {
	sess, err := s.ask(ctx, row.SessionID, row.Request)
	if err != nil {
		if rerr := b.RescheduleSend(ctx, row.SessionID, s.Clock.Real().Add(clock.Backoff(row.Attempts+1))); rerr != nil {
			return NIPSession{}, errors.Join(err, rerr)
		}
		return NIPSession{}, err
	}
	res := TransferResult{}
	return sess, s.conclude(ctx, b, row.SessionID, sess, &res)
}

func (s *Sender) ask(ctx context.Context, sid string, request json.RawMessage) (NIPSession, error) {
	c, err := s.NIBSS()
	if err != nil {
		return NIPSession{}, err
	}
	var sess NIPSession
	_, err = c.Do(ctx, http.MethodPost, transfersPath+"/"+url.PathEscape(sid)+"/status-queries", nil, nil, &sess)
	var gone *PeerError
	if errors.As(err, &gone) && gone.Status == http.StatusNotFound && len(request) > 0 {
		_, err = c.Do(ctx, http.MethodPost, transfersPath, nil, request, &sess)
	}
	return sess, err
}

func (s *Sender) StatusQuery(ctx context.Context, sid string) (NIPSession, error) {
	b := s.Books()
	if b == nil {
		return NIPSession{}, fmt.Errorf("the books of %s are not open yet", s.Own().Name)
	}
	row, open, err := b.OpenSend(ctx, sid)
	if err != nil {
		return NIPSession{}, err
	}
	if open {
		return s.check(ctx, b, row)
	}
	sess, err := s.ask(ctx, sid, nil)
	var gone *PeerError
	if errors.As(err, &gone) && gone.Status == http.StatusNotFound {
		return NIPSession{}, fmt.Errorf("%s has no send %s: %w", s.Own().Name, sid, ErrNoSend)
	}
	return sess, err
}
