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
	"net/url"
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
  beerus init [--dir .beerus] [--seed N] [--partner-port 7443] [--widget-port 7444] [--control-port 7480]
  beerus up [--dir .beerus]
  beerus run --participant cbn|sim|bank-a|bank-b [--dir .beerus] [--bind 0.0.0.0] [--core URL]
  beerus compose [--dir .beerus]
  beerus health URL [--token-file FILE]
  beerus info [--dir .beerus]
  beerus partner add <id> --redirect-uri URL [--redirect-uri URL] [--webhook-url URL] [--name N] [--legal-name N] [--purpose P]
  beerus partner list
  beerus partner rotate-key <id>
  beerus partner remove <id>
  beerus clock [advance <24h|7d|365d> | pace <n>]
  beerus scenarios
  beerus scenario run <file.yaml>
  beerus scenario <name> [--account ID] [--institution bank-a] [--persona adaeze] [--connection ID] [--duration 2h] [--count N]
  beerus personas
  beerus nip name-enquiry <bank_code> <nuban> | status <session_id> | participants | positions
  beerus transfer --from ACCOUNT --to ACCOUNT --amount NAIRA [--to-bank "Bank C"] [--to-name NAME] [--narration TEXT]
  beerus connections
  beerus revoke <connection-id>
  beerus sync
  beerus webhooks [replay <delivery-id>]
  beerus snapshot [take [name] | restore <id>]
  beerus bank [list]
  beerus bank create --name NAME [--type commercial|digital|microfinance] [--id ID] [--nip-code N] [--nuban-code N] [--history-days N] [--settles-through ID]
  beerus bank suspend|resume <id>
  beerus bank open-account <id> --persona ID [--type current|savings]
  beerus bank close <id> --to BANK [--money transfer|payout|leave] [--customers a=payout,b=leave] [--dry-run] [--yes]
  beerus bank purge <id> [--yes]
  beerus registry [list | add-client <id> --cert-cn CN --public-key FILE --redirect-uri URL [--name N] [--legal-name N] [--cac N] | consents [--bank ID]]
  beerus doctor [--dir .beerus] [--fix]
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
	case "run":
		err = cmdRun(args)
	case "compose":
		err = cmdCompose(args)
	case "health":
		err = cmdHealth(args)
	case "info":
		err = cmdInfo(args)
	case "doctor":
		err = cmdDoctor(args)
	case "partner":
		err = cmdPartner(args)
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
	pp := fs.Int("partner-port", 7443, "partner API port")
	wp := fs.Int("widget-port", 7444, "widget port")
	cp := fs.Int("control-port", 7480, "control API port")
	bp := fs.Int("bank-port", 7445, "first online banking port; each bank takes the next one")
	fs.Parse(args)
	c, err := config.Init(*dir, config.InitOptions{Seed: *seed, Today: clock.DateOf(time.Now()), PartnerPort: *pp, WidgetPort: *wp, ControlPort: *cp, BankPort: *bp})
	if err != nil {
		return err
	}
	fmt.Printf("Created %s.\n\n", c.Dir)
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
	for _, b := range c.Banks {
		fmt.Fprintf(w, "%s online banking\t%s\t(database %s)\n", b.Name, b.URL(), b.Database)
		if b.APIPort != 0 {
			fmt.Fprintf(w, "%s open banking API\t%s\t(mTLS)\n", b.Name, b.APIURL())
		}
	}
	fmt.Fprintf(w, "Control API\t%s\t(Bearer token in %s)\n", c.ControlURL(), c.Path("control-token"))
	fmt.Fprintf(w, "Webhook public key\t%s\n", k.PublicWH())
	if err := w.Flush(); err != nil {
		return err
	}
	if len(c.Partners) == 0 {
		fmt.Println("\nNo partner apps yet. Register one when it is ready to connect:\n  beerus partner add balance --redirect-uri http://localhost:3000/bank_connections/callback --webhook-url http://localhost:3000/webhooks/beerus")
		return nil
	}
	fmt.Println("\nPartner apps:")
	for _, p := range c.Partners {
		fmt.Printf("  %-12s credentials in %s\n", p.ID, c.Path("partners", p.ID))
	}
	return nil
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

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	fix := fs.Bool("fix", false, "drop leftover databases, delete snapshots that can no longer be restored and tidy the manifest")
	fs.Parse(args)
	c, err := config.Load(*dir)
	if err != nil {
		return err
	}
	rep, err := connect.Doctor(context.Background(), c, *fix)
	if err != nil {
		return err
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Println(title)
		for _, it := range items {
			fmt.Println("  " + it)
		}
	}
	fmt.Printf("%d databases belong to %s.\n", len(rep.Databases), c.Dir)
	list("Leftover databases:", rep.Orphans)
	list("Snapshots that can no longer be restored:", rep.DeadSnapshots)
	list("Manifest entries for banks that no longer exist:", rep.StaleManifest)
	list("Missing databases (beerus up recreates bank databases; missing snapshot copies cannot be restored):", rep.Missing)
	switch {
	case rep.Fixed:
		fmt.Println("Fixed.")
	case rep.Clean():
		fmt.Println("Nothing is left behind.")
	default:
		fmt.Println("Run beerus doctor --fix to clean this up.")
	}
	return nil
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
	if err := connect.CheckPorts(c); err != nil {
		return err
	}
	if err := c.EnsureCertificates(); err != nil {
		return err
	}
	if err := c.Save(); err != nil {
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
	fmt.Printf("\nControl room        %s\n", app.RoomURL())
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
	contentType := "application/json"
	if raw, ok := body.([]byte); ok {
		rd, contentType = bytes.NewReader(raw), "application/yaml"
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
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
			return errors.New("usage: beerus scenario <name> [flags] | beerus scenario run <file.yaml>")
		}
		if args[0] == "run" {
			if len(args) != 2 {
				return errors.New("usage: beerus scenario run <file.yaml>")
			}
			raw, rerr := os.ReadFile(args[1])
			if rerr != nil {
				return fmt.Errorf("cannot read %s", args[1])
			}
			out, err = c.do("POST", "/scenarios/run", raw)
			break
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
	case "transfer":
		fs := flag.NewFlagSet("transfer", flag.ExitOnError)
		from := fs.String("from", "", "account number, bank:number or acc_ ID to pay from")
		to := fs.String("to", "", "account number, bank:number or acc_ ID to pay to")
		amount := fs.String("amount", "", "amount in naira, such as 25000 or 2500.50")
		toBank := fs.String("to-bank", "", "bank of an outside account (Bank A, Bank B, Bank C, Bank D Microfinance)")
		toName := fs.String("to-name", "", "name of an outside beneficiary")
		narration := fs.String("narration", "", "narration for the debit")
		sessionID := fs.String("session-id", "", "30-digit NIP session ID; sending the same one again returns the first answer")
		fs.Parse(args)
		if *from == "" || *to == "" || *amount == "" {
			return errors.New("usage: beerus transfer --from ACCOUNT --to ACCOUNT --amount NAIRA")
		}
		out, err = c.do("POST", "/transfers", map[string]string{"from": *from, "to": *to, "amount": *amount, "to_bank": *toBank, "to_name": *toName, "narration": *narration, "session_id": *sessionID})
	case "nip":
		switch {
		case len(args) == 3 && args[0] == "name-enquiry":
			out, err = c.do("GET", "/nip/name-enquiry?bank_code="+url.QueryEscape(args[1])+"&account_number="+url.QueryEscape(args[2]), nil)
		case len(args) == 2 && args[0] == "status":
			out, err = c.do("GET", "/nip/transfers/"+url.PathEscape(args[1]), nil)
		case len(args) == 1 && (args[0] == "participants" || args[0] == "positions"):
			out, err = c.do("GET", "/nip/"+args[0], nil)
		default:
			return errors.New("usage: beerus nip [name-enquiry <bank_code> <nuban> | status <session_id> | participants | positions]")
		}
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
	case "bank":
		out, err = cmdBank(c, args)
	case "registry":
		out, err = cmdRegistry(c, args)
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

func cmdRegistry(c *ctl, args []string) (map[string]any, error) {
	const usage = "usage: beerus registry [list | add-client <id> --cert-cn CN --public-key FILE --redirect-uri URL | consents [--bank ID]]"
	switch {
	case len(args) == 0 || args[0] == "list":
		return c.do("GET", "/registry/clients", nil)
	case args[0] == "consents":
		fs := flag.NewFlagSet("registry consents", flag.ExitOnError)
		bank := fs.String("bank", "", "only consent changes recorded by this bank")
		fs.Parse(args[1:])
		return c.do("GET", "/registry/consents?bank="+url.QueryEscape(*bank), nil)
	case args[0] == "add-client" && len(args) >= 2 && !strings.HasPrefix(args[1], "-"):
		fs := flag.NewFlagSet("registry add-client", flag.ExitOnError)
		cn := fs.String("cert-cn", "", "common name of the client's mTLS certificate")
		keyFile := fs.String("public-key", "", "PEM file with the client's EC P-256 public key, used to check its private_key_jwt")
		name := fs.String("name", "", "brand name")
		legal := fs.String("legal-name", "", "legal name")
		cac := fs.String("cac", "", "CAC number")
		var redirects multi
		fs.Var(&redirects, "redirect-uri", "where the bank sends the browser back to (repeatable)")
		fs.Parse(args[2:])
		if *cn == "" || *keyFile == "" || len(redirects) == 0 {
			return nil, errors.New(usage)
		}
		pem, err := os.ReadFile(*keyFile)
		if err != nil {
			return nil, err
		}
		return c.do("POST", "/registry/clients", map[string]any{"id": args[1], "cert_cn": *cn, "public_key_pem": string(pem), "name": *name, "legal_name": *legal, "cac": *cac, "redirect_uris": []string(redirects)})
	}
	return nil, errors.New(usage)
}

func cmdBank(c *ctl, args []string) (map[string]any, error) {
	switch {
	case len(args) == 0 || args[0] == "list":
		fs := flag.NewFlagSet("bank list", flag.ExitOnError)
		state := fs.String("state", "", "only banks in this state: active, suspended, provisioning, repairing or closed")
		if len(args) > 0 {
			fs.Parse(args[1:])
		}
		if *state != "" {
			return c.do("GET", "/banks?state="+url.QueryEscape(*state), nil)
		}
		return c.do("GET", "/banks", nil)
	case args[0] == "create":
		fs := flag.NewFlagSet("bank create", flag.ExitOnError)
		name := fs.String("name", "", "bank name")
		typ := fs.String("type", "commercial", "commercial, digital or microfinance")
		id := fs.String("id", "", "bank ID, derived from the name when empty")
		nip := fs.String("nip-code", "", "6-digit NIP institution code, assigned when empty")
		nuban := fs.String("nuban-code", "", "3-digit NUBAN bank code, assigned when empty")
		days := fs.Int("history-days", 0, "days of history the bank serves")
		via := fs.String("settles-through", "", "bank a microfinance bank settles through")
		fs.Parse(args[1:])
		if *name == "" {
			return nil, errors.New("usage: beerus bank create --name NAME")
		}
		out, err := c.do("POST", "/banks", map[string]any{"name": *name, "type": *typ, "id": *id, "nip_code": *nip, "nuban_code": *nuban, "history_days": *days, "settles_through": *via})
		if err != nil {
			return nil, err
		}
		return waitBank(c, fmt.Sprint(out["id"]))
	case (args[0] == "suspend" || args[0] == "resume") && len(args) == 2:
		return c.do("POST", "/banks/"+args[1]+"/"+args[0], nil)
	case args[0] == "close" && len(args) >= 2:
		fs := flag.NewFlagSet("bank close", flag.ExitOnError)
		dry := fs.Bool("dry-run", false, "show what closing the bank would affect, without closing it")
		money := fs.String("money", "transfer", "what happens to customers' money: transfer, payout or leave")
		to := fs.String("to", "", "bank that receives transferred or paid-out money")
		each := fs.String("customers", "", "choices for single customers, such as adaeze=payout,tunde=leave")
		yes := fs.Bool("yes", false, "close the bank; this cannot be undone")
		fs.Parse(args[2:])
		if *dry || !*yes {
			out, err := c.do("GET", "/banks/"+args[1]+"/close", nil)
			if err == nil && !*dry {
				out["next"] = "Nothing was closed. Run again with --yes to close the bank; closing cannot be undone."
			}
			return out, err
		}
		customers := map[string]string{}
		for _, kv := range strings.Split(*each, ",") {
			if kv == "" {
				continue
			}
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, errors.New("--customers takes persona=choice pairs, such as adaeze=payout")
			}
			customers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		return c.do("POST", "/banks/"+args[1]+"/close", map[string]any{"money": *money, "to": *to, "customers": customers})
	case args[0] == "purge" && len(args) >= 2:
		fs := flag.NewFlagSet("bank purge", flag.ExitOnError)
		yes := fs.Bool("yes", false, "purge the bank; this cannot be undone")
		fs.Parse(args[2:])
		if !*yes {
			out, err := c.do("GET", "/banks/"+args[1]+"/purge", nil)
			if err == nil {
				out["next"] = "Nothing was purged. Run again with --yes to delete the bank's database, its snapshot copies and its beerus.yaml entry; purging cannot be undone."
			}
			return out, err
		}
		return c.do("POST", "/banks/"+args[1]+"/purge", nil)
	case args[0] == "open-account" && len(args) >= 2:
		fs := flag.NewFlagSet("bank open-account", flag.ExitOnError)
		persona := fs.String("persona", "", "persona who opens the account")
		typ := fs.String("type", "current", "current or savings")
		fs.Parse(args[2:])
		if *persona == "" {
			return nil, errors.New("usage: beerus bank open-account <id> --persona ID [--type current|savings]")
		}
		return c.do("POST", "/banks/"+args[1]+"/accounts", map[string]string{"persona": *persona, "type": *typ})
	}
	return nil, errors.New("usage: beerus bank [list [--state STATE] | create --name NAME | suspend <id> | resume <id> | open-account <id> --persona ID | close <id> --to BANK [--money transfer|payout|leave] --yes | purge <id> --yes]")
}

func waitBank(c *ctl, id string) (map[string]any, error) {
	deadline := time.Now().Add(90 * time.Second)
	for {
		out, err := c.do("GET", "/banks", nil)
		if err != nil {
			return nil, err
		}
		data, _ := out["data"].([]any)
		for _, d := range data {
			st, _ := d.(map[string]any)
			if st["id"] != id {
				continue
			}
			if st["phase"] != "provisioning" || time.Now().After(deadline) {
				return st, nil
			}
		}
		if time.Now().After(deadline) {
			return out, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}
