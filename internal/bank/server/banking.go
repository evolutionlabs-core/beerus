package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/money"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/inbound"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type Book struct {
	Name  string
	Phase string
	Bank  *bank.Bank
	Enrol func(ctx context.Context, name, phone string, dob clock.Date) (string, string, error)
	Send  *Sender
}

type Source interface {
	Book(id string) (Book, error)
	Empty() bool
	Refusing(id string) bool
	TakeTimeout(id string) (string, bool)
}

type Banking struct {
	Src   Source
	Clock *clock.Clock
	Log   *slog.Logger
	ID    string
}

var ErrBankNotFound = errors.New("no such bank")

const NoBanksMessage = "there are no banks yet. Add one first with beerus bank create --name NAME"

type UnavailableError struct {
	Msg string
	Err error
}

func (e *UnavailableError) Error() string {
	return e.Msg
}

func (e *UnavailableError) Unwrap() error {
	return e.Err
}

func (s *Banking) bankBook(id string) (Book, error) {
	bk, err := s.Src.Book(id)
	if err != nil {
		return Book{}, err
	}
	if bk.Bank == nil {
		return Book{}, fmt.Errorf("%w: %s", ErrBankNotFound, id)
	}
	return bk, nil
}

func (s *Banking) activeBook(id string) (Book, error) {
	bk, err := s.bankBook(id)
	if err != nil {
		return bk, err
	}
	if bk.Phase != config.PhaseActive {
		return bk, NotActiveError{bk.Name, bk.Phase}
	}
	return bk, nil
}

func noSuchBank(w http.ResponseWriter, r *http.Request, empty func() bool) {
	if empty() {
		web.WriteError(w, r, http.StatusNotFound, "not_found", NoBanksMessage)
		return
	}
	web.WriteError(w, r, http.StatusNotFound, "not_found", "No such bank.")
}

func (s *Banking) bankIDOf(r *http.Request) string {
	if s.ID != "" {
		return s.ID
	}
	return r.PathValue("id")
}

const (
	listDefault = 100
	holdLimit   = 30 * time.Second
)

type CustomerRequest struct {
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	DOB      string `json:"dob"`
	Username string `json:"username"`
	Password string `json:"password"`
	Key      string `json:"-"`
}

type AccountRequest struct {
	Customer string `json:"customer"`
	Type     string `json:"type"`
	Key      string `json:"-"`
}

type CashRequest struct {
	Account string `json:"account"`
	Amount  string `json:"amount"`
	Key     string `json:"-"`
}

type AccountJSON struct {
	NUBAN    string `json:"nuban"`
	Name     string `json:"name"`
	Customer string `json:"customer"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance_minor"`
}

type StatementLine struct {
	Seq          int64     `json:"seq"`
	Amount       int64     `json:"amount_minor"`
	BalanceAfter int64     `json:"balance_after_minor"`
	PostedAt     time.Time `json:"posted_at"`
	ValueDate    string    `json:"value_date"`
	Narration    string    `json:"narration"`
	SessionID    string    `json:"session_id,omitempty"`
}

type CashResult struct {
	Kind     string    `json:"kind"`
	Account  string    `json:"account"`
	Amount   int64     `json:"amount_minor"`
	Balance  int64     `json:"balance_minor"`
	Replayed bool      `json:"replayed"`
	PostedAt time.Time `json:"posted_at"`
}

func writeKey(key, op string) string {
	if key == "" {
		return web.NewID(op)
	}
	return "api:" + key
}

func (s *Banking) CreateCustomer(ctx context.Context, bankID string, req CustomerRequest) (bank.Customer, error) {
	bk, err := s.activeBook(bankID)
	if err != nil {
		return bank.Customer{}, err
	}
	dob, err := clock.ParseDate(req.DOB)
	if err != nil {
		return bank.Customer{}, fmt.Errorf("%w: dob %q must be a date like 1990-04-21", ledger.ErrInvalid, req.DOB)
	}
	if strings.TrimSpace(req.Name) == "" {
		return bank.Customer{}, fmt.Errorf("%w: name is empty", ledger.ErrInvalid)
	}
	hash := ""
	if req.Password != "" {
		if hash, err = bank.HashPassword(req.Password); err != nil {
			return bank.Customer{}, err
		}
	}
	bvn, phone, err := bk.Enrol(ctx, req.Name, req.Phone, dob)
	if err != nil {
		return bank.Customer{}, err
	}
	return bk.Bank.CreateCustomer(ctx, writeKey(req.Key, "cus"), strings.TrimSpace(req.Name), phone, dob, bvn, req.Username, hash, s.Clock.Now())
}

func (s *Banking) CloseCustomer(ctx context.Context, bankID, id string) error {
	bk, err := s.activeBook(bankID)
	if err != nil {
		return err
	}
	return bk.Bank.CloseCustomer(ctx, id, s.Clock.Now())
}

func (s *Banking) Customers(ctx context.Context, bankID string, limit int, cursor string) ([]bank.Customer, string, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return nil, "", err
	}
	return bk.Bank.Customers(ctx, limit, cursor)
}

func accountJSON(ctx context.Context, b *bank.Bank, acc ledger.Account) (AccountJSON, error) {
	bal, err := b.L.Balance(ctx, acc.Code)
	if err != nil {
		return AccountJSON{}, fmt.Errorf("read balance of %s: %w", acc.NUBAN, err)
	}
	return AccountJSON{NUBAN: acc.NUBAN, Name: acc.Name, Customer: acc.Owner, Type: acc.Product, Status: acc.Status, Currency: acc.Currency, Balance: bal}, nil
}

func (s *Banking) OpenAccount(ctx context.Context, bankID string, req AccountRequest) (AccountJSON, error) {
	bk, err := s.activeBook(bankID)
	if err != nil {
		return AccountJSON{}, err
	}
	b := bk.Bank
	acc, err := b.OpenAccount(ctx, writeKey(req.Key, "acc"), req.Customer, req.Type, s.Clock.Now())
	if err != nil {
		return AccountJSON{}, err
	}
	return accountJSON(ctx, b, acc)
}

func (s *Banking) CloseAccount(ctx context.Context, bankID, nuban string) error {
	bk, err := s.activeBook(bankID)
	if err != nil {
		return err
	}
	return bk.Bank.CloseAccount(ctx, nuban, s.Clock.Now())
}

func (s *Banking) Accounts(ctx context.Context, bankID, customer string, limit int, cursor string) ([]AccountJSON, string, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return nil, "", err
	}
	b := bk.Bank
	accts, next, err := b.Accounts(ctx, customer, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	data := make([]AccountJSON, 0, len(accts))
	for _, acc := range accts {
		j, err := accountJSON(ctx, b, acc)
		if err != nil {
			return nil, "", err
		}
		data = append(data, j)
	}
	return data, next, nil
}

func (s *Banking) Statement(ctx context.Context, bankID, nuban string, from, to clock.Date, limit int, cursor string) ([]StatementLine, string, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return nil, "", err
	}
	evs, next, err := bk.Bank.Statement(ctx, nuban, from, to, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	data := make([]StatementLine, 0, len(evs))
	for _, ev := range evs {
		data = append(data, StatementLine{Seq: ev.Seq, Amount: ev.Amount, BalanceAfter: ev.BalanceAfter, PostedAt: ev.PostedAt, ValueDate: ev.ValueDate.String(), Narration: ev.Narration, SessionID: ev.SessionID})
	}
	return data, next, nil
}

func (s *Banking) Cash(ctx context.Context, bankID, kind string, req CashRequest) (CashResult, error) {
	bk, err := s.activeBook(bankID)
	if err != nil {
		return CashResult{}, err
	}
	amount, err := money.ParseNaira(req.Amount)
	if err != nil {
		return CashResult{}, fmt.Errorf("%w: %w", ledger.ErrInvalid, err)
	}
	b := bk.Bank
	now := s.Clock.Now()
	key := writeKey(req.Key, "csh")
	var p ledger.Posted
	if kind == "deposit" {
		p, err = b.CashDeposit(ctx, key, req.Account, amount, now)
	} else {
		p, err = b.CashWithdrawal(ctx, key, req.Account, amount, now)
	}
	if err != nil {
		return CashResult{}, b.CashRefusal(ctx, req.Account, amount, err)
	}
	bal, ok := p.Balances[bank.CustomerAccount(req.Account)]
	if !ok {
		return CashResult{}, fmt.Errorf("read balance of %s: the posted entry has no balance for it", req.Account)
	}
	return CashResult{Kind: kind, Account: req.Account, Amount: amount, Balance: bal, Replayed: p.Replayed, PostedAt: p.PostedAt.In(clock.Lagos)}, nil
}

func ListParams(r *http.Request) (int, string, error) {
	limit := listDefault
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > bank.MaxPage {
			return 0, "", fmt.Errorf("%w: limit must be a number from 1 to %d", ledger.ErrInvalid, bank.MaxPage)
		}
		limit = n
	}
	return limit, r.URL.Query().Get("cursor"), nil
}

func clean(err error) string {
	msg := err.Error()
	for _, p := range []string{ledger.ErrInvalid.Error() + ": ", "bank: ", "ledger: "} {
		if i := strings.LastIndex(msg, p); i >= 0 {
			msg = msg[i+len(p):]
		}
	}
	return web.Sentence(msg)
}

func (s *Banking) Error(w http.ResponseWriter, r *http.Request, err error) {
	WriteBankingError(w, r, s.Log, s.Src.Empty, err)
}

func WriteBankingError(w http.ResponseWriter, r *http.Request, log *slog.Logger, noBanks func() bool, err error) {
	var te *TransferError
	var fe *bank.InsufficientFundsError
	var pe *bank.PayoutTooLargeError
	var de *bank.DailyLimitError
	var ue *UnavailableError
	switch {
	case errors.Is(err, context.Canceled):
	case errors.As(err, &te):
		web.WriteError(w, r, te.Status, te.ErrCode, web.Sentence(te.Error()))
	case errors.Is(err, ErrBankNotFound):
		noSuchBank(w, r, noBanks)
	case errors.Is(err, errBankNotActive):
		web.WriteError(w, r, http.StatusConflict, "bank_not_active", web.Sentence(err.Error()))
	case errors.Is(err, errBankNotClosing):
		web.WriteError(w, r, http.StatusConflict, "bank_not_closing", web.Sentence(err.Error()))
	case errors.Is(err, bank.ErrBadLogin):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_login", "The username or password is wrong.")
	case errors.Is(err, bank.ErrUnknownLine):
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such entry.")
	case errors.Is(err, bank.ErrUnknownCustomer):
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such customer.")
	case errors.Is(err, ledger.ErrUnknownAccount):
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such account.")
	case errors.Is(err, bank.ErrBadCursor):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use next_cursor from the last page.")
	case errors.Is(err, bank.ErrUsernameTaken):
		web.WriteError(w, r, http.StatusConflict, "username_taken", clean(err))
	case errors.Is(err, bank.ErrCustomerClosed):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "customer_closed", clean(err))
	case errors.Is(err, bank.ErrOpenAccounts):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "open_accounts", clean(err))
	case errors.Is(err, bank.ErrBalanceNotZero):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "balance_not_zero", clean(err))
	case errors.Is(err, bank.ErrNUBANsUsed):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "nubans_used", clean(err))
	case errors.Is(err, ledger.ErrIdempotency):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", "That Idempotency-Key was used for a different request.")
	case errors.Is(err, ledger.ErrInvalid):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", clean(err))
	case errors.As(err, &ue):
		log.Warn("banking unavailable", "path", r.URL.Path, "err", ue.Err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", ue.Msg)
	case errors.As(err, &pe):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "insufficient_funds", pe.Message())
	case errors.As(err, &fe):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "insufficient_funds", fe.Message())
	case errors.As(err, &de):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "daily_limit", de.Message())
	case errors.Is(err, ledger.ErrInsufficientFunds):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "insufficient_funds", "The account does not have enough money.")
	case errors.Is(err, ledger.ErrDailyLimit):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "daily_limit", "The account has reached its daily limit.")
	case errors.Is(err, ledger.ErrAccountClosed), errors.Is(err, ledger.ErrAccountFrozen), errors.Is(err, ledger.ErrPostNoDebit):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "account_restricted", clean(err))
	default:
		log.Error("banking", "path", r.URL.Path, "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", web.SomethingWentWrong)
	}
}

func (s *Banking) CustomersByBVN(ctx context.Context, bankID, bvn string, limit int, cursor string) ([]bank.Customer, string, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return nil, "", err
	}
	return bk.Bank.CustomersByBVN(ctx, bvn, limit, cursor)
}

func (s *Banking) HandleCustomers(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := ListParams(r)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	bvn := r.URL.Query().Get("bvn")
	if r.URL.Query().Has("bvn") && !bank.ValidBVN(bvn) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("bvn must be %d digits.", bank.BVNLength))
		return
	}
	var data []bank.Customer
	var next string
	if bvn != "" {
		data, next, err = s.CustomersByBVN(r.Context(), s.bankIDOf(r), bvn, limit, cursor)
	} else {
		data, next, err = s.Customers(r.Context(), s.bankIDOf(r), limit, cursor)
	}
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
}

func (s *Banking) HandleCreateCustomer(w http.ResponseWriter, r *http.Request) {
	key, ok := web.IdempotencyKey(w, r)
	if !ok {
		return
	}
	var req CustomerRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}, with optional username and password.`)
		return
	}
	req.Key = key
	c, err := s.CreateCustomer(r.Context(), s.bankIDOf(r), req)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, c)
}

func (s *Banking) HandleCloseCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("customer_id")
	if err := s.CloseCustomer(r.Context(), s.bankIDOf(r), id); err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"customer": id, "status": bank.CustomerClosed})
}

func (s *Banking) HandleAccounts(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := ListParams(r)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	data, next, err := s.Accounts(r.Context(), s.bankIDOf(r), r.URL.Query().Get("customer"), limit, cursor)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
}

func (s *Banking) HandleOpenAccount(w http.ResponseWriter, r *http.Request) {
	key, ok := web.IdempotencyKey(w, r)
	if !ok {
		return
	}
	var req AccountRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"customer":"cus_...","type":"current"}.`)
		return
	}
	req.Key = key
	acc, err := s.OpenAccount(r.Context(), s.bankIDOf(r), req)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, acc)
}

func (s *Banking) HandleCloseAccount(w http.ResponseWriter, r *http.Request) {
	nuban := r.PathValue("nuban")
	if err := s.CloseAccount(r.Context(), s.bankIDOf(r), nuban); err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"nuban": nuban, "status": "closed"})
}

func (s *Banking) HandleStatement(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := ListParams(r)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	var from, to clock.Date
	for _, d := range []struct {
		name string
		into *clock.Date
	}{{"from", &from}, {"to", &to}} {
		if v := r.URL.Query().Get(d.name); v != "" {
			if *d.into, err = clock.ParseDate(v); err != nil {
				s.Error(w, r, fmt.Errorf("%w: %s %q must be a date like 2026-03-01", ledger.ErrInvalid, d.name, v))
				return
			}
		}
	}
	data, next, err := s.Statement(r.Context(), s.bankIDOf(r), r.PathValue("nuban"), from, to, limit, cursor)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
}

func (s *Banking) HandleCash(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := web.IdempotencyKey(w, r)
		if !ok {
			return
		}
		var req CashRequest
		if err := web.DecodeBody(r, &req); err != nil {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"account":"0123456789","amount":"5000.00"}.`)
			return
		}
		req.Key = key
		res, err := s.Cash(r.Context(), s.bankIDOf(r), kind, req)
		if err != nil {
			s.Error(w, r, err)
			return
		}
		web.WriteJSON(w, http.StatusCreated, res)
	}
}

func (s *Banking) NIP(id string) http.Handler {
	var mu sync.Mutex
	var cached *bank.Bank
	var handler http.Handler
	nibss := inbound.Caller{CommonName: config.NIBSSCN, Role: config.NIBSSRole}
	serve := func(b *bank.Bank) http.Handler {
		mu.Lock()
		defer mu.Unlock()
		if cached != b {
			cached = b
			handler = inbound.New(b, s.Clock, nibss, s.Log)
		}
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bk, err := s.Src.Book(id)
		if err == nil && bk.Phase == config.PhaseClosed {
			inbound.WriteClosed(w, bk.Name)
			return
		}
		if err != nil || bk.Bank == nil || s.Src.Refusing(id) {
			w.Header().Set("Retry-After", "30")
			inbound.WriteUnavailable(w)
			return
		}
		h := serve(bk.Bank)
		if holdsCredit(r) && nibss.Allows(r) {
			if outcome, held := s.Src.TakeTimeout(id); held {
				s.hold(w, r, h, outcome)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func holdsCredit(r *http.Request) bool {
	return (r.Method == http.MethodPost && r.URL.Path == "/v1/nip/credits") || (r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/nip/credits/"))
}

type discard struct {
	header http.Header
}

func (d discard) Header() http.Header {
	return d.header
}

func (d discard) Write(b []byte) (int, error) {
	return len(b), nil
}

func (d discard) WriteHeader(int) {}

func (s *Banking) hold(w http.ResponseWriter, r *http.Request, h http.Handler, outcome string) {
	if outcome == nip.OutcomeCredited && r.Method == http.MethodPost {
		h.ServeHTTP(discard{header: http.Header{}}, r)
	}
	timer := time.NewTimer(holdLimit)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
	case <-timer.C:
		w.Header().Set("Retry-After", "30")
		inbound.WriteUnavailable(w)
	}
}
