package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/jackc/pgx/v5"
)

func (store *Store) RecordRuntimeDispatchWait(ctx context.Context, record application.RuntimeDispatchWaitRecord) (application.RuntimeDispatchWait, error) {
	observation := record.Observation
	wait := observation.Wait
	if err := observation.Validate(); err != nil {
		return application.RuntimeDispatchWait{}, err
	}
	// Do not trust caller time, including response-loss retries.
	wait.ObservedAt = time.Time{}
	if record.EventID == "" || strings.TrimSpace(record.EventID) != record.EventID || strings.ContainsAny(record.EventID, "\r\n\x00") {
		return application.RuntimeDispatchWait{}, fmt.Errorf("invalid diagnostic event identity")
	}
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var tenant, actor, status, session, run string
		var revision, sequence uint64
		var existing json.RawMessage
		err := tx.QueryRow(ctx, `SELECT tenant_id,initiator_user_id,status,revision,COALESCE(worker_session_id,''),COALESCE(current_worker_run_id,''),COALESCE(session_queue_sequence,0),runtime_dispatch_wait FROM tasks WHERE task_id=$1 FOR UPDATE`, observation.TaskID).Scan(&tenant, &actor, &status, &revision, &session, &run, &sequence, &existing)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if tenant != observation.TenantID || actor != observation.UserID {
			return platformpostgres.ErrNotFound
		}
		if status != "queued" || run != "" || revision != wait.TaskRevision || session != wait.WorkerSessionID || sequence != wait.QueueSequence {
			return platformpostgres.ErrRevisionConflict
		}
		if session != "" {
			var queueRevision uint64
			err = tx.QueryRow(ctx, `SELECT queue_revision FROM session_task_queue_entries WHERE task_id=$1 AND worker_session_id=$2 AND session_queue_sequence=$3 AND status IN ('pending','permit_issued')`, observation.TaskID, session, sequence).Scan(&queueRevision)
			if errors.Is(err, pgx.ErrNoRows) {
				return platformpostgres.ErrRevisionConflict
			}
			if err != nil {
				return err
			}
			if queueRevision != wait.QueueRevision {
				return platformpostgres.ErrRevisionConflict
			}
		}
		// New Issue owns this same Task lock. Retain all issuance identities,
		// including inactive history, so an issued-then-expired permit cannot
		// restore an old "never issued" observation (ABA).
		var issuedPermitIDs []string
		if err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(permit_id ORDER BY permit_id COLLATE "C"),ARRAY[]::text[])
FROM session_dispatch_permits WHERE target_kind='task' AND target_id=$1
 AND worker_session_id=$2 AND session_queue_sequence=$3 AND queue_revision=$4`,
			observation.TaskID, session, sequence, wait.QueueRevision).Scan(&issuedPermitIDs); err != nil {
			return err
		}
		if !slices.Equal(issuedPermitIDs, observation.ExpectedPermitIDs) {
			return platformpostgres.ErrRevisionConflict
		}
		if len(existing) > 0 {
			var prior application.RuntimeDispatchWait
			if err = json.Unmarshal(existing, &prior); err != nil {
				return fmt.Errorf("decode stored dispatch wait: %w", err)
			}
			if err = prior.ValidateBinding(); err != nil || prior.ObservedAt.IsZero() {
				return fmt.Errorf("invalid stored dispatch wait")
			}
			withoutTime := prior
			withoutTime.ObservedAt = time.Time{}
			if withoutTime == wait {
				wait = prior
				return nil
			}
		}
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&wait.ObservedAt); err != nil {
			return err
		}
		encoded, err := json.Marshal(wait)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE tasks SET runtime_dispatch_wait=$2 WHERE task_id=$1`, observation.TaskID, encoded); err != nil {
			return err
		}
		return store.appendTaskActionEvent(ctx, tx, record.EventID, tenant, observation.TaskID, "TaskRuntimeDispatchWaitChanged.v1", record.EventID, "", revision, map[string]any{"runtime_dispatch_wait": map[string]any{"reason": wait.Reason, "observed_at": wait.ObservedAt}}, wait.ObservedAt)
	})
	return wait, err
}

// Only the original new Issue transaction calls this. Permit replay must retain
// any refusal observed after that permit was issued.
func (store *Store) clearRuntimeDispatchWait(ctx context.Context, tx platformpostgres.Tx, taskID, tenantID, eventID string, revision uint64, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE tasks SET runtime_dispatch_wait=NULL WHERE task_id=$1 AND runtime_dispatch_wait IS NOT NULL`, taskID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return store.appendTaskActionEvent(ctx, tx, eventID, tenantID, taskID, "TaskRuntimeDispatchWaitChanged.v1", eventID, "", revision, nil, now)
}
