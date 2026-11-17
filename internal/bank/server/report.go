package server

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
)

const (
	reportsPath  = "/v1/settlement-reports"
	positionPath = "/v1/settlement-position"
	reportsPage  = 500
)

type settlementPosition struct {
	Outward       int64 `json:"outward_minor"`
	Inward        int64 `json:"inward_minor"`
	PendingInward int64 `json:"pending_inward_minor"`
	Transfers     int   `json:"transfers"`
	Settlements   int   `json:"settlements"`
}

type SettlementReport struct {
	SessionID string `json:"session_id"`
	Outward   int64  `json:"outward_minor"`
	Inward    int64  `json:"inward_minor"`
	Net       int64  `json:"net_minor"`
}

type reportPageBody struct {
	Data       []SettlementReport `json:"data"`
	NextCursor string             `json:"next_cursor"`
}

type BookingError struct {
	SessionID string
	Err       error
}

func (e *BookingError) Error() string {
	return "post settlement " + e.SessionID + ": " + e.Err.Error()
}

func (e *BookingError) Unwrap() error {
	return e.Err
}

func (r SettlementReport) check() error {
	net, ok := inwardLessOutward(r.Inward, r.Outward)
	if r.Outward < 0 || r.Inward < 0 || !ok || r.Net != net {
		return &BookingError{SessionID: r.SessionID, Err: fmt.Errorf("outward %d, inward %d and net %d do not add up", r.Outward, r.Inward, r.Net)}
	}
	return nil
}

func inwardLessOutward(inward, outward int64) (int64, bool) {
	if outward < 0 && inward > math.MaxInt64+outward || outward > 0 && inward < math.MinInt64+outward {
		return 0, false
	}
	return inward - outward, true
}

func (c NIBSSClient) SettlementReports(ctx context.Context, after string) ([]SettlementReport, error) {
	out, err := AllPages(ctx, func(ctx context.Context, cursor string) ([]SettlementReport, string, error) {
		q := url.Values{"limit": {strconv.Itoa(reportsPage)}}
		switch {
		case cursor != "":
			q.Set("cursor", cursor)
		case after != "":
			q.Set("after", after)
		}
		var page reportPageBody
		_, err := c.Do(ctx, http.MethodGet, reportsPath, q, nil, &page)
		return page.Data, page.NextCursor, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the settlement reports at NIBSS: %w", err)
	}
	return out, nil
}

func (c NIBSSClient) position(ctx context.Context, after string) (settlementPosition, error) {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	var out settlementPosition
	if _, err := c.Do(ctx, http.MethodGet, positionPath, q, nil, &out); err != nil {
		return settlementPosition{}, fmt.Errorf("read the settlement position at NIBSS: %w", err)
	}
	return out, nil
}

func PostSettlements(ctx context.Context, c NIBSSClient, books *bank.Bank, now time.Time) error {
	last, err := books.LastSettlement(ctx)
	if err != nil {
		return err
	}
	list, err := c.SettlementReports(ctx, last)
	if err != nil {
		return err
	}
	for _, r := range list {
		if err := r.check(); err != nil {
			return err
		}
		if _, err := books.Settle(ctx, r.SessionID, r.Outward, r.Inward, now); err != nil {
			return &BookingError{SessionID: r.SessionID, Err: err}
		}
	}
	return nil
}

func nibssProblems(ctx context.Context, c NIBSSClient, posted map[string]int64, id string) ([]string, error) {
	list, err := c.SettlementReports(ctx, "")
	if err != nil {
		return nil, err
	}
	nets := make(map[string]int64, len(list))
	keys := map[string]bool{}
	for _, r := range list {
		nets[r.SessionID] = r.Net
		keys[r.SessionID] = true
	}
	for k := range posted {
		keys[k] = true
	}
	out := []string{}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if nets[k] != posted[k] {
			out = append(out, fmt.Sprintf("session %s at %s: NIBSS nets %d, but the bank posted %d", k, id, nets[k], posted[k]))
		}
	}
	return out, nil
}
