//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	app "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func dispatchWaitFixture(t *testing.T) (*pgxpool.Pool, *taskpostgres.Store, app.RuntimeDispatchWaitObservation) {
	t.Helper()
	pool := postgrestest.Open(t)
	store := taskpostgres.NewStore(pool)
	now := time.Now().UTC()
	admission := admittedTask(t, "wait-assessment", "wait-intent", "Wait without execution", now)
	if err := store.RecordDirectAdmission(context.Background(), admission); err != nil {
		t.Fatal(err)
	}
	created, err := (app.CreateTaskService{Store: store, IDs: &actionIDs{}, Now: func() time.Time { return now }}).Execute(context.Background(), app.CreateTaskCommand{Actor: app.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskSpec: admission.TaskSpec, AcceptanceMode: "manual", IdempotencyKey: "create-wait"})
	if err != nil {
		t.Fatal(err)
	}
	return pool, store, app.RuntimeDispatchWaitObservation{TenantID: "tenant-local", UserID: "user-local", TaskID: created.TaskID, Wait: app.RuntimeDispatchWait{Reason: "capacity_uninitialized", TaskRevision: 1}}
}

func waitOwnerState(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var state string
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object('task',to_jsonb(t),'stream',(SELECT to_jsonb(p) FROM owner_stream_positions p WHERE stream_id='task/'||t.task_id),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY sequence) FROM outbox_events e WHERE aggregate_id=t.task_id))::text FROM tasks t WHERE task_id=$1`, id).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRuntimeDispatchWaitOwnerIdempotencyAndRejectedBindings(t *testing.T) {
	pool, store, observation := dispatchWaitFixture(t)
	ctx := context.Background()
	var original json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(t)-'runtime_dispatch_wait' FROM tasks t WHERE task_id=$1`, observation.TaskID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	observation.Wait.ObservedAt = time.Date(2126, 1, 1, 0, 0, 0, 0, time.UTC)
	first, err := store.RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: observation, EventID: "wait-first"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ObservedAt.Before(before) || first.ObservedAt.After(time.Now().Add(time.Second)) {
		t.Fatal("observation did not use SQL server time")
	}
	state := waitOwnerState(t, pool, observation.TaskID)
	replay, err := store.RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: observation, EventID: "wait-replay"})
	if err != nil || replay != first || state != waitOwnerState(t, pool, observation.TaskID) {
		t.Fatalf("duplicate refreshed clock/owner/outbox: %v", err)
	}
	var after json.RawMessage
	if err = pool.QueryRow(ctx, `SELECT to_jsonb(t)-'runtime_dispatch_wait' FROM tasks t WHERE task_id=$1`, observation.TaskID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(original) != string(after) {
		t.Fatal("diagnostic changed business identity/revision/clocks/FIFO")
	}
	for name, mutate := range map[string]func(*app.RuntimeDispatchWaitObservation){
		"tenant":              func(o *app.RuntimeDispatchWaitObservation) { o.TenantID = "another" },
		"actor":               func(o *app.RuntimeDispatchWaitObservation) { o.UserID = "another" },
		"task":                func(o *app.RuntimeDispatchWaitObservation) { o.TaskID = "missing" },
		"revision":            func(o *app.RuntimeDispatchWaitObservation) { o.Wait.TaskRevision = 2 },
		"unassigned_issuance": func(o *app.RuntimeDispatchWaitObservation) { o.ExpectedPermitIDs = []string{"permit"} },
		"zero_revision":       func(o *app.RuntimeDispatchWaitObservation) { o.Wait.TaskRevision = 0 },
		"identity":            func(o *app.RuntimeDispatchWaitObservation) { o.TaskID = " bad" },
		"reason":              func(o *app.RuntimeDispatchWaitObservation) { o.Wait.Reason = "capacity_binding_mismatch" },
		"null_session_queue":  func(o *app.RuntimeDispatchWaitObservation) { o.Wait.QueueSequence = 1 },
		"missing_queue":       func(o *app.RuntimeDispatchWaitObservation) { o.Wait.WorkerSessionID = "session-other" },
		"wrong_binding": func(o *app.RuntimeDispatchWaitObservation) {
			o.Wait.WorkerSessionID = "session-other"
			o.Wait.QueueSequence = 1
			o.Wait.QueueRevision = 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := observation
			mutate(&bad)
			if _, err := store.RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: bad, EventID: "bad-" + name}); err == nil {
				t.Fatal("invalid diagnostic accepted")
			}
			if state != waitOwnerState(t, pool, observation.TaskID) {
				t.Fatal("rejection mutated owner")
			}
		})
	}
	observation.Wait.Reason = "capacity_exhausted"
	second, err := store.RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: observation, EventID: "wait-second"})
	if err != nil || second.Reason != "capacity_exhausted" {
		t.Fatal(err)
	}
	var n int
	var sequence uint64
	if err = pool.QueryRow(ctx, `SELECT count(*),max(sequence) FROM outbox_events WHERE aggregate_id=$1 AND event_type='TaskRuntimeDispatchWaitChanged.v1'`, observation.TaskID).Scan(&n, &sequence); err != nil {
		t.Fatal(err)
	}
	if n != 2 || sequence != 3 {
		t.Fatalf("reason change events=%d sequence=%d", n, sequence)
	}
}

func TestRuntimeDispatchWaitOwnerRollsBackStreamAndOutboxFaults(t *testing.T) {
	for _, table := range []string{"owner_stream_positions", "outbox_events"} {
		t.Run(table, func(t *testing.T) {
			pool, store, observation := dispatchWaitFixture(t)
			ctx := context.Background()
			before := waitOwnerState(t, pool, observation.TaskID)
			_, err := pool.Exec(ctx, `CREATE FUNCTION fail_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected wait write fault'; END $$; CREATE TRIGGER fail_wait BEFORE INSERT OR UPDATE ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_wait()`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: observation, EventID: "wait-fault"}); err == nil {
				t.Fatal("fault committed")
			}
			if before != waitOwnerState(t, pool, observation.TaskID) {
				t.Fatal("fault leaked Task/stream/Outbox changes")
			}
		})
	}
}

func TestRuntimeDispatchWaitConcurrentDuplicateAndCancel(t *testing.T) {
	pool, store, observation := dispatchWaitFixture(t)
	ctx := context.Background()
	var group sync.WaitGroup
	errs := make(chan error, 8)
	results := make(chan app.RuntimeDispatchWait, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			result, err := store.RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: observation, EventID: fmt.Sprintf("wait-concurrent-%d", i)})
			errs <- err
			results <- result
		}(i)
	}
	group.Wait()
	close(errs)
	close(results)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first app.RuntimeDispatchWait
	for result := range results {
		if first.ObservedAt.IsZero() {
			first = result
		}
		if result != first {
			t.Fatal("concurrent duplicate changed server time")
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='TaskRuntimeDispatchWaitChanged.v1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("duplicate emitted events")
	}
	// A held owner row makes both real commands wait; either serialization is valid.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var blocker int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM tasks WHERE task_id=$1 FOR UPDATE`, observation.TaskID).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, e := (app.TaskActionService{Store: store, IDs: &actionIDs{}, Now: time.Now}).Cancel(ctx, app.CancelTaskCommand{Actor: app.Actor{TenantID: observation.TenantID, UserID: observation.UserID}, TaskID: observation.TaskID, ExpectedTaskRevision: 1, IdempotencyKey: "cancel-wait"})
		done <- e
	}()
	observation.Wait.Reason = "capacity_exhausted"
	go func() {
		_, e := store.RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: observation, EventID: "wait-race"})
		done <- e
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var blocked int
		if err = pool.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))) SELECT count(*) FROM blocked`, blocker).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both real commands did not reach exact Task lock")
		}
		select {
		case e := <-done:
			t.Fatalf("command escaped Task lock: %v", e)
		case <-time.After(5 * time.Millisecond):
		}
	}

	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = <-done; err != nil && !errors.Is(err, platformpostgres.ErrRevisionConflict) {
			t.Fatal(err)
		}
	}
	var status string
	var revision uint64
	if err = pool.QueryRow(ctx, `SELECT status,revision FROM tasks WHERE task_id=$1`, observation.TaskID).Scan(&status, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || revision != 2 {
		t.Fatalf("cancel lost: %s %d", status, revision)
	}
	if _, err = store.RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: observation, EventID: "wait-after-cancel"}); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("terminal diagnostic accepted: %v", err)
	}
}

func TestRuntimeDispatchWaitUsesOneTransactionConnection(t *testing.T) {
	pool, _, observation := dispatchWaitFixture(t)
	config := pool.Config()
	config.MaxConns = 1
	single, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = taskpostgres.NewStore(single).RecordRuntimeDispatchWait(ctx, app.RuntimeDispatchWaitRecord{Observation: observation, EventID: "single-connection-wait"})
	if err != nil {
		t.Fatalf("diagnostic requires a second connection under Task lock: %v", err)
	}
}
