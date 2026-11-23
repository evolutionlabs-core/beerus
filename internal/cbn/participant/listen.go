package participant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
)

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

func (p *Participant) server(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: headerTimeout, ReadTimeout: readLimit, WriteTimeout: writeLimit, IdleTimeout: idleLimit, ErrorLog: slog.NewLogLogger(p.log.Handler(), slog.LevelWarn)}
}

func (p *Participant) Serve(ctx context.Context) error {
	defer p.Close()
	apiLn, err := p.listen(p.file.Port)
	if err != nil {
		return err
	}
	ctlLn, err := p.listen(p.file.ControlPort)
	if err != nil {
		for _, l := range apiLn {
			l.Close()
		}
		return err
	}
	servers := []*http.Server{p.server(p.settlement()), p.server(p.handler())}
	var wg sync.WaitGroup
	failed := make(chan error, len(apiLn)+len(ctlLn))
	serve := func(s *http.Server, ls []net.Listener) {
		for _, ln := range ls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					failed <- err
				}
			}()
		}
	}
	serve(servers[0], apiLn)
	serve(servers[1], ctlLn)
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
