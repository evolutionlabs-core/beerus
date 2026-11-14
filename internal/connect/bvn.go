package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
)

var errNIBSSNotOpen = errors.New("NIBSS is not open yet, so no BVN can be enrolled; try again soon")

type unavailableError struct {
	msg string
}

func (e *unavailableError) Error() string {
	return e.msg
}

type bvnRequest struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	DOB   string `json:"dob"`
}

type bvnReply struct {
	BVN   string `json:"bvn"`
	Phone string `json:"phone"`
}

func (a *App) enrolBVN(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
	if a.role == RoleBank {
		return a.enrolBVNAtCore(ctx, name, phone, dob)
	}
	return a.enrolBVNLocal(ctx, name, phone, dob)
}

func (a *App) enrolBVNLocal(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
	sw := a.nibssStore()
	if sw == nil {
		return "", "", errNIBSSNotOpen
	}
	normal, err := sw.NormalisePhone(phone)
	if errors.Is(err, nibss.ErrBadDetails) {
		return "", "", fmt.Errorf("%w: %s", ledger.ErrInvalid, strings.TrimPrefix(err.Error(), nibss.ErrBadDetails.Error()+": "))
	}
	if err != nil {
		return "", "", fmt.Errorf("normalise phone: %w", err)
	}
	bvn, err := sw.EnrolBVN(ctx, name, normal, dob)
	if errors.Is(err, nibss.ErrBadDetails) {
		return "", "", fmt.Errorf("%w: %s", ledger.ErrInvalid, strings.TrimPrefix(err.Error(), nibss.ErrBadDetails.Error()+": "))
	}
	if err != nil {
		return "", "", fmt.Errorf("enrol a BVN at NIBSS: %w", err)
	}
	return bvn, normal, nil
}

func (a *App) enrolBVNAtCore(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
	var out bvnReply
	_, err := a.coreDo(ctx, http.MethodPost, "/internal/bvns", bvnRequest{Name: name, Phone: phone, DOB: dob.String()}, &out)
	var pe *peerError
	if errors.As(err, &pe) && pe.Status == http.StatusUnprocessableEntity {
		return "", "", fmt.Errorf("%w: %s", ledger.ErrInvalid, pe.Message)
	}
	if errors.As(err, &pe) && pe.Status == http.StatusServiceUnavailable {
		return "", "", &unavailableError{msg: pe.Message}
	}
	if err != nil {
		return "", "", fmt.Errorf("enrol a BVN at NIBSS: %w", err)
	}
	return out.BVN, out.Phone, nil
}

func (a *App) ctlEnrolBVN(w http.ResponseWriter, r *http.Request) {
	var req bvnRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}.`)
		return
	}
	dob, err := clock.ParseDate(req.DOB)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_request", sentence(fmt.Sprintf("dob %q must be a date like 1990-04-21", req.DOB)))
		return
	}
	bvn, phone, err := a.enrolBVNLocal(r.Context(), req.Name, req.Phone, dob)
	if errors.Is(err, errNIBSSNotOpen) {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", sentence(err.Error()))
		return
	}
	if err != nil {
		a.bankingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bvnReply{BVN: bvn, Phone: phone})
}
