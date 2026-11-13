package setup

import (
	"math"
	"testing"
)

func TestRenderSample(t *testing.T) {
	p := Plan{Setup: "beerus", Actions: []Action{
		{Op: OpCreate, Kind: KindBank, ID: "bank-c", Allocated: []string{"nip_code", "nuban_code"}, Fields: []FieldChange{
			{Path: "type", To: "commercial"}, {Path: "nip_code", To: "999003"}, {Path: "nuban_code", To: "903"},
		}},
		{Op: OpUpdate, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{
			{Path: "name", From: "Bank A", To: "Zenith Bank"},
			{Path: "net_debit_cap_minor", From: "50000000000", To: "80000000000"},
		}},
		{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true,
			Fields: []FieldChange{{Path: "on_remove.money", To: MoneyTransfer}, {Path: "on_remove.to", To: "Zenith Bank"}},
			Impact: &Impact{Transfers: 2, NetMinor: 125000000, MoneyMinor: 8421050000, Customers: 3, Connections: 3, Databases: []string{"beerus_x_bank_b"}, Snapshots: []string{"snapshots/bank-b.json"}}},
		{Op: OpDrift, Kind: KindBank, ID: "bank-c", Fields: []FieldChange{{Path: "steps.database", From: stepDone, To: "missing"}}},
	}}
	want := `Setup beerus

  + bank bank-c      create   commercial, NIP 999003 (allocated), NUBAN 903 (allocated)
  ~ bank bank-a      update   name Bank A → Zenith Bank; net_debit_cap 500,000,000.00 → 800,000,000.00
  - bank bank-b      remove   DESTRUCTIVE
        stop routing; revoke 3 connections; settle 2 open transfers (net ₦1,250,000.00);
        transfer ₦84,210,500.00 for 3 customers to Zenith Bank (on_remove); settle the moves;
        drop 1 database and 1 snapshot. Its codes stay reserved.
  ! bank bank-c      drift    database missing; it will be rebuilt

Plan: 1 to create, 1 to update, 1 to remove, 1 to repair.
`
	if got := Render(p); got != want {
		t.Fatalf("render:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderSingularsAndOtherChoices(t *testing.T) {
	p := Plan{Setup: "lagos-dev", Actions: []Action{
		{Op: OpUpdate, Kind: KindSetup, ID: "lagos-dev", Fields: []FieldChange{
			{Path: "nip.tsq_after", From: "2m", To: "5m"},
			{Path: "holidays.add", To: "2026-12-28"},
			{Path: "holidays.remove", From: "2026-12-26"},
		}},
		{Op: OpUpdate, Kind: KindBank, ID: "bank-a", Fields: []FieldChange{
			{Path: "collateral_minor", From: "0", To: "150"},
			{Path: "open_banking.consent_months", From: "12", To: "0"},
			{Path: "on_remove", To: "payout"},
		}},
		{Op: OpClose, Kind: KindBank, ID: "bank-b", Destructive: true,
			Fields: []FieldChange{{Path: "name", From: "Bank B", To: "Sterling Bank"}, {Path: "state", From: StateActive, To: StateClosed}, {Path: "on_remove.money", To: MoneyPayout}},
			Impact: &Impact{Transfers: 1, NetMinor: -5050, MoneyMinor: 100, Customers: 1, Connections: 1}},
		{Op: OpRemove, Kind: KindBank, ID: "mfb", Destructive: true,
			Fields:   []FieldChange{{Path: "on_remove.money", To: MoneyLeave}},
			Impact:   &Impact{MoneyMinor: 20000, Customers: 2, Databases: []string{"a", "b"}, Snapshots: []string{"s1", "s2", "s3"}},
			Blockers: []string{"mfb is the last bank. Keep at least one bank in the file."}},
		{Op: OpRemove, Kind: KindPartner, ID: "balance", Destructive: true, Impact: &Impact{Connections: 2}},
		{Op: OpRemove, Kind: KindPartner, ID: "ledgerly", Destructive: true, Impact: &Impact{}},
		{Op: OpCreate, Kind: KindPartner, ID: "ledgerly2", Fields: []FieldChange{{Path: "name", To: "Ledgerly"}}},
		{Op: OpDrift, Kind: KindBank, ID: "bank-c", Fields: []FieldChange{
			{Path: "steps.certs", From: stepDone, To: "missing"},
			{Path: "steps.site", From: stepDone, To: "missing"},
			{Path: "steps.api", From: stepDone, To: "missing"},
		}},
		{Op: OpDrift, Kind: KindSystem, ID: "nibss"},
	}}
	want := `Setup lagos-dev

  ~ setup lagos-dev        update   nip.tsq_after 2m → 5m; holidays.add +2026-12-28; holidays.remove -2026-12-26
  ~ bank bank-a            update   collateral default → 1.50; open_banking.consent_months 12 → default; on_remove +payout
  - bank bank-b            close    DESTRUCTIVE; name Bank B → Sterling Bank
        stop routing; revoke 1 connection; settle 1 open transfer (net -₦50.50);
        pay out the insured part of ₦1.00 for 1 customer in cash (on_remove). Its database and history stay.
  - bank mfb               remove   DESTRUCTIVE
        stop routing;
        leave ₦200.00 for 2 customers on frozen accounts (on_remove);
        drop 2 databases and 3 snapshots. Its codes stay reserved.
        blocked: mfb is the last bank. Keep at least one bank in the file.
  - partner balance        remove   DESTRUCTIVE
        revoke 2 connections; drop its keys and live rows.
  - partner ledgerly       remove   DESTRUCTIVE
        drop its keys and live rows.
  + partner ledgerly2      create   Ledgerly
  ! bank bank-c            drift    certificates, site and API missing; they will be rebuilt
  ! system nibss           drift    being repaired

Plan: 1 to create, 2 to update, 1 to close, 3 to remove, 2 to repair.
`
	got := Render(p)
	if got != want {
		t.Fatalf("render:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderMoneyDecisionNeeded(t *testing.T) {
	p := Plan{Setup: "beerus", Actions: []Action{
		{Op: OpRemove, Kind: KindBank, ID: "bank-b", Destructive: true, Impact: &Impact{MoneyMinor: 8421050000, Customers: 3}},
	}}
	want := `Setup beerus

  - bank bank-b      remove   DESTRUCTIVE
        stop routing;
        ₦84,210,500.00 for 3 customers needs a money decision: add on_remove or run with --money. Its codes stay reserved.

Plan: 1 to remove.
`
	if got := Render(p); got != want {
		t.Fatalf("render:\n%s\nwant:\n%s", got, want)
	}
}

func TestNaira(t *testing.T) {
	cases := map[int64]string{
		0:             "₦0.00",
		5:             "₦0.05",
		100:           "₦1.00",
		99999:         "₦999.99",
		100000:        "₦1,000.00",
		-125000000:    "-₦1,250,000.00",
		math.MaxInt64: "₦92,233,720,368,547,758.07",
		math.MinInt64: "-₦92,233,720,368,547,758.08",
	}
	for in, want := range cases {
		if got := naira(in); got != want {
			t.Errorf("naira(%d) = %q, want %q", in, got, want)
		}
	}
}
