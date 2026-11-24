package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	cbnparticipant "github.com/evolutionlabs-core/beerus/internal/cbn/participant"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/install"
)

const healthShutdown = 5 * time.Second

func runCBN(ctx context.Context, dir, bind string, log *slog.Logger) error {
	ok, err := setup.Initialized(dir)
	if err != nil {
		return err
	}
	if !ok {
		if err := initFromEnv(dir); err != nil {
			return err
		}
	}
	c, err := setup.LoadConfig(dir)
	if err != nil {
		return err
	}
	if id := os.Getenv("BEERUS_PARTNER_ID"); ok && os.Getenv("BEERUS_PARTNER_REDIRECT_URI") != "" && c.Partner(id) == nil {
		log.Warn("BEERUS_PARTNER_* only applies when the setup is first made; register the partner with beerus partner add once Beerus is running", "partner", id)
	}
	if err := install.PrepareCBN(c); err != nil {
		return err
	}
	if c.Connect.HealthPort <= 0 {
		return fmt.Errorf("%s has no connect.health_port; set it to the port the cbn container listens on", c.Path(setup.LocalFile))
	}
	p, err := cbnparticipant.New(ctx, dir, cbnparticipant.Options{Logger: log, Bind: bind, DatabaseURL: os.Getenv(config.DatabaseURLEnv)})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(c.Connect.HealthPort)))
	if err != nil {
		p.Close()
		return fmt.Errorf("listen for health: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	healthed := make(chan error, 1)
	go func() {
		defer cancel()
		healthed <- serveHealth(runCtx, ln)
	}()
	log.Info("cbn: prepared", "dir", dir)
	serr := p.Serve(runCtx)
	cancel()
	return errors.Join(serr, <-healthed)
}

func serveHealth(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-served:
		return err
	}
	shut, stop := context.WithTimeout(context.WithoutCancel(ctx), healthShutdown)
	defer stop()
	_ = srv.Shutdown(shut)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *supervisor) startCBN() {
	s.cbn = s.launch(config.CBNID, []string{config.DatabaseURLEnv + "=" + s.database}, []string{"run", "--participant", config.CBNID, "--dir", s.dir, "--supervised"})
}

func (s *supervisor) awaitCBN(ctx context.Context) error {
	return s.awaitHealth(ctx, "the CBN", s.cbn, s.cbnHealth)
}

func (s *supervisor) cbnHealth(ctx context.Context, client *http.Client) error {
	f, err := config.LoadCBNFile(s.dir)
	if err != nil {
		return err
	}
	token, err := config.ReadSecret(filepath.Join(config.CBNDir(s.dir), config.CBNTokenFile))
	if err != nil {
		return err
	}
	return controlHealth(ctx, client, "the CBN", f.ControlPort, token)
}
