package connect

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type PartnerRequest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	LegalName    string   `json:"legal_name"`
	Purpose      string   `json:"purpose"`
	RedirectURIs []string `json:"redirect_uris"`
	WebhookURL   string   `json:"webhook_url"`
}

var errPartnerBusy = invalid("another partner change is running; try again")

func (a *App) AddPartner(req PartnerRequest) (string, error) {
	o := config.PartnerOptions{ID: req.ID, Name: req.Name, LegalName: req.LegalName, Purpose: req.Purpose, RedirectURIs: req.RedirectURIs, WebhookURL: req.WebhookURL}
	if err := o.Check(); err != nil {
		return "", invalid("%s", err.Error())
	}
	err := a.applyEdit(func(sp *setup.Spec) error {
		if _, ok := partnerIn(*sp, o.ID); ok {
			return invalid("partner %s already exists; use partner rotate-key to get new credentials", o.ID)
		}
		sp.Spec.Partners = append(sp.Spec.Partners, setup.Partner{ID: o.ID, Name: orDefault(o.Name, o.ID), LegalName: o.LegalName, Purpose: o.Purpose, RedirectURIs: o.RedirectURIs, WebhookURL: o.WebhookURL})
		return nil
	}, ApplyOptions{})
	if err != nil {
		return "", err
	}
	key, err := config.ReadSecret(a.cfg.Path("partners", o.ID, partnerKeyFile))
	if err != nil {
		return "", fmt.Errorf("read the API key of %s: %w", o.ID, err)
	}
	return key, nil
}

func (a *App) RotatePartner(id string) (key string, err error) {
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
	if err := os.Remove(filepath.Join(dir, "client.crt")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove old certificate of %s: %w", id, err)
	}
	key, err = writeAPIKey(a.cfg.Dir, id)
	if err != nil {
		return "", err
	}
	p := *cur
	p.APIKeySHA256 = config.HashKey(key)
	if err := a.issuePartnerCert(&p); err != nil {
		return "", err
	}
	err = a.ctl.update(func(st *setup.Status) (bool, error) {
		r := ensureResource(st, setup.KindPartner, id)
		r.Partner = &setup.PartnerKeys{CertCN: p.CertCN, APIKeySHA256: p.APIKeySHA256, CertSHA256: p.CertSHA256}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	a.replacePartner(id, &p)
	return key, nil
}

func (a *App) RemovePartner(id string) (bool, error) {
	err := a.applyEdit(func(sp *setup.Spec) error {
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
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("restore %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("restore %s: %w", dir, err)
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			return fmt.Errorf("restore %s: %w", name, err)
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

func (a *App) issuePartnerCert(p *config.Partner) error {
	c := config.Config{Dir: a.cfg.Dir, Connect: a.cfg.Connect, Partners: []*config.Partner{p}}
	if err := c.EnsureCertificates(a.clock.Real()); err != nil {
		return fmt.Errorf("issue certificate for %s: %w", p.ID, err)
	}
	return nil
}

func resum(st *setup.Status) error {
	sum, err := specSum(st.Applied)
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
	case errors.Is(err, ErrBankInvalid):
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_request", strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": "))
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "No such partner.")
	default:
		a.log.Error("partner", "err", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (a *App) ctlAddPartner(w http.ResponseWriter, r *http.Request) {
	var req PartnerRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"id":"...","redirect_uris":["https://..."]}.`)
		return
	}
	key, err := a.AddPartner(req)
	if err != nil {
		a.partnerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": req.ID, "api_key": key})
}

func (a *App) ctlRotatePartner(w http.ResponseWriter, r *http.Request) {
	key, err := a.RotatePartner(r.PathValue("id"))
	if err != nil {
		a.partnerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "api_key": key})
}

func (a *App) ctlRemovePartner(w http.ResponseWriter, r *http.Request) {
	gone, err := a.RemovePartner(r.PathValue("id"))
	if err != nil {
		a.partnerError(w, r, err)
		return
	}
	if !gone {
		writeJSON(w, http.StatusAccepted, map[string]string{"id": r.PathValue("id"), "status": "terminating"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "removed"})
}
