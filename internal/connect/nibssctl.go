package connect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss/participant"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	nibssBanksPath = "/v1/control/banks/"
	localhostAddr  = "127.0.0.1:0"
)

type nibssAdmin struct {
	link server.Link
	base string
	port int
}

type nibssDownError struct {
	Port int
	Err  error
}

func (e *nibssDownError) Error() string {
	return fmt.Sprintf("NIBSS is not answering on port %d; it may still be starting", e.Port)
}

func (e *nibssDownError) Unwrap() error {
	return e.Err
}

type registerBody struct {
	NIPCode             string `json:"nip_code"`
	NUBANCode           string `json:"nuban_code"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	CreditURL           string `json:"credit_url"`
	CertificateIdentity string `json:"certificate_identity"`
}

type statusBody struct {
	Status string `json:"status"`
}

func (n *nibssAdmin) register(ctx context.Context, b *config.Bank) (nip.Institution, error) {
	var in nip.Institution
	body := registerBody{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Name: b.Name, Type: b.Type, CreditURL: b.APIURL(), CertificateIdentity: b.ID}
	return in, n.put(ctx, nibssBanksPath+url.PathEscape(b.ID), body, &in)
}

func (n *nibssAdmin) setStatus(ctx context.Context, id, status string) error {
	return n.put(ctx, nibssBanksPath+url.PathEscape(id)+"/status", statusBody{Status: status}, nil)
}

func (n *nibssAdmin) put(ctx context.Context, path string, in, out any) error {
	_, err := n.link.Do(ctx, n.base, http.MethodPut, path, in, out)
	var peer *server.PeerError
	if err == nil || errors.As(err, &peer) || ctx.Err() != nil {
		return err
	}
	return &nibssDownError{Port: n.port, Err: err}
}

type handlerTransport struct {
	h http.Handler
}

type handlerReply struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (r *handlerReply) Header() http.Header {
	return r.header
}

func (r *handlerReply) Write(b []byte) (int, error) {
	return r.body.Write(b)
}

func (r *handlerReply) WriteHeader(code int) {
	r.code = code
}

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	in := req.Clone(req.Context())
	in.RemoteAddr = localhostAddr
	if in.Body == nil {
		in.Body = http.NoBody
	}
	reply := &handlerReply{header: http.Header{}, code: http.StatusOK}
	t.h.ServeHTTP(reply, in)
	return &http.Response{
		Status:        strconv.Itoa(reply.code) + " " + http.StatusText(reply.code),
		StatusCode:    reply.code,
		Header:        reply.header,
		Body:          io.NopCloser(&reply.body),
		ContentLength: int64(reply.body.Len()),
		Request:       req,
	}, nil
}

func (a *App) ensureNIBSSPort() error {
	port := 0
	err := a.ctl.update(func(st *setup.Status) (bool, error) {
		r := ensureResource(st, setup.KindSystem, setup.SystemNIBSS)
		if r.Allocated.Port != 0 {
			port = r.Allocated.Port
			return false, nil
		}
		c := a.cfg.Connect
		used := map[int]bool{c.PartnerPort: true, c.WidgetPort: true, c.ControlPort: true, c.HealthPort: true, c.NIBSSPort: true}
		for _, x := range st.Resources {
			used[x.Allocated.Port], used[x.Allocated.APIPort] = true, true
		}
		p, err := a.cfg.AllocatePort(a.cfg.Connect.NIBSSPort+1, used, portFree)
		if err != nil {
			return false, fmt.Errorf("NIBSS control port: %w", err)
		}
		r.Allocated.Port = p
		r.Owned = append(r.Owned, setup.Owned{Kind: ownedPort, Name: strconv.Itoa(p)})
		port = p
		return true, nil
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.Connect.NIBSSControlPort = port
	a.mu.Unlock()
	return nil
}

func (a *App) ensureNIBSSDir() error {
	now := a.clock.Real()
	if !a.nibssEnsured.IsZero() && now.Sub(a.nibssEnsured) < bankEnsureEvery {
		return nil
	}
	a.mu.Lock()
	world := a.st.WorldID
	a.mu.Unlock()
	if err := a.cfg.EnsureNIBSSDir(world, now); err != nil {
		return fmt.Errorf("ensure the NIBSS directory: %w", err)
	}
	a.nibssEnsured = now
	return nil
}

func (a *App) startNIBSS(ctx context.Context) error {
	if err := a.ensureNIBSSPort(); err != nil {
		return err
	}
	if err := a.ensureNIBSSDir(); err != nil {
		return err
	}
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(a.cfg.Dir), config.NIBSSTokenFile))
	if err != nil {
		return fmt.Errorf("NIBSS control token: %w", err)
	}
	port := a.cfg.Connect.NIBSSControlPort
	n := &nibssAdmin{link: server.NewLink(token, ""), base: "http://127.0.0.1:" + strconv.Itoa(port), port: port}
	if a.role == RoleCore {
		a.nibssCtl = n
		return nil
	}
	p, err := participant.New(ctx, a.cfg.Dir, participant.Options{Logger: a.log, DatabaseURL: a.cfg.DatabaseURL})
	if err != nil {
		return err
	}
	n.link.Client = &http.Client{Timeout: n.link.Client.Timeout, Transport: handlerTransport{p.Handler()}}
	a.nibssProc, a.nibssCtl = p, n
	return nil
}
