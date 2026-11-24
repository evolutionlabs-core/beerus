package nibss

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func testBaseURL() string {
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "host=/tmp dbname=postgres"
}

func scratchPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "n_" + hex.EncodeToString(b)
	cfg, err := pgxpool.ParseConfig(testBaseURL())
	must(t, err)
	cfg.MaxConns = 16
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema+`; SET search_path TO `+schema)
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	if err := pool.Ping(t.Context()); err != nil {
		t.Skipf("no Postgres for NIBSS tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DROP SCHEMA `+schema+` CASCADE`)
		pool.Close()
	})
	return pool
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func testStore(t testing.TB) *Store {
	t.Helper()
	s, err := newStore(context.Background(), scratchPool(t))
	must(t, err)
	return s
}

func wantErr(t testing.TB, err error, msg string) {
	t.Helper()
	if err == nil || err.Error() != msg {
		t.Fatalf("got error %v, want %q", err, msg)
	}
}

func member(id, name, code, nuban string) Member {
	return Member{Institution: nip.Institution{ID: id, Name: name, NIPCode: code, NUBANCode: nuban, Type: "commercial"}}
}

func register(t testing.TB, s *Store, id, name, code, nuban string) {
	t.Helper()
	must(t, s.Register(context.Background(), member(id, name, code, nuban)))
}

func setStatus(t testing.TB, s *Store, id string, statuses ...string) {
	t.Helper()
	for _, st := range statuses {
		must(t, s.SetStatus(context.Background(), id, st))
	}
}

func pages(t testing.TB, name string, list func(limit int, after string) ([]string, string, error), want ...string) {
	t.Helper()
	if got, next, err := list(len(want), ""); err != nil || next != "" || !slices.Equal(got, want) {
		t.Errorf("%s whole list: %q, next %q, %v, want %q", name, got, next, err, want)
	}
	var got []string
	for next, n := "", 0; n == 0 || next != ""; n++ {
		page, nx, err := list(1, next)
		if err != nil || n > len(want) {
			t.Fatalf("%s page %d: %v", name, n, err)
		}
		got, next = append(got, page...), nx
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s pages: %q, want %q", name, got, want)
	}
	_, _, err := list(1, "~bad")
	wantErr(t, err, `cursor "~bad" is not valid; use the next value from the last page`)
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	_, err := Open(ctx, testBaseURL(), "nibss")
	wantErr(t, err, `database name "nibss" must look like beerus_something`)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "beerus_nibss_t" + hex.EncodeToString(b)
	t.Cleanup(func() {
		if c, err := pgx.Connect(context.WithoutCancel(ctx), testBaseURL()); err == nil {
			_, _ = c.Exec(ctx, `DROP DATABASE IF EXISTS `+name)
			c.Close(ctx)
		}
	})
	for i := range 2 {
		s, err := Open(ctx, testBaseURL(), name)
		if err != nil {
			t.Skipf("no Postgres for NIBSS tests (%v); set BEERUS_TEST_DATABASE_URL", err)
		}
		if i == 0 {
			register(t, s, "bank-a", "Bank A", "999001", "901")
		}
		dir, err := s.Directory(ctx)
		s.Close()
		if err != nil || len(dir) != 1 || dir[0].ID != "bank-a" {
			t.Fatalf("open %d: directory %+v, %v", i, dir, err)
		}
	}
}

func TestDirectory(t *testing.T) {
	ctx := context.Background()
	pool := scratchPool(t)
	s, err := newStore(ctx, pool)
	must(t, err)
	if dir, err := s.Directory(ctx); err != nil || len(dir) != 0 {
		t.Fatalf("new directory: %+v, %v", dir, err)
	}
	register(t, s, "bank-c", "Bank C", "999003", "903")
	register(t, s, "bank-a", "Bank A", "999001", "901")
	register(t, s, "bank-b", "Bank B", "999002", "902")
	setStatus(t, s, "bank-b", nip.StatusTerminating, nip.StatusRemoved)
	again, err := newStore(ctx, pool)
	must(t, err)
	dir, err := again.Directory(ctx)
	inst := func(id, name, code, nuban, status string) nip.Institution {
		return nip.Institution{ID: id, Name: name, NIPCode: code, NUBANCode: nuban, Type: "commercial", Status: status}
	}
	want := []nip.Institution{inst("bank-a", "Bank A", "999001", "901", nip.StatusActive), inst("bank-b", "Bank B", "999002", "902", nip.StatusRemoved), inst("bank-c", "Bank C", "999003", "903", nip.StatusActive)}
	if err != nil || !slices.Equal(dir, want) {
		t.Fatalf("directory: %+v, %v", dir, err)
	}
	pages(t, "institutions", func(limit int, after string) ([]string, string, error) {
		got, next, err := s.Institutions(ctx, limit, after)
		ids := []string{}
		for _, in := range got {
			ids = append(ids, in.ID)
		}
		return ids, next, err
	}, "bank-a", "bank-b", "bank-c")
	_, err = institution(ctx, s.pool, "123456", "")
	if !errors.Is(err, nip.ErrUnknownInstitution) {
		t.Fatalf("got %v, want nip.ErrUnknownInstitution", err)
	}
	wantErr(t, err, "bank code 123456 is not in the NIBSS directory")
	for i, st := range []struct{ to, want string }{
		{nip.StatusSuspended, ""}, {nip.StatusActive, ""}, {nip.StatusActive, ""},
		{nip.StatusRemoved, "institution bank-a is active and cannot become removed: status change not allowed"},
		{nip.StatusTerminating, ""},
		{nip.StatusRemoved, ""},
		{nip.StatusActive, "institution bank-a is removed and cannot become active: status change not allowed"},
		{"closed", `unknown institution status "closed"`},
	} {
		if err := s.SetStatus(ctx, "bank-a", st.to); st.want == "" && err != nil || st.want != "" && (err == nil || err.Error() != st.want) {
			t.Fatalf("step %d to %s: %v, want %q", i, st.to, err, st.want)
		}
	}
	err = s.SetStatus(ctx, "bank-x", nip.StatusActive)
	if !errors.Is(err, nip.ErrUnknownInstitution) {
		t.Fatalf("got %v, want nip.ErrUnknownInstitution", err)
	}
	wantErr(t, err, "bank id bank-x is not in the NIBSS directory")
}

func TestRegister(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	register(t, s, "bank-a", "Member A", "999001", "901")
	register(t, s, "bank-gone", "Gone Bank", "999009", "909")
	setStatus(t, s, "bank-gone", nip.StatusTerminating, nip.StatusRemoved)
	a := member("bank-a", "Member A", "999001", "901")
	must(t, s.Register(ctx, a))
	setStatus(t, s, "bank-a", nip.StatusSuspended)
	rebrand := a
	rebrand.Name, rebrand.Type = "Member A Plc", "digital"
	must(t, s.Register(ctx, rebrand))
	if got, _ := institution(ctx, s.pool, "999001", ""); got.Name != "Member A Plc" || got.Type != "digital" || got.Status != nip.StatusSuspended {
		t.Fatalf("rebrand: %+v", got)
	}
	if dir, _ := s.Directory(ctx); len(dir) != 2 {
		t.Fatalf("directory %d rows, want 2", len(dir))
	}
	suspended := member("bank-x", "X", "999031", "931")
	suspended.Status = nip.StatusSuspended
	held := ": code or name already used"
	for _, c := range []struct {
		in   Member
		want string
	}{
		{member("bank-a", "Member A", "999011", "901"), "bank bank-a is registered with nip_code 999001, and this request has nip_code 999011"},
		{member("bank-x", "Member X", "999001", "931"), "bank bank-x: nip_code 999001 is held by bank-a" + held},
		{member("bank-x", "member a plc", "999031", "931"), `bank bank-x: name "member a plc" is held by bank-a` + held},
		{member("bank-x", "Member X", "999031", "901"), "bank bank-x: nuban_code 901 is held by bank-a" + held},
		{member("bank-x", "Member X", "999009", "931"), "bank bank-x: nip_code 999009 is held by removed bank bank-gone" + held},
		{member("bank-gone", "Gone Bank", "999009", "909"), "bank bank-gone was removed, and its id stays reserved" + held},
		{member("bank-x", " ", "999031", "931"), "a bank needs an id, a name and a type"},
		{member("bank-x", "X", "99913", "931"), `bank bank-x: nip_code "99913" must be 6 digits`},
		{member("bank-x", "X", "999031", "93"), `bank bank-x: nuban_code "93" must be 3, 5 or 6 digits`},
		{suspended, "bank bank-x registers as active, not suspended; use SetStatus to change it"},
	} {
		wantErr(t, s.Register(ctx, c.in), c.want)
	}
	must(t, s.Register(ctx, member("bank-y", "Gone Bank", "999032", "932")))

	limits := func() Limits {
		var l Limits
		must(t, s.pool.QueryRow(ctx, `SELECT net_debit_cap_minor, collateral_minor FROM institutions WHERE id = 'bank-a'`).Scan(&l.NetDebitCap, &l.Collateral))
		return l
	}
	for i, l := range []Limits{{500_000_000, 7}, {900_000_000, 0}} {
		a.Limits = l
		if err := s.Register(ctx, a); err != nil || limits() != l {
			t.Fatalf("limits step %d: %+v, %v", i, limits(), err)
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE institutions SET net_debit_cap_minor = NULL WHERE id = 'bank-a'`)
	must(t, err)
	if err := s.Register(ctx, a); err != nil || limits() != a.Limits {
		t.Fatalf("a missing cap after a register again: %+v, %v", limits(), err)
	}
	a.Limits = Limits{NetDebitCap: -1}
	wantErr(t, s.Register(ctx, a), "bank bank-a: net_debit_cap_minor and collateral_minor must not be negative")

	register(t, s, "bank-k", "Member K", "999041", "941")
	contact := func(url, identity string) Member {
		m := member("bank-k", "Member K", "999041", "941")
		m.CreditURL, m.CallerID = url, identity
		return m
	}
	one, two := "http://k.localhost:1", "http://k.localhost:2"
	for _, c := range []struct {
		in   Member
		want string
	}{
		{contact(one, "bank-k"), ""},
		{contact(one, "bank-k"), ""},
		{contact(two, "bank-k"), "bank bank-k is registered with credit_url " + one + ", and this request has credit_url " + two},
	} {
		err := s.Register(ctx, c.in)
		var conflict *RegistrationConflictError
		if c.want == "" && err != nil || c.want != "" && (!errors.As(err, &conflict) || err.Error() != c.want) {
			t.Fatalf("contact %v: got %v, want %q", c.in.CallerID, err, c.want)
		}
	}
	if got, err := s.Member(ctx, "bank-k"); err != nil || got.CreditURL != one || got.CallerID != "bank-k" {
		t.Fatalf("member: %+v, %v", got, err)
	}
	for identity, want := range map[string]bool{"bank-k": true, "bank-z": false} {
		if ok, err := s.Admits(ctx, identity); err != nil || ok != want {
			t.Fatalf("admits %s: %v, %v, want %v", identity, ok, err, want)
		}
	}
	setStatus(t, s, "bank-k", nip.StatusTerminating, nip.StatusRemoved)
	if ok, err := s.Admits(ctx, "bank-k"); err != nil || ok {
		t.Fatalf("a removed bank is admitted: %v, %v", ok, err)
	}
	_, err = s.Member(ctx, "bank-x")
	wantErr(t, err, "bank id bank-x is not in the NIBSS directory")
}

func client(id, cn string) openbanking.RegistryClient {
	return openbanking.RegistryClient{ID: id, Name: id, LegalName: id + " Ltd", CAC: "RC1", Role: "aggregator", CertCN: cn, JWK: openbanking.JWK{Kty: "EC", Crv: "P-256", X: "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU", Y: "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0", Kid: id}, RedirectURIs: []string{"https://" + id + ".localhost/cb"}, RegisteredAt: t0}
}

func consent(bank, id string, src int64, at time.Time) ConsentEvent {
	return ConsentEvent{Bank: bank, ConsentID: id, Incarnation: "db1", ClientID: "client-1", PartnerID: "balance", Status: "Authorised", Via: "bank", ChangedAt: at, SourceSeq: src}
}

func TestRegistryClients(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	first, err := s.EnsureClient(ctx, client("OBR-1", "connect"), false)
	if err != nil || first.RegistryID != "OBR-1" || !first.RegisteredAt.Equal(t0) {
		t.Fatalf("first ensure: %+v, %v", first, err)
	}
	moved := client("OBR-1", "connect")
	moved.RegisteredAt, moved.Name, moved.RedirectURIs = t0.Add(time.Hour), "Renamed", []string{"https://new.localhost/cb", "http://127.0.0.1:9/cb"}
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
		{client("OBR-1", "other"), "client OBR-1 is registered with cert_cn connect, and this request has cert_cn other"},
		{client("OBR-2", "connect"), "cert_cn connect already belongs to client OBR-1"},
	} {
		_, err := s.EnsureClient(ctx, c.c, false)
		var conflict *RegistryConflictError
		if !errors.As(err, &conflict) || err.Error() != c.want {
			t.Fatalf("conflict for %s: %v, want %q", c.c.ID, err, c.want)
		}
	}
	participant := " belongs to a Beerus participant. Use another cert_cn"
	for _, c := range []struct {
		edit func(*openbanking.RegistryClient)
		want string
	}{
		{func(c *openbanking.RegistryClient) { c.ID = "-x" }, `client id "-x" must be letters, digits, dots, dashes and underscores, up to 40 characters`},
		{func(c *openbanking.RegistryClient) { c.Name = "" }, "client OBR-3 needs a name and a legal_name"},
		{func(c *openbanking.RegistryClient) { c.CertCN = "" }, "client OBR-3 needs a cert_cn: the ID it sends in the Beerus-Caller header"},
		{func(c *openbanking.RegistryClient) { c.CertCN = "nibss" }, "client OBR-3 cert_cn nibss" + participant},
		{func(c *openbanking.RegistryClient) { c.Role = "bank" }, `client OBR-3 role must be aggregator, not "bank"`},
		{func(c *openbanking.RegistryClient) { c.RedirectURIs = nil }, "client OBR-3 needs at least one redirect URI"},
		{func(c *openbanking.RegistryClient) { c.RedirectURIs = []string{"http://other.example.com/cb"} }, `redirect URI "http://other.example.com/cb" must be an https URL, or an http URL on localhost`},
	} {
		c3 := client("OBR-3", "three")
		c.edit(&c3)
		_, err := s.EnsureClient(ctx, c3, false)
		wantErr(t, err, c.want)
	}
	for _, id := range []string{"OBR-3", "OBR-2"} {
		_, err := s.EnsureClient(ctx, client(id, "cn-"+id), false)
		must(t, err)
	}
	pages(t, "clients", func(limit int, after string) ([]string, string, error) {
		got, next, err := s.Clients(ctx, limit, after)
		ids := []string{}
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		return ids, next, err
	}, "OBR-1", "OBR-2", "OBR-3")
	if got, err := s.Client(ctx, "OBR-2"); err != nil || got.CertCN != "cn-OBR-2" {
		t.Fatalf("client: %+v, %v", got, err)
	}
	_, err = s.Client(ctx, "OBR-9")
	wantErr(t, err, "client OBR-9 is unknown registry client")
}

func TestConsents(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	record := func(e ConsentEvent) (ConsentEvent, bool, error) { return s.RecordConsent(ctx, e) }
	first, replayed, err := record(consent("bank-a", "c1", 1, t0))
	if err != nil || replayed || first.Seq != 1 || first.PartnerID != "balance" || first.Via != "bank" || !first.ChangedAt.Equal(t0) {
		t.Fatalf("first record: %+v, %v, %v", first, replayed, err)
	}
	if again, replayed, err := record(consent("bank-a", "c1", 1, t0)); err != nil || !replayed || again != first {
		t.Fatalf("replay: %+v, %v, %v", again, replayed, err)
	}
	reborn := consent("bank-a", "c1", 1, t0)
	reborn.Incarnation = "db2"
	for i, e := range []ConsentEvent{consent("bank-b", "c2", 1, t0), consent("bank-a", "c3", 2, t0), reborn, consent("bank-a", "c4", 3, t0), consent("bank-a", "c5", 4, t0)} {
		if got, replayed, err := record(e); err != nil || replayed || got.Seq != int64(i+2) {
			t.Fatalf("event %d: %+v, %v, %v", i, got, replayed, err)
		}
	}
	changed := consent("bank-a", "c1", 1, t0.Add(time.Hour))
	changed.Status = "Revoked"
	_, _, err = record(changed)
	var reused *ConsentReusedError
	if !errors.As(err, &reused) || err.Error() != "source_seq 1 of bank bank-a was first recorded with other status and changed_at" {
		t.Fatalf("a changed repeat: %v", err)
	}
	pages(t, "consents", func(limit int, after string) ([]string, string, error) {
		got, next, err := s.ConsentLedger(ctx, "bank-a", limit, after)
		ids := []string{}
		for _, e := range got {
			ids = append(ids, e.Incarnation+":"+e.ConsentID)
		}
		return ids, next, err
	}, "db1:c5", "db1:c4", "db2:c1", "db1:c3", "db1:c1")
	for _, c := range []struct {
		limit  int
		cursor string
		want   string
	}{
		{0, "", "limit must be from 1 to 1000, not 0"},
		{1001, "", "limit must be from 1 to 1000, not 1001"},
	} {
		_, _, err := s.ConsentLedger(ctx, "", c.limit, c.cursor)
		wantErr(t, err, c.want)
	}
	edit := func(f func(*ConsentEvent)) ConsentEvent {
		e := consent("bank-a", "c1", 1, t0)
		f(&e)
		return e
	}
	for _, c := range []struct {
		e    ConsentEvent
		want string
	}{
		{consent("", "c1", 1, t0), "a consent event needs a bank and a consent id"},
		{edit(func(e *ConsentEvent) { e.Incarnation = "" }), "consent c1 event needs an incarnation"},
		{consent("bank-a", "c1", 0, t0), "consent c1 event: source seq 0 must be 1 or more"},
		{edit(func(e *ConsentEvent) { e.Status = "Maybe" }), `consent c1 event: status "Maybe" must be AwaitingAuthorisation, Authorised, Rejected, Revoked, Expired, Dormant, ReconsentRequired`},
	} {
		_, _, err := record(c.e)
		wantErr(t, err, c.want)
	}
}
