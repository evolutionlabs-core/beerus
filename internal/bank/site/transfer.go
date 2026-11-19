package site

import (
	"context"
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/money"
	"net/http"
	"net/url"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/view"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	accountNumberLen = 10
	formKeyBytes     = 16
)

var (
	errChooseAccount = errors.New("choose one of your accounts to pay from")
	errChooseOwn     = errors.New("choose which of your accounts to pay into")
	errTenDigits     = errors.New("enter a 10-digit account number")
	errFormExpired   = errors.New("the form expired; go back and try again")
)

func userFixable(err error) bool {
	var te *control.TransferError
	return errors.As(err, &te) || errors.Is(err, view.ErrBadAmount) || errors.Is(err, errChooseAccount) || errors.Is(err, errChooseOwn) || errors.Is(err, errTenDigits) || errors.Is(err, errFormExpired)
}

func (s *site) transferPage(ctx context.Context, sess *session, d data) (data, error) {
	if err := s.Host.RefreshBanks(ctx); err != nil {
		return nil, err
	}
	_, accts, err := s.mine(ctx, sess.Customer)
	if err != nil {
		return nil, err
	}
	d["Title"] = "Send money"
	d["Nav"] = "transfer"
	d["Accounts"] = accts
	d["Banks"] = control.OpenBanks(s.Host.Directory())
	return d, nil
}

func (s *site) transferForm(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	d, err := s.transferPage(r.Context(), sess, data{"Form": url.Values{"from": {r.URL.Query().Get("from")}, "bank": {s.id}}})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "bank_transfer", http.StatusOK, sess, d)
}

func (s *site) transferRequest(ctx context.Context, sess *session, f url.Values) (control.TransferRequest, error) {
	_, acc, ok, err := s.one(ctx, sess.Customer, f.Get("from"))
	if err != nil {
		return control.TransferRequest{}, err
	}
	if !ok {
		return control.TransferRequest{}, errChooseAccount
	}
	amount, err := money.ParseNaira(f.Get("amount"))
	if err != nil {
		return control.TransferRequest{}, view.ErrBadAmount
	}
	req := control.TransferRequest{FromBank: s.id, From: acc.NUBAN, Amount: amount, Narration: strings.TrimSpace(f.Get("narration"))}
	if f.Get("mode") == "own" {
		req.ToBank, req.To = s.id, f.Get("own")
		if req.To == "" {
			return req, errChooseOwn
		}
		return req, nil
	}
	req.ToBank, req.To = f.Get("bank"), strings.TrimSpace(f.Get("number"))
	if len(req.To) != accountNumberLen {
		return req, errTenDigits
	}
	return req, nil
}

func (s *site) transferFailed(w http.ResponseWriter, r *http.Request, sess *session, err error) {
	if !userFixable(err) {
		s.fail(w, r, err)
		return
	}
	d, perr := s.transferPage(r.Context(), sess, data{"Error": web.Sentence(err.Error()), "Form": r.PostForm})
	if perr != nil {
		s.fail(w, r, perr)
		return
	}
	s.render(w, "bank_transfer", http.StatusUnprocessableEntity, sess, d)
}

func (s *site) transferReview(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	req, err := s.transferRequest(r.Context(), sess, r.PostForm)
	if err == nil {
		err = s.Host.RefreshBanks(r.Context())
	}
	var q control.TransferResult
	if err == nil {
		q, err = s.Host.Quote(r.Context(), req)
	}
	if err != nil {
		s.transferFailed(w, r, sess, err)
		return
	}
	to := q.ToBank
	if b := s.Host.Lookup(q.ToBank); b != nil {
		to = b.Name
	}
	key, err := web.NewSecret("", formKeyBytes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "bank_review", http.StatusOK, sess, data{"Title": "Review transfer", "Nav": "transfer", "Q": q, "ToBank": to, "Form": r.PostForm, "Key": key})
}

func (s *site) transferConfirm(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	req, err := s.transferRequest(r.Context(), sess, r.PostForm)
	var res control.TransferResult
	if err == nil {
		req.Key, err = sess.transferKey(r.PostForm.Get("key"))
	}
	if err == nil {
		res, err = s.Host.Transfer(r.Context(), req)
	}
	if err != nil {
		s.transferFailed(w, r, sess, err)
		return
	}
	http.Redirect(w, r, "/accounts/"+res.From+"?transfer="+url.QueryEscape(res.Status), http.StatusSeeOther)
}

func (sess *session) transferKey(formKey string) (string, error) {
	if formKey == "" {
		return "", errFormExpired
	}
	return "site:" + web.HashSecret(sess.CSRF+":"+formKey), nil
}
