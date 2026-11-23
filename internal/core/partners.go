package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/disk"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type PartnerRequest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	LegalName    string   `json:"legal_name"`
	Purpose      string   `json:"purpose"`
	RedirectURIs []string `json:"redirect_uris"`
	WebhookURL   string   `json:"webhook_url"`
}

var errPartnerBusy = control.Invalid("another partner change is running; try again")

func (a *App) AddPartner(ctx context.Context, req PartnerRequest) (string, error) {
	o := config.PartnerOptions{ID: req.ID, Name: req.Name, LegalName: req.LegalName, Purpose: req.Purpose, RedirectURIs: req.RedirectURIs, WebhookURL: req.WebhookURL}
	if err := o.Check(); err != nil {
		return "", control.Invalid("%s", err.Error())
	}
	err := a.applyEdit(ctx, func(sp *setup.Spec) error {
		if _, ok := partnerIn(*sp, o.ID); ok {
			return control.Invalid("partner %s already exists; use partner rotate-key to get a new secret", o.ID)
		}
		sp.Spec.Partners = append(sp.Spec.Partners, setup.Partner{ID: o.ID, Name: cmp.Or(o.Name, o.ID), LegalName: o.LegalName, Purpose: o.Purpose, RedirectURIs: o.RedirectURIs, WebhookURL: o.WebhookURL})
		return nil
	}, ApplyOptions{})
	if err != nil {
		return "", err
	}
	secret, err := config.ReadSecret(a.cfg.Path("partners", o.ID, config.PartnerSecretFile))
	if err != nil {
		return "", fmt.Errorf("read the secret of %s: %w", o.ID, err)
	}
	return secret, nil
}

func (a *App) RotatePartner(ctx context.Context, id string) (key string, err error) {
	if !a.partnerMu.TryLock() {
		return "", errPartnerBusy
	}
	defer a.partnerMu.Unlock()
	cur := a.partnerOf(id)
	if cur == nil {
		return "", errNotFound
	}
	dir := a.cfg.Path("partners", id)
	saved, err := readFiles(dir)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, writeFiles(dir, saved))
		}
	}()
	key, err = config.IssueSecret(a.cfg.Dir, id)
	if err != nil {
		return "", err
	}
	p := *cur
	p.SecretSHA256 = config.HashKey(key)
	err = a.ctl.Update(func(st *setup.Status) (bool, error) {
		r := state.EnsureResource(st, setup.KindPartner, id)
		r.Partner = &setup.PartnerKeys{SecretSHA256: p.SecretSHA256}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	a.replacePartner(id, &p)
	a.reloadConnect(ctx)
	return key, nil
}

func (a *App) RemovePartner(ctx context.Context, id string) (bool, error) {
	err := a.applyEdit(ctx, func(sp *setup.Spec) error {
		i := slices.IndexFunc(sp.Spec.Partners, func(p setup.Partner) bool { return p.ID == id })
		if i < 0 {
			return errNotFound
		}
		sp.Spec.Partners = slices.Delete(sp.Spec.Partners, i, i+1)
		return nil
	}, ApplyOptions{Prune: true})
	if err != nil {
		return false, err
	}
	return a.waitGone(setup.KindPartner, id)
}

func readFiles(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out[e.Name()] = raw
	}
	return out, nil
}

func writeFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("restore %s: %w", dir, err)
	}
	for name, raw := range files {
		if err := disk.Write(filepath.Join(dir, name), raw, 0o600); err != nil {
			return fmt.Errorf("restore %s: %w", name, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	for _, e := range entries {
		if _, keep := files[e.Name()]; keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("remove %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (a *App) replacePartner(id string, p *config.Partner) {
	a.pmu.Lock()
	defer a.pmu.Unlock()
	out := make([]*config.Partner, 0, len(a.cfg.Partners))
	for _, x := range a.cfg.Partners {
		switch {
		case x.ID != id:
			out = append(out, x)
		case p != nil:
			out = append(out, p)
		}
	}
	a.cfg.Partners = out
}

func resum(st *setup.Status) error {
	sum, err := setup.SpecSum(st.Applied)
	if err != nil {
		return err
	}
	st.AppliedSHA256 = sum
	return nil
}

func (a *App) partnerError(w http.ResponseWriter, r *http.Request, err error) {
	if a.writeApplyError(w, r, err) {
		return
	}
	switch {
	case errors.Is(err, control.ErrInvalid):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", strings.TrimPrefix(err.Error(), control.ErrInvalid.Error()+": "))
	case errors.Is(err, errNotFound):
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such partner.")
	default:
		a.log.Error("partner", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (a *App) ctlAddPartner(w http.ResponseWriter, r *http.Request) {
	var req PartnerRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"id":"...","redirect_uris":["https://..."]}.`)
		return
	}
	key, err := a.AddPartner(r.Context(), req)
	if err != nil {
		a.partnerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, map[string]string{"client_id": req.ID, "secret": key})
}

func (a *App) ctlRotatePartner(w http.ResponseWriter, r *http.Request) {
	key, err := a.RotatePartner(r.Context(), r.PathValue("id"))
	if err != nil {
		a.partnerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"client_id": r.PathValue("id"), "secret": key})
}

func (a *App) ctlRemovePartner(w http.ResponseWriter, r *http.Request) {
	gone, err := a.RemovePartner(r.Context(), r.PathValue("id"))
	if err != nil {
		a.partnerError(w, r, err)
		return
	}
	if !gone {
		web.WriteJSON(w, http.StatusAccepted, map[string]string{"id": r.PathValue("id"), "status": "terminating"})
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "removed"})
}
