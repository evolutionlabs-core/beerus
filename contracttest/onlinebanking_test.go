package contracttest

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

type bankBrowser struct {
	t    *testing.T
	base string
	c    *http.Client
	last string
	loc  string
}

func (s *Stack) bankBrowser() *bankBrowser {
	jar, _ := cookiejar.New(nil)
	return &bankBrowser{t: s.t, base: s.cfg.Bank("bank-a").URL(), c: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (b *bankBrowser) do(req *http.Request) {
	b.t.Helper()
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b.last = string(body)
	b.loc = resp.Request.URL.Path
}

func (b *bankBrowser) get(path string) {
	b.t.Helper()
	req, _ := http.NewRequest("GET", b.base+path, nil)
	b.do(req)
}

var reBankCSRF = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (b *bankBrowser) post(path string, form url.Values) {
	b.t.Helper()
	if m := reBankCSRF.FindStringSubmatch(b.last); m != nil && form.Get("csrf") == "" {
		form.Set("csrf", m[1])
	}
	req, _ := http.NewRequest("POST", b.base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.base)
	b.do(req)
}

func (s *Stack) bankSignIn(persona string) *bankBrowser {
	s.t.Helper()
	b := s.bankBrowser()
	p := s.persona(persona)
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	m := regexp.MustCompile(`verification code is <span[^>]*>(\d{6})</span>`).FindStringSubmatch(b.last)
	if m == nil {
		s.t.Fatalf("no code on the verify page: %.300s", b.last)
	}
	b.post("/verify", url.Values{"otp": {m[1]}})
	if b.loc != "/accounts" {
		s.t.Fatalf("sign-in ended at %s", b.loc)
	}
	return b
}

func TestOnlineBanking(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	conn := s.connect("adaeze", "bank-a", s.today().AddDays(-20))
	current := fmt.Sprint(accountAt(t, conn, "current")["account_number"])
	b := s.bankSignIn("adaeze")
	if !strings.Contains(b.last, current) {
		t.Fatal("accounts page does not list the current account")
	}
	b.get("/accounts/" + current)
	if !strings.Contains(b.last, "NIP CHARGE") {
		t.Fatal("statement has no fee lines")
	}
	chidi := ""
	for _, p := range s.ctl("GET", "/personas", nil)["data"].([]any) {
		m := p.(map[string]any)
		if m["id"] == "chidi" {
			chidi = fmt.Sprint(m["accounts"].([]any)[0].(map[string]any)["nuban"])
		}
	}
	b.get("/transfer")
	form := url.Values{"from": {current}, "mode": {"other"}, "number": {chidi}, "bank": {"Bank B"}, "amount": {"12000"}, "narration": {"Online test"}}
	b.post("/transfer/review", form)
	if !strings.Contains(b.last, "Chidi Okafor") || !strings.Contains(b.last, "₦12,076.88") {
		t.Fatalf("review page: %.600s", b.last)
	}
	form.Set("csrf", "")
	b.post("/transfer/confirm", form)
	if !strings.Contains(b.last, "Transfer successful") {
		t.Fatalf("confirm page: %.400s", b.last)
	}
	s.syncNow()
	id := fmt.Sprint(accountAt(t, conn, "current")["id"])
	st, _ := s.syncAll(id, "", 500)
	found := false
	for _, l := range st.lines {
		if l["description_raw"] == "Online test" && num(l["amount_minor"]) == -1200000 {
			found = true
		}
	}
	if !found {
		t.Fatal("the online banking transfer did not reach the partner feed")
	}
	c, err := s.app.VerifyBank()
	if err != nil || !c.Ledger.OK() || len(c.Mismatches) > 0 {
		t.Fatalf("bank ledger after online transfer: %v %v %v", err, c.Ledger, c.Mismatches)
	}
	b.get("/apps")
	if !strings.Contains(b.last, "Balance") || !strings.Contains(b.last, "via Beerus Connect") {
		t.Fatalf("connected apps page: %.400s", b.last)
	}
	b.post("/apps/"+conn.ConnectionID+"/revoke", url.Values{})
	if !strings.Contains(b.last, "Access withdrawn") {
		t.Fatalf("revoke page: %.300s", b.last)
	}
	got := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if got.JSON["status"] != "revoked" {
		t.Fatalf("status after revoke in online banking: %v", got.JSON["status"])
	}
	if len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == conn.ConnectionID && d["status"] == "revoked"
	})) == 0 {
		t.Fatal("no revoked webhook")
	}
	other := s.bankBrowser()
	other.get("/accounts")
	if other.loc != "/login" {
		t.Fatal("accounts page served without signing in")
	}
}

func TestEveryDeclaredBankHasOnlineBanking(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	jar, _ := cookiejar.New(nil)
	b := &bankBrowser{t: t, base: s.cfg.Bank("bank-b").URL(), c: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	p := s.persona("ngozi")
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	m := regexp.MustCompile(`verification code is <span[^>]*>(\d{6})</span>`).FindStringSubmatch(b.last)
	if m == nil {
		t.Fatalf("no code on Bank B's verify page: %.300s", b.last)
	}
	b.post("/verify", url.Values{"otp": {m[1]}})
	if b.loc != "/accounts" || !strings.Contains(b.last, "Bank B") {
		t.Fatalf("Bank B sign-in ended at %s", b.loc)
	}
	man, err := s.cfg.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	dbs := 0
	for _, r := range man.Resources {
		if r.Kind == "database" {
			dbs++
		}
	}
	if dbs != 2 {
		t.Fatalf("manifest lists %d bank databases: %+v", dbs, man.Resources)
	}
}
