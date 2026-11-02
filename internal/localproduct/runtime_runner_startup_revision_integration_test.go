//go:build integration

package localproduct

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestBindInitialAgentSessionUsesPostRuntimeReadyRevisions(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 16, 30, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,
   status,active_runtime_instance_id,active_worker_run_id,session_revision,runtime_generation,
   quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at)
VALUES
  ('session-startup','tenant-local','user-local','workspace-local','workspace-revision-local',
   'working-copy-startup','running','runtime-startup','run-startup',3,1,1,
   $1::timestamptz + interval '48 hours',$1::timestamptz + interval '14 days',$1,$1)`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_runs
  (worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,
   agent_profile_revision,agent_profile_digest,status,revision,created_at)
VALUES
  ('run-startup','session-startup','direct_agent_turn','{}'::jsonb,
   'deepseek-codex-openai-v1','1',
   'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
   'starting',1,$1)`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,
   revision,idempotency_key,created_at,started_at,deadline_at)
VALUES
  ('turn-startup','session-startup','run-startup','agent-session-reserved','prompt',
   '{"text":"execute task"}'::jsonb,'delivered',1,'turn-startup',$1,$1,
   $1::timestamptz + interval '24 hours')`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,status,
   image_ref,resource_profile_id,network_policy_id,control_channel_epoch,last_event_sequence,
   revision,created_at)
VALUES
  ('runtime-startup','session-startup','run-startup',1,'docker_container','starting',
   'switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
   'local_standard_v1','m1_public_web_restricted_v1',1,1,2,$1)`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	runner := &runtimeRunner{
		pool: pool,
		configuration: Config{Profile: config.SingleUserLocalProfile{
			TenantID: "tenant-local",
		}},
		binding: workerapp.AgentSessionBindingService{
			Store: workerpostgres.NewAgentSessionBindingStore(pool),
			IDs:   randomIDs{},
			Now:   func() time.Time { return now.Add(time.Second) },
		},
	}
	ownership := workerapp.StartedWorkerRun{
		WorkerSessionID:     "session-startup",
		WorkerRunID:         "run-startup",
		AgentTurnID:         "turn-startup",
		RuntimeInstanceID:   "runtime-startup",
		AgentSessionID:      "agent-session-reserved",
		RuntimeGeneration:   1,
		ControlChannelEpoch: 1,
		SessionRevision:     3,
		RunRevision:         1,
	}
	bound, err := runner.bindInitialAgentSession(
		ctx,
		ownership,
		"agent-session-actual",
	)
	if err != nil {
		t.Fatal(err)
	}
	if bound.AgentSessionID != "agent-session-actual" ||
		bound.SessionRevision != 3 ||
		bound.RunRevision != 1 ||
		bound.TurnRevision != 2 ||
		bound.RuntimeRevision != 2 {
		t.Fatalf("bound startup AgentSession=%+v", bound)
	}
}
