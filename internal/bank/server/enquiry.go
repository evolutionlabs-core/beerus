package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func (s *Sender) Quote(ctx context.Context, req TransferRequest) (TransferResult, error) {
	if err := CheckRequest(req); err != nil {
		return TransferResult{}, err
	}
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	rt, err := s.route(ctx, req)
	if err != nil {
		return TransferResult{}, err
	}
	return Quoter{Fees: s.Fees, Clock: s.Clock, Enquirer: quoteEnquirer{s}}.Quote(ctx, s.Books(), rt, req)
}

type quoteEnquirer struct {
	s *Sender
}

func (q quoteEnquirer) Enquire(ctx context.Context, rt Route, nuban string) (string, error) {
	return q.s.enquire(ctx, rt, nuban, "")
}

func (s *Sender) enquire(ctx context.Context, rt Route, nuban, tail string) (string, error) {
	c, err := s.NIBSS()
	if err != nil {
		return "", err
	}
	out, err := c.Enquire(ctx, rt.ToCode, nuban)
	var away *UnavailableError
	if errors.As(err, &away) {
		return "", Refused(http.StatusServiceUnavailable, nip.Unavailable, "nibss_unavailable", errors.New("NIBSS is not available right now"+tail))
	}
	var refused *PeerError
	if errors.As(err, &refused) {
		return "", Refused(http.StatusBadGateway, nip.SystemError, "bad_gateway", errors.New("NIBSS refused the name enquiry"))
	}
	if err != nil {
		return "", err
	}
	return enquiryResult(rt.ToName, out)
}

func enquiryResult(name string, out nip.NameEnquiry) (string, error) {
	switch out.Code {
	case nip.OK:
		return out.AccountName, nil
	case nip.Unavailable:
		return "", Refused(http.StatusServiceUnavailable, nip.Unavailable, "bank_unavailable", fmt.Errorf("%s is not available right now, so nothing was debited", name))
	case nip.InvalidAccount:
		return "", Refused(http.StatusUnprocessableEntity, nip.InvalidAccount, "invalid_account", fmt.Errorf("that is not a valid account at %s, so nothing was debited", name))
	case nip.NoRecord:
		return "", Refused(http.StatusUnprocessableEntity, nip.NoRecord, "no_record", fmt.Errorf("%s has no such account, so nothing was debited", name))
	}
	return "", Refused(http.StatusUnprocessableEntity, nip.SystemError, "unknown_bank", errors.New(out.Message))
}
