package participant

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/evolutionlabs-core/beerus/internal/nibss/gateway"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (p *Participant) directory() http.Handler {
	api := gateway.New(p.store.Directory, p.log)
	return web.WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := callerIdentity(r)
		if !ok {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", "This certificate may not read the bank directory. Only banks may.")
			return
		}
		admitted, err := p.store.Admits(r.Context(), identity)
		if err != nil {
			p.log.ErrorContext(r.Context(), "certificate identity not checked", "err", err)
			web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "NIBSS is not available right now. Try again soon.")
			return
		}
		if !admitted {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", fmt.Sprintf("This certificate may not read the bank directory. %s is not a bank NIBSS knows.", identity))
			return
		}
		api.ServeHTTP(w, r)
	}))
}

func callerIdentity(r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return "", false
	}
	return r.TLS.PeerCertificates[0].Subject.CommonName, true
}

func (p *Participant) listen(port int) ([]net.Listener, error) {
	hosts := []string{"127.0.0.1", "::1"}
	if p.bind != "" {
		hosts = []string{p.bind}
	}
	out := []net.Listener{}
	for _, host := range hosts {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			if host == "::1" && len(out) > 0 {
				continue
			}
			for _, l := range out {
				l.Close()
			}
			return nil, fmt.Errorf("listen on port %d: %w", port, err)
		}
		out = append(out, ln)
	}
	return out, nil
}

func (p *Participant) Serve(ctx context.Context) error {
	defer p.Close()
	dirLn, err := p.listen(p.file.Port)
	if err != nil {
		return err
	}
	ctlLn, err := p.listen(p.file.ControlPort)
	if err != nil {
		for _, l := range dirLn {
			l.Close()
		}
		return err
	}
	servers := []*http.Server{
		{Handler: p.directory(), ReadHeaderTimeout: headerTimeout, ReadTimeout: readLimit, WriteTimeout: writeLimit, IdleTimeout: idleLimit, ErrorLog: slog.NewLogLogger(p.log.Handler(), slog.LevelWarn)},
		{Handler: p.Handler(), ReadHeaderTimeout: headerTimeout, ReadTimeout: readLimit, WriteTimeout: writeLimit, IdleTimeout: idleLimit, ErrorLog: slog.NewLogLogger(p.log.Handler(), slog.LevelWarn)},
	}
	var wg sync.WaitGroup
	failed := make(chan error, len(dirLn)+len(ctlLn))
	serve := func(s *http.Server, ls []net.Listener, tc *tls.Config) {
		for _, ln := range ls {
			if tc != nil {
				ln = tls.NewListener(ln, tc)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					failed <- err
				}
			}()
		}
	}
	serve(servers[0], dirLn, p.tls)
	serve(servers[1], ctlLn, nil)
	var serr error
	select {
	case <-ctx.Done():
	case serr = <-failed:
	}
	shut, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownWait)
	defer stop()
	for _, s := range servers {
		_ = s.Shutdown(shut)
	}
	wg.Wait()
	return serr
}
