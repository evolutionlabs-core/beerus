package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

type BVNRequest struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	DOB   string `json:"dob"`
}

type BVNReply struct {
	BVN   string `json:"bvn"`
	Phone string `json:"phone"`
}

type NameEnquiry struct {
	Code        string `json:"response_code,omitempty"`
	Message     string `json:"message"`
	BankCode    string `json:"bank_code"`
	BankName    string `json:"bank_name,omitempty"`
	AccountNo   string `json:"account_number"`
	AccountName string `json:"account_name,omitempty"`
}

func (s *Server) CoreDo(ctx context.Context, method, path string, in, out any) (int, error) {
	return s.link.Do(ctx, s.coreURL, method, path, in, out)
}

func (s *Server) coreAway(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &UnavailableError{Msg: s.Bank().Name + " cannot reach NIBSS right now. Try again in a minute.", Err: err}
}

func (s *Server) Enrol(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
	var out BVNReply
	_, err := s.CoreDo(ctx, http.MethodPost, "/internal/bvns", BVNRequest{Name: name, Phone: phone, DOB: dob.String()}, &out)
	var pe *PeerError
	if errors.As(err, &pe) && pe.Status == http.StatusUnprocessableEntity {
		return "", "", fmt.Errorf("%w: %s", ledger.ErrInvalid, pe.Message)
	}
	if errors.As(err, &pe) && pe.Status == http.StatusServiceUnavailable {
		return "", "", &UnavailableError{Msg: pe.Message}
	}
	if err != nil && !errors.As(err, &pe) {
		return "", "", s.coreAway(err)
	}
	if err != nil {
		return "", "", fmt.Errorf("enrol a BVN at NIBSS: %w", err)
	}
	return out.BVN, out.Phone, nil
}

func (s *Server) Enquire(ctx context.Context, rt Route, nuban string) (string, error) {
	var out NameEnquiry
	q := url.Values{"bank_code": {rt.ToCode}, "account_number": {nuban}}
	_, err := s.CoreDo(ctx, http.MethodGet, "/nip/name-enquiry?"+q.Encode(), nil, &out)
	var pe *PeerError
	if err != nil && !errors.As(err, &pe) {
		return "", s.coreAway(err)
	}
	if err != nil {
		return "", err
	}
	if out.Code != nip.OK {
		return "", Refused(http.StatusUnprocessableEntity, out.Code, "name_enquiry_failed", errors.New(out.Message))
	}
	return out.AccountName, nil
}

func (s *Server) Transfer(ctx context.Context, req TransferRequest) (TransferResult, error) {
	body := TransferBody{From: req.From, To: req.To, ToBank: req.ToBank, Amount: fmt.Sprintf("%d.%02d", req.Amount/100, req.Amount%100), Narration: req.Narration}
	var out TransferResult
	status, err := s.CoreDo(ctx, http.MethodPost, "/banks/"+url.PathEscape(req.FromBank)+"/transfers", body, &out)
	var pe *PeerError
	if err != nil && !errors.As(err, &pe) {
		return out, s.coreAway(err)
	}
	if err != nil && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		return out, Refused(status, nip.SystemError, "transfer_refused", err)
	}
	return out, err
}
