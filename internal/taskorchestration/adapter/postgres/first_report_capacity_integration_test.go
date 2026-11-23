//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestFirstReportCapacityMigrationPreservesExistingQueuedTask(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 26)
	seedTask(t, pool, "existing-queued-task")
	ctx := context.Background()
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "000027_first_report_capacity_reservation.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, _, found := strings.Cut(string(source), "-- migrate:down")
	if !found {
		t.Fatal("missing migration down boundary")
	}
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatalf("apply first-report capacity migration over schema 26: %v", err)
	}
	var status string
	var reservations int
	err = pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM first_report_capacity_reservations)
FROM tasks WHERE task_id='existing-queued-task'`).Scan(&status, &reservations)
	if err != nil || status != "queued" || reservations != 0 {
		t.Fatalf("migration changed existing Task or admitted capacity: status=%q reservations=%d err=%v", status, reservations, err)
	}
}

func TestFirstReportCapacityCommitsAtMostOnePermitAndRollsBackTheLoser(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seedTask(t, pool, "first-report-a")
	_, err := pool.Exec(ctx, `
INSERT INTO tasks (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,
  task_spec_snapshot,task_spec_digest,creation_intent_id,status,acceptance_mode,revision,created_at,updated_at)
SELECT 'first-report-b',tenant_id,initiator_user_id,workspace_id,workspace_revision_id,
  task_spec_snapshot,task_spec_digest,creation_intent_id,status,acceptance_mode,revision,created_at,updated_at
FROM tasks WHERE task_id='first-report-a'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE tasks
SET task_spec_snapshot=jsonb_build_object('agent_config',jsonb_build_object('profile_id',$1::text)),
    worker_session_id=task_id,session_queue_sequence=1
WHERE task_id IN ('first-report-a','first-report-b')`, config.DeepSeekFirstReportV1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO worker_sessions (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,
  working_copy_id,status,session_revision,runtime_generation,quota_reservation_revision,
  idle_expires_at,absolute_expires_at,created_at,updated_at)
SELECT task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,
  'copy-' || task_id,'allocating',1,0,1,now()+interval '48 hours',now()+interval '14 days',now(),now()
FROM tasks;
INSERT INTO session_task_queue_entries (queue_entry_id,worker_session_id,task_id,session_queue_sequence,
  queue_revision,expected_session_revision,status,idempotency_key,enqueued_at)
SELECT 'queue-' || task_id,task_id,task_id,1,1,1,'pending','enqueue-' || task_id,now() FROM tasks`)
	if err != nil {
		t.Fatal(err)
	}
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	store := taskpostgres.NewStore(pool)
	newRecord := func(taskID string) taskapp.IssueTaskPermitRecord {
		return taskapp.IssueTaskPermitRecord{
			PermitID: "permit-" + taskID, Now: now, ExpiresAt: now.Add(time.Minute),
			Command: taskapp.IssueTaskPermitCommand{
				TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: taskID,
				RuntimeGeneration: 1, IdempotencyKey: "dispatch-" + taskID,
			},
		}
	}
	legacy := newRecord("first-report-a")
	legacy.Command.CapacityAdmissionRef = "local-standard-admission"
	if _, err := store.IssueTaskPermit(ctx, legacy); !errors.Is(err, workerapp.ErrFirstReportCapacityUnavailable) {
		t.Fatalf("unattested first-report permit was not rejected: %v", err)
	}
	evidence := workerapp.FirstReportCapacityEvidence{
		EngineID: "isolated-engine-1", ObservedAt: now,
		AvailableNanoCPUs: 4_000_000_000, AvailableMemoryBytes: 8 << 30, AvailableStorageBytes: 16 << 30,
	}
	if livePath := os.Getenv("SWITCHBACK_TEST_FIRST_REPORT_CAPACITY_SAMPLE"); livePath != "" {
		body, err := os.ReadFile(livePath)
		if err != nil {
			t.Fatalf("read live Engine sample: %v", err)
		}
		evidence = workerapp.FirstReportCapacityEvidence{}
		if err := json.Unmarshal(body, &evidence); err != nil {
			t.Fatalf("parse live Engine sample: %v", err)
		}
		t.Log("using the real sampler output without changing its timestamp")
	}
	stale := evidence
	stale.ObservedAt = now.Add(-31 * time.Second)
	rejected := newRecord("first-report-a")
	rejected.Command.FirstReportCapacity = &stale
	if _, err := store.IssueTaskPermit(ctx, rejected); !errors.Is(err, workerapp.ErrFirstReportCapacityUnavailable) {
		t.Fatalf("expired capacity evidence was not rejected: %v", err)
	}
	assertDispatchState := func(wantPermits, wantReservations, wantUnchanged int) {
		t.Helper()
		var permits, reservations, allocating, pending int
		err := pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM session_dispatch_permits),
  (SELECT count(*) FROM first_report_capacity_reservations),
  (SELECT count(*) FROM worker_sessions WHERE status='allocating' AND runtime_generation=0 AND session_revision=1),
  (SELECT count(*) FROM session_task_queue_entries WHERE status='pending')`).Scan(
			&permits, &reservations, &allocating, &pending)
		if err != nil || permits != wantPermits || reservations != wantReservations || allocating != wantUnchanged || pending != wantUnchanged {
			t.Fatalf("unexpected dispatch state: permits=%d reservations=%d allocating=%d pending=%d err=%v", permits, reservations, allocating, pending, err)
		}
	}
	assertDispatchState(0, 0, 2)

	const forceQueueFailure = "first_report_reject_permit_issued"
	if _, err := pool.Exec(ctx, `ALTER TABLE session_task_queue_entries ADD CONSTRAINT
first_report_reject_permit_issued CHECK (status <> 'permit_issued')`); err != nil {
		t.Fatal(err)
	}
	rejected.Command.FirstReportCapacity = &evidence
	_, err = store.IssueTaskPermit(ctx, rejected)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != forceQueueFailure {
		t.Fatalf("expected the queue update to fail after inserting the permit and reservation: %v", err)
	}
	assertDispatchState(0, 0, 2)
	t.Log("verified rollback after permit and reservation inserts")
	if _, err := pool.Exec(ctx, `ALTER TABLE session_task_queue_entries DROP CONSTRAINT first_report_reject_permit_issued`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, taskID := range []string{"first-report-a", "first-report-b"} {
		go func() {
			record := newRecord(taskID)
			record.Command.FirstReportCapacity = &evidence
			_, err := store.IssueTaskPermit(ctx, record)
			results <- err
		}()
	}
	committed := 0
	for range 2 {
		err := <-results
		if err == nil {
			committed++
		} else if !errors.Is(err, workerapp.ErrFirstReportCapacityUnavailable) {
			t.Fatal(err)
		}
	}
	if committed != 1 {
		t.Fatalf("competing Tasks committed %d permits", committed)
	}
	assertDispatchState(1, 1, 1)
	var matches int
	err = pool.QueryRow(ctx, `SELECT count(*)
FROM first_report_capacity_reservations r
JOIN session_dispatch_permits p ON p.permit_id=r.permit_id AND p.target_id=r.task_id
WHERE r.engine_id=$1 AND r.available_nano_cpus=$2 AND r.available_memory_bytes=$3
  AND r.available_storage_bytes=$4 AND r.observed_at=$5`, evidence.EngineID, evidence.AvailableNanoCPUs,
		evidence.AvailableMemoryBytes, evidence.AvailableStorageBytes, evidence.ObservedAt).Scan(&matches)
	if err != nil || matches != 1 {
		t.Fatalf("permit did not bind the exact committed capacity sample: matches=%d err=%v", matches, err)
	}
}
