package connect

import (
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

type Source interface {
	Fetch(account string, at time.Time, from clock.Date) ([]world.Presented, error)
	LedgerAt(account string, at time.Time) (int64, error)
	PendingAt(account string, at time.Time) (int64, error)
	EndOfDay(account string, d clock.Date) (int64, error)
}

func (a *App) src(account string) Source {
	if acc, ok := a.world.Account(account); ok {
		if l := a.ledgerFor(acc.Institution); l != nil {
			return l
		}
	}
	return a.world
}

func (a *App) statement(account string, at time.Time) ([]world.Presented, error) {
	return a.src(account).Fetch(account, at, a.world.Config().Genesis)
}

func (a *App) line(account, key string, pending bool, at time.Time) (world.Presented, bool) {
	ls, err := a.statement(account, at)
	if err != nil {
		return world.Presented{}, false
	}
	for _, l := range ls {
		if l.Key == key && l.Pending == pending {
			return l, true
		}
	}
	return world.Presented{}, false
}

func (a *App) trace(sessionID string, at time.Time) []world.Traced {
	out := []world.Traced{}
	if sessionID == "" {
		return out
	}
	for _, acc := range a.world.Accounts() {
		ls, err := a.statement(acc.Key, at)
		if err != nil {
			continue
		}
		for _, l := range ls {
			if l.SessionID == sessionID {
				out = append(out, world.Traced{Account: acc, Line: l})
			}
		}
	}
	return out
}
