package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

var ErrArgusReviewLeaseLost = errors.New("Argus review job no longer owns its lease")

// QueueArgusReviews reconciles the committed delivery event with the current
// Task. The normal Outbox publisher remains free to publish Frontier updates.
func (store *DeliveryStore) QueueArgusReviews(ctx context.Context, scope taskapp.DeliveryReviewScope, profileSHA256 string) error {
	_, err := store.pool.Exec(ctx, `
INSERT INTO argus_report_review_jobs
 (task_id,task_revision,delivery_event_id,worker_run_id,artifact_id,artifact_sha256,profile_sha256)
SELECT t.task_id,t.revision,e.event_id,a.worker_run_id,a.artifact_id,substr(a.object_checksum,8),$4
FROM tasks t
JOIN outbox_events e ON e.producer_context='task-orchestration' AND e.tenant_id=t.tenant_id
 AND e.event_type='TaskStageDelivered.v1' AND e.aggregate_id=t.task_id
 AND e.idempotency_key='TaskStageDelivered.v1/' || t.task_id || '/revision-' || t.revision::text
JOIN artifact_refs a ON a.task_id=t.task_id AND a.tenant_id=t.tenant_id
 AND a.user_id=t.initiator_user_id AND a.workspace_id=t.workspace_id
 AND a.worker_run_id=t.current_worker_run_id AND a.artifact_type='report'
WHERE t.tenant_id=$1 AND t.initiator_user_id=$2 AND t.workspace_id=$3
 AND t.status='delivered' AND t.acceptance_mode='manual'
 AND a.artifact_ref->>'completeness'='complete' AND a.artifact_ref->>'availability'='online'
 AND a.object_checksum ~ '^sha256:[0-9a-f]{64}$'
 AND t.result_refs @> jsonb_build_array(jsonb_build_object(
   'artifact_id',a.artifact_id,'worker_run_id',a.worker_run_id,'sha256',substr(a.object_checksum,8)))
 AND NOT EXISTS (SELECT 1 FROM argus_report_review_jobs j WHERE j.task_id=t.task_id AND j.task_revision=t.revision)
 AND NOT EXISTS (SELECT 1 FROM delivery_reviews r WHERE r.task_id=t.task_id AND r.artifact_id=a.artifact_id)
ORDER BY t.updated_at,t.task_id
LIMIT 32
ON CONFLICT (task_id,task_revision) DO NOTHING`, scope.TenantID, scope.UserID, scope.WorkspaceID, profileSHA256)
	return err
}

func (store *DeliveryStore) ClaimArgusReview(ctx context.Context, scope taskapp.DeliveryReviewScope, profileSHA256 string, lease time.Duration) (taskapp.ArgusReviewJob, bool, error) {
	job := taskapp.ArgusReviewJob{DeliveryReviewScope: scope, ProfileSHA256: profileSHA256}
	err := store.pool.QueryRow(ctx, `
WITH candidate AS (
 SELECT j.task_id,j.task_revision FROM argus_report_review_jobs j
 JOIN tasks t ON t.task_id=j.task_id
 WHERE t.tenant_id=$1 AND t.initiator_user_id=$2 AND t.workspace_id=$3 AND j.profile_sha256=$4
   AND ((j.status='pending' AND j.available_at <= clock_timestamp())
     OR (j.status='running' AND j.lease_until <= clock_timestamp()))
 ORDER BY j.available_at,j.task_id,j.task_revision
 LIMIT 1 FOR UPDATE OF j SKIP LOCKED
)
UPDATE argus_report_review_jobs j
SET status='running',attempt=j.attempt+1,lease_until=clock_timestamp()+$5::bigint*interval '1 second'
FROM candidate c WHERE j.task_id=c.task_id AND j.task_revision=c.task_revision
RETURNING j.task_id,j.task_revision,j.worker_run_id,j.artifact_id,j.artifact_sha256,j.attempt`,
		scope.TenantID, scope.UserID, scope.WorkspaceID, profileSHA256, int64(lease/time.Second)).
		Scan(&job.TaskID, &job.TaskRevision, &job.WorkerRunID, &job.ArtifactID, &job.ArtifactSHA256, &job.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, false, nil
	}
	return job, err == nil, err
}

// ArgusReviewObject resolves only the current authorized delivery to an object
// key. The caller verifies the bytes against the digest before starting Argus.
func (store *DeliveryStore) ArgusReviewObject(ctx context.Context, job taskapp.ArgusReviewJob) (string, error) {
	var objectKey string
	err := store.pool.QueryRow(ctx, `
SELECT a.object_key FROM tasks t JOIN artifact_refs a ON a.task_id=t.task_id
 AND a.tenant_id=t.tenant_id AND a.user_id=t.initiator_user_id AND a.workspace_id=t.workspace_id
WHERE t.task_id=$1 AND t.tenant_id=$2 AND t.initiator_user_id=$3 AND t.workspace_id=$4
 AND t.status='delivered' AND t.acceptance_mode='manual' AND t.revision=$5 AND t.current_worker_run_id=$6
 AND a.worker_run_id=$6 AND a.artifact_id=$7 AND a.object_checksum='sha256:' || $8
 AND a.artifact_type='report' AND a.artifact_ref->>'completeness'='complete' AND a.artifact_ref->>'availability'='online'
 AND t.result_refs @> jsonb_build_array(jsonb_build_object(
   'artifact_id',$7::text,'worker_run_id',$6::text,'sha256',$8::text))`,
		job.TaskID, job.TenantID, job.UserID, job.WorkspaceID, job.TaskRevision,
		job.WorkerRunID, job.ArtifactID, job.ArtifactSHA256).Scan(&objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", taskapp.ErrDeliveryReviewStale
	}
	return objectKey, err
}

func (store *DeliveryStore) FinishArgusReview(ctx context.Context, job taskapp.ArgusReviewJob, review contractsv1.DeliveryReview) error {
	if err := review.Validate(); err != nil {
		return err
	}
	if review.ExpectedTaskRevision != job.TaskRevision || review.WorkerRunID != job.WorkerRunID ||
		review.ArtifactID != job.ArtifactID || review.ArtifactSHA256 != job.ArtifactSHA256 {
		return taskapp.ErrDeliveryReviewStale
	}
	payload, err := json.Marshal(review)
	if err != nil {
		return err
	}
	return platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		if err := updateArgusReviewJob(ctx, tx, job, "recorded", "", 0); err != nil {
			return err
		}
		return recordDeliveryReview(ctx, tx, job.DeliveryReviewScope, review, payload, &taskapp.DeliveryReviewReceipt{})
	})
}

func (store *DeliveryStore) ReleaseArgusReview(ctx context.Context, job taskapp.ArgusReviewJob, code string, terminal bool) error {
	if code == "stale_target" {
		return updateArgusReviewJob(ctx, store.pool, job, "superseded", code, 0)
	}
	if terminal || job.Attempt >= 5 {
		return updateArgusReviewJob(ctx, store.pool, job, "failed", code, 0)
	}
	backoff := int64(2 * job.Attempt)
	return updateArgusReviewJob(ctx, store.pool, job, "pending", code, backoff)
}

type argusReviewJobWriter interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func updateArgusReviewJob(ctx context.Context, db argusReviewJobWriter, job taskapp.ArgusReviewJob, status, code string, backoff int64) error {
	var taskID string
	err := db.QueryRow(ctx, `
UPDATE argus_report_review_jobs
SET status=$5,last_error_code=nullif($6,''),lease_until=NULL,
    available_at=clock_timestamp()+$7::bigint*interval '1 second'
WHERE task_id=$1 AND task_revision=$2 AND attempt=$3 AND profile_sha256=$4
 AND status='running' AND lease_until > clock_timestamp()
RETURNING task_id`, job.TaskID, job.TaskRevision, job.Attempt, job.ProfileSHA256, status, code, backoff).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrArgusReviewLeaseLost
	}
	return err
}
