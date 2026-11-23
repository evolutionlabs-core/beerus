package contracttest

import (
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"net/http"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func rawGet(t *testing.T, s *Stack, key, path string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", s.cfg.PartnerURL()+path, nil)
	req.Header.Set("X-Api-Key", key)
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
	conn := s.connect("tunde", "bank-a", s.today().AddDays(-10))
	oldKey := s.apiKey

	otherKey := fmt.Sprint(s.ctl("POST", "/partners", map[string]any{"id": "ledgerly", "redirect_uris": []string{"https://ledgerly.localhost/cb"}})["api_key"])
	if _, err := s.ctlTry("POST", "/partners", map[string]any{"id": "ledgerly", "redirect_uris": []string{"https://x.localhost/cb"}}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a duplicate partner was accepted: %v", err)
	}
	if _, err := s.ctlTry("POST", "/partners", map[string]any{"id": "Bad ID", "redirect_uris": []string{"https://x.localhost/cb"}}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("an invalid partner ID was accepted: %v", err)
	}
	if code := rawGet(t, s, otherKey, "/v1/webhook_keys"); code != 200 {
		t.Fatalf("a newly added partner got %d", code)
	}
	if code := rawGet(t, s, otherKey, "/v1/connections/"+conn.ConnectionID); code != 404 {
		t.Fatalf("another partner could see Balance's connection: %d", code)
	}
	if code := rawGet(t, s, s.apiKey, "/v1/connections/"+conn.ConnectionID); code != 200 {
		t.Fatalf("Balance could not see its own connection: %d", code)
	}

	newKey := fmt.Sprint(s.ctl("POST", "/partners/balance/rotate-key", nil)["api_key"])
	if code := rawGet(t, s, oldKey, "/v1/webhook_keys"); code == 200 {
		t.Fatal("the old key still works after rotation")
	}
	if code := rawGet(t, s, oldKey, "/v1/webhook_keys"); code != 401 {
		t.Fatalf("the old key after rotation got %d", code)
	}
	if code := rawGet(t, s, newKey, "/v1/connections/"+conn.ConnectionID); code != 200 {
		t.Fatalf("the new credentials got %d", code)
	}

	s.ctl("DELETE", "/partners/ledgerly", nil)
	saved, err := setup.LoadConfig(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Partner("ledgerly") != nil || saved.Partner("balance").APIKeySHA256 != config.HashKey(newKey) {
		t.Fatal("the partner changes were not saved")
	}
	if code := rawGet(t, s, otherKey, "/v1/webhook_keys"); code == 200 {
		t.Fatal("a removed partner still has access")
	}
}
