package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const nibssHealthTimeout = 3 * time.Second

func (s *supervisor) startNIBSS() {
	s.nibss = s.launch(config.NIBSSID, "", []string{config.DatabaseURLEnv + "=" + s.database}, []string{"run", "--participant", config.NIBSSID, "--dir", s.dir, "--supervised"})
}

func (s *supervisor) awaitNIBSS(ctx context.Context) error {
	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(startRetry)
	defer tick.Stop()
	client := &http.Client{Timeout: nibssHealthTimeout}
	for {
		err := s.nibssHealth(ctx, client)
		if err == nil {
			return nil
		}
		select {
		case <-s.nibss.firstExit:
			return fmt.Errorf("NIBSS exited before it was ready: %s", exitText(s.nibss.exit))
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("wait for NIBSS: %w", err)
		case <-tick.C:
		}
	}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(f.ControlPort)+"/v1/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("NIBSS answered " + resp.Status)
	}
	return nil
}
