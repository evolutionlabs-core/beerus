package connect

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
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

func listenLocal(port int) ([]net.Listener, error) {
	out := []net.Listener{}
	for _, host := range []string{"127.0.0.1", "::1"} {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			if host == "::1" {
				continue
			}
			for _, x := range out {
				x.Close()
			}
			return nil, fmt.Errorf("listen on port %d: %w", port, err)
		}
		out = append(out, l)
	}
	return out, nil
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
		{a.cfg.Connect.ControlPort, a.ControlHandler(), nil},
	}
	for _, b := range a.cfg.Banks {
		if h := a.BankHandler(b.ID); h != nil {
			specs = append(specs, struct {
				port int
				h    http.Handler
				tls  *tls.Config
			}{b.Port, h, nil})
		}
	}
	servers := []srv{}
	for _, sp := range specs {
		ls, err := listenLocal(sp.port)
		if err != nil {
			for _, s := range servers {
				for _, l := range s.ls {
					l.Close()
				}
			}
			return err
		}
		servers = append(servers, srv{s: &http.Server{Handler: sp.h, ReadHeaderTimeout: 10 * time.Second, ErrorLog: log.New(quietTLS{a}, "", 0)}, tls: sp.tls, ls: ls})
	}
	errc := make(chan error, 8)
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
	runCtx, cancel := context.WithCancel(ctx)
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
	a.closeLedgers()
	return serr
}

type quietTLS struct {
	a *App
}

func (q quietTLS) Write(p []byte) (int, error) {
	if !bytes.Contains(p, []byte("TLS handshake error")) {
		q.a.log.Warn(string(bytes.TrimSpace(p)))
	}
	return len(p), nil
}
