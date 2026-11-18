package connect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestNIBSSAdminReadsWhatNIBSSKeeps(t *testing.T) {
	bank := func(id, code, status string) string {
		return `{"id":"` + id + `","name":"Bank","nip_code":"` + code + `","nuban_code":"01","type":"commercial","status":"` + status + `"}`
	}
	sessionBody := `{"session_id":"s1","from_code":"000014","to_code":"000015","from_nuban":"9010000011","to_nuban":"9020000022","amount_minor":150000,"narration":"rent","status":"00","outcome":"credited","tsq_attempts":0,"created_at":"2026-11-14T10:30:00+01:00"}`
	closureRow := func(id, status string) string {
		return `{"session_id":"` + id + `","from_account":"9010000011","to_bank_code":"000015","to_account":"9020000022","amount_minor":150000,"status":"` + status + `","created_at":"2026-11-14T10:30:00+01:00"}`
	}
	rows := []struct {
		name   string
		routes map[string]string
		call   func(context.Context, *nibssAdmin) (string, error)
		want   string
		err    string
	}{
		{
			name:   "the register in one page",
			routes: map[string]string{server.NIBSSDirectoryPath: `{"data":[` + bank("bank-a", "000011", "active") + `,` + bank("bank-b", "000012", "removed") + `]}`},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				list, err := n.Directory(ctx)
				out := []string{}
				for _, in := range list {
					out = append(out, in.ID+" "+in.NIPCode+" "+in.Status)
				}
				return strings.Join(out, "|"), err
			},
			want: "bank-a 000011 active|bank-b 000012 removed",
		},
		{
			name:   "an empty register",
			routes: map[string]string{server.NIBSSDirectoryPath: `{"data":[]}`},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				list, err := n.Directory(ctx)
				return fmt.Sprint(len(list)), err
			},
			want: "0",
		},
		{
			name:   "a cursor that does not move",
			routes: map[string]string{server.NIBSSDirectoryPath: `{"data":[` + bank("bank-a", "000011", "active") + `],"next_cursor":"000011"}`},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				_, err := n.Directory(ctx)
				return "", err
			},
			err: `cursor "000011" did not move`,
		},
		{
			name:   "one bank",
			routes: map[string]string{nibssBanksPath + "bank-a": bank("bank-a", "000011", "terminating")},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				in, err := n.member(ctx, "bank-a")
				return in.ID + " " + in.Status, err
			},
			want: "bank-a terminating",
		},
		{
			name:   "an unknown bank",
			routes: map[string]string{},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				_, err := n.member(ctx, "bank-z")
				return "", err
			},
			err: "bank id bank-z is not in the NIBSS directory",
		},
		{
			name:   "one session",
			routes: map[string]string{nibssSessionsPath + "s1": sessionBody},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				x, err := n.session(ctx, "s1")
				return fmt.Sprintf("%s %s %s %d %s %s", x.ID, x.FromCode, x.ToCode, x.Amount, x.Status, x.Outcome), err
			},
			want: "s1 000014 000015 150000 00 credited",
		},
		{
			name:   "an unknown session",
			routes: map[string]string{},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				_, err := n.session(ctx, "s9")
				return "", err
			},
			err: "session s9: 25 no record",
		},
		{
			name:   "closure sessions",
			routes: map[string]string{nibssBanksPath + "bank-a" + nibssClosureSuffix: `{"data":[` + closureRow("c1", "00") + `,` + closureRow("c2", "91") + `]}`},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				list, err := n.closureSessions(ctx, "bank-a")
				out := []string{}
				for _, x := range list {
					out = append(out, fmt.Sprintf("%s %s %s %s %d %s", x.ID, x.FromAccount, x.ToBankCode, x.ToAccount, x.Amount, x.Status))
				}
				return strings.Join(out, "|"), err
			},
			want: "c1 9010000011 000015 9020000022 150000 00|c2 9010000011 000015 9020000022 150000 91",
		},
		{
			name:   "closure sessions of an unknown bank",
			routes: map[string]string{},
			call: func(ctx context.Context, n *nibssAdmin) (string, error) {
				_, err := n.closureSessions(ctx, "bank-z")
				return "", err
			},
			err: "404 Not Found",
		},
	}
	for _, r := range rows {
		got, err := r.call(t.Context(), controlAt(t, r.routes))
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if got != r.want || msg != r.err {
			t.Errorf("%s: %q, %q; want %q, %q", r.name, got, msg, r.want, r.err)
		}
	}
}

func TestOpenTransfersAddPendingInward(t *testing.T) {
	open := func(transfers int, outward, inward, pending int64) string {
		return `{"bank_id":"bank-a","transfers":` + strconv.Itoa(transfers) + `,"outward_minor":` + strconv.FormatInt(outward, 10) + `,"inward_minor":` + strconv.FormatInt(inward, 10) + `,"pending_inward_minor":` + strconv.FormatInt(pending, 10) + `,"unposted_settlements":0}`
	}
	rows := []struct {
		name      string
		routes    map[string]string
		transfers int
		net       int64
		err       string
	}{
		{"nothing open", map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: open(0, 0, 0, 0)}, 0, 0, ""},
		{"sent and received", map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: open(3, 500000, 270000, 0)}, 3, -230000, ""},
		{"pending inward counts", map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: open(3, 500000, 270000, 30000)}, 3, -200000, ""},
		{"only pending inward", map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: open(1, 0, 0, 10000)}, 1, 10000, ""},
		{"largest outward", map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: open(1, math.MaxInt64, 0, 0)}, 1, -math.MaxInt64, ""},
		{"inward and pending overflow", map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: open(2, 0, math.MaxInt64, 1)}, 0, 0, "the open transfers of the bank add up to more than 64 bits"},
		{"a bank NIBSS does not know", map[string]string{}, 0, 0, ""},
	}
	for _, r := range rows {
		a := &App{nibssCtl: controlAt(t, r.routes)}
		transfers, net, err := a.openTransfers(t.Context(), "bank-a")
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if transfers != r.transfers || net != r.net || msg != r.err {
			t.Errorf("%s: %d, %d, %q; want %d, %d, %q", r.name, transfers, net, msg, r.transfers, r.net, r.err)
		}
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
	register := `{"data":[{"id":"bank-x","name":"Bank X","nip_code":"000019","nuban_code":"019","type":"commercial","status":"removed"}]}`
	live := controlAt(t, map[string]string{server.NIBSSDirectoryPath: register})
	away := &nibssAdmin{server.NewNIBSSControl(server.NewLink("", ""), 1)}
	rows := []struct {
		name    string
		ctl     *nibssAdmin
		desired setup.Spec
		applied setup.Spec
		want    int
		err     string
	}{
		{"a new bank reads the register", live, spec("bank-a", "bank-b"), spec("bank-a"), 1, ""},
		{"no new bank reads nothing", away, spec("bank-a"), spec("bank-a", "bank-b"), 0, ""},
		{"a bank taken out reads nothing", away, spec(), spec("bank-a"), 0, ""},
		{"a new bank while NIBSS is away", away, spec("bank-a", "bank-b"), spec("bank-a"), 0, "The NIBSS directory is not open yet, so removed banks cannot be checked. Try again soon."},
	}
	for _, r := range rows {
		a := &App{nibssCtl: r.ctl}
		got, err := a.planDirectory(t.Context(), r.desired, setup.Status{Applied: r.applied})
		msg := ""
		if err != nil {
			var ae *applyError
			if !errors.As(err, &ae) {
				t.Fatalf("%s: %v is not an apply error", r.name, err)
			}
			msg = ae.message
		}
		if len(got) != r.want || msg != r.err {
			t.Errorf("%s: %d banks, %q; want %d, %q", r.name, len(got), msg, r.want, r.err)
		}
	}
}

func TestOpenTransfersWhenNIBSSIsDown(t *testing.T) {
	a := &App{nibssCtl: &nibssAdmin{server.NewNIBSSControl(server.NewLink("", ""), 1)}}
	_, _, err := a.openTransfers(t.Context(), "bank-a")
	want := "NIBSS is not answering, so the open transfers of bank-a cannot be counted. Try again soon."
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestClosureKeyCountsDefiniteFailuresOfTheAccount(t *testing.T) {
	row := func(id, from, status string) string {
		return `{"session_id":"` + id + `","from_account":"` + from + `","to_bank_code":"000002","to_account":"0000000011","amount_minor":100,"status":"` + status + `","created_at":"2026-11-18T06:00:00+01:00"}`
	}
	page := func(rows ...string) map[string]string {
		return map[string]string{nibssBanksPath + "bank-a" + nibssClosureSuffix: `{"data":[` + strings.Join(rows, ",") + `]}`}
	}
	rows := []struct {
		name   string
		routes map[string]string
		want   string
	}{
		{"no moves yet", page(), "close:bank-a:0000000014"},
		{"one move sent", page(row("s1", "0000000014", "00")), "close:bank-a:0000000014"},
		{"one move still pending", page(row("s1", "0000000014", "pending")), "close:bank-a:0000000014"},
		{"one failed move", page(row("s1", "0000000014", "91")), "close:bank-a:0000000014:1"},
		{"two failed, then one sent", page(row("s1", "0000000014", "91"), row("s2", "0000000014", "51"), row("s3", "0000000014", "00")), "close:bank-a:0000000014:2"},
		{"another account's failure", page(row("s1", "0000000022", "91")), "close:bank-a:0000000014"},
	}
	for _, r := range rows {
		a := &App{nibssCtl: controlAt(t, r.routes)}
		got, err := a.closureKey(t.Context(), "bank-a", "0000000014", "close:bank-a:0000000014")
		if err != nil || got != r.want {
			t.Errorf("%s: %q, %v; want %q", r.name, got, err, r.want)
		}
	}
}
