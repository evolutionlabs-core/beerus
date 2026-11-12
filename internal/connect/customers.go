package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
)

const listDefault = 100

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
	At       time.Time `json:"business_time"`
}

var errBankNotFound = errors.New("no such bank")

func (a *App) bankBook(id string) (book, error) {
	bk, ok := a.bookOf(id)
	if !ok {
		return book{}, fmt.Errorf("%w: %s", errBankNotFound, id)
	}
	return bk, nil
}

func (a *App) activeBook(id string) (book, error) {
	bk, err := a.bankBook(id)
	if err != nil {
		return bk, err
	}
	if p := a.bankPhase(id); p != PhaseActive {
		return bk, notActiveError{bk.bank.Name, p}
	}
	return bk, nil
}

func writeKey(key, op string) string {
	if key == "" {
		return newID(op)
	}
	return "api:" + key
}

func (a *App) CreateCustomer(bankID string, req CustomerRequest) (bank.Customer, error) {
	a.gate.RLock()
	defer a.gate.RUnlock()
	bk, err := a.activeBook(bankID)
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
	sw := a.nibssStore()
	if sw == nil {
		return bank.Customer{}, errors.New("NIBSS is not open yet, so no BVN can be enrolled; try again soon")
	}
	phone, err := sw.NormalisePhone(req.Phone)
	if errors.Is(err, nibss.ErrBadDetails) {
		return bank.Customer{}, fmt.Errorf("%w: %s", ledger.ErrInvalid, strings.TrimPrefix(err.Error(), nibss.ErrBadDetails.Error()+": "))
	}
	if err != nil {
		return bank.Customer{}, fmt.Errorf("normalise phone: %w", err)
	}
	hash := ""
	if req.Password != "" {
		if hash, err = bank.HashPassword(req.Password); err != nil {
			return bank.Customer{}, err
		}
	}
	ctx := context.Background()
	bvn, err := sw.EnrolBVN(ctx, req.Name, phone, dob)
	if errors.Is(err, nibss.ErrBadDetails) {
		return bank.Customer{}, fmt.Errorf("%w: %s", ledger.ErrInvalid, strings.TrimPrefix(err.Error(), nibss.ErrBadDetails.Error()+": "))
	}
	if err != nil {
		return bank.Customer{}, fmt.Errorf("enrol a BVN at NIBSS: %w", err)
	}
	return bk.ls.Bank().CreateCustomer(ctx, writeKey(req.Key, "cus"), strings.TrimSpace(req.Name), phone, dob, bvn, req.Username, hash, a.clock.Now())
}

func (a *App) CloseCustomer(bankID, id string) error {
	a.gate.RLock()
	defer a.gate.RUnlock()
	bk, err := a.activeBook(bankID)
	if err != nil {
		return err
	}
	return bk.ls.Bank().CloseCustomer(context.Background(), id, a.clock.Now())
}

func accountJSON(ctx context.Context, b *bank.Bank, acc ledger.Account) (AccountJSON, error) {
	bal, err := b.L.Balance(ctx, acc.Code)
	if err != nil {
		return AccountJSON{}, fmt.Errorf("read balance of %s: %w", acc.NUBAN, err)
	}
	return AccountJSON{NUBAN: acc.NUBAN, Name: acc.Name, Customer: acc.Owner, Type: acc.Product, Status: acc.Status, Currency: acc.Currency, Balance: bal}, nil
}

func (a *App) OpenAccount(bankID string, req AccountRequest) (AccountJSON, error) {
	a.gate.RLock()
	defer a.gate.RUnlock()
	bk, err := a.activeBook(bankID)
	if err != nil {
		return AccountJSON{}, err
	}
	ctx := context.Background()
	b := bk.ls.Bank()
	acc, err := b.OpenAccount(ctx, writeKey(req.Key, "acc"), req.Customer, req.Type, a.clock.Now())
	if err != nil {
		return AccountJSON{}, err
	}
	return accountJSON(ctx, b, acc)
}

func (a *App) CloseAccount(bankID, nuban string) error {
	a.gate.RLock()
	defer a.gate.RUnlock()
	bk, err := a.activeBook(bankID)
	if err != nil {
		return err
	}
	return bk.ls.Bank().CloseAccount(context.Background(), nuban, a.clock.Now())
}

func (a *App) Cash(bankID, kind string, req CashRequest) (CashResult, error) {
	res, err := a.postCash(bankID, kind, req)
	if err != nil {
		return CashResult{}, err
	}
	a.SyncAll()
	return res, nil
}

func (a *App) postCash(bankID, kind string, req CashRequest) (CashResult, error) {
	a.gate.RLock()
	defer a.gate.RUnlock()
	bk, err := a.activeBook(bankID)
	if err != nil {
		return CashResult{}, err
	}
	amount, err := parseNaira(req.Amount)
	if err != nil {
		return CashResult{}, fmt.Errorf("%w: %w", ledger.ErrInvalid, err)
	}
	ctx := context.Background()
	b := bk.ls.Bank()
	now := a.clock.Now()
	key := writeKey(req.Key, "csh")
	var p ledger.Posted
	if kind == "deposit" {
		p, err = b.CashDeposit(ctx, key, req.Account, amount, now)
	} else {
		p, err = b.CashWithdrawal(ctx, key, req.Account, amount, now)
	}
	if err != nil {
		return CashResult{}, err
	}
	bal, ok := p.Balances[bank.CustomerAccount(req.Account)]
	if !ok {
		return CashResult{}, fmt.Errorf("read balance of %s: the posted entry has no balance for it", req.Account)
	}
	return CashResult{Kind: kind, Account: req.Account, Amount: amount, Balance: bal, Replayed: p.Replayed, At: p.PostedAt.In(clock.Lagos)}, nil
}

func listParams(r *http.Request) (int, string, error) {
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

func listPage(data any, next string) map[string]any {
	out := map[string]any{"data": data}
	if next != "" {
		out["next_cursor"] = next
	}
	return out
}

func clean(err error) string {
	msg := err.Error()
	for _, p := range []string{ledger.ErrInvalid.Error() + ": ", "bank: ", "ledger: "} {
		if i := strings.LastIndex(msg, p); i >= 0 {
			msg = msg[i+len(p):]
		}
	}
	return sentence(msg)
}

func (a *App) bankingError(w http.ResponseWriter, r *http.Request, err error) {
	var te *transferError
	switch {
	case errors.As(err, &te):
		writeError(w, r, te.status, te.errCode, sentence(te.Error()))
	case errors.Is(err, errBankNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "No such bank.")
	case errors.Is(err, errBankNotActive):
		writeError(w, r, http.StatusConflict, "bank_not_active", sentence(err.Error()))
	case errors.Is(err, bank.ErrUnknownCustomer):
		writeError(w, r, http.StatusNotFound, "not_found", "No such customer.")
	case errors.Is(err, ledger.ErrUnknownAccount):
		writeError(w, r, http.StatusNotFound, "not_found", "No such account.")
	case errors.Is(err, bank.ErrBadCursor):
		writeError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use next_cursor from the last page.")
	case errors.Is(err, bank.ErrUsernameTaken):
		writeError(w, r, http.StatusConflict, "username_taken", clean(err))
	case errors.Is(err, bank.ErrCustomerClosed):
		writeError(w, r, http.StatusUnprocessableEntity, "customer_closed", clean(err))
	case errors.Is(err, bank.ErrOpenAccounts):
		writeError(w, r, http.StatusUnprocessableEntity, "open_accounts", clean(err))
	case errors.Is(err, bank.ErrBalanceNotZero):
		writeError(w, r, http.StatusUnprocessableEntity, "balance_not_zero", clean(err))
	case errors.Is(err, bank.ErrNUBANsUsed):
		writeError(w, r, http.StatusUnprocessableEntity, "nubans_used", clean(err))
	case errors.Is(err, ledger.ErrIdempotency):
		writeError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", "That Idempotency-Key was used for a different request.")
	case errors.Is(err, ledger.ErrInvalid):
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_request", clean(err))
	case errors.Is(err, ledger.ErrInsufficientFunds):
		writeError(w, r, http.StatusUnprocessableEntity, "insufficient_funds", "The account does not have enough money.")
	case errors.Is(err, ledger.ErrDailyLimit):
		writeError(w, r, http.StatusUnprocessableEntity, "daily_limit", "The account has reached its daily limit.")
	case errors.Is(err, ledger.ErrAccountClosed), errors.Is(err, ledger.ErrAccountFrozen), errors.Is(err, ledger.ErrPostNoDebit):
		writeError(w, r, http.StatusUnprocessableEntity, "account_restricted", clean(err))
	default:
		a.log.Error("banking", "path", r.URL.Path, "err", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", somethingWentWrong)
	}
}

func idemKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 255 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The Idempotency-Key header must be 1 to 255 characters.")
		return "", false
	}
	return key, true
}

func (a *App) ctlCustomers(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listParams(r)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	bk, err := a.bankBook(r.PathValue("id"))
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	data, next, err := bk.ls.Bank().Customers(r.Context(), limit, cursor)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listPage(data, next))
}

func (a *App) ctlCreateCustomer(w http.ResponseWriter, r *http.Request) {
	key, ok := idemKey(w, r)
	if !ok {
		return
	}
	var req CustomerRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}, with optional username and password.`)
		return
	}
	req.Key = key
	c, err := a.CreateCustomer(r.PathValue("id"), req)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *App) ctlCloseCustomer(w http.ResponseWriter, r *http.Request) {
	if err := a.CloseCustomer(r.PathValue("id"), r.PathValue("customer")); err != nil {
		a.bankingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"customer": r.PathValue("customer"), "status": bank.CustomerClosed})
}

func (a *App) ctlAccounts(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listParams(r)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	bk, err := a.bankBook(r.PathValue("id"))
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	ctx := r.Context()
	b := bk.ls.Bank()
	accts, next, err := b.Accounts(ctx, r.URL.Query().Get("customer"), limit, cursor)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	data := []AccountJSON{}
	for _, acc := range accts {
		j, err := accountJSON(ctx, b, acc)
		if err != nil {
			a.bankingError(w, r, err)
			return
		}
		data = append(data, j)
	}
	writeJSON(w, http.StatusOK, listPage(data, next))
}

func (a *App) ctlOpenAccount(w http.ResponseWriter, r *http.Request) {
	key, ok := idemKey(w, r)
	if !ok {
		return
	}
	var req AccountRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"customer":"cus_...","type":"current"}.`)
		return
	}
	req.Key = key
	acc, err := a.OpenAccount(r.PathValue("id"), req)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, acc)
}

func (a *App) ctlCloseAccount(w http.ResponseWriter, r *http.Request) {
	if err := a.CloseAccount(r.PathValue("id"), r.PathValue("nuban")); err != nil {
		a.bankingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"nuban": r.PathValue("nuban"), "status": "closed"})
}

func (a *App) ctlStatement(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listParams(r)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	var from, to clock.Date
	for _, d := range []struct {
		name string
		into *clock.Date
	}{{"from", &from}, {"to", &to}} {
		if s := r.URL.Query().Get(d.name); s != "" {
			if *d.into, err = clock.ParseDate(s); err != nil {
				a.bankingError(w, r, fmt.Errorf("%w: %s %q must be a date like 2026-03-01", ledger.ErrInvalid, d.name, s))
				return
			}
		}
	}
	bk, err := a.bankBook(r.PathValue("id"))
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	evs, next, err := bk.ls.Bank().Statement(r.Context(), r.PathValue("nuban"), from, to, limit, cursor)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	data := []StatementLine{}
	for _, ev := range evs {
		data = append(data, StatementLine{Seq: ev.Seq, Amount: ev.Amount, BalanceAfter: ev.BalanceAfter, PostedAt: ev.PostedAt, ValueDate: ev.ValueDate.String(), Narration: ev.Narration, SessionID: ev.SessionID})
	}
	writeJSON(w, http.StatusOK, listPage(data, next))
}

func (a *App) ctlCash(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := idemKey(w, r)
		if !ok {
			return
		}
		var req CashRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"account":"0123456789","amount":"5000.00"}.`)
			return
		}
		req.Key = key
		res, err := a.Cash(r.PathValue("id"), kind, req)
		if err != nil {
			a.bankingError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	}
}
