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
	f := &cmdFlags{fs: fs, bank: new(string), asJSON: fs.Bool("json", false, "print the API response as JSON")}
	if withBank {
		fs.StringVar(f.bank, "bank", "", "bank ID; the session bank when empty")
	}
	return f
}

func (f *cmdFlags) parse(path string, args []string, positional int) ([]string, error) {
	return parseAfter(path, f.fs, args, positional)
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

func cmdCustomer(args []string) error {
	if len(args) == 0 {
		return usageError("customer")
	}
	f := newFlags("customer", true)
	switch args[0] {
	case "create":
		name := f.fs.String("name", "", "customer's full name")
		phone := f.fs.String("phone", "", "phone number")
		dob := f.fs.String("dob", "", "date of birth as YYYY-MM-DD")
		user := f.fs.String("username", "", "online banking username")
		pwIn := f.fs.Bool("password-stdin", false, "read the online banking password from stdin")
		if _, err := f.parse("customer create", args, 1); err != nil {
			return err
		}
		if *name == "" || *phone == "" || *dob == "" {
			return fmt.Errorf("--name, --phone and --dob are required; %w", usageError("customer create"))
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
		pos, err := f.parse("customer close", args, 2)
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
	return usageError("customer")
}

func cmdAccount(args []string) error {
	if len(args) == 0 {
		return usageError("account")
	}
	f := newFlags("account", true)
	switch args[0] {
	case "open":
		cust := f.fs.String("customer", "", "customer ID")
		typ := f.fs.String("type", "", "account type: current or savings")
		if _, err := f.parse("account open", args, 1); err != nil {
			return err
		}
		if *cust == "" || *typ == "" {
			return fmt.Errorf("--customer and --type are required; %w", usageError("account open"))
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
		pos, err := f.parse("account close", args, 2)
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
	return usageError("account")
}

func cmdList(what string, args []string) error {
	positional := 0
	f := newFlags(what, true)
	limit := f.fs.String("limit", "", "page size, a `number` from 1 to 1000")
	cursor := f.fs.String("cursor", "", "the next_cursor from the last page")
	var cust, from, to *string
	switch what {
	case "accounts":
		cust = f.fs.String("customer", "", "only this customer's accounts, by customer ID")
	case "statement":
		positional = 1
		from = f.fs.String("from", "", "first `date`, as YYYY-MM-DD")
		to = f.fs.String("to", "", "last `date`, as YYYY-MM-DD")
	}
	pos, err := f.parse(what, args, positional)
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ValueDate, stamp(r.PostedAt), connect.FormatNaira(r.Amount), connect.FormatNaira(r.BalanceAfter), r.Narration, r.SessionID)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("print the statement: %w", err)
	}
	fmt.Println(plural(len(rows), "entry", "entries") + " on this page.")
	printNext(out)
	return printBusinessTime(c)
}

func cmdCash(kind string, args []string) error {
	f := newFlags(kind, true)
	amount := f.fs.String("amount", "", "amount in `naira`, such as 5000 or 5000.50")
	pos, err := f.parse(kind, args, 1)
	if err != nil {
		return err
	}
	if *amount == "" {
		return fmt.Errorf("--amount is required; %w", usageError(kind))
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
	f := newFlags("transfer", true)
	f.fs.StringVar(f.bank, "from-bank", "", "bank `ID` of the paying account; the session bank when empty")
	from := f.fs.String("from", "", "`NUBAN` of the account to pay from")
	toBank := f.fs.String("to-bank", "", "bank `ID` of the receiving account; the paying bank when empty")
	to := f.fs.String("to", "", "`NUBAN` of the account to pay into")
	amount := f.fs.String("amount", "", "amount in `naira`, such as 25000 or 2500.50")
	narration := f.fs.String("narration", "", "text shown on the transfer")
	dry := f.fs.Bool("dry-run", false, "show the fee, VAT, stamp duty and total debit without sending")
	if _, err := f.parse("transfer", args, 0); err != nil {
		return err
	}
	if *from == "" || *to == "" || *amount == "" {
		return fmt.Errorf("--from, --to and --amount are required; %w", usageError("transfer"))
	}
	id, c, err := f.dial()
	if err != nil {
		return err
	}
	path := bankPath(id, "transfers")
	if *dry {
		path = bankPath(id, "transfers", "quote")
	}
	out, err := c.do("POST", path, map[string]string{"from": *from, "to_bank": *toBank, "to": *to, "amount": *amount, "narration": *narration})
	if err != nil {
		return err
	}
	return f.show(out, func() error {
		r, err := decodeMap[connect.TransferResult](out)
		if err != nil {
			return err
		}
		if *dry {
			return printQuote(c, r)
		}
		return printTransfer(c, r)
	})
}

func printQuote(c *ctl, r connect.TransferResult) error {
	if r.Kind == "internal" {
		fmt.Printf("Moving %s from %s to %s, %s, costs no charges. Balance after %s.\n", connect.FormatNaira(r.Amount), r.From, r.To, r.ToName, connect.FormatNaira(r.BalanceAfter))
	} else {
		to, err := bankStatus(c, r.ToBank)
		if err != nil {
			return err
		}
		fmt.Printf("Sending %s to %s at %s.\n", connect.FormatNaira(r.Amount), r.ToName, to.Name)
		fmt.Printf("Fee %s, VAT %s and stamp duty %s. Total debit %s. Balance after %s.\n", connect.FormatNaira(r.Fee), connect.FormatNaira(r.VAT), connect.FormatNaira(r.StampDuty), connect.FormatNaira(r.TotalDebit), connect.FormatNaira(r.BalanceAfter))
	}
	fmt.Println("This was a quote. Nothing was sent.")
	return nil
}

func printTransfer(c *ctl, r connect.TransferResult) error {
	at := stamp(r.At)
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
		fmt.Printf("Session %s: pending. Beerus will check with NIBSS at %s. Run beerus nip status %s.\n%s\n", r.SessionID, stamp(*r.NextCheckAt), r.SessionID, charges)
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
		return "down until " + stamp(*f.Until)
	case connect.FaultTimeout:
		if f.Left == 1 {
			return fmt.Sprintf("the next transfer times out (outcome %s)", f.Outcome)
		}
		return fmt.Sprintf("the next %d transfers time out (outcome %s)", f.Left, f.Outcome)
	}
	return f.Kind
}

func cmdFault(args []string) error {
	if len(args) == 0 {
		return usageError("fault")
	}
	f := newFlags("fault", true)
	switch args[0] {
	case "down", "timeout":
		var dur, outcome string
		var count int
		if args[0] == "down" {
			f.fs.StringVar(&dur, "for", "", "`duration` of the fault, such as 1h; the bank stays down until cleared when left out")
		} else {
			f.fs.StringVar(&outcome, "outcome", "", "what really happened to a timed-out transfer: credited, failed or unknown")
			f.fs.IntVar(&count, "count", 0, "how many transfers time out; the next one when left out")
		}
		if _, err := f.parse("fault "+args[0], args, 1); err != nil {
			return err
		}
		id, c, err := f.dial()
		if err != nil {
			return err
		}
		body := map[string]any{"kind": args[0], "bank": id, "for": dur, "outcome": outcome}
		f.fs.Visit(func(fl *flag.Flag) {
			if fl.Name == "count" {
				body["count"] = count
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
		if _, err := f.parse("fault list", args, 1); err != nil {
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
		if _, err := f.parse("fault clear", args, 1); err != nil {
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
	return usageError("fault")
}

func cmdNip(args []string) error {
	if len(args) == 0 {
		return usageError("nip")
	}
	f := newFlags("nip", false)
	switch args[0] {
	case "status":
		pos, err := f.parse("nip status", args, 2)
		if err != nil {
			return err
		}
		c, err := newCtl()
		if err != nil {
			return err
		}
		before, err := c.do("GET", "/nip/sessions/"+url.PathEscape(pos[1]), nil)
		if err != nil {
			return err
		}
		out, err := c.do("POST", "/nip/sessions/"+url.PathEscape(pos[1])+"/status", nil)
		if err != nil {
			return err
		}
		return f.show(out, func() error {
			was, err := decodeMap[nibss.Session](before)
			if err != nil {
				return err
			}
			s, err := decodeMap[nibss.Session](out)
			if err != nil {
				return err
			}
			printSession(s, was.Status == nibss.SessionPending)
			return nil
		})
	case "settle-now":
		if _, err := f.parse("nip settle-now", args, 1); err != nil {
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
				fmt.Printf("No sessions to settle at %s.\n", stamp(r.At))
				return nil
			}
			fmt.Printf("Settled %s at %s.\n", plural(r.Settled, "session", "sessions"), stamp(r.At))
			return nil
		})
	case "name-enquiry", "participants", "positions", "settlement":
		positional := 1
		if args[0] == "name-enquiry" {
			positional = 3
		}
		pos, err := f.parse("nip "+args[0], args, positional)
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
	return usageError("nip")
}

func printSession(s nibss.Session, wasPending bool) {
	switch {
	case s.Status == nibss.SessionOK && s.ResolvedAt != nil && wasPending:
		fmt.Printf("Session %s: was pending; now success at %s\n", s.ID, stamp(*s.ResolvedAt))
	case s.Status == nibss.SessionOK && s.ResolvedAt != nil:
		fmt.Printf("Session %s: success at %s\n", s.ID, stamp(*s.ResolvedAt))
	case s.Status == nibss.SessionPending && s.NextTSQAt != nil:
		fmt.Printf("Session %s: pending. Beerus will check with NIBSS at %s.\n", s.ID, stamp(*s.NextTSQAt))
	case s.Status == nibss.SessionPending:
		fmt.Printf("Session %s: pending.\n", s.ID)
	case s.Outcome == nibss.OutcomeReversed:
		fmt.Printf("Session %s: failed with code %s. The sender's debit was reversed.\n", s.ID, s.Status)
	default:
		fmt.Printf("Session %s: failed with code %s. The sender's debit is being reversed.\n", s.ID, s.Status)
	}
	if wasPending && s.Status != nibss.SessionPending && s.Status != nibss.SessionOK {
		fmt.Println("The session was pending; this check resolved it.")
	}
	fmt.Printf("Amount %s from %s (bank code %s) to %s (bank code %s), started at %s.\n", connect.FormatNaira(s.Amount), s.FromNUBAN, s.FromCode, s.ToNUBAN, s.ToCode, stamp(s.CreatedAt))
	if s.Settlement != "" {
		fmt.Printf("Settled in settlement session %s.\n", s.Settlement)
	}
}
