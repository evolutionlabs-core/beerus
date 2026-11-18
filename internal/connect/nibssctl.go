package connect

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	nibssBanksPath     = control.NIBSSDirectoryPath + "/"
	nibssSessionsPath  = "/v1/control/sessions/"
	nibssClosureSuffix = "/closure-sessions"
	nibssEnquiriesPath = "/v1/control/name-enquiries"
	nibssClientsPath   = "/v1/control/registry/clients"
	nibssConsentsPath  = "/v1/control/consents"
	nibssSettlePath    = "/v1/control/settlement-sessions"
	nibssStatusPath    = "/v1/status"
	nibssOpenSuffix    = "/open-sessions"
	nibssAway          = "NIBSS is not available right now. Try again soon."
	nibssSettleTimeout = 30 * time.Second
)

type nibssAdmin struct {
	control.NIBSSControl
}

type registerBody struct {
	NIPCode             string `json:"nip_code"`
	NUBANCode           string `json:"nuban_code"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	CreditURL           string `json:"credit_url"`
	CertificateIdentity string `json:"certificate_identity"`
	NetDebitCap         int64  `json:"net_debit_cap_minor"`
	Collateral          int64  `json:"collateral_minor"`
}

type clientBody struct {
	Name         string          `json:"name"`
	LegalName    string          `json:"legal_name"`
	CAC          string          `json:"cac"`
	Role         string          `json:"role"`
	CertCN       string          `json:"cert_cn"`
	JWK          openbanking.JWK `json:"jwk"`
	RedirectURIs []string        `json:"redirect_uris"`
}

type statusBody struct {
	Status string `json:"status"`
}

func (n *nibssAdmin) enquire(ctx context.Context, code, nuban string) (nip.NameEnquiry, error) {
	var out nip.NameEnquiry
	q := url.Values{"bank_code": {code}, "account_number": {nuban}}
	_, err := n.Link.Do(ctx, n.Base, http.MethodGet, nibssEnquiriesPath+"?"+q.Encode(), nil, &out)
	var peer *control.PeerError
	switch {
	case err == nil || errors.Is(err, context.Canceled):
		return out, err
	case errors.As(err, &peer) && peer.Status != http.StatusServiceUnavailable:
		return out, err
	}
	return out, &control.UnavailableError{Msg: nibssAway, Err: err}
}

func (n *nibssAdmin) register(ctx context.Context, b config.Bank) (nip.Institution, error) {
	var in nip.Institution
	body := registerBody{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Name: b.Name, Type: b.Type, CreditURL: b.APIURL(), CertificateIdentity: b.ID, NetDebitCap: b.Cap(), Collateral: b.Collateral}
	return in, n.Call(ctx, http.MethodPut, nibssBanksPath+url.PathEscape(b.ID), nil, body, &in)
}

func (n *nibssAdmin) setStatus(ctx context.Context, id, status string) error {
	return n.Call(ctx, http.MethodPut, nibssBanksPath+url.PathEscape(id)+"/status", nil, statusBody{Status: status}, nil)
}

func (n *nibssAdmin) ensureClient(ctx context.Context, c openbanking.RegistryClient) (openbanking.RegistryClient, error) {
	var out openbanking.RegistryClient
	body := clientBody{Name: c.Name, LegalName: c.LegalName, CAC: c.CAC, Role: c.Role, CertCN: c.CertCN, JWK: c.JWK, RedirectURIs: c.RedirectURIs}
	header := http.Header{"If-None-Match": {"*"}}
	return out, n.Call(ctx, http.MethodPut, nibssClientsPath+"/"+url.PathEscape(c.ID), header, body, &out)
}

func (n *nibssAdmin) clients(ctx context.Context) ([]openbanking.RegistryClient, error) {
	return control.AllPages(ctx, func(ctx context.Context, cursor string) ([]openbanking.RegistryClient, string, error) {
		var page struct {
			Data       []openbanking.RegistryClient `json:"data"`
			NextCursor string                       `json:"next_cursor"`
		}
		err := n.Call(ctx, http.MethodGet, nibssClientsPath+"?"+control.PageQuery(cursor), nil, nil, &page)
		return page.Data, page.NextCursor, err
	})
}

type consentPage struct {
	Data       []ConsentEvent `json:"data"`
	NextCursor string         `json:"next_cursor"`
}

func (n *nibssAdmin) consents(ctx context.Context, q url.Values) (consentPage, error) {
	var out consentPage
	return out, n.Call(ctx, http.MethodGet, nibssConsentsPath+"?"+q.Encode(), nil, nil, &out)
}

func (n *nibssAdmin) settle(ctx context.Context, adhoc bool) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, nibssSettleTimeout)
	defer cancel()
	var out struct {
		Posted int `json:"posted_sessions"`
	}
	err := n.Call(ctx, http.MethodPost, nibssSettlePath, nil, map[string]bool{"adhoc": adhoc}, &out)
	return out.Posted, err
}

func (n *nibssAdmin) status(ctx context.Context) (int, []string, error) {
	var out struct {
		Posted   *int     `json:"posted_sessions"`
		Problems []string `json:"problems"`
	}
	if err := n.Call(ctx, http.MethodGet, nibssStatusPath, nil, nil, &out); err != nil {
		return 0, nil, fmt.Errorf("read the status of NIBSS: %w", err)
	}
	if out.Posted == nil {
		return 0, nil, fmt.Errorf("NIBSS could not count its settlement sessions: %s", strings.Join(out.Problems, "; "))
	}
	return *out.Posted, out.Problems, nil
}

func (n *nibssAdmin) nextCutoff(ctx context.Context) (time.Time, error) {
	var out struct {
		NextCutoffAt *time.Time `json:"next_cutoff_at"`
	}
	if err := n.Call(ctx, http.MethodGet, nibssStatusPath, nil, nil, &out); err != nil {
		return time.Time{}, fmt.Errorf("read the next NIP session of NIBSS: %w", err)
	}
	if out.NextCutoffAt == nil {
		return time.Time{}, &uncheckedError{Name: "NIBSS", Why: "has not said when the next NIP session is"}
	}
	return *out.NextCutoffAt, nil
}

type openSessions struct {
	Transfers     int   `json:"transfers"`
	Outward       int64 `json:"outward_minor"`
	Inward        int64 `json:"inward_minor"`
	PendingInward int64 `json:"pending_inward_minor"`
	Unposted      int   `json:"unposted_settlements"`
}

func (o openSessions) inwardAll() (int64, bool) {
	sum := o.Inward + o.PendingInward
	return sum, sum >= o.Inward
}

func (n *nibssAdmin) openSessions(ctx context.Context, id string) (openSessions, error) {
	var out openSessions
	if err := n.Call(ctx, http.MethodGet, nibssBanksPath+url.PathEscape(id)+nibssOpenSuffix, nil, nil, &out); err != nil {
		return openSessions{}, fmt.Errorf("read the open sessions of %s at NIBSS: %w", id, err)
	}
	return out, nil
}

func (n *nibssAdmin) member(ctx context.Context, id string) (nip.Institution, error) {
	var in nip.Institution
	err := n.Call(ctx, http.MethodGet, nibssBanksPath+url.PathEscape(id), nil, nil, &in)
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusNotFound {
		return nip.Institution{}, fmt.Errorf("bank id %s is %w", id, nip.ErrUnknownInstitution)
	}
	return in, err
}

func (n *nibssAdmin) session(ctx context.Context, id string) (control.NIPSession, error) {
	var x control.NIPSession
	err := n.Call(ctx, http.MethodGet, nibssSessionsPath+url.PathEscape(id), nil, nil, &x)
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusNotFound {
		return control.NIPSession{}, fmt.Errorf("session %s: %w", id, nip.ErrNoRecord)
	}
	return x, err
}

func (n *nibssAdmin) closureSessions(ctx context.Context, id string) ([]ClosureSession, error) {
	return control.AllPages(ctx, func(ctx context.Context, cursor string) ([]ClosureSession, string, error) {
		var page struct {
			Data       []ClosureSession `json:"data"`
			NextCursor string           `json:"next_cursor"`
		}
		err := n.Call(ctx, http.MethodGet, nibssBanksPath+url.PathEscape(id)+nibssClosureSuffix+"?"+control.PageQuery(cursor), nil, nil, &page)
		return page.Data, page.NextCursor, err
	})
}
