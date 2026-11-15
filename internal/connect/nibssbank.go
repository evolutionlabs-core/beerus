package connect

import (
	"fmt"
	"net"
	"net/http"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
)

func (a *App) bankNIBSS(id string) (server.NIBSSClient, error) {
	a.mu.Lock()
	hc, port := a.nibssBanks[id], a.cfg.Connect.NIBSSPort
	var name string
	if b := a.cfg.Bank(id); b != nil {
		name = b.Name
	}
	a.mu.Unlock()
	if name == "" {
		return server.NIBSSClient{}, fmt.Errorf("%w: %s", server.ErrBankNotFound, id)
	}
	if hc == nil {
		built, err := server.NIBSSHTTP(a.cfg.Dir, id)
		if err != nil {
			return server.NIBSSClient{}, &server.UnavailableError{Msg: name + " cannot reach NIBSS right now. Try again in a minute.", Err: err}
		}
		a.mu.Lock()
		if a.nibssBanks == nil {
			a.nibssBanks = map[string]*http.Client{}
		}
		if a.nibssBanks[id] == nil {
			a.nibssBanks[id] = built
		}
		hc = a.nibssBanks[id]
		a.mu.Unlock()
	}
	return server.NIBSSClient{HTTP: hc, Base: "https://" + net.JoinHostPort(nibssHost, strconv.Itoa(port)), Away: name + " cannot reach NIBSS right now. Try again in a minute."}, nil
}
