package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type result struct {
	code int
	out  string
	err  string
}

func invoke(fn func(*cli, []string) error, stdin string, tty bool, args ...string) result {
	var out, errOut bytes.Buffer
	e := &cli{in: bufio.NewReader(strings.NewReader(stdin)), out: &out, errOut: &errOut, tty: tty}
	code := e.finish(fn(e, args))
	return result{code, out.String(), errOut.String()}
}

func portsFree(base int) bool {
	for p := base; p < base+8; p++ {
		for _, q := range []int{p, p + config.DefaultAPIPortOffset} {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", q))
			if err != nil {
				return false
			}
			l.Close()
		}
	}
	return true
}

func freeLocal(t *testing.T) setup.Local {
	t.Helper()
	local := connect.DefaultLocal()
	for range 200 {
		base := 20000 + rand.IntN(20000)
		if portsFree(base) {
			local.Connect.PartnerPort, local.Connect.WidgetPort, local.Connect.ControlPort, local.Connect.HealthPort = base, base+1, base+2, base+3
			return local
		}
	}
	t.Fatal("no free block of ports")
	return local
}

func initDir(t *testing.T, local setup.Local, spec setup.Spec) string {
	t.Helper()
	dir := t.TempDir()
	if err := connect.Init(dir, spec, local, clock.DateOf(time.Now())); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSetupCommandsAnswerWithoutARunningBeerus(t *testing.T) {
	spec := setup.DefaultSpec(defaultSetupName, clock.DateOf(time.Now()))
	dir := initDir(t, freeLocal(t), spec)
	file := filepath.Join(dir, "copy.yaml")

	for _, c := range []struct {
		fn   func(*cli, []string) error
		args []string
	}{
		{cmdApply, []string{"--dir", dir}},
		{cmdDiff, []string{"--dir", dir}},
		{cmdGet, []string{"banks", "--dir", dir}},
		{cmdDescribe, []string{"bank", "bank-a", "--dir", dir}},
	} {
		if r := invoke(c.fn, "", false, c.args...); r.code != 1 || r.err != "beerus: "+notRunning+"\n" {
			t.Errorf("%v while stopped: %+v", c.args, r)
		}
	}

	r := invoke(cmdExport, "", false, "--dir", dir, "-o", file)
	if r.code != 0 || r.out != "Wrote "+file+".\n" {
		t.Fatalf("export to a file: %+v", r)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("exported file: %v %v", fi, err)
	}
	if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), "bank-b") || !strings.Contains(string(raw), "nuban_code: \"902\"") && !strings.Contains(string(raw), "nuban_code: 902") {
		t.Fatalf("export should pin the codes:\n%s", raw)
	}
	if r := invoke(cmdVerify, "", false, "--dir", dir); r.code != 0 || !regexp.MustCompile(`^setup sha256:[0-9a-f]{64}\n$`).MatchString(r.out) {
		t.Fatalf("verify: %+v", r)
	}
	if r := invoke(cmdVerify, "", false, "--dir", dir, "--json"); !regexp.MustCompile(`^\{\n  "setup_sha256": "[0-9a-f]{64}"\n\}\n$`).MatchString(r.out) {
		t.Fatalf("verify --json: %+v", r)
	}
	c, err := connect.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if text, err := renderCompose(c); err != nil || !strings.Contains(string(text), "bank-b:") {
		t.Fatalf("compose from applied: %v", err)
	}

	for _, c := range []struct {
		fn    func(*cli, []string) error
		stdin string
		args  []string
		want  string
	}{
		{cmdExport, "", []string{"--dir", dir, "--json", "-o", file}, "beerus: --json and -o cannot be used together\n"},
		{cmdBankEnd, "", []string{"purge", "bank-a", "--dir", dir}, "beerus: purging bank-a deletes its database and cannot be undone. Nothing was changed. Run again with --yes to purge it\n"},
		{cmdBankEnd, "", []string{"purge", "nope", "--yes", "--dir", dir}, "beerus: there is no bank with ID nope. Run beerus get banks to list them\n"},
		{cmdBankEnd, "", []string{"close", "bank-a", "--to", "bank-b", "--dir", dir}, "beerus: --to and --customer need --money transfer, payout or leave\n"},
		{cmdBankEnd, "", []string{"close", "bank-a", "--money", "transfer", "--customer", "cus_1", "--dir", dir}, "beerus: --customer takes id=choice, such as cus_1=payout; got \"cus_1\"\n"},
		{cmdApply, "", []string{"--wait", "-1s", "--dir", dir}, "beerus: --wait cannot be negative; use 0 to skip waiting\n"},
		{cmdGet, "", []string{"cats", "--dir", dir}, ""},
		{cmdBankEnd, "", []string{"close", "--dir", dir}, ""},
	} {
		r := invoke(c.fn, c.stdin, false, c.args...)
		if r.code != 1 || (c.want != "" && r.err != c.want) || (c.want == "" && !strings.Contains(r.err, "usage: beerus")) {
			t.Errorf("%v: %+v", c.args, r)
		}
	}
	if r := invoke(cmdDiff, "", false, "--dir", t.TempDir()); r.code != 1 || !strings.Contains(r.err, "holds no Beerus setup; run beerus up to make one") {
		t.Errorf("diff in an empty dir: %+v", r)
	}
}

func jsonEqual(t *testing.T, a, b string) {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("not json: %v\n%s", err, a)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("not json: %v\n%s", err, b)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("json differs:\n%s\n%s", a, b)
	}
}

func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !ok() {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

func TestSetupCommandsRunASetupThroughItsLife(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BEERUS_TEST_DATABASE_URL to run")
	}
	local := freeLocal(t)
	local.DatabaseURL = url
	today := clock.DateOf(time.Now())
	spec := setup.DefaultSpec("cli", today)
	dir := initDir(t, local, spec)
	withC := spec
	withC.Spec.Banks = append(append([]setup.Bank(nil), spec.Spec.Banks...), setup.Bank{ID: "bank-c", Name: "Bank C", Type: "digital", SettlesThrough: "bank-a"})
	raw, err := setup.Export(withC)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "setup.yaml")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var upOut, upErr bytes.Buffer
	died := make(chan error, 1)
	go func() {
		e := &cli{in: bufio.NewReader(strings.NewReader("")), out: &upOut, errOut: &upErr}
		died <- runUp(ctx, e, []string{"--dir", dir, "-f", file})
	}()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			if err := <-died; err != nil {
				t.Errorf("up: %v\n%s", err, upErr.String())
			}
		}
	}
	t.Cleanup(func() {
		stop()
		if _, err := connect.Reset(context.Background(), dir, url); err != nil {
			t.Errorf("clean up databases: %v", err)
		}
	})
	d := func(fn func(*cli, []string) error, stdin string, tty bool, args ...string) result {
		return invoke(fn, stdin, tty, append(args, "--dir", dir)...)
	}
	banks := func(args ...string) string { return d(cmdGet, "", false, append([]string{"banks"}, args...)...).out }
	until(t, "bank-c to become ready", func() bool {
		return regexp.MustCompile(`bank-c\s+Bank C\s+active\s+yes`).MatchString(banks())
	})
	if !strings.Contains(upOut.String(), "+ bank bank-c") {
		t.Fatalf("up -f should print the plan it applied:\n%s", upOut.String())
	}
	if r := d(cmdReset2, "", false, "cli"); r.code != 1 || !strings.Contains(r.err, "Beerus is running") {
		t.Fatalf("reset while running: %+v", r)
	}

	exported := filepath.Join(t.TempDir(), "export.yaml")
	if r := d(cmdExport, "", false, "-o", exported); r.code != 0 {
		t.Fatalf("export: %+v", r)
	}
	if r := d(cmdDiff, "", false, "-f", exported); r.code != 0 || r.out != "No changes.\n" {
		t.Fatalf("diff of the export: %+v", r)
	}
	body, err := os.ReadFile(exported)
	if err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(t.TempDir(), "renamed.yaml")
	if err := os.WriteFile(renamed, []byte(strings.Replace(string(body), "name: Bank C", "name: Abuja Digital", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	r := d(cmdDiff, "", false, "-f", renamed)
	if r.code != exitChanges || !strings.Contains(r.out, "~ bank bank-c") || !strings.Contains(r.out, "Plan: 1 to update.") {
		t.Fatalf("diff of a rename: %+v", r)
	}
	if r := d(cmdDiff, "", false, "-f", renamed, "--json"); r.code != exitChanges || !strings.Contains(r.out, `"changed": true`) {
		t.Fatalf("diff --json: %+v", r)
	}
	if r := d(cmdApply, "", false, "-f", renamed, "--wait", "60s"); r.code != 0 || !strings.Contains(r.out, "~ bank bank-c") {
		t.Fatalf("apply a rename: %+v", r)
	}
	if got := banks(); !regexp.MustCompile(`bank-c\s+Abuja Digital\s+active\s+yes\s+2`).MatchString(got) {
		t.Fatalf("get banks after the rename:\n%s", got)
	}
	if r := d(cmdApply, "", false, "-f", renamed); r.code != 0 || r.out != "No changes.\n" {
		t.Fatalf("a repeat apply: %+v", r)
	}
	st, err := setup.LoadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r := d(cmdVerify, "", false); r.out != "setup sha256:"+st.AppliedSHA256+"\n" {
		t.Fatalf("verify: %+v", r)
	}
	if r := d(cmdDescribe, "", false, "bank", "bank-c"); r.code != 0 || !strings.Contains(r.out, "Bank bank-c") || !strings.Contains(r.out, "name: Abuja Digital") || !strings.Contains(r.out, "Generation  2 (observed 2)") {
		t.Fatalf("describe: %+v", r)
	}
	if r := d(cmdDescribe, "", false, "bank", "nope"); r.code != 1 || r.err != "beerus: there is no bank with ID nope. Run beerus get banks to list them\n" {
		t.Fatalf("describe a missing bank: %+v", r)
	}

	cl, err := newCtlFor(mustConfig(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		fn   func(*cli, []string) error
		args []string
		path string
	}{
		{"get", cmdGet, []string{"banks"}, "/resources"},
		{"describe", cmdDescribe, []string{"bank", "bank-c"}, "/resources/bank/bank-c"},
	} {
		api, err := cl.send("GET", c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		r := d(c.fn, "", false, append(c.args, "--json")...)
		if r.code != 0 {
			t.Fatalf("%s --json: %+v", c.name, r)
		}
		jsonEqual(t, r.out, string(api))
	}
	exportJSON := d(cmdExport, "", false, "--json")
	wantApplied, _ := json.Marshal(mustStatus(t, dir).Applied)
	jsonEqual(t, exportJSON.out, string(wantApplied))
	if r := d(cmdApply, "", false, "-f", renamed, "--json"); r.code != 0 || !strings.Contains(r.out, `"changed": false`) || !strings.Contains(r.out, `"text": "No changes.\n"`) {
		t.Fatalf("apply --json: %+v", r)
	}

	without := mustStatus(t, dir).Applied
	without.Spec.Banks = without.Spec.Banks[:2]
	trimmed, err := setup.Export(without)
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(t.TempDir(), "cut.yaml")
	if err := os.WriteFile(cut, trimmed, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := d(cmdApply, "", false, "-f", cut); r.code != 1 || r.err != "beerus: The plan removes bank-c. Nothing was changed. Run again with --prune to remove it.\n" {
		t.Fatalf("apply a removal without --prune: %+v", r)
	}
	if r := d(cmdBankEnd, "", false, "purge", "bank-c"); r.code != 1 || !strings.Contains(r.err, "Run again with --yes to purge it") {
		t.Fatalf("purge without --yes: %+v", r)
	}
	if r := d(cmdBankEnd, "bank-x\n", true, "purge", "bank-c"); r.code != 1 || r.err != "Type bank-c to confirm: beerus: bank-c was not confirmed. Nothing was changed\n" || !strings.Contains(r.out, "- bank bank-c") {
		t.Fatalf("purge with the wrong ID: %+v", r)
	}
	if !strings.Contains(banks(), "bank-c") {
		t.Fatal("a refused purge removed the bank")
	}

	ada := map[string]string{"name": "Ada Obi", "phone": "08031234567", "dob": "1990-04-21"}
	cust, err := cl.do("POST", "/banks/bank-b/customers", ada)
	if err != nil {
		t.Fatal(err)
	}
	acct, err := cmdAccount(cl, []string{"open", "--bank", "bank-b", "--customer", fmt.Sprint(cust["id"])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmdCash(cl, "deposit", []string{fmt.Sprint(acct["nuban"]), "--bank", "bank-b", "--amount", "5000"}); err != nil {
		t.Fatal(err)
	}
	r = d(cmdBankEnd, "", false, "purge", "bank-b", "--yes", "--wait", "5s")
	if r.code != exitBlocked || !strings.Contains(r.err, "bank bank-b: MoneyDecisionNeeded:") || !strings.Contains(r.err, "5,000.00") {
		t.Fatalf("purge with money and no decision: %+v", r)
	}
	if r := d(cmdBankEnd, "", false, "close", "bank-a"); r.code != 1 || r.err != "beerus: bank-a needs a money decision. Add --money transfer, payout or leave\n" {
		t.Fatalf("close without a money decision: %+v", r)
	}
	r = d(cmdBankEnd, "", false, "purge", "bank-b", "--yes", "--money", "transfer", "--to", "bank-a", "--settle-now", "--wait", "90s")
	if r.code != 0 || strings.Contains(banks(), "bank-b") {
		t.Fatalf("purge with a money decision: %+v\n%s", r, banks())
	}
	moved, err := cl.do("GET", "/banks/bank-a/customers", nil)
	if err != nil || !strings.Contains(fmt.Sprint(moved["data"]), "Ada Obi") {
		t.Fatalf("customers at bank-a after the purge: %v %v", moved, err)
	}

	if r := d(cmdBankEnd, "bank-c\n", true, "close", "bank-c", "--money", "leave", "--wait", "60s"); r.code != 0 || !strings.Contains(r.err, "Type bank-c to confirm: ") {
		t.Fatalf("close at the prompt: %+v", r)
	}
	if got := banks(); !regexp.MustCompile(`bank-c\s+Abuja Digital\s+closed\s+yes`).MatchString(got) {
		t.Fatalf("get banks after the close:\n%s", got)
	}
	if text, err := renderCompose(mustConfig(t, dir)); err != nil || strings.Contains(string(text), "bank-c:") || !strings.Contains(string(text), "bank-a:") {
		t.Fatalf("compose after the close: %v", err)
	}
	if r := d(cmdBankEnd, "", false, "close", "bank-c", "--money", "leave"); r.code != 1 || r.err != "beerus: bank-c is already closed. Nothing was changed\n" {
		t.Fatalf("close twice: %+v", r)
	}
	closeJSON := d(cmdBankEnd, "bank-c\n", true, "purge", "bank-c", "--json", "--wait", "60s")
	if closeJSON.code != 0 || !strings.Contains(closeJSON.out, `"op": "remove"`) || strings.Contains(banks(), "bank-c") {
		t.Fatalf("purge at the prompt with --json: %+v", closeJSON)
	}

	stop()
	if r := d(cmdReset2, "", false, "wrong"); r.code != 1 || !strings.Contains(r.err, `holds setup "cli", not "wrong"`) {
		t.Fatalf("reset with the wrong name: %+v", r)
	}
	r = d(cmdReset2, "", false, "cli", "--json")
	var dropped struct {
		Dropped []string `json:"dropped"`
	}
	if err := json.Unmarshal([]byte(r.out), &dropped); err != nil || r.code != 0 || len(dropped.Dropped) < 3 {
		t.Fatalf("reset: %+v %v", r, err)
	}
	prefix := strings.TrimSuffix(config.DatabaseName(dir, "x"), "x")
	left, err := store.List(context.Background(), url, prefix)
	if err != nil || len(left) != 0 {
		t.Fatalf("databases left after reset: %v %v", left, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != connect.LocalFile {
		t.Fatalf("left in the dir after reset: %v %v", entries, err)
	}
}

func cmdReset2(e *cli, args []string) error {
	return cmdReset(args, e.out)
}

func mustConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	c, err := connect.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustStatus(t *testing.T, dir string) setup.Status {
	t.Helper()
	st, err := setup.LoadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
