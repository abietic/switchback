//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskdomain "github.com/abietic/switchback/internal/taskorchestration/domain"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestTaskRepositoryExpectedRevisionCAS(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	seedTask(t, pool, "task-cas")
	repository := taskpostgres.Repository{}

	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- platformpostgres.InTransaction(ctx, pool, func(tx platformpostgres.Tx) error {
				_, err := repository.AdvanceStatus(ctx, tx, "task-cas", 1, taskdomain.TaskQueued, taskdomain.TaskRunning, time.Now().UTC())
				return err
			})
		}()
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, platformpostgres.ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected CAS result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	var status string
	var revision uint64
	if err := pool.QueryRow(ctx, "SELECT status, revision FROM tasks WHERE task_id=$1", "task-cas").Scan(&status, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "running" || revision != 2 {
		t.Fatalf("status=%s revision=%d", status, revision)
	}
}

func TestOwnerStreamSequenceIsTransactionalAndContiguous(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	repository := platformpostgres.OwnerStreamRepository{}
	owner := platformpostgres.OwnerStream{
		StreamID: "task/task-stream", OwnerContext: "task-orchestration",
		AggregateType: "Task", AggregateID: "task-stream",
	}

	err := platformpostgres.InTransaction(ctx, pool, func(tx platformpostgres.Tx) error {
		sequence, err := repository.Next(ctx, tx, owner)
		if err != nil {
			return err
		}
		if sequence != 1 {
			t.Fatalf("rolled-back sequence=%d, want 1", sequence)
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("rollback transaction unexpectedly committed")
	}

	const allocations = 8
	sequences := make(chan uint64, allocations)
	errorsFound := make(chan error, allocations)
	var wait sync.WaitGroup
	for range allocations {
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := platformpostgres.InTransaction(ctx, pool, func(tx platformpostgres.Tx) error {
				sequence, err := repository.Next(ctx, tx, owner)
				if err == nil {
					sequences <- sequence
				}
				return err
			})
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(sequences)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("allocate sequence: %v", err)
		}
	}
	values := make([]int, 0, allocations)
	for sequence := range sequences {
		values = append(values, int(sequence))
	}
	sort.Ints(values)
	for index, value := range values {
		if value != index+1 {
			t.Fatalf("sequences=%v", values)
		}
	}
}

func seedTask(t *testing.T, pool *pgxpool.Pool, taskID string) {
	t.Helper()
	now := time.Now().UTC()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
INSERT INTO task_readiness_assessments (
  assessment_id, tenant_id, actor_user_id, workspace_id, workspace_revision_id,
  source, policy_revision, dimensions, blocking_unknowns, assumptions, status,
  content_digest, evaluated_at
) VALUES ('assessment-1', 'tenant-local', 'user-local', 'workspace-demo', 'workspace-revision-1',
  'api', 'direct-ready-v1', '{}'::jsonb, '[]'::jsonb, '[]'::jsonb, 'ready_confirmed',
	'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', $1)`, now)
	if err != nil {
		t.Fatalf("seed readiness assessment: %v", err)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO task_creation_intents (
  intent_id, tenant_id, actor_user_id, workspace_id, workspace_revision_id,
  readiness_assessment_id, task_spec_digest, trigger_policy_revision, status,
  created_at, expires_at
) VALUES ('intent-1', 'tenant-local', 'user-local', 'workspace-demo', 'workspace-revision-1',
  'assessment-1', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
	'direct-create-v1', 'active', $1::timestamptz, $1::timestamptz + interval '15 minutes')`, now)
	if err != nil {
		t.Fatalf("seed creation intent: %v", err)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO tasks (
  task_id, tenant_id, initiator_user_id, workspace_id, workspace_revision_id,
  task_spec_snapshot, task_spec_digest, creation_intent_id, status,
  acceptance_mode, result_refs, revision, created_at, updated_at
) VALUES ($1, 'tenant-local', 'user-local', 'workspace-demo', 'workspace-revision-1',
  '{}'::jsonb, 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  'intent-1', 'queued', 'manual', '[]'::jsonb, 1, $2, $2)`, taskID, now)
	if err != nil {
		t.Fatalf("seed Task: %v", err)
	}
}
