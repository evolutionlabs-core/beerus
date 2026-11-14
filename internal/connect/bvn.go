package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

var errNIBSSNotOpen = errors.New("NIBSS is not open yet, so no BVN can be enrolled; try again soon")

func (a *App) enrolBVN(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
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

func (a *App) ctlEnrolBVN(w http.ResponseWriter, r *http.Request) {
	var req server.BVNRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}.`)
		return
	}
	dob, err := clock.ParseDate(req.DOB)
	if err != nil {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", web.Sentence(fmt.Sprintf("dob %q must be a date like 1990-04-21", req.DOB)))
		return
	}
	bvn, phone, err := a.enrolBVN(r.Context(), req.Name, req.Phone, dob)
	if errors.Is(err, errNIBSSNotOpen) {
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", web.Sentence(err.Error()))
		return
	}
	if err != nil {
		a.banking.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, server.BVNReply{BVN: bvn, Phone: phone})
}
