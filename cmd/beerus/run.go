package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
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
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	participant := fs.String("participant", "", "cbn, sim, bank-a, bank-b or any bank ID")
	bind := fs.String("bind", envOr("BEERUS_BIND", "0.0.0.0"), "address to listen on")
	core := fs.String("core", envOr("BEERUS_CORE_URL", "http://sim:7480"), "the simulator's control URL, for banks")
	healthPort := fs.Int("health-port", 7490, "health port for cbn")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connect.SetBind(*bind)
	switch *participant {
	case "":
		return errors.New("usage: beerus run --participant cbn|sim|bank-a|bank-b")
	case "cbn":
		return runCBN(ctx, *dir, *bind, *healthPort, log)
	case "sim":
		c, err := config.Load(*dir)
		if err != nil {
			return err
		}
		app, err := connect.New(c, connect.Options{Logger: log, Role: connect.RoleSim, CoreURL: *core})
		if err != nil {
			return err
		}
		log.Info("simulator running", "world", app.WorldID())
		return app.Serve(ctx)
	case "connect", "nibss":
		return fmt.Errorf("%s runs inside the sim participant; start --participant sim", *participant)
	}
	c, err := config.Load(*dir)
	if err != nil {
		return err
	}
	if c.Bank(*participant) == nil {
		return fmt.Errorf("%q is not a participant; banks in %s are %s", *participant, c.Path("beerus.yaml"), strings.Join(bankIDs(c), ", "))
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

func runCBN(ctx context.Context, dir, bind string, port int, log *slog.Logger) error {
	if !config.Exists(dir) {
		seed, _ := strconv.ParseInt(envOr("BEERUS_SEED", "0"), 10, 64)
		port := func(key string, fallback int) int {
			n, err := strconv.Atoi(os.Getenv(key))
			if err != nil || n <= 0 {
				return fallback
			}
			return n
		}
		opts := config.InitOptions{Seed: seed, Today: clock.DateOf(time.Now()), PartnerPort: port("BEERUS_PARTNER_PORT", 0), WidgetPort: port("BEERUS_WIDGET_PORT", 0), ControlPort: port("BEERUS_CONTROL_PORT", 0), BankPort: port("BEERUS_BANK_PORT", 0)}
		if _, err := config.Init(dir, opts); err != nil {
			return err
		}
	}
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	if url := os.Getenv("BEERUS_DATABASE_URL"); url != "" {
		c.DatabaseURL = url
	}
	if len(c.Partners) == 0 {
		if redirect := os.Getenv("BEERUS_PARTNER_REDIRECT_URI"); redirect != "" {
			p := config.PartnerOptions{ID: os.Getenv("BEERUS_PARTNER_ID"), Name: os.Getenv("BEERUS_PARTNER_NAME"), LegalName: os.Getenv("BEERUS_PARTNER_LEGAL_NAME"), Purpose: os.Getenv("BEERUS_PARTNER_PURPOSE"), RedirectURIs: []string{redirect}, WebhookURL: os.Getenv("BEERUS_PARTNER_WEBHOOK_URL")}
			if p.ID == "" || p.Name == "" || p.LegalName == "" || p.Purpose == "" {
				return errors.New("BEERUS_PARTNER_REDIRECT_URI needs BEERUS_PARTNER_ID, BEERUS_PARTNER_NAME, BEERUS_PARTNER_LEGAL_NAME and BEERUS_PARTNER_PURPOSE")
			}
			if _, err := c.AddPartner(p); err != nil {
				return err
			}
		}
	}
	if err := c.EnsureCertificates(); err != nil {
		return err
	}
	if err := c.Save(); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /ca.crt", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, c.Path("ca", "root.crt")) })
	srv := &http.Server{Addr: bind + ":" + strconv.Itoa(port), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
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
