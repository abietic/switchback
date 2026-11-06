//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"strings"
	"testing"
	"time"
)

func TestRuntimeDispatchWaitActualRefusalPublicHTTP(t *testing.T) {
	app := newStartGuardApp(t)
	taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "dispatch-wait", "Wait without execution")
	runner := &runtimeRunner{configuration: app.configuration, pool: app.pool}
	if err := runner.startNextQueuedTask(context.Background()); !errors.Is(err, workerapp.CapacityUninitialized) {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := app.outbox.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/v1/views/task_summary/" + taskID, "/api/v1/views/task_summary/index"} {
		dataBytes := guardView(t, app, path)
		var data map[string]any
		if err := json.Unmarshal(dataBytes, &data); err != nil {
			t.Fatal(err)
		}
		if path == "/api/v1/views/task_summary/index" {
			data = data["tasks"].([]any)[0].(map[string]any)
		}
		wait, ok := data["runtime_dispatch_wait"].(map[string]any)
		if !ok || wait["reason"] != "capacity_uninitialized" {
			t.Fatalf("actual queued refusal missing public diagnostic: %s", dataBytes)
		}
	}
}

type dispatchWaitPolicy struct{ cause error }

func (p dispatchWaitPolicy) checkRuntimeStart(context.Context) error { return p.cause }

func dispatchWaitRunner(app *App) *runtimeRunner {
	return &runtimeRunner{configuration: app.configuration, pool: app.pool,
		initialPolicy: unavailableRuntimeStartPolicy{},
		allocator:     workerapp.WorkerSessionAllocator{Store: workerpostgres.NewAllocatorStore(app.pool), IDs: randomIDs{}, Now: time.Now},
		dispatch:      taskapp.SessionDispatchService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}, Now: time.Now},
	}
}

func assertPublicDispatchWait(t *testing.T, app *App, taskID, reason string) map[string]any {
	t.Helper()
	var summary map[string]any
	for _, path := range []string{"/api/v1/views/task_summary/" + taskID, "/api/v1/views/task_summary/index"} {
		raw := guardView(t, app, path)
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if path == "/api/v1/views/task_summary/index" {
			found := false
			for _, item := range data["tasks"].([]any) {
				candidate := item.(map[string]any)
				if candidate["task_id"] == taskID {
					data = candidate
					found = true
					break
				}
			}
			if !found {
				t.Fatal("Task identity missing")
			}
		} else {
			summary = data
		}
		value, exists := data["runtime_dispatch_wait"]
		if reason == "" {
			if exists {
				t.Fatalf("obsolete wait survived: %s", raw)
			}
			continue
		}
		wait, ok := value.(map[string]any)
		if !ok || len(wait) != 2 || wait["reason"] != reason || data["status"] != "queued" {
			t.Fatalf("incorrect public wait: %s", raw)
		}
		if _, err := time.Parse(time.RFC3339Nano, wait["observed_at"].(string)); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"capacity_admission_ref", "queue_revision", "task_revision", "token", "/pool/"} {
			if bytes.Contains(raw, []byte(forbidden)) {
				t.Fatalf("internal diagnostic escaped: %s", forbidden)
			}
		}
	}
	return summary
}

func TestRuntimeDispatchWaitRunnerFiniteCausesAndZeroEffects(t *testing.T) {
	app := newStartGuardApp(t)
	ctx := context.Background()
	taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "wait-classification", "No allocation on refusal")
	runner := dispatchWaitRunner(app)
	var baseline string
	if err := app.pool.QueryRow(ctx, `SELECT (to_jsonb(t)-'runtime_dispatch_wait')::text FROM tasks t WHERE task_id=$1`, taskID).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	reasons := []workerapp.CapacityWaitReason{workerapp.CapacityUninitialized, workerapp.CapacityEvidenceInvalid, workerapp.CapacityEvidenceStale, workerapp.CapacityIdentityDrift, workerapp.CapacityInventoryUnknown, workerapp.CapacityExhausted, workerapp.CapacityPathNotIntegrated}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			publicReason := string(reason)
			if reason == workerapp.CapacityExhausted {
				publicReason = "capacity_exhausted"
			}
			cause := fmt.Errorf("fixture wrapping: %w", reason)
			runner.initialPolicy = dispatchWaitPolicy{cause: cause}
			if err := runner.startNextQueuedTask(ctx); err != cause {
				t.Fatalf("original error object lost: %v", err)
			}
			first := assertPublicDispatchWait(t, app, taskID, publicReason)
			firstBytes, _ := json.Marshal(first)
			if err := runner.startNextQueuedTask(ctx); err != cause {
				t.Fatal(err)
			}
			replayBytes, _ := json.Marshal(assertPublicDispatchWait(t, app, taskID, publicReason))
			if !bytes.Equal(firstBytes, replayBytes) {
				t.Fatal("same reason refreshed observed_at or view")
			}
		})
	}
	for _, cause := range []error{errors.Join(workerapp.CapacityExhausted, context.Canceled), errors.Join(workerapp.CapacityExhausted), workerapp.CapacityBindingMismatch, workerapp.CapacityRequestExpired, workerapp.CapacityContainerProofPending, workerapp.InitialRuntimeStartReplayPending, context.Canceled, errors.New("private-path-and-secret")} {
		runner.initialPolicy = dispatchWaitPolicy{cause: cause}
		if err := runner.startNextQueuedTask(ctx); err != cause {
			t.Fatal("hard/joined cause changed")
		}
		assertPublicDispatchWait(t, app, taskID, "capacity_path_not_integrated")
	}
	var after string
	if err := app.pool.QueryRow(ctx, `SELECT (to_jsonb(t)-'runtime_dispatch_wait')::text FROM tasks t WHERE task_id=$1`, taskID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if baseline != after {
		t.Fatal("wait modified business identity/clocks/revision/FIFO")
	}
	for _, table := range []string{"worker_sessions", "session_task_queue_entries", "session_dispatch_permits", "worker_runs", "agent_turns", "runtime_instances", "scheduled_commands", "runtime_capacity_executions", "runtime_capacity_residents"} {
		var n int
		if err := app.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("denial created %s: %d", table, n)
		}
	}
	// A database recording failure must not be demoted to a schedulable wait.
	if _, err := app.pool.Exec(ctx, `CREATE FUNCTION fail_wait_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='TaskRuntimeDispatchWaitChanged.v1' THEN RAISE EXCEPTION 'fixture event fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_wait_event BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_wait_event()`); err != nil {
		t.Fatal(err)
	}
	runner.initialPolicy = dispatchWaitPolicy{cause: workerapp.CapacityEvidenceInvalid}
	err := runner.startNextQueuedTask(ctx)
	if !errors.Is(err, workerapp.CapacityEvidenceInvalid) || runtimeStagingWait(err) {
		t.Fatalf("record failure swallowed as wait: %v", err)
	}
	assertPublicDispatchWait(t, app, taskID, "capacity_path_not_integrated")
}

func recordDispatchWait(t *testing.T, app *App, observation taskapp.RuntimeDispatchWaitObservation) {
	t.Helper()
	_, err := (taskapp.RuntimeDispatchWaitService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}}).Record(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
}

func enqueueDispatchWaitTask(t *testing.T, app *App, runner *runtimeRunner, taskID string) (taskapp.RuntimeDispatchWaitObservation, taskapp.IssueTaskPermitCommand) {
	t.Helper()
	ctx := context.Background()
	var allocation string
	if err := app.pool.QueryRow(ctx, `SELECT allocation_request_id FROM session_allocation_requests WHERE owner_id=$1`, taskID).Scan(&allocation); err != nil {
		t.Fatal(err)
	}
	allocated, err := runner.allocator.Execute(ctx, workerapp.AllocateWorkerSessionCommand{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, AllocationRequestID: allocation})
	if err != nil {
		t.Fatal(err)
	}
	enqueued, err := runner.dispatch.EnqueueTask(ctx, taskapp.EnqueueTaskCommand{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, TaskID: taskID, WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1, ExpectedSessionRevision: 1, IdempotencyKey: "wait-enqueue"})
	if err != nil {
		t.Fatal(err)
	}
	return taskapp.RuntimeDispatchWaitObservation{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, TaskID: taskID, Wait: taskapp.RuntimeDispatchWait{Reason: "capacity_exhausted", TaskRevision: enqueued.TaskRevision, WorkerSessionID: allocated.WorkerSessionID, QueueSequence: enqueued.SessionQueueSequence, QueueRevision: enqueued.QueueRevision}}, taskapp.IssueTaskPermitCommand{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, WorkerSessionID: allocated.WorkerSessionID, RuntimeGeneration: 1, IdempotencyKey: "wait-issue"}
}

func waitStartCommand(t *testing.T, app *App, permit taskapp.IssuedTaskPermit) workerapp.StartWorkerRunCommand {
	t.Helper()
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	network := integrationNetworkProfile(t)
	return workerapp.StartWorkerRunCommand{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, WorkerSessionID: permit.WorkerSessionID, TaskID: permit.TaskID, PermitID: permit.PermitID, ExpectedSessionRevision: permit.ExpectedSessionRevision, RuntimeGeneration: 1, TriggerType: "task_initial", StartSnapshot: json.RawMessage(`{"diagnostic_test":true}`), AgentProfileID: profile.ID, AgentProfileRevision: "1", AgentProfileDigest: profile.Digest, AgentTurnContent: json.RawMessage(`{"text":"test"}`), TurnIdempotencyKey: "wait-start", RuntimeImageRef: "sha256:" + strings.Repeat("a", 64), ResourceProfileID: config.LocalStandardV1, NetworkPolicyID: network.ID, NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest, NetworkEnforcementStatus: config.NetworkEnforcementActive, ControlChannelEpoch: 1}
}

func TestRuntimeDispatchWaitNewIssueClearsOldButReplayPreservesLaterRefusal(t *testing.T) {
	app := newStartGuardApp(t)
	ctx := context.Background()
	runner := dispatchWaitRunner(app)
	taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "wait-permit", "New issue versus replay")
	// Real preflight refusal, then actual Enqueue changes the binding; no reassign API is invented.
	if err := runner.startNextQueuedTask(ctx); !errors.Is(err, errRuntimeStartCapacityUnavailable) {
		t.Fatal(err)
	}
	assertPublicDispatchWait(t, app, taskID, "capacity_path_not_integrated")
	observation, command := enqueueDispatchWaitTask(t, app, runner, taskID)
	assertPublicDispatchWait(t, app, taskID, "")
	// Actual Issue refusal after a fixture-only successful preflight.
	runner.initialPolicy = dispatchWaitPolicy{}
	if err := runner.startNextQueuedTask(ctx); err != workerapp.CapacityUninitialized {
		t.Fatalf("Issue refusal: %v", err)
	}
	assertPublicDispatchWait(t, app, taskID, "capacity_uninitialized")
	runtimecapacity.Initialize(t, app.pool) // Explicit signed synthetic Trust/Observe; no SQL physics bypass.
	// Fault at the clearing event must roll back the new permit and reservation too.
	var ownerBefore string
	if err := app.pool.QueryRow(ctx, `SELECT jsonb_build_object('task',to_jsonb(t),'stream',(SELECT to_jsonb(p) FROM owner_stream_positions p WHERE stream_id='task/'||t.task_id),'session',(SELECT to_jsonb(s) FROM worker_sessions s WHERE worker_session_id=t.worker_session_id),'queue',(SELECT jsonb_agg(to_jsonb(q)) FROM session_task_queue_entries q WHERE q.task_id=t.task_id))::text FROM tasks t WHERE task_id=$1`, taskID).Scan(&ownerBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := app.pool.Exec(ctx, `CREATE FUNCTION fail_wait_clear() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='TaskRuntimeDispatchWaitChanged.v1' THEN RAISE EXCEPTION 'fixture clear failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_wait_clear BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_wait_clear()`); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.dispatch.IssueTaskPermit(ctx, command); err == nil {
		t.Fatal("clear-event fault committed permit")
	}
	var ownerAfter string
	if err := app.pool.QueryRow(ctx, `SELECT jsonb_build_object('task',to_jsonb(t),'stream',(SELECT to_jsonb(p) FROM owner_stream_positions p WHERE stream_id='task/'||t.task_id),'session',(SELECT to_jsonb(s) FROM worker_sessions s WHERE worker_session_id=t.worker_session_id),'queue',(SELECT jsonb_agg(to_jsonb(q)) FROM session_task_queue_entries q WHERE q.task_id=t.task_id))::text FROM tasks t WHERE task_id=$1`, taskID).Scan(&ownerAfter); err != nil {
		t.Fatal(err)
	}
	if ownerBefore != ownerAfter {
		t.Fatal("failed Issue changed owner/queue/stream/wait")
	}
	for _, table := range []string{"session_dispatch_permits", "runtime_capacity_executions", "runtime_capacity_residents"} {
		var n int
		if err := app.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("failed Issue leaked %s", table)
		}
	}
	if _, err := app.pool.Exec(ctx, `DROP TRIGGER fail_wait_clear ON outbox_events; DROP FUNCTION fail_wait_clear()`); err != nil {
		t.Fatal(err)
	}
	permit, err := runner.dispatch.IssueTaskPermit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicDispatchWait(t, app, taskID, "")
	// A real later preflight refusal on the very same queued binding must survive replay.
	cause := fmt.Errorf("later capacity check: %w", workerapp.CapacityEvidenceStale)
	runner.initialPolicy = dispatchWaitPolicy{cause: cause}
	if err = runner.startNextQueuedTask(ctx); err != cause {
		t.Fatal(err)
	}
	before := assertPublicDispatchWait(t, app, taskID, "capacity_evidence_stale")
	replay, err := runner.dispatch.IssueTaskPermit(ctx, command)
	if err != nil || replay != permit {
		t.Fatalf("permit replay: %v", err)
	}
	after := assertPublicDispatchWait(t, app, taskID, "capacity_evidence_stale")
	beforeBytes, _ := json.Marshal(before)
	afterBytes, _ := json.Marshal(after)
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("permit replay erased or refreshed later wait")
	}
	started, err := (workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(app.pool), IDs: randomIDs{}, Now: time.Now}).Execute(ctx, waitStartCommand(t, app, permit))
	if err != nil {
		t.Fatal(err)
	}
	if !started.Created {
		t.Fatal("initial Start was not original")
	}
	summary := assertPublicDispatchWait(t, app, taskID, "")
	if summary["status"] != "running" || summary["worker_session_id"] != permit.WorkerSessionID {
		t.Fatal("Start lost identity")
	}
	_, err = (taskapp.RuntimeDispatchWaitService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}}).Record(ctx, observation)
	if !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("late observation accepted after Start: %v", err)
	}
	var cleared bool
	if err = app.pool.QueryRow(ctx, `SELECT runtime_dispatch_wait IS NULL FROM tasks WHERE task_id=$1`, taskID).Scan(&cleared); err != nil || !cleared {
		t.Fatal("Start did not clear stored wait")
	}
	t.Log("actual preflight/Issue refusals -> signed new Issue clears -> later actual refusal survives same-permit replay -> original Start clears; exact IDs preserved")
}

func TestRuntimeDispatchWaitCancelAndBindingCAS(t *testing.T) {
	app := newStartGuardApp(t)
	ctx := context.Background()
	runner := dispatchWaitRunner(app)
	taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "wait-cas", "Binding CAS")
	old := taskapp.RuntimeDispatchWaitObservation{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, TaskID: taskID, Wait: taskapp.RuntimeDispatchWait{Reason: "capacity_uninitialized", TaskRevision: 1}}
	recordDispatchWait(t, app, old)
	bound, _ := enqueueDispatchWaitTask(t, app, runner, taskID)
	if _, err := (taskapp.RuntimeDispatchWaitService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}}).Record(ctx, old); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("old preallocation observation accepted: %v", err)
	}
	for _, mutate := range []func(*taskapp.RuntimeDispatchWaitObservation){func(o *taskapp.RuntimeDispatchWaitObservation) { o.Wait.QueueRevision++ }, func(o *taskapp.RuntimeDispatchWaitObservation) { o.Wait.QueueSequence++ }, func(o *taskapp.RuntimeDispatchWaitObservation) { o.Wait.WorkerSessionID = "other" }} {
		bad := bound
		mutate(&bad)
		if _, err := (taskapp.RuntimeDispatchWaitService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}}).Record(ctx, bad); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
			t.Fatalf("wrong assigned binding accepted: %v", err)
		}
	}
	recordDispatchWait(t, app, bound)
	assertPublicDispatchWait(t, app, taskID, "capacity_exhausted")
	_, err := (taskapp.TaskActionService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}, Now: time.Now}).Cancel(ctx, taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: bound.TenantID, UserID: bound.UserID}, TaskID: taskID, ExpectedTaskRevision: 2, IdempotencyKey: "wait-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	summary := assertPublicDispatchWait(t, app, taskID, "")
	if summary["status"] != "cancelled" {
		t.Fatal("cancel status lost")
	}
}

func TestRuntimeDispatchWaitConcurrentStartAndEnqueue(t *testing.T) {
	for _, operation := range []string{"start", "enqueue"} {
		t.Run(operation, func(t *testing.T) {
			app := newStartGuardApp(t)
			runner := dispatchWaitRunner(app)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "wait-lock", "Serialize diagnostic with owner")
			observation := taskapp.RuntimeDispatchWaitObservation{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, TaskID: taskID, Wait: taskapp.RuntimeDispatchWait{Reason: "capacity_exhausted", TaskRevision: 1}}
			var action func() error
			if operation == "start" {
				var command taskapp.IssueTaskPermitCommand
				observation, command = enqueueDispatchWaitTask(t, app, runner, taskID)
				runtimecapacity.Initialize(t, app.pool)
				permit, err := runner.dispatch.IssueTaskPermit(ctx, command)
				if err != nil {
					t.Fatal(err)
				}
				observation.ExpectedPermitIDs = []string{permit.PermitID}
				startCommand := waitStartCommand(t, app, permit)
				action = func() error {
					_, err := (workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(app.pool), IDs: randomIDs{}, Now: time.Now}).Execute(ctx, startCommand)
					return err
				}
			} else {
				var allocation string
				if err := app.pool.QueryRow(ctx, `SELECT allocation_request_id FROM session_allocation_requests WHERE owner_id=$1`, taskID).Scan(&allocation); err != nil {
					t.Fatal(err)
				}
				allocated, err := runner.allocator.Execute(ctx, workerapp.AllocateWorkerSessionCommand{TenantID: observation.TenantID, UserID: observation.UserID, AllocationRequestID: allocation})
				if err != nil {
					t.Fatal(err)
				}
				action = func() error {
					_, err := runner.dispatch.EnqueueTask(ctx, taskapp.EnqueueTaskCommand{TenantID: observation.TenantID, UserID: observation.UserID, TaskID: taskID, WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1, ExpectedSessionRevision: 1, IdempotencyKey: "wait-locked-enqueue"})
					return err
				}
			}
			tx, err := app.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			var blocker int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM tasks WHERE task_id=$1 FOR UPDATE`, taskID).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			actionDone := make(chan error, 1)
			recordDone := make(chan error, 1)
			go func() { actionDone <- action() }()
			go func() {
				_, err := (taskapp.RuntimeDispatchWaitService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}}).Record(ctx, observation)
				recordDone <- err
			}()
			for {
				var blocked int
				if err = app.pool.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))) SELECT count(*) FROM blocked`, blocker).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked >= 2 {
					break
				}
				select {
				case err := <-actionDone:
					t.Fatalf("owner bypassed held row: %v", err)
				case err := <-recordDone:
					t.Fatalf("diagnostic bypassed held row: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-actionDone; err != nil {
				t.Fatal(err)
			}
			if err = <-recordDone; err != nil && !errors.Is(err, platformpostgres.ErrRevisionConflict) {
				t.Fatal(err)
			}
			assertPublicDispatchWait(t, app, taskID, "")
			if _, err = (taskapp.RuntimeDispatchWaitService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}}).Record(ctx, observation); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
				t.Fatalf("old observation revived after %s: %v", operation, err)
			}
			t.Logf("both actual %s and Record were observed blocked by the exact Task row before release", operation)
		})
	}
}

func TestRuntimeDispatchWaitStaleRunnerObservationIsHardJoined(t *testing.T) {
	app := newStartGuardApp(t)
	taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "wait-stale-runner", "Stale refusal")
	entered := make(chan struct{})
	release := make(chan struct{})
	cause := fmt.Errorf("original stale capacity: %w", workerapp.CapacityEvidenceStale)
	runner := dispatchWaitRunner(app)
	runner.initialPolicy = dispatchWaitBlockingPolicy{entered: entered, release: release, cause: cause}
	done := make(chan error, 1)
	go func() { done <- runner.startNextQueuedTask(context.Background()) }()
	<-entered
	_, err := (taskapp.TaskActionService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}, Now: time.Now}).Cancel(context.Background(), taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID}, TaskID: taskID, ExpectedTaskRevision: 1, IdempotencyKey: "stale-cancel"})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	err = <-done
	if !errors.Is(err, cause) || !errors.Is(err, platformpostgres.ErrRevisionConflict) || runtimeStagingWait(err) {
		t.Fatalf("stale observation must preserve cause plus hard CAS error: %v", err)
	}
	assertPublicDispatchWait(t, app, taskID, "")
}

type dispatchWaitBlockingPolicy struct {
	entered, release chan struct{}
	cause            error
}

func (p dispatchWaitBlockingPolicy) checkRuntimeStart(ctx context.Context) error {
	close(p.entered)
	select {
	case <-p.release:
		return p.cause
	case <-ctx.Done():
		return ctx.Err()
	}
}

// This test pauses an already decided refusal after the runner's FIFO snapshot.
// Signed Issue is real; expired/revoked statuses are explicitly historical owner
// fixtures, not a new retirement API or proof of released physical capacity.
func TestRuntimeDispatchWaitLateRefusalFencedByIssuedHistory(t *testing.T) {
	for _, status := range []string{"active", "expired", "revoked"} {
		t.Run(status, func(t *testing.T) {
			app := newStartGuardApp(t)
			runner := dispatchWaitRunner(app)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "late-wait", "Fence old refusal after new Issue")
			observation, command := enqueueDispatchWaitTask(t, app, runner, taskID)
			runtimecapacity.Initialize(t, app.pool)
			entered, release := make(chan struct{}), make(chan struct{})
			cause := fmt.Errorf("decided before Issue: %w", workerapp.CapacityEvidenceStale)
			runner.initialPolicy = dispatchWaitBlockingPolicy{entered: entered, release: release, cause: cause}
			done := make(chan error, 1)
			go func() { done <- runner.startNextQueuedTask(ctx) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			permit, err := runner.dispatch.IssueTaskPermit(ctx, command)
			if err != nil {
				close(release)
				<-done
				t.Fatal(err)
			}
			// An independent connection observes the committed immutable ID.
			var committed string
			if err = app.pool.QueryRow(ctx, `SELECT permit_id FROM session_dispatch_permits WHERE permit_id=$1`, permit.PermitID).Scan(&committed); err != nil || committed != permit.PermitID {
				close(release)
				<-done
				t.Fatal("new Issue did not commit")
			}
			if status != "active" {
				if _, err = app.pool.Exec(ctx, `UPDATE session_dispatch_permits SET status=$2 WHERE permit_id=$1`, permit.PermitID, status); err != nil {
					close(release)
					<-done
					t.Fatal(err)
				}
				var active int
				if err = app.pool.QueryRow(ctx, `SELECT count(*) FROM session_dispatch_permits WHERE worker_session_id=$1 AND status='active'`, permit.WorkerSessionID).Scan(&active); err != nil || active != 0 {
					close(release)
					<-done
					t.Fatal("history fixture still has active permit")
				}
			}
			assertPublicDispatchWait(t, app, taskID, "")
			before := dispatchWaitIssuanceState(t, app, taskID)
			close(release)
			err = <-done
			if err == cause {
				assertPublicDispatchWait(t, app, taskID, "capacity_evidence_stale")
				t.Fatalf("old pre-Issue refusal became public after committed %s permit; expected issuance conflict", status)
			}
			if !errors.Is(err, cause) || !errors.Is(err, platformpostgres.ErrRevisionConflict) || runtimeStagingWait(err) {
				t.Fatalf("late refusal needs cause plus hard issuance conflict: %v", err)
			}
			if after := dispatchWaitIssuanceState(t, app, taskID); after != before {
				t.Fatal("stale observation changed Task/queue/clocks/stream/Outbox/permit/ledger")
			}
			// Direct Store callers cannot omit the issued identity or submit an
			// arbitrary/noncanonical history to bypass the same locked check.
			for _, ids := range [][]string{nil, {}, {"unrelated-permit"}, {permit.PermitID, permit.PermitID}, {"z", "a"}, {" bad"}} {
				invalid := observation
				invalid.ExpectedPermitIDs = ids
				_, recordErr := taskpostgres.NewStore(app.pool).RecordRuntimeDispatchWait(ctx, taskapp.RuntimeDispatchWaitRecord{Observation: invalid, EventID: "bad-history"})
				if recordErr == nil {
					t.Fatal("direct Store bypassed issuance history")
				}
				if len(ids) == 0 && !errors.Is(recordErr, platformpostgres.ErrRevisionConflict) {
					t.Fatalf("missing history must conflict: %v", recordErr)
				}
				if dispatchWaitIssuanceState(t, app, taskID) != before {
					t.Fatal("rejected history changed owner state")
				}
			}
			assertPublicDispatchWait(t, app, taskID, "")
			// A fresh original FIFO snapshot after the same issuance is permitted.
			later := fmt.Errorf("decided after Issue: %w", workerapp.CapacityEvidenceStale)
			runner.initialPolicy = dispatchWaitPolicy{cause: later}
			if err = runner.startNextQueuedTask(ctx); err != later {
				t.Fatalf("true later refusal rejected: %v", err)
			}
			assertPublicDispatchWait(t, app, taskID, "capacity_evidence_stale")
			if status == "active" {
				visible := dispatchWaitIssuanceState(t, app, taskID)
				replay, err := runner.dispatch.IssueTaskPermit(ctx, command)
				if err != nil || replay != permit {
					t.Fatalf("same permit replay failed: %v", err)
				}
				if visible != dispatchWaitIssuanceState(t, app, taskID) {
					t.Fatal("same-permit replay changed later refusal or owner state")
				}
				assertPublicDispatchWait(t, app, taskID, "capacity_evidence_stale")
			}
			t.Logf("old refusal fenced after committed %s issuance; fresh post-issuance refusal visible", status)
		})
	}
}

func dispatchWaitIssuanceState(t *testing.T, app *App, taskID string) string {
	t.Helper()
	var state string
	err := app.pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'task',to_jsonb(t),
 'stream',(SELECT to_jsonb(p) FROM owner_stream_positions p WHERE stream_id='task/'||t.task_id),
 'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY sequence) FROM outbox_events e WHERE aggregate_id=t.task_id),
 'queue',(SELECT jsonb_agg(to_jsonb(q) ORDER BY queue_entry_id) FROM session_task_queue_entries q WHERE task_id=t.task_id),
 'permits',(SELECT jsonb_agg(to_jsonb(p) ORDER BY permit_id) FROM session_dispatch_permits p WHERE target_id=t.task_id),
 'executions',(SELECT jsonb_agg(to_jsonb(e) ORDER BY token) FROM runtime_capacity_executions e WHERE worker_session_id=t.worker_session_id),
 'residents',(SELECT jsonb_agg(to_jsonb(r) ORDER BY commitment_id) FROM runtime_capacity_residents r WHERE worker_session_id=t.worker_session_id)
 )::text FROM tasks t WHERE task_id=$1`, taskID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRuntimeDispatchWaitExactHistoricalIssuanceSet(t *testing.T) {
	app := newStartGuardApp(t)
	runner := dispatchWaitRunner(app)
	ctx := context.Background()
	taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "wait-history-set", "Exact retained issuance set")
	observation, command := enqueueDispatchWaitTask(t, app, runner, taskID)
	runtimecapacity.Initialize(t, app.pool)
	permit, err := runner.dispatch.IssueTaskPermit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	observation.ExpectedPermitIDs = []string{permit.PermitID}
	observation.Wait.Reason = "capacity_evidence_stale"
	// Explicit historical owner rows: several permits may share created_at and
	// older IDs need not sort later. No retirement/re-issue API is invented here.
	insertHistory := func(id string) {
		t.Helper()
		_, err := app.pool.Exec(ctx, `INSERT INTO session_dispatch_permits
 (permit_id,worker_session_id,target_kind,target_id,session_queue_sequence,queue_revision,
 expected_session_revision,runtime_generation,capacity_admission_ref,status,idempotency_key,expires_at,created_at)
 SELECT $2,worker_session_id,target_kind,target_id,session_queue_sequence,queue_revision,
 expected_session_revision,runtime_generation,capacity_admission_ref,'expired',$2,expires_at,created_at
 FROM session_dispatch_permits WHERE permit_id=$1`, permit.PermitID, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertHistory("a-historical-permit")
	before := dispatchWaitIssuanceState(t, app, taskID)
	store := taskpostgres.NewStore(app.pool)
	if _, err = store.RecordRuntimeDispatchWait(ctx, taskapp.RuntimeDispatchWaitRecord{Observation: observation, EventID: "partial-history"}); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("partial immutable history accepted: %v", err)
	}
	if before != dispatchWaitIssuanceState(t, app, taskID) {
		t.Fatal("partial history changed owner")
	}
	// Fresh runner captures all IDs in canonical order, ignoring their timestamps/status.
	cause := fmt.Errorf("fresh complete history: %w", workerapp.CapacityEvidenceStale)
	runner.initialPolicy = dispatchWaitPolicy{cause: cause}
	if err = runner.startNextQueuedTask(ctx); err != cause {
		t.Fatalf("runner did not capture exact multi-ID history: %v", err)
	}
	assertPublicDispatchWait(t, app, taskID, "capacity_evidence_stale")
	observation.ExpectedPermitIDs = []string{"a-historical-permit", permit.PermitID}
	before = dispatchWaitIssuanceState(t, app, taskID)
	if _, err = store.RecordRuntimeDispatchWait(ctx, taskapp.RuntimeDispatchWaitRecord{Observation: observation, EventID: "complete-history-replay"}); err != nil {
		t.Fatal(err)
	}
	if before != dispatchWaitIssuanceState(t, app, taskID) {
		t.Fatal("exact complete-history replay refreshed diagnostic")
	}
	// Inserting an earlier-sorting identity defeats latest-ID/created_at fences,
	// and must be checked before the existing same-reason deduplication branch.
	insertHistory("0-earlier-sorting-history")
	before = dispatchWaitIssuanceState(t, app, taskID)
	if _, err = store.RecordRuntimeDispatchWait(ctx, taskapp.RuntimeDispatchWaitRecord{Observation: observation, EventID: "stale-dedup-history"}); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("dedup bypassed changed issuance set: %v", err)
	}
	if before != dispatchWaitIssuanceState(t, app, taskID) {
		t.Fatal("stale dedup changed owner")
	}
	t.Log("same-time inactive historical rows require exact ordered ID set; omission, partial history and stale dedup rejected")
}
