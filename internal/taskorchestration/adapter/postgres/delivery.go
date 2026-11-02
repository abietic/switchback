package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

type DeliveryStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewDeliveryStore(pool *pgxpool.Pool) *DeliveryStore { return &DeliveryStore{pool: pool} }

func (store *DeliveryStore) RecordStageResult(ctx context.Context, record application.TaskDeliveryRecord) (application.TaskDeliveryResult, error) {
	var result application.TaskDeliveryResult
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		existing, found, err := readDeliveryResult(ctx, tx, record.Command.TaskID, record.Command.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			result = existing
			return nil
		}
		var tenantID, initiatorID, status, acceptanceMode string
		var sessionID, runID *string
		var revision uint64
		err = tx.QueryRow(ctx, `
SELECT tenant_id,initiator_user_id,status,acceptance_mode,worker_session_id,current_worker_run_id,revision
FROM tasks WHERE task_id=$1 FOR UPDATE`, record.Command.TaskID).Scan(
			&tenantID, &initiatorID, &status, &acceptanceMode, &sessionID, &runID, &revision)
		if errors.Is(err, pgx.ErrNoRows) || tenantID != record.Command.Actor.TenantID || initiatorID != record.Command.Actor.UserID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if revision != record.Command.ExpectedTaskRevision || status != string(domain.TaskRunning) ||
			sessionID == nil || *sessionID != record.Command.WorkerSessionID || runID == nil || *runID != record.Command.WorkerRunID {
			return platformpostgres.ErrRevisionConflict
		}
		kinds := make([]domain.ArtifactKind, 0, len(record.Command.ArtifactRefs))
		seen := make(map[string]struct{}, len(record.Command.ArtifactRefs))
		for _, ref := range record.Command.ArtifactRefs {
			if err := ref.Validate(); err != nil || string(ref.TaskID) != record.Command.TaskID ||
				string(ref.WorkerSessionID) != record.Command.WorkerSessionID || string(ref.WorkerRunID) != record.Command.WorkerRunID {
				return fmt.Errorf("stage ArtifactRef does not match Task delivery identity")
			}
			if _, duplicate := seen[ref.Type]; duplicate {
				return fmt.Errorf("stage ArtifactRef type is duplicated")
			}
			seen[ref.Type] = struct{}{}
			kinds = append(kinds, domain.ArtifactKind(ref.Type))
		}
		decision, err := domain.DecideStageResult(domain.StageResult{
			CurrentStatus: domain.TaskStatus(status), AcceptanceMode: domain.AcceptanceMode(acceptanceMode),
			Artifacts: kinds, DeterministicEvidence: record.Command.DeterministicEvidence,
		})
		if err != nil {
			return err
		}
		refs, err := json.Marshal(record.Command.ArtifactRefs)
		if err != nil {
			return err
		}
		var nextRevision uint64
		err = tx.QueryRow(ctx, `
UPDATE tasks SET status=$2,result_refs=$3,revision=revision+1,updated_at=$4
WHERE task_id=$1 AND revision=$5 AND status='running'
RETURNING revision`, record.Command.TaskID, decision.TargetStatus, refs, record.Now, revision).Scan(&nextRevision)
		if err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		transitionResult, _ := json.Marshal(map[string]any{
			"artifact_count": len(record.Command.ArtifactRefs), "event_id": record.EventID,
		})
		if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,'running',$6,'worker_stage_result',$7,$8)`,
			record.TransitionID, record.Command.TaskID, record.Command.IdempotencyKey,
			revision, nextRevision, decision.TargetStatus, transitionResult, record.Now); err != nil {
			return err
		}
		if err := store.appendDeliveryEvent(ctx, tx, record.EventID, record.Command.Actor.TenantID,
			record.Command.TaskID, "TaskStageDelivered.v1", "", nextRevision, record.Now); err != nil {
			return err
		}
		result = application.TaskDeliveryResult{TaskID: record.Command.TaskID, Status: string(decision.TargetStatus), Revision: nextRevision, EventID: record.EventID}
		return nil
	})
	return result, err
}

func (store *DeliveryStore) AcceptTask(ctx context.Context, record application.TaskAcceptanceRecord) (application.TaskDeliveryResult, error) {
	var result application.TaskDeliveryResult
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		existing, found, err := readDeliveryResult(ctx, tx, record.Command.TaskID, record.Command.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			result = existing
			return nil
		}
		var tenantID, initiatorID, status string
		var sessionID *string
		var revision uint64
		err = tx.QueryRow(ctx, `
SELECT tenant_id,initiator_user_id,status,worker_session_id,revision
FROM tasks WHERE task_id=$1 FOR UPDATE`, record.Command.TaskID).Scan(
			&tenantID, &initiatorID, &status, &sessionID, &revision)
		if errors.Is(err, pgx.ErrNoRows) || tenantID != record.Command.Actor.TenantID || initiatorID != record.Command.Actor.UserID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		next, err := domain.AcceptDelivered(domain.TaskActionEvidence{
			ActorUserID: record.Command.Actor.UserID, InitiatorUserID: initiatorID,
			ExpectedRevision: record.Command.ExpectedTaskRevision, CurrentRevision: revision,
			CurrentStatus: domain.TaskStatus(status), SessionLive: sessionID != nil,
		})
		if err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		var nextRevision uint64
		err = tx.QueryRow(ctx, `
UPDATE tasks SET status=$2,revision=revision+1,updated_at=$3
WHERE task_id=$1 AND revision=$4 AND status='delivered'
RETURNING revision`, record.Command.TaskID, next, record.Now, revision).Scan(&nextRevision)
		if err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		if sessionID != nil {
			var sessionRevision uint64
			err := tx.QueryRow(ctx, `
UPDATE worker_sessions
SET bound_task_id=NULL,status=CASE WHEN status='waiting_user' THEN 'interactive' ELSE status END,
    session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND bound_task_id=$2 AND active_worker_run_id IS NULL
  AND status IN ('interactive','idle','waiting_user')
RETURNING session_revision`, *sessionID, record.Command.TaskID, record.Now).Scan(&sessionRevision)
			if err != nil {
				return platformpostgres.ErrRevisionConflict
			}
			if err := refreshSessionIdleDeadline(ctx, tx, *sessionID, sessionRevision, record.Now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,'delivered','completed','initiator_accept',
  jsonb_build_object('accepted',true,'event_id',$6::text),$7)`,
			record.TransitionID, record.Command.TaskID, record.Command.IdempotencyKey,
			revision, nextRevision, record.EventID, record.Now); err != nil {
			return err
		}
		if err := store.appendDeliveryEvent(ctx, tx, record.EventID, record.Command.Actor.TenantID,
			record.Command.TaskID, "TaskAccepted.v1", record.Command.IdempotencyKey,
			nextRevision, record.Now); err != nil {
			return err
		}
		result = application.TaskDeliveryResult{TaskID: record.Command.TaskID, Status: string(next), Revision: nextRevision, EventID: record.EventID}
		return nil
	})
	return result, err
}

func readDeliveryResult(ctx context.Context, tx platformpostgres.Tx, taskID, idempotencyKey string) (application.TaskDeliveryResult, bool, error) {
	var result application.TaskDeliveryResult
	var transitionResult json.RawMessage
	err := tx.QueryRow(ctx, `
SELECT task_id,to_state,result_revision,result FROM task_transitions
WHERE task_id=$1 AND idempotency_key=$2`, taskID, idempotencyKey).Scan(
		&result.TaskID, &result.Status, &result.Revision, &transitionResult)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TaskDeliveryResult{}, false, nil
	}
	if err != nil {
		return application.TaskDeliveryResult{}, false, err
	}
	var metadata struct {
		EventID string `json:"event_id"`
	}
	if json.Unmarshal(transitionResult, &metadata) != nil || metadata.EventID == "" {
		return application.TaskDeliveryResult{}, false, fmt.Errorf("Task delivery result is incomplete")
	}
	result.EventID = metadata.EventID
	return result, true, nil
}

func (store *DeliveryStore) appendDeliveryEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	eventID, tenantID, taskID, eventType, clientCommandID string,
	revision uint64,
	now time.Time,
) error {
	stream := platformpostgres.OwnerStream{StreamID: "task/" + taskID, OwnerContext: "task-orchestration", AggregateType: "Task", AggregateID: taskID}
	sequence, err := store.streams.Next(ctx, tx, stream)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"task_id": taskID, "task_revision": revision})
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: eventID, EventType: eventType,
		ProducerContext: "task-orchestration", StreamID: stream.StreamID, Sequence: sequence,
		TenantID: tenantID, AggregateType: "Task", AggregateID: taskID, TraceID: "task/" + taskID,
		CorrelationID: eventID, CausationID: taskID,
		ClientCommandID: clientCommandID,
		IdempotencyKey:  fmt.Sprintf("%s/%s/revision-%d", eventType, taskID, revision),
		Visibility:      "internal", OccurredAt: now.UTC(), Payload: payload,
	}
	if err := event.Validate(); err != nil {
		return err
	}
	return insertOutboxEvent(ctx, tx, event)
}

var _ application.TaskDeliveryStore = (*DeliveryStore)(nil)
