package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envNumber(key string, positive bool) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", key, v)
	}
	if positive && n <= 0 {
		return 0, fmt.Errorf("%s must be above zero, got %q", key, v)
	}
	return n, nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	participant := fs.String("participant", "", "cbn, core or a bank ID")
	bind := fs.String("bind", envOr("BEERUS_BIND", "127.0.0.1"), "address to listen on")
	core := fs.String("core", os.Getenv("BEERUS_CORE_URL"), "control URL of the core participant, for banks")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connect.SetBind(*bind)
	switch *participant {
	case "":
		return errors.New("usage: beerus run --participant cbn|core|<bank-id>")
	case "cbn":
		return runCBN(ctx, *dir, *bind, log)
	case "core":
		c, err := connect.LoadConfig(*dir)
		if err != nil {
			return err
		}
		app, err := connect.New(c, connect.Options{Logger: log, Role: connect.RoleCore, CoreURL: *core})
		if err != nil {
			return err
		}
		log.Info("core running", "world", app.WorldID())
		return app.Serve(ctx)
	case "connect", "nibss":
		return fmt.Errorf("%s runs inside the core participant; start --participant core", *participant)
	}
	c, err := connect.LoadConfig(*dir)
	if err != nil {
		return err
	}
	if c.Bank(*participant) == nil {
		return fmt.Errorf("%q is not a participant; banks in %s are %s", *participant, c.Dir, strings.Join(bankIDs(c), ", "))
	}
	app, err := connect.New(c, connect.Options{Logger: log, Role: connect.RoleBank, BankID: *participant, CoreURL: *core})
	if err != nil {
		return err
	}
	log.Info("bank running", "bank", *participant)
	return app.ServeBank(ctx)
}

func bankIDs(c *config.Config) []string {
	ids := []string{}
	for _, b := range c.Banks {
		ids = append(ids, b.ID)
	}
	return ids
}

func runCBN(ctx context.Context, dir, bind string, log *slog.Logger) error {
	ok, err := connect.Initialized(dir)
	if err != nil {
		return err
	}
	if !ok {
		if err := initFromEnv(dir); err != nil {
			return err
		}
	}
	c, err := connect.LoadConfig(dir)
	if err != nil {
		return err
	}
	if id := os.Getenv("BEERUS_PARTNER_ID"); ok && os.Getenv("BEERUS_PARTNER_REDIRECT_URI") != "" && c.Partner(id) == nil {
		log.Warn("BEERUS_PARTNER_* only applies when the setup is first made; register the partner with beerus partner add once Beerus is running", "partner", id)
	}
	if err := c.EnsureCertificates(time.Now()); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /ca.crt", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, c.Path("ca", "root.crt")) })
	if c.Connect.HealthPort <= 0 {
		return fmt.Errorf("%s has no connect.health_port; set it to the port the cbn container listens on", c.Path(connect.LocalFile))
	}
	srv := &http.Server{Addr: net.JoinHostPort(bind, strconv.Itoa(c.Connect.HealthPort)), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	log.Info("cbn: certificates issued", "dir", dir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func initFromEnv(dir string) error {
	local := connect.DefaultLocal()
	for key, dst := range map[string]*int{"BEERUS_PARTNER_PORT": &local.Connect.PartnerPort, "BEERUS_WIDGET_PORT": &local.Connect.WidgetPort, "BEERUS_CONTROL_PORT": &local.Connect.ControlPort, "BEERUS_HEALTH_PORT": &local.Connect.HealthPort} {
		n, err := envNumber(key, true)
		if err != nil {
			return err
		}
		if n != 0 {
			*dst = int(n)
		}
	}
	today := clock.DateOf(time.Now())
	spec := setup.DefaultSpec(defaultSetupName, today)
	if redirect := os.Getenv("BEERUS_PARTNER_REDIRECT_URI"); redirect != "" {
		p := setup.Partner{ID: os.Getenv("BEERUS_PARTNER_ID"), Name: os.Getenv("BEERUS_PARTNER_NAME"), LegalName: os.Getenv("BEERUS_PARTNER_LEGAL_NAME"), Purpose: os.Getenv("BEERUS_PARTNER_PURPOSE"), RedirectURIs: []string{redirect}, WebhookURL: os.Getenv("BEERUS_PARTNER_WEBHOOK_URL")}
		if p.ID == "" || p.Name == "" || p.LegalName == "" || p.Purpose == "" {
			return errors.New("BEERUS_PARTNER_REDIRECT_URI needs BEERUS_PARTNER_ID, BEERUS_PARTNER_NAME, BEERUS_PARTNER_LEGAL_NAME and BEERUS_PARTNER_PURPOSE")
		}
		spec.Spec.Partners = append(spec.Spec.Partners, p)
	}
	return connect.Init(dir, spec, local, today)
}

func cmdHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	token := fs.String("token-file", "", "file with a bearer token")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: beerus health <url> [--token-file FILE]")
	}
	req, err := http.NewRequest(http.MethodGet, fs.Arg(0), nil)
	if err != nil {
		return err
	}
	if *token != "" {
		tok, err := config.ReadSecret(*token)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s answered %s", fs.Arg(0), resp.Status)
	}
	return nil
}
