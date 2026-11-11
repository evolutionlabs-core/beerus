package setup

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var (
	ErrSetupMismatch = errors.New("setup mismatch")
	ErrBeingRemoved  = errors.New("being removed")
)

const (
	stepDone = "done"

	ownedDatabase = "database"
	ownedSnapshot = "snapshot"

	fieldNIPCode   = "nip_code"
	fieldNUBANCode = "nuban_code"
)

type codeRange struct {
	field       string
	first, last int
}

type planError struct {
	msg  string
	kind error
}

func (e planError) Error() string { return e.msg }

func (e planError) Unwrap() error { return e.kind }

type codes struct {
	nip, nuban string
	allocated  []string
}

type planner struct {
	desired    Spec
	st         Status
	today      clock.Date
	hasApplied bool
	applied    map[string]Bank
	wanted     map[string]Bank
	resources  map[string]Resource
	tombstones []nibss.Institution
	members    []nibss.Institution
	errs       []error
	nipCodes   codeRange
	nubanCodes codeRange
}

func Build(desired Spec, st Status, dir []nibss.Institution, today clock.Date) Plan {
	pack, err := countrypack.Nigeria()
	if err != nil {
		return Plan{Setup: desired.Metadata.Name, Errors: []error{fmt.Errorf("load simulator code ranges: %w", err)}}
	}
	p := newPlanner(desired, st, dir, today)
	c := pack.SimulatorCodes
	p.nipCodes = codeRange{field: fieldNIPCode, first: c.NIP.First, last: c.NIP.Last}
	p.nubanCodes = codeRange{field: fieldNUBANCode, first: c.NUBAN.First, last: c.NUBAN.Last}
	var actions []Action
	if a, ok := p.setupAction(); ok {
		actions = append(actions, a)
	}
	allocated := p.allocate()
	for _, b := range p.order(desired.Spec.Banks) {
		if a, ok := p.bankAction(b, allocated[b.ID]); ok {
			actions = append(actions, a)
		}
	}
	actions = append(actions, p.partnerActions()...)
	actions = append(actions, p.systemDrift()...)
	actions = append(actions, p.partnerRemovals()...)
	actions = append(actions, p.bankRemovals()...)
	if len(p.errs) > 0 {
		return Plan{Setup: desired.Metadata.Name, Errors: p.errs}
	}
	return Plan{Setup: desired.Metadata.Name, Actions: actions}
}

func newPlanner(desired Spec, st Status, dir []nibss.Institution, today clock.Date) *planner {
	p := &planner{
		desired:    desired,
		st:         st,
		today:      today,
		hasApplied: st.Applied.Metadata.Name != "",
		applied:    map[string]Bank{},
		wanted:     map[string]Bank{},
		resources:  map[string]Resource{},
	}
	for _, b := range st.Applied.Spec.Banks {
		p.applied[b.ID] = b
	}
	for _, b := range desired.Spec.Banks {
		p.wanted[b.ID] = b
	}
	for _, r := range st.Resources {
		p.resources[r.Kind+"/"+r.ID] = r
	}
	rows := append([]nibss.Institution(nil), dir...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].NIPCode < rows[j].NIPCode })
	for _, in := range rows {
		if in.Status == nibss.StatusRemoved {
			p.tombstones = append(p.tombstones, in)
		} else {
			p.members = append(p.members, in)
		}
	}
	return p
}

func (p *planner) fail(path string, kind error, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if pos, ok := p.desired.Pos(path); ok {
		msg = fmt.Sprintf("%s:%d:%d: %s", pos.File, pos.Line, pos.Col, msg)
	}
	p.errs = append(p.errs, planError{msg: msg, kind: kind})
}

func (p *planner) resource(kind, id string) (Resource, bool) {
	r, ok := p.resources[kind+"/"+id]
	return r, ok
}

func (p *planner) setupAction() (Action, bool) {
	d, a := p.desired, p.st.Applied
	if p.st.Setup != "" && d.Metadata.Name != p.st.Setup {
		p.fail("metadata.name", ErrSetupMismatch, "metadata.name is %s, but this dir holds setup %s. Use the file for %s, or another dir.", d.Metadata.Name, p.st.Setup, p.st.Setup)
		return Action{}, false
	}
	if !p.hasApplied {
		return Action{}, false
	}
	p.locked("apiVersion", a.APIVersion, d.APIVersion)
	p.locked("kind", a.Kind, d.Kind)
	def := DefaultSpec(d.Metadata.Name).Spec
	p.locked("nip.sessions", strconv.Itoa(orInt(a.Spec.NIP.Sessions, def.NIP.Sessions)), strconv.Itoa(orInt(d.Spec.NIP.Sessions, def.NIP.Sessions)))
	var fields []FieldChange
	add := func(path, from, to string) {
		if from != to {
			fields = append(fields, FieldChange{Path: path, From: from, To: to})
		}
	}
	add("nip.tsq_after", orString(a.Spec.NIP.TSQAfter, def.NIP.TSQAfter), orString(d.Spec.NIP.TSQAfter, def.NIP.TSQAfter))
	add("nip.tsq_attempts", strconv.Itoa(orInt(a.Spec.NIP.TSQAttempts, def.NIP.TSQAttempts)), strconv.Itoa(orInt(d.Spec.NIP.TSQAttempts, def.NIP.TSQAttempts)))
	add("connect.legal_name", orString(a.Spec.Connect.LegalName, def.Connect.LegalName), orString(d.Spec.Connect.LegalName, def.Connect.LegalName))
	add("connect.brand_name", orString(a.Spec.Connect.BrandName, def.Connect.BrandName), orString(d.Spec.Connect.BrandName, def.Connect.BrandName))
	add("connect.registry_id", orString(a.Spec.Connect.RegistryID, def.Connect.RegistryID), orString(d.Spec.Connect.RegistryID, def.Connect.RegistryID))
	add("connect.cac_number", orString(a.Spec.Connect.CACNumber, def.Connect.CACNumber), orString(d.Spec.Connect.CACNumber, def.Connect.CACNumber))
	fields = append(fields, p.holidayChanges("holidays.add", a.Spec.Holidays.Add, d.Spec.Holidays.Add)...)
	fields = append(fields, p.holidayChanges("holidays.remove", a.Spec.Holidays.Remove, d.Spec.Holidays.Remove)...)
	if len(fields) == 0 {
		return Action{}, false
	}
	return Action{Op: OpUpdate, Kind: KindSetup, ID: d.Metadata.Name, Fields: fields}, true
}

func (p *planner) locked(path, from, to string) {
	if from == to {
		return
	}
	p.fail(path, nil, "%s cannot change from %s to %s. Put back %s.", path, shown(from), shown(to), shown(from))
}

func (p *planner) holidayChanges(list string, applied, desired []string) []FieldChange {
	was, now := set(applied), set(desired)
	var out []FieldChange
	for _, d := range sortedKeys(now) {
		if !was[d] && p.futureHoliday(list, d) {
			out = append(out, FieldChange{Path: list, To: d})
		}
	}
	for _, d := range sortedKeys(was) {
		if !now[d] && p.futureHoliday(list, d) {
			out = append(out, FieldChange{Path: list, From: d})
		}
	}
	return out
}

func (p *planner) futureHoliday(list, raw string) bool {
	path := list + "[" + raw + "]"
	d, err := clock.ParseDate(raw)
	if err != nil {
		p.fail(path, nil, "%s %q is not a date. Write it as YYYY-MM-DD.", list, raw)
		return false
	}
	switch {
	case d == p.today:
		p.fail(path, nil, "%s %s is today and cannot change.", list, raw)
		return false
	case d.Before(p.today):
		p.fail(path, nil, "%s %s is in the past and cannot change.", list, raw)
		return false
	}
	return true
}

func (p *planner) appliedCodes(b Bank) (string, string) {
	r, _ := p.resource(KindBank, b.ID)
	return orString(b.NIPCode, r.Allocated.NIPCode), orString(b.NUBANCode, r.Allocated.NUBANCode)
}

func (p *planner) allocate() map[string]codes {
	usedNIP, usedNUBAN := map[string]string{}, map[string]string{}
	markNUBAN := func(code, owner string) {
		if n, err := world.NormaliseBankCode(code); err == nil && code != "" {
			usedNUBAN[n] = owner
		}
	}
	for _, in := range p.members {
		usedNIP[in.NIPCode] = in.ID
		markNUBAN(in.NUBANCode, in.ID)
	}
	for _, in := range p.tombstones {
		usedNIP[in.NIPCode] = "removed bank " + in.ID
		markNUBAN(in.NUBANCode, "removed bank "+in.ID)
	}
	for _, b := range p.st.Applied.Spec.Banks {
		nip, nuban := p.appliedCodes(b)
		if nip != "" {
			usedNIP[nip] = b.ID
		}
		markNUBAN(nuban, b.ID)
	}
	out := map[string]codes{}
	for _, b := range p.desired.Spec.Banks {
		if _, ok := p.applied[b.ID]; ok {
			continue
		}
		c := codes{nip: b.NIPCode, nuban: b.NUBANCode}
		if c.nip != "" {
			if owner, ok := usedNIP[c.nip]; ok && owner != b.ID {
				p.fail("banks["+b.ID+"].nip_code", nil, "banks[%s].nip_code %s is already used by %s. Choose another code, or leave it out to have one allocated.", b.ID, c.nip, owner)
			}
			usedNIP[c.nip] = b.ID
		}
		if c.nuban != "" {
			n, err := world.NormaliseBankCode(c.nuban)
			if err != nil {
				p.fail("banks["+b.ID+"].nuban_code", nil, "banks[%s].nuban_code %s must have 3, 5 or 6 digits.", b.ID, c.nuban)
			} else if owner, ok := usedNUBAN[n]; ok && owner != b.ID {
				p.fail("banks["+b.ID+"].nuban_code", nil, "banks[%s].nuban_code %s is already used by %s. Choose another code, or leave it out to have one allocated.", b.ID, c.nuban, owner)
			}
			markNUBAN(c.nuban, b.ID)
		}
		out[b.ID] = c
	}
	for _, b := range p.desired.Spec.Banks {
		c, ok := out[b.ID]
		if !ok {
			continue
		}
		if c.nip == "" {
			code, ok := p.nipCodes.next(func(s string) bool { _, used := usedNIP[s]; return used })
			if !ok {
				p.full(b.ID, p.nipCodes)
			} else {
				c.nip = code
				c.allocated = append(c.allocated, fieldNIPCode)
				usedNIP[code] = b.ID
			}
		}
		if c.nuban == "" {
			code, ok := p.nubanCodes.next(func(s string) bool {
				n, _ := world.NormaliseBankCode(s)
				_, used := usedNUBAN[n]
				return used
			})
			if !ok {
				p.full(b.ID, p.nubanCodes)
			} else {
				c.nuban = code
				c.allocated = append(c.allocated, fieldNUBANCode)
				markNUBAN(code, b.ID)
			}
		}
		out[b.ID] = c
	}
	return out
}

func (r codeRange) next(used func(string) bool) (string, bool) {
	for n := r.first; n <= r.last; n++ {
		if s := strconv.Itoa(n); !used(s) {
			return s, true
		}
	}
	return "", false
}

func (p *planner) full(id string, r codeRange) {
	path := "banks[" + id + "]." + r.field
	p.fail(path, nil, "%s cannot be allocated: all %d codes from %d to %d are taken. Set %s by hand, or remove a bank.", path, r.last-r.first+1, r.first, r.last, r.field)
}

func (p *planner) order(banks []Bank) []Bank {
	byID := map[string]Bank{}
	for _, b := range banks {
		byID[b.ID] = b
	}
	state := map[string]int{}
	var out []Bank
	var visit func(b Bank, chain []string) bool
	visit = func(b Bank, chain []string) bool {
		switch state[b.ID] {
		case 1:
			loop := append(chain, b.ID)
			p.fail("banks["+b.ID+"].settles_through", nil, "banks[%s].settles_through forms a loop: %s. Point one of them at a bank that settles directly.", b.ID, strings.Join(loop, " → "))
			return false
		case 2:
			return true
		}
		state[b.ID] = 1
		if via, ok := byID[b.SettlesThrough]; ok {
			if !visit(via, append(chain, b.ID)) {
				state[b.ID] = 2
				return false
			}
		}
		state[b.ID] = 2
		out = append(out, b)
		return true
	}
	for _, b := range banks {
		if state[b.ID] == 0 {
			visit(b, nil)
		}
	}
	return out
}

func (p *planner) bankAction(b Bank, c codes) (Action, bool) {
	r, hasRes := p.resource(KindBank, b.ID)
	if hasRes && r.Phase == PhaseTerminating {
		p.fail("banks["+b.ID+"].id", ErrBeingRemoved, "banks[%s] is being removed and cannot be added back until removal finishes. Take it out of the file, or wait.", b.ID)
		return Action{}, false
	}
	old, existing := p.applied[b.ID]
	p.checkReserved(b, existing && old.Name == b.Name)
	if !existing {
		return p.createAction(b, c)
	}
	return p.updateAction(old, b, r, hasRes)
}

func (p *planner) checkReserved(b Bank, nameKept bool) {
	for _, t := range p.tombstones {
		if t.ID == b.ID {
			p.fail("banks["+b.ID+"].id", nil, "banks[%s].id %s belonged to a removed bank and stays reserved. Choose another id.", b.ID, b.ID)
		}
		if !nameKept && strings.EqualFold(t.Name, b.Name) {
			p.fail("banks["+b.ID+"].name", nil, "banks[%s].name %q belonged to removed bank %s and stays reserved. Choose another name.", b.ID, b.Name, t.ID)
		}
	}
	if nameKept {
		return
	}
	for _, a := range p.st.Applied.Spec.Banks {
		if _, staying := p.wanted[a.ID]; !staying && strings.EqualFold(a.Name, b.Name) {
			p.fail("banks["+b.ID+"].name", nil, "banks[%s].name %q belongs to %s, which is being removed, and stays reserved. Choose another name.", b.ID, b.Name, a.ID)
		}
	}
}

func (p *planner) createAction(b Bank, c codes) (Action, bool) {
	state := orString(b.State, StateActive)
	if state == StateClosed {
		p.fail("banks["+b.ID+"].state", nil, "banks[%s].state cannot be closed for a new bank. Use active or suspended.", b.ID)
		return Action{}, false
	}
	fields := []FieldChange{
		{Path: "type", To: b.Type},
		{Path: fieldNIPCode, To: c.nip},
		{Path: fieldNUBANCode, To: c.nuban},
	}
	if b.SettlesThrough != "" {
		fields = append(fields, FieldChange{Path: "settles_through", To: b.SettlesThrough})
	}
	if state != StateActive {
		fields = append(fields, FieldChange{Path: "state", To: state})
	}
	return Action{Op: OpCreate, Kind: KindBank, ID: b.ID, Fields: fields, Allocated: c.allocated}, true
}

func (p *planner) updateAction(old, b Bank, r Resource, hasRes bool) (Action, bool) {
	path := func(f string) string { return "banks[" + b.ID + "]." + f }
	p.locked(path("type"), old.Type, b.Type)
	nip, nuban := p.appliedCodes(old)
	if b.NIPCode != "" && b.NIPCode != nip {
		p.locked(path("nip_code"), nip, b.NIPCode)
	}
	if b.NUBANCode != "" && !sameNUBAN(b.NUBANCode, nuban) {
		p.locked(path("nuban_code"), nuban, b.NUBANCode)
	}
	p.locked(path("settles_through"), old.SettlesThrough, b.SettlesThrough)
	from, to := orString(old.State, StateActive), orString(b.State, StateActive)
	if from == StateClosed && to != StateClosed {
		p.fail(path("state"), nil, "%s cannot change from closed to %s. A closed bank stays closed; remove it from the file instead.", path("state"), to)
		return Action{}, false
	}
	var fields []FieldChange
	add := func(f, a, z string) {
		if a != z {
			fields = append(fields, FieldChange{Path: f, From: a, To: z})
		}
	}
	add("name", old.Name, b.Name)
	add("state", from, to)
	add("net_debit_cap_minor", i64(old.NetDebitCap), i64(b.NetDebitCap))
	add("collateral_minor", i64(old.Collateral), i64(b.Collateral))
	add("open_banking.consent_months", strconv.Itoa(old.OpenBanking.ConsentMonths), strconv.Itoa(b.OpenBanking.ConsentMonths))
	add("open_banking.refresh_days", strconv.Itoa(old.OpenBanking.RefreshDays), strconv.Itoa(b.OpenBanking.RefreshDays))
	add("open_banking.dormant_days", strconv.Itoa(old.OpenBanking.DormantDays), strconv.Itoa(b.OpenBanking.DormantDays))
	add("open_banking.assertion_seconds", strconv.Itoa(old.OpenBanking.AssertionSeconds), strconv.Itoa(b.OpenBanking.AssertionSeconds))
	add("on_remove", removal(old.OnRemove), removal(b.OnRemove))
	closing := from != StateClosed && to == StateClosed
	if to == StateClosed {
		p.checkTarget(b.ID, b.OnRemove, path("on_remove"))
	}
	if closing {
		a := Action{Op: OpClose, Kind: KindBank, ID: b.ID, Fields: fields, Destructive: true, Impact: &Impact{}}
		a.Fields = append(a.Fields, p.moneyFields(b.OnRemove)...)
		return a, true
	}
	if len(fields) > 0 {
		return Action{Op: OpUpdate, Kind: KindBank, ID: b.ID, Fields: fields}, true
	}
	if !hasRes {
		return Action{Op: OpDrift, Kind: KindBank, ID: b.ID, Fields: []FieldChange{{Path: "status", From: stepDone, To: "missing"}}}, true
	}
	return driftAction(r)
}

func driftAction(r Resource) (Action, bool) {
	switch r.Phase {
	case PhaseActive, PhaseSuspended, PhaseRepairing:
	default:
		return Action{}, false
	}
	var fields []FieldChange
	for _, s := range []string{StepAllocate, StepDirectory, StepDatabase, StepCerts, StepCBN, StepSite, StepAPI} {
		if v, ok := r.Steps[s]; ok && v != stepDone {
			fields = append(fields, FieldChange{Path: "steps." + s, From: stepDone, To: v})
		}
	}
	if len(fields) == 0 && r.Phase != PhaseRepairing {
		return Action{}, false
	}
	return Action{Op: OpDrift, Kind: r.Kind, ID: r.ID, Fields: fields}, true
}

func (p *planner) checkTarget(id string, rm *Removal, path string) {
	if rm == nil {
		return
	}
	needsTarget := rm.Money == MoneyTransfer
	for _, choice := range rm.Customers {
		if choice == MoneyTransfer {
			needsTarget = true
		}
	}
	if !needsTarget || rm.To == "" {
		return
	}
	if rm.To == id {
		p.fail(path, nil, "%s sends money to %s itself. Choose another bank.", path, id)
		return
	}
	t, ok := p.wanted[rm.To]
	if !ok {
		if _, was := p.applied[rm.To]; was {
			p.fail(path, nil, "%s sends money to %s, which is leaving too. Choose a bank that stays.", path, rm.To)
			return
		}
		p.fail(path, nil, "%s sends money to %s, which is not in the file. Choose a bank in the file.", path, rm.To)
		return
	}
	if r, ok := p.resource(KindBank, rm.To); ok && (r.Phase == PhaseTerminating || r.Phase == PhaseClosing || r.Phase == PhaseClosed) {
		p.fail(path, nil, "%s sends money to %s, which is closing or closed. Choose a bank that stays open.", path, rm.To)
		return
	}
	if t.State == StateClosed {
		p.fail(path, nil, "%s sends money to %s, which is closing too. Choose a bank that stays open.", path, rm.To)
	}
}

func (p *planner) moneyFields(rm *Removal) []FieldChange {
	if rm == nil {
		return nil
	}
	out := []FieldChange{{Path: "on_remove.money", To: rm.Money}}
	if rm.To != "" {
		name := rm.To
		if t, ok := p.wanted[rm.To]; ok {
			name = t.Name
		}
		out = append(out, FieldChange{Path: "on_remove.to", To: name})
	}
	return out
}

func (p *planner) bankRemovals() []Action {
	leaving := map[string]bool{}
	var gone []Bank
	for _, b := range p.st.Applied.Spec.Banks {
		if _, ok := p.wanted[b.ID]; !ok {
			leaving[b.ID] = true
			gone = append(gone, b)
		}
	}
	var out []Action
	ordered := p.order(gone)
	for i := len(ordered) - 1; i >= 0; i-- {
		b := ordered[i]
		r, hasRes := p.resource(KindBank, b.ID)
		if hasRes && r.Phase == PhaseTerminating {
			continue
		}
		p.checkTarget(b.ID, b.OnRemove, "on_remove of removed bank "+b.ID)
		a := Action{Op: OpRemove, Kind: KindBank, ID: b.ID, Destructive: true, Fields: p.moneyFields(b.OnRemove), Impact: ownedImpact(r)}
		if len(p.desired.Spec.Banks) == 0 {
			a.Blockers = append(a.Blockers, fmt.Sprintf("%s is the last bank. Keep at least one bank in the file.", b.ID))
		}
		for _, s := range p.desired.Spec.Banks {
			if s.SettlesThrough == b.ID {
				a.Blockers = append(a.Blockers, fmt.Sprintf("%s settles through %s and is not leaving. Remove %s too, or keep %s.", s.ID, b.ID, s.ID, b.ID))
			}
		}
		out = append(out, a)
	}
	return out
}

func ownedImpact(r Resource) *Impact {
	im := &Impact{}
	for _, o := range r.Owned {
		switch o.Kind {
		case ownedDatabase:
			im.Databases = append(im.Databases, o.Name)
		case ownedSnapshot:
			im.Snapshots = append(im.Snapshots, o.Name)
		}
	}
	return im
}

func (p *planner) partnerActions() []Action {
	applied := map[string]Partner{}
	for _, x := range p.st.Applied.Spec.Partners {
		applied[x.ID] = x
	}
	var out []Action
	for _, x := range p.desired.Spec.Partners {
		r, hasRes := p.resource(KindPartner, x.ID)
		if hasRes && r.Phase == PhaseTerminating {
			p.fail("partners["+x.ID+"].id", ErrBeingRemoved, "partners[%s] is being removed and cannot be added back until removal finishes. Take it out of the file, or wait.", x.ID)
			continue
		}
		old, ok := applied[x.ID]
		if !ok {
			out = append(out, Action{Op: OpCreate, Kind: KindPartner, ID: x.ID, Fields: []FieldChange{{Path: "name", To: x.Name}}})
			continue
		}
		var fields []FieldChange
		add := func(f, a, z string) {
			if a != z {
				fields = append(fields, FieldChange{Path: f, From: a, To: z})
			}
		}
		add("name", old.Name, x.Name)
		add("legal_name", old.LegalName, x.LegalName)
		add("purpose", old.Purpose, x.Purpose)
		add("redirect_uris", strings.Join(old.RedirectURIs, ", "), strings.Join(x.RedirectURIs, ", "))
		add("webhook_url", old.WebhookURL, x.WebhookURL)
		if len(fields) > 0 {
			out = append(out, Action{Op: OpUpdate, Kind: KindPartner, ID: x.ID, Fields: fields})
			continue
		}
		if hasRes {
			if a, ok := driftAction(r); ok {
				out = append(out, a)
			}
		}
	}
	return out
}

func (p *planner) partnerRemovals() []Action {
	wanted := map[string]bool{}
	for _, x := range p.desired.Spec.Partners {
		wanted[x.ID] = true
	}
	var out []Action
	ps := p.st.Applied.Spec.Partners
	for i := len(ps) - 1; i >= 0; i-- {
		x := ps[i]
		if wanted[x.ID] {
			continue
		}
		if r, ok := p.resource(KindPartner, x.ID); ok && r.Phase == PhaseTerminating {
			continue
		}
		out = append(out, Action{Op: OpRemove, Kind: KindPartner, ID: x.ID, Destructive: true, Impact: &Impact{}})
	}
	return out
}

func (p *planner) systemDrift() []Action {
	var out []Action
	for _, r := range p.st.Resources {
		if r.Kind != KindSystem {
			continue
		}
		if a, ok := driftAction(r); ok {
			out = append(out, a)
		}
	}
	return out
}

func removal(rm *Removal) string {
	if rm == nil {
		return ""
	}
	s := rm.Money
	if rm.To != "" {
		s += " to " + rm.To
	}
	if len(rm.Customers) > 0 {
		ids := make([]string, 0, len(rm.Customers))
		for id := range rm.Customers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = id + "=" + rm.Customers[id]
		}
		s += " (customers " + strings.Join(parts, ", ") + ")"
	}
	return s
}

func sameNUBAN(a, b string) bool {
	na, errA := world.NormaliseBankCode(a)
	nb, errB := world.NormaliseBankCode(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return na == nb
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func i64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func shown(v string) string {
	if v == "" {
		return "nothing"
	}
	return v
}
