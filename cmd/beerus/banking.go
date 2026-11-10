package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

func bankPath(bank string, parts ...string) (string, error) {
	if bank == "" {
		return "", errors.New("add --bank with the bank's ID, such as --bank bank-a")
	}
	p := "/banks/" + url.PathEscape(bank)
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p, nil
}

func flagsAfter(fs *flag.FlagSet, args []string, positional int) ([]string, error) {
	if len(args) < positional {
		return nil, errors.New("missing argument")
	}
	if err := fs.Parse(args[positional:]); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return args[:positional], nil
}

func readPassword(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the password from stdin: %w", err)
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", errors.New("--password-stdin read an empty password")
	}
	return pw, nil
}

func cmdCustomer(c *ctl, args []string) (map[string]any, error) {
	const usage = "usage: beerus customer create --bank ID --name N --phone P --dob YYYY-MM-DD [--username U --password-stdin] | close <id> --bank ID"
	fs := flag.NewFlagSet("customer", flag.ContinueOnError)
	bank := fs.String("bank", "", "bank ID")
	switch {
	case len(args) > 0 && args[0] == "create":
		name := fs.String("name", "", "full name")
		phone := fs.String("phone", "", "phone number")
		dob := fs.String("dob", "", "date of birth, YYYY-MM-DD")
		user := fs.String("username", "", "online banking username")
		pwIn := fs.Bool("password-stdin", false, "read the online banking password from stdin")
		if _, err := flagsAfter(fs, args, 1); err != nil {
			return nil, fmt.Errorf("%w; %s", err, usage)
		}
		body := map[string]string{"name": *name, "phone": *phone, "dob": *dob, "username": *user}
		if *pwIn {
			pw, err := readPassword(os.Stdin)
			if err != nil {
				return nil, err
			}
			body["password"] = pw
		}
		p, err := bankPath(*bank, "customers")
		if err != nil {
			return nil, err
		}
		return c.do("POST", p, body)
	case len(args) > 1 && args[0] == "close":
		pos, err := flagsAfter(fs, args, 2)
		if err != nil {
			return nil, fmt.Errorf("%w; %s", err, usage)
		}
		p, err := bankPath(*bank, "customers", pos[1], "close")
		if err != nil {
			return nil, err
		}
		return c.do("POST", p, nil)
	}
	return nil, errors.New(usage)
}

func cmdAccount(c *ctl, args []string) (map[string]any, error) {
	const usage = "usage: beerus account open --bank ID --customer ID [--type current|savings] | close <nuban> --bank ID"
	fs := flag.NewFlagSet("account", flag.ContinueOnError)
	bank := fs.String("bank", "", "bank ID")
	switch {
	case len(args) > 0 && args[0] == "open":
		cust := fs.String("customer", "", "customer ID")
		typ := fs.String("type", "current", "current or savings")
		if _, err := flagsAfter(fs, args, 1); err != nil {
			return nil, fmt.Errorf("%w; %s", err, usage)
		}
		p, err := bankPath(*bank, "accounts")
		if err != nil {
			return nil, err
		}
		return c.do("POST", p, map[string]string{"customer": *cust, "type": *typ})
	case len(args) > 1 && args[0] == "close":
		pos, err := flagsAfter(fs, args, 2)
		if err != nil {
			return nil, fmt.Errorf("%w; %s", err, usage)
		}
		p, err := bankPath(*bank, "accounts", pos[1], "close")
		if err != nil {
			return nil, err
		}
		return c.do("POST", p, nil)
	}
	return nil, errors.New(usage)
}

func listQuery(limit, cursor string, more url.Values) string {
	q := url.Values{}
	for k, v := range more {
		if len(v) > 0 && v[0] != "" {
			q[k] = v
		}
	}
	if limit != "" {
		q.Set("limit", limit)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func cmdList(c *ctl, what string, args []string) (map[string]any, error) {
	fs := flag.NewFlagSet(what, flag.ContinueOnError)
	bank := fs.String("bank", "", "bank ID")
	limit := fs.String("limit", "", "page size")
	cursor := fs.String("cursor", "", "next_cursor from the last page")
	cust := fs.String("customer", "", "only this customer's accounts")
	from := fs.String("from", "", "first date, YYYY-MM-DD")
	to := fs.String("to", "", "last date, YYYY-MM-DD")
	positional := 0
	if what == "statement" {
		positional = 1
	}
	pos, err := flagsAfter(fs, args, positional)
	if err != nil {
		return nil, fmt.Errorf("%w; usage: beerus %s --bank ID", err, what)
	}
	var p string
	switch what {
	case "customers":
		p, err = bankPath(*bank, "customers")
	case "accounts":
		p, err = bankPath(*bank, "accounts")
	default:
		p, err = bankPath(*bank, "accounts", pos[0], "statement")
	}
	if err != nil {
		return nil, err
	}
	return c.do("GET", p+listQuery(*limit, *cursor, url.Values{"customer": {*cust}, "from": {*from}, "to": {*to}}), nil)
}

func cmdCash(c *ctl, kind string, args []string) (map[string]any, error) {
	fs := flag.NewFlagSet(kind, flag.ContinueOnError)
	bank := fs.String("bank", "", "bank ID")
	amount := fs.String("amount", "", "amount in naira, such as 5000 or 5000.50")
	pos, err := flagsAfter(fs, args, 1)
	if err != nil {
		return nil, fmt.Errorf("%w; usage: beerus %s <nuban> --bank ID --amount N", err, kind)
	}
	path := "deposits"
	if kind == "withdraw" {
		path = "withdrawals"
	}
	p, err := bankPath(*bank, path)
	if err != nil {
		return nil, err
	}
	return c.do("POST", p, map[string]string{"account": pos[0], "amount": *amount})
}

func cmdTransfer(c *ctl, args []string) (map[string]any, error) {
	fs := flag.NewFlagSet("transfer", flag.ContinueOnError)
	fromBank := fs.String("from-bank", "", "bank of the paying account")
	from := fs.String("from", "", "account number to pay from")
	toBank := fs.String("to-bank", "", "bank of the receiving account; the paying bank when empty")
	to := fs.String("to", "", "account number to pay into")
	amount := fs.String("amount", "", "amount in naira, such as 25000 or 2500.50")
	narration := fs.String("narration", "", "narration")
	if _, err := flagsAfter(fs, args, 0); err != nil {
		return nil, err
	}
	if *from == "" || *to == "" || *amount == "" {
		return nil, errors.New("usage: beerus transfer --from-bank ID --from NUBAN --to NUBAN [--to-bank ID] --amount N [--narration T]")
	}
	p, err := bankPath(*fromBank, "transfers")
	if err != nil {
		return nil, err
	}
	return c.do("POST", p, map[string]string{"from": *from, "to_bank": *toBank, "to": *to, "amount": *amount, "narration": *narration})
}

func cmdFault(c *ctl, args []string) (map[string]any, error) {
	const usage = "usage: beerus fault add down --bank ID [--for 1h] | add timeout --bank ID --outcome credited|failed|unknown [--count N] | clear [--bank ID]"
	fs := flag.NewFlagSet("fault", flag.ContinueOnError)
	bank := fs.String("bank", "", "bank ID")
	switch {
	case len(args) > 1 && args[0] == "add":
		dur := fs.String("for", "", "how long a down fault lasts, such as 1h")
		outcome := fs.String("outcome", "", "what really happened to a timed-out transfer")
		count := fs.Int("count", 0, "how many transfers time out")
		if _, err := flagsAfter(fs, args, 2); err != nil {
			return nil, fmt.Errorf("%w; %s", err, usage)
		}
		body := map[string]any{"kind": args[1], "bank": *bank, "for": *dur, "outcome": *outcome}
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "count" {
				body["count"] = *count
			}
		})
		return c.do("POST", "/faults", body)
	case len(args) > 0 && args[0] == "clear":
		if _, err := flagsAfter(fs, args, 1); err != nil {
			return nil, fmt.Errorf("%w; %s", err, usage)
		}
		return c.do("DELETE", "/faults"+listQuery("", "", url.Values{"bank": {*bank}}), nil)
	}
	return nil, errors.New(usage)
}
