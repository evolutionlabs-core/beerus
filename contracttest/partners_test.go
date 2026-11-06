package contracttest

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
)

func partnerClient(t *testing.T, s *Stack, id string) *http.Client {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(s.cfg.Path("partners", id, "client.crt"), s.cfg.Path("partners", id, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: s.rootPool()}}}
}

func rawGet(t *testing.T, s *Stack, c *http.Client, key, path string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", s.cfg.PartnerURL()+path, nil)
	req.Header.Set("X-Api-Key", key)
	resp, err := c.Do(req)
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
	oldClient := partnerClient(t, s, "balance")
	oldKey := s.apiKey

	otherKey := fmt.Sprint(s.ctl("POST", "/partners", map[string]any{"id": "ledgerly", "redirect_uris": []string{"https://ledgerly.localhost/cb"}})["api_key"])
	if _, err := s.ctlTry("POST", "/partners", map[string]any{"id": "ledgerly", "redirect_uris": []string{"https://x.localhost/cb"}}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a duplicate partner was accepted: %v", err)
	}
	if _, err := s.ctlTry("POST", "/partners", map[string]any{"id": "Bad ID", "redirect_uris": []string{"https://x.localhost/cb"}}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("an invalid partner ID was accepted: %v", err)
	}
	other := partnerClient(t, s, "ledgerly")
	if code := rawGet(t, s, other, otherKey, "/v1/webhook_keys"); code != 200 {
		t.Fatalf("a newly added partner got %d", code)
	}
	if code := rawGet(t, s, other, otherKey, "/v1/connections/"+conn.ConnectionID); code != 404 {
		t.Fatalf("another partner could see Balance's connection: %d", code)
	}
	if code := rawGet(t, s, other, s.apiKey, "/v1/webhook_keys"); code != 403 {
		t.Fatalf("Balance's key with another partner's certificate got %d", code)
	}

	newKey := fmt.Sprint(s.ctl("POST", "/partners/balance/rotate-key", nil)["api_key"])
	if code := rawGet(t, s, oldClient, oldKey, "/v1/webhook_keys"); code == 200 {
		t.Fatal("the old key and certificate still work after rotation")
	}
	if code := rawGet(t, s, partnerClient(t, s, "balance"), oldKey, "/v1/webhook_keys"); code != 401 {
		t.Fatalf("the old key with the new certificate got %d", code)
	}
	if code := rawGet(t, s, partnerClient(t, s, "balance"), newKey, "/v1/connections/"+conn.ConnectionID); code != 200 {
		t.Fatalf("the new credentials got %d", code)
	}

	s.ctl("DELETE", "/partners/ledgerly", nil)
	saved, err := connect.LoadConfig(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Partner("ledgerly") != nil || saved.Partner("balance").APIKeySHA256 != config.HashKey(newKey) {
		t.Fatal("the partner changes were not saved")
	}
	if code := rawGet(t, s, other, otherKey, "/v1/webhook_keys"); code == 200 {
		t.Fatal("a removed partner still has access")
	}
}
