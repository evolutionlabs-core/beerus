package core

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net"
	"net/http"
	"time"
)

func (a *App) Serve(ctx context.Context) error {
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
		{a.cfg.Connect.ControlPort, a.ControlHandler(), nil},
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
	a.setServing(true)
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
	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		a.runPush(runCtx)
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
	a.setServing(false)
	cancel()
	<-done
	<-pushDone
	a.confirmWG.Wait()
	return serr
}

func (a *App) setServing(on bool) {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	a.serving = on
}
