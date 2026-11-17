package connect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	nibssBanksPath     = "/v1/control/banks/"
	nibssEnquiriesPath = "/v1/control/name-enquiries"
	nibssClientsPath   = "/v1/control/registry/clients"
	nibssConsentsPath  = "/v1/control/consents"
	nibssPage          = 500
	nibssAway          = "NIBSS is not available right now. Try again soon."
	nibssHost          = "127.0.0.1"
)

type nibssAdmin struct {
	link server.Link
	base string
	port int
}

type nibssDownError struct {
	Port int
	Err  error
}

func (e *nibssDownError) Error() string {
	return fmt.Sprintf("NIBSS is not answering on port %d: %v", e.Port, e.Err)
}

func (e *nibssDownError) Unwrap() error {
	return e.Err
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
	_, err := n.link.Do(ctx, n.base, http.MethodGet, nibssEnquiriesPath+"?"+q.Encode(), nil, &out)
	var peer *server.PeerError
	switch {
	case err == nil || errors.Is(err, context.Canceled):
		return out, err
	case errors.As(err, &peer) && peer.Status != http.StatusServiceUnavailable:
		return out, err
	}
	return out, &server.UnavailableError{Msg: nibssAway, Err: err}
}

func (n *nibssAdmin) register(ctx context.Context, b config.Bank) (nip.Institution, error) {
	var in nip.Institution
	body := registerBody{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Name: b.Name, Type: b.Type, CreditURL: b.APIURL(), CertificateIdentity: b.ID, NetDebitCap: b.Cap(), Collateral: b.Collateral}
	return in, n.call(ctx, http.MethodPut, nibssBanksPath+url.PathEscape(b.ID), nil, body, &in)
}

func (n *nibssAdmin) setStatus(ctx context.Context, id, status string) error {
	return n.call(ctx, http.MethodPut, nibssBanksPath+url.PathEscape(id)+"/status", nil, statusBody{Status: status}, nil)
}

func (n *nibssAdmin) ensureClient(ctx context.Context, c openbanking.RegistryClient, createOnly bool) (openbanking.RegistryClient, error) {
	var out openbanking.RegistryClient
	body := clientBody{Name: c.Name, LegalName: c.LegalName, CAC: c.CAC, Role: c.Role, CertCN: c.CertCN, JWK: c.JWK, RedirectURIs: c.RedirectURIs}
	var header http.Header
	if createOnly {
		header = http.Header{"If-None-Match": {"*"}}
	}
	return out, n.call(ctx, http.MethodPut, nibssClientsPath+"/"+url.PathEscape(c.ID), header, body, &out)
}

func (n *nibssAdmin) clients(ctx context.Context) ([]openbanking.RegistryClient, error) {
	return server.AllPages(ctx, func(ctx context.Context, cursor string) ([]openbanking.RegistryClient, string, error) {
		q := url.Values{"limit": {strconv.Itoa(nibssPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Data       []openbanking.RegistryClient `json:"data"`
			NextCursor string                       `json:"next_cursor"`
		}
		err := n.call(ctx, http.MethodGet, nibssClientsPath+"?"+q.Encode(), nil, nil, &page)
		return page.Data, page.NextCursor, err
	})
}

type consentPage struct {
	Data       []nibss.ConsentEvent `json:"data"`
	NextCursor string               `json:"next_cursor"`
}

func (n *nibssAdmin) consents(ctx context.Context, q url.Values) (consentPage, error) {
	var out consentPage
	return out, n.call(ctx, http.MethodGet, nibssConsentsPath+"?"+q.Encode(), nil, nil, &out)
}

func (n *nibssAdmin) call(ctx context.Context, method, path string, header http.Header, in, out any) error {
	_, err := n.link.Request(ctx, n.base, method, path, header, in, out)
	var peer *server.PeerError
	if err == nil || errors.As(err, &peer) || ctx.Err() != nil {
		return err
	}
	if unreachable(err) {
		return &nibssDownError{Port: n.port, Err: err}
	}
	return fmt.Errorf("call NIBSS on port %d: %w", n.port, err)
}

func unreachable(err error) bool {
	var op *net.OpError
	var timeout net.Error
	return errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &op) && op.Op == "dial") || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout())
}
