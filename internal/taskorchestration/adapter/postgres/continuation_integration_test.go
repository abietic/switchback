//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestContinueTaskWithWorkerCommitsExactContinuationAtomically(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	seedContinuationTask(t, ctx, pool, now)
	service := taskapp.TaskActionService{
		Store: taskpostgres.NewStore(pool), IDs: &actionIDs{},
		Now: func() time.Time { return now.Add(time.Minute) },
	}
	command := exactContinuationCommand()

	continued, err := service.ContinueTaskWithWorker(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if continued.Status != "running" || continued.Revision != 4 ||
		continued.SessionRevision != 5 ||
		continued.InteractionKind != taskapp.TaskContinuationChat {
		t.Fatalf("continued = %+v", continued)
	}
	replayed, err := service.ContinueTaskWithWorker(ctx, command)
	if err != nil || replayed != continued {
		t.Fatalf("idempotent replay = %+v error=%v", replayed, err)
	}

	assertContinuationState(t, ctx, pool, continued)
	conflict := command
	conflict.Message = "A semantically different continuation."
	if _, err := service.ContinueTaskWithWorker(ctx, conflict); !errors.Is(err, taskapp.ErrIdempotencyConflict) {
		t.Fatalf("same key with different content error = %v", err)
	}
}

func TestContinueTaskWithWorkerRejectsUnauthorizedAndStaleTargetsWithoutPartialWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*taskapp.ContinueTaskWithWorkerCommand)
		want   error
	}{
		{
			name: "wrong initiator",
			mutate: func(command *taskapp.ContinueTaskWithWorkerCommand) {
				command.Actor.UserID = "user-other"
			},
			want: platformpostgres.ErrNotFound,
		},
		{
			name: "stale task",
			mutate: func(command *taskapp.ContinueTaskWithWorkerCommand) {
				command.ExpectedTaskRevision--
			},
			want: platformpostgres.ErrRevisionConflict,
		},
		{
			name: "stale session",
			mutate: func(command *taskapp.ContinueTaskWithWorkerCommand) {
				command.ExpectedSessionRevision--
			},
			want: platformpostgres.ErrRevisionConflict,
		},
		{
			name: "stale run",
			mutate: func(command *taskapp.ContinueTaskWithWorkerCommand) {
				command.ExpectedRunRevision--
			},
			want: platformpostgres.ErrRevisionConflict,
		},
		{
			name: "stale turn",
			mutate: func(command *taskapp.ContinueTaskWithWorkerCommand) {
				command.ExpectedTurnRevision--
			},
			want: platformpostgres.ErrRevisionConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
			seedContinuationTask(t, ctx, pool, now)
			service := taskapp.TaskActionService{
				Store: taskpostgres.NewStore(pool), IDs: &actionIDs{},
				Now: func() time.Time { return now.Add(time.Minute) },
			}
			command := exactContinuationCommand()
			test.mutate(&command)
			if _, err := service.ContinueTaskWithWorker(ctx, command); !errors.Is(err, test.want) {
				t.Fatalf("continuation error = %v, want %v", err, test.want)
			}
			assertContinuationWriteCount(t, ctx, pool, 0)
		})
	}
}

func TestConcurrentTaskContinuationsProduceOneNewRun(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	seedContinuationTask(t, ctx, pool, now)
	service := taskapp.TaskActionService{
		Store: taskpostgres.NewStore(pool), IDs: &actionIDs{},
		Now: func() time.Time { return now.Add(time.Minute) },
	}

	var wait sync.WaitGroup
	errorsByCommand := make(chan error, 2)
	for _, key := range []string{"continue-concurrent-a", "continue-concurrent-b"} {
		wait.Add(1)
		go func(idempotencyKey string) {
			defer wait.Done()
			command := exactContinuationCommand()
			command.IdempotencyKey = idempotencyKey
			_, err := service.ContinueTaskWithWorker(ctx, command)
			errorsByCommand <- err
		}(key)
	}
	wait.Wait()
	close(errorsByCommand)

	var success, stale int
	for err := range errorsByCommand {
		switch {
		case err == nil:
			success++
		case errors.Is(err, platformpostgres.ErrRevisionConflict):
			stale++
		default:
			t.Fatalf("unexpected concurrent continuation error: %v", err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("concurrent results success=%d stale=%d", success, stale)
	}
	assertContinuationWriteCount(t, ctx, pool, 1)
}

func TestSameTaskSecondStageDeliveryUsesDistinctOutboxIdempotency(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	seedContinuationTask(t, ctx, pool, now)
	seedPriorDeliveryEvent(t, ctx, pool, now)
	continued, err := (taskapp.TaskActionService{
		Store: taskpostgres.NewStore(pool), IDs: &actionIDs{},
		Now: func() time.Time { return now.Add(time.Minute) },
	}).ContinueTaskWithWorker(ctx, exactContinuationCommand())
	if err != nil {
		t.Fatal(err)
	}

	delivered, err := (taskapp.TaskDeliveryService{
		Store: taskpostgres.NewDeliveryStore(pool), IDs: &actionIDs{},
		Now: func() time.Time { return now.Add(2 * time.Minute) },
	}).RecordStageResult(ctx, taskapp.RecordStageResultCommand{
		Actor:  taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskID: "task-1", WorkerSessionID: "session-1", WorkerRunID: continued.WorkerRunID,
		ExpectedTaskRevision: continued.Revision, ArtifactRefs: continuationArtifacts(now, continued),
		IdempotencyKey: "deliver-turn/" + continued.AgentTurnID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Status != "delivered" || delivered.Revision != continued.Revision+1 {
		t.Fatalf("delivered=%+v", delivered)
	}
	var keys []string
	rows, err := pool.Query(ctx, `
SELECT idempotency_key FROM outbox_events
WHERE producer_context='task-orchestration' AND event_type='TaskStageDelivered.v1'
ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"TaskStageDelivered.v1/task-1/revision-3",
		"TaskStageDelivered.v1/task-1/revision-5",
	}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("delivery keys=%v want=%v", keys, want)
	}
}

func exactContinuationCommand() taskapp.ContinueTaskWithWorkerCommand {
	return taskapp.ContinueTaskWithWorkerCommand{
		Actor:  taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskID: "task-1", ExpectedTaskRevision: 3,
		WorkerSessionID: "session-1", ExpectedSessionRevision: 4,
		PreviousWorkerRunID: "run-1", ExpectedRunRevision: 3,
		PreviousAgentTurnID: "turn-1", ExpectedTurnRevision: 2,
		InteractionKind: taskapp.TaskContinuationChat,
		Message:         "Continue with the missing edge-case test.",
		IdempotencyKey:  "continue-task-1",
	}
}

func assertContinuationState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	continued taskapp.ContinuedTask,
) {
	t.Helper()
	var taskStatus, currentRunID string
	var taskRevision uint64
	if err := pool.QueryRow(ctx, `SELECT status,current_worker_run_id,revision FROM tasks
WHERE task_id='task-1'`).Scan(&taskStatus, &currentRunID, &taskRevision); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "running" || taskRevision != 4 || currentRunID != continued.WorkerRunID {
		t.Fatalf("Task status=%s revision=%d run=%s", taskStatus, taskRevision, currentRunID)
	}
	var sessionStatus, activeRunID string
	var sessionRevision uint64
	if err := pool.QueryRow(ctx, `SELECT status,active_worker_run_id,session_revision
FROM worker_sessions WHERE worker_session_id='session-1'`).Scan(
		&sessionStatus, &activeRunID, &sessionRevision,
	); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "running" || sessionRevision != 5 || activeRunID != continued.WorkerRunID {
		t.Fatalf("Session status=%s revision=%d run=%s", sessionStatus, sessionRevision, activeRunID)
	}
	assertContinuationWriteCount(t, ctx, pool, 1)
}

func assertContinuationWriteCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	want int,
) {
	t.Helper()
	for _, query := range []string{
		`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id='run-1'`,
		`SELECT count(*) FROM agent_turns WHERE idempotency_key LIKE 'continue-%'`,
		`SELECT count(*) FROM task_transitions WHERE trigger IN ('initiator_chat','initiator_rework')`,
		`SELECT count(*) FROM outbox_events WHERE event_type IN ('TaskContinuationStarted.v1','TaskReworkStarted.v1')`,
		`SELECT count(*) FROM idempotency_results WHERE operation='continue_task_with_worker'`,
	} {
		var count int
		if err := pool.QueryRow(ctx, query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%q count=%d want=%d", query, count, want)
		}
	}
}

func seedPriorDeliveryEvent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO owner_stream_positions
 (stream_id,owner_context,aggregate_type,aggregate_id,next_sequence,revision,updated_at)
VALUES ('task/task-1','task-orchestration','Task','task-1',2,1,$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO outbox_events
 (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,
  aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,
  visibility,payload,occurred_at)
VALUES ('event-prior-delivery','event.v1','TaskStageDelivered.v1','task-orchestration',
  'task/task-1',1,'tenant-local','Task','task-1','task/task-1','event-prior-delivery',
  'task-1','TaskStageDelivered.v1/task-1/revision-3','internal',
  '{"task_id":"task-1","task_revision":3}',$1)`, now); err != nil {
		t.Fatal(err)
	}
}

func continuationArtifacts(now time.Time, continued taskapp.ContinuedTask) []contractsv1.ArtifactRef {
	result := make([]contractsv1.ArtifactRef, 0, 2)
	for index, kind := range []string{"report", "patch"} {
		result = append(result, contractsv1.ArtifactRef{
			SchemaVersion: contractsv1.ArtifactRefSchemaVersion,
			ArtifactID:    fmt.Sprintf("artifact-%s-%d", kind, index),
			TenantID:      "tenant-local", UserID: "user-local", WorkspaceID: "workspace-local",
			TaskID: "task-1", WorkerSessionID: "session-1",
			WorkerRunID:       contractsv1.WorkerRunID(continued.WorkerRunID),
			AgentTurnID:       contractsv1.AgentTurnID(continued.AgentTurnID),
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1, Type: kind,
			URI: "object://artifact/" + kind, SHA256: strings.Repeat("a", 64),
			Permission: contractsv1.ArtifactPermission{
				Visibility: "private", PolicyRevision: "policy-1",
			},
			RetentionPolicyRef: "retain-local", StorageTier: "hot",
			Availability: "online", Completeness: "complete",
			SourceCheckpointRef: contractsv1.CheckpointRef{
				CheckpointID: "checkpoint-" + kind, Digest: "sha256:" + strings.Repeat("b", 64),
			},
			AvailableActions: []string{"download"}, CreatedAt: now,
		})
	}
	return result
}

func seedContinuationTask(
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
 'switchback-worker@sha256:%s','local_standard_v1','m1_public_web_restricted_v1',1,2,$2,$2)`,
			strings.Repeat("b", 64)),
	}
	for index, statement := range statements {
		if _, err := pool.Exec(ctx, statement, digest, now); err != nil {
			t.Fatalf("seed statement %d: %v", index+1, err)
		}
	}
}

func TestInitialExecutionTaskContinuationHardDeniesBeforeNewRun(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedContinuationTask(t, ctx, pool, now)
	service := taskapp.TaskActionService{Store: taskpostgres.NewStore(pool), IDs: &actionIDs{}, Now: func() time.Time { return now.Add(time.Minute) }}
	_, err := service.ContinueTaskWithWorker(ctx, exactContinuationCommand())
	if !errors.Is(err, workerapp.CapacityPathNotIntegrated) {
		t.Fatalf("unsupported Task continuation: %v", err)
	}
	assertContinuationWriteCount(t, ctx, pool, 0)
}

func TestInitialExecutionTaskReworkHardDeniesBeforeNewRun(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedContinuationTask(t, ctx, pool, now)
	c := exactContinuationCommand()
	svc := taskapp.TaskActionService{Store: taskpostgres.NewStore(pool), IDs: &actionIDs{}, Now: func() time.Time { return now.Add(time.Minute) }}
	_, err := svc.Rework(ctx, taskapp.ReworkTaskCommand{Actor: c.Actor, TaskID: c.TaskID, ExpectedTaskRevision: c.ExpectedTaskRevision, Message: c.Message, IdempotencyKey: c.IdempotencyKey})
	if !errors.Is(err, workerapp.CapacityPathNotIntegrated) {
		t.Fatalf("unsupported Task rework: %v", err)
	}
	assertContinuationWriteCount(t, ctx, pool, 0)
}
