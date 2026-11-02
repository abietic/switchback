//go:build integration

package localproduct

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeSettlementDomainPlanUsesPostTerminalGraphRevisions(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 17, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,
   status,active_runtime_instance_id,active_worker_run_id,session_revision,runtime_generation,
   quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at)
VALUES
  ('session-settlement','tenant-local','user-local','workspace-local','workspace-revision-local',
   'working-copy-settlement','running','runtime-settlement','run-settlement',4,1,1,
   $1::timestamptz + interval '48 hours',$1::timestamptz + interval '14 days',$1,$1)`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_runs
  (worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,
   agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at)
VALUES
  ('run-settlement','session-settlement','direct_agent_turn','{}'::jsonb,
   'deepseek-codex-openai-v1','1',
   'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
   'running',2,$1,$1)`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,
   revision,idempotency_key,created_at,started_at,deadline_at)
VALUES
  ('turn-settlement','session-settlement','run-settlement','agent-session-settlement','prompt',
   '{"text":"execute task"}'::jsonb,'consumed',3,'turn-settlement',$1,$1,
   $1::timestamptz + interval '24 hours')`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,container_id,
   status,image_ref,resource_profile_id,network_policy_id,control_channel_epoch,
   last_event_sequence,revision,created_at,started_at)
VALUES
  ('runtime-settlement','session-settlement','run-settlement',1,'docker_container',
   'container-settlement','active',
   'switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
   'local_standard_v1','m1_public_web_restricted_v1',1,20,41,$1,$1)`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	runner := &runtimeRunner{
		pool: pool,
		configuration: Config{Profile: config.SingleUserLocalProfile{
			TenantID: "tenant-local",
		}},
	}
	plan, err := runner.runtimeSettlementDomainPlan(
		ctx,
		workerapp.TurnSettlement{
			TenantID:          "tenant-local",
			WorkerSessionID:   "session-settlement",
			WorkerRunID:       "run-settlement",
			AgentSessionID:    "agent-session-settlement",
			AgentTurnID:       "turn-settlement",
			RuntimeInstanceID: "runtime-settlement",
			RuntimeGeneration: 1,
		},
		pendingTurn{
			TenantID:          "tenant-local",
			WorkerSessionID:   "session-settlement",
			SessionRevision:   4,
			WorkerRunID:       "run-settlement",
			RunRevision:       2,
			AgentSessionID:    "agent-session-settlement",
			AgentTurnID:       "turn-settlement",
			TurnRevision:      2,
			RuntimeInstanceID: "runtime-settlement",
			RuntimeRevision:   3,
			RuntimeGeneration: 1,
		},
		turnTerminalResult{
			reason:     "end_turn",
			sourceKind: workerapp.TurnOutcomeSourceRuntimeEvent,
			occurredAt: now.Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ExpectedSessionRevision != 4 ||
		plan.ExpectedRunRevision != 2 ||
		plan.ExpectedTurnRevision != 3 ||
		plan.ExpectedRuntimeRevision != 41 {
		t.Fatalf("post-terminal domain plan=%+v", plan)
	}
}
