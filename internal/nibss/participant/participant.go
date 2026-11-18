package participant

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	headerTimeout = 10 * time.Second
	readLimit     = 30 * time.Second
	writeLimit    = 60 * time.Second
	idleLimit     = 2 * time.Minute
	shutdownWait  = 5 * time.Second
	kind          = "nibss"
	phaseActive   = "active"
)

type Options struct {
	Logger      *slog.Logger
	Bind        string
	DatabaseURL string
}

type Participant struct {
	dir   string
	file  config.NIBSSFile
	log   *slog.Logger
	clock *clock.Clock
	bind  string
	token string
	store *nibss.Store
	tls   *tls.Config
	banks *bankCaller
	cbn   *cbnCaller

	settles chan settleRequest
}

func New(ctx context.Context, dir string, o Options) (*Participant, error) {
	f, err := config.LoadNIBSSFile(dir)
	if err != nil {
		return nil, err
	}
	if o.DatabaseURL == "" {
		return nil, fmt.Errorf("%s is not set; set it to the Postgres URL of NIBSS", config.DatabaseURLEnv)
	}
	own := config.NIBSSDir(dir)
	token, err := config.ReadSecret(filepath.Join(own, config.NIBSSTokenFile))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	tc, err := serverTLS(own)
	if err != nil {
		return nil, err
	}
	banks, err := newBankCaller(own, remoteBind(o.Bind), bankCallTimeout)
	if err != nil {
		return nil, err
	}
	cbn, err := newCBNCaller(own, f.CBNPort, remoteBind(o.Bind))
	if err != nil {
		return nil, err
	}
	p := &Participant{dir: dir, file: f, log: o.Logger, clock: clock.New(), bind: o.Bind, token: token, tls: tc, banks: banks, cbn: cbn, settles: make(chan settleRequest)}
	if p.log == nil {
		p.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if p.store, err = nibss.Open(context.WithoutCancel(ctx), o.DatabaseURL, f.Database); err != nil {
		return nil, fmt.Errorf("open the NIBSS directory: %w", err)
	}
	if err := p.store.KeepSchedule(ctx, f.Sessions); err != nil {
		p.store.Close()
		return nil, err
	}
	return p, nil
}

func remoteBind(bind string) bool {
	ip := net.ParseIP(bind)
	return bind != "" && (ip == nil || !ip.IsLoopback())
}

func Run(ctx context.Context, dir string, o Options) error {
	p, err := New(ctx, dir, o)
	if err != nil {
		return err
	}
	p.log.Info("nibss running", "port", p.file.Port, "control_port", p.file.ControlPort)
	return p.Serve(ctx)
}

func (p *Participant) Close() {
	p.store.Close()
}

func serverTLS(own string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(own, "server.crt"), filepath.Join(own, "server.key"))
	if err != nil {
		return nil, fmt.Errorf("load NIBSS server certificate: %w", err)
	}
	pool, err := pki.LoadPool(filepath.Join(own, config.NIBSSCAFile))
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
