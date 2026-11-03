package connect

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const oauthCookie = "beerus_oauth"

func (a *bankSite) oauthError(w http.ResponseWriter, s *bankSession, status int, msg string) {
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/", MaxAge: -1})
	a.bankRender(w, "bank_oauth_error", status, s, roomPage{"Title": "Request not valid", "Error": msg})
}

func (a *bankSite) obAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uri, client := q.Get("request_uri"), q.Get("client_id")
	if len(q) != 2 || uri == "" || client == "" {
		a.oauthError(w, nil, http.StatusBadRequest, "This link is not valid. Start again from the app.")
		return
	}
	if _, err := a.ob.Request(r.Context(), uri, client); err != nil {
		a.oauthError(w, nil, http.StatusBadRequest, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: url.Values{"client_id": {client}, "request_uri": {uri}}.Encode(), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	s, _ := a.bankSessionFor(r)
	if s == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	a.auth.mu.Lock()
	s.Verified, s.Fresh, s.Attempts = false, "", 0
	s.OTP, s.OTPExpires = otp(), a.clock.Real().Add(otpLife)
	a.auth.mu.Unlock()
	http.Redirect(w, r, "/verify", http.StatusSeeOther)
}

func (a *bankSite) pendingRequest(w http.ResponseWriter, r *http.Request, s *bankSession) (*openbanking.Request, string) {
	ck, err := r.Cookie(oauthCookie)
	if err != nil {
		a.oauthError(w, s, http.StatusBadRequest, "This request has expired. Start again from the app.")
		return nil, ""
	}
	v, err := url.ParseQuery(ck.Value)
	if err != nil || v.Get("request_uri") == "" {
		a.oauthError(w, s, http.StatusBadRequest, "This request has expired. Start again from the app.")
		return nil, ""
	}
	if s.Fresh != v.Get("request_uri") {
		http.Redirect(w, r, "/authorize?"+v.Encode(), http.StatusSeeOther)
		return nil, ""
	}
	req, err := a.ob.Request(r.Context(), v.Get("request_uri"), v.Get("client_id"))
	if err != nil {
		a.oauthError(w, s, http.StatusBadRequest, err.Error())
		return nil, ""
	}
	return req, v.Get("request_uri")
}

type consentChoice struct {
	ID, Name, Type string
	Checked        bool
}

func (a *bankSite) consentPage(w http.ResponseWriter, s *bankSession, status int, req *openbanking.Request, errMsg string) {
	mine, _ := a.ob.Data().Accounts(s.Persona)
	choices := []consentChoice{}
	for _, m := range mine {
		choices = append(choices, consentChoice{ID: m.ID, Name: m.Name, Type: m.Type, Checked: true})
	}
	c := req.Consent
	data := roomPage{
		"Title": "Share your account data", "Nav": "", "Req": req, "Client": req.Client, "Consent": c, "Choices": choices, "Error": errMsg,
		"Start": c.TransactionFrom.String(), "Effective": req.Effective.String(), "Capped": req.Capped, "Until": req.Until.Format("2 January 2006"),
		"Partner": c.PartnerName, "PartnerLegal": c.PartnerLegal, "Purpose": c.Purpose,
	}
	if c.PartnerName == "" {
		data["Partner"] = req.Client.Name
	}
	if c.PartnerLegal == "" {
		data["PartnerLegal"] = req.Client.LegalName
	}
	if c.Purpose == "" {
		data["Purpose"] = "to read your account details, balances and transactions"
	}
	a.bankRender(w, "bank_consent", status, s, data)
}

func (a *bankSite) obConsentPage(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	req, _ := a.pendingRequest(w, r, s)
	if req == nil {
		return
	}
	a.consentPage(w, s, http.StatusOK, req, "")
}

func (a *bankSite) obConsentSubmit(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	req, uri := a.pendingRequest(w, r, s)
	if req == nil {
		return
	}
	var loc string
	var err error
	if r.PostForm.Get("action") == "approve" {
		loc, err = a.ob.Approve(r.Context(), uri, s.Persona, r.PostForm["account"], r.PostForm.Get("analytics") == "yes")
	} else {
		loc, err = a.ob.Reject(r.Context(), uri)
	}
	switch {
	case errors.Is(err, openbanking.ErrNoAccounts):
		a.consentPage(w, s, http.StatusBadRequest, req, "Choose at least one account.")
		return
	case err != nil:
		a.oauthError(w, s, http.StatusBadRequest, openbanking.ErrInvalidRequest.Error())
		return
	}
	a.auth.mu.Lock()
	s.Fresh = ""
	a.auth.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/", MaxAge: -1})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, loc, http.StatusSeeOther)
}
