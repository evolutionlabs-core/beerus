package connect

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const roomCookie = "beerus_room"

func (a *App) RoomURL() string {
	return a.cfg.ControlURL() + "/ui/login?token=" + url.QueryEscape(a.ctlToken)
}

func (a *App) roomOnly(next http.Handler) http.Handler {
	port := strconv.Itoa(a.cfg.Connect.ControlPort)
	hosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true, "[::1]:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !net.ParseIP(host).IsLoopback() || !hosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/ui/login" {
			next.ServeHTTP(w, r)
			return
		}
		ck, err := r.Cookie(roomCookie)
		if err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(hashSecret(a.ctlToken))) != 1 {
			http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) roomHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", assetHandler())
	mux.HandleFunc("GET /ui/login", a.roomLogin)
	mux.HandleFunc("POST /ui/login", a.roomLogin)
	mux.HandleFunc("GET /ui", a.roomOverview)
	mux.HandleFunc("GET /ui/{$}", a.roomOverview)
	mux.HandleFunc("GET /ui/banks", a.roomBanks)
	mux.HandleFunc("GET /ui/banks/new", a.roomNewBank)
	mux.HandleFunc("POST /ui/banks", a.roomCreateBank)
	mux.HandleFunc("GET /ui/banks/{id}", a.roomBank)
	mux.HandleFunc("POST /ui/banks/{id}/suspend", func(w http.ResponseWriter, r *http.Request) { a.roomSuspend(w, r, true) })
	mux.HandleFunc("POST /ui/banks/{id}/resume", func(w http.ResponseWriter, r *http.Request) { a.roomSuspend(w, r, false) })
	mux.HandleFunc("GET /ui/banks/{id}/close", a.roomCloseBank)
	mux.HandleFunc("POST /ui/banks/{id}/close", a.roomCloseBank)
	mux.HandleFunc("GET /ui/banks/{id}/purge", a.roomPurgeBank)
	mux.HandleFunc("POST /ui/banks/{id}/purge", a.roomPurgeBank)
	mux.HandleFunc("GET /ui/accounts/{key}", a.roomAccount)
	mux.HandleFunc("GET /ui/accounts/{key}/lines/{line}", a.roomLine)
	mux.HandleFunc("GET /ui/transfer", a.roomTransferForm)
	mux.HandleFunc("POST /ui/transfer", a.roomTransfer)
	mux.HandleFunc("GET /ui/connections", a.roomConnections)
	mux.HandleFunc("POST /ui/connections/{id}/revoke", a.roomRevoke)
	mux.HandleFunc("GET /ui/webhooks", a.roomWebhooks)
	mux.HandleFunc("POST /ui/webhooks/{id}/replay", a.roomReplay)
	return a.roomOnly(mux)
}

type roomPage map[string]any

func (a *App) roomRender(w http.ResponseWriter, r *http.Request, name string, status int, nav string, data roomPage) {
	data["Now"] = a.clock.Now()
	data["Nav"] = nav
	data["Banks"] = a.openInstitutions()
	setPageHeaders(w)
	w.WriteHeader(status)
	if err := roomPages.ExecuteTemplate(w, name, data); err != nil {
		a.log.Error("render", "page", name, "err", err)
	}
}

func (a *App) roomLogin(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		tok = strings.TrimSpace(r.PostForm.Get("token"))
	}
	if tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(a.ctlToken)) == 1 {
		http.SetCookie(w, &http.Cookie{Name: roomCookie, Value: hashSecret(a.ctlToken), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
		http.Redirect(w, r, "/ui", http.StatusSeeOther)
		return
	}
	data := roomPage{"Title": "Sign in"}
	if tok != "" {
		data["Error"] = "That token is not valid."
	}
	setPageHeaders(w)
	if err := roomPages.ExecuteTemplate(w, "room_login", data); err != nil {
		a.log.Error("render", "page", "room_login", "err", err)
	}
}

type book struct {
	bank *config.Bank
	ls   *ledgerSource
}

func (a *App) books() []book {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []book{}
	for _, b := range a.cfg.Banks {
		if ls := a.ledgerFor(b.ID); ls != nil {
			out = append(out, book{b, ls})
		}
	}
	return out
}

func (a *App) bookOf(id string) (book, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.cfg.Bank(id)
	if b == nil {
		return book{}, false
	}
	ls := a.ledgerFor(id)
	return book{b, ls}, ls != nil
}

type acctView struct {
	ledger.Account
	Key       string
	Type      string
	Bank      *config.Bank
	Holder    string
	Ledger    int64
	Available int64
}

func (bk book) acctView(ctx context.Context, acc ledger.Account) (acctView, error) {
	bal, err := bk.ls.Bank().L.Balance(ctx, acc.Code)
	if err != nil {
		return acctView{}, fmt.Errorf("read balance of %s: %w", acc.NUBAN, err)
	}
	return acctView{Account: acc, Key: acctKey(bk.bank.ID, acc.NUBAN), Type: acc.Product, Bank: bk.bank, Holder: acc.Name, Ledger: bal, Available: bal}, nil
}

type lineView struct {
	bank.FeedLine
	Bank        *config.Bank
	Description string
	Href        string
}

func newLineView(b *config.Bank, l bank.FeedLine) lineView {
	return lineView{FeedLine: l, Bank: b, Description: Normalise(l.Narration), Href: "/ui/accounts/" + url.PathEscape(acctKey(b.ID, l.NUBAN)) + "/lines/" + url.PathEscape(l.Key)}
}

func (a *App) roomFail(w http.ResponseWriter, r *http.Request, err error) {
	a.log.Error("control room", "path", r.URL.Path, "err", err)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "room_failed", http.StatusInternalServerError, "", roomPage{"Title": "Something went wrong", "Error": err.Error()})
}

func (a *App) roomOverview(w http.ResponseWriter, r *http.Request) {
	type bankSum struct {
		*config.Bank
		bank.Totals
	}
	sums := []bankSum{}
	var total int64
	for _, bk := range a.books() {
		t, err := bk.ls.Bank().Totals(r.Context())
		if err != nil {
			a.roomFail(w, r, err)
			return
		}
		sums = append(sums, bankSum{bk.bank, t})
		total += t.Deposits
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	active := 0
	for _, c := range a.st.Connections {
		if c.Status == "active" || c.Status == "syncing" {
			active++
		}
	}
	a.roomRender(w, r, "overview", http.StatusOK, "overview", roomPage{
		"Title":       "Overview",
		"BankSums":    sums,
		"Total":       total,
		"Connections": len(a.st.Connections),
		"Active":      active,
		"Deliveries":  len(a.st.Deliveries),
	})
}

const pageSize = 50

func (a *App) roomBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, managed := a.bankStatus(id)
	if managed && (status.Phase == PhaseProvisioning || status.Phase == PhaseRepairing) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomRender(w, r, "bank_provisioning", http.StatusOK, "banks", roomPage{"Title": status.Name, "Status": status, "Refresh": true})
		return
	}
	bk, ok := a.bookOf(id)
	if !ok {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
		return
	}
	ctx := r.Context()
	t, err := bk.ls.Bank().Totals(ctx)
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	page, next, err := bk.ls.Bank().Accounts(ctx, "", pageSize, cursor)
	if errors.Is(err, bank.ErrBadCursor) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	accts := []acctView{}
	for _, acc := range page {
		v, err := bk.acctView(ctx, acc)
		if err != nil {
			a.roomFail(w, r, err)
			return
		}
		accts = append(accts, v)
	}
	down := a.bankDown(id, a.clock.Now())
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "bank", http.StatusOK, "banks", roomPage{"Title": bk.bank.Name, "Bank": bk.bank, "Accounts": accts, "Totals": t, "Down": down, "Managed": managed, "Status": status, "Next": next, "First": cursor != ""})
}

func (a *App) roomAccountOf(w http.ResponseWriter, r *http.Request) (book, ledger.Account, bool) {
	id, nuban := splitKey(r.PathValue("key"))
	bk, ok := a.bookOf(id)
	var acc ledger.Account
	var err error
	if ok {
		acc, err = bk.ls.Account(nuban)
		ok = err == nil && acc.Customer
	}
	if !ok && err != nil && !errors.Is(err, ledger.ErrUnknownAccount) {
		a.roomFail(w, r, err)
		return book{}, acc, false
	}
	if !ok {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
	}
	return bk, acc, ok
}

func (a *App) roomAccount(w http.ResponseWriter, r *http.Request) {
	bk, acc, ok := a.roomAccountOf(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v, err := bk.acctView(ctx, acc)
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	ls, next, err := bk.ls.Bank().Recent(ctx, acc.NUBAN, pageSize, cursor)
	if errors.Is(err, bank.ErrBadCursor) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	lines := []lineView{}
	for _, l := range ls {
		lines = append(lines, newLineView(bk.bank, l))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	recs := []map[string]any{}
	for _, rec := range a.st.Accounts {
		if rec.Key != v.Key {
			continue
		}
		c, _ := a.grantFor(rec)
		status := "revoked"
		if c != nil {
			status = c.Status
		}
		name := rec.PartnerID
		if p := a.partnerOf(rec.PartnerID); p != nil {
			name = p.Name
		}
		recs = append(recs, map[string]any{"Partner": name, "ID": rec.ID, "Status": status, "AsAt": rec.AsAt})
	}
	a.roomRender(w, r, "account", http.StatusOK, "banks", roomPage{
		"Title":    v.Name,
		"A":        v,
		"Lines":    lines,
		"Next":     next,
		"First":    cursor != "",
		"Shares":   recs,
		"Transfer": transferOutcome(r.URL.Query().Get("transfer")),
	})
}

func transferOutcome(status string) string {
	switch status {
	case nip.Success:
		return "Transfer sent. Both banks have posted it."
	case nip.Pending:
		return "Transfer pending. The receiving bank did not answer, so NIBSS will check its status."
	case nip.Reversed:
		return "Transfer reversed. The receiving bank could not take it, so the money came back."
	}
	return ""
}

func (a *App) roomLine(w http.ResponseWriter, r *http.Request) {
	bk, acc, ok := a.roomAccountOf(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	l, err := bk.ls.Bank().Line(ctx, acc.NUBAN, r.PathValue("line"))
	if errors.Is(err, bank.ErrUnknownLine) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	v, err := bk.acctView(ctx, acc)
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	trace := []lineView{}
	var debit, credit, charges int64
	if l.SessionID != "" {
		for _, other := range a.books() {
			ls, err := other.ls.Bank().SessionLines(ctx, l.SessionID)
			if err != nil {
				a.roomFail(w, r, err)
				return
			}
			for _, t := range ls {
				trace = append(trace, newLineView(other.bank, t))
				switch {
				case t.Channel == "charge":
					charges -= t.Amount
				case t.Amount < 0:
					debit -= t.Amount
				default:
					credit += t.Amount
				}
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	type share struct {
		Partner, AccountID, TxnID, Status string
		Synced                            bool
	}
	shares := []share{}
	for _, rec := range a.st.Accounts {
		if rec.Key != v.Key {
			continue
		}
		id := txnID(rec.PartnerID, v.Key, l.Key, false)
		sh := share{AccountID: rec.ID, TxnID: id, Status: "revoked"}
		if p := a.partnerOf(rec.PartnerID); p != nil {
			sh.Partner = p.Name
		}
		if c, _ := a.grantFor(rec); c != nil {
			sh.Status = c.Status
		}
		if f := a.st.Feeds[rec.ID]; f != nil {
			_, sh.Synced = f.Lines[id]
		}
		shares = append(shares, sh)
	}
	lv := newLineView(bk.bank, l)
	a.roomRender(w, r, "line", http.StatusOK, "banks", roomPage{
		"Title":   lv.Description,
		"L":       lv,
		"A":       v,
		"Trace":   trace,
		"Debit":   debit,
		"Credit":  credit,
		"Charges": charges,
		"Shares":  shares,
	})
}

func (a *App) roomTransferForm(w http.ResponseWriter, r *http.Request) {
	bank, nuban := splitKey(r.URL.Query().Get("from"))
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "transfer", http.StatusOK, "transfer", roomPage{"Title": "New transfer", "Form": url.Values{"from_bank": {bank}, "from": {nuban}}})
}

func (a *App) roomTransfer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	req := TransferRequest{FromBank: f.Get("from_bank"), From: strings.TrimSpace(f.Get("from")), ToBank: f.Get("to_bank"), To: strings.TrimSpace(f.Get("to")), Narration: strings.TrimSpace(f.Get("narration"))}
	amount, err := parseNaira(f.Get("amount"))
	if err != nil {
		err = errBadAmount
	} else {
		req.Amount = amount
		var res TransferResult
		res, err = a.Transfer(req)
		if err == nil {
			a.SyncAll()
			http.Redirect(w, r, "/ui/accounts/"+url.PathEscape(acctKey(res.FromBank, res.From))+"?transfer="+url.QueryEscape(res.Status), http.StatusSeeOther)
			return
		}
	}
	var te *transferError
	if !errors.As(err, &te) && !errors.Is(err, errBadAmount) {
		a.roomFail(w, r, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "transfer", http.StatusUnprocessableEntity, "transfer", roomPage{"Title": "New transfer", "Form": f, "Error": sentence(err.Error())})
}

var errBadAmount = errors.New("enter an amount in naira, like 25000 or 2500.50")

func sentence(msg string) string {
	if msg == "" {
		return msg
	}
	return strings.ToUpper(msg[:1]) + strings.TrimSuffix(msg[1:], ".") + "."
}

func (a *App) roomConnections(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	type row struct {
		*Connection
		Partner, Bank, Customer string
		Accounts                int
	}
	rows := []row{}
	for _, c := range a.st.Connections {
		x := row{Connection: c, Partner: c.PartnerID, Accounts: len(c.AccountIDs)}
		if p := a.partnerOf(c.PartnerID); p != nil {
			x.Partner = p.Name
		}
		x.Customer = c.CustomerID
		if b := a.cfg.Bank(c.Institution); b != nil {
			x.Bank = b.Name
		}
		rows = append(rows, x)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	a.roomRender(w, r, "connections", http.StatusOK, "connections", roomPage{"Title": "Connections", "Rows": rows})
}

func (a *App) roomRevoke(w http.ResponseWriter, r *http.Request) {
	_ = a.RevokeAtBank(r.PathValue("id"))
	a.poke()
	http.Redirect(w, r, "/ui/connections", http.StatusSeeOther)
}

func (a *App) roomWebhooks(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rows := []*Delivery{}
	for i := len(a.st.Deliveries) - 1; i >= 0 && len(rows) < 100; i-- {
		rows = append(rows, a.st.Deliveries[i])
	}
	a.roomRender(w, r, "webhooks", http.StatusOK, "webhooks", roomPage{"Title": "Webhooks", "Rows": rows})
}

func (a *App) roomReplay(w http.ResponseWriter, r *http.Request) {
	_ = a.Replay(r.PathValue("id"))
	http.Redirect(w, r, "/ui/webhooks", http.StatusSeeOther)
}

func (a *App) roomNotFound(w http.ResponseWriter, r *http.Request) {
	a.roomRender(w, r, "notfound", http.StatusNotFound, "", roomPage{"Title": "Not found"})
}

var roomPages = template.Must(template.New("").Funcs(uiFuncs).Funcs(template.FuncMap{
	"logo":  func() template.HTML { return template.HTML(logoSVG) },
	"pathq": url.PathEscape,
	"amtClass": func(v int64) string {
		if v > 0 {
			return "text-pos"
		}
		return "text-ink"
	},
	"list":       func(v ...any) []any { return v },
	"trimScheme": func(u string) string { return strings.TrimPrefix(u, "http://") },
}).Parse(roomTemplates))

func (a *App) roomNewBank(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "new_bank", http.StatusOK, "banks", roomPage{"Title": "New bank", "Form": url.Values{"type": {"commercial"}}})
}

func (a *App) roomCreateBank(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	b, err := a.CreateBank(BankRequest{Name: f.Get("name"), Type: f.Get("type"), SettlesThrough: f.Get("settles_through")})
	if err != nil {
		msg := err.Error()
		if errors.Is(err, ErrBankInvalid) {
			msg = strings.TrimPrefix(msg, ErrBankInvalid.Error()+": ")
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomRender(w, r, "new_bank", http.StatusUnprocessableEntity, "banks", roomPage{"Title": "New bank", "Form": f, "Error": msg})
		return
	}
	http.Redirect(w, r, "/ui/banks/"+url.PathEscape(b.ID), http.StatusSeeOther)
}

func (a *App) roomSuspend(w http.ResponseWriter, r *http.Request, suspended bool) {
	id := r.PathValue("id")
	if err := a.SetBankSuspended(id, suspended); err != nil {
		if errors.Is(err, errNotFound) {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.roomNotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/ui/banks/"+url.PathEscape(id), http.StatusSeeOther)
}

type bankRow struct {
	*config.Bank
	Phase     string
	URL       string
	Customers int64
	Deposits  int64
}

func (a *App) roomBanks(w http.ResponseWriter, r *http.Request) {
	statuses := map[string]BankStatus{}
	for _, st := range a.BankStatuses() {
		statuses[st.ID] = st
	}
	totals := map[string]bank.Totals{}
	for _, bk := range a.books() {
		if statuses[bk.bank.ID].Phase == PhaseClosed {
			continue
		}
		t, err := bk.ls.Bank().Totals(r.Context())
		if err != nil {
			a.roomFail(w, r, err)
			return
		}
		totals[bk.bank.ID] = t
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rows := []*bankRow{}
	for _, b := range a.cfg.Banks {
		row := &bankRow{Bank: b, Phase: PhaseActive, Customers: totals[b.ID].Customers, Deposits: totals[b.ID].Deposits}
		if st, ok := statuses[b.ID]; ok {
			row.Phase, row.URL = st.Phase, st.URL
		}
		rows = append(rows, row)
	}
	state := r.URL.Query().Get("state")
	counts := map[string]int{}
	shown := []*bankRow{}
	for _, row := range rows {
		if row.Phase == PhaseClosed {
			row.URL = ""
		}
		counts[row.Phase]++
		if state == "" || row.Phase == state {
			shown = append(shown, row)
		}
	}
	filters := []bankFilter{{Label: "All", Count: len(rows), On: state == ""}}
	for _, p := range bankPhases {
		if counts[p] > 0 || p == PhaseActive || p == PhaseSuspended || p == PhaseClosed {
			filters = append(filters, bankFilter{State: p, Label: strings.ToUpper(p[:1]) + p[1:], Count: counts[p], On: state == p})
		}
	}
	a.roomRender(w, r, "banks", http.StatusOK, "banks", roomPage{"Title": "Banks", "Rows": shown, "State": state, "Filters": filters, "CanCreate": len(a.cfg.Banks) > 0, "Warnings": a.BankWarnings()})
}

var bankPhases = []string{PhaseActive, PhaseSuspended, PhaseProvisioning, PhaseRepairing, PhaseClosed}

type bankFilter struct {
	State string
	Label string
	Count int
	On    bool
}

func (a *App) roomCloseBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = r.ParseForm()
	f := r.PostForm
	status := http.StatusOK
	msg := ""
	if r.Method == http.MethodPost {
		req := CloseRequest{Money: f.Get("money"), To: f.Get("to"), Customers: map[string]string{}}
		for k, v := range f {
			if p, ok := strings.CutPrefix(k, "c_"); ok && len(v) > 0 && v[0] != "" {
				req.Customers[p] = v[0]
			}
		}
		var err error
		if strings.TrimSpace(f.Get("confirm")) != id {
			err = invalid("type %s to confirm", id)
		} else {
			_, err = a.CloseBank(id, req)
		}
		if err == nil {
			http.Redirect(w, r, "/ui/banks/"+url.PathEscape(id), http.StatusSeeOther)
			return
		}
		if !errors.Is(err, errNotFound) {
			status, msg = http.StatusUnprocessableEntity, strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": ")
			msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
		}
	}
	imp, err := a.CloseImpact(id)
	if errors.Is(err, errNotFound) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	inst := a.cfg.Bank(id)
	if inst == nil {
		a.roomNotFound(w, r)
		return
	}
	targets := []*config.Bank{}
	for _, b := range a.openInstitutions() {
		if b.ID != id && !a.bankDownLocked(b.ID, now) {
			targets = append(targets, b)
		}
	}
	if f.Get("money") == "" {
		f = url.Values{"money": {"transfer"}}
	}
	a.roomRender(w, r, "close_bank", status, "banks", roomPage{"Title": "Close " + inst.Name, "Bank": inst, "Impact": imp, "Targets": targets, "Form": f, "Error": msg})
}

func (a *App) roomPurgeBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = r.ParseForm()
	status := http.StatusOK
	msg := ""
	if r.Method == http.MethodPost {
		var err error
		if strings.TrimSpace(r.PostForm.Get("confirm")) != id {
			err = invalid("type %s to confirm", id)
		} else {
			_, err = a.PurgeBank(id, false)
		}
		switch {
		case err == nil:
			http.Redirect(w, r, "/ui/banks?state=closed", http.StatusSeeOther)
			return
		case a.cfg.Bank(id) == nil:
			http.Redirect(w, r, "/ui/banks?state=closed", http.StatusSeeOther)
			return
		case !errors.Is(err, errNotFound):
			status, msg = http.StatusUnprocessableEntity, strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": ")
			msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
		}
	}
	imp, err := a.PurgeImpact(id)
	if err != nil && !errors.Is(err, errNotFound) {
		a.roomFail(w, r, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	inst := a.cfg.Bank(id)
	if err != nil || inst == nil {
		a.roomNotFound(w, r)
		return
	}
	a.roomRender(w, r, "purge_bank", status, "banks", roomPage{"Title": "Purge " + inst.Name, "Bank": inst, "Impact": imp, "Error": msg})
}
