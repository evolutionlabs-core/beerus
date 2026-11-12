package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	groupMain    = "Main commands"
	groupSetup   = "Setup"
	groupBanking = "Banking"
	groupOpen    = "Open banking"
	groupFaults  = "Failures and recovery"
	groupRunning = "Running Beerus"
	nameColumn   = 14
)

type command struct {
	path    string
	group   string
	summary string
	usage   string
	run     func(args []string) error
}

type helpError struct {
	text string
}

func (h *helpError) Error() string { return h.text }

func groupOrder() []string {
	return []string{groupMain, groupSetup, groupBanking, groupOpen, groupFaults, groupRunning}
}

func commands() []command {
	return []command{
		{"up", groupMain, "Start Beerus and apply your setup", "up [options]", cmdUp},
		{"bank", groupMain, "Create, suspend, close and remove banks", "", cmdBankGroup},
		{"bank list", "", "List banks and their state", "bank list [options]", nil},
		{"bank create", "", "Create a bank", "bank create --name NAME [options]", nil},
		{"bank suspend", "", "Suspend a bank", "bank suspend <id>", nil},
		{"bank resume", "", "Resume a suspended bank", "bank resume <id>", nil},
		{"bank close", "", "Close a bank and decide what happens to its money", "bank close <id> [options]", nil},
		{"bank purge", "", "Delete a bank and its database", "bank purge <id> [options]", nil},
		{"customer", groupMain, "Create and close customers", "", cmdCustomer},
		{"customer create", "", "Create a customer", "customer create --name NAME --phone PHONE --dob YYYY-MM-DD [options]", nil},
		{"customer close", "", "Close a customer", "customer close <id> [options]", nil},
		{"account", groupMain, "Open and close accounts", "", cmdAccount},
		{"account open", "", "Open a current or savings account", "account open --customer ID --type current|savings [options]", nil},
		{"account close", "", "Close an account", "account close <nuban> [options]", nil},
		{"transfer", groupMain, "Send money between accounts over NIP", "transfer --from NUBAN --to NUBAN --amount NAIRA [options]", cmdTransfer},
		{"nip", groupMain, "Check NIP sessions and settle now", "", cmdNip},
		{"nip status", "", "Check a NIP session with NIBSS now", "nip status <session_id> [options]", nil},
		{"nip settle-now", "", "Settle open NIP sessions now", "nip settle-now [options]", nil},
		{"nip name-enquiry", "", "Look up the name on an account", "nip name-enquiry <bank_code> <nuban> [options]", nil},
		{"nip participants", "", "List the banks in the NIP directory", "nip participants [options]", nil},
		{"nip positions", "", "Show each bank's NIP position", "nip positions [options]", nil},
		{"nip settlement", "", "Check that NIP settlement balances", "nip settlement [options]", nil},

		{"init", groupSetup, "Create the state directory and a setup file", "init [options]", cmdInit},
		{"apply", groupSetup, "Apply the setup file to the running Beerus", "apply [options]", runCLI(cmdApply)},
		{"diff", groupSetup, "Show what apply would change", "diff [options]", runCLI(cmdDiff)},
		{"get", groupSetup, "List banks or partners", "get banks|partners [options]", runCLI(cmdGet)},
		{"describe", groupSetup, "Show one bank or partner in detail", "describe bank|partner <id> [options]", runCLI(cmdDescribe)},
		{"export", groupSetup, "Write the running setup as a file", "export [options]", runCLI(cmdExport)},
		{"verify", groupSetup, "Print the hash of the applied setup", "verify [options]", runCLI(cmdVerify)},
		{"reset", groupSetup, "Drop the databases and clear the state directory", "reset <setup-name> [options]", func(args []string) error { return cmdReset(args, os.Stdout) }},

		{"use", groupBanking, "Choose the bank that later commands use", "use <bank>", cmdUse},
		{"customers", groupBanking, "List a bank's customers", "customers [options]", bind(cmdList, "customers")},
		{"accounts", groupBanking, "List a bank's accounts", "accounts [options]", bind(cmdList, "accounts")},
		{"deposit", groupBanking, "Put cash into an account", "deposit <nuban> --amount NAIRA [options]", bind(cmdCash, "deposit")},
		{"withdraw", groupBanking, "Take cash out of an account", "withdraw <nuban> --amount NAIRA [options]", bind(cmdCash, "withdraw")},
		{"statement", groupBanking, "Show an account's statement", "statement <nuban> [options]", bind(cmdList, "statement")},

		{"partner", groupOpen, "Add, list, rekey and remove partner apps", "", cmdPartner},
		{"partner add", "", "Register a partner app", "partner add <id> --redirect-uri URL [options]", nil},
		{"partner list", "", "List partner apps", "partner list [options]", nil},
		{"partner rotate-key", "", "Issue new credentials for a partner app", "partner rotate-key <id> [options]", nil},
		{"partner remove", "", "Remove a partner app", "partner remove <id> [options]", nil},
		{"registry", groupOpen, "Open banking clients and consents", "", cmdRegistry},
		{"registry list", "", "List registered clients", "registry list [options]", nil},
		{"registry add-client", "", "Register a client", "registry add-client <id> --cert-cn CN --public-key FILE --redirect-uri URL [options]", nil},
		{"registry consents", "", "List consent changes", "registry consents [options]", nil},
		{"connections", groupOpen, "List partner connections", "connections", cmdConnections},
		{"revoke", groupOpen, "Revoke a connection at the bank", "revoke <connection-id>", cmdRevoke},
		{"sync", groupOpen, "Sync all connections now", "sync", cmdSync},
		{"webhooks", groupOpen, "List webhook deliveries and replay one", "", cmdWebhooks},
		{"webhooks list", "", "List recent webhook deliveries", "webhooks list", nil},
		{"webhooks replay", "", "Send a webhook delivery again", "webhooks replay <delivery-id>", nil},

		{"fault", groupFaults, "Make a bank go down or time out", "", cmdFault},
		{"fault down", "", "Take a bank down", "fault down [options]", nil},
		{"fault timeout", "", "Make transfers time out", "fault timeout --outcome credited|failed|unknown [options]", nil},
		{"fault list", "", "List active faults", "fault list [options]", nil},
		{"fault clear", "", "Clear active faults", "fault clear [options]", nil},
		{"snapshot", groupFaults, "Save and restore Beerus's data", "", cmdSnapshot},
		{"snapshot list", "", "List snapshots", "snapshot list", nil},
		{"snapshot take", "", "Take a snapshot", "snapshot take [name]", nil},
		{"snapshot restore", "", "Restore a snapshot", "snapshot restore <id>", nil},

		{"run", groupRunning, "Run one participant: cbn, core or a bank", "run --participant cbn|core|<bank-id> [options]", cmdRun},
		{"compose", groupRunning, "Print a Docker Compose file for the setup", "compose [options]", cmdCompose},
		{"info", groupRunning, "Show addresses and partner apps", "info [options]", cmdInfo},
		{"doctor", groupRunning, "Find and clean up leftovers", "doctor [options]", cmdDoctor},
		{"trust", groupRunning, "Trust the Beerus root certificate", "trust [options]", bind(cmdTrust, true)},
		{"untrust", groupRunning, "Stop trusting the Beerus root certificate", "untrust [options]", bind(cmdTrust, false)},
		{"health", groupRunning, "Check that a URL answers", "health <url> [options]", cmdHealth},
		{"version", groupRunning, "Print the version", "version", cmdVersion},
	}
}

func bind[T any](f func(T, []string) error, v T) func([]string) error {
	return func(args []string) error { return f(v, args) }
}

func runCLI(f func(*cli, []string) error) func([]string) error {
	return func(args []string) error { return f(newCLI(), args) }
}

func lookup(path string) (command, bool) {
	for _, c := range commands() {
		if c.path == path {
			return c, true
		}
	}
	return command{}, false
}

func subcommands(path string) []command {
	var out []command
	for _, c := range commands() {
		if parent, _, ok := strings.Cut(c.path, " "); ok && parent == path {
			out = append(out, c)
		}
	}
	return out
}

func subName(c command) string {
	_, name, _ := strings.Cut(c.path, " ")
	return name
}

func usageLine(path string) string {
	c, ok := lookup(path)
	if !ok {
		return "beerus help"
	}
	if c.usage != "" {
		return "beerus " + c.usage
	}
	return "beerus " + path + " <command> [options]"
}

func usageError(path string) error {
	subs := subcommands(path)
	c, _ := lookup(path)
	if c.usage != "" || len(subs) == 0 {
		return errors.New("usage: " + usageLine(path))
	}
	names := make([]string, len(subs))
	for i, s := range subs {
		names[i] = subName(s)
	}
	return fmt.Errorf("usage: beerus %s %s", path, strings.Join(names, "|"))
}

func unknownCommand(name string) error {
	return fmt.Errorf("unknown command %q. Run beerus help.", name)
}

func helpRequested(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "-help", "--help":
			return true
		}
	}
	return false
}

func parseFlags(path string, fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return leafHelp(path, fs)
	}
	if err != nil {
		return fmt.Errorf("%w; %s", err, usageError(path))
	}
	return nil
}

func parseAfter(path string, fs *flag.FlagSet, args []string, positional int) ([]string, error) {
	if helpRequested(args) {
		return nil, leafHelp(path, fs)
	}
	if len(args) < positional {
		return nil, usageError(path)
	}
	for _, a := range args[:positional] {
		if strings.HasPrefix(a, "-") {
			return nil, fmt.Errorf("%q should be a value, not a flag; %w", a, usageError(path))
		}
	}
	if err := parseFlags(path, fs, args[positional:]); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q; %w", fs.Arg(0), usageError(path))
	}
	return args[:positional], nil
}

func noFlags(path string, args []string, positional int) ([]string, error) {
	return parseAfter(path, flag.NewFlagSet(path, flag.ContinueOnError), args, positional)
}

func leafHelp(path string, fs *flag.FlagSet) *helpError {
	c, _ := lookup(path)
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: %s\n\n%s.\n", usageLine(path), c.summary)
	rows := flagRows(fs)
	if len(rows) > 0 {
		b.WriteString("\nOptions:\n")
		writeRows(&b, rows)
	}
	return &helpError{b.String()}
}

type row struct {
	name, text string
}

func flagRows(fs *flag.FlagSet) []row {
	var rows []row
	fs.VisitAll(func(f *flag.Flag) {
		if f.Usage == "" {
			return
		}
		dash := "--"
		if len(f.Name) == 1 {
			dash = "-"
		}
		kind, text := flag.UnquoteUsage(f)
		name := dash + f.Name
		switch {
		case kind == "":
		case strings.Contains(f.Usage, "`"), kind == "duration":
			name += " " + strings.ToUpper(kind)
		default:
			name += " " + strings.ToUpper(f.Name)
		}
		switch f.DefValue {
		case "", "0", "false", "0s":
		default:
			text += " (default " + f.DefValue + ")"
		}
		rows = append(rows, row{name, capitalize(text)})
	})
	return rows
}

func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func writeRows(b *strings.Builder, rows []row) {
	width := nameColumn
	for _, r := range rows {
		width = max(width, len(r.name)+3)
	}
	for _, r := range rows {
		fmt.Fprintf(b, "  %-*s%s\n", width, r.name, r.text)
	}
}

func groupHelp(path string) *helpError {
	c, _ := lookup(path)
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: %s\n\n%s.\n\nCommands:\n", usageLine(path), c.summary)
	var rows []row
	for _, s := range subcommands(path) {
		rows = append(rows, row{subName(s), s.summary})
	}
	writeRows(&b, rows)
	fmt.Fprintf(&b, "\nRun \"beerus %s <command> --help\" for a command's options.\n", path)
	return &helpError{b.String()}
}

func topHelp() *helpError {
	var b strings.Builder
	b.WriteString("Usage: beerus <command> [args]\n\n")
	b.WriteString("Beerus runs Nigeria's banking infrastructure on your machine:\n")
	b.WriteString("banks, NIBSS, the CBN and open banking, in one binary.\n")
	for _, g := range groupOrder() {
		fmt.Fprintf(&b, "\n%s:\n", g)
		var rows []row
		for _, c := range commands() {
			if c.group == g {
				rows = append(rows, row{c.path, c.summary})
			}
		}
		writeRows(&b, rows)
	}
	b.WriteString("\nGlobal options:\n")
	writeRows(&b, []row{{"-h, --help", "Show help for any command"}})
	b.WriteString("\nEnvironment:\n")
	writeRows(&b, []row{{"BEERUS_DIR", "State directory (default .beerus)"}})
	b.WriteString("\nRun \"beerus <command> --help\" for a command's options.\n")
	return &helpError{b.String()}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		return topHelp()
	}
	name, rest := args[0], args[1:]
	switch name {
	case "-h", "-help", "--help":
		return topHelp()
	case "help":
		return helpFor(rest)
	}
	c, ok := lookup(name)
	if !ok {
		return unknownCommand(name)
	}
	if len(subcommands(name)) > 0 && len(rest) > 0 && helpRequested(rest[:1]) {
		return groupHelp(name)
	}
	return c.run(rest)
}

func helpFor(words []string) error {
	if len(words) == 0 {
		return topHelp()
	}
	if helpRequested(words[:1]) {
		return topHelp()
	}
	path := words[0]
	if _, ok := lookup(path); !ok {
		return unknownCommand(path)
	}
	if len(words) > 1 && !strings.HasPrefix(words[1], "-") {
		path += " " + words[1]
		if _, ok := lookup(path); !ok {
			return unknownCommand(path)
		}
	}
	if len(subcommands(path)) > 0 {
		return groupHelp(path)
	}
	return dispatch(append(strings.Fields(path), "--help"))
}

func report(out, errOut io.Writer, err error) int {
	if err == nil {
		return 0
	}
	var h *helpError
	if errors.As(err, &h) {
		fmt.Fprint(out, h.text)
		return 0
	}
	code := 1
	var se *statusError
	if errors.As(err, &se) {
		code = se.code
		if se.err == nil {
			return code
		}
	}
	fmt.Fprintln(errOut, "beerus:", err)
	return code
}
