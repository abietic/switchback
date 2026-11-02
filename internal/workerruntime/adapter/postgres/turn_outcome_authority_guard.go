package postgres

import (
	"context"
	"fmt"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

// assertNoCompetingTurnOutcomeAuthority must run while the caller holds the
// exact AgentTurn outcome advisory lock. A completed Runtime event has already
// released ordinary recovery/loss authority, while a Backend deadline remains
// the durable proof that the deadline path owns terminality after completion.
func assertNoCompetingTurnOutcomeAuthority(
	ctx context.Context,
	tx platformpostgres.Tx,
	agentTurnID string,
) error {
	if agentTurnID == "" {
		return application.ErrTurnOutcomeAuthorityConflict
	}
	var claimed bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM worker_turn_settlements
  WHERE agent_turn_id=$1
    AND (
      source_kind='backend_deadline'
      OR phase <> 'completed'
    )
)`,
		agentTurnID,
	).Scan(&claimed); err != nil {
		return fmt.Errorf(
			"check competing AgentTurn outcome authority: %w",
			err,
		)
	}
	if claimed {
		return application.ErrTurnOutcomeAuthorityClaimed
	}
	return nil
}
