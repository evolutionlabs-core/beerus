package core

import (
	"context"
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/view"
)

func (a *App) revokeBankConnections(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	return nil, a.revokeScoped(ctx, "banks", r.ID)
}

func (a *App) revokePartnerConnections(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	return nil, a.revokeScoped(ctx, "partners", r.ID)
}

func (a *App) revokeScoped(ctx context.Context, kind, id string) error {
	left, err := a.agg.RevokeScope(ctx, kind, id)
	if err != nil {
		return fmt.Errorf("revoke the connections of %s: %w", id, err)
	}
	if left > 0 {
		return fmt.Errorf("%s of %s still open after the revoke", view.Plural(left, "connection", "connections"), id)
	}
	return nil
}
