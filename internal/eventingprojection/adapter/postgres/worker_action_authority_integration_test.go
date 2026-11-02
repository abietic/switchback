//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestDeliveredBoundTaskProjectsTaskContinuationInsteadOfStandaloneFollowUp(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 25, 10, 0, 0, 0, time.UTC)
	seedDeliveredBoundTask(t, ctx, pool, now)

	_, actions, _, _, err := (Materializer{
		Pool: pool, UserID: "user-local", Now: func() time.Time { return now },
	}).workerDetail(ctx, "tenant-local", "session-1")
	if err != nil {
		t.Fatal(err)
	}

	if hasAction(actions, "worker.follow_up", "session-1", 4) {
		t.Fatal("delivered bound Task advertised standalone worker.follow_up")
	}
	if !hasAction(actions, "task.continue", "task-1", 3) {
		t.Fatalf("delivered bound Task actions = %+v, want exact task.continue authority", actions)
	}
}

func hasAction(actions []projectionapp.ActionRef, action, targetID string, revision uint64) bool {
	for _, candidate := range actions {
		if candidate.Action == action && candidate.TargetID == targetID &&
			candidate.ExpectedRevision == revision {
			return true
		}
	}
	return false
}

func seedDeliveredBoundTask(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	now time.Time,
) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	profile, _ := platformconfig.ProductionProfileRegistry().Agent(platformconfig.DeepSeekCodexOpenAIV1)
	statements := []string{
		`INSERT INTO task_readiness_assessments
 (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,
  dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
 VALUES ('assessment-1','tenant-local','user-local','workspace-local','workspace-revision-local-v1',
 'api','policy-1','{}','[]','[]','ready_confirmed',$1,$2)`,
		`INSERT INTO task_creation_intents
 (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,
  task_spec_digest,trigger_policy_revision,status,consumed_by_task_id,created_at,expires_at,consumed_at)
 VALUES ('intent-1','tenant-local','user-local','workspace-local','workspace-revision-local-v1',
 'assessment-1',$1,'trigger-1','consumed','task-1',$2::timestamptz,$2::timestamptz + interval '15 minutes',$2::timestamptz)`,
		`INSERT INTO tasks
 (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,
  task_spec_digest,creation_intent_id,worker_session_id,current_worker_run_id,status,
  acceptance_mode,result_refs,revision,created_at,updated_at)
 VALUES ('task-1','tenant-local','user-local','workspace-local','workspace-revision-local-v1',
 '{}',$1,'intent-1','session-1','run-1','delivered','manual','[]',3,$2,$2)`,
		`WITH ignored AS (SELECT $1::text) INSERT INTO worker_sessions
 (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,
  active_runtime_instance_id,bound_task_id,session_revision,runtime_generation,quota_reservation_revision,
  idle_expires_at,absolute_expires_at,created_at,updated_at)
 VALUES ('session-1','tenant-local','user-local','workspace-local','workspace-revision-local-v1',
 'working-copy-1','interactive','runtime-1','task-1',4,1,1,$2::timestamptz + interval '48 hours',
 $2::timestamptz + interval '14 days',$2::timestamptz,$2::timestamptz)`,
		fmt.Sprintf(`WITH ignored AS (SELECT $1::text) INSERT INTO worker_runs
 (worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,agent_profile_id,
  agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at,terminal_at)
 VALUES ('run-1','session-1','task-1','task_initial','{}','%s','1','%s','completed',3,$2,$2,$2)`,
			profile.ID, profile.Digest),
		`WITH ignored AS (SELECT $1::text) INSERT INTO agent_turns
 (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
  idempotency_key,created_at,started_at,deadline_at,terminal_at)
 VALUES ('turn-1','session-1','run-1','codex-session-real-1','prompt','{"text":"initial"}',
 'completed',2,'initial-turn',$2::timestamptz,$2::timestamptz,$2::timestamptz + interval '24 hours',$2::timestamptz)`,
		fmt.Sprintf(`WITH ignored AS (SELECT $1::text) INSERT INTO runtime_instances
 (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,container_id,status,
  image_ref,resource_profile_id,network_policy_id,control_channel_epoch,revision,created_at,started_at)
 VALUES ('runtime-1','session-1','run-1',1,'docker_container','container-1','active',
 'switchback-worker@sha256:%s','local_standard_v1','m1_public_web_restricted_v1',1,2,$2,$2)`,
			strings.Repeat("b", 64)),
	}
	for index, statement := range statements {
		if _, err := pool.Exec(ctx, statement, digest, now); err != nil {
			t.Fatalf("seed statement %d: %v", index+1, err)
		}
	}
}
