package connect

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nibss/gateway"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

func (a *App) NIBSSTLS() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(a.cfg.Path("nibss", "server.crt"), a.cfg.Path("nibss", "server.key"))
	if err != nil {
		return nil, fmt.Errorf("load NIBSS server certificate: %w", err)
	}
	pool, err := pki.LoadPool(a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func (a *App) listDirectory(ctx context.Context) ([]nibss.Institution, error) {
	s := a.nibssStore()
	if s == nil {
		return nil, gateway.ErrUnavailable
	}
	return s.Directory(ctx)
}
