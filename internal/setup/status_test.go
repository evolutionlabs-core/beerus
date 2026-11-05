package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func sampleStatus() Status {
	at := time.Date(2026, time.November, 4, 9, 12, 0, 0, time.UTC)
	return Status{
		Setup:         "beerus",
		SetupKey:      1<<63 + 5,
		Applied:       DefaultSpec("beerus", clock.Date{Y: 2026, M: time.November, D: 4}),
		AppliedSHA256: "abc",
		Resources: []Resource{{
			Kind:                KindBank,
			ID:                  "bank-b",
			Generation:          3,
			ObservedGeneration:  3,
			Phase:               PhaseTerminating,
			DeletionRequestedAt: &at,
			Finalizers:          []string{FinalizerSettleInflight},
			Done:                []string{FinalizerStopRouting},
			Money:               &Removal{Money: MoneyTransfer, To: "bank-a"},
			Allocated:           Allocation{NIPCode: "999002", NUBANCode: "902", Port: 7451, APIPort: 7461},
			Owned:               []Owned{{Kind: "database", Name: "beerus_x_bank_b"}},
			Steps:               map[string]string{StepAllocate: "done"},
			Result:              json.RawMessage(`{"moved_minor":5}`),
			Conditions:          []Condition{{Type: ConditionReady, Status: "False", Reason: ReasonTerminating, Message: "Removing bank-b.", ChangedAt: at}},
			Events:              []Event{{LastAt: at, Reason: "FinalizerDone", Message: "stop-routing done", Count: 1}},
		}},
	}
}

func TestStatusRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := sampleStatus()
	if err := SaveStatus(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	in.Version = StatusVersion
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("got  %+v\nwant %+v", out, in)
	}
	if out.SetupKey != 1<<63+5 {
		t.Fatalf("setup key = %d", out.SetupKey)
	}
}

func TestSaveStatusWritesMode0600AndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveStatus(dir, sampleStatus()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir holds %d files", len(entries))
	}
}

func TestSaveStatusMissingDir(t *testing.T) {
	err := SaveStatus(filepath.Join(t.TempDir(), "gone"), sampleStatus())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}

func TestLoadStatusMissingFile(t *testing.T) {
	_, err := LoadStatus(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}

func TestLoadStatusRejects(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"newer version", `{"version":2,"setup":"beerus","future":true}`, "control.json has version 2, but this beerus reads up to version 1. Upgrade beerus"},
		{"no version", `{"setup":"beerus"}`, "control.json has no valid version (got 0). Run beerus reset to start again"},
		{"unknown field", `{"version":1,"setup":"beerus","colour":"red"}`, `decode control.json: json: unknown field "colour"`},
		{"not json", `{`, "decode control.json: unexpected end of JSON input"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "control.json"), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadStatus(dir)
			if err == nil || err.Error() != c.want {
				t.Fatalf("got %v", err)
			}
		})
	}
}
