package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func sampleStatus() Status {
	at := time.Date(2026, time.November, 4, 9, 12, 0, 0, time.UTC)
	return Status{
		Setup:         "beerus",
		SetupKey:      1<<63 + 5,
		Applied:       DefaultSpec("beerus"),
		AppliedSHA256: "abc",
		Resources: []Resource{{
			Kind:                KindBank,
			ID:                  "bank-b",
			Generation:          3,
			ObservedGeneration:  3,
			Phase:               config.PhaseTerminating,
			DeletionRequestedAt: &at,
			Finalizers:          []string{FinalizerSettleInflight},
			Done:                []string{FinalizerStopRouting},
			Money:               &Removal{Money: MoneyTransfer, To: "bank-a"},
			Allocated:           Allocation{NIPCode: "999002", NUBANCode: "902", Port: 7451, APIPort: 7461},
			Owned:               []Owned{{Kind: "database", Name: "beerus_x_bank_b"}},
			Steps:               map[string]string{config.StepAllocate: "done"},
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

func writeStatus(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "control.json"), []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadStatusErrors(t *testing.T) {
	older := "This setup was made by an older Beerus. Run beerus reset, then beerus init."
	rows := []struct {
		name, body, want string
		is               error
		corrupt          bool
	}{
		{"version 1", `{"version":1,"setup":"beerus","clock":{"anchor_business":"2026-11-04T08:00:00+01:00"},"applied":{"spec":{"start":"2026-11-04T08:00:00+01:00"}}}`, "%s: " + older, ErrOldSetup, false},
		{"version 0", `{"version":0,"setup":"beerus"}`, "%s: " + older, ErrOldSetup, false},
		{"no version", `{"setup":"beerus"}`, "%s: " + older, ErrOldSetup, false},
		{"newer version", `{"version":3,"setup":"beerus","future":true}`, "control.json has version 3, but this beerus reads up to version 2. Upgrade beerus", nil, false},
		{"unknown field", `{"version":2,"setup":"beerus","colour":"red"}`, `decode control.json: json: unknown field "colour"`, nil, false},
		{"not json", `{`, "decode control.json: unexpected end of JSON input", nil, true},
		{"empty", ``, "decode control.json: unexpected end of JSON input", nil, true},
		{"version as text", `{"version":"x"}`, "", nil, true},
	}
	for _, r := range rows {
		dir := writeStatus(t, r.body, 0o600)
		_, err := LoadStatus(dir)
		want := r.want
		if strings.Contains(want, "%s") {
			want = fmt.Sprintf(want, dir)
		}
		if err == nil || (want != "" && err.Error() != want) || (r.is != nil && !errors.Is(err, r.is)) || IsCorrupt(err) != r.corrupt {
			t.Errorf("%s: got %v (corrupt %v), want %q (corrupt %v)", r.name, err, IsCorrupt(err), want, r.corrupt)
		}
	}
	if _, err := LoadStatus(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: got %v", err)
	}
}

func TestSaveStatus(t *testing.T) {
	dir := writeStatus(t, "{}", 0o644)
	if err := SaveStatus(dir, sampleStatus()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || info.Mode().Perm() != 0o600 || len(entries) != 1 {
		t.Fatalf("mode %v, %d files, %v", info.Mode().Perm(), len(entries), err)
	}
	if err := SaveStatus(filepath.Join(dir, "gone"), sampleStatus()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir: got %v", err)
	}
}

func TestLoadStatusDropsOldPartnerCertificateFields(t *testing.T) {
	dir := writeStatus(t, `{"version":2,"setup":"beerus","resources":[{"kind":"Partner","id":"balance","partner":{"cert_cn":"balance","api_key_sha256":"abc","cert_sha256":"def"}}]}`, 0o600)
	st, err := LoadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Resources[0].Partner; got == nil || *got != (PartnerKeys{SecretSHA256: "abc"}) {
		t.Fatalf("partner keys %+v, want only the old key hash abc as the secret hash", got)
	}
	if err := SaveStatus(dir, st); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "control.json"))
	if err != nil || bytes.Contains(saved, []byte("cert_")) || bytes.Contains(saved, []byte("api_key_sha256")) || !bytes.Contains(saved, []byte(`"secret_sha256":"abc"`)) {
		t.Fatalf("a saved status still holds old fields or lost the secret hash: %v %s", err, saved)
	}
}
