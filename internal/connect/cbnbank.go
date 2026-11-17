package connect

import (
	"fmt"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
)

func (a *App) bankCBN(id string) (server.CBNClient, error) {
	a.mu.Lock()
	hc, port := a.cbnBanks[id], a.cfg.Connect.CBNPort
	var name string
	if b := a.cfg.Bank(id); b != nil {
		name = b.Name
	}
	a.mu.Unlock()
	if name == "" {
		return server.CBNClient{}, fmt.Errorf("%w: %s", server.ErrBankNotFound, id)
	}
	if hc == nil {
		built, err := server.CBNHTTP(a.cfg.Dir, id, a.link.Remote())
		if err != nil {
			return server.CBNClient{}, &server.UnavailableError{Msg: name + " cannot reach the CBN right now. Try again in a minute.", Err: err}
		}
		a.mu.Lock()
		if a.cbnBanks == nil {
			a.cbnBanks = map[string]*http.Client{}
		}
		if a.cbnBanks[id] == nil {
			a.cbnBanks[id] = built
		}
		hc = a.cbnBanks[id]
		a.mu.Unlock()
	}
	return server.NewCBNClient(hc, port, name), nil
}
