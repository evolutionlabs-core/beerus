package connect

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	cbnparticipant "github.com/evolutionlabs-core/beerus/internal/cbn/participant"
	"github.com/evolutionlabs-core/beerus/internal/config"
	nibssparticipant "github.com/evolutionlabs-core/beerus/internal/nibss/participant"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func PrepareCBN(cfg *config.Config, now time.Time) error {
	ctl, err := openStore(cfg.Dir)
	if err != nil {
		return err
	}
	if err := ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: ownedDatabase, Name: config.DatabaseName(cfg.Dir, setup.SystemCBN)}); err != nil {
		return err
	}
	if err := ensureCBNPorts(ctl, cfg); err != nil {
		return err
	}
	if err := cfg.EnsureCBNDir(now); err != nil {
		return fmt.Errorf("ensure the CBN directory: %w", err)
	}
	return nil
}

func (a *App) ensureCBNDir() error {
	if a.role == RoleCore {
		return nil
	}
	now := a.clock.Real()
	if ensuredRecently(a.cbnEnsured, now) {
		return nil
	}
	if err := a.cfg.EnsureCBNDir(now); err != nil {
		return fmt.Errorf("ensure the CBN directory: %w", err)
	}
	a.cbnEnsured = now
	return nil
}

func (a *App) prepareCBN() error {
	if err := a.ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: ownedDatabase, Name: config.DatabaseName(a.cfg.Dir, setup.SystemCBN)}); err != nil {
		return err
	}
	if err := ensureCBNPorts(a.ctl, a.cfg); err != nil {
		return err
	}
	if err := a.ensureCBNDir(); err != nil {
		return err
	}
	token, err := config.ReadSecret(filepath.Join(config.CBNDir(a.cfg.Dir), config.CBNTokenFile))
	if err != nil {
		return fmt.Errorf("CBN control token: %w", err)
	}
	port := a.cfg.Connect.CBNControlPort
	base := a.cfg.CBNURL()
	if a.role != RoleCore {
		base = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	a.cbnCtl = &cbnAdmin{link: server.NewLink(token, a.link.Bind), base: base, port: port}
	return nil
}

func (a *App) openParticipants(ctx context.Context) error {
	if a.role == RoleCore {
		return nil
	}
	cbn, err := cbnparticipant.New(ctx, a.cfg.Dir, cbnparticipant.Options{Logger: a.log, DatabaseURL: a.cfg.DatabaseURL})
	if err != nil {
		return err
	}
	ns, err := nibssparticipant.New(ctx, a.cfg.Dir, nibssparticipant.Options{Logger: a.log, DatabaseURL: a.cfg.DatabaseURL})
	if err != nil {
		cbn.Close()
		return err
	}
	a.localCBN, a.localNIBSS = cbn, ns
	return nil
}

func (a *App) serveCBN(ctx context.Context, errc chan<- error) (<-chan struct{}, error) {
	done := make(chan struct{})
	if a.localCBN == nil {
		close(done)
		return done, nil
	}
	go func() {
		defer close(done)
		if err := a.localCBN.Serve(ctx); err != nil {
			errc <- fmt.Errorf("serve CBN: %w", err)
		}
	}()
	return done, nil
}
