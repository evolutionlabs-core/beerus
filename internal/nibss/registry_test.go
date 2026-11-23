package nibss

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func client(id, cn string) openbanking.RegistryClient {
	return openbanking.RegistryClient{ID: id, Name: id, LegalName: id + " Ltd", CAC: "RC1", Role: "aggregator", CertCN: cn, JWK: openbanking.JWK{Kty: "EC", Crv: "P-256", X: "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU", Y: "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0", Kid: id}, RedirectURIs: []string{"https://" + id + ".localhost/cb"}, RegisteredAt: t0}
}

func TestEnsureClient(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	first, err := s.EnsureClient(ctx, client("OBR-1", "connect"), false)
	if err != nil || first.RegistryID != "OBR-1" || !first.RegisteredAt.Equal(t0) {
		t.Fatalf("first ensure: %+v, %v", first, err)
	}
	moved := client("OBR-1", "connect")
	moved.RegisteredAt = t0.Add(time.Hour)
	moved.Name = "Renamed"
	moved.RedirectURIs = []string{"https://new.localhost/cb", "http://127.0.0.1:9/cb"}
	again, err := s.EnsureClient(ctx, moved, false)
	if err != nil || again.Name != "Renamed" || len(again.RedirectURIs) != 2 || !again.RegisteredAt.Equal(t0) {
		t.Fatalf("repeat ensure: %+v, %v", again, err)
	}
	_, err = s.EnsureClient(ctx, client("OBR-1", "connect"), true)
	wantErr(t, err, "client OBR-1 is already registered")
	for _, c := range []struct {
		c    openbanking.RegistryClient
		want string
	}{
		{func() openbanking.RegistryClient { c := client("OBR-1", "other"); return c }(), "client OBR-1 is registered with cert_cn connect, and this request has cert_cn other"},
		{client("OBR-2", "connect"), "cert_cn connect already belongs to client OBR-1"},
	} {
		_, err := s.EnsureClient(ctx, c.c, false)
		var conflict *RegistryConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("conflict for %s: %v", c.c.ID, err)
		}
		wantErr(t, err, c.want)
	}
	for _, c := range []struct {
		edit func(*openbanking.RegistryClient)
		want string
	}{
		{func(c *openbanking.RegistryClient) { c.ID = "-x" }, `client id "-x" must be letters, digits, dots, dashes and underscores, up to 40 characters`},
		{func(c *openbanking.RegistryClient) { c.Name = "" }, "client OBR-3 needs a name and a legal_name"},
		{func(c *openbanking.RegistryClient) { c.CertCN = "" }, "client OBR-3 needs a cert_cn: the ID it sends in the Beerus-Caller header"},
		{func(c *openbanking.RegistryClient) { c.Role = "bank" }, `client OBR-3 role must be aggregator, not "bank"`},
		{func(c *openbanking.RegistryClient) { c.RedirectURIs = nil }, "client OBR-3 needs at least one redirect URI"},
		{func(c *openbanking.RegistryClient) { c.RedirectURIs = []string{"http://other.example.com/cb"} }, `redirect URI "http://other.example.com/cb" must be an https URL, or an http URL on localhost`},
	} {
		c3 := client("OBR-3", "three")
		c.edit(&c3)
		_, err := s.EnsureClient(ctx, c3, false)
		wantErr(t, err, c.want)
	}
}

func TestClientsPaging(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for _, id := range []string{"OBR-3", "OBR-1", "OBR-2"} {
		if _, err := s.EnsureClient(ctx, client(id, "cn-"+id), false); err != nil {
			t.Fatal(err)
		}
	}
	page, next, err := s.Clients(ctx, 2, "")
	if err != nil || len(page) != 2 || page[0].ID != "OBR-1" || page[1].ID != "OBR-2" || next != "OBR-2" {
		t.Fatalf("page 1: %+v, %q, %v", page, next, err)
	}
	page, next, err = s.Clients(ctx, 2, next)
	if err != nil || len(page) != 1 || page[0].ID != "OBR-3" || next != "" {
		t.Fatalf("page 2: %+v, %q, %v", page, next, err)
	}
	_, _, err = s.Clients(ctx, 2, "-bad")
	wantErr(t, err, `cursor "-bad" is not valid; use the next value from the last page`)
	got, err := s.Client(ctx, "OBR-2")
	if err != nil || got.CertCN != "cn-OBR-2" {
		t.Fatalf("client: %+v, %v", got, err)
	}
	_, err = s.Client(ctx, "OBR-9")
	wantErr(t, err, "client OBR-9 is unknown registry client")
}
