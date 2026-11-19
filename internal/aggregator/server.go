package aggregator

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net"
	"net/http"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/pki"
)

func (a *App) PartnerTLS() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(a.cfg.Path("connect", "server.crt"), a.cfg.Path("connect", "server.key"))
	if err != nil {
		return nil, err
	}
	pool, err := pki.LoadPool(a.cfg.Path("ca", "connect-partners.crt"))
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func (a *App) WidgetTLS() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(a.cfg.Path("connect", "server.crt"), a.cfg.Path("connect", "server.key"))
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

func (a *App) Serve(ctx context.Context) error {
	ptls, err := a.PartnerTLS()
	if err != nil {
		return err
	}
	var wtls *tls.Config
	if a.cfg.Connect.WidgetTLS {
		if wtls, err = a.WidgetTLS(); err != nil {
			return err
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 8)
	type srv struct {
		s   *http.Server
		tls *tls.Config
		ls  []net.Listener
	}
	specs := []struct {
		port int
		h    http.Handler
		tls  *tls.Config
	}{
		{a.cfg.Connect.PartnerPort, a.PartnerHandler(), ptls},
		{a.cfg.Connect.WidgetPort, a.WidgetHandler(), wtls},
		{a.cfg.Connect.ConnectControlPort, a.ControlHandler(), nil},
	}
	servers := []srv{}
	for _, sp := range specs {
		ls, err := a.link.Listen(sp.port)
		if err != nil {
			for _, s := range servers {
				for _, l := range s.ls {
					l.Close()
				}
			}
			return err
		}
		servers = append(servers, srv{s: &http.Server{Handler: sp.h, ReadHeaderTimeout: 10 * time.Second, ErrorLog: control.ErrorLog(a.log)}, tls: sp.tls, ls: ls})
	}
	for _, s := range servers {
		for _, l := range s.ls {
			ln := l
			if s.tls != nil {
				ln = tls.NewListener(l, s.tls)
			}
			go func(s *http.Server, l net.Listener) {
				if err := s.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
					errc <- err
				}
			}(s.s, ln)
		}
	}
	done := make(chan struct{})
	go func() {
		a.Run(runCtx)
		close(done)
	}()
	var serr error
	select {
	case <-ctx.Done():
	case serr = <-errc:
	}
	shut, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	for _, s := range servers {
		_ = s.s.Shutdown(shut)
	}
	cancel()
	<-done
	return serr
}
