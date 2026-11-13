package bank

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	CustomerActive = "active"
	CustomerClosed = "closed"
)

const (
	ProductCurrent = "current"
	ProductSavings = "savings"
)

const (
	MaxPage        = 1000
	MaxNUBANSerial = 999999999
)

var (
	ErrUnknownCustomer = errors.New("bank: unknown customer")
	ErrCustomerClosed  = errors.New("bank: customer is closed")
	ErrOpenAccounts    = errors.New("bank: customer has open accounts")
	ErrBalanceNotZero  = errors.New("bank: account balance is not zero")
	ErrUsernameTaken   = errors.New("bank: username is taken")
	ErrBadLogin        = errors.New("bank: wrong username or password")
	ErrBadCursor       = errors.New("bank: invalid cursor")
	ErrNUBANsUsed      = errors.New("bank: no NUBANs left")
)

type Customer struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Phone     string     `json:"phone"`
	BVN       string     `json:"bvn"`
	Status    string     `json:"status"`
	Username  string     `json:"username,omitempty"`
	DOB       clock.Date `json:"dob"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("%w: password is empty", ledger.ErrInvalid)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

func digits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func ValidUsername(u string) bool {
	return u != "" && strings.Trim(u, "abcdefghijklmnopqrstuvwxyz0123456789._-") == ""
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ledger.ErrInvalid}, args...)...)
}

func validateCustomer(key, name, phone string, dob clock.Date, bvn, username, passwordHash string, at time.Time) error {
	switch {
	case key == "":
		return invalid("idempotency key is empty")
	case strings.TrimSpace(name) == "":
		return invalid("name is empty")
	case !digits(strings.TrimPrefix(phone, "+")):
		return invalid("phone %q must be digits with an optional leading +", phone)
	case at.IsZero():
		return invalid("time is empty")
	case dob.IsZero():
		return invalid("date of birth is empty")
	case dob.After(clock.DateOf(at)):
		return invalid("date of birth %s is after today, %s", dob, clock.DateOf(at))
	case !digits(bvn):
		return invalid("BVN %q must be digits", bvn)
	case username == "" && passwordHash != "":
		return invalid("a password needs a username")
	case username != "" && !ValidUsername(username):
		return invalid("username %q may only use a-z, 0-9, dot, dash and underscore", username)
	case username != "" && passwordHash == "":
		return invalid("username %q needs a password", username)
	}
	if passwordHash != "" {
		if _, err := bcrypt.Cost([]byte(passwordHash)); err != nil {
			return invalid("password hash is not a bcrypt hash")
		}
	}
	return nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const customerCols = `id, name, phone, bvn, status, coalesce(username, ''), dob, created_at, closed_at`

func scanCustomer(row pgx.Row, extra ...any) (Customer, error) {
	var c Customer
	var dob time.Time
	dest := append([]any{&c.ID, &c.Name, &c.Phone, &c.BVN, &c.Status, &c.Username, &dob, &c.CreatedAt, &c.ClosedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Customer{}, err
	}
	c.DOB = clock.Date{Y: dob.Year(), M: dob.Month(), D: dob.Day()}
	return c, nil
}

func uniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func (b *Bank) CreateCustomer(ctx context.Context, key, name, phone string, dob clock.Date, bvn, username, passwordHash string, at time.Time) (Customer, error) {
	if err := validateCustomer(key, name, phone, dob, bvn, username, passwordHash, at); err != nil {
		return Customer{}, err
	}
	id := world.ID("cus", b.P.ID, key)
	pool := b.L.Pool()
	c, err := scanCustomer(pool.QueryRow(ctx, `INSERT INTO customers (id, idempotency_key, name, phone, dob, bvn, username, password_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (idempotency_key) DO NOTHING RETURNING `+customerCols,
		id, key, name, phone, dob.Start(), bvn, nullable(username), nullable(passwordHash), at))
	switch {
	case err == nil:
		return c, nil
	case uniqueViolation(err, "customers_username_key"):
		return Customer{}, fmt.Errorf("%w: %s", ErrUsernameTaken, username)
	case !errors.Is(err, pgx.ErrNoRows):
		return Customer{}, fmt.Errorf("create customer: %w", err)
	}
	var hash string
	c, err = scanCustomer(pool.QueryRow(ctx, `SELECT `+customerCols+`, coalesce(password_hash, '') FROM customers WHERE idempotency_key = $1`, key), &hash)
	if err != nil {
		return Customer{}, fmt.Errorf("read customer: %w", err)
	}
	if c.Name != name || c.Phone != phone || c.DOB != dob || c.BVN != bvn || c.Username != username || (hash == "") != (passwordHash == "") {
		return Customer{}, fmt.Errorf("create customer %s: %w", c.ID, ledger.ErrIdempotency)
	}
	return c, nil
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (b *Bank) CloseCustomer(ctx context.Context, id string, at time.Time) error {
	if at.IsZero() {
		return invalid("time is empty")
	}
	tx, err := b.L.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin close customer: %w", err)
	}
	defer tx.Rollback(ctx)
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM customers WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrUnknownCustomer, id)
	}
	if err != nil {
		return fmt.Errorf("lock customer: %w", err)
	}
	if status == CustomerClosed {
		return nil
	}
	var open int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM gl_accounts WHERE customer AND owner = $1 AND status <> 'closed'`, id).Scan(&open); err != nil {
		return fmt.Errorf("count open accounts: %w", err)
	}
	if open > 0 {
		return fmt.Errorf("%w: customer %s still has %s; close them first", ErrOpenAccounts, id, plural(open, "open account", "open accounts"))
	}
	if _, err := tx.Exec(ctx, `UPDATE customers SET status = 'closed', closed_at = $2 WHERE id = $1`, id, at); err != nil {
		return fmt.Errorf("close customer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit close customer: %w", err)
	}
	return nil
}

const accountCols = `a.id, a.code, a.name, a.kind, a.currency, a.customer, coalesce(a.nuban, ''), coalesce(a.product, ''), coalesce(a.owner, ''), a.status, a.kyc_tier, a.overdraft_limit`

func scanAccount(row pgx.Row, extra ...any) (ledger.Account, error) {
	var a ledger.Account
	var kind string
	dest := append([]any{&a.ID, &a.Code, &a.Name, &kind, &a.Currency, &a.Customer, &a.NUBAN, &a.Product, &a.Owner, &a.Status, &a.KYCTier, &a.OverdraftLimit}, extra...)
	err := row.Scan(dest...)
	a.Kind = ledger.Kind(kind)
	return a, err
}

type opened struct {
	account  ledger.Account
	customer string
	product  string
}

func priorOpen(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key string) (opened, error) {
	var o opened
	var err error
	o.account, err = scanAccount(q.QueryRow(ctx, `SELECT `+accountCols+`, o.customer_id, o.product FROM account_opens o JOIN gl_accounts a ON a.id = o.account_id WHERE o.idempotency_key = $1`, key), &o.customer, &o.product)
	return o, err
}

func (o opened) matches(customer, product string) (ledger.Account, error) {
	if o.customer != customer || o.product != product {
		return ledger.Account{}, fmt.Errorf("open account %s: %w", o.account.NUBAN, ledger.ErrIdempotency)
	}
	return o.account, nil
}

func (b *Bank) OpenAccount(ctx context.Context, key, customer, product string, at time.Time) (ledger.Account, error) {
	switch {
	case key == "":
		return ledger.Account{}, invalid("idempotency key is empty")
	case product != ProductCurrent && product != ProductSavings:
		return ledger.Account{}, invalid("account type %q must be %s or %s", product, ProductCurrent, ProductSavings)
	case at.IsZero():
		return ledger.Account{}, invalid("time is empty")
	}
	if _, err := world.NormaliseBankCode(b.P.NUBANCode); err != nil {
		return ledger.Account{}, fmt.Errorf("open account: %w", err)
	}
	pool := b.L.Pool()
	o, err := priorOpen(ctx, pool, key)
	if err == nil {
		return o.matches(customer, product)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ledger.Account{}, fmt.Errorf("read account open: %w", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ledger.Account{}, fmt.Errorf("begin open account: %w", err)
	}
	defer tx.Rollback(ctx)
	var name, status string
	err = tx.QueryRow(ctx, `SELECT name, status FROM customers WHERE id = $1 FOR SHARE`, customer).Scan(&name, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Account{}, fmt.Errorf("%w: %s", ErrUnknownCustomer, customer)
	}
	if err != nil {
		return ledger.Account{}, fmt.Errorf("lock customer: %w", err)
	}
	if status == CustomerClosed {
		return ledger.Account{}, fmt.Errorf("%w: %s", ErrCustomerClosed, customer)
	}
	var id int64
	var nuban string
	for {
		var serial int64
		err := tx.QueryRow(ctx, `SELECT nextval('nuban_serial')`).Scan(&serial)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "2200H" {
			return ledger.Account{}, fmt.Errorf("%w: all %d serials are used", ErrNUBANsUsed, MaxNUBANSerial)
		}
		if err != nil {
			return ledger.Account{}, fmt.Errorf("next NUBAN serial: %w", err)
		}
		nuban = world.NUBAN(b.P.NUBANCode, uint64(serial))
		err = tx.QueryRow(ctx, `INSERT INTO gl_accounts (code, name, kind, currency, customer, nuban, product, owner)
			VALUES ($1, $2, 'liability', 'NGN', true, $3, $4, $5) ON CONFLICT DO NOTHING RETURNING id`,
			CustomerAccount(nuban), name, nuban, product, customer).Scan(&id)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ledger.Account{}, fmt.Errorf("insert account: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO balances (account_id) VALUES ($1)`, id); err != nil {
		return ledger.Account{}, fmt.Errorf("insert balance: %w", err)
	}
	ct, err := tx.Exec(ctx, `INSERT INTO account_opens (idempotency_key, account_id, customer_id, product, opened_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) DO NOTHING`, key, id, customer, product, at)
	if err != nil {
		return ledger.Account{}, fmt.Errorf("record account open: %w", err)
	}
	if ct.RowsAffected() == 0 {
		if err := tx.Rollback(ctx); err != nil {
			return ledger.Account{}, fmt.Errorf("roll back account open: %w", err)
		}
		o, err := priorOpen(ctx, pool, key)
		if err != nil {
			return ledger.Account{}, fmt.Errorf("read account open: %w", err)
		}
		return o.matches(customer, product)
	}
	a, err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountCols+` FROM gl_accounts a WHERE a.id = $1`, id))
	if err != nil {
		return ledger.Account{}, fmt.Errorf("read account: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ledger.Account{}, fmt.Errorf("commit open account: %w", err)
	}
	return a, nil
}

func naira(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	whole := strconv.FormatInt(minor/100, 10)
	var sb strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			sb.WriteByte(',')
		}
		sb.WriteRune(r)
	}
	return fmt.Sprintf("%s₦%s.%02d", sign, sb.String(), minor%100)
}

func (b *Bank) CloseAccount(ctx context.Context, nuban string, at time.Time) error {
	if at.IsZero() {
		return invalid("time is empty")
	}
	tx, err := b.L.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin close account: %w", err)
	}
	defer tx.Rollback(ctx)
	var id int64
	var status string
	err = tx.QueryRow(ctx, `SELECT id, status FROM gl_accounts WHERE nuban = $1 AND customer FOR UPDATE`, nuban).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("close account %s: %w", nuban, ledger.ErrUnknownAccount)
	}
	if err != nil {
		return fmt.Errorf("lock account: %w", err)
	}
	if status == "closed" {
		return nil
	}
	var balance int64
	if err := tx.QueryRow(ctx, `SELECT ledger FROM balances WHERE account_id = $1 FOR UPDATE`, id).Scan(&balance); err != nil {
		return fmt.Errorf("read balance: %w", err)
	}
	if balance != 0 {
		return fmt.Errorf("%w: account %s holds %s; move it out by transfer or cash first", ErrBalanceNotZero, nuban, naira(balance))
	}
	if _, err := tx.Exec(ctx, `UPDATE gl_accounts SET status = 'closed', closed_at = $2 WHERE id = $1`, id, at); err != nil {
		return fmt.Errorf("close account: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit close account: %w", err)
	}
	return nil
}

func checkLimit(limit int) error {
	if limit < 1 || limit > MaxPage {
		return invalid("limit must be from 1 to %d", MaxPage)
	}
	return nil
}

func (b *Bank) Customers(ctx context.Context, limit int, cursor string) ([]Customer, string, error) {
	if err := checkLimit(limit); err != nil {
		return nil, "", err
	}
	pool := b.L.Pool()
	after := int64(0)
	if cursor != "" {
		err := pool.QueryRow(ctx, `SELECT seq FROM customers WHERE id = $1`, cursor).Scan(&after)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", fmt.Errorf("%w: %q", ErrBadCursor, cursor)
		}
		if err != nil {
			return nil, "", fmt.Errorf("read cursor: %w", err)
		}
	}
	rows, err := pool.Query(ctx, `SELECT `+customerCols+` FROM customers WHERE seq > $1 ORDER BY seq LIMIT $2`, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list customers: %w", err)
	}
	defer rows.Close()
	out := []Customer{}
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, "", fmt.Errorf("scan customer: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list customers: %w", err)
	}
	if len(out) > limit {
		return out[:limit], out[limit-1].ID, nil
	}
	return out, "", nil
}

func (b *Bank) Customer(ctx context.Context, id string) (Customer, error) {
	c, err := scanCustomer(b.L.Pool().QueryRow(ctx, `SELECT `+customerCols+` FROM customers WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, fmt.Errorf("%w: %s", ErrUnknownCustomer, id)
	}
	if err != nil {
		return Customer{}, fmt.Errorf("read customer %s: %w", id, err)
	}
	return c, nil
}

func (b *Bank) CustomerByBVN(ctx context.Context, bvn string) (Customer, error) {
	c, err := scanCustomer(b.L.Pool().QueryRow(ctx, `SELECT `+customerCols+` FROM customers WHERE bvn = $1 AND status = $2 ORDER BY seq LIMIT 1`, bvn, CustomerActive))
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, fmt.Errorf("%w: no active customer with BVN %s", ErrUnknownCustomer, bvn)
	}
	if err != nil {
		return Customer{}, fmt.Errorf("find customer by BVN: %w", err)
	}
	return c, nil
}

func (b *Bank) CustomerAccounts(ctx context.Context, customer string) ([]ledger.Account, error) {
	if customer == "" {
		return nil, invalid("customer is empty")
	}
	out := []ledger.Account{}
	cursor := ""
	for {
		page, next, err := b.Accounts(ctx, customer, MaxPage, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

func (b *Bank) Accounts(ctx context.Context, customer string, limit int, cursor string) ([]ledger.Account, string, error) {
	if err := checkLimit(limit); err != nil {
		return nil, "", err
	}
	pool := b.L.Pool()
	if customer != "" {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1)`, customer).Scan(&exists); err != nil {
			return nil, "", fmt.Errorf("read customer: %w", err)
		}
		if !exists {
			return nil, "", fmt.Errorf("%w: %s", ErrUnknownCustomer, customer)
		}
	}
	after := int64(0)
	if cursor != "" {
		err := pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE nuban = $1 AND customer`, cursor).Scan(&after)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", fmt.Errorf("%w: %q", ErrBadCursor, cursor)
		}
		if err != nil {
			return nil, "", fmt.Errorf("read cursor: %w", err)
		}
	}
	rows, err := pool.Query(ctx, `SELECT `+accountCols+` FROM gl_accounts a WHERE a.customer AND a.id > $1 AND ($2 = '' OR a.owner = $2) ORDER BY a.id LIMIT $3`, after, customer, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()
	out := []ledger.Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, "", fmt.Errorf("scan account: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list accounts: %w", err)
	}
	if len(out) > limit {
		return out[:limit], out[limit-1].NUBAN, nil
	}
	return out, "", nil
}

func (b *Bank) Statement(ctx context.Context, nuban string, from, to clock.Date, limit int, cursor string) ([]ledger.Event, string, error) {
	if err := checkLimit(limit); err != nil {
		return nil, "", err
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return nil, "", invalid("from %s is after to %s", from, to)
	}
	after := int64(0)
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("%w: %q", ErrBadCursor, cursor)
		}
		after = n
	}
	pool := b.L.Pool()
	var id int64
	err := pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE nuban = $1 AND customer`, nuban).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", fmt.Errorf("statement %s: %w", nuban, ledger.ErrUnknownAccount)
	}
	if err != nil {
		return nil, "", fmt.Errorf("read account: %w", err)
	}
	var lo, hi *time.Time
	if !from.IsZero() {
		t := from.Start()
		lo = &t
	}
	if !to.IsZero() {
		t := to.End()
		hi = &t
	}
	rows, err := pool.Query(ctx, `SELECT seq, entry_id, amount, balance_after, posted_at, value_date, narration, coalesce(session_id, '')
		FROM account_events WHERE account_id = $1 AND seq > $2 AND ($3::timestamptz IS NULL OR posted_at >= $3) AND ($4::timestamptz IS NULL OR posted_at < $4)
		ORDER BY seq LIMIT $5`, id, after, lo, hi, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("read statement: %w", err)
	}
	defer rows.Close()
	out := []ledger.Event{}
	for rows.Next() {
		var ev ledger.Event
		var vd time.Time
		if err := rows.Scan(&ev.Seq, &ev.EntryID, &ev.Amount, &ev.BalanceAfter, &ev.PostedAt, &vd, &ev.Narration, &ev.SessionID); err != nil {
			return nil, "", fmt.Errorf("scan statement line: %w", err)
		}
		ev.ValueDate = clock.Date{Y: vd.Year(), M: vd.Month(), D: vd.Day()}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("read statement: %w", err)
	}
	if len(out) > limit {
		return out[:limit], strconv.FormatInt(out[limit-1].Seq, 10), nil
	}
	return out, "", nil
}

func (b *Bank) NameEnquiry(ctx context.Context, nuban string) (string, error) {
	if !world.ValidNUBAN(b.P.NUBANCode, nuban) {
		return "", fmt.Errorf("name enquiry %s: %w", nuban, nibss.ErrInvalidAccount)
	}
	a, err := b.L.AccountByNUBAN(ctx, nuban)
	if errors.Is(err, ledger.ErrUnknownAccount) || (err == nil && !a.Customer) {
		return "", fmt.Errorf("name enquiry %s: %w", nuban, nibss.ErrNoRecord)
	}
	if err != nil {
		return "", fmt.Errorf("name enquiry %s: %w", nuban, err)
	}
	if a.Status == "closed" || a.Status == "frozen" {
		return "", fmt.Errorf("name enquiry %s: account is %s: %w", nuban, a.Status, nibss.ErrInvalidAccount)
	}
	return a.Name, nil
}

var decoyHash = sync.OnceValues(func() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("read random: %w", err)
	}
	h, err := bcrypt.GenerateFromPassword(secret, bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash decoy: %w", err)
	}
	return h, nil
})

func (b *Bank) Authenticate(ctx context.Context, username, password string) (Customer, error) {
	var hash string
	c, err := scanCustomer(b.L.Pool().QueryRow(ctx, `SELECT `+customerCols+`, password_hash FROM customers WHERE username = $1`, username), &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		decoy, derr := decoyHash()
		if derr != nil {
			return Customer{}, derr
		}
		_ = bcrypt.CompareHashAndPassword(decoy, []byte(password))
		return Customer{}, ErrBadLogin
	}
	if err != nil {
		return Customer{}, fmt.Errorf("read login: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return Customer{}, ErrBadLogin
	}
	if c.Status != CustomerActive {
		return Customer{}, ErrBadLogin
	}
	return c, nil
}
