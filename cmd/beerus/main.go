package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
)

var version = "0.1.0"

const usage = `Beerus simulates a Nigerian banking ecosystem on your machine.

Usage:
  beerus init [--dir .beerus] [--seed N] [--webhook-url URL] [--redirect-uri URL] [--partner-port 7443] [--widget-port 7444] [--control-port 7480]
  beerus up [--dir .beerus]
  beerus info [--dir .beerus]
  beerus clock [advance <24h|7d|365d> | pace <n>]
  beerus scenarios
  beerus scenario <name> [--account ID] [--institution bank-a] [--persona adaeze] [--connection ID] [--duration 2h] [--count N]
  beerus personas
  beerus connections
  beerus revoke <connection-id>
  beerus sync
  beerus webhooks [replay <delivery-id>]
  beerus snapshot [take [name] | restore <id>]
  beerus trust [--dir .beerus]
  beerus untrust [--dir .beerus]
  beerus version

The state directory defaults to .beerus, or $BEERUS_DIR when set.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "up":
		err = cmdUp(args)
	case "info":
		err = cmdInfo(args)
	case "trust":
		err = cmdTrust(args, true)
	case "untrust":
		err = cmdTrust(args, false)
	case "version":
		fmt.Println("beerus", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		err = cmdControl(cmd, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "beerus:", err)
		os.Exit(1)
	}
}

func defaultDir() string {
	if d := os.Getenv("BEERUS_DIR"); d != "" {
		return d
	}
	return ".beerus"
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	seed := fs.Int64("seed", 20261028, "world seed")
	hook := fs.String("webhook-url", "", "Balance's webhook URL")
	redirect := fs.String("redirect-uri", "", "Balance's widget redirect URI")
	pp := fs.Int("partner-port", 7443, "partner API port")
	wp := fs.Int("widget-port", 7444, "widget port")
	cp := fs.Int("control-port", 7480, "control API port")
	fs.Parse(args)
	c, key, err := config.Init(*dir, config.InitOptions{Seed: *seed, Today: clock.DateOf(time.Now()), WebhookURL: *hook, RedirectURI: *redirect, PartnerPort: *pp, WidgetPort: *wp, ControlPort: *cp})
	if err != nil {
		return err
	}
	fmt.Printf("Created %s.\n\nBalance's API key (shown once, also saved to %s):\n  %s\n\n", c.Dir, c.Path("partners", "balance", "api-key"), key)
	return printInfo(c)
}

func printInfo(c *config.Config) error {
	k, err := config.WebhookKey(c)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "Partner API\t%s\t(mTLS + X-Api-Key)\n", c.PartnerURL())
	fmt.Fprintf(w, "Connect widget\t%s\n", c.WidgetURL())
	fmt.Fprintf(w, "Customer page\t%s/customer\n", c.WidgetURL())
	fmt.Fprintf(w, "Control API\t%s\t(Bearer token in %s)\n", c.ControlURL(), c.Path("control-token"))
	fmt.Fprintf(w, "Client certificate\t%s\n", c.Path("partners", "balance", "client.crt"))
	fmt.Fprintf(w, "Client key\t%s\n", c.Path("partners", "balance", "client.key"))
	fmt.Fprintf(w, "Server CA\t%s\n", c.Path("partners", "balance", "ca.crt"))
	fmt.Fprintf(w, "API key file\t%s\n", c.Path("partners", "balance", "api-key"))
	fmt.Fprintf(w, "Webhook URL\t%s\n", c.Partners[0].WebhookURL)
	fmt.Fprintf(w, "Webhook public key\t%s\n", k.PublicWH())
	fmt.Fprintf(w, "Redirect URIs\t%s\n", strings.Join(c.Partners[0].RedirectURIs, ", "))
	return w.Flush()
}

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	fs.Parse(args)
	c, err := config.Load(*dir)
	if err != nil {
		return err
	}
	return printInfo(c)
}

func cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	fs.Parse(args)
	if !config.Exists(*dir) {
		if err := cmdInit([]string{"--dir", *dir}); err != nil {
			return err
		}
		fmt.Println()
	}
	c, err := config.Load(*dir)
	if err != nil {
		return err
	}
	if err := c.EnsureCertificates(); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app, err := connect.New(c, connect.Options{Logger: log})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("Beerus Connect (stub) is running. Business time %s, world %s.\n\n", app.Clock().Now().Format(time.RFC1123), app.WorldID())
	if err := printInfo(c); err != nil {
		return err
	}
	fmt.Println("\nPersonas (username / password):")
	for _, p := range app.World().Personas() {
		fmt.Printf("  %-8s %s  %s\n", p.Username, p.Password, p.Name)
	}
	fmt.Println("\nPress Ctrl+C to stop.")
	return app.Serve(ctx)
}

type ctl struct {
	base  string
	token string
}

func newCtl() (*ctl, error) {
	c, err := config.Load(defaultDir())
	if err != nil {
		return nil, fmt.Errorf("%w (run beerus init or set BEERUS_DIR)", err)
	}
	tok, err := config.ReadSecret(c.Path("control-token"))
	if err != nil {
		return nil, err
	}
	return &ctl{base: c.ControlURL(), token: tok}, nil
}

func (c *ctl) do(method, path string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("is beerus up running? %w", err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode >= 300 {
		if e, ok := out["error"].(map[string]any); ok {
			return nil, errors.New(fmt.Sprint(e["message"]))
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return out, nil
}

func pretty(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func cmdControl(cmd string, args []string) error {
	c, err := newCtl()
	if err != nil {
		return err
	}
	var out map[string]any
	switch cmd {
	case "clock":
		switch {
		case len(args) == 0:
			out, err = c.do("GET", "/clock", nil)
		case args[0] == "advance" && len(args) == 2:
			out, err = c.do("POST", "/clock/advance", map[string]string{"by": args[1]})
		case args[0] == "pace" && len(args) == 2:
			var p float64
			if _, e := fmt.Sscan(args[1], &p); e != nil {
				return e
			}
			out, err = c.do("POST", "/clock/pace", map[string]float64{"pace": p})
		default:
			return errors.New("usage: beerus clock [advance <dur> | pace <n>]")
		}
	case "scenarios":
		out, err = c.do("GET", "/scenarios", nil)
	case "scenario":
		if len(args) == 0 {
			return errors.New("usage: beerus scenario <name> [flags]")
		}
		fs := flag.NewFlagSet("scenario", flag.ExitOnError)
		acc := fs.String("account", "", "account ID")
		inst := fs.String("institution", "", "institution ID")
		persona := fs.String("persona", "", "persona ID")
		conn := fs.String("connection", "", "connection ID")
		dur := fs.String("duration", "", "duration")
		count := fs.Int("count", 0, "count")
		fs.Parse(args[1:])
		out, err = c.do("POST", "/scenarios/"+args[0], map[string]any{"account_id": *acc, "institution": *inst, "persona": *persona, "connection_id": *conn, "duration": *dur, "count": *count})
	case "personas":
		out, err = c.do("GET", "/personas", nil)
	case "connections":
		out, err = c.do("GET", "/connections", nil)
	case "revoke":
		if len(args) != 1 {
			return errors.New("usage: beerus revoke <connection-id>")
		}
		out, err = c.do("POST", "/connections/"+args[0]+"/revoke", nil)
	case "sync":
		out, err = c.do("POST", "/sync", nil)
	case "webhooks":
		if len(args) == 2 && args[0] == "replay" {
			out, err = c.do("POST", "/webhooks/deliveries/"+args[1]+"/replay", nil)
		} else {
			out, err = c.do("GET", "/webhooks/deliveries?limit=20", nil)
		}
	case "snapshot":
		switch {
		case len(args) == 0:
			out, err = c.do("GET", "/snapshots", nil)
		case args[0] == "take":
			name := ""
			if len(args) > 1 {
				name = args[1]
			}
			out, err = c.do("POST", "/snapshots", map[string]string{"name": name})
		case args[0] == "restore" && len(args) == 2:
			out, err = c.do("POST", "/snapshots/"+args[1]+"/restore", nil)
		default:
			return errors.New("usage: beerus snapshot [take [name] | restore <id>]")
		}
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		return err
	}
	pretty(out)
	return nil
}
