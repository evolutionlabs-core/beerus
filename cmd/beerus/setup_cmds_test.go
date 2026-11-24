package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/seed"
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

func initDir(t *testing.T, spec setup.Spec) string {
	t.Helper()
	dir := t.TempDir()
	if err := seed.Init(dir, spec, setup.DefaultLocal(), clock.DateOf(time.Now())); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustStatus(t *testing.T, dir string) setup.Status {
	t.Helper()
	st, err := setup.LoadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSetupCommandsAnswerWithoutARunningBeerus(t *testing.T) {
	spec := banktest.Spec(defaultSetupName)
	dir := initDir(t, spec)
	file := filepath.Join(dir, "copy.yaml")

	for _, c := range []struct {
		fn   func(*cli, []string) error
		args []string
	}{
		{cmdApply, []string{"--dir", dir}},
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
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "bank-b") || strings.Contains(string(raw), "nuban_code") {
		t.Fatalf("export should leave out the assigned codes:\n%s", raw)
	}
	if r := invoke(cmdDiff, "", false, "--dir", dir, "-f", file); r.code != 0 || r.out != "No changes.\n" {
		t.Fatalf("offline diff of the export: %+v", r)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(dir, "renamed.yaml")
	if err := os.WriteFile(renamed, []byte(strings.Replace(string(body), "name: Bank B", "name: Zenith Bank", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := invoke(cmdDiff, "", false, "--dir", dir, "-f", renamed); r.code != exitChanges || !strings.Contains(r.out, "~ bank bank-b") || !strings.Contains(r.out, "Zenith Bank") || r.err != "" {
		t.Fatalf("offline diff of a rename: %+v", r)
	}
	st := mustStatus(t, dir)
	st.Tombstones = append(st.Tombstones, nip.Institution{ID: "bank-z", Name: "Bank Z", NIPCode: "000026", NUBANCode: "026", Type: "commercial", Status: nip.StatusRemoved})
	if err := setup.SaveStatus(dir, st); err != nil {
		t.Fatal(err)
	}
	reused := filepath.Join(dir, "reused.yaml")
	if err := os.WriteFile(reused, []byte(strings.Replace(string(body), "id: bank-b", "id: bank-z", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := invoke(cmdDiff, "", false, "--dir", dir, "-f", reused); r.code != 1 || !strings.Contains(r.err, "banks[bank-z].id bank-z belonged to a removed bank. Choose another id.") {
		t.Fatalf("offline diff that reuses a removed bank's id: %+v", r)
	}
	if r := invoke(cmdVerify, "", false, "--dir", dir); r.code != 0 || !regexp.MustCompile(`^setup sha256:[0-9a-f]{64}\n$`).MatchString(r.out) {
		t.Fatalf("verify: %+v", r)
	}
	if r := invoke(cmdVerify, "", false, "--dir", dir, "--json"); !regexp.MustCompile(`^\{\n  "setup_sha256": "[0-9a-f]{64}"\n\}\n$`).MatchString(r.out) {
		t.Fatalf("verify --json: %+v", r)
	}
	c, err := setup.LoadConfig(dir)
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
		{cmdBankEnd, "", []string{"purge", "bank-b", "--yes", "--wait", "0", "--dir", dir, "--money", "transfer"}, "beerus: --money transfer needs --to <bank>\n"},
		{cmdBankEnd, "", []string{"purge", "bank-b", "--yes", "--wait", "0", "--dir", dir, "--money", "payout", "--customer", "cus_1=transfer"}, "beerus: --customer with transfer needs --to <bank>\n"},
		{cmdBankEnd, "", []string{"purge", "bank-b", "--yes", "--wait", "0", "--dir", dir, "--money", "move", "--to", "bank-a"}, "beerus: --money \"move\" is not valid. Use transfer, payout or leave\n"},
		{cmdBankEnd, "", []string{"purge", "bank-b", "--yes", "--wait", "0", "--dir", dir, "--money", "leave", "--to", "bank-a"}, "beerus: --to is only for --money transfer. Payout is cash and needs no bank\n"},
		{cmdBankEnd, "", []string{"purge", "bank-b", "--yes", "--wait", "0", "--dir", dir, "--money", "payout", "--to", "bank-a"}, "beerus: --to is only for --money transfer. Payout is cash and needs no bank\n"},
		{cmdApply, "", []string{"--prune", "--money", "transfer", "--wait", "0", "--dir", dir}, "beerus: --money transfer needs --to <bank>\n"},
		{cmdApply, "", []string{"--wait", "-1s", "--dir", dir}, "beerus: --wait cannot be negative; use 0 to skip waiting\n"},
		{cmdGet, "", []string{"cats", "--dir", dir}, ""},
		{cmdBankEnd, "", []string{"close", "--dir", dir}, ""},
	} {
		r := invoke(c.fn, c.stdin, false, c.args...)
		if r.code != 1 || (c.want != "" && r.err != c.want) || (c.want == "" && !strings.Contains(r.err, "usage: beerus")) {
			t.Errorf("%v: %+v", c.args, r)
		}
	}
	if r := invoke(cmdDiff, "", false, "--dir", t.TempDir()); r.code != 1 || !strings.Contains(r.err, "holds no Beerus setup; run beerus init to make one") {
		t.Errorf("diff in an empty dir: %+v", r)
	}
}
