package world

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const retailGolden = "098c7224ba85b6544c970a95e1fa6def0b34e8b09af35904a74a031a417eac34"

var goldenNow = time.Date(2026, time.January, 15, 12, 0, 0, 0, clock.Lagos)

func generate(t *testing.T, banks []Institution, days int, now time.Time) Template {
	t.Helper()
	tmpl, err := Generate(TemplateRetail, 42, banks, days, now, clock.NewCalendar(nil, nil), testFees(t))
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func digest(t *testing.T, tmpl Template) string {
	t.Helper()
	b, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func bankC() Institution {
	return Institution{ID: "bank-c", Name: "Bank C", Type: "digital", NIPCode: "999003", NUBANCode: "903", Style: "digital"}
}

func onlyBank(tmpl Template, bank string) Template {
	out := Template{}
	for _, c := range tmpl.Customers {
		if c.Bank == bank {
			out.Customers = append(out.Customers, c)
		}
	}
	for _, a := range tmpl.Accounts {
		if a.Bank == bank {
			out.Accounts = append(out.Accounts, a)
		}
	}
	for _, p := range tmpl.Postings {
		if p.Bank == bank {
			out.Postings = append(out.Postings, p)
		}
	}
	return out
}

func kind(p SeedPosting) string {
	return p.Key[strings.LastIndex(p.Key, ":")+1:]
}

func TestRetailGoldenDigest(t *testing.T) {
	tmpl := generate(t, DefaultInstitutions(), 90, goldenNow)
	if got := digest(t, tmpl); got != retailGolden {
		t.Fatalf("retail digest for value 42 over 90 days is %s, want %s (%d customers, %d accounts, %d postings)", got, retailGolden, len(tmpl.Customers), len(tmpl.Accounts), len(tmpl.Postings))
	}
}

func TestRetailIsDeterministic(t *testing.T) {
	a := generate(t, DefaultInstitutions(), 90, goldenNow)
	b := generate(t, DefaultInstitutions(), 90, goldenNow)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two runs with the same template and value differ")
	}
	for i := range a.Customers {
		if a.Customers[i].Password != b.Customers[i].Password {
			t.Fatalf("customer %s has two passwords", a.Customers[i].Key)
		}
	}
}

func baseKey(key string) string {
	key = strings.TrimSuffix(key, ":rev")
	for _, c := range []string{":fee", ":vat", ":stamp", ":emtl"} {
		if strings.HasSuffix(key, c) {
			return strings.TrimSuffix(key, c)
		}
	}
	return key
}

func byKey(ps []SeedPosting) map[string]SeedPosting {
	out := map[string]SeedPosting{}
	for _, p := range ps {
		out[p.Key] = p
	}
	return out
}

func TestRetailAddingABankOnlyMovesTransfersToIt(t *testing.T) {
	defaults := DefaultInstitutions()
	a, b, c := defaults[0], defaults[1], bankC()
	for _, step := range []struct {
		before, after []Institution
		added         Institution
	}{
		{[]Institution{a}, []Institution{a, b}, b},
		{[]Institution{a, b}, []Institution{a, b, c}, c},
	} {
		was := onlyBank(generate(t, step.before, 90, goldenNow), a.ID)
		now := onlyBank(generate(t, step.after, 90, goldenNow), a.ID)
		if !reflect.DeepEqual(was.Customers, now.Customers) || !reflect.DeepEqual(was.Accounts, now.Accounts) {
			t.Fatalf("adding %s changes bank-a's customers or accounts", step.added.ID)
		}
		before, after := byKey(was.Postings), byKey(now.Postings)
		moved := map[string]bool{}
		for k, p := range after {
			q, ok := before[k]
			if ok && k == baseKey(k) && p.CounterpartyBank != q.CounterpartyBank {
				if p.CounterpartyBank != step.added.NIPCode {
					t.Fatalf("adding %s moves %s from %s to %s", step.added.ID, k, q.CounterpartyBank, p.CounterpartyBank)
				}
				moved[k] = true
			}
		}
		if len(moved) == 0 {
			t.Fatalf("adding %s moves no transfers to it", step.added.ID)
		}
		keys := map[string]bool{}
		for k := range before {
			keys[k] = true
		}
		for k := range after {
			keys[k] = true
		}
		for k := range keys {
			p, inAfter := after[k]
			q, inBefore := before[k]
			switch {
			case !moved[baseKey(k)]:
				if !inAfter || !inBefore || !reflect.DeepEqual(p, q) {
					t.Fatalf("adding %s changes posting %s, which did not move: %+v to %+v", step.added.ID, k, q, p)
				}
			case k == baseKey(k) || k == baseKey(k)+":rev":
				if !inAfter || !inBefore || p.Amount != q.Amount || !p.PostedAt.Equal(q.PostedAt) || p.ValueDate != q.ValueDate || p.Counterparty != q.Counterparty || p.CounterpartyBank != step.added.NIPCode {
					t.Fatalf("adding %s changes the principal of moved transfer %s: %+v to %+v", step.added.ID, k, q, p)
				}
			}
		}
	}
	ab := generate(t, []Institution{a, b}, 90, goldenNow)
	ba := generate(t, []Institution{b, a}, 90, goldenNow)
	if !reflect.DeepEqual(onlyBank(ab, a.ID), onlyBank(ba, a.ID)) {
		t.Fatal("bank-a depends on the order of the banks")
	}
}

func TestRetailIntraBankTransfersFollowTheChargeRules(t *testing.T) {
	fees := testFees(t)
	tmpl := generate(t, DefaultInstitutions(), 90, goldenNow)
	keys := byKey(tmpl.Postings)
	intra, nip := 0, 0
	for _, p := range tmpl.Postings {
		if p.Key != baseKey(p.Key) {
			continue
		}
		switch kind(p) {
		case "out":
			_, fee := keys[p.Key+":fee"]
			_, vat := keys[p.Key+":vat"]
			_, stamp := keys[p.Key+":stamp"]
			wantStamp := fees.StampDuty(-p.Amount, p.ValueDate, false) > 0
			if p.Channel == "internal" {
				intra++
				if fee || vat || stamp != wantStamp {
					t.Fatalf("intra-bank outflow %s has fee %v, VAT %v, stamp %v", p.Key, fee, vat, stamp)
				}
			} else {
				nip++
				if !fee || !vat || stamp != wantStamp {
					t.Fatalf("NIP outflow %s has fee %v, VAT %v, stamp %v", p.Key, fee, vat, stamp)
				}
			}
		case "in":
			_, emtl := keys[p.Key+":emtl"]
			want := p.Channel == "nip" && fees.EMTL(p.Amount, p.ValueDate) > 0
			if emtl != want {
				t.Fatalf("inflow %s on channel %s has EMTL %v", p.Key, p.Channel, emtl)
			}
		}
	}
	if intra == 0 || nip == 0 {
		t.Fatalf("%d intra-bank and %d NIP outflows, want some of each", intra, nip)
	}
}

func TestRetailOneBankTransfersAreInternal(t *testing.T) {
	a := DefaultInstitutions()[0]
	tmpl := generate(t, []Institution{a}, 90, goldenNow)
	transfers := 0
	for _, p := range tmpl.Postings {
		switch kind(p) {
		case "in", "out", "salary":
			transfers++
			if p.CounterpartyBank != a.NIPCode || p.Channel != "internal" || strings.HasPrefix(p.Narration, "NIP") {
				t.Fatalf("one-bank transfer %s has bank %s, channel %q, narration %q", p.Key, p.CounterpartyBank, p.Channel, p.Narration)
			}
		}
	}
	if transfers == 0 {
		t.Fatal("no transfers in a one-bank setup")
	}
}

func TestRetailPersonaIsTheSameAtEveryBank(t *testing.T) {
	tmpl := generate(t, []Institution{DefaultInstitutions()[0], DefaultInstitutions()[1], bankC()}, 0, goldenNow)
	w := New(Config{Seed: 42, Institutions: DefaultInstitutions(), Fees: testFees(t)}, Overrides{})
	byUser := map[string]SeedCustomer{}
	phones := map[string]string{}
	shared := 0
	for _, c := range tmpl.Customers {
		p, ok := w.PersonaByUsername(c.Username)
		if !ok {
			t.Fatalf("customer %s has unknown username %q", c.Key, c.Username)
		}
		if c.Password != p.Password || c.Name != p.Name {
			t.Fatalf("customer %s is %q with password %q, want %q with %q", c.Key, c.Name, c.Password, p.Name, p.Password)
		}
		if prev, ok := byUser[c.Username]; ok {
			shared++
			if prev.Phone != c.Phone || prev.DOB != c.DOB {
				t.Fatalf("%s has phone %s and DOB %s at %s but %s and %s at %s", c.Username, prev.Phone, prev.DOB, prev.Bank, c.Phone, c.DOB, c.Bank)
			}
			continue
		}
		if other, ok := phones[c.Phone]; ok {
			t.Fatalf("%s and %s share phone %s", other, c.Username, c.Phone)
		}
		if len(c.Phone) != 11 {
			t.Fatalf("phone %q has %d digits, want 11", c.Phone, len(c.Phone))
		}
		byUser[c.Username], phones[c.Phone] = c, c.Username
	}
	if shared == 0 {
		t.Fatal("no persona banks at more than one bank")
	}
}

func TestRetailAccountNamesFollowBankStyle(t *testing.T) {
	tmpl := generate(t, DefaultInstitutions(), 0, goldenNow)
	for _, a := range tmpl.Accounts {
		upper := a.Name == strings.ToUpper(a.Name)
		if (a.Bank == "bank-a") != upper {
			t.Fatalf("account %s at %s is named %q", a.Key, a.Bank, a.Name)
		}
		if !a.OpenedAt.Equal(clock.DateOf(goldenNow).Start()) {
			t.Fatalf("account %s opened at %s, want the start of history", a.Key, a.OpenedAt)
		}
	}
}

func TestRetailPaydayIsLastBusinessDayOnOrBeforeThe25th(t *testing.T) {
	tmpl := generate(t, DefaultInstitutions(), 90, goldenNow)
	dates := map[string][]string{}
	for _, p := range tmpl.Postings {
		if kind(p) == "salary" {
			acct := p.Account
			dates[acct] = append(dates[acct], p.PostedAt.In(clock.Lagos).Format("2006-01-02"))
		}
	}
	if len(dates) == 0 {
		t.Fatal("no salary postings")
	}
	want := []string{"2025-10-24", "2025-11-25", "2025-12-24"}
	for acct, got := range dates {
		if !slices.Equal(got, want) {
			t.Fatalf("salary for %s paid on %v, want %v", acct, got, want)
		}
	}
}

func TestPaydayRule(t *testing.T) {
	cal := clock.NewCalendar(nil, nil)
	for _, c := range []struct {
		date string
		want bool
	}{
		{"2025-10-24", true},
		{"2025-10-25", false},
		{"2025-12-24", true},
		{"2025-12-25", false},
		{"2026-01-23", true},
		{"2026-01-26", false},
		{"2028-02-25", true},
	} {
		d, err := clock.ParseDate(c.date)
		if err != nil {
			t.Fatal(err)
		}
		if got := payday(d, cal); got != c.want {
			t.Fatalf("payday(%s) = %v, want %v", c.date, got, c.want)
		}
	}
}

func TestRetailMonthEndChargesFallOnTheLastDay(t *testing.T) {
	for _, c := range []struct {
		now  time.Time
		days int
		want []string
	}{
		{goldenNow, 90, []string{"2025-10-31", "2025-11-30", "2025-12-31"}},
		{time.Date(2028, time.March, 2, 12, 0, 0, 0, clock.Lagos), 5, []string{"2028-02-29"}},
	} {
		tmpl := generate(t, DefaultInstitutions(), c.days, c.now)
		seen := map[string]bool{}
		for _, p := range tmpl.Postings {
			switch kind(p) {
			case "sms", "smsvat", "maint", "maintvat":
				d := clock.DateOf(p.PostedAt)
				if d.AddDays(1).D != 1 {
					t.Fatalf("month-end charge %s posted on %s", p.Key, d)
				}
				seen[d.String()] = true
			}
		}
		got := []string{}
		for d := range seen {
			got = append(got, d)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Fatalf("month-end charges on %v, want %v", got, c.want)
		}
	}
}

func TestRetailWeekendsAndHolidaysAreQuieter(t *testing.T) {
	cal := clock.NewCalendar(nil, nil)
	tmpl := generate(t, DefaultInstitutions(), 365, goldenNow)
	days := map[bool]map[clock.Date]bool{true: {}, false: {}}
	count := map[bool]int{}
	for d := clock.DateOf(goldenNow).AddDays(-365); d.Before(clock.DateOf(goldenNow)); d = d.AddDays(1) {
		days[cal.IsBusinessDay(d)][d] = true
	}
	for _, p := range tmpl.Postings {
		if kind(p) == "in" {
			count[cal.IsBusinessDay(clock.DateOf(p.PostedAt))]++
		}
	}
	busy := float64(count[true]) / float64(len(days[true]))
	quiet := float64(count[false]) / float64(len(days[false]))
	if quiet*2 > busy {
		t.Fatalf("%.1f inflows per weekend or holiday, %.1f per business day; want under half", quiet, busy)
	}
}

func TestRetailPostsNothingAtOrAfterNow(t *testing.T) {
	now := time.Date(2026, time.January, 15, 0, 0, 1, 0, clock.Lagos)
	tmpl := generate(t, DefaultInstitutions(), 90, now)
	keys := map[string]bool{}
	for _, p := range tmpl.Postings {
		keys[p.Key] = true
	}
	reversals := 0
	for _, p := range tmpl.Postings {
		if !p.PostedAt.Before(now) {
			t.Fatalf("posting %s at %s is not before %s", p.Key, p.PostedAt, now)
		}
		if p.PostedAt.Before(clock.DateOf(now).AddDays(-90).Start()) {
			t.Fatalf("posting %s at %s is before the history starts", p.Key, p.PostedAt)
		}
		if strings.HasSuffix(p.Key, ":rev") {
			reversals++
			if !keys[strings.TrimSuffix(p.Key, ":rev")] {
				t.Fatalf("reversal %s has no original", p.Key)
			}
			if p.Channel != "reversal" {
				t.Fatalf("reversal %s has channel %q", p.Key, p.Channel)
			}
		}
	}
	if reversals == 0 {
		t.Fatal("no reversals in 90 days")
	}
}

func TestRetailPostingsCarryStructuredFields(t *testing.T) {
	tmpl := generate(t, DefaultInstitutions(), 90, goldenNow)
	for _, p := range tmpl.Postings {
		if p.Narration == "" || p.Channel == "" || p.Amount == 0 {
			t.Fatalf("posting %s lacks narration, channel or amount: %+v", p.Key, p)
		}
		switch p.Channel {
		case "nip", "internal":
			if p.Counterparty == "" || p.CounterpartyBank == "" {
				t.Fatalf("posting %s has no counterparty", p.Key)
			}
		case "charge":
			if p.Category == "" {
				t.Fatalf("charge %s has no category", p.Key)
			}
		}
	}
}

func TestRetailBalancesNeverGoNegative(t *testing.T) {
	tmpl := generate(t, DefaultInstitutions(), 365, goldenNow)
	bal := map[string]int64{}
	for _, a := range tmpl.Accounts {
		bal[a.Key] = a.Opening
	}
	for _, p := range tmpl.Postings {
		bal[p.Account] += p.Amount
		if bal[p.Account] < 0 {
			t.Fatalf("account %s goes to %d after %s", p.Account, bal[p.Account], p.Key)
		}
	}
}

func TestGenerateRejectsBadInput(t *testing.T) {
	cal := clock.NewCalendar(nil, nil)
	a := DefaultInstitutions()[0]
	for _, c := range []struct {
		template string
		banks    []Institution
		cal      *clock.Calendar
		want     string
	}{
		{"business", nil, cal, `unknown template "business"; known templates: retail`},
		{TemplateRetail, nil, nil, "generate seed: no holiday calendar"},
		{TemplateRetail, []Institution{a, {Name: "Bank X"}}, cal, "bank 2 in the list has no id"},
		{TemplateRetail, []Institution{{ID: "bank-x", Style: "online"}}, cal, `bank bank-x has style "online"; it must be traditional or digital`},
		{TemplateRetail, []Institution{a, a}, cal, "bank bank-a is listed twice"},
	} {
		_, err := Generate(c.template, 42, c.banks, 90, goldenNow, c.cal, testFees(t))
		if err == nil || err.Error() != c.want {
			t.Fatalf("got %v, want %q", err, c.want)
		}
	}
	if _, err := Generate(TemplateRetail, 42, nil, 90, goldenNow, cal, nil); err == nil || err.Error() != "generate seed: no fee schedule" {
		t.Fatalf("no fee schedule: got %v", err)
	}
}

func TestGenerateWithNoBanksIsEmpty(t *testing.T) {
	tmpl := generate(t, nil, 90, goldenNow)
	if len(tmpl.Customers)+len(tmpl.Accounts)+len(tmpl.Postings) != 0 {
		t.Fatalf("no banks gave %d customers", len(tmpl.Customers))
	}
}

func TestRetailMaintenanceFeeLeavesOutReversedDebits(t *testing.T) {
	fees := testFees(t)
	tmpl := generate(t, DefaultInstitutions(), 365, goldenNow)
	keys := byKey(tmpl.Postings)
	type month struct {
		account string
		m       clock.Date
	}
	turnover := map[month]int64{}
	reversed := map[month]int64{}
	lateReversals := 0
	for _, p := range tmpl.Postings {
		if p.Amount >= 0 || (p.Channel != "nip" && p.Channel != "internal") {
			continue
		}
		m := month{p.Account, monthOf(p.PostedAt)}
		if rev, ok := keys[p.Key+":rev"]; ok {
			reversed[m] += -p.Amount
			if monthOf(rev.PostedAt) != m.m {
				lateReversals++
			}
			continue
		}
		turnover[m] += -p.Amount
	}
	checked := 0
	for _, p := range tmpl.Postings {
		if kind(p) != "maint" {
			continue
		}
		m := month{p.Account, monthOf(p.PostedAt)}
		want := (turnover[m]*fees.MaintenancePerMille() + 500) / 1000
		if -p.Amount != want {
			t.Fatalf("maintenance fee %s is %d, want %d on turnover %d", p.Key, -p.Amount, want, turnover[m])
		}
		if reversed[m] > 0 {
			checked++
		}
	}
	if checked == 0 || lateReversals == 0 {
		t.Fatalf("%d maintenance fees in months with a reversed debit and %d reversals in a later month; want some of each", checked, lateReversals)
	}
}
