package connect

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	directoryTTL     = 10 * time.Second
	directoryPage    = 500
	directoryTimeout = 5 * time.Second
	directoryIdle    = 90 * time.Second
)

type directoryAnswer struct {
	Data       []nibss.Institution `json:"data"`
	NextCursor string              `json:"next_cursor"`
}

func (a *App) directoryClient() (*http.Client, error) {
	a.dirMu.Lock()
	have := a.dirClient
	a.dirMu.Unlock()
	if have != nil {
		return have, nil
	}
	cert, err := tls.LoadX509KeyPair(a.cfg.Path("banks", a.bankID, "client.crt"), a.cfg.Path("banks", a.bankID, "client.key"))
	if err != nil {
		return nil, fmt.Errorf("load bank client certificate: %w", err)
	}
	pool, err := pki.LoadPool(a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	tc := &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: config.NIBSSHost, MinVersion: tls.VersionTLS12}
	a.dirMu.Lock()
	defer a.dirMu.Unlock()
	if a.dirClient == nil {
		a.dirClient = &http.Client{Timeout: directoryTimeout, Transport: &http.Transport{TLSClientConfig: tc, IdleConnTimeout: directoryIdle, MaxIdleConnsPerHost: 1}}
	}
	return a.dirClient, nil
}

func (a *App) directoryURL(cursor string) (string, error) {
	core, err := url.Parse(a.coreURL)
	if err != nil || core.Hostname() == "" {
		return "", fmt.Errorf("core URL %q has no host", a.coreURL)
	}
	if a.cfg.Connect.NIBSSPort == 0 {
		return "", fmt.Errorf("%s sets no connect.nibss_port", a.cfg.Path(LocalFile))
	}
	q := url.Values{"limit": {strconv.Itoa(directoryPage)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	host := net.JoinHostPort(core.Hostname(), strconv.Itoa(a.cfg.Connect.NIBSSPort))
	return "https://" + host + "/v1/banks?" + q.Encode(), nil
}

func (a *App) directoryFetch(ctx context.Context, client *http.Client, cursor string) (directoryAnswer, error) {
	var page directoryAnswer
	target, err := a.directoryURL(cursor)
	if err != nil {
		return page, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return page, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return page, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPeerBody+1))
	if err != nil {
		return page, fmt.Errorf("read the directory answer: %w", err)
	}
	if len(raw) > maxPeerBody {
		return page, fmt.Errorf("the directory answer is larger than %d MiB", maxPeerBody>>20)
	}
	if resp.StatusCode != http.StatusOK {
		return page, fmt.Errorf("NIBSS answered %s", resp.Status)
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return page, fmt.Errorf("decode the directory answer: %w", err)
	}
	return page, nil
}

func (a *App) refreshBanks(ctx context.Context) error {
	if a.role != RoleBank {
		return nil
	}
	a.dirMu.Lock()
	fresh := a.dirBanks != nil && a.clock.Real().Sub(a.dirAt) < directoryTTL
	a.dirMu.Unlock()
	if fresh {
		return nil
	}
	client, err := a.directoryClient()
	if err != nil {
		return err
	}
	banks := []*config.Bank{}
	cursor := ""
	for {
		page, err := a.directoryFetch(ctx, client, cursor)
		if err != nil {
			return fmt.Errorf("read the NIBSS directory: %w", err)
		}
		for _, in := range page.Data {
			banks = append(banks, directoryBank(in))
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	a.dirMu.Lock()
	a.dirBanks, a.dirAt = banks, a.clock.Real()
	a.dirMu.Unlock()
	return nil
}

func directoryBank(in nibss.Institution) *config.Bank {
	b := &config.Bank{ID: in.ID, Name: in.Name, Type: in.Type, NIPCode: in.NIPCode, NUBANCode: in.NUBANCode}
	switch in.Status {
	case nibss.StatusSuspended:
		b.State = "suspended"
	case nibss.StatusTerminating:
		b.State = "closed"
	}
	return b
}
