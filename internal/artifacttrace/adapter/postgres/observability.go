package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
)

var (
	ErrObservabilityTargetNotFound = errors.New("observability projection target not found")
	ErrObservabilityTargetMismatch = errors.New("observability projection target identity mismatch")
)

type ObservabilityStore struct {
	pool *pgxpool.Pool
}

func NewObservabilityStore(pool *pgxpool.Pool) *ObservabilityStore {
	return &ObservabilityStore{pool: pool}
}

func (store *ObservabilityStore) RecordObservabilityProjection(
	ctx context.Context,
	record artifactapp.ObservabilityProjectionRecord,
) error {
	if store == nil || store.pool == nil {
		return fmt.Errorf("observability PostgreSQL pool is required")
	}
	return platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		if err := lockObservabilityTarget(ctx, tx, record); err != nil {
			return err
		}
		turnIDs, modelCount := projectionShape(record.Projection)
		var verifiedAt any
		var errorCode any
		if record.Status == artifactapp.ObservabilityPassed {
			verifiedAt = record.RecordedAt
		} else {
			errorCode = record.ErrorCode
		}
		commandTag, err := tx.Exec(ctx, `
INSERT INTO run_observability_projections
  (worker_run_id,tenant_id,user_id,workspace_id,worker_session_id,runtime_instance_id,
   runtime_generation,agent_session_id,trace_id,agent_turn_ids,model_observation_count,
   execution_outcome,status,error_code,attempted_at,verified_at,revision)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,1)
ON CONFLICT (worker_run_id) DO UPDATE SET
  status=EXCLUDED.status,error_code=EXCLUDED.error_code,attempted_at=EXCLUDED.attempted_at,
  verified_at=EXCLUDED.verified_at,revision=run_observability_projections.revision+1
WHERE run_observability_projections.tenant_id=EXCLUDED.tenant_id
  AND run_observability_projections.user_id=EXCLUDED.user_id
  AND run_observability_projections.workspace_id=EXCLUDED.workspace_id
  AND run_observability_projections.worker_session_id=EXCLUDED.worker_session_id
  AND run_observability_projections.runtime_instance_id=EXCLUDED.runtime_instance_id
  AND run_observability_projections.runtime_generation=EXCLUDED.runtime_generation
  AND run_observability_projections.agent_session_id=EXCLUDED.agent_session_id
  AND run_observability_projections.trace_id=EXCLUDED.trace_id
  AND run_observability_projections.agent_turn_ids=EXCLUDED.agent_turn_ids
  AND run_observability_projections.model_observation_count=EXCLUDED.model_observation_count
  AND run_observability_projections.execution_outcome=EXCLUDED.execution_outcome`,
			record.Projection.WorkerRunID, record.Projection.TenantID, record.Projection.UserID,
			record.Projection.WorkspaceID, record.Projection.WorkerSessionID,
			record.Projection.RuntimeInstanceID, record.Projection.RuntimeGeneration,
			record.Projection.AgentSessionID, record.Projection.TraceID, turnIDs, modelCount,
			record.ExecutionOutcome, record.Status, errorCode, record.RecordedAt, verifiedAt,
		)
		if err != nil {
			return fmt.Errorf("upsert observability projection status: %w", err)
		}
		if commandTag.RowsAffected() != 1 {
			return ErrObservabilityTargetMismatch
		}
		return nil
	})
}

func lockObservabilityTarget(
	ctx context.Context,
	tx platformpostgres.Tx,
	record artifactapp.ObservabilityProjectionRecord,
) error {
	projection := record.Projection
	var runStatus string
	var runtimeGeneration uint32
	err := tx.QueryRow(ctx, `
SELECT run.status,runtime.generation
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id AND run.worker_run_id=$2
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id AND runtime.worker_run_id=run.worker_run_id
 AND runtime.runtime_instance_id=$3
WHERE session.worker_session_id=$1
  AND session.tenant_id=$4 AND session.user_id=$5 AND session.workspace_id=$6
FOR UPDATE OF run,runtime`,
		projection.WorkerSessionID, projection.WorkerRunID, projection.RuntimeInstanceID,
		projection.TenantID, projection.UserID, projection.WorkspaceID,
	).Scan(&runStatus, &runtimeGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrObservabilityTargetNotFound
	}
	if err != nil {
		return fmt.Errorf("lock observability projection target: %w", err)
	}
	if runStatus != record.ExecutionOutcome || runtimeGeneration != projection.RuntimeGeneration {
		return ErrObservabilityTargetMismatch
	}

	rows, err := tx.Query(ctx, `
SELECT turn.agent_turn_id,turn.agent_session_id,manifest.manifest
FROM agent_turns turn
JOIN trace_manifests manifest ON manifest.agent_turn_id=turn.agent_turn_id
WHERE turn.worker_session_id=$1 AND turn.worker_run_id=$2 AND turn.status IN ('completed','failed','cancelled','timed_out')
ORDER BY turn.created_at,turn.agent_turn_id
FOR UPDATE OF turn,manifest`, projection.WorkerSessionID, projection.WorkerRunID)
	if err != nil {
		return fmt.Errorf("lock observability Turn manifests: %w", err)
	}
	defer rows.Close()
	manifests := make(map[string]contractsv1.TraceManifest, len(projection.Turns))
	for rows.Next() {
		var turnID, agentSessionID string
		var manifest contractsv1.TraceManifest
		if err := rows.Scan(&turnID, &agentSessionID, &manifest); err != nil {
			return fmt.Errorf("scan observability Turn manifest: %w", err)
		}
		if agentSessionID != string(projection.AgentSessionID) {
			return ErrObservabilityTargetMismatch
		}
		manifests[turnID] = manifest
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate observability Turn manifests: %w", err)
	}
	if len(manifests) != len(projection.Turns) {
		return ErrObservabilityTargetMismatch
	}
	for _, turn := range projection.Turns {
		stored, exists := manifests[string(turn.AgentTurnID)]
		if !exists || stored.TraceStreamID != turn.TraceManifest.TraceStreamID ||
			stored.LangfuseExport.TraceID != projection.TraceID ||
			stored.RuntimeInstanceID != projection.RuntimeInstanceID ||
			stored.RuntimeGeneration != projection.RuntimeGeneration {
			return ErrObservabilityTargetMismatch
		}
	}
	return nil
}

func projectionShape(projection artifactapp.RunObservabilityProjection) ([]string, int) {
	turnIDs := make([]string, len(projection.Turns))
	modelCount := 0
	for index, turn := range projection.Turns {
		turnIDs[index] = string(turn.AgentTurnID)
		modelCount += len(turn.ModelObservations)
	}
	slices.Sort(turnIDs)
	return turnIDs, modelCount
}

var _ artifactapp.ObservabilityStatusStore = (*ObservabilityStore)(nil)
