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
	bk, ok := s.a.bookOf(id)
	if !ok {
		return server.Book{}, fmt.Errorf("%w: %s", server.ErrBankNotFound, id)
	}
	return server.Book{Name: s.a.bankCopy(id).Name, Phase: s.a.bankPhase(id), Bank: bk.ls.Bank()}, nil
}

func (s bookSource) Enrol(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
	return s.a.enrolBVN(ctx, name, phone, dob)
}

func (s bookSource) Empty() bool {
	return len(s.a.bankList()) == 0
}
