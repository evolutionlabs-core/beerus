package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

var version = "0.1.0"

const defaultSetupName = "beerus"

const usage = `Beerus simulates a Nigerian banking ecosystem on your machine.

Usage:
  beerus init [--dir .beerus] [--partner-port 7443] [--widget-port 7444] [--control-port 7480] [--health-port 7490]
  beerus up [-f file] [--prune] [--dir .beerus]
  beerus apply [-f file] [--prune] [--money transfer|payout|leave --to BANK --customer ID=CHOICE] [--settle-now] [--wait 5m] [--json]
  beerus diff [-f file] [--json]
  beerus get banks|partners [--limit N] [--cursor C] [--json]
  beerus describe bank|partner <id> [--json]
  beerus export [-o file] [--json]
  beerus verify [--json]
  beerus run --participant cbn|sim|bank-a|bank-b [--dir .beerus] [--bind 0.0.0.0] [--core URL]
  beerus compose [--dir .beerus]
  beerus health URL [--token-file FILE]
  beerus info [--dir .beerus]
  beerus partner add <id> --redirect-uri URL [--redirect-uri URL] [--webhook-url URL] [--name N] [--legal-name N] [--purpose P]
  beerus partner list
  beerus partner rotate-key <id>
  beerus partner remove <id>
  beerus clock
  beerus scenarios
  beerus scenario run <file.yaml>
  beerus scenario <name> --connection ID
  beerus use <bank>
  beerus customer create [--bank ID] --name N --phone P --dob YYYY-MM-DD [--username U --password-stdin] | close <id>
  beerus customers [--bank ID] [--limit N] [--cursor C]
  beerus account open [--bank ID] --customer ID --type current|savings | close <nuban>
  beerus accounts [--bank ID] [--customer ID] [--limit N] [--cursor C]
  beerus deposit|withdraw <nuban> [--bank ID] --amount NAIRA
  beerus statement <nuban> [--bank ID] [--from YYYY-MM-DD] [--to YYYY-MM-DD] [--limit N] [--cursor C]
  beerus transfer --from NUBAN --to NUBAN --amount NAIRA [--from-bank ID] [--to-bank ID] [--narration TEXT]
  beerus nip status <session_id> | settle-now | name-enquiry <bank_code> <nuban> | participants | positions | settlement
  beerus fault down [--bank ID] [--for 1h] | timeout [--bank ID] --outcome credited|failed|unknown [--count N] | list | clear [--bank ID]
  beerus connections
  beerus revoke <connection-id>
  beerus sync
  beerus webhooks [replay <delivery-id>]
  beerus snapshot [take [name] | restore <id>]
  beerus bank [list]
  beerus bank create --name NAME [--type commercial|digital|microfinance] [--id ID] [--nip-code N] [--nuban-code N] [--settles-through ID]
  beerus bank suspend|resume <id>
  beerus bank close <id> [--money transfer|payout|leave --to BANK --customer ID=CHOICE] [--settle-now] [--wait 5m] [--yes] [--json]
  beerus bank purge <id> [--money transfer|payout|leave --to BANK --customer ID=CHOICE] [--settle-now] [--wait 5m] [--yes] [--json]
  beerus registry [list | add-client <id> --cert-cn CN --public-key FILE --redirect-uri URL [--name N] [--legal-name N] [--cac N] | consents [--bank ID]]
  beerus doctor [--dir .beerus] [--fix]
  beerus reset <setup-name> [--dir .beerus] [--json]
  beerus trust [--dir .beerus]
  beerus untrust [--dir .beerus]
  beerus version

The state directory defaults to .beerus, or $BEERUS_DIR when set.
Banking commands use the bank chosen by beerus use when --bank is left out, and take --json to print the API response.
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
	case "apply":
		os.Exit(runSetup(cmdApply, args))
	case "diff":
		os.Exit(runSetup(cmdDiff, args))
	case "get":
		os.Exit(runSetup(cmdGet, args))
	case "describe":
		os.Exit(runSetup(cmdDescribe, args))
	case "export":
		os.Exit(runSetup(cmdExport, args))
	case "verify":
		os.Exit(runSetup(cmdVerify, args))
	case "bank":
		if len(args) > 0 && (args[0] == "close" || args[0] == "purge") {
			os.Exit(runSetup(cmdBankEnd, args))
		}
		err = cmdControl(cmd, args)
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
	case "reset":
		err = cmdReset(args, os.Stdout)
	case "partner":
		err = cmdPartner(args)
	case "trust":
		err = cmdTrust(args, true)
	case "untrust":
		err = cmdTrust(args, false)
	case "use":
		err = cmdUse(args)
	case "customer", "customers", "account", "accounts", "statement", "deposit", "withdraw", "transfer", "fault", "nip":
		err = cmdBanking(cmd, args)
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
	flagged := connect.DefaultLocal().Connect
	fs.IntVar(&flagged.PartnerPort, "partner-port", flagged.PartnerPort, "partner API port; banks take the next free ports")
	fs.IntVar(&flagged.WidgetPort, "widget-port", flagged.WidgetPort, "widget port")
	fs.IntVar(&flagged.ControlPort, "control-port", flagged.ControlPort, "control API port")
	fs.IntVar(&flagged.HealthPort, "health-port", flagged.HealthPort, "health port of the cbn container")
	fs.Parse(args)
	local, err := connect.FreshLocal(*dir)
	if err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "partner-port":
			local.Connect.PartnerPort = flagged.PartnerPort
		case "widget-port":
			local.Connect.WidgetPort = flagged.WidgetPort
		case "control-port":
			local.Connect.ControlPort = flagged.ControlPort
		case "health-port":
			local.Connect.HealthPort = flagged.HealthPort
		}
	})
	today := clock.DateOf(time.Now())
	if err := connect.Init(*dir, setup.DefaultSpec(defaultSetupName, today), local, today); err != nil {
		return err
	}
	c, err := connect.LoadConfig(*dir)
	if err != nil {
		return err
	}
	fmt.Printf("Created %s. The setup is in %s; machine settings are in %s.\n\n", c.Dir, c.Path(connect.SpecFile), c.Path(connect.LocalFile))
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
	c, err := connect.LoadConfig(*dir)
	if err != nil {
		return err
	}
	return printInfo(c)
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	fix := fs.Bool("fix", false, "drop leftover databases, delete snapshots that can no longer be restored and forget banks that no longer exist")
	fs.Parse(args)
	c, err := connect.LoadConfig(*dir)
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
	list("Status entries for banks that no longer exist:", rep.StaleOwned)
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

type ctl struct {
	base  string
	token string
}

func newCtl() (*ctl, error) {
	c, err := connect.LoadConfig(defaultDir())
	if err != nil {
		return nil, fmt.Errorf("%w (run beerus up or set BEERUS_DIR)", err)
	}
	return newCtlFor(c)
}

func newCtlFor(c *config.Config) (*ctl, error) {
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
		if len(args) != 0 {
			return errors.New("usage: beerus clock")
		}
		out, err = c.do("GET", "/clock", nil)
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
		conn := fs.String("connection", "", "connection ID")
		fs.Parse(args[1:])
		out, err = c.do("POST", "/scenarios/"+args[0], map[string]any{"connection_id": *conn})
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
		via := fs.String("settles-through", "", "bank a microfinance bank settles through")
		fs.Parse(args[1:])
		if *name == "" {
			return nil, errors.New("usage: beerus bank create --name NAME")
		}
		out, err := c.do("POST", "/banks", map[string]any{"name": *name, "type": *typ, "id": *id, "nip_code": *nip, "nuban_code": *nuban, "settles_through": *via})
		if err != nil {
			return nil, err
		}
		return waitBank(c, fmt.Sprint(out["id"]))
	case (args[0] == "suspend" || args[0] == "resume") && len(args) == 2:
		return c.do("POST", "/banks/"+args[1]+"/"+args[0], nil)
	}
	return nil, errors.New("usage: beerus bank [list [--state STATE] | create --name NAME | suspend <id> | resume <id> | close <id> | purge <id>]")
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
