package main

import (
	"bufio"
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
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	exitChanges  = 2
	exitBlocked  = 3
	pollEvery    = 500 * time.Millisecond
	apiTimeout   = 2 * time.Minute
	startTimeout = 30 * time.Second
	startRetry   = 200 * time.Millisecond
	defaultWait  = 5 * time.Minute
	codeNotReady = "not_ready"
	opFinish     = "finish"
	yamlIndent   = 2
	notRunning   = "Beerus is not running. Start it with beerus up."
	upHint       = "Banks are empty. Create a customer with beerus customer create."
)

var errNotRunning = errors.New(notRunning)

var apiClient = &http.Client{Timeout: apiTimeout}

type statusError struct {
	code int
	err  error
}

func (e *statusError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *statusError) Unwrap() error { return e.err }

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Message }

type cli struct {
	in     *bufio.Reader
	out    io.Writer
	errOut io.Writer
	tty    bool
}

func newCLI() *cli {
	fi, err := os.Stdin.Stat()
	return &cli{in: bufio.NewReader(os.Stdin), out: os.Stdout, errOut: os.Stderr, tty: err == nil && fi.Mode()&os.ModeCharDevice != 0}
}

func runSetup(run func(*cli, []string) error, args []string) int {
	e := newCLI()
	return e.finish(run(e, args))
}

func (e *cli) finish(err error) int {
	if err == nil {
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
	fmt.Fprintln(e.errOut, "beerus:", err)
	return code
}

func (e *cli) flagset(name string) (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", defaultDir(), "state directory")
	asJSON := fs.Bool("json", false, "print the API response as is")
	return fs, dir, asJSON
}

func (e *cli) client(dir string) (*ctl, error) {
	c, err := connect.LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	cl, err := newCtlFor(c)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotRunning
	}
	return cl, err
}

func (c *ctl) send(method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return nil, errNotRunning
		}
		return nil, fmt.Errorf("call %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response of %s %s: %w", method, path, err)
	}
	if resp.StatusCode < http.StatusMultipleChoices {
		return raw, nil
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Error.Message == "" {
		return nil, &apiError{Status: resp.StatusCode, Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return nil, &apiError{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
}

func decode[T any](raw []byte, what string) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("decode %s: %w", what, err)
	}
	return v, nil
}

func (e *cli) printJSON(raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return fmt.Errorf("format response: %w", err)
	}
	buf.WriteByte('\n')
	_, err := e.out.Write(buf.Bytes())
	return err
}

func listAnd(xs []string) string {
	if len(xs) < 2 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

type applyRequest struct {
	File string `json:"file"`
	Spec string `json:"spec"`
	connect.ApplyOptions
}

type planReply struct {
	Text     string         `json:"text"`
	Changed  bool           `json:"changed"`
	Actions  []setup.Action `json:"actions"`
	KeyFiles []string       `json:"api_key_files"`
}

type resourcePage struct {
	Data []setup.Resource `json:"data"`
	Next string           `json:"next_cursor"`
}

type moneyFlags struct {
	money     string
	to        string
	customers multi
}

func (m *moneyFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&m.money, "money", "", "what happens to customers' money when a bank goes: transfer, payout or leave")
	fs.StringVar(&m.to, "to", "", "bank that receives transferred money")
	fs.Var(&m.customers, "customer", "choice for one customer, such as cus_1=payout (repeatable)")
}

func (m moneyFlags) removal() (*setup.Removal, error) {
	if m.money == "" && m.to == "" && len(m.customers) == 0 {
		return nil, nil
	}
	if m.money == "" {
		return nil, errors.New("--to and --customer need --money transfer, payout or leave")
	}
	rm := &setup.Removal{Money: m.money, To: m.to}
	for _, kv := range m.customers {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("--customer takes id=choice, such as cus_1=payout; got %q", kv)
		}
		if rm.Customers == nil {
			rm.Customers = map[string]string{}
		}
		rm.Customers[k] = v
	}
	return rm, nil
}

func specRequest(dir, file string) (applyRequest, error) {
	path := file
	if path == "" {
		path = filepath.Join(dir, connect.SpecFile)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return applyRequest{}, fmt.Errorf("read %s: %w", path, err)
	}
	return applyRequest{File: path, Spec: string(raw)}, nil
}

func (e *cli) plan(c *ctl, req applyRequest) ([]byte, planReply, error) {
	raw, err := c.send("POST", "/plan", req)
	if err != nil {
		return nil, planReply{}, err
	}
	p, err := decode[planReply](raw, "plan")
	return raw, p, err
}

func (e *cli) confirm(c *ctl, req *applyRequest, asJSON bool, ops ...string) error {
	if req.Prune || !e.tty {
		return nil
	}
	_, p, err := e.plan(c, *req)
	if err != nil {
		return err
	}
	var ids []string
	for _, a := range p.Actions {
		if a.Destructive && slices.Contains(ops, a.Op) && !slices.Contains(req.Confirmed, a.ID) {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	shown := e.out
	if asJSON {
		shown = e.errOut
	}
	fmt.Fprint(shown, p.Text)
	for _, id := range ids {
		fmt.Fprintf(e.errOut, "Type %s to confirm: ", id)
		line, err := e.in.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("no answer for %s. Nothing was changed", id)
		}
		if strings.TrimSpace(line) != id {
			return fmt.Errorf("%s was not confirmed. Nothing was changed", id)
		}
		req.Confirmed = append(req.Confirmed, id)
	}
	return nil
}

type target struct {
	kind string
	id   string
	op   string
}

func (t target) label() string { return strings.ToLower(t.kind) + " " + t.id }

func ready(r setup.Resource) bool {
	switch r.Phase {
	case setup.PhaseActive, setup.PhaseSuspended, setup.PhaseClosed:
		return r.ObservedGeneration == r.Generation
	}
	return false
}

func finished(t target, r *setup.Resource) bool {
	switch t.op {
	case setup.OpRemove:
		return r == nil
	case setup.OpClose:
		return r != nil && r.Phase == setup.PhaseClosed
	case opFinish:
		return r == nil || ready(*r)
	}
	return r != nil && ready(*r)
}

func blockers(r setup.Resource) []string {
	var out []string
	for _, c := range r.Conditions {
		if c.Status == "True" && (c.Type == setup.ConditionBlocked || c.Type == setup.ConditionDegraded) {
			out = append(out, fmt.Sprintf("%s %s: %s: %s", strings.ToLower(r.Kind), r.ID, c.Reason, c.Message))
		}
	}
	return out
}

func (e *cli) resources(c *ctl) ([]setup.Resource, error) {
	var all []setup.Resource
	cursor := ""
	for {
		q := ""
		if cursor != "" {
			q = "?cursor=" + url.QueryEscape(cursor)
		}
		raw, err := c.send("GET", "/resources"+q, nil)
		if err != nil {
			return nil, err
		}
		page, err := decode[resourcePage](raw, "resources")
		if err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.Next == "" {
			return all, nil
		}
		cursor = page.Next
	}
}

func findResource(all []setup.Resource, kind, id string) *setup.Resource {
	for i := range all {
		if strings.EqualFold(all[i].Kind, kind) && all[i].ID == id {
			return &all[i]
		}
	}
	return nil
}

func (e *cli) wait(c *ctl, p planReply, wait time.Duration) error {
	var targets []target
	for _, a := range p.Actions {
		if a.Op != setup.OpDrift {
			targets = append(targets, target{kind: a.Kind, id: a.ID, op: a.Op})
		}
	}
	res, err := e.resources(c)
	if err != nil {
		return err
	}
	for _, r := range res {
		busy := r.Phase == setup.PhaseClosing || r.Phase == setup.PhaseTerminating
		if busy && !slices.ContainsFunc(targets, func(t target) bool { return t.kind == r.Kind && t.id == r.ID }) {
			targets = append(targets, target{kind: r.Kind, id: r.ID, op: opFinish})
		}
	}
	if len(targets) == 0 {
		return nil
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	var shown []string
	for {
		var pending, blocked []string
		for _, t := range targets {
			r := findResource(res, t.kind, t.id)
			if finished(t, r) {
				continue
			}
			pending = append(pending, t.label())
			if r != nil {
				blocked = append(blocked, blockers(*r)...)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		for _, b := range blocked {
			if !slices.Contains(shown, b) {
				fmt.Fprintln(e.errOut, b)
				shown = append(shown, b)
			}
		}
		select {
		case <-deadline.C:
			if len(blocked) > 0 {
				return &statusError{exitBlocked, errors.New(strings.Join(blocked, "\n"))}
			}
			verb := "has"
			if len(pending) > 1 {
				verb = "have"
			}
			return &statusError{exitBlocked, fmt.Errorf("%s %s not finished after %s. Run beerus get banks to follow it", listAnd(pending), verb, wait)}
		case <-tick.C:
		}
		if res, err = e.resources(c); err != nil {
			return err
		}
	}
}

func (e *cli) runApply(c *ctl, req applyRequest, wait time.Duration, asJSON bool) error {
	raw, err := c.send("POST", "/apply", req)
	if err != nil {
		return err
	}
	p, err := decode[planReply](raw, "apply reply")
	if err != nil {
		return err
	}
	if asJSON {
		if err := e.printJSON(raw); err != nil {
			return err
		}
	} else {
		fmt.Fprint(e.out, p.Text)
		for _, f := range p.KeyFiles {
			fmt.Fprintf(e.out, "API key written to %s\n", f)
		}
	}
	if wait == 0 {
		return nil
	}
	return e.wait(c, p, wait)
}

func checkWait(wait time.Duration) error {
	if wait < 0 {
		return errors.New("--wait cannot be negative; use 0 to skip waiting")
	}
	return nil
}

func cmdApply(e *cli, args []string) error {
	fs, dir, asJSON := e.flagset("apply")
	file := fs.String("f", "", "setup file; defaults to setup.yaml in the state directory")
	prune := fs.Bool("prune", false, "remove banks and partners that are not in the file")
	settle := fs.Bool("settle-now", false, "run an extra NIP settlement session for banks that are closing or being removed")
	wait := fs.Duration("wait", defaultWait, "how long to wait for the changes to finish; 0 does not wait")
	var mf moneyFlags
	mf.register(fs)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; usage: beerus apply [-f file] [--prune] [--money transfer|payout|leave --to BANK --customer ID=CHOICE] [--settle-now] [--wait 5m] [--json]", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := checkWait(*wait); err != nil {
		return err
	}
	rm, err := mf.removal()
	if err != nil {
		return err
	}
	c, err := e.client(*dir)
	if err != nil {
		return err
	}
	req, err := specRequest(*dir, *file)
	if err != nil {
		return err
	}
	req.ApplyOptions = connect.ApplyOptions{Prune: *prune, Money: rm, SettleNow: *settle}
	if err := e.confirm(c, &req, *asJSON, setup.OpRemove); err != nil {
		return err
	}
	return e.runApply(c, req, *wait, *asJSON)
}

func cmdDiff(e *cli, args []string) error {
	fs, dir, asJSON := e.flagset("diff")
	file := fs.String("f", "", "setup file; defaults to setup.yaml in the state directory")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; usage: beerus diff [-f file] [--json]", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	c, err := e.client(*dir)
	if err != nil {
		return err
	}
	req, err := specRequest(*dir, *file)
	if err != nil {
		return err
	}
	raw, p, err := e.plan(c, req)
	if err != nil {
		return err
	}
	if *asJSON {
		err = e.printJSON(raw)
	} else {
		_, err = fmt.Fprint(e.out, p.Text)
	}
	if err != nil {
		return err
	}
	if p.Changed {
		return &statusError{code: exitChanges}
	}
	return nil
}

func loadApplied(dir string) (setup.Status, error) {
	ok, err := connect.Initialized(dir)
	if err != nil {
		return setup.Status{}, err
	}
	if !ok {
		return setup.Status{}, fmt.Errorf("%s holds no Beerus setup; run beerus up to make one", dir)
	}
	return setup.LoadStatus(dir)
}

func names(st setup.Status) map[string]string {
	out := map[string]string{}
	for _, b := range st.Applied.Spec.Banks {
		out[setup.KindBank+"/"+b.ID] = b.Name
	}
	for _, p := range st.Applied.Spec.Partners {
		out[setup.KindPartner+"/"+p.ID] = p.Name
	}
	return out
}

func kindOf(word string) (string, error) {
	switch strings.ToLower(strings.TrimSuffix(word, "s")) {
	case "bank":
		return setup.KindBank, nil
	case "partner":
		return setup.KindPartner, nil
	}
	return "", fmt.Errorf("unknown kind %q; use bank or partner", word)
}

func cmdGet(e *cli, args []string) error {
	fs, dir, asJSON := e.flagset("get")
	limit := fs.String("limit", "", "page size")
	cursor := fs.String("cursor", "", "next_cursor from the last page")
	const usage = "usage: beerus get banks|partners [--limit N] [--cursor C] [--json]"
	pos, err := flagsAfter(fs, args, 1)
	if err != nil {
		return fmt.Errorf("%w; %s", err, usage)
	}
	kind, err := kindOf(pos[0])
	if err != nil {
		return fmt.Errorf("%w; %s", err, usage)
	}
	st, err := loadApplied(*dir)
	if err != nil {
		return err
	}
	c, err := e.client(*dir)
	if err != nil {
		return err
	}
	raw, err := c.send("GET", "/resources"+listQuery(*limit, *cursor, nil), nil)
	if err != nil {
		return err
	}
	if *asJSON {
		return e.printJSON(raw)
	}
	page, err := decode[resourcePage](raw, "resources")
	if err != nil {
		return err
	}
	known := names(st)
	w := tabwriter.NewWriter(e.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPHASE\tREADY\tGEN\tMESSAGE")
	for _, r := range page.Data {
		if r.Kind != kind {
			continue
		}
		msg := strings.Join(blockers(r), "; ")
		ok := "no"
		if ready(r) {
			ok = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", r.ID, known[r.Kind+"/"+r.ID], r.Phase, ok, r.Generation, msg)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if page.Next != "" {
		fmt.Fprintf(e.out, "More: run again with --cursor %s\n", page.Next)
	}
	return nil
}

func marshalYAML(v any) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(yamlIndent)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("encode yaml: %w", err)
	}
	return buf.String(), nil
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

func (e *cli) describeText(r setup.Resource, st setup.Status) error {
	fmt.Fprintf(e.out, "%s %s\n", r.Kind, r.ID)
	ok := "no"
	if ready(r) {
		ok = "yes"
	}
	w := tabwriter.NewWriter(e.out, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "  Phase\t%s\n  Ready\t%s\n  Generation\t%d (observed %d)\n", r.Phase, ok, r.Generation, r.ObservedGeneration)
	if r.DeletionRequestedAt != nil {
		fmt.Fprintf(w, "  Deletion requested\t%s\n", r.DeletionRequestedAt.Format(time.RFC3339))
	}
	if r.Attempts > 0 && r.NextAttemptAt != nil {
		fmt.Fprintf(w, "  Attempts\t%d, next at %s\n", r.Attempts, r.NextAttemptAt.Format(time.RFC3339))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	var spec any
	for _, b := range st.Applied.Spec.Banks {
		if r.Kind == setup.KindBank && b.ID == r.ID {
			spec = b
		}
	}
	for _, p := range st.Applied.Spec.Partners {
		if r.Kind == setup.KindPartner && p.ID == r.ID {
			spec = p
		}
	}
	if spec != nil {
		text, err := marshalYAML(spec)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Spec\n%s", indent(text))
	}
	if len(r.Conditions) > 0 {
		fmt.Fprintln(e.out, "Conditions")
		for _, c := range r.Conditions {
			fmt.Fprintf(e.out, "  %s=%s %s %s\n", c.Type, c.Status, c.Reason, c.Message)
		}
	}
	if len(r.Finalizers) > 0 {
		fmt.Fprintln(e.out, "Finalizers")
		for _, f := range r.Finalizers {
			state := "pending"
			if slices.Contains(r.Done, f) {
				state = "done"
			}
			fmt.Fprintf(e.out, "  %s %s\n", f, state)
		}
	}
	if len(r.Owned) > 0 {
		fmt.Fprintln(e.out, "Owned")
		for _, o := range r.Owned {
			fmt.Fprintf(e.out, "  %s %s\n", o.Kind, o.Name)
		}
	}
	if len(r.Result) > 0 {
		var buf bytes.Buffer
		if err := json.Indent(&buf, r.Result, "  ", "  "); err != nil {
			return fmt.Errorf("format result: %w", err)
		}
		fmt.Fprintf(e.out, "Result\n  %s\n", buf.String())
	}
	if len(r.Events) > 0 {
		fmt.Fprintln(e.out, "Events")
		for _, ev := range r.Events {
			fmt.Fprintf(e.out, "  %s %s x%d %s\n", ev.LastAt.Format(time.RFC3339), ev.Reason, ev.Count, ev.Message)
		}
	}
	return nil
}

func cmdDescribe(e *cli, args []string) error {
	fs, dir, asJSON := e.flagset("describe")
	const usage = "usage: beerus describe bank|partner <id> [--json]"
	pos, err := flagsAfter(fs, args, 2)
	if err != nil {
		return fmt.Errorf("%w; %s", err, usage)
	}
	kind, err := kindOf(pos[0])
	if err != nil {
		return fmt.Errorf("%w; %s", err, usage)
	}
	st, err := loadApplied(*dir)
	if err != nil {
		return err
	}
	c, err := e.client(*dir)
	if err != nil {
		return err
	}
	raw, err := c.send("GET", "/resources/"+strings.ToLower(kind)+"/"+url.PathEscape(pos[1]), nil)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return fmt.Errorf("there is no %s with ID %s. Run beerus get %ss to list them", strings.ToLower(kind), pos[1], strings.ToLower(kind))
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return e.printJSON(raw)
	}
	r, err := decode[setup.Resource](raw, "resource")
	if err != nil {
		return err
	}
	return e.describeText(r, st)
}

func cmdExport(e *cli, args []string) error {
	fs, dir, asJSON := e.flagset("export")
	out := fs.String("o", "", "write the setup to this file instead of printing it")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; usage: beerus export [-o file] [--json]", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *out != "" && *asJSON {
		return errors.New("--json and -o cannot be used together")
	}
	st, err := loadApplied(*dir)
	if err != nil {
		return err
	}
	if *asJSON {
		raw, err := json.Marshal(st.Applied)
		if err != nil {
			return fmt.Errorf("encode setup: %w", err)
		}
		return e.printJSON(raw)
	}
	raw, err := setup.Export(st.Applied)
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = e.out.Write(raw)
		return err
	}
	if err := config.WriteSecret(*out, string(raw)); err != nil {
		return fmt.Errorf("write %s: %w", *out, err)
	}
	fmt.Fprintf(e.out, "Wrote %s.\n", *out)
	return nil
}

func cmdVerify(e *cli, args []string) error {
	fs, dir, asJSON := e.flagset("verify")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; usage: beerus verify [--json]", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	st, err := loadApplied(*dir)
	if err != nil {
		return err
	}
	if st.AppliedSHA256 == "" {
		return fmt.Errorf("%s has no setup hash; run beerus apply", *dir)
	}
	if *asJSON {
		raw, err := json.Marshal(map[string]string{"setup_sha256": st.AppliedSHA256})
		if err != nil {
			return fmt.Errorf("encode hash: %w", err)
		}
		return e.printJSON(raw)
	}
	fmt.Fprintf(e.out, "setup sha256:%s\n", st.AppliedSHA256)
	return nil
}

func bankIn(spec setup.Spec, id string) (int, error) {
	i := slices.IndexFunc(spec.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
	if i < 0 {
		return 0, fmt.Errorf("there is no bank with ID %s. Run beerus get banks to list them", id)
	}
	return i, nil
}

func cmdBankEnd(e *cli, args []string) error {
	verb := args[0]
	fs, dir, asJSON := e.flagset("bank " + verb)
	yes := fs.Bool("yes", false, "skip the typed confirmation")
	settle := fs.Bool("settle-now", false, "run an extra NIP settlement session")
	wait := fs.Duration("wait", defaultWait, "how long to wait for the bank to finish; 0 does not wait")
	var mf moneyFlags
	mf.register(fs)
	usage := fmt.Sprintf("usage: beerus bank %s <id> [--money transfer|payout|leave --to BANK --customer ID=CHOICE] [--settle-now] [--wait 5m] [--yes] [--json]", verb)
	pos, err := flagsAfter(fs, args[1:], 1)
	if err != nil {
		return fmt.Errorf("%w; %s", err, usage)
	}
	id := pos[0]
	if err := checkWait(*wait); err != nil {
		return err
	}
	rm, err := mf.removal()
	if err != nil {
		return err
	}
	st, err := loadApplied(*dir)
	if err != nil {
		return err
	}
	spec := st.Applied
	spec.Spec.Banks = slices.Clone(spec.Spec.Banks)
	i, err := bankIn(spec, id)
	if err != nil {
		return err
	}
	op := setup.OpClose
	if verb == "purge" {
		op = setup.OpRemove
		spec.Spec.Banks = slices.Delete(spec.Spec.Banks, i, i+1)
		if !*yes && !e.tty {
			return fmt.Errorf("purging %s deletes its database and cannot be undone. Nothing was changed. Run again with --yes to purge it", id)
		}
	} else {
		if spec.Spec.Banks[i].State == setup.StateClosed {
			return fmt.Errorf("%s is already closed. Nothing was changed", id)
		}
		if rm == nil && spec.Spec.Banks[i].OnRemove == nil {
			return fmt.Errorf("%s needs a money decision. Add --money transfer, payout or leave", id)
		}
		spec.Spec.Banks[i].State = setup.StateClosed
		if rm != nil {
			spec.Spec.Banks[i].OnRemove, rm = rm, nil
		}
	}
	raw, err := setup.Export(spec)
	if err != nil {
		return err
	}
	c, err := e.client(*dir)
	if err != nil {
		return err
	}
	req := applyRequest{File: filepath.Join(*dir, connect.SpecFile), Spec: string(raw), ApplyOptions: connect.ApplyOptions{Prune: verb == "purge" && *yes, Money: rm, SettleNow: *settle}}
	if !*yes {
		if err := e.confirm(c, &req, *asJSON, op); err != nil {
			return err
		}
	}
	return e.runApply(c, req, *wait, *asJSON)
}

func (e *cli) startApply(ctx context.Context, c *ctl, req applyRequest, died <-chan error) error {
	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(startRetry)
	defer tick.Stop()
	for {
		raw, err := c.send("POST", "/apply", req)
		var ae *apiError
		if err == nil {
			p, derr := decode[planReply](raw, "apply reply")
			if derr != nil {
				return derr
			}
			if p.Changed {
				fmt.Fprint(e.out, p.Text)
			}
			for _, f := range p.KeyFiles {
				fmt.Fprintf(e.out, "API key written to %s\n", f)
			}
			return nil
		}
		if !errors.Is(err, errNotRunning) && !(errors.As(err, &ae) && ae.Code == codeNotReady) {
			return err
		}
		select {
		case serr := <-died:
			return errors.Join(err, serr)
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("apply the setup file: %w", err)
		case <-tick.C:
		}
	}
}

func cmdUp(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runUp(ctx, newCLI(), args)
}

func runUp(ctx context.Context, e *cli, args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", defaultDir(), "state directory")
	file := fs.String("f", "", "setup file to start from or apply")
	prune := fs.Bool("prune", false, "remove banks and partners that are not in the file")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; usage: beerus up [-f file] [--prune] [--dir .beerus]", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	var req applyRequest
	if *file != "" {
		var err error
		if req, err = specRequest(*dir, *file); err != nil {
			return err
		}
		if _, errs := setup.Parse(req.File, []byte(req.Spec)); len(errs) > 0 {
			return errors.Join(errs...)
		}
	}
	ok, err := connect.Initialized(*dir)
	if err != nil {
		return err
	}
	fresh := !ok
	switch {
	case fresh && *file == "":
		if err := cmdInit([]string{"--dir", *dir}); err != nil {
			return err
		}
		fmt.Fprintln(e.out)
	case fresh:
		spec, _ := setup.Parse(req.File, []byte(req.Spec))
		if err := connect.Init(*dir, spec, connect.DefaultLocal(), clock.DateOf(time.Now())); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Created %s from %s.\n\n", *dir, req.File)
	}
	c, err := connect.LoadConfig(*dir)
	if err != nil {
		return err
	}
	if err := connect.CheckPorts(c); err != nil {
		return err
	}
	if err := c.EnsureCertificates(); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(e.errOut, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app, err := connect.New(c, connect.Options{Logger: log})
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Beerus Connect (stub) is running. Business time %s, world %s.\n\n", app.Clock().Now().Format(time.RFC1123), app.WorldID())
	if err := printInfo(c); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "\nControl room        %s\n", app.RoomURL())
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	died := make(chan error, 1)
	go func() { died <- app.Serve(serveCtx) }()
	if !fresh {
		if *file == "" {
			if req, err = specRequest(*dir, ""); err != nil {
				cancel()
				return errors.Join(err, <-died)
			}
		}
		req.Prune = *prune
		cl, err := newCtlFor(c)
		if err == nil {
			err = e.startApply(serveCtx, cl, req, died)
		}
		if err != nil {
			cancel()
			return errors.Join(err, <-died)
		}
	} else {
		fmt.Fprintln(e.out, "\n"+upHint)
	}
	fmt.Fprintln(e.out, "\nPress Ctrl+C to stop.")
	return <-died
}
