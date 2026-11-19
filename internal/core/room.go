package core

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/money"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/view"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const roomCookie = "beerus_room"

func RoomURL(cfg *config.Config, token string) string {
	return cfg.ControlURL() + "/ui/login?token=" + url.QueryEscape(token)
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
		if err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(web.HashSecret(a.link.Token))) != 1 {
			http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) roomHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", view.Assets())
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
	view.SetHeaders(w.Header())
	w.WriteHeader(status)
	if err := roomPages.ExecuteTemplate(w, name, data); err != nil {
		a.log.Error("render", "page", name, "err", err)
	}
}

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The form could not be read.", http.StatusBadRequest)
		return false
	}
	return true
}

func (a *App) roomLogin(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if r.Method == http.MethodPost {
		if !parseForm(w, r) {
			return
		}
		tok = strings.TrimSpace(r.PostForm.Get("token"))
	}
	if tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(a.link.Token)) == 1 {
		http.SetCookie(w, &http.Cookie{Name: roomCookie, Value: web.HashSecret(a.link.Token), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
		http.Redirect(w, r, "/ui", http.StatusSeeOther)
		return
	}
	data := roomPage{"Title": "Sign in"}
	if tok != "" {
		data["Error"] = "That token is not valid."
	}
	view.SetHeaders(w.Header())
	if err := roomPages.ExecuteTemplate(w, "room_login", data); err != nil {
		a.log.Error("render", "page", "room_login", "err", err)
	}
}

type book struct {
	bank  *config.Bank
	books bankBooks
}

func (a *App) books() []book {
	out := []book{}
	for _, b := range a.bankList() {
		if bb := a.booksOf(b.ID); bb != nil {
			out = append(out, book{b, bb})
		}
	}
	return out
}

func (a *App) bookOf(id string) (book, bool) {
	b := a.bankByID(id)
	if b == nil {
		return book{}, false
	}
	bb := a.booksOf(id)
	return book{b, bb}, bb != nil
}

type acctView struct {
	control.AccountJSON
	Key       string
	Bank      *config.Bank
	Holder    string
	Ledger    int64
	Available int64
}

func (bk book) acctView(acc control.AccountJSON) acctView {
	return acctView{AccountJSON: acc, Key: acctKey(bk.bank.ID, acc.NUBAN), Bank: bk.bank, Holder: acc.Name, Ledger: acc.Balance, Available: acc.Balance}
}

type lineView struct {
	FeedLine
	Bank        *config.Bank
	Description string
	Href        string
}

func newLineView(b *config.Bank, l FeedLine) lineView {
	return lineView{FeedLine: l, Bank: b, Description: view.Normalise(l.Narration), Href: "/ui/accounts/" + url.PathEscape(acctKey(b.ID, l.NUBAN)) + "/lines/" + url.PathEscape(l.Key)}
}

func (a *App) roomFail(w http.ResponseWriter, r *http.Request, err error) {
	a.log.Error("control room", "path", r.URL.Path, "err", err)
	status, msg := http.StatusInternalServerError, err.Error()
	var down *bankDownError
	var connectDown *control.ConnectDown
	switch {
	case errors.As(err, &down):
		status, msg = http.StatusServiceUnavailable, fmt.Sprintf(bankAway, down.Name)
	case errors.As(err, &connectDown):
		status, msg = http.StatusServiceUnavailable, control.ConnectAway
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "room_failed", status, "", roomPage{"Title": "Something went wrong", "Error": msg})
}

func (a *App) roomOverview(w http.ResponseWriter, r *http.Request) {
	type bankSum struct {
		*config.Bank
		Totals
	}
	sums := []bankSum{}
	var total int64
	for _, bk := range a.books() {
		t, err := bk.books.totals(r.Context())
		if err != nil {
			a.roomFail(w, r, err)
			return
		}
		sums = append(sums, bankSum{bk.bank, t})
		total += t.Deposits
	}
	cs, err := a.agg.State(r.Context())
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "overview", http.StatusOK, "overview", roomPage{
		"Title":       "Overview",
		"BankSums":    sums,
		"Total":       total,
		"Connections": cs.Connections,
		"Active":      cs.Active,
		"Deliveries":  cs.Deliveries,
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
	t, err := bk.books.totals(ctx)
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	page, next, err := bk.books.accounts(ctx, "", pageSize, cursor)
	if errors.Is(err, errBankCursor) {
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
		accts = append(accts, bk.acctView(acc))
	}
	down := a.downBanks()[id]
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "bank", http.StatusOK, "banks", roomPage{"Title": bk.bank.Name, "Bank": bk.bank, "Accounts": accts, "Totals": t, "Down": down, "Managed": managed, "Status": status, "Next": next, "First": cursor != ""})
}

func (a *App) roomAccountOf(w http.ResponseWriter, r *http.Request) (book, control.AccountJSON, bool) {
	id, nuban := splitKey(r.PathValue("key"))
	bk, ok := a.bookOf(id)
	var acc control.AccountJSON
	var err error
	if ok {
		acc, err = bk.books.account(r.Context(), nuban)
		ok = err == nil
	}
	if !ok && err != nil && !errors.Is(err, errUnknownAccount) {
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
	v := bk.acctView(acc)
	cursor := r.URL.Query().Get("cursor")
	ls, next, err := bk.books.recent(ctx, acc.NUBAN, pageSize, cursor)
	if errors.Is(err, errBankCursor) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	if err := a.markRemoved(ctx, ls); err != nil {
		a.roomFail(w, r, err)
		return
	}
	lines := []lineView{}
	for _, l := range ls {
		lines = append(lines, newLineView(bk.bank, l))
	}
	shares, err := a.agg.Shares(ctx, v.Key, "")
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	recs := []map[string]any{}
	for _, sh := range shares {
		recs = append(recs, map[string]any{"Partner": a.partnerName(sh.Partner), "ID": sh.AccountID, "Status": sh.Status, "AsAt": sh.AsAt})
	}
	a.roomRender(w, r, "account", http.StatusOK, "banks", roomPage{
		"Title":    v.Name,
		"A":        v,
		"Lines":    lines,
		"Next":     next,
		"First":    cursor != "",
		"Shares":   recs,
		"Transfer": view.TransferOutcome(r.URL.Query().Get("transfer")),
	})
}

func (a *App) roomLine(w http.ResponseWriter, r *http.Request) {
	bk, acc, ok := a.roomAccountOf(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	l, err := bk.books.line(ctx, acc.NUBAN, r.PathValue("line"))
	if errors.Is(err, errUnknownLine) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	v := bk.acctView(acc)
	if err := a.markRemoved(ctx, []FeedLine{l}); err != nil {
		a.roomFail(w, r, err)
		return
	}
	trace := []lineView{}
	var debit, credit, charges int64
	if l.SessionID != "" {
		for _, other := range a.books() {
			ls, err := other.books.sessionLines(ctx, l.SessionID)
			if err != nil {
				a.roomFail(w, r, err)
				return
			}
			if err := a.markRemoved(ctx, ls); err != nil {
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
	given, err := a.agg.Shares(ctx, v.Key, l.Key)
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	type share struct {
		Partner, AccountID, TxnID, Status string
		Synced                            bool
	}
	shares := []share{}
	for _, sh := range given {
		x := share{AccountID: sh.AccountID, TxnID: sh.TxnID, Status: sh.Status, Synced: sh.Synced}
		if p := a.partnerOf(sh.Partner); p != nil {
			x.Partner = p.Name
		}
		shares = append(shares, x)
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
	a.roomRender(w, r, "transfer", http.StatusOK, "transfer", roomPage{"Title": "New transfer", "Key": web.NewID("idk"), "Form": url.Values{"from_bank": {bank}, "from": {nuban}}})
}

func (a *App) roomTransfer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	req := control.TransferRequest{FromBank: f.Get("from_bank"), From: strings.TrimSpace(f.Get("from")), ToBank: f.Get("to_bank"), To: strings.TrimSpace(f.Get("to")), Narration: strings.TrimSpace(f.Get("narration")), Key: f.Get("key")}
	amount, err := money.ParseNaira(f.Get("amount"))
	if err != nil {
		err = view.ErrBadAmount
	} else {
		req.Amount = amount
		var res control.TransferResult
		res, err = a.transfer(r.Context(), req)
		if err == nil {
			a.SyncAll(r.Context())
			http.Redirect(w, r, "/ui/accounts/"+url.PathEscape(acctKey(res.FromBank, res.From))+"?transfer="+url.QueryEscape(res.Status), http.StatusSeeOther)
			return
		}
	}
	var te *control.TransferError
	refused := errors.Is(err, view.ErrBadAmount) || (errors.As(err, &te) && te.Status < http.StatusInternalServerError)
	status, key, message := http.StatusUnprocessableEntity, web.NewID("idk"), web.Sentence(err.Error())
	if !refused {
		a.log.Error("control room transfer", "err", err)
		status = http.StatusServiceUnavailable
		if f.Get("key") != "" {
			key = f.Get("key")
		}
		message += " The transfer may or may not have gone through. Check the account, then send again. The same transfer is never sent twice."
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "transfer", status, "transfer", roomPage{"Title": "New transfer", "Key": key, "Form": f, "Error": message})
}

func (a *App) partnerName(id string) string {
	if p := a.partnerOf(id); p != nil {
		return p.Name
	}
	return id
}

func (a *App) roomConnections(w http.ResponseWriter, r *http.Request) {
	list, err := a.agg.Connections(r.Context(), "", "")
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	type row struct {
		ID, Status, Partner, Bank, Customer string
		Accounts                            int
		ExpiresAt, CreatedAt                time.Time
	}
	rows := []row{}
	for _, c := range list {
		x := row{ID: c.ID, Status: c.Status, Partner: a.partnerName(c.PartnerID), Bank: c.Institution.Name, Customer: c.CustomerID, Accounts: len(c.AccountIDs), ExpiresAt: parseTime(c.ConsentExpiresAt), CreatedAt: parseTime(&c.CreatedAt)}
		if b := a.cfg.Bank(c.Institution.ID); b != nil {
			x.Bank = b.Name
		}
		rows = append(rows, x)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	a.roomRender(w, r, "connections", http.StatusOK, "connections", roomPage{"Title": "Connections", "Rows": rows})
}

func (a *App) roomRevoke(w http.ResponseWriter, r *http.Request) {
	err := a.agg.RevokeConnection(r.Context(), r.PathValue("id"))
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusNotFound {
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		a.log.Error("revoke consent", "connection", r.PathValue("id"), "err", err)
		http.Error(w, "The bank did not revoke the consent. Try again.", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/ui/connections", http.StatusSeeOther)
}

func (a *App) roomWebhooks(w http.ResponseWriter, r *http.Request) {
	cs, err := a.agg.State(r.Context())
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	page, ok := view.PageParam(w, r, "webhook list", view.PagesOf(cs.Deliveries, pageSize))
	if !ok {
		return
	}
	var rows []control.DeliveryView
	cursor := ""
	for n := 1; n <= page; n++ {
		if rows, cursor, err = a.agg.Deliveries(r.Context(), pageSize, cursor); err != nil {
			a.roomFail(w, r, err)
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "webhooks", http.StatusOK, "webhooks", roomPage{"Title": "Webhooks", "Rows": rows, "Page": page, "Older": cursor != "", "Newer": page > 1})
}

func (a *App) roomReplay(w http.ResponseWriter, r *http.Request) {
	err := a.agg.Replay(r.Context(), r.PathValue("id"))
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusNotFound {
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		a.roomFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/ui/webhooks", http.StatusSeeOther)
}

func (a *App) roomNotFound(w http.ResponseWriter, r *http.Request) {
	a.roomRender(w, r, "notfound", http.StatusNotFound, "", roomPage{"Title": "Not found"})
}

var roomPages = template.Must(template.New("").Funcs(view.Funcs()).Funcs(template.FuncMap{
	"logo":  func() template.HTML { return template.HTML(view.LogoSVG) },
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
	b, err := a.CreateBank(r.Context(), BankRequest{Name: f.Get("name"), Type: f.Get("type"), SettlesThrough: f.Get("settles_through")})
	if err != nil {
		msg := err.Error()
		if errors.Is(err, control.ErrInvalid) {
			msg = strings.TrimPrefix(msg, control.ErrInvalid.Error()+": ")
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
	if err := a.SetBankSuspended(r.Context(), id, suspended); err != nil {
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
	Customers int
	Deposits  int64
}

func (a *App) roomBanks(w http.ResponseWriter, r *http.Request) {
	statuses := map[string]BankStatus{}
	for _, st := range a.BankStatuses() {
		statuses[st.ID] = st
	}
	totals := map[string]Totals{}
	for _, bk := range a.books() {
		if statuses[bk.bank.ID].Phase == PhaseClosed {
			continue
		}
		t, err := bk.books.totals(r.Context())
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
	a.roomRender(w, r, "banks", http.StatusOK, "banks", roomPage{"Title": "Banks", "Rows": shown, "State": state, "Filters": filters, "Warnings": a.BankWarnings()})
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
	if !parseForm(w, r) {
		return
	}
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
			err = control.Invalid("type %s to confirm", id)
		} else {
			_, err = a.CloseBank(r.Context(), id, req)
		}
		if err == nil {
			http.Redirect(w, r, "/ui/banks/"+url.PathEscape(id), http.StatusSeeOther)
			return
		}
		if !errors.Is(err, errNotFound) {
			status, msg = http.StatusUnprocessableEntity, strings.TrimPrefix(err.Error(), control.ErrInvalid.Error()+": ")
			msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
		}
	}
	imp, err := a.CloseImpact(r.Context(), id)
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
	down := a.downBanks()
	a.mu.Lock()
	defer a.mu.Unlock()
	inst := a.cfg.Bank(id)
	if inst == nil {
		a.roomNotFound(w, r)
		return
	}
	targets := []*config.Bank{}
	for _, b := range a.openInstitutions() {
		if b.ID != id && !down[b.ID] {
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
	if !parseForm(w, r) {
		return
	}
	status := http.StatusOK
	msg := ""
	if r.Method == http.MethodPost {
		var err error
		if strings.TrimSpace(r.PostForm.Get("confirm")) != id {
			err = control.Invalid("type %s to confirm", id)
		} else {
			_, err = a.PurgeBank(r.Context(), id, false)
		}
		switch {
		case err == nil:
			http.Redirect(w, r, "/ui/banks?state=closed", http.StatusSeeOther)
			return
		case a.cfg.Bank(id) == nil:
			http.Redirect(w, r, "/ui/banks?state=closed", http.StatusSeeOther)
			return
		case !errors.Is(err, errNotFound):
			status, msg = http.StatusUnprocessableEntity, strings.TrimPrefix(err.Error(), control.ErrInvalid.Error()+": ")
			msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
		}
	}
	imp, err := a.PurgeImpact(r.Context(), id)
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
