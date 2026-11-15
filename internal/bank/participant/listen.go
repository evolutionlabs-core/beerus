package participant

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/site"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	headerTimeout = 10 * time.Second
	apiReadLimit  = 30 * time.Second
	apiWriteLimit = 60 * time.Second
	apiIdle       = 2 * time.Minute
)

type listeners struct {
	site *http.Server
	api  *http.Server
	wg   sync.WaitGroup
}

func (p *Participant) Handler() (http.Handler, error) {
	ob, err := p.openBanking()
	if err != nil {
		return nil, err
	}
	return p.handler(ob), nil
}

func (p *Participant) handler(ob *openbanking.Server) http.Handler {
	return site.New(site.Config{Host: p.srv, Open: ob, Sender: p.sender, Link: p.link, Clock: p.clock, Log: p.log, Brand: p.cfg.Connect.BrandName, API: p.srv.API()})
}

func (p *Participant) openBanking() (*openbanking.Server, error) {
	return server.NewOpenBanking(server.OpenConfig{Bank: p.srv.Bank(), Books: func() *store.Books { return p.books }, Registry: p.srv, Outbox: true, Clock: p.clock})
}

func (p *Participant) start(ctx context.Context) error {
	ob, err := p.openBanking()
	if err != nil {
		return err
	}
	if err := ob.Resume(ctx); err != nil {
		p.log.Error("open banking requests not cleared", "bank", p.id, "err", err)
	}
	b := p.srv.Bank()
	tc, err := server.OpenAPITLS(p.dir, p.id, filepath.Join(p.dir, "banks", p.id, config.BankCAFile))
	if err != nil {
		return err
	}
	siteLn, err := p.link.Listen(b.Port)
	if err != nil {
		return err
	}
	apiLn, err := p.link.Listen(b.APIPort)
	if err != nil {
		closeAll(siteLn)
		return err
	}
	l := &listeners{
		site: &http.Server{Handler: p.handler(ob), ReadHeaderTimeout: headerTimeout},
		api:  &http.Server{Handler: server.OpenAPI(p.srv, ob, p.log), ReadHeaderTimeout: headerTimeout, ReadTimeout: apiReadLimit, WriteTimeout: apiWriteLimit, IdleTimeout: apiIdle, ErrorLog: server.ErrorLog(p.log)},
	}
	l.serve(l.site, siteLn, nil)
	l.serve(l.api, apiLn, tc)
	p.mu.Lock()
	p.open, p.serving = ob, l
	p.mu.Unlock()
	return nil
}

func (p *Participant) stop(ctx context.Context) {
	p.mu.Lock()
	l := p.serving
	p.serving = nil
	p.mu.Unlock()
	if l == nil {
		return
	}
	_ = l.site.Shutdown(ctx)
	_ = l.api.Shutdown(ctx)
	l.wg.Wait()
}

func (p *Participant) restartIfStale(ctx context.Context) {
	if !p.srv.TakeStale() {
		return
	}
	p.mu.Lock()
	l := p.serving
	p.serving = nil
	p.mu.Unlock()
	if l != nil {
		_ = l.site.Close()
		_ = l.api.Close()
		l.wg.Wait()
	}
	if err := p.start(ctx); err != nil {
		p.log.Error("bank site not restarted; retrying", "bank", p.id, "err", err)
		p.srv.MarkStale()
	}
}

func closeAll(ls []net.Listener) {
	for _, l := range ls {
		l.Close()
	}
}

func (l *listeners) serve(s *http.Server, ls []net.Listener, tc *tls.Config) {
	for _, ln := range ls {
		if tc != nil {
			ln = tls.NewListener(ln, tc)
		}
		l.wg.Add(1)
		go func(ln net.Listener) {
			defer l.wg.Done()
			_ = s.Serve(ln)
		}(ln)
	}
}
