package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

const (
	agentTurnOutcomeLockPrefix          = "worker-runtime:agent-turn-outcome:"
	runtimeTurnOutcomeGraphLockAttempts = 2
)

type runtimeTurnOutcomeLock struct {
	WorkerSessionID string
	WorkerRunID     string
	AgentTurnID     string
}

// lockAgentTurnOutcome serializes durable source claims before either caller
// locks the mutable Session/Run/Turn/Runtime graph. The Runtime terminal-event
// and recovery paths both use it before checking their competing facts.
func lockAgentTurnOutcome(
	ctx context.Context,
	tx platformpostgres.Tx,
	agentTurnID string,
) error {
	if agentTurnID == "" {
		return application.ErrTurnOutcomeAuthorityConflict
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		agentTurnOutcomeLockPrefix+agentTurnID,
	); err != nil {
		return fmt.Errorf("lock AgentTurn outcome authority: %w", err)
	}
	return nil
}

// lockRuntimeTurnOutcomeBeforeGraph resolves the currently active Turn without
// taking mutable graph locks, then enters the same outcome-authority critical
// section used by terminal/deadline/recovery writers. Callers must revalidate
// the returned identity after they lock Session/Runtime because a queued Turn
// can win between the initial read and those locks.
func lockRuntimeTurnOutcomeBeforeGraph(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
) (runtimeTurnOutcomeLock, error) {
	identity, found, err := readRuntimeTurnOutcomeIdentity(
		ctx,
		tx,
		runtimeInstanceID,
	)
	if err != nil || !found || identity.AgentTurnID == "" {
		return identity, err
	}
	if identity.WorkerRunID == "" {
		return runtimeTurnOutcomeLock{},
			application.ErrTurnOutcomeAuthorityConflict
	}
	if err := lockAgentTurnOutcome(
		ctx,
		tx,
		identity.AgentTurnID,
	); err != nil {
		return runtimeTurnOutcomeLock{}, err
	}
	return identity, nil
}

func validateRuntimeTurnOutcomeAfterGraphLock(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	locked runtimeTurnOutcomeLock,
) error {
	current, found, err := readRuntimeTurnOutcomeIdentity(
		ctx,
		tx,
		runtimeInstanceID,
	)
	if err != nil {
		return err
	}
	if !found {
		current = runtimeTurnOutcomeLock{}
	}
	if current != locked {
		return platformpostgres.ErrRevisionConflict
	}
	return nil
}

func readRuntimeTurnOutcomeIdentity(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
) (runtimeTurnOutcomeLock, bool, error) {
	var identity runtimeTurnOutcomeLock
	err := tx.QueryRow(ctx, `
SELECT session.worker_session_id,
       COALESCE(session.active_worker_run_id,''),
       COALESCE((
         SELECT turn.agent_turn_id
         FROM agent_turns turn
         WHERE turn.worker_session_id=session.worker_session_id
           AND turn.worker_run_id=session.active_worker_run_id
         ORDER BY turn.created_at DESC,turn.agent_turn_id DESC
         LIMIT 1
       ),'')
FROM runtime_instances runtime
JOIN worker_sessions session
  ON session.worker_session_id=runtime.worker_session_id
WHERE runtime.runtime_instance_id=$1
  AND session.active_runtime_instance_id=runtime.runtime_instance_id`,
		runtimeInstanceID,
	).Scan(
		&identity.WorkerSessionID,
		&identity.WorkerRunID,
		&identity.AgentTurnID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimeTurnOutcomeLock{}, false, nil
	}
	if err != nil {
		return runtimeTurnOutcomeLock{},
			false,
			fmt.Errorf(
				"resolve Runtime AgentTurn outcome identity: %w",
				err,
			)
	}
	if (identity.WorkerRunID == "") !=
		(identity.AgentTurnID == "") {
		return runtimeTurnOutcomeLock{},
			false,
			application.ErrTurnOutcomeAuthorityConflict
	}
	return identity, true, nil
}

func rejectTerminalEventClaimedByRuntimeRecovery(
	ctx context.Context,
	tx platformpostgres.Tx,
	agentTurnID string,
	runtimeInstanceID string,
	runtimeGeneration uint64,
	channelEpoch uint64,
	acceptedAt time.Time,
) error {
	var claimed bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM runtime_recovery_incidents incident
  JOIN agent_turns turn
    ON turn.agent_turn_id=incident.agent_turn_id
  WHERE incident.agent_turn_id=$1
    AND incident.failed_runtime_instance_id=$2
    AND incident.failed_generation=$3
    AND incident.failed_channel_epoch=$4
    AND $5::timestamptz < turn.deadline_at
    AND incident.phase NOT IN ('recovered','exhausted')
)`,
		agentTurnID,
		runtimeInstanceID,
		runtimeGeneration,
		channelEpoch,
		acceptedAt,
	).Scan(&claimed); err != nil {
		return fmt.Errorf(
			"check Runtime recovery authority before terminal event Accept: %w",
			err,
		)
	}
	if claimed {
		return application.ErrTurnOutcomeAuthorityClaimed
	}
	return nil
}
