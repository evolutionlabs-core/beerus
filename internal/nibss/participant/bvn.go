package participant

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (p *Participant) enrolBVN(w http.ResponseWriter, r *http.Request) {
	if !p.admit(w, r, "enrol a BVN") {
		return
	}
	var req nip.BVNRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}.`)
		return
	}
	dob, err := clock.ParseDate(req.DOB)
	if err != nil {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", web.Sentence(fmt.Sprintf("dob %q must be a date like 1990-04-21", req.DOB)))
		return
	}
	phone, err := p.store.NormalisePhone(req.Phone)
	var bvn string
	if err == nil {
		bvn, err = p.store.EnrolBVN(r.Context(), req.Name, phone, dob)
	}
	if errors.Is(err, nibss.ErrBadDetails) {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", web.Sentence(strings.TrimPrefix(err.Error(), nibss.ErrBadDetails.Error()+": ")))
		return
	}
	if err != nil {
		p.unavailable(w, r, "BVN not enrolled", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, nip.BVNReply{BVN: bvn, Phone: phone})
}
