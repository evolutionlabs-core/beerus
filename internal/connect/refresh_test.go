package connect

import (
	"testing"

	"context"
	"crypto/tls"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss/gateway"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/pki"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"io"
	"log/slog"
	"net"
	"net/http"
)

func TestNIBSSPortDefaultAndConflicts(t *testing.T) {
	ln := setup.LocalConnect{PartnerPort: 7443, WidgetPort: 7444, ControlPort: 7480, HealthPort: 7490}
	bankAt := func(port int) setup.Status {
		return setup.Status{Resources: []setup.Resource{{ID: "bank-a", Allocated: setup.Allocation{Port: port, APIPort: port + 1}}}}
	}
	rows := []struct {
		name string
		ln   setup.LocalConnect
		st   setup.Status
		port int
		err  string
	}{
		{"default", ln, setup.Status{}, defaultNIBSSPort, ""},
		{"set", setup.LocalConnect{PartnerPort: 7443, NIBSSPort: 9000}, setup.Status{}, 9000, ""},
		{"a connect port holds it", setup.LocalConnect{PartnerPort: 7443, HealthPort: defaultNIBSSPort}, setup.Status{}, 0, "NIBSS port 7481 is already used by connect.health_port; set connect.nibss_port in local.yaml to a free port"},
		{"a bank port holds it", ln, bankAt(defaultNIBSSPort), 0, "NIBSS port 7481 is already used by bank-a online banking; set connect.nibss_port in local.yaml to a free port"},
		{"a bank API port holds it", ln, bankAt(defaultNIBSSPort - 1), 0, "NIBSS port 7481 is already used by bank-a open banking API; set connect.nibss_port in local.yaml to a free port"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			port, err := nibssPort(r.ln, r.st)
			if r.err == "" && (err != nil || port != r.port) {
				t.Fatalf("port %d, error %v; want %d", port, err, r.port)
			}
			if r.err != "" && (err == nil || err.Error() != r.err) {
				t.Fatalf("error %v, want %q", err, r.err)
			}
		})
	}
}

func TestBankRoleDirectoryComesFromNIBSS(t *testing.T) {
	cfg := bankDirConfig(t, &config.Bank{ID: "bank-a", Name: "Bank A", Port: 1, APIPort: 2, Database: "db"})
	cert, err := tls.LoadX509KeyPair(cfg.Path("nibss", "server.crt"), cfg.Path("nibss", "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pki.LoadPool(cfg.Path("ca", "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	list := func(context.Context) ([]nip.Institution, error) {
		out := []nip.Institution{}
		for _, in := range []struct{ id, status string }{{"bank-a", nip.StatusActive}, {"bank-b", nip.StatusSuspended}, {"bank-c", nip.StatusTerminating}, {"bank-d", nip.StatusActive}} {
			out = append(out, nip.Institution{ID: in.id, Name: in.id, Status: in.status})
		}
		return out, nil
	}
	hs := &http.Server{Handler: gateway.New(list, slog.New(slog.NewTextHandler(io.Discard, nil))), ReadHeaderTimeout: probeLimit}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = hs.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}))
	}()
	t.Cleanup(func() {
		_ = hs.Close()
		<-served
	})
	srv := server.New(server.Settings{
		Dir:       cfg.Dir,
		Bank:      *cfg.Bank("bank-a"),
		NIBSSPort: ln.Addr().(*net.TCPAddr).Port,
		CoreURL:   "http://127.0.0.1:1",
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:     clock.New(nil),
		Phase:     PhaseActive,
		Reloaded:  func(config.Bank) {},
	})
	a := &App{role: RoleBank, bankID: "bank-a", cfg: cfg, srv: srv}
	if err := a.refreshBanks(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range a.openInstitutions() {
		got = append(got, b.ID)
	}
	if len(got) != 3 || got[0] != "bank-a" || got[1] != "bank-b" || got[2] != "bank-d" {
		t.Fatalf("offered %v, want bank-a, bank-b and bank-d without the terminating bank-c", got)
	}
	if own := a.bankLocked("bank-a"); own == nil || own.Name != "Bank A" {
		t.Fatalf("a bank's own entry must come from its own config, got %+v", own)
	}
	if other := a.bankLocked("bank-b"); other == nil || other.Name != "bank-b" || !other.Suspended() {
		t.Fatalf("another bank must come from the directory, got %+v", other)
	}
}
