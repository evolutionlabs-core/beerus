package setup

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

var today = clock.Date{Y: 2026, M: 11, D: 5}

func spec() Spec {
	return Spec{
		APIVersion: APIVersion,
		Kind:       KindSetup,
		Metadata:   Metadata{Name: "beerus"},
		Spec: Body{
			NIP:      NIP{Sessions: 4, TSQAfter: "2m"},
			Connect:  ConnectLegal{LegalName: "Beerus Connect Ltd", BrandName: "Beerus", RegistryID: "OB-0001", CACNumber: "RC-000001"},
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
	return map[string]string{config.StepAllocate: stepDone, config.StepDirectory: stepDone, config.StepDatabase: stepDone, config.StepFiles: stepDone, config.StepCBN: stepDone, config.StepSite: stepDone, config.StepAPI: stepDone}
}

func status(applied Spec) Status {
	st := Status{Version: StatusVersion, Setup: applied.Metadata.Name, Applied: applied}
	for _, b := range applied.Spec.Banks {
		db := "beerus_x_" + strings.ReplaceAll(b.ID, "-", "_")
		st.Resources = append(st.Resources, Resource{
			Kind:      KindBank,
			ID:        b.ID,
			Phase:     config.PhaseActive,
			Allocated: Allocation{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Database: db},
			Owned:     []Owned{{Kind: ownedDatabase, Name: db}, {Kind: "port", Name: "7451"}},
			Steps:     doneSteps(),
		})
	}
	for _, x := range applied.Spec.Partners {
		st.Resources = append(st.Resources, Resource{Kind: KindPartner, ID: x.ID, Phase: config.PhaseActive})
	}
	st.Resources = append(st.Resources, Resource{Kind: KindSystem, ID: SystemNIBSS, Phase: config.PhaseActive, Steps: map[string]string{config.StepDatabase: stepDone}})
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

type (
	edit   = func(*Spec)
	statEd = func(*Status)
)

func eb(id string, f func(*Bank)) edit { return func(s *Spec) { f(bank(s, id)) } }
func ab(id string, f func(*Bank)) statEd {
	return func(st *Status) { f(bank(&st.Applied, id)) }
}
func rs(kind, id string, f func(*Resource)) statEd { return func(st *Status) { f(res(st, kind, id)) } }
func phase(kind, id, p string) statEd              { return rs(kind, id, func(r *Resource) { r.Phase = p }) }
func step(kind, id, v string) statEd {
	return rs(kind, id, func(r *Resource) { r.Steps[config.StepDatabase] = v })
}
func drop(id string) edit {
	return func(s *Spec) { s.Spec.Banks = slices.DeleteFunc(s.Spec.Banks, func(b Bank) bool { return b.ID == id }) }
}
func add(b Bank) edit { return func(s *Spec) { s.Spec.Banks = append(s.Spec.Banks, b) } }
func addApplied(b Bank) statEd {
	return func(st *Status) { st.Applied.Spec.Banks = append(st.Applied.Spec.Banks, b) }
}
func both(fs ...edit) edit {
	return func(s *Spec) {
		for _, f := range fs {
			f(s)
		}
	}
}
func bothSt(fs ...statEd) statEd {
	return func(st *Status) {
		for _, f := range fs {
			f(st)
		}
	}
}
func pinned(path string, line, col int) edit {
	return func(s *Spec) { s.pos = map[string]Pos{path: {File: "setup.yaml", Line: line, Col: col}} }
}
func holidays(add, remove []string) edit {
	return func(s *Spec) { s.Spec.Holidays = Holidays{Add: add, Remove: remove} }
}
func appliedHolidays(add, remove []string) statEd {
	return func(st *Status) { st.Applied.Spec.Holidays = Holidays{Add: add, Remove: remove} }
}

func institution(id, name, code, nuban, status string) nip.Institution {
	return nip.Institution{ID: id, Name: name, NIPCode: code, NUBANCode: nuban, Type: "commercial", Status: status}
}

func fc(path, from, to string) FieldChange { return FieldChange{Path: path, From: from, To: to} }

func upd(kind, id string, f ...FieldChange) Action {
	return Action{Op: OpUpdate, Kind: kind, ID: id, Fields: f}
}

func gone(kind, id string, dbs ...string) Action {
	return Action{Op: OpRemove, Kind: kind, ID: id, Destructive: true, Impact: &Impact{Databases: dbs}}
}

func drift(kind, id string, f ...FieldChange) Action {
	return Action{Op: OpDrift, Kind: kind, ID: id, Fields: f}
}

func create(id, typ, nipCode, nuban string, extra ...FieldChange) Action {
	f := []FieldChange{fc("type", "", typ), fc("nip_code", "", nipCode), fc("nuban_code", "", nuban)}
	return Action{Op: OpCreate, Kind: KindBank, ID: id, Allocated: []string{"nip_code", "nuban_code"}, Fields: append(f, extra...)}
}

func closing(id string, f ...FieldChange) Action {
	return Action{Op: OpClose, Kind: KindBank, ID: id, Destructive: true, Impact: &Impact{}, Fields: f}
}

func realPack(t *testing.T) *countrypack.Pack {
	t.Helper()
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func smallPack() *countrypack.Pack {
	return &countrypack.Pack{
		DepositInsurance: countrypack.DepositInsurance{BankTypes: map[string]string{"commercial": "deposit_money_bank", "digital": "deposit_money_bank", "microfinance": "microfinance_bank"}},
		BankCodes: countrypack.BankCodes{Categories: map[string][]countrypack.CodeTier{
			"deposit_money_bank": {{First: 1, Last: 2, NUBANDigits: 3, NIPPrefix: "000", NIPDigits: 3}, {First: 1000, Last: 1001, NUBANDigits: 6, NIPDigits: 6}},
			"microfinance_bank":  {{First: 1, Last: 9, NUBANPrefix: "5", NUBANDigits: 4, NIPPrefix: "090", NIPDigits: 3}},
		}},
	}
}

type planCase struct {
	name    string
	desired edit
	status  statEd
	dir     []nip.Institution
	want    []Action
	errs    []string
	is      error
	small   bool
	fresh   bool
}

func buildCases(t *testing.T, cases []planCase) {
	t.Helper()
	for _, c := range cases {
		st := status(spec())
		if c.status != nil {
			c.status(&st)
		}
		if c.fresh {
			st = Status{}
		}
		desired := spec()
		if c.desired != nil {
			c.desired(&desired)
		}
		pack := realPack(t)
		if c.small {
			pack = smallPack()
		}
		p := Build(pack, desired, st, c.dir, today)
		got := []string{}
		for _, err := range p.Errors {
			got = append(got, err.Error())
		}
		if c.errs == nil {
			c.errs = []string{}
		}
		if p.Setup != desired.Metadata.Name || !reflect.DeepEqual(got, c.errs) {
			t.Errorf("%s: setup %q errors %q, want %q", c.name, p.Setup, got, c.errs)
		}
		if c.is != nil && !errors.Is(p.Errors[0], c.is) {
			t.Errorf("%s: error %v is not %v", c.name, p.Errors[0], c.is)
		}
		if len(c.errs) > 0 && p.Actions != nil || len(c.errs) == 0 && !reflect.DeepEqual(p.Actions, c.want) {
			t.Errorf("%s: actions\n got %+v\nwant %+v", c.name, p.Actions, c.want)
		}
	}
}

func TestBuildNoChanges(t *testing.T) {
	transferToA := &Removal{Money: MoneyTransfer, To: "bank-a"}
	noCodes := func(b *Bank) { b.NIPCode, b.NUBANCode = "", "" }
	buildCases(t, []planCase{
		{name: "same spec"},
		{name: "codes left out keep the allocated ones", desired: eb("bank-a", noCodes)},
		{name: "codes allocated earlier and never pinned in applied", status: ab("bank-b", noCodes), desired: eb("bank-b", noCodes)},
		{name: "same nuban code written with leading zeros", desired: eb("bank-a", func(b *Bank) { b.NUBANCode = "000901" })},
		{name: "state active written out", desired: eb("bank-a", func(b *Bank) { b.State = StateActive })},
		{name: "holidays reordered and repeated", status: appliedHolidays([]string{"2026-12-28", "2027-01-02"}, nil), desired: holidays([]string{"2027-01-02", "2026-12-28", "2027-01-02"}, nil)},
		{name: "omitted defaults match written defaults", status: func(st *Status) {
			d := DefaultSpec("beerus").Spec
			st.Applied.Spec.NIP, st.Applied.Spec.Connect = d.NIP, d.Connect
		}, desired: func(s *Spec) { s.Spec.NIP, s.Spec.Connect = NIP{}, ConnectLegal{} }},
		{name: "bank already terminating and out of the file", status: phase(KindBank, "bank-b", config.PhaseTerminating), desired: drop("bank-b")},
		{name: "partner already terminating and out of the file", status: phase(KindPartner, "balance", config.PhaseTerminating), desired: func(s *Spec) { s.Spec.Partners = nil }},
		{name: "bank closing with state closed", status: bothSt(ab("bank-b", func(b *Bank) { b.State, b.OnRemove = StateClosed, transferToA }), phase(KindBank, "bank-b", config.PhaseClosing)), desired: eb("bank-b", func(b *Bank) { b.State, b.OnRemove = StateClosed, transferToA })},
		{name: "provisioning bank with steps still running", status: rs(KindBank, "bank-b", func(r *Resource) {
			r.Phase, r.Steps = config.PhaseProvisioning, map[string]string{config.StepAllocate: stepDone}
		})},
	})
}

func TestBuildLockedFields(t *testing.T) {
	buildCases(t, []planCase{
		{name: "apiVersion", desired: func(s *Spec) { s.APIVersion = "beerus.dev/v2" }, errs: []string{"apiVersion cannot change from beerus.dev/v1 to beerus.dev/v2. Put back beerus.dev/v1."}},
		{name: "kind", desired: func(s *Spec) { s.Kind = "World" }, errs: []string{"kind cannot change from Setup to World. Put back Setup."}},
		{name: "metadata.name", desired: both(func(s *Spec) { s.Metadata.Name = "lagos-dev" }, pinned("metadata.name", 4, 9)), errs: []string{"setup.yaml:4:9: metadata.name is lagos-dev, but this dir holds setup beerus. Use the file for beerus, or another dir."}, is: ErrSetupMismatch},
		{name: "nip.sessions", desired: func(s *Spec) { s.Spec.NIP.Sessions = 6 }, errs: []string{"nip.sessions cannot change from 4 to 6. Put back 4."}},
		{name: "bank type", desired: eb("bank-a", func(b *Bank) { b.Type = "microfinance" }), errs: []string{"banks[bank-a].type cannot change from commercial to microfinance. Put back commercial."}},
		{name: "nuban_code with its line", desired: both(eb("bank-b", func(b *Bank) { b.NUBANCode = "905" }), pinned("banks[bank-b].nuban_code", 31, 19)), errs: []string{"setup.yaml:31:19: banks[bank-b].nuban_code cannot change; it is 902"}},
		{name: "nip_code", desired: eb("bank-b", func(b *Bank) { b.NIPCode = "999009" }), errs: []string{"banks[bank-b].nip_code cannot change; it is 999002"}},
		{name: "nip_code against an allocated code", status: ab("bank-b", func(b *Bank) { b.NIPCode = "" }), desired: eb("bank-b", func(b *Bank) { b.NIPCode = "999009" }), errs: []string{"banks[bank-b].nip_code cannot change; it is 999002"}},
		{name: "settles_through set later", desired: eb("bank-b", func(b *Bank) { b.SettlesThrough = "bank-a" }), errs: []string{"banks[bank-b].settles_through cannot change from nothing to bank-a. Put back nothing."}},
		{name: "two locked fields reported together", desired: both(eb("bank-a", func(b *Bank) { b.Type = "digital" }), func(s *Spec) { s.Spec.NIP.Sessions = 2 }), errs: []string{"nip.sessions cannot change from 4 to 2. Put back 4.", "banks[bank-a].type cannot change from commercial to digital. Put back commercial."}},
	})
}

func TestBuildChangeableFields(t *testing.T) {
	ob := OpenBanking{ConsentMonths: 6, RefreshDays: 30, DormantDays: 90, AssertionSeconds: 300, AccessSeconds: 900}
	partner := Partner{ID: "balance", Name: "Balance Ltd", LegalName: "Balance Technologies Ltd", Purpose: "account information", RedirectURIs: []string{"https://a/cb", "https://b/cb"}, WebhookURL: "https://localhost:4000/webhooks"}
	legal := ConnectLegal{LegalName: "Acme Connect Ltd", BrandName: "Acme", RegistryID: "OB-0002", CACNumber: "RC-000002"}
	buildCases(t, []planCase{
		{name: "name is a rebrand", desired: eb("bank-a", func(b *Bank) { b.Name = "Zenith Bank" }), want: []Action{upd(KindBank, "bank-a", fc("name", "Bank A", "Zenith Bank"))}},
		{name: "active to suspended", desired: eb("bank-b", func(b *Bank) { b.State = StateSuspended }), want: []Action{upd(KindBank, "bank-b", fc("state", StateActive, StateSuspended))}},
		{name: "suspended to active", status: ab("bank-b", func(b *Bank) { b.State = StateSuspended }), want: []Action{upd(KindBank, "bank-b", fc("state", StateSuspended, StateActive))}},
		{name: "caps", status: ab("bank-a", func(b *Bank) { b.NetDebitCap = 50000000000 }), desired: eb("bank-a", func(b *Bank) { b.NetDebitCap, b.Collateral = 80000000000, 100000000000 }), want: []Action{upd(KindBank, "bank-a", fc("net_debit_cap_minor", "50000000000", "80000000000"), fc("collateral_minor", "0", "100000000000"))}},
		{name: "open banking", desired: eb("bank-a", func(b *Bank) { b.OpenBanking = ob }), want: []Action{upd(KindBank, "bank-a", fc("open_banking.consent_months", "0", "6"), fc("open_banking.refresh_days", "0", "30"), fc("open_banking.dormant_days", "0", "90"), fc("open_banking.assertion_seconds", "0", "300"), fc("open_banking.access_seconds", "0", "900"))}},
		{name: "on_remove", desired: eb("bank-b", func(b *Bank) {
			b.OnRemove = &Removal{Money: MoneyTransfer, To: "bank-a", Customers: map[string]string{"c2": MoneyLeave, "c1": MoneyPayout}}
		}), want: []Action{upd(KindBank, "bank-b", fc("on_remove", "", "transfer to bank-a (customers c1=payout, c2=leave)"))}},
		{name: "nip tsq and connect", desired: func(s *Spec) { s.Spec.NIP.TSQAfter, s.Spec.Connect = "5m", legal }, want: []Action{upd(KindSetup, "beerus", fc("nip.tsq_after", "2m", "5m"), fc("connect.legal_name", "Beerus Connect Ltd", "Acme Connect Ltd"), fc("connect.brand_name", "Beerus", "Acme"), fc("connect.registry_id", "OB-0001", "OB-0002"), fc("connect.cac_number", "RC-000001", "RC-000002"))}},
		{name: "partner fields", desired: func(s *Spec) { s.Spec.Partners[0] = partner }, want: []Action{upd(KindPartner, "balance", fc("name", "Balance", "Balance Ltd"), fc("legal_name", "", "Balance Technologies Ltd"), fc("purpose", "", "account information"), fc("redirect_uris", "", "https://a/cb, https://b/cb"), fc("webhook_url", "https://localhost:3000/webhooks", "https://localhost:4000/webhooks"))}},
		{name: "partner added and removed", desired: func(s *Spec) { s.Spec.Partners = []Partner{{ID: "ledgerly", Name: "Ledgerly"}} }, want: []Action{{Op: OpCreate, Kind: KindPartner, ID: "ledgerly", Fields: []FieldChange{fc("name", "", "Ledgerly")}}, gone(KindPartner, "balance")}},
	})
}

func TestBuildHolidays(t *testing.T) {
	dates := func(d ...string) []string { return d }
	holiday := func(path, from, to string) []Action { return []Action{upd(KindSetup, "beerus", fc(path, from, to))} }
	buildCases(t, []planCase{
		{name: "future add", desired: holidays(dates("2026-12-28", "2026-11-06"), nil), want: holiday("holidays.add", "", "2026-11-06")},
		{name: "future removal from add", desired: holidays(nil, nil), want: holiday("holidays.add", "2026-12-28", "")},
		{name: "future remove entry", desired: holidays(dates("2026-12-28"), dates("2026-12-25")), want: holiday("holidays.remove", "", "2026-12-25")},
		{name: "far future", desired: holidays(dates("2026-12-28", "2999-12-31"), nil), want: holiday("holidays.add", "", "2999-12-31")},
		{name: "past add with its line", desired: both(holidays(dates("2026-12-28", "2026-11-01"), nil), pinned("holidays.add[2026-11-01]", 12, 12)), errs: []string{"setup.yaml:12:12: holidays.add 2026-11-01 is in the past and cannot change."}},
		{name: "today", desired: holidays(dates("2026-12-28"), dates("2026-11-05")), errs: []string{"holidays.remove 2026-11-05 is today and cannot change."}},
		{name: "past date dropped from the file", status: appliedHolidays(dates("2026-10-30", "2026-12-28"), nil), errs: []string{"holidays.add 2026-10-30 is in the past and cannot change."}},
		{name: "past dates kept unchanged", status: appliedHolidays(dates("2025-01-02", "2026-12-28"), nil), desired: holidays(dates("2025-01-02", "2026-12-28"), nil)},
		{name: "leap day in the past", status: appliedHolidays(dates("2026-12-28"), dates("2024-02-29")), errs: []string{"holidays.remove 2024-02-29 is in the past and cannot change."}},
		{name: "not a date", desired: holidays(dates("2026-12-28", "2026-02-30"), nil), errs: []string{`holidays.add "2026-02-30" is not a date. Write it as YYYY-MM-DD.`}},
	})
}

func TestBuildStateTransitions(t *testing.T) {
	toA := &Removal{Money: MoneyTransfer, To: "bank-a"}
	closedB := bothSt(ab("bank-b", func(b *Bank) { b.State = StateClosed }), phase(KindBank, "bank-b", config.PhaseClosed))
	buildCases(t, []planCase{
		{name: "active to closed", desired: eb("bank-b", func(b *Bank) { b.State, b.OnRemove = StateClosed, toA }), want: []Action{closing("bank-b", fc("state", StateActive, StateClosed), fc("on_remove", "", "transfer to bank-a"), fc("on_remove.money", "", MoneyTransfer), fc("on_remove.to", "", "Bank A"))}},
		{name: "suspended to closed", status: ab("bank-b", func(b *Bank) { b.State, b.OnRemove = StateSuspended, &Removal{Money: MoneyLeave} }), desired: eb("bank-b", func(b *Bank) { b.State, b.OnRemove = StateClosed, &Removal{Money: MoneyLeave} }), want: []Action{closing("bank-b", fc("state", StateSuspended, StateClosed), fc("on_remove.money", "", MoneyLeave))}},
		{name: "closed is one-way", status: closedB, desired: eb("bank-b", func(b *Bank) { b.State = StateActive }), errs: []string{"banks[bank-b].state cannot change from closed to active. A closed bank stays closed; remove it from the file instead."}},
		{name: "new bank cannot start closed", desired: add(Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", State: StateClosed}), errs: []string{"banks[bank-c].state cannot be closed for a new bank. Use active or suspended."}},
		{name: "new bank can start suspended", desired: add(Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", State: StateSuspended}), want: []Action{create("bank-c", "commercial", "000001", "001", fc("state", "", StateSuspended))}},
		{name: "closed bank removed from the file", status: closedB, desired: drop("bank-b"), want: []Action{gone(KindBank, "bank-b", "beerus_x_bank_b")}},
	})
}

func TestBuildRemoval(t *testing.T) {
	mfb := Bank{ID: "mfb", Name: "Mfb", Type: "microfinance", NIPCode: "999003", NUBANCode: "903", SettlesThrough: "bank-b"}
	removeB := gone(KindBank, "bank-b", "beerus_x_bank_b")
	buildCases(t, []planCase{
		{name: "plain removal", desired: drop("bank-b"), want: []Action{removeB}},
		{name: "removal shows the money target", status: ab("bank-b", func(b *Bank) { b.OnRemove = &Removal{Money: MoneyTransfer, To: "bank-a"} }), desired: both(drop("bank-b"), eb("bank-a", func(b *Bank) { b.Name = "Zenith Bank" })), want: []Action{upd(KindBank, "bank-a", fc("name", "Bank A", "Zenith Bank")), {Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Fields: []FieldChange{fc("on_remove.money", "", MoneyTransfer), fc("on_remove.to", "", "Zenith Bank")}, Impact: removeB.Impact}}},
		{name: "removing every bank is allowed", desired: func(s *Spec) { s.Spec.Banks = nil }, want: []Action{removeB, gone(KindBank, "bank-a", "beerus_x_bank_a")}},
		{name: "settled-through bank blocks", status: addApplied(mfb), desired: both(drop("bank-b"), add(mfb)), want: []Action{drift(KindBank, "mfb", fc("status", stepDone, "missing")), {Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: removeB.Impact, Blockers: []string{"mfb settles through bank-b and is not leaving. Remove mfb too, or keep bank-b."}}}},
		{name: "settled-through bank leaving with its settler, settler first", status: func(st *Status) { st.Applied.Spec.Banks = append([]Bank{mfb}, st.Applied.Spec.Banks...) }, desired: drop("bank-b"), want: []Action{gone(KindBank, "mfb"), removeB}},
		{name: "re-add while terminating", status: phase(KindBank, "bank-b", config.PhaseTerminating), desired: pinned("banks[bank-b].id", 27, 11), errs: []string{"setup.yaml:27:11: banks[bank-b] is being removed, and its id is never reused. Choose another id."}, is: ErrBeingRemoved},
		{name: "partner re-add while terminating", status: phase(KindPartner, "balance", config.PhaseTerminating), errs: []string{"partners[balance] is being removed and cannot be added back until removal finishes. Take it out of the file, or wait."}, is: ErrBeingRemoved},
		{name: "re-add after removal finished hits the tombstone", status: func(st *Status) { st.Applied.Spec.Banks, st.Resources = st.Applied.Spec.Banks[:1], st.Resources[:1] }, dir: []nip.Institution{institution("bank-b", "Bank B", "999002", "902", nip.StatusRemoved)}, desired: eb("bank-b", func(b *Bank) { b.NIPCode, b.NUBANCode = "", "" }), errs: []string{"banks[bank-b].id bank-b belonged to a removed bank. Choose another id."}},
		{name: "partners removed before banks, in reverse", status: func(st *Status) {
			st.Applied.Spec.Partners = append(st.Applied.Spec.Partners, Partner{ID: "ledgerly", Name: "Ledgerly"})
		}, desired: both(drop("bank-b"), func(s *Spec) { s.Spec.Partners = nil }), want: []Action{gone(KindPartner, "ledgerly"), gone(KindPartner, "balance"), removeB}},
	})
}

func TestBuildOnRemoveTarget(t *testing.T) {
	bankC := Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", NIPCode: "999003", NUBANCode: "903"}
	withC := func(p string, f func(*Bank)) statEd {
		return func(st *Status) {
			c := bankC
			f(&c)
			st.Applied.Spec.Banks = append(st.Applied.Spec.Banks, c)
			st.Resources = append(st.Resources, Resource{Kind: KindBank, ID: "bank-c", Phase: p, Steps: doneSteps()})
		}
	}
	none := func(*Bank) {}
	closeBTo := func(to string) edit {
		return eb("bank-b", func(b *Bank) { b.State, b.OnRemove = StateClosed, &Removal{Money: MoneyTransfer, To: to} })
	}
	buildCases(t, []planCase{
		{name: "close to itself", desired: closeBTo("bank-b"), errs: []string{"banks[bank-b].on_remove sends money to bank-b itself. Choose another bank."}},
		{name: "close to a bank closing too", status: withC(config.PhaseActive, none), desired: both(add(bankC), closeBTo("bank-c"), eb("bank-c", func(b *Bank) { b.State, b.OnRemove = StateClosed, &Removal{Money: MoneyLeave} })), errs: []string{"banks[bank-b].on_remove sends money to bank-c, which is closing too. Choose a bank that stays open."}},
		{name: "close to a bank already terminating", status: withC(config.PhaseTerminating, none), desired: closeBTo("bank-c"), errs: []string{"banks[bank-b].on_remove sends money to bank-c, which is leaving too. Choose a bank that stays."}},
		{name: "close to a bank already closed", status: withC(config.PhaseClosed, func(b *Bank) { b.State = StateClosed }), desired: both(add(bankC), eb("bank-c", func(b *Bank) { b.State = StateClosed }), closeBTo("bank-c")), errs: []string{"banks[bank-b].on_remove sends money to bank-c, which is closing or closed. Choose a bank that stays open."}},
		{name: "close and rename together", desired: eb("bank-b", func(b *Bank) { b.Name, b.State, b.OnRemove = "Sterling Bank", StateClosed, &Removal{Money: MoneyLeave} }), want: []Action{closing("bank-b", fc("name", "Bank B", "Sterling Bank"), fc("state", StateActive, StateClosed), fc("on_remove", "", MoneyLeave), fc("on_remove.money", "", MoneyLeave))}},
		{name: "removal to a bank removed in the same plan", status: bothSt(withC(config.PhaseActive, none), ab("bank-b", func(b *Bank) { b.OnRemove = &Removal{Money: MoneyTransfer, To: "bank-c"} })), desired: drop("bank-b"), errs: []string{"on_remove of removed bank bank-b sends money to bank-c, which is leaving too. Choose a bank that stays."}},
		{name: "removal to a bank that was never in the file", status: ab("bank-b", func(b *Bank) {
			b.OnRemove = &Removal{Money: MoneyPayout, Customers: map[string]string{"c1": MoneyTransfer}, To: "bank-z"}
		}), desired: drop("bank-b"), errs: []string{"on_remove of removed bank bank-b sends money to bank-z, which is not in the file. Choose a bank in the file."}},
		{name: "payout needs no target", status: ab("bank-b", func(b *Bank) { b.OnRemove = &Removal{Money: MoneyPayout, To: "bank-b"} }), desired: drop("bank-b"), want: []Action{{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Fields: []FieldChange{fc("on_remove.money", "", MoneyPayout), fc("on_remove.to", "", "bank-b")}, Impact: &Impact{Databases: []string{"beerus_x_bank_b"}}}}},
	})
}

func TestBuildAllocation(t *testing.T) {
	newBank := func(id, typ string) Bank { return Bank{ID: id, Name: "Bank " + id, Type: typ} }
	old := func(id, code, nuban string) nip.Institution {
		return institution(id, "Old "+id, code, nuban, nip.StatusRemoved)
	}
	member := func(id, code, nuban string) nip.Institution {
		return institution(id, id, code, nuban, nip.StatusActive)
	}
	noCodes := func(s *Spec) {
		s.Spec.Banks[0].NIPCode, s.Spec.Banks[0].NUBANCode, s.Spec.Banks[1].NIPCode, s.Spec.Banks[1].NUBANCode = "", "", "", ""
	}
	buildCases(t, []planCase{
		{name: "first deposit money bank", desired: add(newBank("bank-c", "commercial")), want: []Action{create("bank-c", "commercial", "000001", "001")}},
		{name: "first microfinance bank", desired: add(newBank("mfb", "microfinance")), want: []Action{create("mfb", "microfinance", "090001", "50001")}},
		{name: "file order, and every kind of holder is skipped", dir: []nip.Institution{old("old", "000001", "001"), member("bank-z", "000002", "000002"), member("mfb-z", "090001", "50001")}, desired: both(drop("bank-b"), add(newBank("bank-c", "digital")), add(newBank("bank-d", "commercial")), add(newBank("mfb", "microfinance"))), want: []Action{create("bank-c", "digital", "000003", "003"), create("bank-d", "commercial", "000004", "004"), create("mfb", "microfinance", "090002", "50002"), gone(KindBank, "bank-b", "beerus_x_bank_b")}},
		{name: "the first tier runs out and the second pads to six digits", small: true, dir: []nip.Institution{old("old-1", "000001", "001"), old("old-2", "000002", "002")}, desired: both(add(newBank("bank-c", "commercial")), add(newBank("bank-d", "commercial"))), want: []Action{create("bank-c", "commercial", "001000", "001000"), create("bank-d", "commercial", "001001", "001001")}},
		{name: "every tier used", small: true, dir: []nip.Institution{old("old-1", "000001", "001"), old("old-2", "000002", "002"), old("old-3", "001000", "001000"), old("old-4", "001001", "001001")}, desired: add(newBank("bank-c", "commercial")), errs: []string{"banks[bank-c]: no bank codes left for deposit money banks (all 4 are used). Run beerus reset to start again."}},
		{name: "codes cannot be set on a new bank", desired: add(Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", NIPCode: "123456", NUBANCode: "123"}), errs: []string{"banks[bank-c].nip_code is assigned by Beerus. Remove it.", "banks[bank-c].nuban_code is assigned by Beerus. Remove it."}},
		{name: "an existing bank may repeat its codes", desired: eb("bank-b", func(b *Bank) { b.NUBANCode = "000902" })},
		{name: "an existing bank cannot change its codes", desired: eb("bank-b", func(b *Bank) { b.NIPCode, b.NUBANCode = "999009", "905" }), errs: []string{"banks[bank-b].nip_code cannot change; it is 999002", "banks[bank-b].nuban_code cannot change; it is 902"}},
		{name: "first apply allocates in file order around the directory", fresh: true, dir: []nip.Institution{member("bank-a", "999001", "901"), member("bank-z", "999002", "902")}, desired: noCodes, want: []Action{create("bank-a", "commercial", "000001", "001"), create("bank-b", "digital", "000002", "002"), {Op: OpCreate, Kind: KindPartner, ID: "balance", Fields: []FieldChange{fc("name", "", "Balance")}}}},
	})
}

func TestBuildSettlesThroughOrderAndDrift(t *testing.T) {
	missing := fc("steps.database", stepDone, "missing")
	buildCases(t, []planCase{
		{name: "settler created after its bank", desired: both(add(Bank{ID: "mfb", Name: "Mfb", Type: "microfinance", SettlesThrough: "bank-c"}), add(Bank{ID: "bank-c", Name: "Bank C", Type: "commercial"})), want: []Action{create("bank-c", "commercial", "000001", "001"), create("mfb", "microfinance", "090001", "50001", fc("settles_through", "", "bank-c"))}},
		{name: "loop", desired: both(add(Bank{ID: "x", Name: "X", Type: "commercial", SettlesThrough: "y"}), add(Bank{ID: "y", Name: "Y", Type: "commercial", SettlesThrough: "x"})), errs: []string{"banks[x].settles_through forms a loop: x → y → x. Point one of them at a bank that settles directly."}},
		{name: "database missing", status: step(KindBank, "bank-a", "missing"), want: []Action{drift(KindBank, "bank-a", missing)}},
		{name: "repairing with two parts", status: rs(KindBank, "bank-b", func(r *Resource) {
			r.Phase, r.Steps[config.StepFiles], r.Steps[config.StepSite] = config.PhaseRepairing, "missing", "failed"
		}), want: []Action{drift(KindBank, "bank-b", fc("steps.files", stepDone, "missing"), fc("steps.site", stepDone, "failed"))}},
		{name: "repairing with no step recorded", status: phase(KindPartner, "balance", config.PhaseRepairing), want: []Action{drift(KindPartner, "balance")}},
		{name: "status record missing", status: func(st *Status) { st.Resources = st.Resources[1:] }, want: []Action{drift(KindBank, "bank-a", fc("status", stepDone, "missing"))}},
		{name: "system database missing", status: step(KindSystem, SystemNIBSS, "missing"), want: []Action{drift(KindSystem, SystemNIBSS, missing)}},
		{name: "terminating bank is not drift", status: bothSt(phase(KindBank, "bank-b", config.PhaseTerminating), step(KindBank, "bank-b", "missing")), desired: drop("bank-b")},
		{name: "an update wins over drift", status: step(KindBank, "bank-a", "missing"), desired: eb("bank-a", func(b *Bank) { b.Name = "Zenith Bank" }), want: []Action{upd(KindBank, "bank-a", fc("name", "Bank A", "Zenith Bank"))}},
	})
}

func TestBuildIsPure(t *testing.T) {
	desired := spec()
	add(Bank{ID: "bank-c", Name: "Bank C", Type: "commercial"})(&desired)
	st, before := status(spec()), status(spec())
	a := Build(realPack(t), desired, st, nil, today)
	b := Build(realPack(t), desired, st, nil, today)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(st, before) {
		t.Fatal("Build is not deterministic or changed its input")
	}
}
