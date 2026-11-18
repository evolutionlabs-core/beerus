package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	cbnPage        = 500
	cbnDialTimeout = time.Second
	localhostHost  = ".localhost"
	loopback       = "127.0.0.1"
)

type CBNClient NIBSSClient

type cbnNet struct {
	BankID string `json:"bank_id"`
	Net    int64  `json:"net_minor"`
}

type cbnPosting struct {
	Kind      string   `json:"kind"`
	SessionID string   `json:"session_id"`
	Nets      []cbnNet `json:"nets"`
}

type cbnPostingPage struct {
	Data       []cbnPosting `json:"data"`
	NextCursor string       `json:"next_cursor"`
}

func CBNHTTP(dir, id string, remote bool) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: cbnDialTimeout}
	return peerHTTP(dir, id, config.CBNHost, func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err == nil && !remote && strings.HasSuffix(host, localhostHost) {
			addr = net.JoinHostPort(loopback, port)
		}
		return dialer.DialContext(ctx, network, addr)
	})
}

func NewCBNClient(hc *http.Client, port int, name string) CBNClient {
	return CBNClient{HTTP: hc, Base: "https://" + net.JoinHostPort(config.CBNHost, strconv.Itoa(port)), Away: name + " cannot reach the CBN right now. Try again in a minute."}
}

func (c CBNClient) Balance(ctx context.Context, id string) (int64, error) {
	var out struct {
		Balance int64 `json:"balance_minor"`
	}
	if _, err := NIBSSClient(c).Do(ctx, http.MethodGet, "/v1/banks/"+url.PathEscape(id)+"/settlement-balance", nil, nil, &out); err != nil {
		return 0, fmt.Errorf("read the settlement balance of %s at the CBN: %w", id, err)
	}
	return out.Balance, nil
}

func (c CBNClient) sessionNets(ctx context.Context, id string) (map[string]int64, error) {
	list, err := AllPages(ctx, func(ctx context.Context, cursor string) ([]cbnPosting, string, error) {
		q := url.Values{"limit": {strconv.Itoa(cbnPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page cbnPostingPage
		_, err := NIBSSClient(c).Do(ctx, http.MethodGet, "/v1/settlement-postings", q, nil, &page)
		return page.Data, page.NextCursor, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the settlement postings of %s at the CBN: %w", id, err)
	}
	out := map[string]int64{}
	for _, p := range list {
		if p.Kind != "session" {
			continue
		}
		for _, n := range p.Nets {
			if n.BankID == id {
				out[p.SessionID] = n.Net
			}
		}
	}
	return out, nil
}

func SettlementProblems(ctx context.Context, c CBNClient, books *bank.Bank, id string) ([]string, error) {
	held, err := books.L.Balance(ctx, bank.Settlement)
	if err != nil {
		return nil, fmt.Errorf("read the settlement balance of %s: %w", id, err)
	}
	posted, err := books.Settlements(ctx)
	if err != nil {
		return nil, err
	}
	moved, err := c.sessionNets(ctx, id)
	if err != nil {
		return nil, err
	}
	at, err := c.Balance(ctx, id)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for k := range posted {
		keys[k] = true
	}
	for k := range moved {
		keys[k] = true
	}
	out := []string{}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if posted[k] != moved[k] {
			out = append(out, fmt.Sprintf("session %s at %s: the bank posted %d, but the CBN moved %d", k, id, posted[k], moved[k]))
		}
	}
	if held != at {
		out = append(out, fmt.Sprintf("%s's books show %d at the CBN, but the CBN shows %d", id, held, at))
	}
	return out, nil
}

func (s *Server) CBN() (CBNClient, error) {
	hc, err := s.cbnHTTP()
	if err != nil {
		return CBNClient{}, s.coreAway(err)
	}
	if s.cbnPort == 0 {
		return CBNClient{}, s.coreAway(fmt.Errorf("%s sets no cbn_port", config.BankFilePath(s.dir, s.id)))
	}
	return NewCBNClient(hc, s.cbnPort, s.Bank().Name), nil
}

func (s *Server) cbnHTTP() (*http.Client, error) {
	s.dirMu.Lock()
	have := s.cbnClient
	s.dirMu.Unlock()
	if have != nil {
		return have, nil
	}
	built, err := CBNHTTP(s.dir, s.id, s.link.Remote())
	if err != nil {
		return nil, err
	}
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	if s.cbnClient == nil {
		s.cbnClient = built
	}
	return s.cbnClient, nil
}

func (s *Server) CheckSettlement(ctx context.Context) error {
	c, err := s.CBN()
	if err != nil {
		return err
	}
	problems, err := SettlementProblems(ctx, c, s.books, s.id)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("check the settlement of %s: %w", s.id, err)
	}
	at := s.clock.Real()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlementProblems, s.settlementCheckedAt = problems, &at
	return nil
}
