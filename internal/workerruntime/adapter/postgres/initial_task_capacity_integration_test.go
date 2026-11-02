//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func initialCapacityQueue(t *testing.T, pool *pgxpool.Pool, count int) (taskapp.SessionDispatchService, []taskapp.IssueTaskPermitCommand, *concurrentAllocationIDs) {
	t.Helper()
	return initialCapacityQueueAt(t, pool, count, time.Now().UTC())
}

func initialCapacityQueueAt(t *testing.T, pool *pgxpool.Pool, count int, now time.Time) (taskapp.SessionDispatchService, []taskapp.IssueTaskPermitCommand, *concurrentAllocationIDs) {
	t.Helper()
	ctx := context.Background()
	seedAllocationRequests(t, pool, count, now)
	ids := &concurrentAllocationIDs{}
	dispatch := taskapp.SessionDispatchService{Store: taskpostgres.NewStore(pool), IDs: ids, Now: func() time.Time { return now }}
	commands := make([]taskapp.IssueTaskPermitCommand, 0, count)
	for i := 1; i <= count; i++ {
		allocated, err := (workerapp.WorkerSessionAllocator{Store: workerpostgres.NewAllocatorStore(pool), IDs: ids, Now: func() time.Time { return now }}).Execute(ctx, workerapp.AllocateWorkerSessionCommand{TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: fmt.Sprintf("allocation-%02d", i)})
		if err != nil {
			t.Fatal(err)
		}
		_, err = dispatch.EnqueueTask(ctx, taskapp.EnqueueTaskCommand{TenantID: "tenant-local", UserID: "user-local", TaskID: fmt.Sprintf("task-%02d", i), WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1, ExpectedSessionRevision: 1, IdempotencyKey: fmt.Sprintf("enqueue-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		commands = append(commands, taskapp.IssueTaskPermitCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: allocated.WorkerSessionID, RuntimeGeneration: 1, IdempotencyKey: fmt.Sprintf("initial-%d", i)})
	}
	return dispatch, commands, ids
}
func TestInitialTaskCapacityRejectsUninitializedProductionPermit(t *testing.T) {
	pool := postgrestest.Open(t)
	dispatch, commands, _ := initialCapacityQueue(t, pool, 1)
	got, err := dispatch.IssueTaskPermit(context.Background(), commands[0])
	if !errors.Is(err, workerapp.CapacityUninitialized) {
		t.Fatalf("public production IssueTaskPermit authorized without capacity: permit=%+v error=%v", got, err)
	}
	assertAllocationCount(t, pool, "session_dispatch_permits", 0)
	assertAllocationCount(t, pool, "runtime_capacity_executions", 0)
	var generation int
	if err := pool.QueryRow(context.Background(), `SELECT runtime_generation FROM worker_sessions WHERE worker_session_id=$1`, commands[0].WorkerSessionID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 0 {
		t.Fatalf("rejection advanced generation=%d", generation)
	}
}

func TestInitialTaskCapacityReserveConsumeReplay(t *testing.T) {
	pool, store, key, facts := capacityStore(t)
	dispatch, commands, ids := initialCapacityQueue(t, pool, 1)
	// A caller's business clock cannot grant a century-long capacity lease.
	dispatch.Now = func() time.Time { return time.Date(2126, 1, 1, 0, 0, 0, 0, time.UTC) }
	permit, err := dispatch.IssueTaskPermit(context.Background(), commands[0])
	if err != nil {
		t.Fatal(err)
	}
	var token string
	var expiry, ledgerExpiry time.Time
	if err = pool.QueryRow(context.Background(), `SELECT p.capacity_admission_ref,p.expires_at,e.expires_at FROM session_dispatch_permits p JOIN runtime_capacity_executions e ON e.permit_id=p.permit_id WHERE p.permit_id=$1`, permit.PermitID).Scan(&token, &expiry, &ledgerExpiry); err != nil {
		t.Fatal(err)
	}
	if token == "" || !expiry.Equal(ledgerExpiry) || expiry.Sub(time.Now()) > 6*time.Minute {
		t.Fatalf("SQL TTL token=%s expiry=%s ledger=%s", token, expiry, ledgerExpiry)
	}
	facts.Sequence++
	facts.ObservedAt = time.Now().UTC()
	if err = store.ObservePool(context.Background(), signCapacity(t, key, facts)); err != nil {
		t.Fatal(err)
	}
	replay, err := dispatch.IssueTaskPermit(context.Background(), commands[0])
	if err != nil || replay != permit {
		t.Fatalf("permit replay=%+v original=%+v error=%v", replay, permit, err)
	}
	start := workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: time.Now}
	command := residentStartCommand(permit)
	command.StartSnapshot = []byte(`{"runtime_capacity":"forged caller authority"}`)
	started, err := start.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	facts.Sequence++
	facts.ObservedAt = time.Now().UTC()
	if err = store.ObservePool(context.Background(), signCapacity(t, key, facts)); err != nil {
		t.Fatal(err)
	}
	startedAgain, err := start.Execute(context.Background(), command)
	historical := started
	historical.Created = false
	if err != nil || !started.Created || startedAgain != historical {
		t.Fatalf("start replay=%+v original=%+v error=%v", startedAgain, started, err)
	}
	replay, err = dispatch.IssueTaskPermit(context.Background(), commands[0])
	if err != nil || replay != permit {
		t.Fatalf("consumed permit replay=%+v error=%v", replay, err)
	}
	var gotToken, run, turn, runtime string
	var revision uint64
	if err = pool.QueryRow(context.Background(), `SELECT token::text,run_id,turn_id,runtime_id FROM runtime_capacity_executions WHERE permit_id=$1`, permit.PermitID).Scan(&gotToken, &run, &turn, &runtime); err != nil {
		t.Fatal(err)
	}
	if token != gotToken || run != started.WorkerRunID || turn != started.AgentTurnID || runtime != started.RuntimeInstanceID {
		t.Fatalf("identities changed %s %s %s %s", gotToken, run, turn, runtime)
	}
	if err = pool.QueryRow(context.Background(), `SELECT (start_snapshot->'runtime_capacity'->'Target'->>'TaskRevision')::bigint FROM worker_runs WHERE worker_run_id=$1`, run).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("snapshot lacked server baseline revision=%d", revision)
	}
	for _, table := range []string{"runtime_capacity_residents", "runtime_capacity_executions", "session_dispatch_permits", "worker_runs", "agent_turns", "runtime_instances", "scheduled_commands", "task_transitions"} {
		assertInitialCapacityCount(t, pool, table, 1)
	}
}

func TestInitialTaskCapacityBindingsAndRollback(t *testing.T) {
	for _, mode := range []string{"actor", "task", "session", "generation", "revision", "profile", "trigger", "queue", "task_revision", "expiry", "legacy_active", "stale_capacity", "invalid_capacity", "outbox"} {
		t.Run(mode, func(t *testing.T) {
			pool, capacity, key, facts := capacityStore(t)
			dispatch, commands, ids := initialCapacityQueue(t, pool, 2)
			permit, err := dispatch.IssueTaskPermit(context.Background(), commands[0])
			if err != nil {
				t.Fatal(err)
			}
			cmd := residentStartCommand(permit)
			switch mode {
			case "actor":
				cmd.UserID = "other-actor"
			case "task":
				cmd.TaskID = "task-02"
			case "session":
				cmd.WorkerSessionID = commands[1].WorkerSessionID
			case "generation":
				cmd.RuntimeGeneration++
			case "revision":
				cmd.ExpectedSessionRevision++
			case "profile":
				cmd.ResourceProfileID = "caller-profile"
			case "trigger":
				cmd.TriggerType = "task_rework"
			case "queue":
				_, err = pool.Exec(context.Background(), `UPDATE session_task_queue_entries SET queue_revision=queue_revision+1`)
			case "task_revision":
				_, err = pool.Exec(context.Background(), `UPDATE tasks SET revision=revision+1`)
			case "expiry":
				_, err = pool.Exec(context.Background(), `UPDATE runtime_capacity_executions SET expires_at=clock_timestamp()-interval '1 second'`)
			case "legacy_active":
				_, err = pool.Exec(context.Background(), `UPDATE session_dispatch_permits SET capacity_admission_ref='caller-text-legacy'`)
			case "stale_capacity", "invalid_capacity":
				facts.Sequence++
				if mode == "stale_capacity" {
					facts.ObservedAt = time.Now().Add(-time.Minute)
				}
				raw := signCapacity(t, key, facts)
				if mode == "invalid_capacity" {
					raw[len(raw)-5] ^= 1
				}
				if observationErr := capacity.ObservePool(context.Background(), raw); observationErr == nil {
					t.Fatal("bad sample accepted")
				}
			case "outbox":
				_, err = pool.Exec(context.Background(), `CREATE FUNCTION fail_initial_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'initial capacity outbox fault'; END $$; CREATE TRIGGER fail_initial_outbox BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_initial_outbox()`)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = (workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: time.Now}).Execute(context.Background(), cmd)
			if err == nil {
				t.Fatal("invalid or failing start accepted")
			}
			t.Logf("rejected %s: %v", mode, err)
			for _, table := range []string{"worker_runs", "agent_turns", "runtime_instances", "scheduled_commands", "task_transitions"} {
				assertInitialCapacityCount(t, pool, table, 0)
			}
			var consumed bool
			var status string
			if err = pool.QueryRow(context.Background(), `SELECT consumed_at IS NOT NULL FROM runtime_capacity_executions`).Scan(&consumed); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(context.Background(), `SELECT status FROM session_dispatch_permits`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if consumed || status != "active" {
				t.Fatalf("failed start leaked consumed=%v permit=%s", consumed, status)
			}
		})
	}
}

func TestInitialTaskCapacityPermitRollbackAndExpiredReplay(t *testing.T) {
	for _, mode := range []string{"queue_write_fault", "expired_replay", "legacy_consumed"} {
		t.Run(mode, func(t *testing.T) {
			pool, _, _, _ := capacityStore(t)
			dispatch, commands, ids := initialCapacityQueue(t, pool, 1)
			ctx := context.Background()
			if mode == "queue_write_fault" {
				if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_initial_queue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'initial queue fault'; END $$; CREATE TRIGGER fail_initial_queue BEFORE UPDATE ON session_task_queue_entries FOR EACH ROW EXECUTE FUNCTION fail_initial_queue()`); err != nil {
					t.Fatal(err)
				}
				if _, err := dispatch.IssueTaskPermit(ctx, commands[0]); err == nil {
					t.Fatal("queue fault was ignored")
				}
				for _, table := range []string{"runtime_capacity_residents", "runtime_capacity_executions", "session_dispatch_permits"} {
					assertInitialCapacityCount(t, pool, table, 0)
				}
				var generation, revision int
				if err := pool.QueryRow(ctx, `SELECT runtime_generation,session_revision FROM worker_sessions`).Scan(&generation, &revision); err != nil {
					t.Fatal(err)
				}
				if generation != 0 || revision != 1 {
					t.Fatalf("rollback generation=%d revision=%d", generation, revision)
				}
				return
			}
			permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
			if err != nil {
				t.Fatal(err)
			}
			if mode == "expired_replay" {
				if _, err = pool.Exec(ctx, `UPDATE runtime_capacity_executions SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
				if _, err = dispatch.IssueTaskPermit(ctx, commands[0]); !errors.Is(err, workerapp.CapacityRequestExpired) {
					t.Fatalf("expired replay=%v", err)
				}
				assertAllocationCount(t, pool, "runtime_capacity_executions", 1)
				return
			}
			start := workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: time.Now}
			if _, err = start.Execute(ctx, residentStartCommand(permit)); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `UPDATE session_dispatch_permits SET capacity_admission_ref='legacy-consumed-without-ledger'`); err != nil {
				t.Fatal(err)
			}
			if _, err = start.Execute(ctx, residentStartCommand(permit)); !errors.Is(err, workerapp.CapacityBindingMismatch) {
				t.Fatalf("legacy consumed replay=%v", err)
			}
			if _, err = dispatch.IssueTaskPermit(ctx, commands[0]); !errors.Is(err, workerapp.CapacityBindingMismatch) {
				t.Fatalf("legacy consumed permit replay=%v", err)
			}
			assertAllocationCount(t, pool, "worker_runs", 1)
		})
	}
}

func TestInitialTaskCapacityPoolBeforeBusinessLocksAndLastSlot(t *testing.T) {
	pool, _, _, _ := capacityStore(t)
	dispatch, commands, _ := initialCapacityQueue(t, pool, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gatePool := lockOrderPool(t, pool, "initial-capacity-gate")
	gate, err := gatePool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if _, err = gate.Exec(ctx, `SELECT 1 FROM runtime_capacity_pools WHERE pool_id='local' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	firstPool := lockOrderPool(t, pool, "initial-capacity-first")
	secondPool := lockOrderPool(t, pool, "initial-capacity-second")
	results := make(chan error, 2)
	first := dispatch
	first.Store = taskpostgres.NewStore(firstPool)
	second := dispatch
	second.Store = taskpostgres.NewStore(secondPool)
	go func() { _, err := first.IssueTaskPermit(ctx, commands[0]); results <- err }()
	waitDispatchLockEdge(t, ctx, pool, "initial-capacity-first", "initial-capacity-gate")
	go func() { _, err := second.IssueTaskPermit(ctx, commands[1]); results <- err }()
	// PostgreSQL reports the tuple waiter behind the first waiting transaction.
	waitInitialCapacityWaiters(t, ctx, pool, 2)
	if _, err = gate.Exec(ctx, `SELECT 1 FROM tasks ORDER BY task_id FOR UPDATE NOWAIT`); err != nil {
		t.Fatalf("permit locked Task before pool: %v", err)
	}
	if _, err = gate.Exec(ctx, `SELECT 1 FROM worker_sessions ORDER BY worker_session_id FOR UPDATE NOWAIT`); err != nil {
		t.Fatalf("permit locked Session before pool: %v", err)
	}
	if err = gate.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	success, denied := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, workerapp.CapacityExhausted) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("last slot successes=%d denied=%d", success, denied)
	}
	var untouched int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM worker_sessions s JOIN tasks t ON t.worker_session_id=s.worker_session_id JOIN session_task_queue_entries q ON q.task_id=t.task_id WHERE s.runtime_generation=0 AND s.session_revision=1 AND s.status='allocating' AND t.status='queued' AND t.revision=2 AND q.status='pending'`).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if untouched != 1 {
		t.Fatalf("rejected initial graph changed: untouched=%d", untouched)
	}
	for _, table := range []string{"runtime_capacity_residents", "runtime_capacity_executions", "session_dispatch_permits"} {
		assertInitialCapacityCount(t, pool, table, 1)
	}
	for _, table := range []string{"worker_runs", "agent_turns", "scheduled_commands"} {
		assertInitialCapacityCount(t, pool, table, 0)
	}
}
func waitInitialCapacityWaiters(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name IN ('initial-capacity-first','initial-capacity-second') AND cardinality(pg_blocking_pids(pid))>0 AND wait_event_type='Lock'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == want {
			t.Logf("catalog proves %d simultaneous pool waiters", n)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("missing capacity lock waiters")
		case <-tick.C:
		}
	}
}

func TestInitialTaskCapacitySingleConnectionAndStartLockPrefix(t *testing.T) {
	pool, _, _, _ := capacityStore(t)
	dispatch, commands, ids := initialCapacityQueue(t, pool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := pool.Config()
	cfg.MaxConns = 1
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	dispatch.Store = taskpostgres.NewStore(single)
	permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
	if err != nil {
		t.Fatal(err)
	}
	gatePool := lockOrderPool(t, pool, "initial-start-gate")
	gate, err := gatePool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if _, err = gate.Exec(ctx, `SELECT 1 FROM runtime_capacity_pools WHERE pool_id='local' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	startPool := lockOrderPool(t, pool, "initial-start")
	result := make(chan error, 1)
	go func() {
		_, err := (workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(startPool), IDs: ids, Now: time.Now}).Execute(ctx, residentStartCommand(permit))
		result <- err
	}()
	waitDispatchLockEdge(t, ctx, pool, "initial-start", "initial-start-gate")
	for _, table := range []string{"tasks", "worker_sessions", "session_dispatch_permits"} {
		if _, err = gate.Exec(ctx, "SELECT 1 FROM "+table+" FOR UPDATE NOWAIT"); err != nil {
			t.Fatalf("start acquired %s before pool: %v", table, err)
		}
	}
	if err = gate.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	if _, err = (workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(single), IDs: ids, Now: time.Now}).Execute(ctx, residentStartCommand(permit)); err != nil {
		t.Fatalf("single-connection start replay: %v", err)
	}
}

func assertInitialCapacityCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	if table != "scheduled_commands" {
		assertAllocationCount(t, pool, table, want)
		return
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM scheduled_commands WHERE command_type='agent_turn_deadline'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("AgentTurn deadline count=%d want=%d", count, want)
	}
}

func TestInitialTaskCapacityRejectsInvalidAndStaleEvidence(t *testing.T) {
	for _, mode := range []string{"invalid", "stale"} {
		t.Run(mode, func(t *testing.T) {
			pool, store, key, facts := capacityStore(t)
			dispatch, commands, _ := initialCapacityQueue(t, pool, 1)
			facts.Sequence++
			if mode == "stale" {
				facts.ObservedAt = time.Now().Add(-time.Minute)
			}
			raw := signCapacity(t, key, facts)
			if mode == "invalid" {
				raw[len(raw)-5] ^= 1
			}
			if err := store.ObservePool(context.Background(), raw); err == nil {
				t.Fatal("bad evidence was accepted")
			}
			if _, err := dispatch.IssueTaskPermit(context.Background(), commands[0]); err == nil {
				t.Fatal("bad evidence authorized permit")
			} else {
				t.Logf("%s rejected: %v", mode, err)
			}
			for _, table := range []string{"session_dispatch_permits", "runtime_capacity_executions", "runtime_capacity_residents", "worker_runs", "agent_turns", "scheduled_commands"} {
				assertInitialCapacityCount(t, pool, table, 0)
			}
			var gen, rev int
			if err := pool.QueryRow(context.Background(), `SELECT runtime_generation,session_revision FROM worker_sessions`).Scan(&gen, &rev); err != nil {
				t.Fatal(err)
			}
			if gen != 0 || rev != 1 {
				t.Fatalf("failed capacity incremented generation/revision=%d/%d", gen, rev)
			}
		})
	}
}
