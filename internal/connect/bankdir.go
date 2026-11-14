package connect

import (
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

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
		delete(a.bankDigests, id)
		return "", nil
	}
	digest, err := a.cfg.BankFileDigest(snap)
	if err != nil {
		return "", err
	}
	if !force && a.bankDigests[id] == digest {
		return digest, nil
	}
	if err := a.cfg.EnsureBankDir(snap, a.clock.Real()); err != nil {
		return "", fmt.Errorf("ensure the bank directory of %s: %w", id, err)
	}
	if a.bankDigests == nil {
		a.bankDigests = map[string]string{}
	}
	a.bankDigests[id] = digest
	return digest, nil
}
