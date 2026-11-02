//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestConcurrentScheduledCommandPollersClaimOneFencedDelivery(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,fencing_token,attempt_count,idempotency_key,created_at,updated_at)
VALUES ('command-once','tenant-local','test_command','TestTarget','target-1',1,'{}'::jsonb,
  $1,'pending',0,0,'test/target-1',$2,$2)`, now.Add(-time.Second), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	handler := &countingScheduledHandler{}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, owner := range []string{"poller-a", "poller-b"} {
		wait.Add(1)
		go func(owner string) {
			defer wait.Done()
			<-start
			_, err := (platformpostgres.ScheduledCommandPoller{
				Pool: pool, Handler: handler, LeaseOwner: owner,
				LeaseDuration: time.Minute, BatchSize: 1, Now: func() time.Time { return now },
			}).RunOnce(ctx)
			results <- err
		}(owner)
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if handler.calls.Load() != 1 {
		t.Fatalf("handler calls=%d, want 1", handler.calls.Load())
	}
	var status string
	var token uint64
	if err := pool.QueryRow(ctx, `SELECT status,fencing_token FROM scheduled_commands
WHERE scheduled_command_id='command-once'`).Scan(&status, &token); err != nil {
		t.Fatal(err)
	}
	if status != "applied" || token != 1 {
		t.Fatalf("status=%s token=%d", status, token)
	}
}

func TestScheduledCommandPollerFiltersTenantAndCommandTypeBeforeClaim(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,fencing_token,attempt_count,
   idempotency_key,created_at,updated_at)
VALUES
  ('deadline-local','tenant-local','agent_turn_deadline','AgentTurn',
   'turn-local',1,'{}'::jsonb,$1,'pending',0,0,'deadline/local',$2,$2),
  ('cleanup-local','tenant-local','runtime_cleanup_reconcile','RuntimeInstance',
   'runtime-local',1,'{}'::jsonb,$1,'pending',0,0,'cleanup/local',$2,$2),
  ('deadline-foreign','tenant-foreign','agent_turn_deadline','AgentTurn',
   'turn-foreign',1,'{}'::jsonb,$1,'pending',0,0,'deadline/foreign',$2,$2)`,
		now.Add(-time.Second),
		now.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	handler := &recordingScheduledHandler{}
	applied, err := (platformpostgres.ScheduledCommandPoller{
		Pool: pool, Handler: handler, LeaseOwner: "deadline-only",
		LeaseDuration: time.Minute, BatchSize: 8, Now: func() time.Time { return now },
		TenantID: "tenant-local", CommandTypes: []string{"agent_turn_deadline"},
	}).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("applied=%d, want 1", applied)
	}
	if got := handler.commandIDs(); len(got) != 1 || got[0] != "deadline-local" {
		t.Fatalf("handled commands=%v", got)
	}
	rows, err := pool.Query(ctx, `
SELECT scheduled_command_id,status,fencing_token
FROM scheduled_commands
ORDER BY scheduled_command_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	statuses := make(map[string]string)
	tokens := make(map[string]uint64)
	for rows.Next() {
		var id, status string
		var token uint64
		if err := rows.Scan(&id, &status, &token); err != nil {
			t.Fatal(err)
		}
		statuses[id] = status
		tokens[id] = token
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if statuses["deadline-local"] != "applied" ||
		tokens["deadline-local"] != 1 ||
		statuses["cleanup-local"] != "pending" ||
		tokens["cleanup-local"] != 0 ||
		statuses["deadline-foreign"] != "pending" ||
		tokens["deadline-foreign"] != 0 {
		t.Fatalf("statuses=%v tokens=%v", statuses, tokens)
	}
}

func TestScheduledCommandPollerCompletesClaimAfterWorkerCancellation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	seedScheduledCommand(t, pool, "cancel-complete", now)

	applied, err := (platformpostgres.ScheduledCommandPoller{
		Pool: pool,
		Handler: cancellingScheduledHandler{
			cancel: cancel,
		},
		LeaseOwner: "cancel-complete-owner", LeaseDuration: time.Minute,
		BatchSize: 1, Now: func() time.Time { return now },
	}).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("applied=%d, want 1", applied)
	}
	assertScheduledCommandLeaseState(
		t,
		pool,
		"cancel-complete",
		"applied",
		"",
		"",
	)
}

func TestScheduledCommandPollerReleasesClaimAfterWorkerCancellation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Date(2026, 7, 27, 10, 30, 0, 0, time.UTC)
	seedScheduledCommand(t, pool, "cancel-release", now)
	handlerErr := errors.New("scheduled handler failed")

	applied, err := (platformpostgres.ScheduledCommandPoller{
		Pool: pool,
		Handler: cancellingScheduledHandler{
			cancel: cancel,
			err:    handlerErr,
		},
		LeaseOwner: "cancel-release-owner", LeaseDuration: time.Minute,
		BatchSize: 1, Now: func() time.Time { return now },
	}).RunOnce(ctx)
	if applied != 0 || err == nil ||
		!strings.Contains(err.Error(), handlerErr.Error()) ||
		strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("applied=%d error=%v", applied, err)
	}
	assertScheduledCommandLeaseState(
		t,
		pool,
		"cancel-release",
		"pending",
		"",
		handlerErr.Error(),
	)
}

func TestScheduledCommandPollerSurfacesFinalizationFenceLossDuringCancellation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Date(2026, 7, 27, 10, 45, 0, 0, time.UTC)
	seedScheduledCommand(t, pool, "cancel-fence-loss", now)

	applied, err := (platformpostgres.ScheduledCommandPoller{
		Pool: pool,
		Handler: stealingScheduledLeaseHandler{
			pool:   pool,
			cancel: cancel,
			now:    now,
		},
		LeaseOwner: "original-owner", LeaseDuration: time.Minute,
		BatchSize: 1, Now: func() time.Time { return now },
	}).RunOnce(ctx)
	if applied != 0 ||
		!errors.Is(
			err,
			platformpostgres.ErrScheduledCommandFinalization,
		) ||
		!errors.Is(err, platformpostgres.ErrScheduledCommandLeaseLost) {
		t.Fatalf("applied=%d error=%v", applied, err)
	}
	assertScheduledCommandLeaseState(
		t,
		pool,
		"cancel-fence-loss",
		"leased",
		"replacement-owner",
		"",
	)
}

func TestScheduledCommandPollerDueOutstandingUsesExactFilters(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,lease_owner,lease_expires_at,
   fencing_token,attempt_count,idempotency_key,created_at,updated_at)
VALUES
  ('deadline-leased','tenant-local','agent_turn_deadline','AgentTurn',
   'turn-local',1,'{}'::jsonb,$1,'leased','foreign-owner',$2,1,1,
   'deadline/leased',$3,$3),
  ('cleanup-pending','tenant-local','runtime_cleanup_reconcile','RuntimeInstance',
   'runtime-local',1,'{}'::jsonb,$1,'pending',NULL,NULL,0,0,
   'cleanup/pending',$3,$3),
  ('deadline-foreign','tenant-foreign','agent_turn_deadline','AgentTurn',
   'turn-foreign',1,'{}'::jsonb,$1,'pending',NULL,NULL,0,0,
   'deadline/foreign',$3,$3)`,
		now.Add(-time.Second),
		now.Add(time.Minute),
		now.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	poller := platformpostgres.ScheduledCommandPoller{
		Pool: pool, Handler: &recordingScheduledHandler{},
		LeaseOwner: "deadline-only", LeaseDuration: time.Minute,
		BatchSize: 8, Now: func() time.Time { return now },
		TenantID: "tenant-local", CommandTypes: []string{"agent_turn_deadline"},
	}
	outstanding, err := poller.HasDueOutstanding(ctx)
	if err != nil || !outstanding {
		t.Fatalf("outstanding=%t error=%v", outstanding, err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE scheduled_commands
SET status='applied',lease_owner=NULL,lease_expires_at=NULL
WHERE scheduled_command_id='deadline-leased'`); err != nil {
		t.Fatal(err)
	}
	outstanding, err = poller.HasDueOutstanding(ctx)
	if err != nil || outstanding {
		t.Fatalf("filtered outstanding=%t error=%v", outstanding, err)
	}
}

type countingScheduledHandler struct {
	calls atomic.Uint64
}

func (handler *countingScheduledHandler) HandleScheduledCommand(
	context.Context,
	platformpostgres.ScheduledCommand,
) error {
	handler.calls.Add(1)
	return nil
}

type recordingScheduledHandler struct {
	mu       sync.Mutex
	commands []string
}

func (handler *recordingScheduledHandler) HandleScheduledCommand(
	_ context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.commands = append(handler.commands, command.ID)
	return nil
}

func (handler *recordingScheduledHandler) commandIDs() []string {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]string(nil), handler.commands...)
}

type cancellingScheduledHandler struct {
	cancel context.CancelFunc
	err    error
}

func (handler cancellingScheduledHandler) HandleScheduledCommand(
	context.Context,
	platformpostgres.ScheduledCommand,
) error {
	handler.cancel()
	return handler.err
}

type stealingScheduledLeaseHandler struct {
	pool   *pgxpool.Pool
	cancel context.CancelFunc
	now    time.Time
}

func (handler stealingScheduledLeaseHandler) HandleScheduledCommand(
	_ context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if _, err := handler.pool.Exec(context.Background(), `
UPDATE scheduled_commands
SET lease_owner='replacement-owner',
    lease_expires_at=$2,
    fencing_token=fencing_token+1,
    updated_at=$3
WHERE scheduled_command_id=$1
  AND status='leased'`,
		command.ID,
		handler.now.Add(time.Minute),
		handler.now,
	); err != nil {
		return err
	}
	handler.cancel()
	return context.Canceled
}

func seedScheduledCommand(
	t *testing.T,
	pool *pgxpool.Pool,
	id string,
	now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,fencing_token,attempt_count,
   idempotency_key,created_at,updated_at)
VALUES ($1,'tenant-local','test_command','TestTarget','target-1',1,
  '{}'::jsonb,$2,'pending',0,0,$3,$4,$4)`,
		id,
		now.Add(-time.Second),
		"test/"+id,
		now.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
}

func assertScheduledCommandLeaseState(
	t *testing.T,
	pool *pgxpool.Pool,
	id string,
	wantStatus string,
	wantOwner string,
	wantError string,
) {
	t.Helper()
	var status string
	var owner, lastError *string
	if err := pool.QueryRow(context.Background(), `
SELECT status,lease_owner,last_error_code
FROM scheduled_commands
WHERE scheduled_command_id=$1`,
		id,
	).Scan(&status, &owner, &lastError); err != nil {
		t.Fatal(err)
	}
	gotOwner := ""
	if owner != nil {
		gotOwner = *owner
	}
	gotError := ""
	if lastError != nil {
		gotError = *lastError
	}
	if status != wantStatus || gotOwner != wantOwner ||
		gotError != wantError {
		t.Fatalf(
			"status=%s owner=%q error=%q, want %s/%q/%q",
			status,
			gotOwner,
			gotError,
			wantStatus,
			wantOwner,
			wantError,
		)
	}
}
