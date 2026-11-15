package connect

import (
	"context"
	"fmt"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/site"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

type bankHost struct {
	a  *App
	id string
}

func (a *App) bankHandler(id string) http.Handler {
	a.mu.Lock()
	b := a.cfg.Bank(id)
	have := a.ledgerFor(id) != nil
	brand := a.cfg.Connect.BrandName
	a.mu.Unlock()
	if b == nil || !have {
		return nil
	}
	return site.New(site.Config{Host: bankHost{a, id}, Open: a.openBanking(id), Sender: a.sender, Link: a.link, Clock: a.clock, Log: a.log, Brand: brand})
}

func (h bankHost) Bank() config.Bank {
	b, _ := h.a.bankCopy(h.id)
	return b
}

func (h bankHost) Phase() (string, string) {
	return h.a.bankPhaseReason(h.id)
}

func (h bankHost) Books() (*bank.Bank, error) {
	l := h.a.obLedger(h.id)
	if l == nil {
		return nil, fmt.Errorf("the books of %s are not open", h.id)
	}
	return l.Bank(), nil
}

func (h bankHost) Refusing(id string) bool {
	return h.a.bankRefusing(id)
}

func (h bankHost) RefreshRegistry(context.Context) error {
	return nil
}

func (h bankHost) RefreshBanks(context.Context) error {
	return nil
}

func (h bankHost) Directory() []*config.Bank {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	out := make([]*config.Bank, 0, len(h.a.cfg.Banks))
	for _, b := range h.a.cfg.Banks {
		c := *b
		out = append(out, &c)
	}
	return out
}

func (h bankHost) Lookup(id string) *config.Bank {
	for _, b := range h.Directory() {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (h bankHost) Quote(ctx context.Context, req server.TransferRequest) (server.TransferResult, error) {
	return h.a.Quote(ctx, req)
}

func (h bankHost) Transfer(ctx context.Context, req server.TransferRequest) (server.TransferResult, error) {
	res, err := h.a.Transfer(req)
	if err != nil {
		return res, err
	}
	h.a.SyncAll(ctx)
	return res, nil
}

func (h bankHost) Banking() *server.Banking {
	return h.a.banking
}
