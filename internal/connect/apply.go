package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	codeInvalidSpec    = "invalid_spec"
	codeApplyRunning   = "apply_running"
	codePruneRequired  = "prune_required"
	codeSetupMismatch  = "setup_mismatch"
	codeBeingRemoved   = "being_removed"
	codeNotReady       = "not_ready"
	partnerKeyFile     = "api-key"
	fieldNIPCode       = "nip_code"
	fieldNUBANCode     = "nuban_code"
	resourceSettleWait = 15 * time.Second
	resourcePollEvery  = 50 * time.Millisecond
)

type applyError struct {
	status  int
	code    string
	message string
}

func (e *applyError) Error() string { return e.message }

func applyFail(status int, code, format string, args ...any) error {
	return &applyError{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

var errApplyRunning = applyFail(http.StatusConflict, codeApplyRunning, "Another apply is running. Try again when it finishes.")

func planFailure(errs []error) error {
	msgs := make([]string, len(errs))
	status, code := http.StatusUnprocessableEntity, codeInvalidSpec
	for i, e := range errs {
		msgs[i] = e.Error()
		switch {
		case errors.Is(e, setup.ErrSetupMismatch):
			status, code = http.StatusConflict, codeSetupMismatch
		case errors.Is(e, setup.ErrBeingRemoved) && code == codeInvalidSpec:
			status, code = http.StatusConflict, codeBeingRemoved
		}
	}
	return &applyError{status: status, code: code, message: strings.Join(msgs, "\n")}
}

type planned struct {
	plan    setup.Plan
	desired setup.Spec
	st      setup.Status
}

type admission struct {
	pl       planned
	money    *setup.Removal
	retarget map[string]bool
	dir      string
	now      time.Time
	ports    map[string][2]int
	partners map[string]*config.Partner
}

func (a *App) Plan(desired setup.Spec) (setup.Plan, error) {
	st, err := a.ctl.status()
	if err != nil {
		return setup.Plan{}, err
	}
	pl, err := a.buildPlan(desired, st, ApplyOptions{})
	return pl.plan, err
}

func (a *App) Apply(desired setup.Spec, opt ApplyOptions) (setup.Plan, error) {
	if !a.applyMu.TryLock() {
		return setup.Plan{}, errApplyRunning
	}
	defer a.applyMu.Unlock()
	return a.applyLocked(func(setup.Status) (setup.Spec, error) { return desired, nil }, opt)
}

func (a *App) applyEdit(edit func(*setup.Spec) error, opt ApplyOptions) error {
	if !a.applyMu.TryLock() {
		return errApplyRunning
	}
	defer a.applyMu.Unlock()
	_, err := a.applyLocked(func(st setup.Status) (setup.Spec, error) {
		cp, err := cloneStatus(st)
		if err != nil {
			return setup.Spec{}, err
		}
		spec := cp.Applied
		return spec, edit(&spec)
	}, opt)
	return err
}

func (a *App) applyLocked(next func(setup.Status) (setup.Spec, error), opt ApplyOptions) (setup.Plan, error) {
	st, err := a.ctl.status()
	if err != nil {
		return setup.Plan{}, err
	}
	desired, err := next(st)
	if err != nil {
		return setup.Plan{}, err
	}
	pl, err := a.buildPlan(desired, st, opt)
	if err != nil {
		return setup.Plan{}, err
	}
	if err := admissible(pl.plan, opt); err != nil {
		return pl.plan, err
	}
	retarget := retargeted(pl.st, opt.Money)
	if !changes(pl.plan) && len(retarget) == 0 {
		return pl.plan, nil
	}
	ad, err := a.prepare(pl, opt.Money, retarget)
	if err != nil {
		return pl.plan, err
	}
	if err := a.ctl.update(func(st *setup.Status) (bool, error) { return admit(st, ad) }); err != nil {
		return pl.plan, errors.Join(fmt.Errorf("admit the plan: %w", err), a.discardPartners(ad))
	}
	a.startAdmitted(ad)
	a.poke()
	return pl.plan, nil
}

func changes(p setup.Plan) bool {
	return slices.ContainsFunc(p.Actions, func(act setup.Action) bool { return act.Op != setup.OpDrift })
}

func admissible(p setup.Plan, opt ApplyOptions) error {
	var blockers, removes []string
	for _, act := range p.Actions {
		blockers = append(blockers, act.Blockers...)
		if act.Op == setup.OpRemove && !opt.Prune && !slices.Contains(opt.Confirmed, act.ID) {
			removes = append(removes, act.ID)
		}
	}
	if len(blockers) > 0 {
		return applyFail(http.StatusUnprocessableEntity, codeInvalidSpec, "%s", strings.Join(blockers, "\n"))
	}
	if len(removes) > 0 {
		pronoun := "it"
		if len(removes) > 1 {
			pronoun = "them"
		}
		return applyFail(http.StatusConflict, codePruneRequired, "The plan removes %s. Nothing was changed. Run again with --prune to remove %s.", andList(removes), pronoun)
	}
	return nil
}

func andList(xs []string) string {
	if len(xs) < 2 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func moneyOpen(st *setup.Status, id string) bool {
	r := findResource(st, setup.KindBank, id)
	return r == nil || (r.Phase != setup.PhaseClosed && !slices.Contains(r.Done, setup.FinalizerDecideMoney))
}

func withMoney(desired setup.Spec, st *setup.Status, rm *setup.Removal) setup.Spec {
	if rm == nil {
		return desired
	}
	stays := map[string]bool{}
	desired.Spec.Banks = slices.Clone(desired.Spec.Banks)
	for i := range desired.Spec.Banks {
		b := &desired.Spec.Banks[i]
		stays[b.ID] = true
		if b.State == setup.StateClosed && moneyOpen(st, b.ID) {
			b.OnRemove = rm
		}
	}
	for i := range st.Applied.Spec.Banks {
		if b := &st.Applied.Spec.Banks[i]; !stays[b.ID] && moneyOpen(st, b.ID) {
			b.OnRemove = rm
		}
	}
	return desired
}

func retargeted(st setup.Status, rm *setup.Removal) map[string]bool {
	out := map[string]bool{}
	if rm == nil {
		return out
	}
	for _, r := range st.Resources {
		if r.Kind == setup.KindBank && (r.Phase == setup.PhaseClosing || r.Phase == setup.PhaseTerminating) && moneyOpen(&st, r.ID) && !reflect.DeepEqual(r.Money, rm) {
			out[r.ID] = true
		}
	}
	return out
}

func bankIn(spec setup.Spec, id string) (setup.Bank, bool) {
	for _, b := range spec.Spec.Banks {
		if b.ID == id {
			return b, true
		}
	}
	return setup.Bank{}, false
}

func partnerIn(spec setup.Spec, id string) (setup.Partner, bool) {
	for _, p := range spec.Spec.Partners {
		if p.ID == id {
			return p, true
		}
	}
	return setup.Partner{}, false
}

func (a *App) buildPlan(desired setup.Spec, st setup.Status, opt ApplyOptions) (planned, error) {
	desired = withMoney(desired, &st, opt.Money)
	if errs := setup.Validate(desired); len(errs) > 0 {
		return planned{}, planFailure(errs)
	}
	sw := a.nibssStore()
	if sw == nil {
		return planned{}, applyFail(http.StatusServiceUnavailable, codeNotReady, "The NIBSS directory is not open yet, so removed banks cannot be checked. Try again soon.")
	}
	dir, err := sw.Directory(context.Background())
	if err != nil {
		return planned{}, fmt.Errorf("read the NIBSS directory: %w", err)
	}
	plan := setup.Build(desired, st, dir, a.clock.Today())
	if len(plan.Errors) > 0 {
		return planned{}, planFailure(plan.Errors)
	}
	pl := planned{plan: plan, desired: desired, st: st}
	if err := a.fillImpact(&pl); err != nil {
		return planned{}, err
	}
	return pl, nil
}

func (a *App) fillImpact(pl *planned) error {
	for i := range pl.plan.Actions {
		act := &pl.plan.Actions[i]
		if act.Impact == nil {
			continue
		}
		var err error
		switch act.Kind {
		case setup.KindBank:
			err = a.bankImpact(act, pl.st)
		case setup.KindPartner:
			act.Impact.Connections = a.partnerConnections(act.ID)
		}
		if err != nil {
			return fmt.Errorf("read the impact on %s %s: %w", strings.ToLower(act.Kind), act.ID, err)
		}
	}
	return nil
}

func (a *App) partnerConnections(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, c := range a.st.Connections {
		if c.PartnerID == id && c.Status != "revoked" && c.Status != "expired" {
			n++
		}
	}
	return n
}

func (a *App) bankImpact(act *setup.Action, st setup.Status) error {
	im := act.Impact
	a.mu.Lock()
	open := a.ledgerFor(act.ID) != nil
	a.mu.Unlock()
	if open {
		ci, err := a.CloseImpact(act.ID)
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		if err == nil {
			for _, c := range ci.Customers {
				if c.Deposits > 0 {
					im.Customers++
				}
			}
			im.MoneyMinor, im.Connections = ci.Deposits, len(ci.Connections)
		}
	}
	code := ""
	if b, ok := bankIn(st.Applied, act.ID); ok {
		code = b.NIPCode
	}
	if r := findResource(&st, setup.KindBank, act.ID); r != nil && code == "" {
		code = r.Allocated.NIPCode
	}
	transfers, net, err := a.openTransfers(code)
	if err != nil {
		return err
	}
	im.Transfers, im.NetMinor = transfers, net
	snaps, err := a.snapshotsWith(act.ID)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if !slices.Contains(im.Snapshots, s) {
			im.Snapshots = append(im.Snapshots, s)
		}
	}
	sort.Strings(im.Snapshots)
	return nil
}

func (a *App) openTransfers(code string) (int, int64, error) {
	sw := a.nibssStore()
	if sw == nil || code == "" {
		return 0, 0, nil
	}
	sessions, err := sw.Open(context.Background())
	if err != nil {
		return 0, 0, fmt.Errorf("read open NIP sessions: %w", err)
	}
	count := 0
	var net int64
	for _, x := range sessions {
		var delta int64
		switch {
		case x.ToCode == code && x.FromCode != code:
			delta = x.Amount
		case x.FromCode == code && x.ToCode != code:
			delta = -x.Amount
		case x.FromCode != code:
			continue
		}
		count++
		sum := net + delta
		if (delta > 0 && sum < net) || (delta < 0 && sum > net) {
			return 0, 0, fmt.Errorf("the open transfers of the bank add up to more than 64 bits")
		}
		net = sum
	}
	return count, net, nil
}

func (a *App) prepare(pl planned, money *setup.Removal, retarget map[string]bool) (*admission, error) {
	ad := &admission{pl: pl, money: money, retarget: retarget, dir: a.cfg.Dir, now: a.clock.Real(), ports: map[string][2]int{}, partners: map[string]*config.Partner{}}
	if err := a.allocatePorts(ad); err != nil {
		return nil, err
	}
	for _, act := range pl.plan.Actions {
		if act.Kind != setup.KindPartner || act.Op != setup.OpCreate {
			continue
		}
		p, err := a.makePartner(pl.desired, act.ID)
		if err != nil {
			return nil, errors.Join(err, a.discardPartners(ad))
		}
		ad.partners[act.ID] = p
	}
	return ad, nil
}

func (a *App) allocatePorts(ad *admission) error {
	var creates []string
	for _, act := range ad.pl.plan.Actions {
		if act.Kind == setup.KindBank && act.Op == setup.OpCreate {
			creates = append(creates, act.ID)
		}
	}
	if len(creates) == 0 {
		return nil
	}
	if err := a.postgresRoom(context.Background()); err != nil {
		return applyFail(http.StatusUnprocessableEntity, codeInvalidSpec, "%s", strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": "))
	}
	c := a.cfg.Connect
	used := map[int]bool{c.PartnerPort: true, c.WidgetPort: true, c.ControlPort: true, c.HealthPort: true}
	top := c.PartnerPort
	for _, r := range ad.pl.st.Resources {
		for _, p := range []int{r.Allocated.Port, r.Allocated.APIPort} {
			if p != 0 {
				used[p] = true
				top = max(top, p)
			}
		}
	}
	for _, id := range creates {
		port, err := a.cfg.AllocatePort(top+1, used, portFree)
		if err != nil {
			return applyFail(http.StatusUnprocessableEntity, codeInvalidSpec, "%s: online banking: %v", id, err)
		}
		used[port] = true
		api, err := a.cfg.AllocatePort(port+a.cfg.APIPortOffset(), used, portFree)
		if err != nil {
			return applyFail(http.StatusUnprocessableEntity, codeInvalidSpec, "%s: bank API: %v", id, err)
		}
		used[api] = true
		top = port
		ad.ports[id] = [2]int{port, api}
	}
	return nil
}

func (a *App) makePartner(desired setup.Spec, id string) (*config.Partner, error) {
	sp, _ := partnerIn(desired, id)
	key, err := writeAPIKey(a.cfg.Dir, id)
	if err != nil {
		return nil, err
	}
	p := runtimePartner(sp, setup.PartnerKeys{CertCN: id, APIKeySHA256: config.HashKey(key)}, setup.PartnerLocal{})
	if err := a.issuePartnerCert(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (a *App) discardPartners(ad *admission) error {
	var errs []error
	for id := range ad.partners {
		if err := os.RemoveAll(a.cfg.Path("partners", id)); err != nil {
			errs = append(errs, fmt.Errorf("remove partners/%s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func pinnedCodes(st *setup.Status, old setup.Bank, id string, created map[string]string) (string, string) {
	nip, nuban := old.NIPCode, old.NUBANCode
	if r := findResource(st, setup.KindBank, id); r != nil {
		nip, nuban = orDefault(nip, r.Allocated.NIPCode), orDefault(nuban, r.Allocated.NUBANCode)
	}
	return orDefault(nip, created[fieldNIPCode]), orDefault(nuban, created[fieldNUBANCode])
}

func createdFields(p setup.Plan) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, act := range p.Actions {
		if act.Kind != setup.KindBank || act.Op != setup.OpCreate {
			continue
		}
		out[act.ID] = map[string]string{}
		for _, f := range act.Fields {
			out[act.ID][f.Path] = f.To
		}
	}
	return out
}

func nextApplied(st *setup.Status, ad *admission) setup.Spec {
	desired := ad.pl.desired
	out := st.Applied
	body := desired.Spec
	body.Start = st.Applied.Spec.Start
	body.NIP.Sessions = st.Applied.Spec.NIP.Sessions
	created := createdFields(ad.pl.plan)
	body.Banks = make([]setup.Bank, 0, len(desired.Spec.Banks))
	stays := map[string]bool{}
	for _, b := range desired.Spec.Banks {
		stays[b.ID] = true
		old, _ := bankIn(st.Applied, b.ID)
		b.NIPCode, b.NUBANCode = pinnedCodes(st, old, b.ID, created[b.ID])
		body.Banks = append(body.Banks, b)
	}
	removing := map[string]bool{}
	for _, act := range ad.pl.plan.Actions {
		if act.Kind == setup.KindBank && act.Op == setup.OpRemove {
			removing[act.ID] = true
		}
	}
	for _, old := range st.Applied.Spec.Banks {
		if stays[old.ID] {
			continue
		}
		if planned, ok := bankIn(ad.pl.st.Applied, old.ID); ok && (removing[old.ID] || ad.retarget[old.ID]) {
			old.OnRemove = planned.OnRemove
		}
		body.Banks = append(body.Banks, old)
	}
	body.Partners = slices.Clone(desired.Spec.Partners)
	wanted := map[string]bool{}
	for _, p := range desired.Spec.Partners {
		wanted[p.ID] = true
	}
	for _, old := range st.Applied.Spec.Partners {
		if !wanted[old.ID] {
			body.Partners = append(body.Partners, old)
		}
	}
	out.Spec = body
	return out
}

func admit(st *setup.Status, ad *admission) (bool, error) {
	st.Applied = nextApplied(st, ad)
	created := createdFields(ad.pl.plan)
	for _, act := range ad.pl.plan.Actions {
		switch act.Kind {
		case setup.KindBank:
			admitBank(st, act, ad, created[act.ID])
		case setup.KindPartner:
			admitPartner(st, act, ad)
		}
	}
	for id := range ad.retarget {
		if r := findResource(st, setup.KindBank, id); r != nil {
			r.Money = ad.money
			r.Attempts, r.NextAttemptAt = 0, nil
		}
	}
	sum, err := specSum(st.Applied)
	if err != nil {
		return false, err
	}
	st.AppliedSHA256 = sum
	return true, nil
}

func leaving(r *setup.Resource, phase string, finalizers []string, rm *setup.Removal, now time.Time) {
	r.Generation++
	r.Phase = phase
	if r.DeletionRequestedAt == nil {
		r.DeletionRequestedAt = &now
	}
	r.Finalizers = finalizers
	r.Done = slices.DeleteFunc(slices.Clone(r.Done), func(name string) bool { return !slices.Contains(finalizers, name) })
	r.Money = rm
	r.Attempts, r.NextAttemptAt = 0, nil
	setCondition(r, setup.Condition{Type: setup.ConditionProgressing, Status: "True", Reason: setup.ReasonTerminating, Message: phase, ChangedAt: now})
}

func admitBank(st *setup.Status, act setup.Action, ad *admission, fields map[string]string) {
	switch act.Op {
	case setup.OpCreate:
		r := ensureResource(st, setup.KindBank, act.ID)
		gen := r.Generation + 1
		ports := ad.ports[act.ID]
		*r = setup.Resource{
			Kind:       setup.KindBank,
			ID:         act.ID,
			Generation: gen,
			Phase:      setup.PhaseProvisioning,
			Allocated: setup.Allocation{
				NIPCode:   fields[fieldNIPCode],
				NUBANCode: fields[fieldNUBANCode],
				Database:  config.DatabaseName(ad.dir, act.ID),
				Port:      ports[0],
				APIPort:   ports[1],
			},
			Steps: map[string]string{setup.StepAllocate: stepDone},
		}
	case setup.OpUpdate:
		if r := findResource(st, setup.KindBank, act.ID); r != nil {
			r.Generation++
		}
	case setup.OpClose:
		if r := findResource(st, setup.KindBank, act.ID); r != nil {
			d, _ := bankIn(ad.pl.desired, act.ID)
			leaving(r, setup.PhaseClosing, finalizerNames(bankFinalizers)[:len(bankFinalizers)-1], d.OnRemove, ad.now)
		}
	case setup.OpRemove:
		if r := findResource(st, setup.KindBank, act.ID); r != nil {
			b, _ := bankIn(ad.pl.st.Applied, act.ID)
			leaving(r, setup.PhaseTerminating, finalizerNames(bankFinalizers), b.OnRemove, ad.now)
		}
	}
}

func admitPartner(st *setup.Status, act setup.Action, ad *admission) {
	switch act.Op {
	case setup.OpCreate:
		p := ad.partners[act.ID]
		r := ensureResource(st, setup.KindPartner, act.ID)
		gen := r.Generation + 1
		*r = setup.Resource{
			Kind:               setup.KindPartner,
			ID:                 act.ID,
			Generation:         gen,
			ObservedGeneration: gen,
			Phase:              setup.PhaseActive,
			Partner:            &setup.PartnerKeys{CertCN: p.CertCN, APIKeySHA256: p.APIKeySHA256, CertSHA256: p.CertSHA256},
			Owned:              []setup.Owned{{Kind: ownedDir, Name: "partners/" + act.ID}},
		}
	case setup.OpUpdate:
		if r := findResource(st, setup.KindPartner, act.ID); r != nil {
			r.Generation++
		}
	case setup.OpRemove:
		if r := findResource(st, setup.KindPartner, act.ID); r != nil {
			leaving(r, setup.PhaseTerminating, finalizerNames(partnerFinalizers), nil, ad.now)
		}
	}
}

func (a *App) startAdmitted(ad *admission) {
	st, err := a.ctl.status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	for _, act := range ad.pl.plan.Actions {
		r := findResource(&st, act.Kind, act.ID)
		if r == nil {
			continue
		}
		switch {
		case act.Kind == setup.KindBank && act.Op == setup.OpCreate:
			sb, _ := bankIn(st.Applied, act.ID)
			b := runtimeBank(sb, *r)
			a.lifeMu.Lock()
			a.mu.Lock()
			a.cfg.Banks = append(slices.Clone(a.cfg.Banks), b)
			a.mu.Unlock()
			a.status[b.ID] = newStatus(b, false)
			a.lifeMu.Unlock()
		case act.Kind == setup.KindBank && (act.Op == setup.OpClose || act.Op == setup.OpRemove):
			a.lifeMu.Lock()
			if b := a.cfg.Bank(act.ID); b != nil && !b.Closed() {
				a.mu.Lock()
				b.State = setup.StateSuspended
				a.mu.Unlock()
			}
			if s := a.status[act.ID]; s != nil {
				s.Phase, s.Reason = r.Phase, ""
			}
			a.lifeMu.Unlock()
		case act.Kind == setup.KindPartner && act.Op == setup.OpCreate:
			a.pmu.Lock()
			a.cfg.Partners = append(slices.Clone(a.cfg.Partners), ad.partners[act.ID])
			a.pmu.Unlock()
		}
	}
}

func (a *App) Resource(kind, id string) (setup.Resource, error) {
	st, err := a.ctl.status()
	if err != nil {
		return setup.Resource{}, err
	}
	for _, r := range st.Resources {
		if strings.EqualFold(r.Kind, kind) && r.ID == id {
			return r, nil
		}
	}
	return setup.Resource{}, errNotFound
}

func (a *App) resources(limit int, cursor string) ([]setup.Resource, string, error) {
	st, err := a.ctl.status()
	if err != nil {
		return nil, "", err
	}
	all := st.Resources
	sort.Slice(all, func(i, j int) bool { return resourceKey(all[i]) < resourceKey(all[j]) })
	start := sort.Search(len(all), func(i int) bool { return cursor == "" || resourceKey(all[i]) > cursor })
	page := all[start:]
	if len(page) <= limit {
		return page, "", nil
	}
	return page[:limit], resourceKey(page[limit-1]), nil
}

func resourceKey(r setup.Resource) string {
	return strings.ToLower(r.Kind) + "/" + r.ID
}

func (a *App) waitGone(kind, id string) (bool, error) {
	deadline := time.NewTimer(resourceSettleWait)
	defer deadline.Stop()
	tick := time.NewTicker(resourcePollEvery)
	defer tick.Stop()
	for {
		_, err := a.Resource(kind, id)
		switch {
		case errors.Is(err, errNotFound):
			return true, nil
		case err != nil:
			return false, err
		}
		select {
		case <-deadline.C:
			return false, nil
		case <-tick.C:
		}
	}
}

func (a *App) writeApplyError(w http.ResponseWriter, r *http.Request, err error) bool {
	var ae *applyError
	switch {
	case errors.As(err, &ae):
		writeError(w, r, ae.status, ae.code, ae.message)
	case errors.Is(err, ErrBankInvalid):
		writeError(w, r, http.StatusUnprocessableEntity, codeInvalidSpec, strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": "))
	default:
		return false
	}
	return true
}

type applyRequest struct {
	File string `json:"file"`
	Spec string `json:"spec"`
	ApplyOptions
}

type planBody struct {
	setup.Plan
	Text     string   `json:"text"`
	Changed  bool     `json:"changed"`
	KeyFiles []string `json:"api_key_files,omitempty"`
}

func (a *App) planBody(p setup.Plan) planBody {
	out := planBody{Plan: p, Text: setup.Render(p), Changed: changes(p)}
	for _, act := range p.Actions {
		if act.Kind == setup.KindPartner && act.Op == setup.OpCreate {
			out.KeyFiles = append(out.KeyFiles, filepath.Join(a.cfg.Path("partners", act.ID), partnerKeyFile))
		}
	}
	return out
}

func (a *App) parseRequest(w http.ResponseWriter, r *http.Request) (setup.Spec, ApplyOptions, bool) {
	var req applyRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"file":"setup.yaml","spec":"<the file's text>"}.`)
		return setup.Spec{}, ApplyOptions{}, false
	}
	spec, errs := setup.Parse(orDefault(req.File, SpecFile), []byte(req.Spec))
	if len(errs) > 0 {
		a.writeApplyError(w, r, planFailure(errs))
		return setup.Spec{}, ApplyOptions{}, false
	}
	return spec, req.ApplyOptions, true
}

func (a *App) ctlPlan(w http.ResponseWriter, r *http.Request) {
	spec, opt, ok := a.parseRequest(w, r)
	if !ok {
		return
	}
	st, err := a.ctl.status()
	if err == nil {
		var pl planned
		if pl, err = a.buildPlan(spec, st, opt); err == nil {
			writeJSON(w, http.StatusOK, a.planBody(pl.plan))
			return
		}
	}
	a.applyFailed(w, r, err)
}

func (a *App) ctlApply(w http.ResponseWriter, r *http.Request) {
	spec, opt, ok := a.parseRequest(w, r)
	if !ok {
		return
	}
	plan, err := a.Apply(spec, opt)
	if err != nil {
		a.applyFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a.planBody(plan))
}

func (a *App) applyFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "No such resource.")
		return
	}
	if a.writeApplyError(w, r, err) {
		return
	}
	a.log.Error("apply", "err", err)
	writeError(w, r, http.StatusInternalServerError, "internal_error", err.Error())
}

func (a *App) ctlResources(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listParams(r)
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	data, next, err := a.resources(limit, cursor)
	if err != nil {
		a.applyFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listPage(data, next))
}

func (a *App) ctlResource(w http.ResponseWriter, r *http.Request) {
	res, err := a.Resource(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		a.applyFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
