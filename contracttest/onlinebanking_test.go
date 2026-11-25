package contracttest

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestOnlineBanking(t *testing.T) {
	s := sharedStack(t, 0)
	s.needsNodes()
	conn := s.connect("adaeze", "bank-a")
	current := fmt.Sprint(accountAt(t, conn, "current")["account_number"])
	b := s.bankSignIn("adaeze")
	if !strings.Contains(b.last, current) {
		t.Fatal("accounts page does not list the current account")
	}
	b.get("/accounts/" + current)
	if !strings.Contains(b.last, "1,250,000.00") {
		t.Fatalf("statement does not show the opening deposit: %.600s", b.last)
	}
	chidi := s.persona("chidi").Accounts[0]
	b.get("/transfer")
	form := url.Values{"from": {current}, "mode": {"other"}, "number": {chidi}, "bank": {"bank-b"}, "amount": {"12000"}, "narration": {"Online test"}}
	b.post("/transfer/review", form)
	if !strings.Contains(b.last, "Chidi Okafor") || !strings.Contains(b.last, "₦12,060.75") {
		t.Fatalf("review page: %.600s", b.last)
	}
	form.Set("csrf", "")
	form.Set("key", regexp.MustCompile(`name="key" value="([^"]+)"`).FindStringSubmatch(b.last)[1])
	b.post("/transfer/confirm", form)
	if b.loc != "/accounts/"+current || !strings.Contains(b.last, "Transfer sent. Both banks have posted it.") || !strings.Contains(b.last, "NIP CHARGE") {
		t.Fatalf("after confirm at %s: %.600s", b.loc, b.last)
	}
	s.syncNow()
	id := fmt.Sprint(accountAt(t, conn, "current")["id"])
	st, _ := s.syncAll(conn.AccessToken, id, "", 500)
	found := false
	for _, l := range st.lines {
		if strings.Contains(fmt.Sprint(l["description_raw"]), "Online test") && num(l["amount_minor"]) == -1200000 {
			found = true
		}
	}
	if !found {
		t.Fatal("the online banking transfer did not reach the partner feed")
	}
	c, err := s.app.VerifyBank(context.Background())
	if err != nil || !c.OK() {
		t.Fatalf("bank ledger after online transfer: %v %v", err, c)
	}
	other := s.bankBrowserAt("bank-a")
	other.get("/accounts")
	if other.loc != "/login" {
		t.Fatal("accounts page served without signing in")
	}
	if b := s.signInAt("bank-b", "ngozi"); !strings.Contains(b.last, "Bank B") {
		t.Fatalf("Bank B sign-in: %.300s", b.last)
	}
}
