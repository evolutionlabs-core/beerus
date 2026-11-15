package connect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	nibssRetry     = 10 * time.Second
	directoryRetry = 3 * time.Second
)

func openNIBSS(ctx context.Context, cfg *config.Config, ctl *ctlStore) (*nibss.Store, error) {
	name := config.DatabaseName(cfg.Dir, setup.SystemNIBSS)
	if err := ctl.Claim(setup.KindSystem, setup.SystemNIBSS, setup.Owned{Kind: ownedDatabase, Name: name}); err != nil {
		return nil, err
	}
	s, err := nibss.Open(ctx, cfg.DatabaseURL, name)
	if err != nil {
		return nil, fmt.Errorf("open the NIBSS directory: %w", err)
	}
	if err := ctl.update(func(st *setup.Status) (bool, error) {
		r := ensureResource(st, setup.KindSystem, setup.SystemNIBSS)
		if r.Phase == config.PhaseActive {
			return false, nil
		}
		r.Phase = config.PhaseActive
		return true, nil
	}); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (a *App) nibssStore() *nibss.Store {
	a.mu.Lock()
	if a.nibss != nil || a.nibssOpening || a.clock.Real().Sub(a.nibssTried) < nibssRetry {
		s := a.nibss
		a.mu.Unlock()
		return s
	}
	a.nibssTried, a.nibssOpening = a.clock.Real(), true
	a.mu.Unlock()
	s, err := openNIBSS(context.Background(), a.cfg, a.ctl)
	a.mu.Lock()
	a.nibssOpening = false
	if err != nil {
		a.mu.Unlock()
		a.log.Error("NIBSS directory not ready; retrying", "err", err)
		return nil
	}
	a.nibss = s
	a.mu.Unlock()
	if err := a.refreshDirectory(context.Background()); err != nil {
		a.log.Error("NIBSS directory not read", "err", err)
	}
	return s
}

func (a *App) syncDirectoryDue(ctx context.Context) {
	a.mu.Lock()
	s := a.nibss
	due := s != nil && !a.dirSynced && a.clock.Real().Sub(a.dirTried) >= directoryRetry
	if due {
		a.dirTried = a.clock.Real()
	}
	a.mu.Unlock()
	if !due {
		return
	}
	if err := a.syncDirectory(ctx, s); err != nil {
		a.logNIBSS("NIBSS directory out of date; retrying", err)
		return
	}
	a.mu.Lock()
	a.dirSynced = true
	a.mu.Unlock()
}

func (a *App) logNIBSS(msg string, err error) {
	var down *nibssDownError
	if errors.As(err, &down) {
		a.log.Warn(msg, "err", err)
		return
	}
	a.log.Error(msg, "err", err)
}

func (a *App) syncDirectory(ctx context.Context, s *nibss.Store) error {
	list, err := s.Directory(ctx)
	if err != nil {
		return fmt.Errorf("read the NIBSS directory: %w", err)
	}
	a.mu.Lock()
	banks := slices.Clone(a.cfg.Banks)
	a.mu.Unlock()
	member := map[string]bool{}
	var errs []error
	for _, b := range banks {
		member[b.ID] = true
		errs = append(errs, a.registerMember(ctx, b))
	}
	for _, in := range list {
		if in.Status == nip.StatusRemoved || member[in.ID] {
			continue
		}
		for _, next := range []string{nip.StatusTerminating, nip.StatusRemoved} {
			if in.Status == next {
				continue
			}
			if err := a.nibssCtl.setStatus(ctx, in.ID, next); err != nil {
				errs = append(errs, fmt.Errorf("set NIBSS status of %s to %s: %w", in.ID, next, err))
				break
			}
		}
	}
	errs = append(errs, a.refreshDirectory(ctx))
	return errors.Join(errs...)
}

func (a *App) dropNIBSS(ctx context.Context) error {
	a.mu.Lock()
	s := a.nibss
	a.nibss = nil
	a.mu.Unlock()
	if s == nil {
		return nil
	}
	s.Close()
	name := config.DatabaseName(a.cfg.Dir, setup.SystemNIBSS)
	if err := store.DropDatabases(ctx, a.cfg.DatabaseURL, []string{name}); err != nil {
		return fmt.Errorf("drop the NIBSS database: %w", err)
	}
	return a.ctl.Release(setup.Owned{Kind: ownedDatabase, Name: name})
}

func memberOf(b *config.Bank) nip.Institution {
	return nip.Institution{ID: b.ID, Name: b.Name, NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Type: b.Type}
}

func memberStatus(b *config.Bank) string {
	if b.Suspended() || b.Closed() {
		return nip.StatusSuspended
	}
	return nip.StatusActive
}

func (a *App) registerMember(ctx context.Context, b *config.Bank) error {
	in, err := a.nibssCtl.register(ctx, b)
	if err != nil {
		return fmt.Errorf("register %s with NIBSS: %w", b.ID, err)
	}
	if in.Status != nip.StatusTerminating && in.Status != memberStatus(b) {
		if err := a.nibssCtl.setStatus(ctx, b.ID, memberStatus(b)); err != nil {
			return fmt.Errorf("set NIBSS status of %s: %w", b.ID, err)
		}
	}
	if err := a.ctl.update(func(st *setup.Status) (bool, error) {
		r := findResource(st, setup.KindBank, b.ID)
		if r == nil || r.Steps[config.StepDirectory] == stepDone {
			return false, nil
		}
		if r.Steps == nil {
			r.Steps = map[string]string{}
		}
		r.Steps[config.StepDirectory] = stepDone
		return true, nil
	}); err != nil {
		return err
	}
	return a.refreshDirectory(ctx)
}

func (a *App) attachMember(ls *ledgerSource) {
	a.mu.Lock()
	s := a.nibss
	a.mu.Unlock()
	if s != nil && ls != nil {
		s.Attach(ls.Inst.ID, member{a, ls})
	}
}

func (a *App) setMemberStatus(ctx context.Context, id, status string) error {
	if err := a.nibssCtl.setStatus(ctx, id, status); err != nil {
		return fmt.Errorf("set NIBSS status of %s to %s: %w", id, status, err)
	}
	return a.refreshDirectory(ctx)
}

func (a *App) stopRouting(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	sw := a.nibssStore()
	if sw == nil {
		return nil, errors.New("NIBSS is not open, so routing to the bank cannot be stopped; try again soon")
	}
	if err := a.setMemberStatus(ctx, r.ID, nip.StatusTerminating); err != nil {
		return nil, err
	}
	in, err := sw.Institution(ctx, r.Allocated.NIPCode)
	if err != nil {
		return nil, fmt.Errorf("read back the NIBSS status of %s: %w", r.ID, err)
	}
	if in.Status != nip.StatusTerminating {
		return nil, fmt.Errorf("NIBSS shows %s as %s, not %s", r.ID, in.Status, nip.StatusTerminating)
	}
	return nil, nil
}

func (a *App) removeMember(ctx context.Context, id string) error {
	a.mu.Lock()
	s := a.nibss
	a.mu.Unlock()
	if s == nil {
		return nil
	}
	s.Attach(id, nil)
	list, err := s.Directory(ctx)
	if err != nil {
		return fmt.Errorf("read the NIBSS directory: %w", err)
	}
	cur := ""
	for _, in := range list {
		if in.ID == id {
			cur = in.Status
		}
	}
	for _, status := range []string{nip.StatusTerminating, nip.StatusRemoved} {
		if cur == nip.StatusRemoved || cur == "" {
			break
		}
		if cur == status {
			continue
		}
		if err := a.nibssCtl.setStatus(ctx, id, status); err != nil {
			return fmt.Errorf("set NIBSS status of %s to %s: %w", id, status, err)
		}
		cur = status
	}
	return a.refreshDirectory(ctx)
}

func (a *App) refreshDirectory(ctx context.Context) error {
	a.mu.Lock()
	s := a.nibss
	a.mu.Unlock()
	if s == nil {
		return nil
	}
	list, err := s.Directory(ctx)
	if err != nil {
		return fmt.Errorf("read the NIBSS directory: %w", err)
	}
	members := make([]nip.Participant, 0, len(list))
	for _, in := range list {
		if in.Status != nip.StatusRemoved {
			members = append(members, nip.Participant{Code: in.NIPCode, Name: in.Name, BankID: in.ID})
		}
	}
	a.mu.Lock()
	a.members = members
	a.mu.Unlock()
	return nil
}

const removedSuffix = " (removed)"

func (a *App) markRemoved(ctx context.Context, lines []bank.FeedLine) error {
	if !slices.ContainsFunc(lines, func(l bank.FeedLine) bool { return l.Counterparty != nil }) {
		return nil
	}
	s := a.nibssStore()
	if s == nil {
		return nil
	}
	directory, err := s.Directory(ctx)
	if err != nil {
		return fmt.Errorf("read the NIBSS directory: %w", err)
	}
	removed := map[string]bool{}
	for _, in := range directory {
		removed[in.NIPCode] = in.Status == nip.StatusRemoved
	}
	for _, l := range lines {
		if cp := l.Counterparty; cp != nil && removed[cp.BankCode] && !strings.HasSuffix(cp.BankName, removedSuffix) {
			cp.BankName += removedSuffix
		}
	}
	return nil
}
