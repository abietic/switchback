package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
)

func (store *DeliveryStore) RecordDeliveryReview(ctx context.Context, scope application.DeliveryReviewScope, review contractsv1.DeliveryReview) (application.DeliveryReviewReceipt, error) {
	receipt := application.DeliveryReviewReceipt{DeliveryReview: review, Reviewer: "argus"}
	if err := review.Validate(); err != nil {
		return receipt, err
	}
	payload, err := json.Marshal(review)
	if err != nil {
		return receipt, err
	}
	err = platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		// Serialize against Accept and Rework, which lock the same Task row.
		var current bool
		err := tx.QueryRow(ctx, `
SELECT t.status='delivered' AND t.revision=$5 AND t.current_worker_run_id IS NOT DISTINCT FROM $6::text
   AND t.result_refs @> jsonb_build_array(jsonb_build_object('artifact_id',$7::text,'worker_run_id',$6::text,'sha256',$8::text))
   AND EXISTS (
     SELECT 1 FROM artifact_refs a WHERE a.artifact_id=$7 AND a.task_id=t.task_id
       AND a.tenant_id=t.tenant_id AND a.user_id=t.initiator_user_id AND a.workspace_id=t.workspace_id
       AND a.worker_run_id=$6 AND a.artifact_type='report' AND a.object_checksum='sha256:' || $8
       AND a.artifact_ref->>'completeness'='complete' AND a.artifact_ref->>'availability'='online'
   )
FROM tasks t WHERE t.task_id=$1 AND t.tenant_id=$2 AND t.initiator_user_id=$3 AND t.workspace_id=$4
FOR UPDATE OF t`, scope.TaskID, scope.TenantID, scope.UserID, scope.WorkspaceID,
			review.ExpectedTaskRevision, review.WorkerRunID, review.ArtifactID, review.ArtifactSHA256).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrDeliveryReviewUnavailable
		}
		if err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `
SELECT payload=$3::jsonb, recorded_at FROM delivery_reviews WHERE task_id=$1 AND review_run_id=$2`,
			scope.TaskID, review.ReviewRunID, payload).Scan(&same, &receipt.RecordedAt)
		if err == nil {
			if same {
				return nil
			}
			return application.ErrIdempotencyConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !current {
			return application.ErrDeliveryReviewStale
		}
		err = tx.QueryRow(ctx, `
INSERT INTO delivery_reviews (review_run_id,task_id,worker_run_id,artifact_id,artifact_sha256,payload)
VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING RETURNING recorded_at`,
			review.ReviewRunID, scope.TaskID, review.WorkerRunID, review.ArtifactID, review.ArtifactSHA256, payload).Scan(&receipt.RecordedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrIdempotencyConflict
		}
		return err
	})
	return receipt, err
}

func (store *DeliveryStore) ReadDeliveryReview(ctx context.Context, scope application.DeliveryReviewScope) (application.CurrentDeliveryReview, error) {
	var result application.CurrentDeliveryReview
	var payload []byte
	err := store.pool.QueryRow(ctx, `
SELECT t.revision, COALESCE(review.receipt,'null'::jsonb)
FROM tasks t LEFT JOIN LATERAL (
  SELECT r.payload || jsonb_build_object('reviewer','argus','recorded_at',r.recorded_at) AS receipt
  FROM delivery_reviews r
  WHERE r.task_id=t.task_id AND t.status IN ('delivered','completed') AND r.worker_run_id=t.current_worker_run_id
    AND t.result_refs @> jsonb_build_array(jsonb_build_object(
      'artifact_id',r.artifact_id,'worker_run_id',r.worker_run_id,'sha256',r.artifact_sha256))
  ORDER BY r.recorded_at DESC,r.review_run_id DESC LIMIT 1
) review ON true
WHERE t.task_id=$1 AND t.tenant_id=$2 AND t.initiator_user_id=$3 AND t.workspace_id=$4`,
		scope.TaskID, scope.TenantID, scope.UserID, scope.WorkspaceID).Scan(&result.TaskRevision, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, application.ErrDeliveryReviewUnavailable
	}
	if err != nil {
		return result, err
	}
	return result, json.Unmarshal(payload, &result.Review)
}

var _ application.DeliveryReviewStore = (*DeliveryStore)(nil)
