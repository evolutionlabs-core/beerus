package site

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const oauthCookie = "beerus_oauth"

func (s *site) oauthError(w http.ResponseWriter, sess *session, status int, msg string) {
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/", MaxAge: -1})
	s.render(w, "bank_oauth_error", status, sess, data{"Title": "Request not valid", "Error": msg})
}

func (s *site) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uri, client := q.Get("request_uri"), q.Get("client_id")
	if len(q) != 2 || uri == "" || client == "" {
		s.oauthError(w, nil, http.StatusBadRequest, "This link is not valid. Start again from the app.")
		return
	}
	if _, err := s.Open.Request(r.Context(), uri, client); err != nil {
		s.oauthError(w, nil, http.StatusBadRequest, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: url.Values{"client_id": {client}, "request_uri": {uri}}.Encode(), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	sess, _ := s.sessionFor(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.auth.mu.Lock()
	sess.Verified, sess.Fresh, sess.Attempts = false, "", 0
	sess.OTP, sess.OTPExpires = otp(), s.Clock.Real().Add(otpLife)
	s.auth.mu.Unlock()
	http.Redirect(w, r, "/verify", http.StatusSeeOther)
}

func (s *site) pendingRequest(w http.ResponseWriter, r *http.Request, sess *session) (*openbanking.Request, string) {
	ck, err := r.Cookie(oauthCookie)
	if err != nil {
		s.oauthError(w, sess, http.StatusBadRequest, "This request has expired. Start again from the app.")
		return nil, ""
	}
	v, err := url.ParseQuery(ck.Value)
	if err != nil || v.Get("request_uri") == "" {
		s.oauthError(w, sess, http.StatusBadRequest, "This request has expired. Start again from the app.")
		return nil, ""
	}
	if sess.Fresh != v.Get("request_uri") {
		http.Redirect(w, r, "/authorize?"+v.Encode(), http.StatusSeeOther)
		return nil, ""
	}
	req, err := s.Open.Request(r.Context(), v.Get("request_uri"), v.Get("client_id"))
	if err != nil {
		s.oauthError(w, sess, http.StatusBadRequest, err.Error())
		return nil, ""
	}
	return req, v.Get("request_uri")
}

var scopeLabels = map[string]string{"accounts": "account details", "balances": "balances", "transactions": "transactions"}

func sharedData(scopes []string) string {
	labels := []string{}
	for _, s := range scopes {
		labels = append(labels, scopeLabels[s])
	}
	switch len(labels) {
	case 0:
		return "nothing"
	case 1:
		return labels[0]
	}
	return strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
}

type consentChoice struct {
	ID, Name, Type string
	Checked        bool
}

func (s *site) renderConsent(w http.ResponseWriter, sess *session, status int, req *openbanking.Request, errMsg string) {
	mine, err := s.Open.Data().Accounts(sess.Customer)
	if err != nil {
		s.Log.Error("consent page", "bank", s.id, "err", err)
		http.Error(w, "Something went wrong at the bank. Try again soon.", http.StatusInternalServerError)
		return
	}
	choices := []consentChoice{}
	for _, m := range mine {
		choices = append(choices, consentChoice{ID: m.ID, Name: m.Name, Type: m.Type, Checked: true})
	}
	c := req.Consent
	d := data{
		"Title": "Share your account data", "Nav": "", "Req": req, "Client": req.Client, "Consent": c, "Choices": choices, "Error": errMsg,
		"Start": c.TransactionFrom.String(), "Effective": req.Effective.String(), "Capped": req.Capped, "Months": req.Months, "Until": req.Until.Format("2 January 2006"), "Shared": strings.ToUpper(sharedData(c.Scopes)[:1]) + sharedData(c.Scopes)[1:],
		"Partner": c.PartnerName, "PartnerLegal": c.PartnerLegal, "Purpose": c.Purpose,
	}
	if c.PartnerName == "" {
		d["Partner"] = req.Client.Name
	}
	if c.PartnerLegal == "" {
		d["PartnerLegal"] = req.Client.LegalName
	}
	if c.Purpose == "" {
		d["Purpose"] = "to read your " + sharedData(c.Scopes)
	}
	s.render(w, "bank_consent", status, sess, d)
}

func (s *site) consentPage(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	req, _ := s.pendingRequest(w, r, sess)
	if req == nil {
		return
	}
	s.renderConsent(w, sess, http.StatusOK, req, "")
}

func (s *site) consentSubmit(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	req, uri := s.pendingRequest(w, r, sess)
	if req == nil {
		return
	}
	var loc string
	var err error
	if r.PostForm.Get("action") == "approve" {
		loc, err = s.Open.Approve(r.Context(), uri, sess.Customer, r.PostForm["account"], r.PostForm.Get("analytics") == "yes")
	} else {
		loc, err = s.Open.Reject(r.Context(), uri)
	}
	switch {
	case errors.Is(err, openbanking.ErrNoAccounts):
		s.renderConsent(w, sess, http.StatusBadRequest, req, "Choose at least one account.")
		return
	case err != nil:
		s.oauthError(w, sess, http.StatusBadRequest, openbanking.ErrInvalidRequest.Error())
		return
	}
	s.auth.mu.Lock()
	sess.Fresh = ""
	s.auth.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/", MaxAge: -1})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, loc, http.StatusSeeOther)
}
