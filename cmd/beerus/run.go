package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/seed"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/aggregator"
	"github.com/evolutionlabs-core/beerus/internal/bank/participant"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/core"
	nibssparticipant "github.com/evolutionlabs-core/beerus/internal/nibss/participant"
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
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	which := fs.String("participant", "", "what to run: cbn, core, nibss, connect or a bank ID")
	bind := fs.String("bind", envOr("BEERUS_BIND", "127.0.0.1"), "`address` to listen on")
	supervised := fs.Bool("supervised", false, "stop when standard input closes")
	coreURL := fs.String("core", os.Getenv("BEERUS_CORE_URL"), "`URL` of the core participant's control API, for banks")
	if _, err := parseAfter("run", fs, args, 0); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *supervised {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}
	switch *which {
	case "":
		return usageError("run")
	case "cbn":
		return runCBN(ctx, *dir, *bind, log)
	case "core":
		c, err := setup.LoadConfig(*dir)
		if err != nil {
			return err
		}
		app, err := core.New(c, core.Options{Logger: log, CoreURL: *coreURL, Bind: *bind})
		if err != nil {
			return err
		}
		log.Info("core running", "world", app.WorldID())
		return app.Serve(ctx)
	case "nibss":
		return nibssparticipant.Run(ctx, *dir, nibssparticipant.Options{Logger: log, Bind: *bind, DatabaseURL: os.Getenv(config.DatabaseURLEnv)})
	case connectID:
		return aggregator.Run(ctx, *dir, aggregator.Options{Logger: log, Bind: *bind})
	}
	return participant.Run(ctx, *dir, participant.Options{Logger: log, BankID: *which, CoreURL: *coreURL, Bind: *bind, DatabaseURL: os.Getenv(config.DatabaseURLEnv)})
}

func initFromEnv(dir string) error {
	local := setup.DefaultLocal()
	for key, dst := range map[string]*int{"BEERUS_PARTNER_PORT": &local.Connect.PartnerPort, "BEERUS_WIDGET_PORT": &local.Connect.WidgetPort, "BEERUS_CONTROL_PORT": &local.Connect.ControlPort, "BEERUS_CONNECT_CONTROL_PORT": &local.Connect.ConnectControlPort, "BEERUS_HEALTH_PORT": &local.Connect.HealthPort, "BEERUS_NIBSS_PORT": &local.Connect.NIBSSPort, "BEERUS_CBN_PORT": &local.Connect.CBNPort} {
		n, err := envNumber(key, true)
		if err != nil {
			return err
		}
		if n != 0 {
			*dst = int(n)
		}
	}
	today := clock.DateOf(clock.New().Now())
	spec := setup.DefaultSpec(defaultSetupName)
	if path := os.Getenv("BEERUS_SETUP_FILE"); path != "" {
		var err error
		if spec, err = readSpec(path); err != nil {
			return fmt.Errorf("BEERUS_SETUP_FILE: %w", err)
		}
	}
	if redirect := os.Getenv("BEERUS_PARTNER_REDIRECT_URI"); redirect != "" {
		p := setup.Partner{ID: os.Getenv("BEERUS_PARTNER_ID"), Name: os.Getenv("BEERUS_PARTNER_NAME"), LegalName: os.Getenv("BEERUS_PARTNER_LEGAL_NAME"), Purpose: os.Getenv("BEERUS_PARTNER_PURPOSE"), RedirectURIs: []string{redirect}, WebhookURL: os.Getenv("BEERUS_PARTNER_WEBHOOK_URL")}
		if p.ID == "" || p.Name == "" || p.LegalName == "" || p.Purpose == "" {
			return errors.New("BEERUS_PARTNER_REDIRECT_URI needs BEERUS_PARTNER_ID, BEERUS_PARTNER_NAME, BEERUS_PARTNER_LEGAL_NAME and BEERUS_PARTNER_PURPOSE")
		}
		spec.Spec.Partners = append(spec.Spec.Partners, p)
	}
	return seed.Init(dir, spec, local, today)
}

func cmdHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	token := fs.String("token-file", "", "`file` with a bearer token")
	if err := parseFlags("health", fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usageError("health")
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
