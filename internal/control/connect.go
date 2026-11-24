package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	ConnectAway     = "Connect is not available right now. Try again soon."
	ConnectHealth   = "/v1/health"
	ConnectStatus   = "/v1/status"
	ConnectBase     = "/v1/control"
	ConnectPageMax  = 500
	ConnectPageSize = 50
)

type ConnectState struct {
	Status      string   `json:"status"`
	WorldID     string   `json:"world_id"`
	Registered  bool     `json:"registered"`
	Connections int      `json:"connections"`
	Active      int      `json:"active_connections"`
	Deliveries  int      `json:"deliveries"`
	Warnings    []string `json:"warnings"`
}

type InstitutionRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ConnectionView struct {
	ID                 string         `json:"id"`
	Status             string         `json:"status"`
	Institution        InstitutionRef `json:"institution"`
	UserID             string         `json:"user_id"`
	StartDate          string         `json:"start_date"`
	EffectiveStartDate *string        `json:"effective_start_date"`
	ConsentExpiresAt   *string        `json:"consent_expires_at"`
	LastSyncedAt       *string        `json:"last_synced_at"`
	CreatedAt          string         `json:"created_at"`
	PartnerID          string         `json:"partner_id"`
	CustomerID         string         `json:"customer_id"`
	AccountIDs         []string       `json:"account_ids"`
}

type DeliveryView struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	Attempts   int             `json:"attempts"`
	LastStatus int             `json:"last_status"`
	LastError  string          `json:"last_error"`
	Body       json.RawMessage `json:"body"`
	CreatedAt  time.Time       `json:"created_at"`
}

type ConnectImpactConnection struct {
	ID       string `json:"id"`
	Partner  string `json:"partner"`
	Customer string `json:"customer"`
	Status   string `json:"status"`
}

type ConnectImpact struct {
	Open      []ConnectImpactConnection `json:"open_connections"`
	Connected []string                  `json:"connected_accounts"`
	Total     int                       `json:"total_connections"`
}

type ConnectRevoked struct {
	Remaining int `json:"remaining"`
}

type ConnectShare struct {
	Partner   string    `json:"partner_id"`
	AccountID string    `json:"account_id"`
	Status    string    `json:"status"`
	AsAt      time.Time `json:"as_at"`
	TxnID     string    `json:"txn_id,omitempty"`
	Synced    bool      `json:"synced"`
}

type ConnectDown struct {
	Err error
}

func (e *ConnectDown) Error() string {
	return ConnectAway
}

func (e *ConnectDown) Unwrap() error {
	return e.Err
}

type ConnectControl struct {
	Link Link
	Base string
}

func NewConnectControl(link Link, host string, port int) ConnectControl {
	return ConnectControl{Link: link, Base: "http://" + hostPort(host, port)}
}

func hostPort(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}

func (c ConnectControl) Call(ctx context.Context, method, path string, in, out any) error {
	_, err := c.Link.Do(ctx, c.Base, method, path, in, out)
	var peer *PeerError
	if err == nil || ctx.Err() != nil || errors.As(err, &peer) {
		return err
	}
	if Unreachable(err) {
		return &ConnectDown{Err: err}
	}
	return fmt.Errorf("call Connect: %w", err)
}

func (c ConnectControl) State(ctx context.Context) (ConnectState, error) {
	var out ConnectState
	return out, c.Call(ctx, http.MethodGet, ConnectStatus, nil, &out)
}

func (c ConnectControl) Connections(ctx context.Context, bank, partner string) ([]ConnectionView, error) {
	return AllPages(ctx, func(ctx context.Context, cursor string) ([]ConnectionView, string, error) {
		q := url.Values{"limit": {strconv.Itoa(ConnectPageMax)}}
		if bank != "" {
			q.Set("bank", bank)
		}
		if partner != "" {
			q.Set("partner", partner)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Data       []ConnectionView `json:"data"`
			NextCursor string           `json:"next_cursor"`
		}
		err := c.Call(ctx, http.MethodGet, ConnectBase+"/connections?"+q.Encode(), nil, &page)
		return page.Data, page.NextCursor, err
	})
}

func (c ConnectControl) Deliveries(ctx context.Context, limit int, cursor string) ([]DeliveryView, string, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var page struct {
		Data       []DeliveryView `json:"data"`
		NextCursor string         `json:"next_cursor"`
	}
	err := c.Call(ctx, http.MethodGet, ConnectBase+"/webhooks/deliveries?"+q.Encode(), nil, &page)
	return page.Data, page.NextCursor, err
}

func (c ConnectControl) Replay(ctx context.Context, id string) error {
	return c.Call(ctx, http.MethodPost, ConnectBase+"/webhooks/deliveries/"+url.PathEscape(id)+"/replay", nil, nil)
}

func (c ConnectControl) RevokeConnection(ctx context.Context, id string) error {
	return c.Call(ctx, http.MethodPost, ConnectBase+"/connections/"+url.PathEscape(id)+"/revoke", nil, nil)
}

func (c ConnectControl) RevokeScope(ctx context.Context, kind, id string) (int, error) {
	var out ConnectRevoked
	err := c.Call(ctx, http.MethodPost, ConnectBase+"/"+kind+"/"+url.PathEscape(id)+"/revoke-connections", nil, &out)
	return out.Remaining, err
}

func (c ConnectControl) Drop(ctx context.Context, kind, id string) error {
	return c.Call(ctx, http.MethodDelete, ConnectBase+"/"+kind+"/"+url.PathEscape(id), nil, nil)
}

func (c ConnectControl) Impact(ctx context.Context, kind, id string) (ConnectImpact, error) {
	var out ConnectImpact
	q := url.Values{kind: {id}}
	return out, c.Call(ctx, http.MethodGet, ConnectBase+"/impact?"+q.Encode(), nil, &out)
}

func (c ConnectControl) Shares(ctx context.Context, account, line string) ([]ConnectShare, error) {
	q := url.Values{"account": {account}}
	if line != "" {
		q.Set("line", line)
	}
	var out struct {
		Data []ConnectShare `json:"data"`
	}
	return out.Data, c.Call(ctx, http.MethodGet, ConnectBase+"/shares?"+q.Encode(), nil, &out)
}

func (c ConnectControl) Sync(ctx context.Context) error {
	return c.Call(ctx, http.MethodPost, ConnectBase+"/sync", nil, nil)
}

func (c ConnectControl) Reload(ctx context.Context) error {
	return c.Call(ctx, http.MethodPost, ConnectBase+"/reload", nil, nil)
}
