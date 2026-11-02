//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestWorkerRepositoriesUseExpectedRevisionCAS(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedWorkerGraph(t, pool, now)
	repository := workerpostgres.Repository{}

	tests := []struct {
		name    string
		advance func(platformpostgres.Tx) (uint64, error)
	}{
		{
			name: "WorkerSession",
			advance: func(tx platformpostgres.Tx) (uint64, error) {
				return repository.AdvanceSessionStatus(ctx, tx, "session-1", 1, workerdomain.SessionAllocating, workerdomain.SessionInteractive, now.Add(time.Second), "")
			},
		},
		{
			name: "WorkerRun",
			advance: func(tx platformpostgres.Tx) (uint64, error) {
				return repository.AdvanceRunStatus(ctx, tx, "run-1", 1, workerdomain.RunStarting, workerdomain.RunRunning, now.Add(time.Second), "")
			},
		},
		{
			name: "AgentTurn",
			advance: func(tx platformpostgres.Tx) (uint64, error) {
				return repository.AdvanceTurnStatus(ctx, tx, "turn-1", 1, workerdomain.TurnAccepted, workerdomain.TurnDelivered, now.Add(time.Second), "")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var revision uint64
			err := platformpostgres.InTransaction(ctx, pool, func(tx platformpostgres.Tx) error {
				var err error
				revision, err = test.advance(tx)
				return err
			})
			if err != nil || revision != 2 {
				t.Fatalf("revision=%d err=%v", revision, err)
			}
		})
	}
}

func seedWorkerGraph(t *testing.T, pool platformpostgres.Execer, now time.Time) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`INSERT INTO worker_session_continuity_quotas
  (tenant_id,user_id,quota_limit,reserved_count,revision,policy_revision,updated_at)
VALUES ('tenant-local','user-local',10,1,1,'local-v1',$1)`,
		`INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,
   session_revision,runtime_generation,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at)
VALUES ('session-1','tenant-local','user-local','workspace-demo','workspace-revision-1','working-copy-1','allocating',
  1,0,1,$1::timestamptz + interval '48 hours',$1::timestamptz + interval '14 days',$1,$1)`,
		`INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,
   dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
VALUES ('assessment-1','tenant-local','user-local','workspace-demo','workspace-revision-1','api','direct-ready-v1',
  '{}'::jsonb,'[]'::jsonb,'[]'::jsonb,'ready_confirmed',
  'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',$1)`,
		`INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,
   task_spec_digest,trigger_policy_revision,status,created_at,expires_at)
VALUES ('intent-1','tenant-local','user-local','workspace-demo','workspace-revision-1','assessment-1',
  'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','direct-create-v1','active',
  $1,$1::timestamptz + interval '15 minutes')`,
		`INSERT INTO tasks
  (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,task_spec_digest,
   creation_intent_id,status,acceptance_mode,result_refs,revision,created_at,updated_at)
VALUES ('task-1','tenant-local','user-local','workspace-demo','workspace-revision-1','{}'::jsonb,
  'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','intent-1','queued','manual','[]'::jsonb,1,$1,$1)`,
		`INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,
   agent_profile_digest,status,revision,created_at)
VALUES ('run-1','session-1','task-1','task_initial','{}'::jsonb,'deepseek-codex-openai-v1','1',
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','starting',1,$1)`,
		`INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,idempotency_key,created_at)
VALUES ('turn-1','session-1','run-1','agent-session-1','prompt','{}'::jsonb,'accepted',1,'turn-command-1',$1)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed Worker graph: %v", err)
		}
	}
}
