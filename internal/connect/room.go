package connect

import (
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
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
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
	mux.HandleFunc("POST /ui/banks/{id}/accounts", a.roomOpenAccount)
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
	mux.HandleFunc("POST /ui/clock", a.roomClock)
	mux.HandleFunc("GET /ui/connections", a.roomConnections)
	mux.HandleFunc("POST /ui/connections/{id}/revoke", a.roomRevoke)
	mux.HandleFunc("GET /ui/webhooks", a.roomWebhooks)
	mux.HandleFunc("POST /ui/webhooks/{id}/replay", a.roomReplay)
	return a.roomOnly(mux)
}

type roomPage map[string]any

func (a *App) roomRender(w http.ResponseWriter, r *http.Request, name string, status int, nav string, data roomPage) {
	st := a.clock.State()
	data["Back"] = r.URL.RequestURI()
	data["Now"] = a.clock.Now()
	data["Pace"] = st.Pace
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
		if !roomForm(w, r) {
			return
		}
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

type acctView struct {
	world.Account
	Bank      world.Institution
	Holder    string
	Ledger    int64
	Available int64
	Pending   int
}

func (a *App) pageFailed(w http.ResponseWriter, what string, err error) {
	a.log.Error(what, "err", err)
	http.Error(w, "The request failed. Try again.", http.StatusInternalServerError)
}

func (a *App) acctView(acc world.Account, at time.Time) (acctView, error) {
	v := acctView{Account: acc}
	v.Bank, _ = a.world.Institution(acc.Institution)
	if p, ok := a.world.Persona(acc.Owner); ok {
		v.Holder = p.Name
	}
	var err error
	v.Ledger, err = a.src(acc.Key).LedgerAt(acc.Key, at)
	if err != nil {
		return acctView{}, fmt.Errorf("read balance of %s: %w", acc.Key, err)
	}
	pend, err := a.src(acc.Key).PendingAt(acc.Key, at)
	if err != nil {
		return acctView{}, fmt.Errorf("read pending total of %s: %w", acc.Key, err)
	}
	v.Available = v.Ledger
	if pend < 0 {
		v.Available += pend
	}
	ls, err := a.src(acc.Key).Fetch(acc.Key, at, clock.DateOf(at).AddDays(-3))
	if err != nil {
		return acctView{}, fmt.Errorf("read recent lines of %s: %w", acc.Key, err)
	}
	for _, l := range ls {
		if l.Pending {
			v.Pending++
		}
	}
	return v, nil
}

type lineView struct {
	world.Presented
	Account     world.Account
	Bank        world.Institution
	Description string
	Holder      string
	Href        string
}

func lineHref(account string, l world.Presented) string {
	h := "/ui/accounts/" + url.PathEscape(account) + "/lines/" + url.PathEscape(l.Key)
	if l.Pending {
		h += "?pending=1"
	}
	return h
}

func (a *App) lineView(acc world.Account, l world.Presented) lineView {
	inst, _ := a.world.Institution(acc.Institution)
	holder := ""
	if p, ok := a.world.Persona(acc.Owner); ok {
		holder = p.Name
	}
	return lineView{Presented: l, Account: acc, Bank: inst, Description: Normalise(l.Narration), Holder: holder, Href: lineHref(acc.Key, l)}
}

func (a *App) roomOverview(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	type bankSum struct {
		world.Institution
		Accounts int
		Deposits int64
	}
	sums := map[string]*bankSum{}
	for _, inst := range a.world.Institutions() {
		sums[inst.ID] = &bankSum{Institution: inst}
	}
	recent := []lineView{}
	for _, acc := range a.world.Accounts() {
		s := sums[acc.Institution]
		s.Accounts++
		bal, err := a.src(acc.Key).LedgerAt(acc.Key, now)
		if err != nil {
			a.pageFailed(w, "overview", fmt.Errorf("read balance of %s: %w", acc.Key, err))
			return
		}
		s.Deposits += bal
		ls, err := a.src(acc.Key).Fetch(acc.Key, now, clock.DateOf(now).AddDays(-14))
		if err != nil {
			a.pageFailed(w, "overview", fmt.Errorf("read recent lines of %s: %w", acc.Key, err))
			return
		}
		if len(ls) > recentLimit {
			ls = ls[len(ls)-recentLimit:]
		}
		for _, l := range ls {
			recent = append(recent, a.lineView(acc, l))
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].At.After(recent[j].At) })
	if len(recent) > recentLimit {
		recent = recent[:recentLimit]
	}
	banks := []*bankSum{}
	var total int64
	for _, inst := range a.world.Institutions() {
		banks = append(banks, sums[inst.ID])
		total += sums[inst.ID].Deposits
	}
	active := 0
	for _, c := range a.st.Connections {
		if c.Status == "active" || c.Status == "syncing" {
			active++
		}
	}
	a.roomRender(w, r, "overview", http.StatusOK, "overview", roomPage{
		"Title":       "Overview",
		"BankSums":    banks,
		"Total":       total,
		"Recent":      recent,
		"RecentNote":  recentNote(len(recent)),
		"Connections": len(a.st.Connections),
		"Active":      active,
		"WorldID":     a.st.WorldID,
		"Deliveries":  len(a.st.Deliveries),
	})
}

func (a *App) roomBank(w http.ResponseWriter, r *http.Request) {
	status, managed := a.bankStatus(r.PathValue("id"))
	a.mu.Lock()
	defer a.mu.Unlock()
	inst, ok := a.world.Institution(r.PathValue("id"))
	ok = ok && !a.world.Purged(inst.ID)
	if managed && (status.Phase == PhaseProvisioning || status.Phase == PhaseRepairing) {
		a.roomRender(w, r, "bank_provisioning", http.StatusOK, "banks", roomPage{"Title": status.Name, "Status": status, "Refresh": true})
		return
	}
	if !ok {
		a.roomNotFound(w, r)
		return
	}
	now := a.clock.Now()
	accts := []acctView{}
	var total int64
	for _, acc := range a.world.Accounts() {
		if acc.Institution == inst.ID {
			v, err := a.acctView(acc, now)
			if err != nil {
				a.pageFailed(w, "bank", err)
				return
			}
			total += v.Ledger
			accts = append(accts, v)
		}
	}
	a.roomRender(w, r, "bank", http.StatusOK, "banks", roomPage{"Title": inst.Name, "Bank": inst, "Accounts": accts, "Total": total, "Down": a.world.Down(inst.ID, now), "Managed": managed, "Status": status, "Personas": a.world.Personas(), "Error": r.URL.Query().Get("error")})
}

const pageSize = 50

func pagesOf(rows, size int) int {
	return max((rows+size-1)/size, 1)
}

func pageParam(w http.ResponseWriter, r *http.Request, what string, last int) (int, bool) {
	raw := r.URL.Query().Get("page")
	if raw == "" {
		return 1, true
	}
	page, err := strconv.Atoi(raw)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		http.Error(w, fmt.Sprintf("The page %q must be a whole number.", raw), http.StatusUnprocessableEntity)
		return 0, false
	}
	page = max(page, 1)
	if page > last {
		http.Error(w, fmt.Sprintf("The page %s is too high. The %s allows pages 1 to %d.", raw, what, last), http.StatusUnprocessableEntity)
		return 0, false
	}
	return page, true
}

const recentLimit = 14

func recentNote(n int) string {
	if n == 1 {
		return "Showing the latest transaction across all accounts. Open an account to see its full statement."
	}
	return fmt.Sprintf("Showing the latest %d transactions across all accounts. Open an account to see its full statement.", n)
}

func (a *App) roomAccount(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	acc, ok := a.world.Account(r.PathValue("key"))
	if !ok {
		a.roomNotFound(w, r)
		return
	}
	now := a.clock.Now()
	ls, err := a.statement(acc.Key, now)
	if err != nil {
		a.pageFailed(w, "account", fmt.Errorf("read statement of %s: %w", acc.Key, err))
		return
	}
	page, ok := pageParam(w, r, "statement", pagesOf(len(ls), pageSize)+1)
	if !ok {
		return
	}
	end := len(ls) - (page-1)*pageSize
	if end < 0 {
		end = 0
	}
	start := end - pageSize
	if start < 0 {
		start = 0
	}
	lines := []lineView{}
	for i := end - 1; i >= start; i-- {
		lines = append(lines, a.lineView(acc, ls[i]))
	}
	recs := []map[string]any{}
	for _, rec := range a.st.Accounts {
		if rec.WorldKey != acc.Key {
			continue
		}
		c, _ := a.grantFor(rec)
		status := "revoked"
		if c != nil {
			status = c.Status
		}
		p := a.partnerOf(rec.PartnerID)
		name := rec.PartnerID
		if p != nil {
			name = p.Name
		}
		recs = append(recs, map[string]any{"Partner": name, "ID": rec.ID, "Status": status, "AsAt": rec.AsAt})
	}
	v, err := a.acctView(acc, now)
	if err != nil {
		a.pageFailed(w, "account", err)
		return
	}
	a.roomRender(w, r, "account", http.StatusOK, acc.Institution, roomPage{
		"Title":  v.Name,
		"A":      v,
		"Lines":  lines,
		"Count":  len(ls),
		"Page":   page,
		"Older":  start > 0,
		"Newer":  page > 1,
		"Shares": recs,
	})
}

func (a *App) roomLine(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	acc, ok := a.world.Account(r.PathValue("key"))
	if !ok {
		a.roomNotFound(w, r)
		return
	}
	now := a.clock.Now()
	pending := r.URL.Query().Get("pending") == "1"
	l, ok := a.line(acc.Key, r.PathValue("line"), pending, now)
	if !ok {
		a.roomNotFound(w, r)
		return
	}
	lv := a.lineView(acc, l)
	trace := []lineView{}
	var debit, credit, charges int64
	for _, t := range a.trace(l.SessionID, now) {
		tv := a.lineView(t.Account, t.Line)
		trace = append(trace, tv)
		switch {
		case t.Line.Channel == "charge":
			charges += -t.Line.Amount
		case t.Line.Amount < 0:
			debit += -t.Line.Amount
		default:
			credit += t.Line.Amount
		}
	}
	type share struct {
		Partner, AccountID, TxnID, Status string
		Synced                            bool
		Stored                            *Txn
	}
	shares := []share{}
	for _, rec := range a.st.Accounts {
		if rec.WorldKey != acc.Key {
			continue
		}
		id := txnID(rec.PartnerID, acc.Key, l.Key, l.Pending)
		sh := share{AccountID: rec.ID, TxnID: id, Status: "revoked"}
		if p := a.partnerOf(rec.PartnerID); p != nil {
			sh.Partner = p.Name
		}
		if c, _ := a.grantFor(rec); c != nil {
			sh.Status = c.Status
		}
		if f := a.st.Feeds[rec.ID]; f != nil {
			if t, ok := f.Lines[id]; ok {
				sh.Synced = true
				sh.Stored = &t
			}
		}
		shares = append(shares, sh)
	}
	av, err := a.acctView(acc, now)
	if err != nil {
		a.pageFailed(w, "line", err)
		return
	}
	a.roomRender(w, r, "line", http.StatusOK, acc.Institution, roomPage{
		"Title":   lv.Description,
		"L":       lv,
		"A":       av,
		"Trace":   trace,
		"Debit":   debit,
		"Credit":  credit,
		"Charges": charges,
		"Shares":  shares,
		"Flash":   r.URL.Query().Get("done") == "1",
	})
}

func (a *App) transferData(r *http.Request) (roomPage, error) {
	now := a.clock.Now()
	accts := []acctView{}
	for _, acc := range a.world.Accounts() {
		v, err := a.acctView(acc, now)
		if err != nil {
			return nil, err
		}
		accts = append(accts, v)
	}
	return roomPage{"Title": "New transfer", "Accounts": accts, "From": r.URL.Query().Get("from"), "Others": []string{"Bank C", "Bank D Microfinance", "Bank A", "Bank B"}}, nil
}

func (a *App) roomTransferForm(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, err := a.transferData(r)
	if err != nil {
		a.pageFailed(w, "transfer", err)
		return
	}
	a.roomRender(w, r, "transfer", http.StatusOK, "transfer", data)
}

func (a *App) roomTransfer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	amount, err := parseNaira(f.Get("amount"))
	req := world.TransferRequest{From: f.Get("from"), To: f.Get("to"), Narration: strings.TrimSpace(f.Get("narration")), Amount: amount}
	if req.To == "other" {
		req.To = strings.TrimSpace(f.Get("other_number"))
		req.ToBank = f.Get("other_bank")
		req.ToName = strings.TrimSpace(f.Get("other_name"))
	}
	a.mu.Lock()
	var res world.TransferResult
	if err == nil {
		res, err = a.world.Transfer(a.clock.Now(), req)
	}
	if err != nil {
		data, derr := a.transferData(r)
		if derr != nil {
			a.mu.Unlock()
			a.pageFailed(w, "transfer", derr)
			return
		}
		data["From"] = f.Get("from")
		data["To"] = f.Get("to")
		data["Form"] = f
		msg := transferMessage(err)
		if errors.Is(err, world.ErrInsufficientFunds) {
			msg = "The account does not have enough available money for this transfer and its charges."
		}
		data["Error"] = msg
		a.roomRender(w, r, "transfer", http.StatusUnprocessableEntity, "transfer", data)
		a.mu.Unlock()
		return
	}
	a.dirty()
	a.mu.Unlock()
	a.SyncAll()
	http.Redirect(w, r, "/ui/accounts/"+url.PathEscape(res.From)+"/lines/"+url.PathEscape(res.LineKey)+"?done=1", http.StatusSeeOther)
}

func roomForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return false
	}
	return true
}

func (a *App) roomClock(w http.ResponseWriter, r *http.Request) {
	if !roomForm(w, r) {
		return
	}
	if by := r.PostForm.Get("by"); by != "" {
		if d, err := parseDuration(by); err == nil && d > 0 {
			a.mu.Lock()
			_, err := a.clock.Advance(d)
			if err != nil {
				a.mu.Unlock()
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			a.dirty()
			a.mu.Unlock()
			a.Step()
		}
	}
	if p := r.PostForm.Get("pace"); p != "" {
		if v, err := strconv.ParseFloat(p, 64); err == nil {
			if err := a.clock.SetPace(v); err != nil {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			a.mu.Lock()
			a.dirty()
			a.mu.Unlock()
		}
	}
	back := r.PostForm.Get("back")
	if !strings.HasPrefix(back, "/ui") {
		back = "/ui"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
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
		if inst, ok := a.world.Institution(c.Institution); ok {
			x.Bank = inst.Name
		}
		if p, ok := a.world.Persona(c.PersonaID); ok {
			x.Customer = p.Name
		}
		rows = append(rows, x)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	a.roomRender(w, r, "connections", http.StatusOK, "connections", roomPage{"Title": "Connections", "Rows": rows})
}

func (a *App) roomRevoke(w http.ResponseWriter, r *http.Request) {
	if err := a.RevokeAtBank(r.PathValue("id")); err != nil {
		if errors.Is(err, errNotFound) {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.roomNotFound(w, r)
			return
		}
		a.pageFailed(w, "revoke", err)
		return
	}
	a.poke()
	http.Redirect(w, r, "/ui/connections", http.StatusSeeOther)
}

func (a *App) roomWebhooks(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	page, ok := pageParam(w, r, "webhook list", pagesOf(len(a.st.Deliveries), pageSize)+1)
	if !ok {
		return
	}
	end := len(a.st.Deliveries) - (page-1)*pageSize
	if end <= 0 && page > 1 {
		a.roomNotFound(w, r)
		return
	}
	end = max(end, 0)
	start := max(end-pageSize, 0)
	rows := []*Delivery{}
	for i := end - 1; i >= start; i-- {
		rows = append(rows, a.st.Deliveries[i])
	}
	a.roomRender(w, r, "webhooks", http.StatusOK, "webhooks", roomPage{"Title": "Webhooks", "Rows": rows, "Page": page, "Older": start > 0, "Newer": page > 1})
}

func (a *App) roomReplay(w http.ResponseWriter, r *http.Request) {
	if err := a.Replay(r.PathValue("id")); err != nil {
		if errors.Is(err, errNotFound) {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.roomNotFound(w, r)
			return
		}
		a.pageFailed(w, "replay", err)
		return
	}
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
	"isFrozen":   func(p float64) bool { return p == 0 },
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
	world.Institution
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
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	rows := []*bankRow{}
	byID := map[string]*bankRow{}
	for _, inst := range a.world.Institutions() {
		row := &bankRow{Institution: inst, Phase: PhaseActive}
		if st, ok := statuses[inst.ID]; ok {
			row.Phase, row.URL = st.Phase, st.URL
		}
		rows = append(rows, row)
		byID[inst.ID] = row
	}
	for _, st := range a.orderedStatuses(statuses) {
		if byID[st.ID] == nil {
			row := &bankRow{Institution: world.Institution{ID: st.ID, Name: st.Name}, Phase: st.Phase, URL: st.URL}
			rows = append(rows, row)
			byID[st.ID] = row
		}
	}
	owners := map[string]map[string]bool{}
	for _, acc := range a.world.Accounts() {
		row := byID[acc.Institution]
		if row == nil {
			continue
		}
		if owners[acc.Institution] == nil {
			owners[acc.Institution] = map[string]bool{}
		}
		owners[acc.Institution][acc.Owner] = true
		bal, err := a.src(acc.Key).LedgerAt(acc.Key, now)
		if err != nil {
			a.pageFailed(w, "banks", fmt.Errorf("read balance of %s: %w", acc.Key, err))
			return
		}
		row.Deposits += bal
	}
	for id, o := range owners {
		byID[id].Customers = len(o)
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

func (a *App) orderedStatuses(m map[string]BankStatus) []BankStatus {
	out := []BankStatus{}
	for _, b := range a.cfg.Banks {
		if st, ok := m[b.ID]; ok {
			out = append(out, st)
		}
	}
	return out
}

func (a *App) roomOpenAccount(w http.ResponseWriter, r *http.Request) {
	if !roomForm(w, r) {
		return
	}
	id := r.PathValue("id")
	acc, err := a.OpenAccount(id, r.PostForm.Get("persona"), r.PostForm.Get("type"))
	if errors.Is(err, errNotFound) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.roomNotFound(w, r)
		return
	}
	if err != nil {
		http.Redirect(w, r, "/ui/banks/"+url.PathEscape(id)+"?error="+url.QueryEscape(strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": ")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/ui/accounts/"+url.PathEscape(acc.Key), http.StatusSeeOther)
}

func (a *App) roomCloseBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !roomForm(w, r) {
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
	a.mu.Lock()
	defer a.mu.Unlock()
	inst, ok := a.world.Institution(id)
	if err != nil || !ok {
		a.roomNotFound(w, r)
		return
	}
	targets := []world.Institution{}
	for _, b := range a.openInstitutions() {
		if b.ID != id && a.cfg.Bank(b.ID) != nil && !a.world.Down(b.ID, a.clock.Now()) {
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
	if !roomForm(w, r) {
		return
	}
	status := http.StatusOK
	msg := ""
	if r.Method == http.MethodPost {
		var err error
		if strings.TrimSpace(r.PostForm.Get("confirm")) != id {
			err = invalid("type %s to confirm", id)
		} else {
			_, err = a.PurgeBank(id)
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
	inst, ok := a.world.Institution(id)
	if err != nil || !ok {
		a.roomNotFound(w, r)
		return
	}
	a.roomRender(w, r, "purge_bank", status, "banks", roomPage{"Title": "Purge " + inst.Name, "Bank": inst, "Impact": imp, "Error": msg})
}
