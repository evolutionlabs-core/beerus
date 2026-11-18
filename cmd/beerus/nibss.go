package main

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func (s *supervisor) startNIBSS() {
	s.nibss = s.launch(config.NIBSSID, []string{config.DatabaseURLEnv + "=" + s.database}, []string{"run", "--participant", config.NIBSSID, "--dir", s.dir, "--supervised"})
}

func (s *supervisor) awaitNIBSS(ctx context.Context) error {
	return s.awaitHealth(ctx, "NIBSS", s.nibss, s.nibssHealth)
}

func (s *supervisor) nibssHealth(ctx context.Context, client *http.Client) error {
	f, err := config.LoadNIBSSFile(s.dir)
	if err != nil {
		return err
	}
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(s.dir), config.NIBSSTokenFile))
	if err != nil {
		return err
	}
	return controlHealth(ctx, client, "NIBSS", f.ControlPort, token)
}
