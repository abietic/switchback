//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestCreateTaskCommitsIntentTaskAllocationOutboxAndIdempotency(t *testing.T) {
	pool := postgrestest.Open(t)
	store := taskpostgres.NewStore(pool)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	admission := admittedTask(t, "assessment-create-1", "intent-create-1", "Repair counter", now)
	if err := store.RecordDirectAdmission(ctx, admission); err != nil {
		t.Fatalf("record admission: %v", err)
	}
	service := application.CreateTaskService{
		Store: store,
		IDs:   &sequenceIDs{values: []string{"task-1", "transition-1", "allocation-1", "event-1"}},
		Now:   func() time.Time { return now.Add(time.Second) },
	}

	created, err := service.Execute(ctx, application.CreateTaskCommand{
		Actor:    application.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskSpec: admission.TaskSpec, AcceptanceMode: "manual", IdempotencyKey: "create-command-1",
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	if created.TaskID != "task-1" || created.Revision != 1 || created.SessionAllocationSequence != 1 {
		t.Fatalf("unexpected CreateTask result: %+v", created)
	}
	assertCount(t, pool, "tasks", 1)
	assertCount(t, pool, "task_transitions", 1)
	assertCount(t, pool, "session_allocation_requests", 1)
	assertCount(t, pool, "outbox_events", 1)
	assertCount(t, pool, "idempotency_results", 1)
	var clientCommandID, eventIdempotencyKey, serverCommandID string
	if err := pool.QueryRow(ctx, `
SELECT client_command_id,idempotency_key,correlation_id
FROM outbox_events WHERE event_id='event-1'`).Scan(
		&clientCommandID, &eventIdempotencyKey, &serverCommandID,
	); err != nil {
		t.Fatal(err)
	}
	if clientCommandID != "create-command-1" ||
		eventIdempotencyKey != "task-created/task-1" ||
		serverCommandID != "event-1" {
		t.Fatalf("Task outbox identities client=%q idempotency=%q command=%q",
			clientCommandID, eventIdempotencyKey, serverCommandID)
	}
	var intentStatus, consumedBy string
	if err := pool.QueryRow(ctx, "SELECT status, consumed_by_task_id FROM task_creation_intents WHERE intent_id=$1", "intent-create-1").Scan(&intentStatus, &consumedBy); err != nil {
		t.Fatal(err)
	}
	if intentStatus != "consumed" || consumedBy != "task-1" {
		t.Fatalf("intent status=%s consumed_by=%s", intentStatus, consumedBy)
	}
}

func TestCreateTaskRollsBackEveryWriteWhenTaskInsertFails(t *testing.T) {
	pool := postgrestest.Open(t)
	store := taskpostgres.NewStore(pool)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	first := admittedTask(t, "assessment-first", "intent-first", "First task", now)
	second := admittedTask(t, "assessment-second", "intent-second", "Second task", now.Add(time.Second))
	if err := store.RecordDirectAdmission(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDirectAdmission(ctx, second); err != nil {
		t.Fatal(err)
	}
	firstService := application.CreateTaskService{
		Store: store, IDs: &sequenceIDs{values: []string{"duplicate-task", "transition-first", "allocation-first", "event-first"}},
		Now: func() time.Time { return now.Add(2 * time.Second) },
	}
	if _, err := firstService.Execute(ctx, application.CreateTaskCommand{
		Actor: application.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskSpec: first.TaskSpec,
		AcceptanceMode: "manual", IdempotencyKey: "create-first",
	}); err != nil {
		t.Fatal(err)
	}
	failingService := application.CreateTaskService{
		Store: store, IDs: &sequenceIDs{values: []string{"duplicate-task", "transition-second", "allocation-second", "event-second"}},
		Now: func() time.Time { return now.Add(3 * time.Second) },
	}
	if _, err := failingService.Execute(ctx, application.CreateTaskCommand{
		Actor: application.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskSpec: second.TaskSpec,
		AcceptanceMode: "manual", IdempotencyKey: "create-second",
	}); err == nil {
		t.Fatal("duplicate Task identity unexpectedly committed")
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM task_creation_intents WHERE intent_id='intent-second'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("failed transaction consumed intent: %s", status)
	}
	var nextSequence uint64
	if err := pool.QueryRow(ctx, "SELECT next_sequence FROM session_allocation_queue_positions WHERE tenant_id='tenant-local' AND user_id='user-local'").Scan(&nextSequence); err != nil {
		t.Fatal(err)
	}
	if nextSequence != 2 {
		t.Fatalf("failed transaction consumed allocation sequence: next=%d", nextSequence)
	}
	assertCount(t, pool, "idempotency_results", 1)
}

func TestCreateTaskResponseLossRetryReturnsOriginalResultWithoutNewWrites(t *testing.T) {
	pool := postgrestest.Open(t)
	store := taskpostgres.NewStore(pool)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 2, 30, 0, 0, time.UTC)
	admission := admittedTask(t, "assessment-retry", "intent-retry", "Retry-safe task", now)
	if err := store.RecordDirectAdmission(ctx, admission); err != nil {
		t.Fatal(err)
	}
	command := application.CreateTaskCommand{
		Actor:    application.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskSpec: admission.TaskSpec, AcceptanceMode: "manual", IdempotencyKey: "create-retry",
	}
	first, err := (application.CreateTaskService{
		Store: store, IDs: &sequenceIDs{values: []string{"task-retry", "transition-retry", "allocation-retry", "event-retry"}},
		Now: func() time.Time { return now.Add(time.Second) },
	}).Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (application.CreateTaskService{
		Store: store, IDs: &sequenceIDs{values: []string{"task-unused", "transition-unused", "allocation-unused", "event-unused"}},
		Now: func() time.Time { return now.Add(2 * time.Second) },
	}).Execute(ctx, command)
	if err != nil {
		t.Fatalf("retry CreateTask: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("retry result=%+v, want original=%+v", second, first)
	}
	for _, table := range []string{"tasks", "task_transitions", "session_allocation_requests", "outbox_events", "idempotency_results"} {
		assertCount(t, pool, table, 1)
	}
	var nextSequence uint64
	if err := pool.QueryRow(ctx, `SELECT next_sequence FROM session_allocation_queue_positions
WHERE tenant_id='tenant-local' AND user_id='user-local'`).Scan(&nextSequence); err != nil {
		t.Fatal(err)
	}
	if nextSequence != 2 {
		t.Fatalf("response-loss retry consumed sequence: next=%d", nextSequence)
	}
}

func TestCreateTaskRejectsStaleIntentWithoutPartialWrites(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(context.Context, *pgxpool.Pool, string, time.Time) error
	}{
		{
			name: "expired",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, intentID string, now time.Time) error {
				_, err := pool.Exec(ctx, "UPDATE task_creation_intents SET expires_at=$2 WHERE intent_id=$1", intentID, now.Add(500*time.Millisecond))
				return err
			},
		},
		{
			name: "revoked",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, intentID string, _ time.Time) error {
				_, err := pool.Exec(ctx, "UPDATE task_creation_intents SET status='revoked' WHERE intent_id=$1", intentID)
				return err
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			store := taskpostgres.NewStore(pool)
			ctx := context.Background()
			now := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)
			intentID := "intent-stale-" + testCase.name
			admission := admittedTask(t, "assessment-stale-"+testCase.name, intentID, "Stale intent", now)
			if err := store.RecordDirectAdmission(ctx, admission); err != nil {
				t.Fatal(err)
			}
			if err := testCase.mutate(ctx, pool, intentID, now); err != nil {
				t.Fatal(err)
			}
			_, err := (application.CreateTaskService{
				Store: store, IDs: &sequenceIDs{values: []string{"task-stale", "transition-stale", "allocation-stale", "event-stale"}},
				Now: func() time.Time { return now.Add(time.Second) },
			}).Execute(ctx, application.CreateTaskCommand{
				Actor:    application.Actor{TenantID: "tenant-local", UserID: "user-local"},
				TaskSpec: admission.TaskSpec, AcceptanceMode: "manual", IdempotencyKey: "create-stale-" + testCase.name,
			})
			if !errors.Is(err, application.ErrCreationIntentInvalid) {
				t.Fatalf("error=%v, want ErrCreationIntentInvalid", err)
			}
			for _, table := range []string{"tasks", "task_transitions", "session_allocation_requests", "outbox_events", "idempotency_results", "session_allocation_queue_positions"} {
				assertCount(t, pool, table, 0)
			}
		})
	}
}

func TestCreateTaskIdempotencyContentConflictPreservesOriginal(t *testing.T) {
	pool := postgrestest.Open(t)
	store := taskpostgres.NewStore(pool)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 3, 30, 0, 0, time.UTC)
	firstAdmission := admittedTask(t, "assessment-conflict-1", "intent-conflict-1", "Original task", now)
	secondAdmission := admittedTask(t, "assessment-conflict-2", "intent-conflict-2", "Different task", now.Add(time.Second))
	if err := store.RecordDirectAdmission(ctx, firstAdmission); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDirectAdmission(ctx, secondAdmission); err != nil {
		t.Fatal(err)
	}
	first, err := (application.CreateTaskService{
		Store: store, IDs: &sequenceIDs{values: []string{"task-conflict-1", "transition-conflict-1", "allocation-conflict-1", "event-conflict-1"}},
		Now: func() time.Time { return now.Add(2 * time.Second) },
	}).Execute(ctx, application.CreateTaskCommand{
		Actor:    application.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskSpec: firstAdmission.TaskSpec, AcceptanceMode: "manual", IdempotencyKey: "create-conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (application.CreateTaskService{
		Store: store, IDs: &sequenceIDs{values: []string{"task-conflict-2", "transition-conflict-2", "allocation-conflict-2", "event-conflict-2"}},
		Now: func() time.Time { return now.Add(3 * time.Second) },
	}).Execute(ctx, application.CreateTaskCommand{
		Actor:    application.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskSpec: secondAdmission.TaskSpec, AcceptanceMode: "manual", IdempotencyKey: "create-conflict",
	})
	if !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatalf("error=%v, want ErrIdempotencyConflict", err)
	}
	if first.TaskID != "task-conflict-1" {
		t.Fatalf("original result changed: %+v", first)
	}
	for _, table := range []string{"tasks", "task_transitions", "session_allocation_requests", "outbox_events", "idempotency_results"} {
		assertCount(t, pool, table, 1)
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM task_creation_intents WHERE intent_id='intent-conflict-2'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("conflicting retry consumed second intent: %s", status)
	}
}

func admittedTask(t *testing.T, assessmentID, intentID, title string, now time.Time) application.DirectAdmissionResult {
	t.Helper()
	networkProfile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	service := application.DirectAdmission{
		IDs: &sequenceIDs{values: []string{assessmentID, intentID}},
		Now: func() time.Time { return now },
		Policy: application.DirectAdmissionPolicy{
			ReadinessPolicyRevision: "direct-ready-v1", TriggerPolicyRevision: "direct-create-v1",
			NetworkProfile: networkProfile,
		},
	}
	command := directAdmissionCommandForStore()
	command.Request.Title = title
	command.Request.Goal = title
	result, err := service.Admit(command)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func directAdmissionCommandForStore() application.DirectAdmissionCommand {
	return application.DirectAdmissionCommand{
		Actor: application.Actor{TenantID: "tenant-local", UserID: "user-local"},
		Workspace: application.WorkspaceRevisionEvidence{
			TenantID: "tenant-local", WorkspaceID: "workspace-demo", WorkspaceRevisionID: "workspace-revision-1",
			ResolutionID: "resolution-1", ResolutionRevision: 1, MembershipRevision: 1, Readable: true,
		},
		AgentProfile: application.AgentProfileEvidence{ProfileID: "deepseek-codex-openai-v1", Revision: "1", Enabled: true},
		Request: application.DirectTaskRequest{
			ProjectID: "project-switchback", Title: "Repair counter", Goal: "Repair counter",
			TargetKind: "workspace", TargetRef: "workspace-revision-1", Constraints: []string{},
			AcceptanceCriteria: []string{"Tests pass"}, AcceptanceMode: "manual",
			UserVisibleTypes: []string{"report", "patch"},
		},
	}
}

type sequenceIDs struct {
	values []string
	index  int
}

func (ids *sequenceIDs) NewID(string) (string, error) {
	value := ids.values[ids.index]
	ids.index++
	return value, nil
}

func assertCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count=%d want=%d", table, got, want)
	}
}
