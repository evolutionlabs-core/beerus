package participant

import (
	"context"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/site"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	headerTimeout = 10 * time.Second
	apiReadLimit  = 30 * time.Second
	apiWriteLimit = 60 * time.Second
	apiIdle       = 2 * time.Minute
	promoteDrain  = 3 * time.Second
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
	return server.NewOpenBanking(server.OpenConfig{Bank: p.srv.Bank(), Books: p.booksNow, Registry: p.srv, Clock: p.clock})
}

func (p *Participant) startProvisioning() error {
	b := p.srv.Bank()
	siteLn, err := p.link.Listen(b.Port)
	if err != nil {
		return err
	}
	l := &listeners{site: &http.Server{Handler: p.provisioningHandler(), ReadHeaderTimeout: headerTimeout}}
	l.serve(l.site, siteLn)
	p.mu.Lock()
	p.serving = l
	p.mu.Unlock()
	return nil
}

func (p *Participant) promote(ctx context.Context) bool {
	p.mu.Lock()
	waiting := p.books != nil && p.open == nil
	l := p.serving
	if waiting {
		p.serving = nil
	}
	p.mu.Unlock()
	if !waiting {
		return true
	}
	if l != nil {
		drain, stop := context.WithTimeout(context.WithoutCancel(ctx), promoteDrain)
		_ = l.site.Shutdown(drain)
		stop()
		l.wg.Wait()
	}
	if err := p.start(ctx); err != nil {
		p.log.Error("bank site not started; retrying", "bank", p.id, "err", err)
		if err := p.startProvisioning(); err != nil {
			p.log.Error("bank setup page not restarted", "bank", p.id, "err", err)
		}
		return false
	}
	return true
}

func (p *Participant) start(ctx context.Context) error {
	if p.booksNow() == nil {
		return p.startProvisioning()
	}
	ob, err := p.openBanking()
	if err != nil {
		return err
	}
	if err := ob.Resume(ctx); err != nil {
		p.log.Error("open banking requests not cleared", "bank", p.id, "err", err)
	}
	b := p.srv.Bank()
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
		api:  &http.Server{Handler: server.OpenAPI(p.srv, ob, p.log), ReadHeaderTimeout: headerTimeout, ReadTimeout: apiReadLimit, WriteTimeout: apiWriteLimit, IdleTimeout: apiIdle, ErrorLog: control.ErrorLog(p.log)},
	}
	l.serve(l.site, siteLn)
	l.serve(l.api, apiLn)
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
	if l.api != nil {
		_ = l.api.Shutdown(ctx)
	}
	l.wg.Wait()
}

func (p *Participant) refreshIfStale() {
	if !p.srv.TakeStale() {
		return
	}
	p.mu.Lock()
	open := p.open
	p.mu.Unlock()
	if open == nil {
		p.srv.MarkStale()
		return
	}
	open.SetTerms(server.OpenTerms(p.srv.Bank()))
}

func closeAll(ls []net.Listener) {
	for _, l := range ls {
		l.Close()
	}
}

func (l *listeners) serve(s *http.Server, ls []net.Listener) {
	for _, ln := range ls {
		l.wg.Add(1)
		go func(ln net.Listener) {
			defer l.wg.Done()
			_ = s.Serve(ln)
		}(ln)
	}
}
