package connect

import (
	"sync"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type ApplyOptions struct {
	Prune     bool           `json:"prune,omitempty"`
	Money     *setup.Removal `json:"money,omitempty"`
	Confirmed []string       `json:"confirmed,omitempty"`
	SettleNow bool           `json:"settle_now,omitempty"`
}

type ctlStore struct {
	mu  sync.Mutex
	dir string
	st  setup.Status
}

type finalizer struct {
	name string
	run  func(a *App, r setup.Resource) (*setup.Condition, error)
}

var bankFinalizers = []finalizer{
	{name: setup.FinalizerStopRouting},
	{name: setup.FinalizerRevokeConnections},
	{name: setup.FinalizerSettleInflight},
	{name: setup.FinalizerDecideMoney},
	{name: setup.FinalizerSettleMoves},
	{name: setup.FinalizerPurgeOwned},
}

var partnerFinalizers = []finalizer{
	{name: setup.FinalizerRevokeConnections},
	{name: setup.FinalizerPurgeOwned},
}
