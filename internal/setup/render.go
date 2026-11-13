package setup

import (
	"fmt"
	"strconv"
	"strings"
)

var opSigns = map[string]string{
	OpCreate: "+",
	OpUpdate: "~",
	OpClose:  "-",
	OpRemove: "-",
	OpDrift:  "!",
}

var opSummary = []struct{ op, verb string }{
	{OpCreate, "create"},
	{OpUpdate, "update"},
	{OpClose, "close"},
	{OpRemove, "remove"},
	{OpDrift, "repair"},
}

var stepParts = map[string]string{
	StepAllocate:          "allocation",
	StepDirectory:         "directory row",
	StepDatabase:          "database",
	StepLedgerSchema:      "ledger schema",
	StepOpenBankingSchema: "open banking schema",
	StepCerts:             "certificates",
	StepCBN:               "CBN member",
	StepSite:              "site",
	StepAPI:               "API",
	"status":              "status record",
}

func Render(p Plan) string {
	if len(p.Actions) == 0 {
		return "No changes.\n"
	}
	nameW, opW := 0, 0
	for _, a := range p.Actions {
		nameW = max(nameW, len(label(a)))
		opW = max(opW, len(a.Op))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Setup %s\n\n", p.Setup)
	counts := map[string]int{}
	for _, a := range p.Actions {
		counts[a.Op]++
		head := fmt.Sprintf("  %s %-*s  %-*s  %s", opSigns[a.Op], nameW+4, label(a), opW+1, a.Op, summary(a))
		b.WriteString(strings.TrimRight(head, " ") + "\n")
		for _, l := range details(a) {
			b.WriteString("        " + l + "\n")
		}
		for _, bl := range a.Blockers {
			b.WriteString("        blocked: " + bl + "\n")
		}
	}
	var parts []string
	for _, s := range opSummary {
		if n := counts[s.op]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d to %s", n, s.verb))
		}
	}
	fmt.Fprintf(&b, "\nPlan: %s.\n", strings.Join(parts, ", "))
	return b.String()
}

func label(a Action) string {
	return strings.ToLower(a.Kind) + " " + a.ID
}

func summary(a Action) string {
	switch a.Op {
	case OpCreate:
		return createSummary(a)
	case OpUpdate:
		return changes(a.Fields, nil)
	case OpClose:
		if c := changes(a.Fields, closeShown); c != "" {
			return "DESTRUCTIVE; " + c
		}
		return "DESTRUCTIVE"
	case OpRemove:
		return "DESTRUCTIVE"
	case OpDrift:
		return driftSummary(a)
	}
	return ""
}

func createSummary(a Action) string {
	allocated := map[string]bool{}
	for _, f := range a.Allocated {
		allocated[f] = true
	}
	var parts []string
	for _, f := range a.Fields {
		var s string
		switch f.Path {
		case "type", "name":
			s = f.To
		case fieldNIPCode:
			s = "NIP " + f.To
		case fieldNUBANCode:
			s = "NUBAN " + f.To
		case "settles_through":
			s = "settles through " + f.To
		default:
			s = f.Path + " " + f.To
		}
		if allocated[f.Path] {
			s += " (allocated)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func driftSummary(a Action) string {
	if len(a.Fields) == 0 {
		return "being repaired"
	}
	names := make([]string, 0, len(a.Fields))
	for _, f := range a.Fields {
		step := strings.TrimPrefix(f.Path, "steps.")
		names = append(names, orString(stepParts[step], step))
	}
	pronoun := "it"
	if len(names) > 1 {
		pronoun = "they"
	}
	return fmt.Sprintf("%s missing; %s will be rebuilt", list(names), pronoun)
}

var closeShown = map[string]bool{"state": true, "on_remove.money": true, "on_remove.to": true}

func changes(fields []FieldChange, skip map[string]bool) string {
	var parts []string
	for _, f := range fields {
		if !skip[f.Path] {
			parts = append(parts, change(f))
		}
	}
	return strings.Join(parts, "; ")
}

func change(f FieldChange) string {
	switch {
	case f.From == "":
		return f.Path + " +" + value(f.Path, f.To)
	case f.To == "":
		return f.Path + " -" + value(f.Path, f.From)
	}
	return fmt.Sprintf("%s %s → %s", strings.TrimSuffix(f.Path, "_minor"), value(f.Path, f.From), value(f.Path, f.To))
}

func value(path, v string) string {
	numeric := strings.HasSuffix(path, "_minor") || strings.HasPrefix(path, "open_banking.")
	if numeric && v == "0" {
		return "default"
	}
	if strings.HasSuffix(path, "_minor") {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return amount(n)
		}
	}
	return v
}

func details(a Action) []string {
	if a.Impact == nil {
		return nil
	}
	im := a.Impact
	if a.Kind == KindPartner {
		var first []string
		if im.Connections > 0 {
			first = append(first, "revoke "+count(im.Connections, "connection", "connections"))
		}
		first = append(first, "drop its keys and live rows")
		return []string{strings.Join(first, "; ") + "."}
	}
	first := []string{"stop routing"}
	if im.Connections > 0 {
		first = append(first, "revoke "+count(im.Connections, "connection", "connections"))
	}
	if im.Transfers > 0 {
		first = append(first, fmt.Sprintf("settle %s (net %s)", count(im.Transfers, "open transfer", "open transfers"), naira(im.NetMinor)))
	}
	groups := [][]string{first}
	if m := money(a); len(m) > 0 {
		groups = append(groups, m)
	}
	var last string
	if a.Op == OpRemove {
		var drop []string
		if n := len(im.Databases); n > 0 {
			drop = append(drop, count(n, "database", "databases"))
		}
		if len(drop) > 0 {
			groups = append(groups, []string{"drop " + list(drop)})
		}
		last = "Its ID and codes stay reserved."
	} else {
		last = "Its database and history stay."
	}
	lines := make([]string, len(groups))
	for i, g := range groups {
		end := ";"
		if i == len(groups)-1 {
			end = ". " + last
		}
		lines[i] = strings.Join(g, "; ") + end
	}
	return lines
}

func money(a Action) []string {
	im := a.Impact
	if im.MoneyMinor == 0 {
		return nil
	}
	var choice, to string
	for _, f := range a.Fields {
		switch f.Path {
		case "on_remove.money":
			choice = f.To
		case "on_remove.to":
			to = f.To
		}
	}
	held := fmt.Sprintf("%s for %s", naira(im.MoneyMinor), count(im.Customers, "customer", "customers"))
	switch choice {
	case MoneyTransfer:
		return []string{fmt.Sprintf("transfer %s to %s (on_remove)", held, to), "settle the moves"}
	case MoneyPayout:
		return []string{fmt.Sprintf("pay out the insured part of %s in cash (on_remove)", held)}
	case MoneyLeave:
		return []string{fmt.Sprintf("leave %s on frozen accounts (on_remove)", held)}
	}
	return []string{fmt.Sprintf("%s needs a money decision: add on_remove or run with --money", held)}
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func list(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func naira(minor int64) string {
	if minor < 0 {
		return "-₦" + amount(minor)[1:]
	}
	return "₦" + amount(minor)
}

func amount(minor int64) string {
	sign := ""
	u := uint64(minor)
	if minor < 0 {
		sign, u = "-", uint64(-(minor+1))+1
	}
	whole := strconv.FormatUint(u/100, 10)
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return fmt.Sprintf("%s%s.%02d", sign, b.String(), u%100)
}
