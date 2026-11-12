package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
)

type multi []string

func (m *multi) String() string {
	return strings.Join(*m, ",")
}

func (m *multi) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func cmdPartner(args []string) error {
	if len(args) == 0 {
		return usageError("partner")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add", "list", "rotate-key", "remove":
	default:
		return usageError("partner")
	}
	path := "partner " + sub
	fs := flag.NewFlagSet(path, flag.ContinueOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	var redirects multi
	hook := new(string)
	name := new(string)
	legal := new(string)
	purpose := new(string)
	if sub == "add" {
		fs.Var(&redirects, "redirect-uri", "`URL` where the widget sends the browser back to (repeatable)")
		fs.StringVar(hook, "webhook-url", "", "`URL` where webhooks are sent")
		fs.StringVar(name, "name", "", "app name shown on the consent screen")
		fs.StringVar(legal, "legal-name", "", "legal `name` shown on the consent screen")
		fs.StringVar(purpose, "purpose", "", "purpose shown on the consent screen")
	}
	positional := 1
	if sub == "list" {
		positional = 0
	}
	pos, err := parseAfter(path, fs, rest, positional)
	if err != nil {
		return err
	}
	id := ""
	if positional == 1 {
		id = pos[0]
	}
	c, err := connect.LoadConfig(*dir)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		if len(c.Partners) == 0 {
			fmt.Println("No partner apps. Add one with beerus partner add.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tREDIRECT URIS\tWEBHOOK URL")
		for _, p := range c.Partners {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Name, strings.Join(p.RedirectURIs, " "), p.WebhookURL)
		}
		return w.Flush()
	}
	ctl, err := newCtlFor(c)
	if err != nil {
		return err
	}
	switch sub {
	case "add":
		out, err := ctl.do("POST", "/partners", map[string]any{"id": id, "name": *name, "legal_name": *legal, "purpose": *purpose, "redirect_uris": []string(redirects), "webhook_url": *hook})
		if err != nil {
			return err
		}
		fmt.Printf("Registered %s.\n\n", id)
		return printCredentials(*dir, id, fmt.Sprint(out["api_key"]))
	case "rotate-key":
		out, err := ctl.do("POST", "/partners/"+url.PathEscape(id)+"/rotate-key", nil)
		if err != nil {
			return err
		}
		fmt.Printf("Issued new credentials for %s. The old API key and certificate no longer work.\n\n", id)
		return printCredentials(*dir, id, fmt.Sprint(out["api_key"]))
	case "remove":
		if _, err := ctl.do("DELETE", "/partners/"+url.PathEscape(id), nil); err != nil {
			return err
		}
		fmt.Printf("Removed %s. Its key and certificate no longer work.\n", id)
		return nil
	}
	return usageError("partner")
}

func printCredentials(dir, id, key string) error {
	c, err := connect.LoadConfig(dir)
	if err != nil {
		return err
	}
	k, err := config.WebhookKey(c)
	if err != nil {
		return err
	}
	p := c.Partner(id)
	if p == nil {
		return fmt.Errorf("partner %s is not in the setup", id)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "API key\t%s\t(shown once; also in %s)\n", key, c.Path("partners", id, "api-key"))
	fmt.Fprintf(w, "Partner API\t%s\n", c.PartnerURL())
	fmt.Fprintf(w, "Client certificate\t%s\n", c.Path("partners", id, "client.crt"))
	fmt.Fprintf(w, "Client key\t%s\n", c.Path("partners", id, "client.key"))
	fmt.Fprintf(w, "Server CA\t%s\n", c.Path("partners", id, "ca.crt"))
	fmt.Fprintf(w, "Redirect URIs\t%s\n", strings.Join(p.RedirectURIs, ", "))
	fmt.Fprintf(w, "Webhook URL\t%s\n", p.WebhookURL)
	fmt.Fprintf(w, "Webhook public key\t%s\n", k.PublicWH())
	return w.Flush()
}
