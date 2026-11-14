package connect

import (
	"context"
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type bookSource struct {
	a *App
}

func (s bookSource) Book(id string) (server.Book, error) {
	s.a.mu.Lock()
	b := s.a.cfg.Bank(id)
	var ls *ledgerSource
	if b != nil {
		ls = s.a.ledgerFor(id)
	}
	s.a.mu.Unlock()
	if b == nil {
		return server.Book{}, fmt.Errorf("%w: %s", server.ErrBankNotFound, id)
	}
	bk := server.Book{Name: s.a.bankCopy(id).Name, Phase: s.a.bankPhase(id)}
	if ls != nil {
		bk.Bank = ls.Bank()
	}
	return bk, nil
}

func (s bookSource) Enrol(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
	return s.a.enrolBVN(ctx, name, phone, dob)
}

func (s bookSource) Empty() bool {
	return len(s.a.bankList()) == 0
}

func (s bookSource) Refusing(id string) bool {
	return s.a.bankRefusing(id)
}
