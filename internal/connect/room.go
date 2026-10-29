package connect

import (
	"crypto/subtle"
	"errors"
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
	mux.HandleFunc("GET /ui/banks/{id}", a.roomBank)
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
	data["Banks"] = a.world.Institutions()
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

type acctView struct {
	world.Account
	Bank      world.Institution
	Holder    string
	Ledger    int64
	Available int64
	Pending   int
}

func (a *App) acctView(acc world.Account, at time.Time) acctView {
	v := acctView{Account: acc}
	v.Bank, _ = a.world.Institution(acc.Institution)
	if p, ok := a.world.Persona(acc.Owner); ok {
		v.Holder = p.Name
	}
	v.Ledger, _ = a.src(acc.Key).LedgerAt(acc.Key, at)
	pend, _ := a.src(acc.Key).PendingAt(acc.Key, at)
	v.Available = v.Ledger
	if pend < 0 {
		v.Available += pend
	}
	ls, _ := a.src(acc.Key).Fetch(acc.Key, at, clock.DateOf(at).AddDays(-3))
	for _, l := range ls {
		if l.Pending {
			v.Pending++
		}
	}
	return v
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
		bal, _ := a.src(acc.Key).LedgerAt(acc.Key, now)
		s.Deposits += bal
		ls, _ := a.src(acc.Key).Fetch(acc.Key, now, clock.DateOf(now).AddDays(-14))
		if len(ls) > 6 {
			ls = ls[len(ls)-6:]
		}
		for _, l := range ls {
			recent = append(recent, a.lineView(acc, l))
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].At.After(recent[j].At) })
	if len(recent) > 14 {
		recent = recent[:14]
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
		"Connections": len(a.st.Connections),
		"Active":      active,
		"WorldID":     a.st.WorldID,
		"Deliveries":  len(a.st.Deliveries),
	})
}

func (a *App) roomBank(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	inst, ok := a.world.Institution(r.PathValue("id"))
	if !ok {
		a.roomNotFound(w, r)
		return
	}
	now := a.clock.Now()
	accts := []acctView{}
	var total int64
	for _, acc := range a.world.Accounts() {
		if acc.Institution == inst.ID {
			v := a.acctView(acc, now)
			total += v.Ledger
			accts = append(accts, v)
		}
	}
	a.roomRender(w, r, "bank", http.StatusOK, inst.ID, roomPage{"Title": inst.Name, "Bank": inst, "Accounts": accts, "Total": total, "Down": a.world.Down(inst.ID, now)})
}

const pageSize = 50

func (a *App) roomAccount(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	acc, ok := a.world.Account(r.PathValue("key"))
	if !ok {
		a.roomNotFound(w, r)
		return
	}
	now := a.clock.Now()
	ls, _ := a.statement(acc.Key, now)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
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
	v := a.acctView(acc, now)
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
	a.roomRender(w, r, "line", http.StatusOK, acc.Institution, roomPage{
		"Title":   lv.Description,
		"L":       lv,
		"A":       a.acctView(acc, now),
		"Trace":   trace,
		"Debit":   debit,
		"Credit":  credit,
		"Charges": charges,
		"Shares":  shares,
		"Flash":   r.URL.Query().Get("done") == "1",
	})
}

func (a *App) transferData(r *http.Request) roomPage {
	now := a.clock.Now()
	accts := []acctView{}
	for _, acc := range a.world.Accounts() {
		accts = append(accts, a.acctView(acc, now))
	}
	return roomPage{"Title": "New transfer", "Accounts": accts, "From": r.URL.Query().Get("from"), "Others": []string{"Bank C", "Bank D Microfinance", "Bank A", "Bank B"}}
}

func (a *App) roomTransferForm(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roomRender(w, r, "transfer", http.StatusOK, "transfer", a.transferData(r))
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
		data := a.transferData(r)
		data["From"] = f.Get("from")
		data["To"] = f.Get("to")
		data["Form"] = f
		msg := err.Error()
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

func (a *App) roomClock(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if by := r.PostForm.Get("by"); by != "" {
		if d, err := parseDuration(by); err == nil && d > 0 {
			a.mu.Lock()
			_, _ = a.clock.Advance(d)
			a.dirty()
			a.mu.Unlock()
			a.Step()
		}
	}
	if p := r.PostForm.Get("pace"); p != "" {
		if v, err := strconv.ParseFloat(p, 64); err == nil {
			_ = a.clock.SetPace(v)
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
	"isFrozen": func(p float64) bool { return p == 0 },
	"list":     func(v ...any) []any { return v },
}).Parse(roomTemplates))
