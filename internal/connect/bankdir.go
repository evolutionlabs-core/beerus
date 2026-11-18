package connect

import (
	"fmt"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const bankEnsureEvery = time.Minute

type bankEnsured struct {
	digest string
	at     time.Time
}

func ensuredRecently(at, now time.Time) bool {
	return !at.IsZero() && now.Sub(at) < bankEnsureEvery
}

func (a *App) ensureBankDir(id string, force bool) (string, error) {
	a.bankFileMu.Lock()
	defer a.bankFileMu.Unlock()
	a.mu.Lock()
	var snap *config.Bank
	if b := a.cfg.Bank(id); b != nil {
		copied := *b
		snap = &copied
	}
	a.mu.Unlock()
	if snap == nil {
		delete(a.bankEnsures, id)
		return "", nil
	}
	digest, err := a.cfg.BankFileDigest(snap)
	if err != nil {
		return "", err
	}
	now := a.clock.Real()
	last, ok := a.bankEnsures[id]
	if !force && ok && last.digest == digest && ensuredRecently(last.at, now) {
		return digest, nil
	}
	if err := a.cfg.EnsureBankDir(snap, now); err != nil {
		return "", fmt.Errorf("ensure the bank directory of %s: %w", id, err)
	}
	if a.bankEnsures == nil {
		a.bankEnsures = map[string]bankEnsured{}
	}
	a.bankEnsures[id] = bankEnsured{digest: digest, at: now}
	return digest, nil
}
