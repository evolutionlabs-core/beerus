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
	"text/tabwriter"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

var version = "0.1.0"

const defaultSetupName = "beerus"

func main() {
	os.Exit(report(os.Stdout, os.Stderr, dispatch(os.Args[1:])))
}

func cmdVersion(args []string) error {
	if _, err := noFlags("version", args, 0); err != nil {
		return err
	}
	fmt.Println("beerus", version)
	return nil
}

func defaultDir() string {
	if d := os.Getenv("BEERUS_DIR"); d != "" {
		return d
	}
	return ".beerus"
}

func cmdInit(args []string) error {
	c, err := initState(args)
	if err != nil {
		return err
	}
	return printInfo(c)
}

func initState(args []string) (*config.Config, error) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	file := fs.String("f", "", "setup `FILE` to start from")
	flagged := connect.DefaultLocal().Connect
	fs.IntVar(&flagged.PartnerPort, "partner-port", flagged.PartnerPort, "partner API `port`; banks take the next free ports")
	fs.IntVar(&flagged.WidgetPort, "widget-port", flagged.WidgetPort, "widget `port`")
	fs.IntVar(&flagged.ControlPort, "control-port", flagged.ControlPort, "control API `port`")
	fs.IntVar(&flagged.HealthPort, "health-port", flagged.HealthPort, "health `port` of the cbn container")
	if err := parseFlags("init", fs, args); err != nil {
		return nil, err
	}
	local, err := connect.FreshLocal(*dir)
	if err != nil {
		return nil, err
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
	spec := setup.DefaultSpec(defaultSetupName, today)
	if *file != "" {
		if spec, err = readSpec(*file); err != nil {
			return nil, err
		}
	}
	if err := connect.Init(*dir, spec, local, today); err != nil {
		return nil, err
	}
	c, err := connect.LoadConfig(*dir)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Created %s. The setup is in %s; machine settings are in %s.\n\n", c.Dir, c.Path(connect.SpecFile), c.Path(connect.LocalFile))
	return c, nil
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
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	if _, err := parseAfter("info", fs, args, 0); err != nil {
		return err
	}
	c, err := connect.LoadConfig(*dir)
	if err != nil {
		return err
	}
	return printInfo(c)
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	fix := fs.Bool("fix", false, "drop leftover databases, delete snapshots that can no longer be restored and forget banks that no longer exist")
	if _, err := parseAfter("doctor", fs, args, 0); err != nil {
		return err
	}
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

func cmdConnections(args []string) error {
	if _, err := noFlags("connections", args, 0); err != nil {
		return err
	}
	return printControl("GET", "/connections")
}

func cmdRevoke(args []string) error {
	pos, err := noFlags("revoke", args, 1)
	if err != nil {
		return err
	}
	return printControl("POST", "/connections/"+url.PathEscape(pos[0])+"/revoke")
}

func cmdSync(args []string) error {
	if _, err := noFlags("sync", args, 0); err != nil {
		return err
	}
	return printControl("POST", "/sync")
}

func cmdWebhooks(args []string) error {
	if len(args) == 0 {
		return cmdWebhooks([]string{"list"})
	}
	switch args[0] {
	case "list":
		if _, err := noFlags("webhooks list", args, 1); err != nil {
			return err
		}
		return printControl("GET", "/webhooks/deliveries?limit=20")
	case "replay":
		pos, err := noFlags("webhooks replay", args, 2)
		if err != nil {
			return err
		}
		return printControl("POST", "/webhooks/deliveries/"+url.PathEscape(pos[1])+"/replay")
	}
	return usageError("webhooks")
}

func cmdSnapshot(args []string) error {
	if len(args) == 0 {
		return cmdSnapshot([]string{"list"})
	}
	switch args[0] {
	case "list":
		if _, err := noFlags("snapshot list", args, 1); err != nil {
			return err
		}
		return printControl("GET", "/snapshots")
	case "take":
		if helpRequested(args[1:]) {
			return leafHelp("snapshot take", flag.NewFlagSet("snapshot take", flag.ContinueOnError))
		}
		if len(args) > 2 {
			return usageError("snapshot take")
		}
		name := ""
		if len(args) == 2 {
			name = args[1]
		}
		return printControlBody("POST", "/snapshots", map[string]string{"name": name})
	case "restore":
		pos, err := noFlags("snapshot restore", args, 2)
		if err != nil {
			return err
		}
		return printControl("POST", "/snapshots/"+url.PathEscape(pos[1])+"/restore")
	}
	return usageError("snapshot")
}

func printControl(method, path string) error {
	return printControlBody(method, path, nil)
}

func printControlBody(method, path string, body any) error {
	c, err := newCtl()
	if err != nil {
		return err
	}
	out, err := c.do(method, path, body)
	if err != nil {
		return err
	}
	pretty(out)
	return nil
}

func cmdRegistry(args []string) error {
	if len(args) == 0 {
		return cmdRegistry([]string{"list"})
	}
	switch args[0] {
	case "list":
		if _, err := noFlags("registry list", args, 1); err != nil {
			return err
		}
		return printControl("GET", "/registry/clients")
	case "consents":
		fs := flag.NewFlagSet("registry consents", flag.ContinueOnError)
		bank := fs.String("bank", "", "only consent changes recorded by this bank")
		if _, err := parseAfter("registry consents", fs, args, 1); err != nil {
			return err
		}
		return printControl("GET", "/registry/consents?bank="+url.QueryEscape(*bank))
	case "add-client":
		return cmdRegistryAdd(args)
	}
	return usageError("registry")
}

func cmdRegistryAdd(args []string) error {
	fs := flag.NewFlagSet("registry add-client", flag.ContinueOnError)
	cn := fs.String("cert-cn", "", "common name (`CN`) of the client's mTLS certificate")
	keyFile := fs.String("public-key", "", "PEM `file` with the client's EC P-256 public key, used to check its private_key_jwt")
	name := fs.String("name", "", "brand name")
	legal := fs.String("legal-name", "", "legal `name`")
	cac := fs.String("cac", "", "CAC number")
	var redirects multi
	fs.Var(&redirects, "redirect-uri", "`URL` where the bank sends the browser back to (repeatable)")
	pos, err := parseAfter("registry add-client", fs, args, 2)
	if err != nil {
		return err
	}
	if *cn == "" || *keyFile == "" || len(redirects) == 0 {
		return usageError("registry add-client")
	}
	pem, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	return printControlBody("POST", "/registry/clients", map[string]any{"id": pos[1], "cert_cn": *cn, "public_key_pem": string(pem), "name": *name, "legal_name": *legal, "cac": *cac, "redirect_uris": []string(redirects)})
}

func cmdBankGroup(args []string) error {
	if len(args) == 0 {
		return cmdBankList([]string{"list"})
	}
	switch args[0] {
	case "close", "purge":
		return cmdBankEnd(newCLI(), args)
	case "list":
		return cmdBankList(args)
	case "create":
		return cmdBankCreate(args)
	case "suspend", "resume":
		pos, err := noFlags("bank "+args[0], args, 2)
		if err != nil {
			return err
		}
		return printControl("POST", "/banks/"+url.PathEscape(pos[1])+"/"+args[0])
	}
	return usageError("bank")
}

func cmdBankList(args []string) error {
	fs := flag.NewFlagSet("bank list", flag.ContinueOnError)
	state := fs.String("state", "", "only banks in this state: active, suspended, provisioning, repairing or closed")
	if _, err := parseAfter("bank list", fs, args, 1); err != nil {
		return err
	}
	if *state != "" {
		return printControl("GET", "/banks?state="+url.QueryEscape(*state))
	}
	return printControl("GET", "/banks")
}

func cmdBankCreate(args []string) error {
	fs := flag.NewFlagSet("bank create", flag.ContinueOnError)
	name := fs.String("name", "", "bank name")
	typ := fs.String("type", "commercial", "bank type: commercial, digital or microfinance")
	id := fs.String("id", "", "bank ID, derived from the name when empty")
	nip := fs.String("nip-code", "", "6-digit NIP institution `code`, assigned when empty")
	nuban := fs.String("nuban-code", "", "3-digit NUBAN bank `code`, assigned when empty")
	via := fs.String("settles-through", "", "`bank` that a microfinance bank settles through")
	if _, err := parseAfter("bank create", fs, args, 1); err != nil {
		return err
	}
	if *name == "" {
		return usageError("bank create")
	}
	c, err := newCtl()
	if err != nil {
		return err
	}
	out, err := c.do("POST", "/banks", map[string]any{"name": *name, "type": *typ, "id": *id, "nip_code": *nip, "nuban_code": *nuban, "settles_through": *via})
	if err != nil {
		return err
	}
	out, err = waitBank(c, fmt.Sprint(out["id"]))
	if err != nil {
		return err
	}
	pretty(out)
	return nil
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
