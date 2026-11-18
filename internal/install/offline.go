package install

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

const (
	offlineTimeout    = 5 * time.Second
	directoryPath     = "/v1/control/banks"
	directoryPageSize = 500
	nibssHost         = "127.0.0.1"
)

var errNIBSSDown = errors.New("NIBSS is not answering")

type PlanReply struct {
	setup.Plan
	Text    string `json:"text"`
	Changed bool   `json:"changed"`
	Warning string `json:"warning,omitempty"`
}

func PlanOffline(dir string, desired setup.Spec) (PlanReply, error) {
	cfg, err := state.LoadConfig(dir)
	if err != nil {
		return PlanReply{}, err
	}
	st, err := setup.LoadStatus(dir)
	if err != nil {
		return PlanReply{}, err
	}
	if errs := setup.Validate(desired); len(errs) > 0 {
		return PlanReply{}, errors.Join(errs...)
	}
	warning := ""
	ctx, cancel := context.WithTimeout(context.Background(), offlineTimeout)
	defer cancel()
	rows, err := offlineDirectory(ctx, cfg)
	switch {
	case errors.Is(err, errNIBSSDown) || errors.Is(err, os.ErrNotExist):
		rows = slices.Clone(st.Tombstones)
	case err != nil:
		rows = slices.Clone(st.Tombstones)
		warning = fmt.Sprintf("NIBSS did not answer as expected (%v); the plan checks codes against this setup and the removed banks cached in %s only.", err, state.StatusFile)
	}
	pack, err := countrypack.Nigeria()
	if err != nil {
		return PlanReply{}, fmt.Errorf("load the country pack: %w", err)
	}
	plan := setup.Build(pack, desired, st, rows, clock.New().Today())
	if len(plan.Errors) > 0 {
		return PlanReply{}, errors.Join(plan.Errors...)
	}
	return PlanReply{Plan: plan, Text: setup.Render(plan), Changed: plan.Changes(), Warning: warning}, nil
}

func offlineDirectory(ctx context.Context, cfg *config.Config) ([]nip.Institution, error) {
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(cfg.Dir), config.NIBSSTokenFile))
	if err != nil {
		return nil, fmt.Errorf("NIBSS control token: %w", err)
	}
	port := cfg.Connect.NIBSSControlPort
	link := server.NewLink(token, "")
	base := "http://" + nibssHost + ":" + strconv.Itoa(port)
	return server.AllPages(ctx, func(ctx context.Context, cursor string) ([]nip.Institution, string, error) {
		var page struct {
			Data       []nip.Institution `json:"data"`
			NextCursor string            `json:"next_cursor"`
		}
		q := url.Values{"limit": {strconv.Itoa(directoryPageSize)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		_, err := link.Request(ctx, base, http.MethodGet, directoryPath+"?"+q.Encode(), nil, nil, &page)
		return page.Data, page.NextCursor, directoryError(ctx, err, port)
	})
}

func directoryError(ctx context.Context, err error, port int) error {
	var peer *server.PeerError
	if err == nil || errors.As(err, &peer) || ctx.Err() != nil {
		return err
	}
	if unreachable(err) {
		return fmt.Errorf("%w on port %d: %v", errNIBSSDown, port, err)
	}
	return fmt.Errorf("call NIBSS on port %d: %w", port, err)
}

func unreachable(err error) bool {
	var op *net.OpError
	var timeout net.Error
	return errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &op) && op.Op == "dial") || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout())
}
