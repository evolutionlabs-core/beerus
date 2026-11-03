package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

type multi []string

func (m *multi) String() string {
	return strings.Join(*m, ",")
}

func (m *multi) Set(v string) error {
	*m = append(*m, v)
	return nil
}

const partnerUsage = "usage: beerus partner add <id> --redirect-uri URL [--webhook-url URL] | list | rotate-key <id> | remove <id>"

func cmdPartner(args []string) error {
	if len(args) == 0 {
		return errors.New(partnerUsage)
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("partner "+sub, flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	var redirects multi
	fs.Var(&redirects, "redirect-uri", "where the widget sends the browser back to (repeatable)")
	hook := fs.String("webhook-url", "", "where webhooks are sent")
	name := fs.String("name", "", "app name shown on the consent screen")
	legal := fs.String("legal-name", "", "legal name shown on the consent screen")
	purpose := fs.String("purpose", "", "purpose shown on the consent screen")
	id := ""
	if sub != "list" {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return errors.New(partnerUsage)
		}
		id, rest = rest[0], rest[1:]
	}
	fs.Parse(rest)
	c, err := config.Load(*dir)
	if err != nil {
		return fmt.Errorf("%w (run beerus init or beerus up first)", err)
	}
	switch sub {
	case "add":
		key, err := c.AddPartner(config.PartnerOptions{ID: id, Name: *name, LegalName: *legal, Purpose: *purpose, RedirectURIs: redirects, WebhookURL: *hook})
		if err != nil {
			return err
		}
		fmt.Printf("Registered %s.\n\n", id)
		return printCredentials(c, id, key)
	case "rotate-key":
		key, err := c.RotatePartner(id)
		if err != nil {
			return err
		}
		fmt.Printf("Issued new credentials for %s. The old API key and certificate stop working once Beerus reloads.\n\n", id)
		return printCredentials(c, id, key)
	case "remove":
		if err := c.RemovePartner(id); err != nil {
			return err
		}
		fmt.Printf("Removed %s. Its key and certificate no longer work.\n", id)
		reload(c)
		return nil
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
	return errors.New(partnerUsage)
}

func printCredentials(c *config.Config, id, key string) error {
	k, err := config.WebhookKey(c)
	if err != nil {
		return err
	}
	p := c.Partner(id)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "API key\t%s\t(shown once; also in %s)\n", key, c.Path("partners", id, "api-key"))
	fmt.Fprintf(w, "Partner API\t%s\n", c.PartnerURL())
	fmt.Fprintf(w, "Client certificate\t%s\n", c.Path("partners", id, "client.crt"))
	fmt.Fprintf(w, "Client key\t%s\n", c.Path("partners", id, "client.key"))
	fmt.Fprintf(w, "Server CA\t%s\n", c.Path("partners", id, "ca.crt"))
	fmt.Fprintf(w, "Redirect URIs\t%s\n", strings.Join(p.RedirectURIs, ", "))
	fmt.Fprintf(w, "Webhook URL\t%s\n", p.WebhookURL)
	fmt.Fprintf(w, "Webhook public key\t%s\n", k.PublicWH())
	if err := w.Flush(); err != nil {
		return err
	}
	reload(c)
	return nil
}

func reload(c *config.Config) {
	tok, err := config.ReadSecret(c.Path("control-token"))
	if err != nil {
		return
	}
	req, _ := http.NewRequest("POST", c.ControlURL()+"/partners/reload", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		fmt.Println("\nBeerus is not running; the change applies when you start it.")
		return
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		fmt.Println("\nThe running Beerus picked up the change.")
	}
}
