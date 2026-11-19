package site

import (
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/consent"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/view"
)

type Config struct {
	Host   server.Host
	Open   *openbanking.Server
	Sender *consent.Sender
	Link   control.Link
	Clock  *clock.Clock
	Log    *slog.Logger
	Brand  string
	API    http.Handler
}

type site struct {
	Config
	id   string
	port string
	auth *auth
}

type data map[string]any

func New(c Config) http.Handler {
	own := c.Host.Bank()
	s := &site{Config: c, id: own.ID, port: strconv.Itoa(own.Port), auth: newAuth()}
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", view.Assets())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/accounts", http.StatusSeeOther) })
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("GET /verify", s.verifyPage)
	mux.HandleFunc("POST /verify", s.verifySubmit)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /accounts", s.accounts)
	mux.HandleFunc("GET /accounts/{nuban}", s.statement)
	mux.HandleFunc("GET /accounts/{nuban}/tx/{key}", s.tx)
	mux.HandleFunc("GET /transfer", s.transferForm)
	mux.HandleFunc("POST /transfer/review", s.transferReview)
	mux.HandleFunc("POST /transfer/confirm", s.transferConfirm)
	mux.HandleFunc("GET /apps", s.apps)
	mux.HandleFunc("POST /apps/{id}/revoke", s.revoke)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /internal/revoke", s.internalRevoke)
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("GET /authorize/consent", s.consentPage)
	mux.HandleFunc("POST /authorize/consent", s.consentSubmit)
	if c.API != nil {
		mux.Handle("/v1/", c.API)
	}
	return s.guard(mux)
}

func (s *site) guard(next http.Handler) http.Handler {
	hosts := map[string]bool{s.id + ".localhost:" + s.port: true, "127.0.0.1:" + s.port: true, "localhost:" + s.port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.Link.Allowed(r) || !hosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		phase, _ := s.Host.Phase()
		if phase == config.PhaseClosed && !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.Error(w, s.Host.Bank().Name+" is closed.", http.StatusConflict)
			return
		}
		if phase == config.PhaseSuspended && !strings.HasPrefix(r.URL.Path, "/assets/") {
			s.render(w, "bank_unavailable", http.StatusServiceUnavailable, nil, data{"Title": s.Host.Bank().Name})
			return
		}
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/authorize") {
			if err := s.Host.RefreshRegistry(r.Context()); err != nil {
				s.Log.Error("registry not readable", "bank", s.id, "err", err)
				http.Error(w, "starting", http.StatusServiceUnavailable)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *site) render(w http.ResponseWriter, name string, status int, sess *session, d data) {
	own := s.Host.Bank()
	d["Bank"] = &own
	d["Now"] = s.Clock.Now()
	if sess != nil && sess.Verified {
		d["User"] = sess.Name
		d["CSRF"] = sess.CSRF
	}
	if _, ok := d["Title"]; !ok {
		d["Title"] = own.Name
	}
	view.SetHeaders(w.Header())
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, d); err != nil {
		s.Log.Error("render", "page", name, "err", err)
	}
}

func (s *site) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ue *control.UnavailableError
	if errors.As(err, &ue) {
		s.Log.Warn("online banking unavailable", "bank", s.id, "path", r.URL.Path, "err", ue.Err)
		http.Error(w, ue.Msg, http.StatusServiceUnavailable)
		return
	}
	s.Log.Error("online banking", "bank", s.id, "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong at the bank. Try again soon.", http.StatusInternalServerError)
}

var pages = template.Must(template.New("").Funcs(view.Funcs()).Funcs(template.FuncMap{
	"amtClass": func(v int64) string {
		if v > 0 {
			return "text-pos"
		}
		return "text-ink"
	},
	"title": strings.Title,
	"val": func(f url.Values, k string) string {
		if f == nil {
			return ""
		}
		return f.Get(k)
	},
}).Parse(templates))
