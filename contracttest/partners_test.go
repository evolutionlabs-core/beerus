package contracttest

import (
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"net/http"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func rawGet(t *testing.T, s *Stack, clientID, secret, path string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", s.cfg.PartnerURL()+path, nil)
	req.Header.Set(config.ClientIDHeader, clientID)
	req.Header.Set(config.SecretHeader, secret)
	resp, err := s.raw.Do(req)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPartnerLifecycle(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	conn := s.connect("tunde", "bank-a")
	oldSecret := s.secret

	otherSecret := fmt.Sprint(s.ctl("POST", "/partners", map[string]any{"id": "ledgerly", "redirect_uris": []string{"https://ledgerly.localhost/cb"}})["secret"])
	if _, err := s.ctlTry("POST", "/partners", map[string]any{"id": "ledgerly", "redirect_uris": []string{"https://x.localhost/cb"}}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a duplicate partner was accepted: %v", err)
	}
	if _, err := s.ctlTry("POST", "/partners", map[string]any{"id": "Bad ID", "redirect_uris": []string{"https://x.localhost/cb"}}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("an invalid partner ID was accepted: %v", err)
	}
	if code := rawGet(t, s, "ledgerly", otherSecret, "/v1/webhook_keys"); code != 200 {
		t.Fatalf("a newly added partner got %d", code)
	}
	if code := rawGet(t, s, "ledgerly", otherSecret, "/v1/connections/"+conn.ConnectionID); code != 404 {
		t.Fatalf("another partner could see Balance's connection: %d", code)
	}
	if code := rawGet(t, s, "balance", s.secret, "/v1/connections/"+conn.ConnectionID); code != 200 {
		t.Fatalf("Balance could not see its own connection: %d", code)
	}

	newSecret := fmt.Sprint(s.ctl("POST", "/partners/balance/rotate-key", nil)["secret"])
	if code := rawGet(t, s, "balance", oldSecret, "/v1/webhook_keys"); code == 200 {
		t.Fatal("the old secret still works after rotation")
	}
	if code := rawGet(t, s, "balance", oldSecret, "/v1/webhook_keys"); code != 401 {
		t.Fatalf("the old secret after rotation got %d", code)
	}
	if code := rawGet(t, s, "balance", newSecret, "/v1/connections/"+conn.ConnectionID); code != 200 {
		t.Fatalf("the new secret got %d", code)
	}

	s.ctl("DELETE", "/partners/ledgerly", nil)
	saved, err := setup.LoadConfig(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Partner("ledgerly") != nil || saved.Partner("balance").SecretSHA256 != config.HashKey(newSecret) {
		t.Fatal("the partner changes were not saved")
	}
	if code := rawGet(t, s, "ledgerly", otherSecret, "/v1/webhook_keys"); code == 200 {
		t.Fatal("a removed partner still has access")
	}
}
