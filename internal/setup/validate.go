package setup

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var BankTypes = []string{"commercial", "digital", "microfinance"}

var (
	specIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	specPartnerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	specNIPPattern     = regexp.MustCompile(`^\d{6}$`)
	specSystemIDs      = []string{SystemCBN, SystemNIBSS}
	specStates         = []string{StateActive, StateSuspended, StateClosed}
	specMoneyChoices   = []string{MoneyTransfer, MoneyPayout, MoneyLeave}
)

const (
	specMaxBankName  = 40
	specIDRule       = "Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter"
	specMinutesInDay = int(24 * time.Hour / time.Minute)
)

func Validate(s Spec) []error {
	var out []error
	for _, p := range s.check() {
		out = append(out, p.err)
	}
	return out
}

type specProblem struct {
	path string
	err  error
}

type specChecker struct {
	s        Spec
	problems []specProblem
}

func (c *specChecker) add(path, format string, args ...any) {
	c.problems = append(c.problems, specProblem{path: path, err: specErrorf(c.s, path, format, args...)})
}

func specErrorf(s Spec, path, format string, args ...any) error {
	return posErrorf(s.pos, path, format, args...)
}

func posErrorf(pos map[string]Pos, path, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...) + "."
	for p := path; ; p = specParent(p) {
		if at, ok := pos[p]; ok {
			return fmt.Errorf("%s:%d:%d: %s", at.File, at.Line, at.Col, msg)
		}
		if p == "" {
			return errors.New(msg)
		}
	}
}

func (s Spec) has(path string) bool {
	_, ok := s.Pos(path)
	return ok
}

func (s Spec) check() []specProblem {
	c := &specChecker{s: s}
	c.header()
	c.nip()
	c.holidays()
	c.banks()
	c.partners()
	return c.problems
}

func (c *specChecker) header() {
	s := c.s
	switch s.APIVersion {
	case APIVersion:
	case "":
		c.add("apiVersion", "apiVersion is required. Add apiVersion: %s", APIVersion)
	default:
		c.add("apiVersion", "apiVersion %q is not supported. Use %s", s.APIVersion, APIVersion)
	}
	switch s.Kind {
	case KindSetup:
	case "":
		c.add("kind", "kind is required. Add kind: %s", KindSetup)
	default:
		c.add("kind", "kind %q is not supported. Use %s", s.Kind, KindSetup)
	}
	switch {
	case s.Metadata.Name == "":
		c.add("metadata.name", "metadata.name is required. %s", specIDRule)
	case !specIDPattern.MatchString(s.Metadata.Name):
		c.add("metadata.name", "metadata.name %q is not valid. %s", s.Metadata.Name, specIDRule)
	}
	if s.Spec.Start == "" {
		c.add("start", "start is required. Add an RFC 3339 time such as 2026-11-04T08:00:00+01:00")
	} else if _, err := time.Parse(time.RFC3339, s.Spec.Start); err != nil {
		c.add("start", "start %q is not an RFC 3339 time. Use a time such as 2026-11-04T08:00:00+01:00", s.Spec.Start)
	}
}

func (c *specChecker) positive(path string, v int64, hint string) {
	if v < 0 || (v == 0 && c.s.has(path)) {
		c.add(path, "%s %d must be more than zero. %s", path, v, hint)
	}
}

func (c *specChecker) nip() {
	n := c.s.Spec.NIP
	if n.Sessions < 0 || (c.s.has("nip.sessions") && (n.Sessions == 0 || specMinutesInDay%n.Sessions != 0)) {
		c.add("nip.sessions", "nip.sessions %d does not split the day into whole minutes. Use a number such as 1, 2, 4, 6, 12 or 24", n.Sessions)
	}
	if n.TSQAfter != "" || c.s.has("nip.tsq_after") {
		if d, err := time.ParseDuration(n.TSQAfter); err != nil {
			c.add("nip.tsq_after", "nip.tsq_after %q is not a duration. Use a duration such as 2m", n.TSQAfter)
		} else if d <= 0 {
			c.add("nip.tsq_after", "nip.tsq_after %s must be longer than zero. Use a duration such as 2m", n.TSQAfter)
		}
	}
	c.positive("nip.tsq_attempts", int64(n.TSQAttempts), "Use a number such as 3, or leave it out")
}

func (c *specChecker) holidays() {
	h := c.s.Spec.Holidays
	add := c.dates("holidays.add", h.Add)
	c.dates("holidays.remove", h.Remove)
	paths := specItemPaths("holidays.remove", h.Remove)
	for i, d := range h.Remove {
		if add[d] {
			c.add(paths[i], "holidays.remove %s is also in holidays.add. Keep it in one list", d)
		}
	}
}

func (c *specChecker) dates(path string, list []string) map[string]bool {
	seen := map[string]bool{}
	paths := specItemPaths(path, list)
	for i, d := range list {
		if _, err := clock.ParseDate(d); err != nil {
			c.add(paths[i], "%s %q is not a date. Use YYYY-MM-DD", path, d)
			continue
		}
		if seen[d] {
			c.add(paths[i], "%s %s appears twice. Keep one", path, d)
		}
		seen[d] = true
	}
	return seen
}

func (c *specChecker) banks() {
	banks := c.s.Spec.Banks
	if len(banks) == 0 {
		c.add("banks", "banks is empty. Add at least one bank")
		return
	}
	ids := make([]string, len(banks))
	byID := map[string]Bank{}
	for i, b := range banks {
		ids[i] = b.ID
		if _, ok := byID[b.ID]; !ok && b.ID != "" {
			byID[b.ID] = b
		}
	}
	paths := specItemPaths("banks", ids)
	firstID := map[string]bool{}
	names := map[string]string{}
	nips := map[string]string{}
	nubans := map[string]string{}
	for i, b := range banks {
		p := paths[i]
		switch {
		case b.ID == "":
			c.add(p+".id", "%s.id is required. %s", p, specIDRule)
		case !specIDPattern.MatchString(b.ID):
			c.add(p+".id", "%s.id %q is not valid. %s", p, b.ID, specIDRule)
		case slices.Contains(specSystemIDs, b.ID):
			c.add(p+".id", "%s.id %s is reserved for the %s. Choose another id", p, b.ID, strings.ToUpper(b.ID))
		case firstID[b.ID]:
			c.add(p+".id", "%s.id %s is already used by another bank. Give each bank its own id", p, b.ID)
		}
		firstID[b.ID] = true
		c.bankName(p, b, names)
		c.bankCodes(p, b, nips, nubans)
		c.bankRules(p, b, byID)
	}
}

func (c *specChecker) bankName(p string, b Bank, names map[string]string) {
	if b.Name == "" {
		c.add(p+".name", "%s.name is required. Add the bank's name", p)
		return
	}
	if n := utf8.RuneCountInString(b.Name); n > specMaxBankName {
		c.add(p+".name", "%s.name has %d characters. Use 1 to %d", p, n, specMaxBankName)
		return
	}
	key := strings.ToLower(b.Name)
	if other, ok := names[key]; ok {
		c.add(p+".name", "%s.name %q is already used by %s. Give each bank its own name", p, b.Name, other)
		return
	}
	names[key] = b.ID
}

func (c *specChecker) bankCodes(p string, b Bank, nips, nubans map[string]string) {
	if b.NIPCode != "" || c.s.has(p+".nip_code") {
		if !specNIPPattern.MatchString(b.NIPCode) {
			c.add(p+".nip_code", "%s.nip_code %q is not valid. Use 6 digits, or leave it out to have one allocated", p, b.NIPCode)
		} else if other, ok := nips[b.NIPCode]; ok {
			c.add(p+".nip_code", "%s.nip_code %s is already used by %s. Give each bank its own code", p, b.NIPCode, other)
		} else {
			nips[b.NIPCode] = b.ID
		}
	}
	if b.NUBANCode != "" || c.s.has(p+".nuban_code") {
		norm, err := world.NormaliseBankCode(b.NUBANCode)
		if err != nil || (len(b.NUBANCode) == 6 && b.NUBANCode[0] != '9') {
			c.add(p+".nuban_code", "%s.nuban_code %q is not valid. Use 3 or 5 digits, or 6 digits starting with 9, or leave it out to have one allocated", p, b.NUBANCode)
		} else if other, ok := nubans[norm]; ok {
			c.add(p+".nuban_code", "%s.nuban_code %s is already used by %s. Give each bank its own code", p, b.NUBANCode, other)
		} else {
			nubans[norm] = b.ID
		}
	}
}

func (c *specChecker) bankRules(p string, b Bank, byID map[string]Bank) {
	switch {
	case b.Type == "":
		c.add(p+".type", "%s.type is required. Use %s", p, specChoices(BankTypes))
	case !slices.Contains(BankTypes, b.Type):
		c.add(p+".type", "%s.type %q is not a bank type. Use %s", p, b.Type, specChoices(BankTypes))
	}
	if b.State != "" || c.s.has(p+".state") {
		if !slices.Contains(specStates, b.State) {
			c.add(p+".state", "%s.state %q is not valid. Use %s", p, b.State, specChoices(specStates))
		} else if b.State == StateClosed && b.OnRemove == nil {
			c.add(p+".state", "%s.state closed needs on_remove. Add on_remove with money: %s", p, specChoices(specMoneyChoices))
		}
	}
	if b.SettlesThrough != "" || c.s.has(p+".settles_through") {
		via, ok := byID[b.SettlesThrough]
		switch {
		case b.SettlesThrough == b.ID:
			c.add(p+".settles_through", "%s.settles_through cannot name the bank itself. Name another bank", p)
		case !ok:
			c.add(p+".settles_through", "%s.settles_through %q is not a bank in this file. Name a bank listed under banks", p, b.SettlesThrough)
		case via.Type == "microfinance":
			c.add(p+".settles_through", "%s.settles_through %s is a microfinance bank. Name a commercial or digital bank", p, b.SettlesThrough)
		case via.State == StateClosed:
			c.add(p+".settles_through", "%s.settles_through %s is closed. Name an open bank", p, b.SettlesThrough)
		}
	}
	c.positive(p+".net_debit_cap_minor", b.NetDebitCap, "Give it in kobo, or leave it out for the default")
	c.positive(p+".collateral_minor", b.Collateral, "Give it in kobo, or leave it out for the default")
	ob := b.OpenBanking
	for _, f := range []struct {
		name string
		v    int
	}{{"consent_months", ob.ConsentMonths}, {"refresh_days", ob.RefreshDays}, {"dormant_days", ob.DormantDays}, {"assertion_seconds", ob.AssertionSeconds}} {
		c.positive(p+".open_banking."+f.name, int64(f.v), "Leave it out for the default")
	}
	if b.OnRemove != nil {
		c.removal(p+".on_remove", b, byID)
	}
}

func (c *specChecker) removal(p string, b Bank, byID map[string]Bank) {
	r := b.OnRemove
	moves := false
	switch {
	case r.Money == "":
		c.add(p+".money", "%s.money is required. Use %s", p, specChoices(specMoneyChoices))
	case !slices.Contains(specMoneyChoices, r.Money):
		c.add(p+".money", "%s.money %q is not valid. Use %s", p, r.Money, specChoices(specMoneyChoices))
	default:
		moves = r.Money != MoneyLeave
	}
	customers := make([]string, 0, len(r.Customers))
	for id := range r.Customers {
		customers = append(customers, id)
	}
	slices.Sort(customers)
	for _, id := range customers {
		choice := r.Customers[id]
		if !slices.Contains(specMoneyChoices, choice) {
			c.add(p+".customers["+id+"]", "%s.customers[%s] %q is not valid. Use %s", p, id, choice, specChoices(specMoneyChoices))
			continue
		}
		moves = moves || choice != MoneyLeave
	}
	switch {
	case r.To == "" && moves:
		c.add(p+".to", "%s.to is required when money moves. Name the bank that receives it", p)
	case r.To == "":
	case !moves && r.Money == MoneyLeave:
		c.add(p+".to", "%s.to is set but no money moves. Remove to, or choose %s or %s", p, MoneyTransfer, MoneyPayout)
	case r.To == b.ID:
		c.add(p+".to", "%s.to cannot name the bank being removed. Name another bank", p)
	default:
		if _, ok := byID[r.To]; !ok {
			c.add(p+".to", "%s.to %q is not a bank in this file. Name a bank listed under banks", p, r.To)
		}
	}
}

func (c *specChecker) partners() {
	partners := c.s.Spec.Partners
	ids := make([]string, len(partners))
	for i, pt := range partners {
		ids[i] = pt.ID
	}
	paths := specItemPaths("partners", ids)
	seen := map[string]bool{}
	for i, pt := range partners {
		p := paths[i]
		switch {
		case pt.ID == "":
			c.add(p+".id", "%s.id is required. Use 1 to 40 lowercase letters, digits or hyphens", p)
		case !specPartnerPattern.MatchString(pt.ID):
			c.add(p+".id", "%s.id %q is not valid. Use 1 to 40 lowercase letters, digits or hyphens, starting with a letter or digit", p, pt.ID)
		case seen[pt.ID]:
			c.add(p+".id", "%s.id %s is already used by another partner. Give each partner its own id", p, pt.ID)
		}
		seen[pt.ID] = true
		if pt.Name == "" {
			c.add(p+".name", "%s.name is required. Add the partner's name", p)
		}
		uris := specItemPaths(p+".redirect_uris", pt.RedirectURIs)
		for j, u := range pt.RedirectURIs {
			c.webURL(uris[j], p+".redirect_uris", u)
		}
		if pt.WebhookURL != "" || c.s.has(p+".webhook_url") {
			c.webURL(p+".webhook_url", p+".webhook_url", pt.WebhookURL)
		}
	}
}

func (c *specChecker) webURL(path, name, raw string) {
	if !specWebURL(raw) {
		c.add(path, "%s %q is not an http or https URL. Use a full URL such as https://localhost:3000/callback", name, raw)
	}
}

func specWebURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func specChoices(list []string) string {
	if len(list) < 2 {
		return strings.Join(list, "")
	}
	return strings.Join(list[:len(list)-1], ", ") + " or " + list[len(list)-1]
}
