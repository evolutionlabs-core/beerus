package participant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	nubanDigits   = 10
	nipCodeDigits = 6
)

var (
	nubanRe   = regexp.MustCompile(`^[0-9]{10}$`)
	nipCodeRe = regexp.MustCompile(`^[0-9]{6}$`)
)

func (p *Participant) nameEnquiry(w http.ResponseWriter, r *http.Request) {
	if p.admit(w, r, "ask for account names") {
		p.answerEnquiry(w, r)
	}
}

func (p *Participant) controlNameEnquiry(w http.ResponseWriter, r *http.Request) {
	p.answerEnquiry(w, r)
}

func (p *Participant) answerEnquiry(w http.ResponseWriter, r *http.Request) {
	code, nuban := r.URL.Query().Get("bank_code"), r.URL.Query().Get("account_number")
	switch {
	case !nipCodeRe.MatchString(code):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("bank_code must have %d digits.", nipCodeDigits))
		return
	case !nubanRe.MatchString(nuban):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("account_number must have %d digits.", nubanDigits))
		return
	}
	out, err := p.enquire(r.Context(), code, nuban)
	if err != nil {
		p.unavailable(w, r, "name enquiry not answered", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, out)
}

func (p *Participant) enquire(ctx context.Context, code, nuban string) (nip.NameEnquiry, error) {
	out := nip.NameEnquiry{BankCode: code, AccountNo: nuban}
	m, err := p.store.Callee(ctx, code, nuban)
	if errors.Is(err, nip.ErrUnknownInstitution) {
		out.Code, out.Message = nip.SystemError, "Bank code "+code+" is not in the NIBSS directory."
		return out, nil
	}
	if m.ID == "" {
		return out, err
	}
	out.BankName = m.Name
	if err != nil {
		out.Code, out.Message = refusal(m, err)
		return out, nil
	}
	name, err := p.banks.nameAt(ctx, m, nuban)
	if err == nil {
		out.Code, out.Message, out.AccountName = nip.OK, "Successful.", name
		return out, nil
	}
	if !errors.Is(err, nip.ErrInvalidAccount) && !errors.Is(err, nip.ErrNoRecord) && !errors.Is(err, nip.ErrUnavailable) {
		p.log.WarnContext(ctx, "bank gave no usable name", "bank", m.ID, "err", err)
	}
	out.Code, out.Message = refusal(m, err)
	return out, nil
}

func refusal(m nibss.Member, err error) (string, string) {
	switch {
	case errors.Is(err, nip.ErrInvalidAccount):
		return nip.InvalidAccount, "That is not a valid account number at " + m.Name + "."
	case errors.Is(err, nip.ErrNoRecord):
		return nip.NoRecord, "No such account at " + m.Name + "."
	}
	return nip.Unavailable, m.Name + " is not available right now."
}
