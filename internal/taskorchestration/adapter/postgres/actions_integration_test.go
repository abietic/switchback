//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	platformconfig "github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestReworkAndCancelUseExactResidentSession(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
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
		`WITH ignored AS (SELECT $1::text) INSERT INTO scheduled_commands
 (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
  due_at,status,idempotency_key,created_at,updated_at)
 VALUES ('idle-session-1','tenant-local','worker_session_idle_expiry','WorkerSession','session-1',4,
  '{}',$2::timestamptz + interval '48 hours','pending','worker_session_idle_expiry/session-1',$2,$2)`,
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
 'switchback-worker@sha256:%s','local_standard_v1','m1_public_web_restricted_v1',1,2,$2,$2)`, strings.Repeat("b", 64)),
	}
	for index, statement := range statements {
		if _, err := pool.Exec(ctx, statement, digest, now); err != nil {
			t.Fatalf("seed statement %d: %v", index+1, err)
		}
	}
	ids := &actionIDs{}
	service := taskapp.TaskActionService{Store: taskpostgres.NewStore(pool), IDs: ids, Now: func() time.Time { return now.Add(time.Minute) }}
	reworked, err := service.Rework(ctx, taskapp.ReworkTaskCommand{
		Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-1",
		ExpectedTaskRevision: 3, Message: "Add the missing edge-case test.", IdempotencyKey: "rework-1",
	})
	if err != nil || reworked.Status != "running" || reworked.Revision != 4 || reworked.WorkerRunID == "run-1" {
		t.Fatalf("reworked=%+v error=%v", reworked, err)
	}
	retry, err := service.Rework(ctx, taskapp.ReworkTaskCommand{
		Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-1",
		ExpectedTaskRevision: 3, Message: "Add the missing edge-case test.", IdempotencyKey: "rework-1",
	})
	if err != nil || retry != reworked {
		t.Fatalf("rework retry=%+v error=%v", retry, err)
	}
	var runtimeID, agentSessionID string
	if err := pool.QueryRow(ctx, `SELECT active_runtime_instance_id FROM worker_sessions WHERE worker_session_id='session-1'`).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT agent_session_id FROM agent_turns WHERE agent_turn_id=$1`, reworked.AgentTurnID).Scan(&agentSessionID); err != nil {
		t.Fatal(err)
	}
	if runtimeID != "runtime-1" || agentSessionID != "codex-session-real-1" {
		t.Fatalf("runtime=%s AgentSession=%s", runtimeID, agentSessionID)
	}
	cancelled, err := service.Cancel(ctx, taskapp.CancelTaskCommand{
		Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-1",
		ExpectedTaskRevision: reworked.Revision, IdempotencyKey: "cancel-rework-1",
	})
	if err != nil || cancelled.Status != "cancelled" || cancelled.Revision != 5 {
		t.Fatalf("cancelled=%+v error=%v", cancelled, err)
	}
	var controlType, controlStatus, controlTurnID string
	if err := pool.QueryRow(ctx, `SELECT command_type,status,agent_turn_id FROM worker_user_commands
WHERE idempotency_key='task-cancel-control/cancel-rework-1'`).Scan(&controlType, &controlStatus, &controlTurnID); err != nil {
		t.Fatal(err)
	}
	if controlType != "interrupt" || controlStatus != "accepted" || controlTurnID != reworked.AgentTurnID {
		t.Fatalf("control=%s status=%s turn=%s", controlType, controlStatus, controlTurnID)
	}
}

type actionIDs struct{ value atomic.Uint64 }

func (ids *actionIDs) NewID(kind string) (string, error) {
	return fmt.Sprintf("%s-%d", kind, ids.value.Add(1)), nil
}
