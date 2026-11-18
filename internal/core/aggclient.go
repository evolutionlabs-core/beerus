package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (a *App) reloadConnect(ctx context.Context) {
	err := a.agg.Reload(ctx)
	var down *control.ConnectDown
	if err != nil && !errors.As(err, &down) {
		a.log.Warn("Connect did not reload the setup", "err", err)
	}
}

func (a *App) syncConnect(ctx context.Context) {
	err := a.agg.Sync(ctx)
	var down *control.ConnectDown
	if err != nil && !errors.As(err, &down) {
		a.log.Warn("Connect did not sync", "err", err)
	}
}

func (a *App) SyncAll(ctx context.Context) {
	a.syncConnect(ctx)
	a.Step(ctx)
}

func (a *App) connectFailed(w http.ResponseWriter, r *http.Request, err error) {
	var peer *control.PeerError
	if errors.As(err, &peer) {
		web.WriteError(w, r, peer.Status, peer.Code, peer.Message)
		return
	}
	a.log.ErrorContext(r.Context(), "Connect call failed", "err", err)
	web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", control.ConnectAway)
}

func (a *App) dropConnect(ctx context.Context, kind, id string) error {
	if err := a.agg.Drop(ctx, kind, id); err != nil {
		return fmt.Errorf("drop the connections of %s: %w", id, err)
	}
	return nil
}

func (a *App) ctlConnections(w http.ResponseWriter, r *http.Request) {
	rows, err := a.agg.Connections(r.Context(), "", "")
	if err != nil {
		a.connectFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (a *App) ctlRevokeConnection(w http.ResponseWriter, r *http.Request) {
	if err := a.agg.RevokeConnection(r.Context(), r.PathValue("id")); err != nil {
		a.connectFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (a *App) ctlDeliveries(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	if v := r.URL.Query().Get("limit"); v != "" {
		q.Set("limit", v)
	}
	if v := r.URL.Query().Get("cursor"); v != "" {
		q.Set("cursor", v)
	}
	var page struct {
		Data       []control.DeliveryView `json:"data"`
		NextCursor string                 `json:"next_cursor"`
	}
	if err := a.agg.Call(r.Context(), http.MethodGet, control.ConnectBase+"/webhooks/deliveries?"+q.Encode(), nil, &page); err != nil {
		a.connectFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(page.Data, page.NextCursor))
}

func (a *App) ctlReplay(w http.ResponseWriter, r *http.Request) {
	if err := a.agg.Replay(r.Context(), r.PathValue("id")); err != nil {
		a.connectFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "pending"})
}

func (a *App) ctlSync(w http.ResponseWriter, r *http.Request) {
	err := a.agg.Sync(r.Context())
	a.Step(r.Context())
	if err != nil {
		a.connectFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "synced"})
}

func (a *App) revokedConnections(ctx context.Context, bank string) ([]string, error) {
	rows, err := a.agg.Connections(ctx, bank, "")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, c := range rows {
		if c.Status == "revoked" {
			out = append(out, c.ID)
		}
	}
	slices.Sort(out)
	return out, nil
}

func parseTime(s *string) time.Time {
	if s == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return time.Time{}
	}
	return t
}
