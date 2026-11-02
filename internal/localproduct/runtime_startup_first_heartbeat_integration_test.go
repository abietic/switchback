//go:build integration

package localproduct

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestStartupSafetyDrainOpensOverdueFirstHeartbeatRecoveryBeforeRestore(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	runtimeStartedAt := time.Date(
		2026,
		7,
		27,
		8,
		0,
		0,
		0,
		time.UTC,
	)
	resumeAt := runtimeStartedAt.Add(5 * time.Minute)
	unhealthyDueAt := resumeAt.Add(workerapp.RuntimeUnhealthyAfter)
	now := unhealthyDueAt.Add(time.Second)

	seedBackendDeadlineClaimGraph(
		t,
		pool,
		runtimeStartedAt.Add(24*time.Hour),
	)
	if _, err := pool.Exec(ctx, `
UPDATE worker_runs
SET start_snapshot=jsonb_build_object(
      'working_copy_checkpoint_digest',
      'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
    )
WHERE worker_run_id='run-deadline-claim'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_control_channels
  (runtime_instance_id,worker_session_id,runtime_generation,channel_epoch,
   credential_digest,process_nonce,stream_attempt,owner_id,owner_fence,
   resume_claim_revision,resume_ready_digest,resume_completed_revision,
   resume_completed_at,resume_completed_state,status,revision,bound_at,
   claimed_at,updated_at)
VALUES
  ('runtime-deadline-claim','session-deadline-claim',1,7,
   'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
   'process-startup-watchdog',1,'backend-before-restart',1,1,
   'sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
   2,$1,'{}'::json,'connected',3,$2,$2,$1)`,
		resumeAt,
		runtimeStartedAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_first_heartbeat_watchdogs
  (runtime_instance_id,tenant_id,worker_session_id,runtime_generation,
   channel_epoch,anchor_kind,anchor_revision,
   first_authenticated_resume_at,suspect_due_at,unhealthy_due_at,status,
   suspect_at,revision,created_at,updated_at)
VALUES
  ('runtime-deadline-claim','tenant-local','session-deadline-claim',1,7,
   'resume_complete_v1',2,$1,$1::timestamptz + interval '45 seconds',
   $1::timestamptz + interval '90 seconds','suspect',
   $1::timestamptz + interval '45 seconds',2,$1,
   $1::timestamptz + interval '45 seconds')`,
		resumeAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,idempotency_key,created_at,updated_at)
VALUES
  ('runtime-first-heartbeat-startup-unhealthy','tenant-local',
   'runtime_first_heartbeat_evaluate','RuntimeFirstHeartbeatWatchdog',
   'runtime-deadline-claim',2,
   jsonb_build_object(
     'runtime_generation',1,
     'channel_epoch',7,
     'anchor_kind','resume_complete_v1',
     'anchor_revision',2,
     'stage','unhealthy'
   ),
   $1::timestamptz + interval '90 seconds','pending',
   'first-heartbeat/1/7/unhealthy',$1,$1)`,
		resumeAt,
	); err != nil {
		t.Fatal(err)
	}

	runner := startupDeadlineTestRunner(pool, func() time.Time {
		return now
	})
	runner.configuration.Profile.ActorUserID = "user-local"
	handler, ok := runner.scheduled.Handler.(localScheduledCommandHandler)
	if !ok {
		t.Fatalf(
			"startup ScheduledCommand handler=%T",
			runner.scheduled.Handler,
		)
	}
	ids := randomIDs{}
	handler.firstHeartbeat =
		workerpostgres.RuntimeFirstHeartbeatCommandHandler{
			Watchdog: workerapp.RuntimeFirstHeartbeatService{
				Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
				IDs:   ids,
				Now: func() time.Time {
					return now
				},
			},
			Recovery: workerapp.RuntimeRecoveryService{
				Store: workerpostgres.NewRuntimeRecoveryStore(pool),
				IDs:   ids,
				Now: func() time.Time {
					return now
				},
			},
		}
	runner.scheduled.Handler = handler

	before, err := runner.loadStartupRuntimeCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 ||
		!before[0].LivenessAnchor.Equal(resumeAt) {
		t.Fatalf(
			"pre-drain startup candidates=%+v, want the durable Resume anchor",
			before,
		)
	}

	if err := drainStartupDeadlineCommands(
		ctx,
		runner.startupDeadlineScheduledCommandPoller(),
	); err != nil {
		t.Fatal(err)
	}

	var (
		commandStatus, watchdogStatus string
		runtimeStatus, sessionStatus  string
		incidentCount                 int
	)
	if err := pool.QueryRow(ctx, `
SELECT command.status,watchdog.status,runtime.status,session.status,
       (SELECT count(*)
        FROM runtime_recovery_incidents incident
        WHERE incident.failed_runtime_instance_id=runtime.runtime_instance_id)
FROM scheduled_commands command
JOIN runtime_first_heartbeat_watchdogs watchdog
  ON watchdog.runtime_instance_id=command.target_id
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
JOIN worker_sessions session
  ON session.worker_session_id=runtime.worker_session_id
WHERE command.scheduled_command_id=
  'runtime-first-heartbeat-startup-unhealthy'`).Scan(
		&commandStatus,
		&watchdogStatus,
		&runtimeStatus,
		&sessionStatus,
		&incidentCount,
	); err != nil {
		t.Fatal(err)
	}
	if commandStatus != "applied" ||
		watchdogStatus != "unhealthy" ||
		runtimeStatus != "unhealthy" ||
		sessionStatus != "recovering" ||
		incidentCount != 1 {
		t.Fatalf(
			"startup watchdog command=%s watchdog=%s runtime=%s session=%s incidents=%d",
			commandStatus,
			watchdogStatus,
			runtimeStatus,
			sessionStatus,
			incidentCount,
		)
	}

	after, err := runner.loadStartupRuntimeCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf(
			"unhealthy Runtime remained eligible for startup restore: %+v",
			after,
		)
	}
}
