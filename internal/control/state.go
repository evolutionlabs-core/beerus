package control

import (
	"context"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"time"
)

type State struct {
	ParticipantID       string     `json:"participant_id"`
	Kind                string     `json:"kind"`
	Phase               string     `json:"phase"`
	Reason              string     `json:"reason,omitempty"`
	Generation          int64      `json:"generation"`
	ObservedGeneration  int64      `json:"observed_generation"`
	Steps               []Step     `json:"steps"`
	Problems            []string   `json:"problems,omitempty"`
	SettlementProblems  []string   `json:"settlement_problems,omitempty"`
	SettlementCheckedAt *time.Time `json:"settlement_checked_at,omitempty"`
}

type Step struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Done        bool       `json:"done"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type PhaseRequest struct {
	Phase      string `json:"phase"`
	Reason     string `json:"reason"`
	Generation int64  `json:"generation"`
	WorldID    string `json:"world_id"`
}

type FaultView struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Outcome   string     `json:"outcome,omitempty"`
	Left      int        `json:"left,omitempty"`
	UntilAt   *time.Time `json:"until_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

const FaultPageMax = 500

type OpenSends struct {
	Open int `json:"open_sends"`
}

func OpenBanks(all []*config.Bank) []*config.Bank {
	out := []*config.Bank{}
	for _, b := range all {
		if !b.Closed() {
			out = append(out, b)
		}
	}
	return out
}

func AllPages[T any](ctx context.Context, page func(ctx context.Context, cursor string) ([]T, string, error)) ([]T, error) {
	out := []T{}
	cursor := ""
	for {
		data, next, err := page(ctx, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
		if next == "" {
			return out, nil
		}
		if next == cursor {
			return nil, fmt.Errorf("cursor %q did not move", cursor)
		}
		cursor = next
	}
}
