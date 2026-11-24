package nibss

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const testDatabase = "beerus_test"

func testBaseURL() string {
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "host=/tmp dbname=postgres"
}

func testConn(ctx context.Context, t testing.TB, db string) *pgx.Conn {
	cfg, err := pgx.ParseConfig(testBaseURL())
	if err != nil {
		t.Fatal(err)
	}
	if db != "" {
		cfg.Database = db
	}
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Skipf("no Postgres for NIBSS tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	return c
}

func scratchPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	c := testConn(ctx, t, "")
	var exists bool
	if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, testDatabase).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		_, err := c.Exec(ctx, `CREATE DATABASE `+testDatabase)
		var pgErr *pgconn.PgError
		if err != nil && !(errors.As(err, &pgErr) && (pgErr.Code == "42P04" || pgErr.Code == "23505")) {
			t.Fatal(err)
		}
	}
	c.Close(ctx)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "n_" + hex.EncodeToString(b)
	admin := testConn(ctx, t, testDatabase)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	admin.Close(ctx)
	cfg, err := pgxpool.ParseConfig(testBaseURL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = testDatabase
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		c := testConn(context.Background(), t, testDatabase)
		_, _ = c.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		c.Close(context.Background())
	})
	return pool
}

func testStore(t testing.TB) *Store {
	t.Helper()
	s, err := newStore(context.Background(), scratchPool(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func register(t testing.TB, s *Store, id, name, code, nuban string) {
	t.Helper()
	if err := s.Register(context.Background(), member(id, name, code, nuban)); err != nil {
		t.Fatal(err)
	}
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

func registerCapped(t testing.TB, s *Store, id, name, code, nuban string, l Limits) {
	t.Helper()
	m := member(id, name, code, nuban)
	m.Limits = l
	if err := s.Register(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

func setStatus(t testing.TB, s *Store, id, status string) {
	t.Helper()
	if err := s.SetStatus(context.Background(), id, status); err != nil {
		t.Fatal(err)
	}
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	_, err := Open(ctx, testBaseURL(), "nibss")
	wantErr(t, err, `database name "nibss" must look like beerus_something`)
	testConn(ctx, t, "").Close(ctx)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "beerus_nibss_t" + hex.EncodeToString(b)
	t.Cleanup(func() {
		c := testConn(context.Background(), t, "")
		_, _ = c.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name)
		c.Close(context.Background())
	})
	for i := range 2 {
		s, err := Open(ctx, testBaseURL(), name)
		if err != nil {
			t.Fatal(err)
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
	if err != nil {
		t.Fatal(err)
	}
	if dir, err := s.Directory(ctx); err != nil || len(dir) != 0 {
		t.Fatalf("new directory: %+v, %v", dir, err)
	}
	register(t, s, "bank-c", "Bank C", "999003", "903")
	register(t, s, "bank-a", "Bank A", "999001", "901")
	register(t, s, "bank-b", "Bank B", "999002", "902")
	setStatus(t, s, "bank-b", nip.StatusTerminating)
	setStatus(t, s, "bank-b", nip.StatusRemoved)
	again, err := newStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := again.Directory(ctx)
	want := []nip.Institution{
		{ID: "bank-a", Name: "Bank A", NIPCode: "999001", NUBANCode: "901", Type: "commercial", Status: nip.StatusActive},
		{ID: "bank-b", Name: "Bank B", NIPCode: "999002", NUBANCode: "902", Type: "commercial", Status: nip.StatusRemoved},
		{ID: "bank-c", Name: "Bank C", NIPCode: "999003", NUBANCode: "903", Type: "commercial", Status: nip.StatusActive},
	}
	if err != nil || !slices.Equal(dir, want) {
		t.Fatalf("directory: %+v, %v", dir, err)
	}
	pages := []struct {
		name  string
		limit int
		after string
		want  string
		next  string
		err   string
	}{
		{"every bank in code order", 10, "", "bank-a active|bank-b removed|bank-c active", "", ""},
		{"first page", 2, "", "bank-a active|bank-b removed", "999002", ""},
		{"second page", 2, "999002", "bank-c active", "", ""},
		{"page that ends exactly", 3, "", "bank-a active|bank-b removed|bank-c active", "", ""},
		{"after the last", 2, "999003", "", "", ""},
		{"a bad cursor", 2, "nope", "", "", `cursor "nope" is not valid; use the next value from the last page`},
	}
	for _, r := range pages {
		got, next, err := s.Institutions(ctx, r.limit, r.after)
		parts := []string{}
		for _, in := range got {
			parts = append(parts, in.ID+" "+in.Status)
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if have := strings.Join(parts, "|"); have != r.want || next != r.next || msg != r.err {
			t.Errorf("%s: %q, next %q, error %q; want %q, next %q, error %q", r.name, have, next, msg, r.want, r.next, r.err)
		}
	}
	_, err = institution(ctx, s.pool, "123456", "")
	if !errors.Is(err, nip.ErrUnknownInstitution) {
		t.Fatalf("got %v, want nip.ErrUnknownInstitution", err)
	}
	wantErr(t, err, "bank code 123456 is not in the NIBSS directory")
	steps := []struct{ to, want string }{
		{nip.StatusSuspended, ""},
		{nip.StatusSuspended, ""},
		{nip.StatusActive, ""},
		{nip.StatusRemoved, "institution bank-a is active and cannot become removed: status change not allowed"},
		{nip.StatusTerminating, ""},
		{nip.StatusActive, "institution bank-a is terminating and cannot become active: status change not allowed"},
		{nip.StatusRemoved, ""},
		{nip.StatusActive, "institution bank-a is removed and cannot become active: status change not allowed"},
	}
	for i, st := range steps {
		err := s.SetStatus(ctx, "bank-a", st.to)
		if st.want == "" && err != nil || st.want != "" && (err == nil || err.Error() != st.want) {
			t.Fatalf("step %d to %s: %v, want %q", i, st.to, err, st.want)
		}
	}
	wantErr(t, s.SetStatus(ctx, "bank-a", "closed"), `unknown institution status "closed"`)
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
	setStatus(t, s, "bank-gone", nip.StatusTerminating)
	setStatus(t, s, "bank-gone", nip.StatusRemoved)
	a := member("bank-a", "Member A", "999001", "901")
	if err := s.Register(ctx, a); err != nil {
		t.Fatalf("a repeat: %v", err)
	}
	setStatus(t, s, "bank-a", nip.StatusSuspended)
	rebrand := a
	rebrand.Name, rebrand.Type = "Member A Plc", "digital"
	if err := s.Register(ctx, rebrand); err != nil {
		t.Fatal(err)
	}
	if got, _ := institution(ctx, s.pool, "999001", ""); got.Name != "Member A Plc" || got.Type != "digital" || got.Status != nip.StatusSuspended {
		t.Fatalf("rebrand: %+v", got)
	}
	if dir, _ := s.Directory(ctx); len(dir) != 2 {
		t.Fatalf("directory %d rows, want 2", len(dir))
	}
	refusals := []struct {
		in   Member
		want string
	}{
		{member("bank-a", "Member A", "999001", "911"), "bank bank-a is registered with nuban_code 901, and this request has nuban_code 911"},
		{member("bank-a", "Member A", "999011", "901"), "bank bank-a is registered with nip_code 999001, and this request has nip_code 999011"},
		{member("bank-x", "Member X", "999001", "931"), "bank bank-x: nip_code 999001 is held by bank-a: code or name already used"},
		{member("bank-x", "Member X", "999031", "901"), "bank bank-x: nuban_code 901 is held by bank-a: code or name already used"},
		{member("bank-x", "member a plc", "999031", "931"), `bank bank-x: name "member a plc" is held by bank-a: code or name already used`},
		{member("bank-x", "Member X", "999009", "931"), "bank bank-x: nip_code 999009 is held by removed bank bank-gone: code or name already used"},
		{member("bank-x", "Member X", "999031", "909"), "bank bank-x: nuban_code 909 is held by removed bank bank-gone: code or name already used"},
		{member("bank-gone", "Gone Bank", "999009", "909"), "bank bank-gone was removed, and its id stays reserved: code or name already used"},
		{member("bank-x", " ", "999031", "931"), "a bank needs an id, a name and a type"},
		{member("bank-x", "X", "99913", "931"), `bank bank-x: nip_code "99913" must be 6 digits`},
		{member("bank-x", "X", "999031", "93"), `bank bank-x: nuban_code "93" must be 3, 5 or 6 digits`},
	}
	for _, c := range refusals {
		wantErr(t, s.Register(ctx, c.in), c.want)
	}
	if err := s.Register(ctx, member("bank-y", "Gone Bank", "999032", "932")); err != nil {
		t.Fatalf("a removed bank's name is free again: %v", err)
	}
	suspended := member("bank-x", "X", "999031", "931")
	suspended.Status = nip.StatusSuspended
	wantErr(t, s.Register(ctx, suspended), "bank bank-x registers as active, not suspended; use SetStatus to change it")

	limits := func() Limits {
		t.Helper()
		var l Limits
		if err := s.pool.QueryRow(ctx, `SELECT net_debit_cap_minor, collateral_minor FROM institutions WHERE id = 'bank-a'`).Scan(&l.NetDebitCap, &l.Collateral); err != nil {
			t.Fatal(err)
		}
		return l
	}
	a.Limits = Limits{NetDebitCap: 500_000_000, Collateral: 7}
	if err := s.Register(ctx, a); err != nil || limits() != a.Limits {
		t.Fatalf("limits at registration: %+v, %v", limits(), err)
	}
	a.Limits = Limits{NetDebitCap: 900_000_000}
	if err := s.Register(ctx, a); err != nil || limits() != a.Limits {
		t.Fatalf("new limits are not a conflict: %+v, %v", limits(), err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE institutions SET net_debit_cap_minor = NULL WHERE id = 'bank-a'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(ctx, a); err != nil || limits() != a.Limits {
		t.Fatalf("a missing cap after a register again: %+v, %v", limits(), err)
	}
	a.Limits = Limits{NetDebitCap: -1}
	wantErr(t, s.Register(ctx, a), "bank bank-a: net_debit_cap_minor and collateral_minor must not be negative")
	registerContact(t, s)
}

func registerContact(t *testing.T, s *Store) {
	ctx := context.Background()
	register(t, s, "bank-k", "Member K", "999041", "941")
	withContact := func(url, identity string) Member {
		m := member("bank-k", "Member K", "999041", "941")
		m.CreditURL, m.CallerID = url, identity
		return m
	}
	steps := []struct {
		name string
		in   Member
		want string
	}{
		{"fills in what was empty", withContact("http://a.localhost:1", "bank-k"), ""},
		{"repeat", withContact("http://a.localhost:1", "bank-k"), ""},
		{"other url", withContact("http://a.localhost:2", "bank-k"), "bank bank-k is registered with credit_url http://a.localhost:1, and this request has credit_url http://a.localhost:2"},
		{"other identity and url", withContact("http://a.localhost:2", "bank-z"), "bank bank-k is registered with credit_url http://a.localhost:1 and caller_id bank-k, and this request has credit_url http://a.localhost:2 and caller_id bank-z"},
	}
	for _, st := range steps {
		err := s.Register(ctx, st.in)
		var conflict *RegistrationConflictError
		if st.want == "" && err != nil || st.want != "" && (!errors.As(err, &conflict) || err.Error() != st.want) {
			t.Fatalf("%s: got %v, want %q", st.name, err, st.want)
		}
	}
	if got, err := s.Member(ctx, "bank-k"); err != nil || got.CreditURL != "http://a.localhost:1" || got.CallerID != "bank-k" {
		t.Fatalf("member: %+v, %v", got, err)
	}
	for identity, want := range map[string]bool{"bank-k": true, "bank-z": false} {
		if ok, err := s.Admits(ctx, identity); err != nil || ok != want {
			t.Fatalf("admits %s: %v, %v, want %v", identity, ok, err, want)
		}
	}
	setStatus(t, s, "bank-k", nip.StatusTerminating)
	setStatus(t, s, "bank-k", nip.StatusRemoved)
	if ok, err := s.Admits(ctx, "bank-k"); err != nil || ok {
		t.Fatalf("a removed bank is admitted: %v, %v", ok, err)
	}
	_, err := s.Member(ctx, "bank-x")
	wantErr(t, err, "bank id bank-x is not in the NIBSS directory")
}

func TestRegistryAndConsents(t *testing.T) {
	s := testStore(t)
	t.Run("registry", func(t *testing.T) { registryClients(t, s) })
	t.Run("consents", func(t *testing.T) { consents(t, s) })
}

func registryClients(t *testing.T, s *Store) {
	ctx := context.Background()
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
	conflicts := []struct {
		c    openbanking.RegistryClient
		want string
	}{
		{client("OBR-1", "other"), "client OBR-1 is registered with cert_cn connect, and this request has cert_cn other"},
		{client("OBR-2", "connect"), "cert_cn connect already belongs to client OBR-1"},
	}
	for _, c := range conflicts {
		_, err := s.EnsureClient(ctx, c.c, false)
		var conflict *RegistryConflictError
		if !errors.As(err, &conflict) || err.Error() != c.want {
			t.Fatalf("conflict for %s: %v, want %q", c.c.ID, err, c.want)
		}
	}
	invalid := []struct {
		edit func(*openbanking.RegistryClient)
		want string
	}{
		{func(c *openbanking.RegistryClient) { c.ID = "-x" }, `client id "-x" must be letters, digits, dots, dashes and underscores, up to 40 characters`},
		{func(c *openbanking.RegistryClient) { c.Name = "" }, "client OBR-3 needs a name and a legal_name"},
		{func(c *openbanking.RegistryClient) { c.CertCN = "" }, "client OBR-3 needs a cert_cn: the ID it sends in the Beerus-Caller header"},
		{func(c *openbanking.RegistryClient) { c.CertCN = "nibss" }, "client OBR-3 cert_cn nibss belongs to a Beerus participant. Use another cert_cn"},
		{func(c *openbanking.RegistryClient) { c.CertCN = "cbn" }, "client OBR-3 cert_cn cbn belongs to a Beerus participant. Use another cert_cn"},
		{func(c *openbanking.RegistryClient) { c.Role = "bank" }, `client OBR-3 role must be aggregator, not "bank"`},
		{func(c *openbanking.RegistryClient) { c.RedirectURIs = nil }, "client OBR-3 needs at least one redirect URI"},
		{func(c *openbanking.RegistryClient) { c.RedirectURIs = []string{"http://other.example.com/cb"} }, `redirect URI "http://other.example.com/cb" must be an https URL, or an http URL on localhost`},
	}
	for _, c := range invalid {
		c3 := client("OBR-3", "three")
		c.edit(&c3)
		_, err := s.EnsureClient(ctx, c3, false)
		wantErr(t, err, c.want)
	}
	for _, id := range []string{"OBR-3", "OBR-2"} {
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
	if got, err := s.Client(ctx, "OBR-2"); err != nil || got.CertCN != "cn-OBR-2" {
		t.Fatalf("client: %+v, %v", got, err)
	}
	_, err = s.Client(ctx, "OBR-9")
	wantErr(t, err, "client OBR-9 is unknown registry client")
}

func client(id, cn string) openbanking.RegistryClient {
	return openbanking.RegistryClient{ID: id, Name: id, LegalName: id + " Ltd", CAC: "RC1", Role: "aggregator", CertCN: cn, JWK: openbanking.JWK{Kty: "EC", Crv: "P-256", X: "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU", Y: "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0", Kid: id}, RedirectURIs: []string{"https://" + id + ".localhost/cb"}, RegisteredAt: t0}
}

func consent(bank, id string, src int64, at time.Time) ConsentEvent {
	return ConsentEvent{Bank: bank, ConsentID: id, Incarnation: "db1", ClientID: "client-1", PartnerID: "balance", Status: "Authorised", Via: "bank", ChangedAt: at, SourceSeq: src}
}

func consents(t *testing.T, s *Store) {
	ctx := context.Background()
	record := func(e ConsentEvent) (ConsentEvent, bool, error) { return s.RecordConsent(ctx, e) }
	first, replayed, err := record(consent("bank-a", "c1", 1, t0))
	if err != nil || replayed || first.Seq != 1 || first.PartnerID != "balance" || first.Via != "bank" || !first.ChangedAt.Equal(t0) {
		t.Fatalf("first record: %+v, %v, %v", first, replayed, err)
	}
	again, replayed, err := record(consent("bank-a", "c1", 1, t0))
	if err != nil || !replayed || again != first {
		t.Fatalf("replay: %+v, %v, %v", again, replayed, err)
	}
	reborn := consent("bank-a", "c1", 1, t0)
	reborn.Incarnation = "db2"
	rows := []struct {
		name string
		e    ConsentEvent
		seq  int64
	}{
		{"same source seq at another bank", consent("bank-b", "c2", 1, t0), 2},
		{"next source seq", consent("bank-a", "c3", 2, t0), 3},
		{"same source seq in a new incarnation", reborn, 4},
		{"later events", consent("bank-a", "c4", 3, t0), 5},
		{"and more", consent("bank-a", "c5", 4, t0), 6},
	}
	for _, r := range rows {
		got, replayed, err := record(r.e)
		if err != nil || replayed || got.Seq != r.seq {
			t.Fatalf("%s: %+v, %v, %v", r.name, got, replayed, err)
		}
	}
	changed := consent("bank-a", "c1", 1, t0.Add(time.Hour))
	changed.Status = "Revoked"
	_, _, err = record(changed)
	var reused *ConsentReusedError
	if !errors.As(err, &reused) || err.Error() != "source_seq 1 of bank bank-a was first recorded with other status and changed_at" {
		t.Fatalf("a changed repeat: %v", err)
	}
	page, next, err := s.ConsentLedger(ctx, "bank-a", 2, "")
	if err != nil || len(page) != 2 || page[0].ConsentID != "c5" || page[1].ConsentID != "c4" || next != "5" {
		t.Fatalf("page 1: %+v, %q, %v", page, next, err)
	}
	page, next, err = s.ConsentLedger(ctx, "bank-a", 2, next)
	if err != nil || len(page) != 2 || page[0].Incarnation != "db2" || page[1].ConsentID != "c3" || next != "3" {
		t.Fatalf("page 2: %+v, %q, %v", page, next, err)
	}
	if all, next, err := s.ConsentLedger(ctx, "", 10, ""); err != nil || len(all) != 6 || next != "" {
		t.Fatalf("all banks: %d, %q, %v", len(all), next, err)
	}
	if exact, next, err := s.ConsentLedger(ctx, "bank-b", 1, ""); err != nil || len(exact) != 1 || next != "" {
		t.Fatalf("exact page: %+v, %q, %v", exact, next, err)
	}
	for _, c := range []struct {
		limit  int
		cursor string
		want   string
	}{
		{0, "", "limit must be from 1 to 1000, not 0"},
		{1001, "", "limit must be from 1 to 1000, not 1001"},
		{10, "abc", `cursor "abc" is not valid; use the next value from the last page`},
		{10, "0", `cursor "0" is not valid; use the next value from the last page`},
	} {
		_, _, err := s.ConsentLedger(ctx, "", c.limit, c.cursor)
		wantErr(t, err, c.want)
	}
	badStatus := consent("bank-a", "c1", 1, t0)
	badStatus.Status = "Maybe"
	noIncarnation := consent("bank-a", "c1", 1, t0)
	noIncarnation.Incarnation = ""
	for _, c := range []struct {
		e    ConsentEvent
		want string
	}{
		{consent("", "c1", 1, t0), "a consent event needs a bank and a consent id"},
		{noIncarnation, "consent c1 event needs an incarnation"},
		{consent("bank-a", "c1", 1, time.Time{}), "consent c1 event needs a changed time"},
		{consent("bank-a", "c1", 0, t0), "consent c1 event: source seq 0 must be 1 or more"},
		{badStatus, `consent c1 event: status "Maybe" must be AwaitingAuthorisation, Authorised, Rejected, Revoked, Expired, Dormant, ReconsentRequired`},
	} {
		_, _, err := record(c.e)
		wantErr(t, err, c.want)
	}
}
