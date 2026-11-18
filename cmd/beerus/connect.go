package main

import (
	"context"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const connectID = "connect"

func (s *supervisor) startConnect() {
	s.connect = s.launch(connectID, nil, []string{"run", "--participant", connectID, "--dir", s.dir, "--supervised"})
}

func (s *supervisor) awaitConnect(ctx context.Context) error {
	return s.awaitHealth(ctx, "Connect", s.connect, s.connectHealth)
}

func (s *supervisor) connectHealth(ctx context.Context, client *http.Client) error {
	c, err := setup.LoadConfig(s.dir)
	if err != nil {
		return err
	}
	token, err := config.ReadSecret(c.Path("control-token"))
	if err != nil {
		return err
	}
	return controlHealth(ctx, client, "Connect", c.Connect.ConnectControlPort, token)
}
