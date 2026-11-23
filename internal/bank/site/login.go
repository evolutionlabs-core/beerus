package site

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	cookieName       = "beerus_bank"
	otpLife          = 5 * time.Minute
	maxAttempts      = 5
	lockout          = 15 * time.Minute
	maxTrackedLogins = 10000
	sessionLife      = 30 * time.Minute
	sweepEvery       = time.Minute
	maxShownUsers    = 5
)

type session struct {
	Customer   string
	Name       string
	CSRF       string
	Verified   bool
	OTP        string
	OTPExpires time.Time
	Attempts   int
	Expires    time.Time
	Fresh      string
}

type failures struct {
	Count int
	First time.Time
}

type auth struct {
	mu       sync.Mutex
	sessions map[string]*session
	failures map[string]failures
	swept    time.Time
	locked   map[string]time.Time
}

func newAuth() *auth {
	return &auth{sessions: map[string]*session{}, failures: map[string]failures{}, locked: map[string]time.Time{}}
}

func (ba *auth) sweepLocked(real time.Time) {
	if real.Sub(ba.swept) < sweepEvery {
		return
	}
	ba.swept = real
	for k, f := range ba.failures {
		if real.Sub(f.First) > lockout {
			delete(ba.failures, k)
		}
	}
	for k, until := range ba.locked {
		if !real.Before(until) {
			delete(ba.locked, k)
		}
	}
	for k, s := range ba.sessions {
		if real.After(s.Expires) {
			delete(ba.sessions, k)
		}
	}
}

func otp() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (s *site) sessionFor(r *http.Request) (*session, string) {
	ck, err := r.Cookie(cookieName)
	if err != nil {
		return nil, ""
	}
	ba := s.auth
	ba.mu.Lock()
	defer ba.mu.Unlock()
	h := web.HashSecret(ck.Value)
	sess := ba.sessions[h]
	if sess == nil || s.Clock.Real().After(sess.Expires) {
		delete(ba.sessions, h)
		return nil, ""
	}
	return sess, h
}

func (s *site) user(w http.ResponseWriter, r *http.Request) *session {
	sess, _ := s.sessionFor(r)
	if sess == nil || !sess.Verified {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(sess.CSRF)) != 1 {
			http.Error(w, "the form expired; go back and try again", http.StatusForbidden)
			return nil
		}
	}
	return sess
}

func (s *site) renderLogin(w http.ResponseWriter, r *http.Request, status int, msg string) {
	books, err := s.Host.Books()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	names, total, err := books.Usernames(r.Context(), maxShownUsers)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := data{"Title": "Sign in", "TestUsers": strings.Join(names, ", "), "TestUserCount": total, "TestUsersMore": total - len(names)}
	if msg != "" {
		d["Error"] = msg
	}
	s.render(w, "bank_login", status, nil, d)
}

func (s *site) loginPage(w http.ResponseWriter, r *http.Request) {
	s.renderLogin(w, r, http.StatusOK, "")
}

func (s *site) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	user := strings.TrimSpace(r.PostForm.Get("username"))
	if !bank.ValidUsername(user) {
		s.renderLogin(w, r, http.StatusUnauthorized, "The username or password is wrong.")
		return
	}
	ba := s.auth
	real := s.Clock.Real()
	ba.mu.Lock()
	ba.sweepLocked(real)
	until := ba.locked[user]
	full := len(ba.failures)+len(ba.locked) >= maxTrackedLogins
	ba.mu.Unlock()
	if full {
		s.renderLogin(w, r, http.StatusTooManyRequests, fmt.Sprintf("Too many sign-in attempts are being tracked (%d). Try again in 15 minutes.", maxTrackedLogins))
		return
	}
	if real.Before(until) {
		s.renderLogin(w, r, http.StatusTooManyRequests, "Too many attempts. Try again in 15 minutes.")
		return
	}
	books, err := s.Host.Books()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, err := books.Authenticate(r.Context(), user, r.PostForm.Get("password"))
	if errors.Is(err, bank.ErrBadLogin) {
		ba.mu.Lock()
		f := ba.failures[user]
		if f.Count == 0 {
			f.First = real
		}
		f.Count++
		if f.Count >= maxAttempts {
			ba.locked[user] = real.Add(lockout)
			delete(ba.failures, user)
		} else {
			ba.failures[user] = f
		}
		ba.mu.Unlock()
		s.renderLogin(w, r, http.StatusUnauthorized, "The username or password is wrong.")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	raw, err := web.NewSecret("bs_", 20)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	csrf, err := web.NewSecret("", 16)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	code, err := otp()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sess := &session{Customer: c.ID, Name: c.Name, CSRF: csrf, OTP: code, OTPExpires: real.Add(otpLife), Expires: real.Add(sessionLife)}
	ba.mu.Lock()
	delete(ba.failures, user)
	ba.sessions[web.HashSecret(raw)] = sess
	ba.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: raw, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLife.Seconds())})
	http.Redirect(w, r, "/verify", http.StatusSeeOther)
}

func (s *site) verifyPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFor(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "bank_verify", http.StatusOK, nil, data{"Title": "Verify it's you", "OTP": sess.OTP})
}

func (s *site) verifySubmit(w http.ResponseWriter, r *http.Request) {
	sess, h := s.sessionFor(r)
	if sess == nil || r.ParseForm() != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ba := s.auth
	ba.mu.Lock()
	if s.Clock.Real().After(sess.OTPExpires) {
		delete(ba.sessions, h)
		ba.mu.Unlock()
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if subtle.ConstantTimeCompare([]byte(sess.OTP), []byte(strings.TrimSpace(r.PostForm.Get("otp")))) != 1 {
		sess.Attempts++
		if sess.Attempts >= maxAttempts {
			delete(ba.sessions, h)
			ba.mu.Unlock()
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		code := sess.OTP
		ba.mu.Unlock()
		s.render(w, "bank_verify", http.StatusUnauthorized, nil, data{"Title": "Verify it's you", "OTP": code, "Error": "That code is wrong."})
		return
	}
	sess.Verified = true
	sess.OTP = ""
	ba.mu.Unlock()
	if ck, err := r.Cookie(oauthCookie); err == nil && ck.Value != "" {
		if v, err := url.ParseQuery(ck.Value); err == nil {
			ba.mu.Lock()
			sess.Fresh = v.Get("request_uri")
			ba.mu.Unlock()
			http.Redirect(w, r, "/authorize/consent", http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

func (s *site) logout(w http.ResponseWriter, r *http.Request) {
	if _, h := s.sessionFor(r); h != "" {
		ba := s.auth
		ba.mu.Lock()
		delete(ba.sessions, h)
		ba.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
