package connect

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss/gateway"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

func TestBankDirectoryComesFromNIBSS(t *testing.T) {
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
	srv := server.New(server.Config{
		Dir:       cfg.Dir,
		Bank:      *cfg.Bank("bank-a"),
		NIBSSPort: ln.Addr().(*net.TCPAddr).Port,
		CoreURL:   "http://127.0.0.1:1",
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:     clock.New(),
		Phase:     PhaseActive,
	})
	if err := srv.RefreshBanks(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range server.OpenBanks(srv.Directory()) {
		got = append(got, b.ID)
	}
	if len(got) != 3 || got[0] != "bank-a" || got[1] != "bank-b" || got[2] != "bank-d" {
		t.Fatalf("offered %v, want bank-a, bank-b and bank-d without the terminating bank-c", got)
	}
	if own := srv.Lookup("bank-a"); own == nil || own.Name != "Bank A" {
		t.Fatalf("a bank's own entry must come from its own config, got %+v", own)
	}
	if other := srv.Lookup("bank-b"); other == nil || other.Name != "bank-b" || !other.Suspended() {
		t.Fatalf("another bank must come from the directory, got %+v", other)
	}
}
