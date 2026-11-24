package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func bankJSON(id, code, status string) string {
	return `{"id":"` + id + `","name":"Bank","nip_code":"` + code + `","nuban_code":"01","type":"commercial","status":"` + status + `"}`
}

func moveJSON(id, from, status string) string {
	return `{"session_id":"` + id + `","from_account":"` + from + `","to_bank_code":"000015","to_account":"9020000022","amount_minor":150000,"status":"` + status + `","created_at":"2026-11-14T10:30:00+01:00"}`
}

func openJSON(transfers int, outward, inward, pending int64) string {
	return `{"bank_id":"bank-a","transfers":` + strconv.Itoa(transfers) + `,"outward_minor":` + strconv.FormatInt(outward, 10) + `,"inward_minor":` + strconv.FormatInt(inward, 10) + `,"pending_inward_minor":` + strconv.FormatInt(pending, 10) + `,"unposted_settlements":0}`
}

func readNIBSS(ctx context.Context, n *nibssAdmin, op string) (string, error) {
	switch op {
	case "directory":
		list, err := n.Directory(ctx)
		out := []string{}
		for _, in := range list {
			out = append(out, in.ID+" "+in.NIPCode+" "+in.Status)
		}
		return strings.Join(out, "|"), err
	case "member":
		in, err := n.member(ctx, "bank-a")
		return in.ID + " " + in.Status, err
	case "session":
		x, err := n.session(ctx, "s1")
		return fmt.Sprintf("%s %s %s %d %s %s", x.ID, x.FromCode, x.ToCode, x.Amount, x.Status, x.Outcome), err
	}
	list, err := n.closureSessions(ctx, "bank-a")
	out := []string{}
	for _, x := range list {
		out = append(out, x.ID+" "+x.Status)
	}
	return strings.Join(out, "|"), err
}

func TestNIBSSAdminReadsWhatNIBSSKeeps(t *testing.T) {
	session := `{"session_id":"s1","from_code":"000014","to_code":"000015","from_nuban":"9010000011","to_nuban":"9020000022","amount_minor":150000,"narration":"rent","status":"00","outcome":"credited","tsq_attempts":0,"created_at":"2026-11-14T10:30:00+01:00"}`
	for _, r := range []struct{ name, op, path, body, want string }{
		{"the register in one page", "directory", control.NIBSSDirectoryPath, `{"data":[` + bankJSON("bank-a", "000011", "active") + `,` + bankJSON("bank-b", "000012", "removed") + `]}`, "bank-a 000011 active|bank-b 000012 removed"},
		{"a cursor that does not move", "directory", control.NIBSSDirectoryPath, `{"data":[` + bankJSON("bank-a", "000011", "active") + `],"next_cursor":"000011"}`, `cursor "000011" did not move`},
		{"one bank", "member", nibssBanksPath + "bank-a", bankJSON("bank-a", "000011", "terminating"), "bank-a terminating"},
		{"an unknown bank", "member", "", "", "bank id bank-a is not in the NIBSS directory"},
		{"one session", "session", nibssSessionsPath + "s1", session, "s1 000014 000015 150000 00 credited"},
		{"an unknown session", "session", "", "", "session s1: 25 no record"},
		{"closure sessions", "closure", nibssBanksPath + "bank-a" + nibssClosureSuffix, `{"data":[` + moveJSON("c1", "9010000011", "00") + `,` + moveJSON("c2", "9010000011", "91") + `]}`, "c1 00|c2 91"},
	} {
		got, err := readNIBSS(t.Context(), controlAt(map[string]string{r.path: r.body}), r.op)
		if err != nil {
			got = err.Error()
		}
		if got != r.want {
			t.Errorf("%s: %q, want %q", r.name, got, r.want)
		}
	}
}

func TestOpenTransfersAddPendingInward(t *testing.T) {
	overflow := "the open transfers of the bank add up to more than 64 bits"
	away := &nibssAdmin{control.NewNIBSSControl(control.NewLink("", ""), 1)}
	for _, r := range []struct {
		name      string
		body      string
		transfers int
		net       int64
		err       string
	}{
		{"nothing open", openJSON(0, 0, 0, 0), 0, 0, ""},
		{"sent and received", openJSON(3, 500000, 270000, 0), 3, -230000, ""},
		{"pending inward counts", openJSON(3, 500000, 270000, 30000), 3, -200000, ""},
		{"largest outward", openJSON(1, math.MaxInt64, 0, 0), 1, -math.MaxInt64, ""},
		{"inward and pending overflow", openJSON(2, 0, math.MaxInt64, 1), 0, 0, overflow},
		{"a bank NIBSS does not know", "", 0, 0, ""},
	} {
		a := &App{nibssCtl: controlAt(map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: r.body})}
		if r.body == "" {
			a.nibssCtl = controlAt(nil)
		}
		transfers, net, err := a.openTransfers(t.Context(), "bank-a")
		if msg := fmt.Sprint(err); (err == nil && r.err != "") || (err != nil && !strings.Contains(msg, r.err)) || transfers != r.transfers || net != r.net {
			t.Errorf("%s: %d, %d, %v; want %d, %d, %q", r.name, transfers, net, err, r.transfers, r.net, r.err)
		}
	}
	a := &App{nibssCtl: away}
	if _, _, err := a.openTransfers(t.Context(), "bank-a"); err == nil || err.Error() != "NIBSS is not answering, so the open transfers of bank-a cannot be counted. Try again soon." {
		t.Errorf("NIBSS down: %v", err)
	}
}

func TestPlanReadsTheRegisterOnlyToAddABank(t *testing.T) {
	spec := func(ids ...string) setup.Spec {
		var s setup.Spec
		for _, id := range ids {
			s.Spec.Banks = append(s.Spec.Banks, setup.Bank{ID: id})
		}
		return s
	}
	live := controlAt(map[string]string{control.NIBSSDirectoryPath: `{"data":[` + bankJSON("bank-x", "000019", "removed") + `]}`})
	away := &nibssAdmin{control.NewNIBSSControl(control.NewLink("", ""), 1)}
	for _, r := range []struct {
		name             string
		ctl              *nibssAdmin
		desired, applied setup.Spec
		want             int
		err              string
	}{
		{"a new bank reads the register", live, spec("bank-a", "bank-b"), spec("bank-a"), 1, ""},
		{"no new bank reads nothing", away, spec("bank-a"), spec("bank-a", "bank-b"), 0, ""},
		{"a new bank while NIBSS is away", away, spec("bank-a", "bank-b"), spec("bank-a"), 0, "The NIBSS directory is not open yet, so removed banks cannot be checked. Try again soon."},
	} {
		a := &App{nibssCtl: r.ctl}
		got, err := a.planDirectory(t.Context(), r.desired, setup.Status{Applied: r.applied})
		msg := ""
		var ae *applyError
		if errors.As(err, &ae) {
			msg = ae.message
		}
		if (err != nil) != (r.err != "") || len(got) != r.want || msg != r.err {
			t.Errorf("%s: %d banks, %v; want %d, %q", r.name, len(got), err, r.want, r.err)
		}
	}
}

func TestClosureKeyCountsDefiniteFailuresOfTheAccount(t *testing.T) {
	move := func(status, from string) string { return moveJSON("s", from, status) }
	join := func(rows ...string) string {
		return `{"data":[` + strings.Join(rows, ",") + `]}`
	}
	const key = "close:bank-a:0000000014"
	for _, r := range []struct{ name, body, want string }{
		{"no moves yet", join(), key},
		{"one move still pending", join(move("pending", "0000000014")), key},
		{"one failed move", join(move("91", "0000000014")), key + ":1"},
		{"two failed, then one sent", join(move("91", "0000000014"), move("51", "0000000014"), move("00", "0000000014")), key + ":2"},
		{"another account's failure", join(move("91", "0000000022")), key},
	} {
		a := &App{nibssCtl: controlAt(map[string]string{nibssBanksPath + "bank-a" + nibssClosureSuffix: r.body})}
		if got, err := a.closureKey(t.Context(), "bank-a", "0000000014", key); err != nil || got != r.want {
			t.Errorf("%s: %q, %v; want %q", r.name, got, err, r.want)
		}
	}
}
