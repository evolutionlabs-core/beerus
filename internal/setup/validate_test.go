package setup

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func validSpec(t *testing.T) Spec {
	t.Helper()
	return clearPos(parseFixture(t, "default.yaml"))
}

func TestValidateDefaultSpecIsClean(t *testing.T) {
	if errs := Validate(DefaultSpec("beerus", clock.Date{Y: 2026, M: time.November, D: 4})); len(errs) > 0 {
		t.Fatal(errStrings(errs))
	}
	if banks := DefaultSpec("beerus", clock.Date{Y: 2026, M: time.November, D: 4}).Spec.Banks; len(banks) != 0 {
		t.Fatalf("a new setup has %d banks", len(banks))
	}
	if errs := Validate(validSpec(t)); len(errs) > 0 {
		t.Fatal(errStrings(errs))
	}
}

func TestValidateRules(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Spec)
		want []string
	}{
		{"no api version", func(s *Spec) { s.APIVersion = "" }, []string{"apiVersion is required. Add apiVersion: beerus.dev/v1."}},
		{"wrong kind", func(s *Spec) { s.Kind = "Cluster" }, []string{`kind "Cluster" is not supported. Use Setup.`}},
		{"no kind", func(s *Spec) { s.Kind = "" }, []string{"kind is required. Add kind: Setup."}},
		{"no name", func(s *Spec) { s.Metadata.Name = "" }, []string{"metadata.name is required. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter."}},
		{"one letter name", func(s *Spec) { s.Metadata.Name = "a" }, []string{`metadata.name "a" is not valid. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter.`}},
		{"32 letter name", func(s *Spec) { s.Metadata.Name = "abcdefghijklmnopqrstuvwxyzabcdef" }, []string{`metadata.name "abcdefghijklmnopqrstuvwxyzabcdef" is not valid. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter.`}},
		{"start without offset", func(s *Spec) { s.Spec.Start = "2026-11-04T08:00:00" }, []string{`start "2026-11-04T08:00:00" is not an RFC 3339 time. Use a time such as 2026-11-04T08:00:00+01:00.`}},
		{"negative sessions", func(s *Spec) { s.Spec.NIP.Sessions = -4 }, []string{"nip.sessions -4 does not split the day into whole minutes. Use a number such as 1, 2, 4, 6, 12 or 24."}},
		{"bad tsq after", func(s *Spec) { s.Spec.NIP.TSQAfter = "soon" }, []string{`nip.tsq_after "soon" is not a duration. Use a duration such as 2m.`}},
		{"negative tsq after", func(s *Spec) { s.Spec.NIP.TSQAfter = "-1m" }, []string{"nip.tsq_after -1m must be longer than zero. Use a duration such as 2m."}},
		{"negative tsq attempts", func(s *Spec) { s.Spec.NIP.TSQAttempts = -1 }, []string{"nip.tsq_attempts -1 must be more than zero. Use a number such as 3, or leave it out."}},
		{"bad holiday", func(s *Spec) { s.Spec.Holidays.Add = []string{"2026-02-30"} }, []string{`holidays.add "2026-02-30" is not a date. Use YYYY-MM-DD.`}},
		{"leap day holiday", func(s *Spec) { s.Spec.Holidays.Add = []string{"2028-02-29"} }, nil},
		{"repeated holiday", func(s *Spec) { s.Spec.Holidays.Add = []string{"2026-12-28", "2026-12-28"} }, []string{"holidays.add 2026-12-28 appears twice. Keep one."}},
		{"holiday in both lists", func(s *Spec) {
			s.Spec.Holidays.Add = []string{"2026-12-28"}
			s.Spec.Holidays.Remove = []string{"2026-12-28"}
		}, []string{"holidays.remove 2026-12-28 is also in holidays.add. Keep it in one list."}},
		{"no banks", func(s *Spec) { s.Spec.Banks = nil }, nil},
		{"no bank id", func(s *Spec) { s.Spec.Banks[1].ID = "" }, []string{"banks[1].id is required. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter."}},
		{"bad bank id", func(s *Spec) { s.Spec.Banks[1].ID = "Bank_B" }, []string{`banks[Bank_B].id "Bank_B" is not valid. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter.`}},
		{"cbn as a bank id", func(s *Spec) { s.Spec.Banks[1].ID = "cbn" }, []string{"banks[cbn].id cbn is reserved for the CBN. Choose another id."}},
		{"nibss as a bank id", func(s *Spec) { s.Spec.Banks[1].ID = "nibss" }, []string{"banks[nibss].id nibss is reserved for the NIBSS. Choose another id."}},
		{"no bank name", func(s *Spec) { s.Spec.Banks[0].Name = "" }, []string{"banks[bank-a].name is required. Add the bank's name."}},
		{"41 character bank name", func(s *Spec) { s.Spec.Banks[0].Name = strings.Repeat("x", 41) }, []string{"banks[bank-a].name has 41 characters. Use 1 to 40."}},
		{"40 character bank name", func(s *Spec) { s.Spec.Banks[0].Name = strings.Repeat("é", 40) }, nil},
		{"same name in another case", func(s *Spec) { s.Spec.Banks[1].Name = "BANK A" }, []string{`banks[bank-b].name "BANK A" is already used by bank-a. Give each bank its own name.`}},
		{"no type", func(s *Spec) { s.Spec.Banks[0].Type = "" }, []string{"banks[bank-a].type is required. Use commercial, digital or microfinance."}},
		{"short nip code", func(s *Spec) { s.Spec.Banks[0].NIPCode = "99901" }, []string{`banks[bank-a].nip_code "99901" is not valid. Use 6 digits.`}},
		{"repeated nip code", func(s *Spec) { s.Spec.Banks[1].NIPCode = "999001" }, []string{"banks[bank-b].nip_code 999001 is already used by bank-a. Give each bank its own code."}},
		{"nuban code of 4 digits", func(s *Spec) { s.Spec.Banks[0].NUBANCode = "9010" }, []string{`banks[bank-a].nuban_code "9010" is not valid. Use 3, 5 or 6 digits.`}},
		{"6 digit nuban code without 9", func(s *Spec) { s.Spec.Banks[0].NUBANCode = "100901" }, nil},
		{"nuban code same after normalising", func(s *Spec) { s.Spec.Banks[1].NUBANCode = "000901" }, []string{"banks[bank-b].nuban_code 000901 is already used by bank-a. Give each bank its own code."}},
		{"5 and 6 digit nuban codes clash", func(s *Spec) {
			s.Spec.Banks[0].NUBANCode = "50211"
			s.Spec.Banks[1].NUBANCode = "950211"
		}, []string{"banks[bank-b].nuban_code 950211 is already used by bank-a. Give each bank its own code."}},
		{"missing codes are fine", func(s *Spec) { s.Spec.Banks[1].NIPCode, s.Spec.Banks[1].NUBANCode = "", "" }, nil},
		{"settles through itself", func(s *Spec) { s.Spec.Banks[1].SettlesThrough = "bank-b" }, []string{"banks[bank-b].settles_through cannot name the bank itself. Name another bank."}},
		{"settles through unknown", func(s *Spec) { s.Spec.Banks[1].SettlesThrough = "bank-z" }, []string{`banks[bank-b].settles_through "bank-z" is not a bank in this file. Name a bank listed under banks.`}},
		{"settles through microfinance", func(s *Spec) {
			s.Spec.Banks[0].Type = "microfinance"
			s.Spec.Banks[1].SettlesThrough = "bank-a"
		}, []string{"banks[bank-b].settles_through bank-a is a microfinance bank. Name a commercial or digital bank."}},
		{"settles through closed", func(s *Spec) {
			s.Spec.Banks[0].State = StateClosed
			s.Spec.Banks[0].OnRemove = &Removal{Money: MoneyLeave}
			s.Spec.Banks[1].SettlesThrough = "bank-a"
		}, []string{"banks[bank-b].settles_through bank-a is closed. Name an open bank."}},
		{"bad state", func(s *Spec) { s.Spec.Banks[0].State = "paused" }, []string{`banks[bank-a].state "paused" is not valid. Use active, suspended or closed.`}},
		{"negative cap", func(s *Spec) { s.Spec.Banks[0].NetDebitCap = -1 }, []string{"banks[bank-a].net_debit_cap_minor -1 must be more than zero. Give it in kobo, or leave it out for the default."}},
		{"negative collateral", func(s *Spec) { s.Spec.Banks[0].Collateral = -5 }, []string{"banks[bank-a].collateral_minor -5 must be more than zero. Give it in kobo, or leave it out for the default."}},
		{"negative consent months", func(s *Spec) { s.Spec.Banks[0].OpenBanking.ConsentMonths = -1 }, []string{"banks[bank-a].open_banking.consent_months -1 must be more than zero. Leave it out for the default."}},
		{"no money choice", func(s *Spec) { s.Spec.Banks[1].OnRemove = &Removal{To: "bank-a"} }, []string{"banks[bank-b].on_remove.money is required. Use transfer, payout or leave."}},
		{"bad money choice", func(s *Spec) { s.Spec.Banks[1].OnRemove = &Removal{Money: "burn"} }, []string{`banks[bank-b].on_remove.money "burn" is not valid. Use transfer, payout or leave.`}},
		{"transfer without target", func(s *Spec) { s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyTransfer} }, []string{"banks[bank-b].on_remove.to is required when money is transferred. Name the bank that receives it."}},
		{"payout without target", func(s *Spec) { s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyPayout} }, nil},
		{"payout with target", func(s *Spec) { s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyPayout, To: "bank-a"} }, []string{"banks[bank-b].on_remove.to is set but no money is transferred. Payout is cash and needs no bank. Remove to, or choose transfer."}},
		{"leave with target", func(s *Spec) { s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyLeave, To: "bank-a"} }, []string{"banks[bank-b].on_remove.to is set but no money is transferred. Payout is cash and needs no bank. Remove to, or choose transfer."}},
		{"leave with a customer moving", func(s *Spec) {
			s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyLeave, Customers: map[string]string{"cus_1": MoneyTransfer}}
		}, []string{"banks[bank-b].on_remove.to is required when money is transferred. Name the bank that receives it."}},
		{"leave with a customer moving and target", func(s *Spec) {
			s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyLeave, To: "bank-a", Customers: map[string]string{"cus_1": MoneyTransfer}}
		}, nil},
		{"bad customer choices sorted", func(s *Spec) {
			s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyLeave, Customers: map[string]string{"cus_b": "keep", "cus_a": "gift"}}
		}, []string{
			`banks[bank-b].on_remove.customers[cus_a] "gift" is not valid. Use transfer, payout or leave.`,
			`banks[bank-b].on_remove.customers[cus_b] "keep" is not valid. Use transfer, payout or leave.`,
		}},
		{"target is itself", func(s *Spec) { s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyTransfer, To: "bank-b"} }, []string{"banks[bank-b].on_remove.to cannot name the bank being removed. Name another bank."}},
		{"closed with on_remove", func(s *Spec) {
			s.Spec.Banks[1].State = StateClosed
			s.Spec.Banks[1].OnRemove = &Removal{Money: MoneyTransfer, To: "bank-a"}
		}, nil},
		{"no partner id", func(s *Spec) { s.Spec.Partners = []Partner{{Name: "Balance"}} }, []string{"partners[0].id is required. Use 1 to 40 lowercase letters, digits or hyphens."}},
		{"bad partner id", func(s *Spec) { s.Spec.Partners = []Partner{{ID: "-bal", Name: "Balance"}} }, []string{`partners[-bal].id "-bal" is not valid. Use 1 to 40 lowercase letters, digits or hyphens, starting with a letter or digit.`}},
		{"no partner name", func(s *Spec) { s.Spec.Partners = []Partner{{ID: "balance"}} }, []string{"partners[balance].name is required. Add the partner's name."}},
		{"bad redirect", func(s *Spec) {
			s.Spec.Partners = []Partner{{ID: "balance", Name: "Balance", RedirectURIs: []string{"https://ok.example/cb", "ftp://x/cb"}}}
		}, []string{`partners[balance].redirect_uris "ftp://x/cb" is not an http or https URL. Use a full URL such as https://localhost:3000/callback.`}},
		{"bad webhook", func(s *Spec) {
			s.Spec.Partners = []Partner{{ID: "balance", Name: "Balance", WebhookURL: "localhost:3000"}}
		}, []string{`partners[balance].webhook_url "localhost:3000" is not an http or https URL. Use a full URL such as https://localhost:3000/callback.`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validSpec(t)
			c.edit(&s)
			if got := errStrings(Validate(s)); !slices.Equal(got, c.want) {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestValidateUsesPositionsWhenParsed(t *testing.T) {
	s := parseFixture(t, "default.yaml")
	s.Spec.Banks[1].Type = "online"
	want := []string{`setup.yaml:27:7: banks[bank-b].type "online" is not a bank type. Use commercial, digital or microfinance.`}
	if got := errStrings(Validate(s)); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestValidateFallsBackToTheParentPosition(t *testing.T) {
	s := parseFixture(t, "default.yaml")
	s.Spec.Banks[0].SettlesThrough = "bank-z"
	want := []string{`setup.yaml:20:7: banks[bank-a].settles_through "bank-z" is not a bank in this file. Name a bank listed under banks.`}
	if got := errStrings(Validate(s)); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}
