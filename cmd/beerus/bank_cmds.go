package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

type cmdFlags struct {
	fs     *flag.FlagSet
	bank   *string
	asJSON *bool
}

func newFlags(name string, withBank bool) *cmdFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := &cmdFlags{fs: fs, bank: new(string), asJSON: fs.Bool("json", false, "print the API response as JSON")}
	if withBank {
		fs.StringVar(f.bank, "bank", "", "bank ID; the session bank when empty")
	}
	return f
}

func (f *cmdFlags) parse(args []string, positional int, usage string) ([]string, error) {
	if len(args) < positional {
		return nil, errors.New(usage)
	}
	for _, a := range args[:positional] {
		if strings.HasPrefix(a, "-") {
			return nil, fmt.Errorf("%q should be a value, not a flag; %s", a, usage)
		}
	}
	if err := f.fs.Parse(args[positional:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, errors.New(usage)
		}
		return nil, fmt.Errorf("%w; %s", err, usage)
	}
	if f.fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q; %s", f.fs.Arg(0), usage)
	}
	return args[:positional], nil
}

func (f *cmdFlags) dial() (string, *ctl, error) {
	id, err := resolveBank(*f.bank)
	if err != nil {
		return "", nil, err
	}
	c, err := newCtl()
	return id, c, err
}

func (f *cmdFlags) show(out map[string]any, human func() error) error {
	if *f.asJSON {
		pretty(out)
		return nil
	}
	return human()
}

func decodeMap[T any](m map[string]any) (T, error) {
	var v T
	raw, err := json.Marshal(m)
	if err != nil {
		return v, fmt.Errorf("read the response: %w", err)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("read the response: %w", err)
	}
	return v, nil
}

func decodeList[T any](m map[string]any) ([]T, error) {
	raw, err := json.Marshal(m["data"])
	if err != nil {
		return nil, fmt.Errorf("read the response: %w", err)
	}
	var v []T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("read the response: %w", err)
	}
	return v, nil
}

func businessNow(c *ctl) (time.Time, error) {
	out, err := c.do("GET", "/clock", nil)
	if err != nil {
		return time.Time{}, err
	}
	v, err := decodeMap[struct {
		At time.Time `json:"business_time"`
	}](out)
	return v.At, err
}

func stamp(t time.Time) string {
	return t.In(clock.Lagos).Format("Mon 2 Jan 2006 15:04 MST")
}

func hm(t time.Time) string {
	return t.In(clock.Lagos).Format("15:04 MST")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func printBusinessTime(c *ctl) error {
	now, err := businessNow(c)
	if err != nil {
		return err
	}
	fmt.Println("Business time:", stamp(now))
	return nil
}

func printNext(out map[string]any) {
	if next, ok := out["next_cursor"].(string); ok && next != "" {
		fmt.Printf("More results. Run again with --cursor %s to see the next page.\n", next)
	}
}

func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
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

func bankPath(bank string, parts ...string) string {
	p := "/banks/" + url.PathEscape(bank)
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p
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

func cmdBanking(cmd string, args []string) error {
	switch cmd {
	case "customer":
		return cmdCustomer(args)
	case "account":
		return cmdAccount(args)
	case "customers", "accounts", "statement":
		return cmdList(cmd, args)
	case "deposit", "withdraw":
		return cmdCash(cmd, args)
	case "transfer":
		return cmdTransfer(args)
	case "fault":
		return cmdFault(args)
	}
	return cmdNip(args)
}

func cmdCustomer(args []string) error {
	const usage = "usage: beerus customer create --name N --phone P --dob YYYY-MM-DD [--username U --password-stdin] | close <id>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	f := newFlags("customer", true)
	switch args[0] {
	case "create":
		name := f.fs.String("name", "", "full name")
		phone := f.fs.String("phone", "", "phone number")
		dob := f.fs.String("dob", "", "date of birth, YYYY-MM-DD")
		user := f.fs.String("username", "", "online banking username")
		pwIn := f.fs.Bool("password-stdin", false, "read the online banking password from stdin")
		if _, err := f.parse(args, 1, usage); err != nil {
			return err
		}
		if *name == "" || *phone == "" || *dob == "" {
			return fmt.Errorf("--name, --phone and --dob are required; %s", usage)
		}
		id, err := resolveBank(*f.bank)
		if err != nil {
			return err
		}
		body := map[string]string{"name": *name, "phone": *phone, "dob": *dob, "username": *user}
		if *pwIn {
			if body["password"], err = readPassword(os.Stdin); err != nil {
				return err
			}
		}
		c, err := newCtl()
		if err != nil {
			return err
		}
		out, err := c.do("POST", bankPath(id, "customers"), body)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			cu, err := decodeMap[bank.Customer](out)
			if err != nil {
				return err
			}
			fmt.Printf("Created customer %s, %s, at %s. BVN %s.\n", cu.ID, cu.Name, id, cu.BVN)
			if cu.Username != "" {
				fmt.Printf("%s can sign in to online banking as %s.\n", cu.Name, cu.Username)
			}
			fmt.Println("Business time:", stamp(cu.CreatedAt))
			return nil
		})
	case "close":
		pos, err := f.parse(args, 2, usage)
		if err != nil {
			return err
		}
		id, c, err := f.dial()
		if err != nil {
			return err
		}
		out, err := c.do("POST", bankPath(id, "customers", args[1], "close"), nil)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			fmt.Printf("Closed customer %s at %s.\n", pos[1], id)
			return printBusinessTime(c)
		})
	}
	return errors.New(usage)
}

func cmdAccount(args []string) error {
	const usage = "usage: beerus account open --customer ID [--type current|savings] | close <nuban>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	f := newFlags("account", true)
	switch args[0] {
	case "open":
		cust := f.fs.String("customer", "", "customer ID")
		typ := f.fs.String("type", "", "current or savings")
		if _, err := f.parse(args, 1, usage); err != nil {
			return err
		}
		if *cust == "" || *typ == "" {
			return fmt.Errorf("--customer and --type are required; %s", usage)
		}
		id, c, err := f.dial()
		if err != nil {
			return err
		}
		out, err := c.do("POST", bankPath(id, "accounts"), map[string]string{"customer": *cust, "type": *typ})
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			a, err := decodeMap[connect.AccountJSON](out)
			if err != nil {
				return err
			}
			fmt.Printf("Opened %s account %s for %s (%s) at %s. Balance %s.\n", a.Type, a.NUBAN, a.Name, a.Customer, id, connect.FormatNaira(a.Balance))
			return printBusinessTime(c)
		})
	case "close":
		pos, err := f.parse(args, 2, usage)
		if err != nil {
			return err
		}
		id, c, err := f.dial()
		if err != nil {
			return err
		}
		out, err := c.do("POST", bankPath(id, "accounts", pos[1], "close"), nil)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			fmt.Printf("Closed account %s at %s.\n", pos[1], id)
			return printBusinessTime(c)
		})
	}
	return errors.New(usage)
}

func cmdList(what string, args []string) error {
	usage := "usage: beerus " + what + " [--limit N] [--cursor C]"
	positional := 0
	f := newFlags(what, true)
	limit := f.fs.String("limit", "", "page size")
	cursor := f.fs.String("cursor", "", "next_cursor from the last page")
	var cust, from, to *string
	switch what {
	case "accounts":
		cust = f.fs.String("customer", "", "only this customer's accounts")
		usage = "usage: beerus accounts [--customer ID] [--limit N] [--cursor C]"
	case "statement":
		positional = 1
		from = f.fs.String("from", "", "first date, YYYY-MM-DD")
		to = f.fs.String("to", "", "last date, YYYY-MM-DD")
		usage = "usage: beerus statement <nuban> [--from YYYY-MM-DD] [--to YYYY-MM-DD] [--limit N] [--cursor C]"
	}
	pos, err := f.parse(args, positional, usage)
	if err != nil {
		return err
	}
	id, c, err := f.dial()
	if err != nil {
		return err
	}
	more := url.Values{}
	var path string
	switch what {
	case "customers":
		path = bankPath(id, "customers")
	case "accounts":
		path = bankPath(id, "accounts")
		more.Set("customer", *cust)
	default:
		path = bankPath(id, "accounts", pos[0], "statement")
		more.Set("from", *from)
		more.Set("to", *to)
	}
	out, err := c.do("GET", path+listQuery(*limit, *cursor, more), nil)
	if err != nil {
		return err
	}
	return f.show(out, func() error {
		switch what {
		case "customers":
			return printCustomers(c, out)
		case "accounts":
			return printAccounts(c, out)
		}
		return printStatement(c, out, pos[0])
	})
}

func printCustomers(c *ctl, out map[string]any) error {
	rows, err := decodeList[bank.Customer](out)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("No customers.")
		return printBusinessTime(c)
	}
	w := newTable()
	fmt.Fprintln(w, "ID\tNAME\tPHONE\tBVN\tSTATUS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Phone, r.BVN, r.Status)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("print customers: %w", err)
	}
	fmt.Println(plural(len(rows), "customer", "customers") + " on this page.")
	printNext(out)
	return printBusinessTime(c)
}

func printAccounts(c *ctl, out map[string]any) error {
	rows, err := decodeList[connect.AccountJSON](out)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("No accounts.")
		return printBusinessTime(c)
	}
	w := newTable()
	fmt.Fprintln(w, "NUBAN\tOWNER\tCUSTOMER\tTYPE\tSTATUS\tBALANCE")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.NUBAN, r.Name, r.Customer, r.Type, r.Status, connect.FormatNaira(r.Balance))
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("print accounts: %w", err)
	}
	fmt.Println(plural(len(rows), "account", "accounts") + " on this page.")
	printNext(out)
	return printBusinessTime(c)
}

func printStatement(c *ctl, out map[string]any, nuban string) error {
	rows, err := decodeList[connect.StatementLine](out)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Printf("No entries on account %s.\n", nuban)
		return printBusinessTime(c)
	}
	w := newTable()
	fmt.Fprintln(w, "DATE\tTIME\tAMOUNT\tBALANCE\tNARRATION\tSESSION")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ValueDate, hm(r.PostedAt), connect.FormatNaira(r.Amount), connect.FormatNaira(r.BalanceAfter), r.Narration, r.SessionID)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("print the statement: %w", err)
	}
	fmt.Println(plural(len(rows), "entry", "entries") + " on this page.")
	printNext(out)
	return printBusinessTime(c)
}

func cmdCash(kind string, args []string) error {
	usage := "usage: beerus " + kind + " <nuban> --amount NAIRA"
	f := newFlags(kind, true)
	amount := f.fs.String("amount", "", "amount in naira, such as 5000 or 5000.50")
	pos, err := f.parse(args, 1, usage)
	if err != nil {
		return err
	}
	if *amount == "" {
		return fmt.Errorf("--amount is required; %s", usage)
	}
	id, c, err := f.dial()
	if err != nil {
		return err
	}
	path := "deposits"
	if kind == "withdraw" {
		path = "withdrawals"
	}
	out, err := c.do("POST", bankPath(id, path), map[string]string{"account": pos[0], "amount": *amount})
	if err != nil {
		return err
	}
	return f.show(out, func() error {
		r, err := decodeMap[connect.CashResult](out)
		if err != nil {
			return err
		}
		verb, prep := "Deposited", "into"
		if kind == "withdraw" {
			verb, prep = "Withdrew", "from"
		}
		fmt.Printf("%s %s %s account %s at %s. Balance %s.\n", verb, connect.FormatNaira(r.Amount), prep, r.Account, id, connect.FormatNaira(r.Balance))
		fmt.Println("Business time:", stamp(r.At))
		return nil
	})
}

func cmdTransfer(args []string) error {
	const usage = "usage: beerus transfer --from NUBAN --to NUBAN --amount NAIRA [--from-bank ID] [--to-bank ID] [--narration TEXT]"
	f := newFlags("transfer", true)
	f.fs.StringVar(f.bank, "from-bank", "", "bank of the paying account; the session bank when empty")
	from := f.fs.String("from", "", "account number to pay from")
	toBank := f.fs.String("to-bank", "", "bank of the receiving account; the paying bank when empty")
	to := f.fs.String("to", "", "account number to pay into")
	amount := f.fs.String("amount", "", "amount in naira, such as 25000 or 2500.50")
	narration := f.fs.String("narration", "", "narration")
	if _, err := f.parse(args, 0, usage); err != nil {
		return err
	}
	if *from == "" || *to == "" || *amount == "" {
		return fmt.Errorf("--from, --to and --amount are required; %s", usage)
	}
	id, c, err := f.dial()
	if err != nil {
		return err
	}
	out, err := c.do("POST", bankPath(id, "transfers"), map[string]string{"from": *from, "to_bank": *toBank, "to": *to, "amount": *amount, "narration": *narration})
	if err != nil {
		return err
	}
	return f.show(out, func() error {
		r, err := decodeMap[connect.TransferResult](out)
		if err != nil {
			return err
		}
		return printTransfer(c, r)
	})
}

func printTransfer(c *ctl, r connect.TransferResult) error {
	at := hm(r.At)
	if r.Kind == "internal" {
		fmt.Printf("Moved %s from %s to %s, %s, at %s. Balance %s.\n", connect.FormatNaira(r.Amount), r.From, r.To, r.ToName, at, connect.FormatNaira(r.BalanceAfter))
		return nil
	}
	to, err := bankStatus(c, r.ToBank)
	if err != nil {
		return err
	}
	fmt.Printf("Sending %s to %s at %s.\n", connect.FormatNaira(r.Amount), r.ToName, to.Name)
	charges := fmt.Sprintf("Total debit %s: fee %s, VAT %s and stamp duty %s. Balance %s.", connect.FormatNaira(r.TotalDebit), connect.FormatNaira(r.Fee), connect.FormatNaira(r.VAT), connect.FormatNaira(r.StampDuty), connect.FormatNaira(r.BalanceAfter))
	switch r.Status {
	case nip.Success:
		fmt.Printf("Session %s: success at %s\n%s\n", r.SessionID, at, charges)
	case nip.Pending:
		if r.NextCheckAt == nil {
			fmt.Printf("Session %s: pending. Run beerus nip status %s.\n%s\n", r.SessionID, r.SessionID, charges)
			break
		}
		fmt.Printf("Session %s: pending. Beerus will check with NIBSS at %s. Run beerus nip status %s.\n%s\n", r.SessionID, hm(*r.NextCheckAt), r.SessionID, charges)
	case nip.Reversed:
		fmt.Printf("Session %s: failed with code %s. The debit and its charges were reversed at %s. Balance %s.\n", r.SessionID, r.ResponseCode, at, connect.FormatNaira(r.BalanceAfter))
	default:
		return fmt.Errorf("the transfer came back with the unknown status %q", r.Status)
	}
	return nil
}

func describeFault(f connect.Fault) string {
	switch f.Kind {
	case connect.FaultDown:
		if f.Until == nil {
			return "down until cleared"
		}
		return "down until " + hm(*f.Until)
	case connect.FaultTimeout:
		if f.Left == 1 {
			return fmt.Sprintf("the next transfer times out (outcome %s)", f.Outcome)
		}
		return fmt.Sprintf("the next %d transfers time out (outcome %s)", f.Left, f.Outcome)
	}
	return f.Kind
}

func cmdFault(args []string) error {
	const usage = "usage: beerus fault down [--bank ID] [--for 1h] | timeout [--bank ID] --outcome credited|failed|unknown [--count N] | list [--bank ID] | clear [--bank ID]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	f := newFlags("fault", true)
	switch args[0] {
	case "down", "timeout":
		dur := f.fs.String("for", "", "how long a down fault lasts, such as 1h")
		outcome := f.fs.String("outcome", "", "what really happened to a timed-out transfer")
		count := f.fs.Int("count", 0, "how many transfers time out")
		if _, err := f.parse(args, 1, usage); err != nil {
			return err
		}
		id, c, err := f.dial()
		if err != nil {
			return err
		}
		body := map[string]any{"kind": args[0], "bank": id, "for": *dur, "outcome": *outcome}
		f.fs.Visit(func(fl *flag.Flag) {
			if fl.Name == "count" {
				body["count"] = *count
			}
		})
		out, err := c.do("POST", "/faults", body)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			fl, err := decodeMap[connect.Fault](out)
			if err != nil {
				return err
			}
			fmt.Printf("Fault %s on %s: %s.\n", fl.ID, fl.Bank, describeFault(fl))
			return printBusinessTime(c)
		})
	case "list":
		if _, err := f.parse(args, 1, usage); err != nil {
			return err
		}
		c, err := newCtl()
		if err != nil {
			return err
		}
		out, err := c.do("GET", "/faults", nil)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			rows, err := decodeList[connect.Fault](out)
			if err != nil {
				return err
			}
			w := newTable()
			shown := 0
			for _, r := range rows {
				if *f.bank != "" && r.Bank != *f.bank {
					continue
				}
				if shown == 0 {
					fmt.Fprintln(w, "ID\tBANK\tWHAT")
				}
				shown++
				fmt.Fprintf(w, "%s\t%s\t%s\n", r.ID, r.Bank, describeFault(r))
			}
			if shown == 0 {
				fmt.Println("No faults.")
				return printBusinessTime(c)
			}
			if err := w.Flush(); err != nil {
				return fmt.Errorf("print faults: %w", err)
			}
			return printBusinessTime(c)
		})
	case "clear":
		if _, err := f.parse(args, 1, usage); err != nil {
			return err
		}
		c, err := newCtl()
		if err != nil {
			return err
		}
		out, err := c.do("DELETE", "/faults"+listQuery("", "", url.Values{"bank": {*f.bank}}), nil)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			r, err := decodeMap[struct {
				Cleared int `json:"cleared"`
			}](out)
			if err != nil {
				return err
			}
			if r.Cleared == 0 {
				fmt.Println("No faults to clear.")
				return printBusinessTime(c)
			}
			fmt.Printf("Cleared %s.\n", plural(r.Cleared, "fault", "faults"))
			return printBusinessTime(c)
		})
	}
	return errors.New(usage)
}

func cmdNip(args []string) error {
	const usage = "usage: beerus nip status <session_id> | settle-now | name-enquiry <bank_code> <nuban> | participants | positions | settlement"
	if len(args) == 0 {
		return errors.New(usage)
	}
	f := newFlags("nip", false)
	switch args[0] {
	case "status":
		pos, err := f.parse(args, 2, usage)
		if err != nil {
			return err
		}
		c, err := newCtl()
		if err != nil {
			return err
		}
		out, err := c.do("GET", "/nip/sessions/"+url.PathEscape(pos[1]), nil)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			s, err := decodeMap[nibss.Session](out)
			if err != nil {
				return err
			}
			printSession(s)
			return nil
		})
	case "settle-now":
		if _, err := f.parse(args, 1, usage); err != nil {
			return err
		}
		c, err := newCtl()
		if err != nil {
			return err
		}
		out, err := c.do("POST", "/nip/settle-now", nil)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			r, err := decodeMap[struct {
				Settled int       `json:"settled_sessions"`
				At      time.Time `json:"business_time"`
			}](out)
			if err != nil {
				return err
			}
			if r.Settled == 0 {
				fmt.Printf("No sessions to settle at %s.\n", hm(r.At))
				return nil
			}
			fmt.Printf("Settled %s at %s.\n", plural(r.Settled, "session", "sessions"), hm(r.At))
			return nil
		})
	case "name-enquiry", "participants", "positions", "settlement":
		positional := 1
		if args[0] == "name-enquiry" {
			positional = 3
		}
		pos, err := f.parse(args, positional, usage)
		if err != nil {
			return err
		}
		c, err := newCtl()
		if err != nil {
			return err
		}
		path := "/nip/" + args[0]
		if args[0] == "name-enquiry" {
			path += "?bank_code=" + url.QueryEscape(pos[1]) + "&account_number=" + url.QueryEscape(pos[2])
		}
		out, err := c.do("GET", path, nil)
		if err != nil {
			return err
		}
		pretty(out)
		return nil
	}
	return errors.New(usage)
}

func printSession(s nibss.Session) {
	switch {
	case s.Status == nibss.SessionOK && s.ResolvedAt != nil:
		fmt.Printf("Session %s: success at %s\n", s.ID, hm(*s.ResolvedAt))
	case s.Status == nibss.SessionPending && s.NextTSQAt != nil:
		fmt.Printf("Session %s: pending. Beerus will check with NIBSS at %s.\n", s.ID, hm(*s.NextTSQAt))
	case s.Status == nibss.SessionPending:
		fmt.Printf("Session %s: pending.\n", s.ID)
	case s.Outcome == nibss.OutcomeReversed:
		fmt.Printf("Session %s: failed with code %s. The sender's debit was reversed.\n", s.ID, s.Status)
	default:
		fmt.Printf("Session %s: failed with code %s. The sender's debit is being reversed.\n", s.ID, s.Status)
	}
	fmt.Printf("Amount %s from %s (bank code %s) to %s (bank code %s), started at %s.\n", connect.FormatNaira(s.Amount), s.FromNUBAN, s.FromCode, s.ToNUBAN, s.ToCode, hm(s.CreatedAt))
	if s.Settlement != "" {
		fmt.Printf("Settled in settlement session %s.\n", s.Settlement)
	}
}
