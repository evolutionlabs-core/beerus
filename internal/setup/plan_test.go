package setup

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
)

var today = clock.Date{Y: 2026, M: 11, D: 5}

func spec() Spec {
	return Spec{
		APIVersion: APIVersion,
		Kind:       KindSetup,
		Metadata:   Metadata{Name: "beerus"},
		Spec: Body{
			Start: "2026-11-04T08:00:00+01:00",
			NIP:   NIP{Sessions: 4, TSQAfter: "2m", TSQAttempts: 3},
			Connect: ConnectLegal{
				LegalName:  "Beerus Connect Ltd",
				BrandName:  "Beerus",
				RegistryID: "OB-0001",
				CACNumber:  "RC-000001",
			},
			Holidays: Holidays{Add: []string{"2026-12-28"}},
			Banks: []Bank{
				{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "999001", NUBANCode: "901"},
				{ID: "bank-b", Name: "Bank B", Type: "digital", NIPCode: "999002", NUBANCode: "902"},
			},
			Partners: []Partner{{ID: "balance", Name: "Balance", WebhookURL: "https://localhost:3000/webhooks"}},
		},
	}
}

func doneSteps() map[string]string {
	return map[string]string{StepAllocate: stepDone, StepDirectory: stepDone, StepDatabase: stepDone, StepCerts: stepDone, StepCBN: stepDone, StepSite: stepDone, StepAPI: stepDone}
}

func status(applied Spec) Status {
	st := Status{Version: StatusVersion, Setup: applied.Metadata.Name, Applied: applied}
	for _, b := range applied.Spec.Banks {
		st.Resources = append(st.Resources, Resource{
			Kind:      KindBank,
			ID:        b.ID,
			Phase:     PhaseActive,
			Allocated: Allocation{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Database: "beerus_x_" + strings.ReplaceAll(b.ID, "-", "_")},
			Owned:     []Owned{{Kind: ownedDatabase, Name: "beerus_x_" + strings.ReplaceAll(b.ID, "-", "_")}, {Kind: "port", Name: "7451"}},
			Steps:     doneSteps(),
		})
	}
	for _, x := range applied.Spec.Partners {
		st.Resources = append(st.Resources, Resource{Kind: KindPartner, ID: x.ID, Phase: PhaseActive})
	}
	st.Resources = append(st.Resources, Resource{Kind: KindSystem, ID: SystemNIBSS, Phase: PhaseActive, Steps: map[string]string{StepDatabase: stepDone}})
	return st
}

func bank(s *Spec, id string) *Bank {
	for i := range s.Spec.Banks {
		if s.Spec.Banks[i].ID == id {
			return &s.Spec.Banks[i]
		}
	}
	panic("no bank " + id)
}

func res(st *Status, kind, id string) *Resource {
	for i := range st.Resources {
		if st.Resources[i].Kind == kind && st.Resources[i].ID == id {
			return &st.Resources[i]
		}
	}
	panic("no resource " + id)
}

func dropBank(s *Spec, id string) {
	var keep []Bank
	for _, b := range s.Spec.Banks {
		if b.ID != id {
			keep = append(keep, b)
		}
	}
	s.Spec.Banks = keep
}

func tombstone(id, name, nip, nuban string) nibss.Institution {
	return nibss.Institution{ID: id, Name: name, NIPCode: nip, NUBANCode: nuban, Type: "commercial", Status: nibss.StatusRemoved}
}

func member(id, name, nip, nuban string) nibss.Institution {
	return nibss.Institution{ID: id, Name: name, NIPCode: nip, NUBANCode: nuban, Type: "commercial", Status: nibss.StatusActive}
}

type planCase struct {
	name    string
	desired func(*Spec)
	status  func(*Status)
	dir     []nibss.Institution
	want    []Action
	errs    []string
	is      error
}

func run(t *testing.T, cases []planCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			applied := spec()
			st := status(applied)
			if c.status != nil {
				c.status(&st)
			}
			desired := spec()
			if c.desired != nil {
				c.desired(&desired)
			}
			p := Build(realPack(t), desired, st, c.dir, today)
			if p.Setup != desired.Metadata.Name {
				t.Fatalf("setup = %q, want %q", p.Setup, desired.Metadata.Name)
			}
			var got []string
			for _, err := range p.Errors {
				got = append(got, err.Error())
			}
			if !reflect.DeepEqual(got, c.errs) {
				t.Fatalf("errors:\n got %q\nwant %q", got, c.errs)
			}
			if c.is != nil && !errors.Is(p.Errors[0], c.is) {
				t.Fatalf("error %v is not %v", p.Errors[0], c.is)
			}
			if len(c.errs) > 0 && p.Actions != nil {
				t.Fatalf("actions with errors: %+v", p.Actions)
			}
			if !reflect.DeepEqual(p.Actions, c.want) {
				t.Fatalf("actions:\n got %+v\nwant %+v", p.Actions, c.want)
			}
		})
	}
}

func TestBuildNoChanges(t *testing.T) {
	run(t, []planCase{
		{name: "same spec"},
		{
			name: "codes left out keep the allocated ones",
			desired: func(s *Spec) {
				bank(s, "bank-a").NIPCode, bank(s, "bank-a").NUBANCode = "", ""
			},
		},
		{
			name: "codes allocated earlier and never pinned in applied",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").NIPCode, bank(&st.Applied, "bank-b").NUBANCode = "", ""
			},
			desired: func(s *Spec) {
				bank(s, "bank-b").NIPCode, bank(s, "bank-b").NUBANCode = "", ""
			},
		},
		{
			name:    "same nuban code written with leading zeros",
			desired: func(s *Spec) { bank(s, "bank-a").NUBANCode = "000901" },
		},
		{
			name:    "state active written out",
			desired: func(s *Spec) { bank(s, "bank-a").State = StateActive },
		},
		{
			name:    "same start in another offset",
			desired: func(s *Spec) { s.Spec.Start = "2026-11-04T07:00:00Z" },
		},
		{
			name: "holidays reordered and repeated",
			status: func(st *Status) {
				st.Applied.Spec.Holidays.Add = []string{"2026-12-28", "2027-01-02"}
			},
			desired: func(s *Spec) {
				s.Spec.Holidays.Add = []string{"2027-01-02", "2026-12-28", "2027-01-02"}
			},
		},
		{
			name: "omitted defaults match written defaults",
			status: func(st *Status) {
				d := DefaultSpec("beerus", today).Spec
				st.Applied.Spec.NIP, st.Applied.Spec.Connect = d.NIP, d.Connect
			},
			desired: func(s *Spec) {
				s.Spec.NIP, s.Spec.Connect = NIP{}, ConnectLegal{}
			},
		},
		{
			name: "bank already terminating and out of the file",
			status: func(st *Status) {
				res(st, KindBank, "bank-b").Phase = PhaseTerminating
			},
			desired: func(s *Spec) { dropBank(s, "bank-b") },
		},
		{
			name: "partner already terminating and out of the file",
			status: func(st *Status) {
				res(st, KindPartner, "balance").Phase = PhaseTerminating
			},
			desired: func(s *Spec) { s.Spec.Partners = nil },
		},
		{
			name: "bank closing with state closed",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").State = StateClosed
				bank(&st.Applied, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-a"}
				res(st, KindBank, "bank-b").Phase = PhaseClosing
			},
			desired: func(s *Spec) {
				bank(s, "bank-b").State = StateClosed
				bank(s, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-a"}
			},
		},
		{
			name: "provisioning bank with steps still running",
			status: func(st *Status) {
				r := res(st, KindBank, "bank-b")
				r.Phase, r.Steps = PhaseProvisioning, map[string]string{StepAllocate: stepDone}
			},
		},
	})
}

func TestBuildNoChangesRendersNoChanges(t *testing.T) {
	s := spec()
	p := Build(realPack(t), s, status(s), nil, today)
	if got := Render(p); got != "No changes.\n" {
		t.Fatalf("render = %q", got)
	}
}

func TestBuildLockedFields(t *testing.T) {
	run(t, []planCase{
		{
			name:    "apiVersion",
			desired: func(s *Spec) { s.APIVersion = "beerus.dev/v2" },
			errs:    []string{"apiVersion cannot change from beerus.dev/v1 to beerus.dev/v2. Put back beerus.dev/v1."},
		},
		{
			name:    "kind",
			desired: func(s *Spec) { s.Kind = "World" },
			errs:    []string{"kind cannot change from Setup to World. Put back Setup."},
		},
		{
			name: "metadata.name",
			desired: func(s *Spec) {
				s.Metadata.Name = "lagos-dev"
				s.pos = map[string]Pos{"metadata.name": {File: "setup.yaml", Line: 4, Col: 9}}
			},
			errs: []string{"setup.yaml:4:9: metadata.name is lagos-dev, but this dir holds setup beerus. Use the file for beerus, or another dir."},
			is:   ErrSetupMismatch,
		},
		{
			name:    "start",
			desired: func(s *Spec) { s.Spec.Start = "2026-11-05T08:00:00+01:00" },
			errs:    []string{"start cannot change from 2026-11-04T08:00:00+01:00 to 2026-11-05T08:00:00+01:00. Put back 2026-11-04T08:00:00+01:00."},
		},
		{
			name:    "nip.sessions",
			desired: func(s *Spec) { s.Spec.NIP.Sessions = 6 },
			errs:    []string{"nip.sessions cannot change from 4 to 6. Put back 4."},
		},
		{
			name:    "bank type",
			desired: func(s *Spec) { bank(s, "bank-a").Type = "microfinance" },
			errs:    []string{"banks[bank-a].type cannot change from commercial to microfinance. Put back commercial."},
		},
		{
			name: "nuban_code with its line (C5)",
			desired: func(s *Spec) {
				bank(s, "bank-b").NUBANCode = "905"
				s.pos = map[string]Pos{"banks[bank-b].nuban_code": {File: "setup.yaml", Line: 31, Col: 19}}
			},
			errs: []string{"setup.yaml:31:19: banks[bank-b].nuban_code cannot change; it is 902"},
		},
		{
			name:    "nip_code",
			desired: func(s *Spec) { bank(s, "bank-b").NIPCode = "999009" },
			errs:    []string{"banks[bank-b].nip_code cannot change; it is 999002"},
		},
		{
			name: "nip_code against an allocated code",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").NIPCode = ""
			},
			desired: func(s *Spec) { bank(s, "bank-b").NIPCode = "999009" },
			errs:    []string{"banks[bank-b].nip_code cannot change; it is 999002"},
		},
		{
			name:    "settles_through set later",
			desired: func(s *Spec) { bank(s, "bank-b").SettlesThrough = "bank-a" },
			errs:    []string{"banks[bank-b].settles_through cannot change from nothing to bank-a. Put back nothing."},
		},
		{
			name: "two locked fields reported together",
			desired: func(s *Spec) {
				bank(s, "bank-a").Type = "digital"
				s.Spec.NIP.Sessions = 2
			},
			errs: []string{
				"nip.sessions cannot change from 4 to 2. Put back 4.",
				"banks[bank-a].type cannot change from commercial to digital. Put back commercial.",
			},
		},
	})
}

func TestBuildChangeableFields(t *testing.T) {
	run(t, []planCase{
		{
			name:    "name is a rebrand (C5)",
			desired: func(s *Spec) { bank(s, "bank-a").Name = "Zenith Bank" },
			want:    []Action{{Op: OpUpdate, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{{Path: "name", From: "Bank A", To: "Zenith Bank"}}}},
		},
		{
			name:    "active to suspended",
			desired: func(s *Spec) { bank(s, "bank-b").State = StateSuspended },
			want:    []Action{{Op: OpUpdate, Kind: KindBank, ID: "bank-b", Fields: []FieldChange{{Path: "state", From: StateActive, To: StateSuspended}}}},
		},
		{
			name:   "suspended to active",
			status: func(st *Status) { bank(&st.Applied, "bank-b").State = StateSuspended },
			want:   []Action{{Op: OpUpdate, Kind: KindBank, ID: "bank-b", Fields: []FieldChange{{Path: "state", From: StateSuspended, To: StateActive}}}},
		},
		{
			name: "caps",
			status: func(st *Status) {
				bank(&st.Applied, "bank-a").NetDebitCap = 50000000000
			},
			desired: func(s *Spec) {
				bank(s, "bank-a").NetDebitCap = 80000000000
				bank(s, "bank-a").Collateral = 100000000000
			},
			want: []Action{{Op: OpUpdate, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{
				{Path: "net_debit_cap_minor", From: "50000000000", To: "80000000000"},
				{Path: "collateral_minor", From: "0", To: "100000000000"},
			}}},
		},
		{
			name: "open banking",
			desired: func(s *Spec) {
				bank(s, "bank-a").OpenBanking = OpenBanking{ConsentMonths: 6, RefreshDays: 30, DormantDays: 90, AssertionSeconds: 300}
			},
			want: []Action{{Op: OpUpdate, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{
				{Path: "open_banking.consent_months", From: "0", To: "6"},
				{Path: "open_banking.refresh_days", From: "0", To: "30"},
				{Path: "open_banking.dormant_days", From: "0", To: "90"},
				{Path: "open_banking.assertion_seconds", From: "0", To: "300"},
			}}},
		},
		{
			name: "on_remove",
			desired: func(s *Spec) {
				bank(s, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-a", Customers: map[string]string{"c2": MoneyLeave, "c1": MoneyPayout}}
			},
			want: []Action{{Op: OpUpdate, Kind: KindBank, ID: "bank-b", Fields: []FieldChange{
				{Path: "on_remove", To: "transfer to bank-a (customers c1=payout, c2=leave)"},
			}}},
		},
		{
			name: "nip tsq and connect",
			desired: func(s *Spec) {
				s.Spec.NIP.TSQAfter, s.Spec.NIP.TSQAttempts = "5m", 4
				s.Spec.Connect = ConnectLegal{LegalName: "Acme Connect Ltd", BrandName: "Acme", RegistryID: "OB-0002", CACNumber: "RC-000002"}
			},
			want: []Action{{Op: OpUpdate, Kind: KindSetup, ID: "beerus", Fields: []FieldChange{
				{Path: "nip.tsq_after", From: "2m", To: "5m"},
				{Path: "nip.tsq_attempts", From: "3", To: "4"},
				{Path: "connect.legal_name", From: "Beerus Connect Ltd", To: "Acme Connect Ltd"},
				{Path: "connect.brand_name", From: "Beerus", To: "Acme"},
				{Path: "connect.registry_id", From: "OB-0001", To: "OB-0002"},
				{Path: "connect.cac_number", From: "RC-000001", To: "RC-000002"},
			}}},
		},
		{
			name: "partner fields",
			desired: func(s *Spec) {
				s.Spec.Partners[0] = Partner{ID: "balance", Name: "Balance Ltd", LegalName: "Balance Technologies Ltd", Purpose: "account information", RedirectURIs: []string{"https://a/cb", "https://b/cb"}, WebhookURL: "https://localhost:4000/webhooks"}
			},
			want: []Action{{Op: OpUpdate, Kind: KindPartner, ID: "balance", Fields: []FieldChange{
				{Path: "name", From: "Balance", To: "Balance Ltd"},
				{Path: "legal_name", To: "Balance Technologies Ltd"},
				{Path: "purpose", To: "account information"},
				{Path: "redirect_uris", To: "https://a/cb, https://b/cb"},
				{Path: "webhook_url", From: "https://localhost:3000/webhooks", To: "https://localhost:4000/webhooks"},
			}}},
		},
		{
			name: "partner added and removed",
			desired: func(s *Spec) {
				s.Spec.Partners = []Partner{{ID: "ledgerly", Name: "Ledgerly"}}
			},
			want: []Action{
				{Op: OpCreate, Kind: KindPartner, ID: "ledgerly", Fields: []FieldChange{{Path: "name", To: "Ledgerly"}}},
				{Op: OpRemove, Kind: KindPartner, ID: "balance", Destructive: true, Impact: &Impact{}},
			},
		},
	})
}

func TestBuildHolidays(t *testing.T) {
	run(t, []planCase{
		{
			name:    "future add (C6)",
			desired: func(s *Spec) { s.Spec.Holidays.Add = append(s.Spec.Holidays.Add, "2026-11-06") },
			want:    []Action{{Op: OpUpdate, Kind: KindSetup, ID: "beerus", Fields: []FieldChange{{Path: "holidays.add", To: "2026-11-06"}}}},
		},
		{
			name:    "future removal from add",
			desired: func(s *Spec) { s.Spec.Holidays.Add = nil },
			want:    []Action{{Op: OpUpdate, Kind: KindSetup, ID: "beerus", Fields: []FieldChange{{Path: "holidays.add", From: "2026-12-28"}}}},
		},
		{
			name:    "future remove entry",
			desired: func(s *Spec) { s.Spec.Holidays.Remove = []string{"2026-12-25"} },
			want:    []Action{{Op: OpUpdate, Kind: KindSetup, ID: "beerus", Fields: []FieldChange{{Path: "holidays.remove", To: "2026-12-25"}}}},
		},
		{
			name: "past add with its line (C6)",
			desired: func(s *Spec) {
				s.Spec.Holidays.Add = append(s.Spec.Holidays.Add, "2026-11-01")
				s.pos = map[string]Pos{"holidays.add[2026-11-01]": {File: "setup.yaml", Line: 12, Col: 12}}
			},
			errs: []string{"setup.yaml:12:12: holidays.add 2026-11-01 is in the past and cannot change."},
		},
		{
			name:    "today (C6)",
			desired: func(s *Spec) { s.Spec.Holidays.Remove = []string{"2026-11-05"} },
			errs:    []string{"holidays.remove 2026-11-05 is today and cannot change."},
		},
		{
			name:    "past date dropped from the file",
			status:  func(st *Status) { st.Applied.Spec.Holidays.Add = []string{"2026-10-30", "2026-12-28"} },
			errs:    []string{"holidays.add 2026-10-30 is in the past and cannot change."},
			desired: func(s *Spec) {},
		},
		{
			name:   "past dates kept unchanged",
			status: func(st *Status) { st.Applied.Spec.Holidays.Add = []string{"2025-01-02", "2026-12-28"} },
			desired: func(s *Spec) {
				s.Spec.Holidays.Add = []string{"2025-01-02", "2026-12-28"}
			},
		},
		{
			name:    "leap day in the past",
			status:  func(st *Status) { st.Applied.Spec.Holidays.Remove = []string{"2024-02-29"} },
			desired: func(s *Spec) {},
			errs:    []string{"holidays.remove 2024-02-29 is in the past and cannot change."},
		},
		{
			name:    "far future",
			desired: func(s *Spec) { s.Spec.Holidays.Add = append(s.Spec.Holidays.Add, "2999-12-31") },
			want:    []Action{{Op: OpUpdate, Kind: KindSetup, ID: "beerus", Fields: []FieldChange{{Path: "holidays.add", To: "2999-12-31"}}}},
		},
		{
			name:    "not a date",
			desired: func(s *Spec) { s.Spec.Holidays.Add = append(s.Spec.Holidays.Add, "2026-02-30") },
			errs:    []string{`holidays.add "2026-02-30" is not a date. Write it as YYYY-MM-DD.`},
		},
	})
}

func TestBuildStateTransitions(t *testing.T) {
	transfer := &Removal{Money: MoneyTransfer, To: "bank-a"}
	run(t, []planCase{
		{
			name: "active to closed",
			desired: func(s *Spec) {
				bank(s, "bank-b").State = StateClosed
				bank(s, "bank-b").OnRemove = transfer
			},
			want: []Action{{Op: OpClose, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{}, Fields: []FieldChange{
				{Path: "state", From: StateActive, To: StateClosed},
				{Path: "on_remove", To: "transfer to bank-a"},
				{Path: "on_remove.money", To: MoneyTransfer},
				{Path: "on_remove.to", To: "Bank A"},
			}}},
		},
		{
			name: "suspended to closed",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").State = StateSuspended
				bank(&st.Applied, "bank-b").OnRemove = &Removal{Money: MoneyLeave}
			},
			desired: func(s *Spec) {
				bank(s, "bank-b").State = StateClosed
				bank(s, "bank-b").OnRemove = &Removal{Money: MoneyLeave}
			},
			want: []Action{{Op: OpClose, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{}, Fields: []FieldChange{
				{Path: "state", From: StateSuspended, To: StateClosed},
				{Path: "on_remove.money", To: MoneyLeave},
			}}},
		},
		{
			name: "closed is one-way",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").State = StateClosed
				res(st, KindBank, "bank-b").Phase = PhaseClosed
			},
			desired: func(s *Spec) { bank(s, "bank-b").State = StateActive },
			errs:    []string{"banks[bank-b].state cannot change from closed to active. A closed bank stays closed; remove it from the file instead."},
		},
		{
			name: "new bank cannot start closed",
			desired: func(s *Spec) {
				s.Spec.Banks = append(s.Spec.Banks, Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", State: StateClosed})
			},
			errs: []string{"banks[bank-c].state cannot be closed for a new bank. Use active or suspended."},
		},
		{
			name: "new bank can start suspended",
			desired: func(s *Spec) {
				s.Spec.Banks = append(s.Spec.Banks, Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", State: StateSuspended})
			},
			want: []Action{{Op: OpCreate, Kind: KindBank, ID: "bank-c", Allocated: []string{"nip_code", "nuban_code"}, Fields: []FieldChange{
				{Path: "type", To: "commercial"}, {Path: "nip_code", To: "000001"}, {Path: "nuban_code", To: "001"}, {Path: "state", To: StateSuspended},
			}}},
		},
		{
			name: "closed bank removed from the file",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").State = StateClosed
				res(st, KindBank, "bank-b").Phase = PhaseClosed
			},
			desired: func(s *Spec) { dropBank(s, "bank-b") },
			want:    []Action{{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{Databases: []string{"beerus_x_bank_b"}}}},
		},
	})
}

func TestBuildRemoval(t *testing.T) {
	removeB := Action{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{Databases: []string{"beerus_x_bank_b"}}}
	run(t, []planCase{
		{
			name:    "plain removal",
			desired: func(s *Spec) { dropBank(s, "bank-b") },
			want:    []Action{removeB},
		},
		{
			name: "removal shows the money target",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-a"}
			},
			desired: func(s *Spec) {
				dropBank(s, "bank-b")
				bank(s, "bank-a").Name = "Zenith Bank"
			},
			want: []Action{
				{Op: OpUpdate, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{{Path: "name", From: "Bank A", To: "Zenith Bank"}}},
				{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true,
					Fields: []FieldChange{{Path: "on_remove.money", To: MoneyTransfer}, {Path: "on_remove.to", To: "Zenith Bank"}},
					Impact: &Impact{Databases: []string{"beerus_x_bank_b"}}},
			},
		},
		{
			name:    "removing every bank is allowed",
			desired: func(s *Spec) { s.Spec.Banks = nil },
			want: []Action{
				{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{Databases: []string{"beerus_x_bank_b"}}},
				{Op: OpRemove, Kind: KindBank, ID: "bank-a", Destructive: true, Impact: &Impact{Databases: []string{"beerus_x_bank_a"}}},
			},
		},
		{
			name: "settled-through bank blocks (C18)",
			status: func(st *Status) {
				st.Applied.Spec.Banks = append(st.Applied.Spec.Banks, Bank{ID: "mfb", Name: "Mfb", Type: "microfinance", NIPCode: "999003", NUBANCode: "903", SettlesThrough: "bank-b"})
			},
			desired: func(s *Spec) {
				dropBank(s, "bank-b")
				s.Spec.Banks = append(s.Spec.Banks, Bank{ID: "mfb", Name: "Mfb", Type: "microfinance", NIPCode: "999003", NUBANCode: "903", SettlesThrough: "bank-b"})
			},
			want: []Action{
				{Op: OpDrift, Kind: KindBank, ID: "mfb", Fields: []FieldChange{{Path: "status", From: stepDone, To: "missing"}}},
				{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{Databases: []string{"beerus_x_bank_b"}}, Blockers: []string{"mfb settles through bank-b and is not leaving. Remove mfb too, or keep bank-b."}},
			},
		},
		{
			name: "settled-through bank leaving with its settler, settler first",
			status: func(st *Status) {
				st.Applied.Spec.Banks = append([]Bank{{ID: "mfb", Name: "Mfb", Type: "microfinance", NIPCode: "999003", NUBANCode: "903", SettlesThrough: "bank-b"}}, st.Applied.Spec.Banks...)
			},
			desired: func(s *Spec) { dropBank(s, "bank-b") },
			want: []Action{
				{Op: OpRemove, Kind: KindBank, ID: "mfb", Destructive: true, Impact: &Impact{}},
				removeB,
			},
		},
		{
			name: "re-add while terminating (C19)",
			status: func(st *Status) {
				res(st, KindBank, "bank-b").Phase = PhaseTerminating
			},
			desired: func(s *Spec) {
				s.pos = map[string]Pos{"banks[bank-b].id": {File: "setup.yaml", Line: 27, Col: 11}}
			},
			errs: []string{"setup.yaml:27:11: banks[bank-b] is being removed, and its id is never reused. Choose another id."},
			is:   ErrBeingRemoved,
		},
		{
			name:   "partner re-add while terminating",
			status: func(st *Status) { res(st, KindPartner, "balance").Phase = PhaseTerminating },
			errs:   []string{"partners[balance] is being removed and cannot be added back until removal finishes. Take it out of the file, or wait."},
			is:     ErrBeingRemoved,
		},
		{
			name:    "re-add after removal finished hits the tombstone",
			status:  func(st *Status) { st.Applied.Spec.Banks = st.Applied.Spec.Banks[:1]; st.Resources = st.Resources[:1] },
			dir:     []nibss.Institution{tombstone("bank-b", "Bank B", "999002", "902")},
			desired: func(s *Spec) { bank(s, "bank-b").NIPCode, bank(s, "bank-b").NUBANCode = "", "" },
			errs:    []string{"banks[bank-b].id bank-b belonged to a removed bank. Choose another id."},
		},
		{
			name: "partners removed before banks, in reverse",
			status: func(st *Status) {
				st.Applied.Spec.Partners = append(st.Applied.Spec.Partners, Partner{ID: "ledgerly", Name: "Ledgerly"})
			},
			desired: func(s *Spec) {
				dropBank(s, "bank-b")
				s.Spec.Partners = nil
			},
			want: []Action{
				{Op: OpRemove, Kind: KindPartner, ID: "ledgerly", Destructive: true, Impact: &Impact{}},
				{Op: OpRemove, Kind: KindPartner, ID: "balance", Destructive: true, Impact: &Impact{}},
				removeB,
			},
		},
	})
}

func TestBuildOnRemoveTarget(t *testing.T) {
	three := func(s *Spec) {
		s.Spec.Banks = append(s.Spec.Banks, Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", NIPCode: "999003", NUBANCode: "903"})
	}
	run(t, []planCase{
		{
			name: "close to itself",
			desired: func(s *Spec) {
				bank(s, "bank-b").State = StateClosed
				bank(s, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-b"}
			},
			errs: []string{"banks[bank-b].on_remove sends money to bank-b itself. Choose another bank."},
		},
		{
			name: "close to a bank closing too",
			status: func(st *Status) {
				three(&st.Applied)
				st.Resources = append(st.Resources, Resource{Kind: KindBank, ID: "bank-c", Phase: PhaseActive, Steps: doneSteps()})
			},
			desired: func(s *Spec) {
				three(s)
				bank(s, "bank-b").State = StateClosed
				bank(s, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-c"}
				bank(s, "bank-c").State = StateClosed
				bank(s, "bank-c").OnRemove = &Removal{Money: MoneyLeave}
			},
			errs: []string{"banks[bank-b].on_remove sends money to bank-c, which is closing too. Choose a bank that stays open."},
		},
		{
			name: "close to a bank already terminating",
			status: func(st *Status) {
				three(&st.Applied)
				st.Resources = append(st.Resources, Resource{Kind: KindBank, ID: "bank-c", Phase: PhaseTerminating})
			},
			desired: func(s *Spec) {
				bank(s, "bank-b").State = StateClosed
				bank(s, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-c"}
			},
			errs: []string{"banks[bank-b].on_remove sends money to bank-c, which is leaving too. Choose a bank that stays."},
		},
		{
			name: "close to a bank already closed",
			status: func(st *Status) {
				three(&st.Applied)
				bank(&st.Applied, "bank-c").State = StateClosed
				st.Resources = append(st.Resources, Resource{Kind: KindBank, ID: "bank-c", Phase: PhaseClosed})
			},
			desired: func(s *Spec) {
				three(s)
				bank(s, "bank-c").State = StateClosed
				bank(s, "bank-b").State = StateClosed
				bank(s, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-c"}
			},
			errs: []string{"banks[bank-b].on_remove sends money to bank-c, which is closing or closed. Choose a bank that stays open."},
		},
		{
			name: "close and rename together",
			desired: func(s *Spec) {
				bank(s, "bank-b").Name = "Sterling Bank"
				bank(s, "bank-b").State = StateClosed
				bank(s, "bank-b").OnRemove = &Removal{Money: MoneyLeave}
			},
			want: []Action{{Op: OpClose, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{}, Fields: []FieldChange{
				{Path: "name", From: "Bank B", To: "Sterling Bank"},
				{Path: "state", From: StateActive, To: StateClosed},
				{Path: "on_remove", To: MoneyLeave},
				{Path: "on_remove.money", To: MoneyLeave},
			}}},
		},
		{
			name: "removal to a bank removed in the same plan",
			status: func(st *Status) {
				three(&st.Applied)
				bank(&st.Applied, "bank-b").OnRemove = &Removal{Money: MoneyTransfer, To: "bank-c"}
				st.Resources = append(st.Resources, Resource{Kind: KindBank, ID: "bank-c", Phase: PhaseActive, Steps: doneSteps()})
			},
			desired: func(s *Spec) { dropBank(s, "bank-b") },
			errs:    []string{"on_remove of removed bank bank-b sends money to bank-c, which is leaving too. Choose a bank that stays."},
		},
		{
			name: "removal to a bank that was never in the file",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").OnRemove = &Removal{Money: MoneyPayout, Customers: map[string]string{"c1": MoneyTransfer}, To: "bank-z"}
			},
			desired: func(s *Spec) { dropBank(s, "bank-b") },
			errs:    []string{"on_remove of removed bank bank-b sends money to bank-z, which is not in the file. Choose a bank in the file."},
		},
		{
			name: "payout needs no target",
			status: func(st *Status) {
				bank(&st.Applied, "bank-b").OnRemove = &Removal{Money: MoneyPayout, To: "bank-b"}
			},
			desired: func(s *Spec) { dropBank(s, "bank-b") },
			want: []Action{{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true,
				Fields: []FieldChange{{Path: "on_remove.money", To: MoneyPayout}, {Path: "on_remove.to", To: "bank-b"}},
				Impact: &Impact{Databases: []string{"beerus_x_bank_b"}}}},
		},
	})
}

func smallPack() *countrypack.Pack {
	return &countrypack.Pack{
		DepositInsurance: countrypack.DepositInsurance{BankTypes: map[string]string{"commercial": "deposit_money_bank", "digital": "deposit_money_bank", "microfinance": "microfinance_bank"}},
		BankCodes: countrypack.BankCodes{Categories: map[string][]countrypack.CodeTier{
			"deposit_money_bank": {
				{First: 1, Last: 2, NUBANDigits: 3, NIPPrefix: "000", NIPDigits: 3},
				{First: 1000, Last: 1001, NUBANDigits: 6, NIPDigits: 6},
			},
			"microfinance_bank": {{First: 1, Last: 9, NUBANPrefix: "5", NUBANDigits: 4, NIPPrefix: "090", NIPDigits: 3}},
		}},
	}
}

func TestBuildAllocation(t *testing.T) {
	newBank := func(id, typ string) Bank { return Bank{ID: id, Name: "Bank " + id, Type: typ} }
	create := func(id, typ, nip, nuban string) Action {
		return Action{Op: OpCreate, Kind: KindBank, ID: id, Allocated: []string{"nip_code", "nuban_code"}, Fields: []FieldChange{
			{Path: "type", To: typ}, {Path: "nip_code", To: nip}, {Path: "nuban_code", To: nuban},
		}}
	}
	cases := []struct {
		name    string
		small   bool
		dir     []nibss.Institution
		desired func(*Spec)
		want    []Action
		errs    []string
	}{
		{
			name:    "first deposit money bank",
			desired: func(s *Spec) { s.Spec.Banks = append(s.Spec.Banks, newBank("bank-c", "commercial")) },
			want:    []Action{create("bank-c", "commercial", "000001", "001")},
		},
		{
			name:    "first microfinance bank",
			desired: func(s *Spec) { s.Spec.Banks = append(s.Spec.Banks, newBank("mfb", "microfinance")) },
			want:    []Action{create("mfb", "microfinance", "090001", "50001")},
		},
		{
			name: "file order, and every kind of holder is skipped",
			dir: []nibss.Institution{
				tombstone("old", "Old Bank", "000001", "001"),
				member("bank-z", "Bank Z", "000002", "000002"),
				member("mfb-z", "Mfb Z", "090001", "50001"),
			},
			desired: func(s *Spec) {
				dropBank(s, "bank-b")
				s.Spec.Banks = append(s.Spec.Banks, newBank("bank-c", "digital"), newBank("bank-d", "commercial"), newBank("mfb", "microfinance"))
			},
			want: []Action{
				create("bank-c", "digital", "000003", "003"),
				create("bank-d", "commercial", "000004", "004"),
				create("mfb", "microfinance", "090002", "50002"),
				{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{Databases: []string{"beerus_x_bank_b"}}},
			},
		},
		{
			name:  "the first tier runs out and the second pads to six digits",
			small: true,
			dir:   []nibss.Institution{tombstone("old-1", "Old 1", "000001", "001"), tombstone("old-2", "Old 2", "000002", "002")},
			desired: func(s *Spec) {
				s.Spec.Banks = append(s.Spec.Banks, newBank("bank-c", "commercial"), newBank("bank-d", "commercial"))
			},
			want: []Action{
				create("bank-c", "commercial", "001000", "001000"),
				create("bank-d", "commercial", "001001", "001001"),
			},
		},
		{
			name:  "every tier used",
			small: true,
			dir: []nibss.Institution{
				tombstone("old-1", "Old 1", "000001", "001"), tombstone("old-2", "Old 2", "000002", "002"),
				tombstone("old-3", "Old 3", "001000", "001000"), tombstone("old-4", "Old 4", "001001", "001001"),
			},
			desired: func(s *Spec) { s.Spec.Banks = append(s.Spec.Banks, newBank("bank-c", "commercial")) },
			errs:    []string{"banks[bank-c]: no bank codes left for deposit money banks (all 4 are used). Run beerus reset to start again."},
		},
		{
			name: "codes cannot be set on a new bank",
			desired: func(s *Spec) {
				c := newBank("bank-c", "commercial")
				c.NIPCode, c.NUBANCode = "123456", "123"
				s.Spec.Banks = append(s.Spec.Banks, c)
			},
			errs: []string{"banks[bank-c].nip_code is assigned by Beerus. Remove it.", "banks[bank-c].nuban_code is assigned by Beerus. Remove it."},
		},
		{
			name:    "an existing bank may repeat its codes",
			desired: func(s *Spec) { bank(s, "bank-b").NUBANCode = "000902" },
		},
		{
			name: "an existing bank cannot change its codes",
			desired: func(s *Spec) {
				bank(s, "bank-b").NIPCode, bank(s, "bank-b").NUBANCode = "999009", "905"
			},
			errs: []string{"banks[bank-b].nip_code cannot change; it is 999002", "banks[bank-b].nuban_code cannot change; it is 902"},
		},
	}
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			desired := spec()
			c.desired(&desired)
			use := pack
			if c.small {
				use = smallPack()
			}
			p := Build(use, desired, status(spec()), c.dir, today)
			got := []string{}
			for _, err := range p.Errors {
				got = append(got, err.Error())
			}
			if len(c.errs) == 0 {
				c.errs = []string{}
			}
			if !reflect.DeepEqual(got, c.errs) {
				t.Fatalf("errors = %q, want %q", got, c.errs)
			}
			if len(c.errs) == 0 && !reflect.DeepEqual(p.Actions, c.want) {
				t.Fatalf("actions = %+v, want %+v", p.Actions, c.want)
			}
		})
	}
}

func TestBuildRejectsAStartAfterToday(t *testing.T) {
	desired := spec()
	clearCodes(&desired)
	desired.Spec.Start = "2026-11-06T00:00:00+01:00"
	dir := []nibss.Institution{member("bank-a", "Bank A", "999001", "901"), member("bank-b", "Bank B", "999002", "902")}
	p := Build(realPack(t), desired, Status{}, dir, today)
	want := "start 2026-11-06T00:00:00+01:00 is after today, 2026-11-05. Beerus runs on real time; use today or an earlier date."
	if len(p.Errors) != 1 || p.Errors[0].Error() != want || !errors.Is(p.Errors[0], ErrFutureStart) {
		t.Fatalf("errors = %v, want %q", p.Errors, want)
	}
	desired.Spec.Start = "2026-11-05T23:59:00+01:00"
	if p := Build(realPack(t), desired, Status{}, dir, today); len(p.Errors) > 0 {
		t.Fatalf("a start later today: %v", p.Errors)
	}
}

func TestBuildFirstApply(t *testing.T) {
	desired := spec()
	clearCodes(&desired)
	dir := []nibss.Institution{member("bank-a", "Bank A", "999001", "901"), member("bank-z", "Bank Z", "999002", "902")}
	p := Build(realPack(t), desired, Status{}, dir, today)
	want := []Action{
		{Op: OpCreate, Kind: KindBank, ID: "bank-a", Allocated: []string{"nip_code", "nuban_code"}, Fields: []FieldChange{{Path: "type", To: "commercial"}, {Path: "nip_code", To: "000001"}, {Path: "nuban_code", To: "001"}}},
		{Op: OpCreate, Kind: KindBank, ID: "bank-b", Allocated: []string{"nip_code", "nuban_code"}, Fields: []FieldChange{{Path: "type", To: "digital"}, {Path: "nip_code", To: "000002"}, {Path: "nuban_code", To: "002"}}},
		{Op: OpCreate, Kind: KindPartner, ID: "balance", Fields: []FieldChange{{Path: "name", To: "Balance"}}},
	}
	if len(p.Errors) > 0 || !reflect.DeepEqual(p.Actions, want) {
		t.Fatalf("plan = %+v, errors %v", p.Actions, p.Errors)
	}
}

func TestBuildSettlesThroughOrder(t *testing.T) {
	run(t, []planCase{
		{
			name: "settler created after its bank",
			desired: func(s *Spec) {
				s.Spec.Banks = append(s.Spec.Banks,
					Bank{ID: "mfb", Name: "Mfb", Type: "microfinance", SettlesThrough: "bank-c"},
					Bank{ID: "bank-c", Name: "Bank C", Type: "commercial"},
				)
			},
			want: []Action{
				{Op: OpCreate, Kind: KindBank, ID: "bank-c", Allocated: []string{"nip_code", "nuban_code"}, Fields: []FieldChange{{Path: "type", To: "commercial"}, {Path: "nip_code", To: "000001"}, {Path: "nuban_code", To: "001"}}},
				{Op: OpCreate, Kind: KindBank, ID: "mfb", Allocated: []string{"nip_code", "nuban_code"}, Fields: []FieldChange{{Path: "type", To: "microfinance"}, {Path: "nip_code", To: "090001"}, {Path: "nuban_code", To: "50001"}, {Path: "settles_through", To: "bank-c"}}},
			},
		},
		{
			name: "loop",
			desired: func(s *Spec) {
				s.Spec.Banks = append(s.Spec.Banks,
					Bank{ID: "x", Name: "X", Type: "commercial", SettlesThrough: "y"},
					Bank{ID: "y", Name: "Y", Type: "commercial", SettlesThrough: "x"},
				)
			},
			errs: []string{"banks[x].settles_through forms a loop: x → y → x. Point one of them at a bank that settles directly."},
		},
	})
}

func TestBuildDrift(t *testing.T) {
	run(t, []planCase{
		{
			name:   "database missing",
			status: func(st *Status) { res(st, KindBank, "bank-a").Steps[StepDatabase] = "missing" },
			want:   []Action{{Op: OpDrift, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{{Path: "steps.database", From: stepDone, To: "missing"}}}},
		},
		{
			name: "repairing with two parts",
			status: func(st *Status) {
				r := res(st, KindBank, "bank-b")
				r.Phase = PhaseRepairing
				r.Steps[StepCerts], r.Steps[StepSite] = "missing", "failed"
			},
			want: []Action{{Op: OpDrift, Kind: KindBank, ID: "bank-b", Fields: []FieldChange{
				{Path: "steps.certs", From: stepDone, To: "missing"},
				{Path: "steps.site", From: stepDone, To: "failed"},
			}}},
		},
		{
			name:   "repairing with no step recorded",
			status: func(st *Status) { res(st, KindPartner, "balance").Phase = PhaseRepairing },
			want:   []Action{{Op: OpDrift, Kind: KindPartner, ID: "balance"}},
		},
		{
			name:   "status record missing",
			status: func(st *Status) { st.Resources = st.Resources[1:] },
			want:   []Action{{Op: OpDrift, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{{Path: "status", From: stepDone, To: "missing"}}}},
		},
		{
			name: "system database missing",
			status: func(st *Status) {
				res(st, KindSystem, SystemNIBSS).Steps[StepDatabase] = "missing"
			},
			want: []Action{{Op: OpDrift, Kind: KindSystem, ID: SystemNIBSS, Fields: []FieldChange{{Path: "steps.database", From: stepDone, To: "missing"}}}},
		},
		{
			name: "terminating bank is not drift",
			status: func(st *Status) {
				r := res(st, KindBank, "bank-b")
				r.Phase, r.Steps[StepDatabase] = PhaseTerminating, "missing"
			},
			desired: func(s *Spec) { dropBank(s, "bank-b") },
		},
		{
			name: "an update wins over drift",
			status: func(st *Status) {
				res(st, KindBank, "bank-a").Steps[StepDatabase] = "missing"
			},
			desired: func(s *Spec) { bank(s, "bank-a").Name = "Zenith Bank" },
			want:    []Action{{Op: OpUpdate, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{{Path: "name", From: "Bank A", To: "Zenith Bank"}}}},
		},
	})
}

func TestBuildIsPure(t *testing.T) {
	desired := spec()
	desired.Spec.Banks = append(desired.Spec.Banks, Bank{ID: "bank-c", Name: "Bank C", Type: "commercial"})
	st := status(spec())
	before := status(spec())
	a := Build(realPack(t), desired, st, nil, today)
	b := Build(realPack(t), desired, st, nil, today)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(st, before) {
		t.Fatal("Build is not deterministic or changed its input")
	}
}

func clearCodes(s *Spec) {
	for i := range s.Spec.Banks {
		s.Spec.Banks[i].NIPCode, s.Spec.Banks[i].NUBANCode = "", ""
	}
}

func realPack(t *testing.T) *countrypack.Pack {
	t.Helper()
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	return pack
}
