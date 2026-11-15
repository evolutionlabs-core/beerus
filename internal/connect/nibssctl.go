package connect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const nibssBanksPath = "/v1/control/banks/"

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
}

type statusBody struct {
	Status string `json:"status"`
}

func (n *nibssAdmin) register(ctx context.Context, b config.Bank) (nip.Institution, error) {
	var in nip.Institution
	body := registerBody{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Name: b.Name, Type: b.Type, CreditURL: b.APIURL(), CertificateIdentity: b.ID}
	return in, n.put(ctx, nibssBanksPath+url.PathEscape(b.ID), body, &in)
}

func (n *nibssAdmin) setStatus(ctx context.Context, id, status string) error {
	return n.put(ctx, nibssBanksPath+url.PathEscape(id)+"/status", statusBody{Status: status}, nil)
}

func (n *nibssAdmin) put(ctx context.Context, path string, in, out any) error {
	_, err := n.link.Do(ctx, n.base, http.MethodPut, path, in, out)
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
