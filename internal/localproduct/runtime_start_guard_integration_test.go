//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func newStartGuardApp(t *testing.T) *App {
	t.Helper()
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := newWithPool(context.Background(), Config{Profile: profile, NetworkProfile: integrationNetworkProfile(t), DataRoot: t.TempDir(), ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID, WorkspaceRevisionID: defaultWorkspaceRevisionID}, postgrestest.Open(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}

func TestProductionStartGuardLeavesTasksQueuedBeforeAllocation(t *testing.T) {
	app := newStartGuardApp(t)
	first := createRuntimeRunnerTask(t, app.Handler(), app.pool, "guard-first", "First guarded task")
	second := createRuntimeRunnerTask(t, app.Handler(), app.pool, "guard-second", "Second guarded task")
	runner := &runtimeRunner{configuration: app.configuration, pool: app.pool,
		allocator: workerapp.WorkerSessionAllocator{Store: workerpostgres.NewAllocatorStore(app.pool), IDs: randomIDs{}, Now: time.Now},
		dispatch:  taskapp.SessionDispatchService{Store: taskpostgres.NewStore(app.pool), IDs: randomIDs{}, Now: time.Now, PermitTTL: time.Minute},
	}
	for i := 0; i < 3; i++ {
		if err := runner.startNextQueuedTask(context.Background()); !errors.Is(err, workerapp.CapacityUninitialized) {
			t.Fatalf("new start was not capacity denied: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := app.outbox.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{first, second} {
		view := httptest.NewRecorder()
		app.Handler().ServeHTTP(view, httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/"+id, nil))
		if view.Code != http.StatusOK || !bytes.Contains(view.Body.Bytes(), []byte(`"status":"queued"`)) {
			t.Fatalf("queued public Task view status=%d body=%s", view.Code, view.Body.String())
		}
	}
	// These permit/deadline/allocation facts have no public read model.
	for _, table := range []string{"worker_sessions", "session_dispatch_permits", "session_task_queue_entries", "worker_runs", "agent_turns", "runtime_instances", "scheduled_commands"} {
		var count int
		if err := app.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("capacity wait created %d %s", count, table)
		}
	}
	var allocationCount int
	if err := app.pool.QueryRow(context.Background(), `SELECT count(*) FROM session_allocation_requests WHERE status='pending'`).Scan(&allocationCount); err != nil {
		t.Fatal(err)
	}
	if allocationCount != 2 {
		t.Fatalf("original allocation queue count=%d", allocationCount)
	}
	var firstOwner, secondOwner string
	if err := app.pool.QueryRow(context.Background(), `SELECT owner_id FROM session_allocation_requests WHERE session_allocation_sequence=1`).Scan(&firstOwner); err != nil {
		t.Fatal(err)
	}
	if err := app.pool.QueryRow(context.Background(), `SELECT owner_id FROM session_allocation_requests WHERE session_allocation_sequence=2`).Scan(&secondOwner); err != nil {
		t.Fatal(err)
	}
	if firstOwner != first || secondOwner != second {
		t.Fatal("capacity wait reordered allocation FIFO")
	}
	// Historical allocation/enqueue survives the upgrade unchanged too.
	var allocationID string
	if err := app.pool.QueryRow(context.Background(), `SELECT allocation_request_id FROM session_allocation_requests WHERE owner_id=$1`, first).Scan(&allocationID); err != nil {
		t.Fatal(err)
	}
	allocated, err := runner.allocator.Execute(context.Background(), workerapp.AllocateWorkerSessionCommand{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, AllocationRequestID: allocationID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.dispatch.EnqueueTask(context.Background(), taskapp.EnqueueTaskCommand{TenantID: app.configuration.Profile.TenantID, UserID: app.configuration.Profile.ActorUserID, TaskID: first, WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1, ExpectedSessionRevision: 1, IdempotencyKey: "guard-preexisting-enqueue"})
	if err != nil {
		t.Fatal(err)
	}
	before := guardView(t, app, "/api/v1/views/task_summary/"+first)
	for i := 0; i < 3; i++ {
		if err := runner.startNextQueuedTask(context.Background()); !errors.Is(err, workerapp.CapacityUninitialized) {
			t.Fatal(err)
		}
	}
	after := guardView(t, app, "/api/v1/views/task_summary/"+first)
	var beforeData, afterData map[string]any
	if err := json.Unmarshal(before, &beforeData); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &afterData); err != nil {
		t.Fatal(err)
	}
	// Actual refusal adds only a diagnostic; exact queued business identity stays fixed.
	delete(beforeData, "runtime_dispatch_wait")
	delete(afterData, "runtime_dispatch_wait")
	beforeBusiness, _ := json.Marshal(beforeData)
	afterBusiness, _ := json.Marshal(afterData)
	if !bytes.Equal(beforeBusiness, afterBusiness) {
		t.Fatal("existing queued binding changed")
	}
	var sequence uint64
	var status string
	if err := app.pool.QueryRow(context.Background(), `SELECT session_queue_sequence,status FROM session_task_queue_entries WHERE task_id=$1`, first).Scan(&sequence, &status); err != nil {
		t.Fatal(err)
	}
	if sequence != 1 || status != "pending" {
		t.Fatalf("existing Session queue sequence=%d status=%s", sequence, status)
	}
	var sessions, permits int
	if err := app.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM worker_sessions),(SELECT count(*) FROM session_dispatch_permits)`).Scan(&sessions, &permits); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || permits != 0 {
		t.Fatalf("existing capacity wait sessions=%d permits=%d", sessions, permits)
	}
}

func guardRequest(app *App, path string, body map[string]any) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded)))
	return response
}

func TestProductionStartGuardHTTPRejectsFollowUpGenerically(t *testing.T) {
	app := newStartGuardApp(t)
	body := map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": "blocked-follow-up", "expected_session_revision": 1, "worker_run_id": "run-unavailable", "expected_run_revision": 1, "agent_turn_id": "turn-unavailable", "expected_turn_revision": 1, "content": "Continue"}
	response := guardRequest(app, "/api/v1/worker-sessions/session-unavailable/follow-up", body)
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"command_conflict\"}\n" {
		t.Fatalf("unavailable execution status=%d body=%s", response.Code, response.Body.String())
	}
	body["user_id"] = "client-supplied-actor"
	response = guardRequest(app, "/api/v1/worker-sessions/session-unavailable/follow-up", body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("actor validation status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProductionStartGuardDurableQueuePreservesTerminalReport(t *testing.T) {
	f := newSupplementFixture(t, nil)
	taskID := createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "guard-durable-task", "Preserve the prior report")
	turn, callback := f.next()
	f.send(callback)
	queueID := f.queue(turn)
	f.startPolicy.deny.Store(true)
	f.finish(turn, callback)
	f.waitApplied("terminal-" + string(turn.start.Envelope.AgentTurnID))
	deadline := time.Now().Add(5 * time.Second)
	var settlement workerapp.TurnSettlement
	var checkpoint runtimeTurnSettlementCheckpoint
	for {
		var err error
		settlement, err = f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turn.start.Envelope.AgentTurnID))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(settlement.PhaseState, &checkpoint); err != nil {
			t.Fatal(err)
		}
		if checkpoint.ContinuationPlan != nil {
			_, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx)
			if errors.Is(err, errRuntimeStartCapacityUnavailable) {
				break
			}
			if err != nil && !errors.Is(err, errRuntimeSettlementOwnerBusy) {
				t.Fatalf("continuation did not wait for capacity: %v", err)
			}
			if settlement.Phase == workerapp.TurnSettlementCompleted {
				t.Fatal("capacity denial consumed continuation")
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no durable continuation plan")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if checkpoint.ContinuationPlan.QueueCommandID != queueID || checkpoint.ContinuationResult != nil || settlement.Phase != workerapp.TurnSettlementTerminalEventApplied {
		t.Fatalf("capacity wait fabricated continuation: phase=%s checkpoint=%+v", settlement.Phase, checkpoint.ContinuationResult)
	}
	for i := 0; i < 3; i++ {
		_, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx)
		if err != nil && !errors.Is(err, errRuntimeStartCapacityUnavailable) && !errors.Is(err, errRuntimeSettlementOwnerBusy) {
			t.Fatal(err)
		}
	}
	var status string
	var sequence uint64
	if err := f.pool.QueryRow(f.ctx, `SELECT status,queue_sequence FROM worker_user_commands WHERE command_id=$1`, queueID).Scan(&status, &sequence); err != nil {
		t.Fatal(err)
	}
	if status != "accepted" || sequence != 1 {
		t.Fatalf("queue head status=%s sequence=%d", status, sequence)
	}
	if n := f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID)); n != 0 {
		t.Fatalf("successor Runs=%d", n)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.app.outbox.RunOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	view := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(view, httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/"+taskID, nil))
	if view.Code != http.StatusOK || !bytes.Contains(view.Body.Bytes(), []byte(`"status":"delivered"`)) {
		t.Fatalf("prior delivered report view status=%d body=%s", view.Code, view.Body.String())
	}
	workerView := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(workerView, httptest.NewRequest(http.MethodGet, "/api/v1/views/worker_detail/"+string(turn.start.Envelope.WorkerSessionID), nil))
	if workerView.Code != http.StatusOK || !bytes.Contains(workerView.Body.Bytes(), []byte(`"report"`)) {
		t.Fatalf("public Worker report status=%d body=%s", workerView.Code, workerView.Body.String())
	}
}

// Freeze after the real first checkpoint transaction, then release the live
// supervisors with cancellation. The connected journal naturally redelivers
// their terminal envelopes to the production reconciliation pass.
func newCapacitySettlementFixture(t *testing.T, blocked int) (*supplementFixture, []observationFixtureTurn, []string, string) {
	t.Helper()
	return newCapacitySettlementFixtureWithQueue(t, blocked, true)
}

func newCapacitySettlementFixtureWithQueue(t *testing.T, blocked int, enqueue bool) (*supplementFixture, []observationFixtureTurn, []string, string) {
	t.Helper()
	frozen := make(chan workerapp.TurnSettlement, blocked+1)
	release := make(chan struct{})
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		f.runtime.heartbeatStart = make(chan struct{})
		f.settlements.afterFreeze = func(ctx context.Context, s workerapp.TurnSettlement) error {
			var cp runtimeTurnSettlementCheckpoint
			if err := json.Unmarshal(s.PhaseState, &cp); err != nil {
				return err
			}
			if s.Phase == workerapp.TurnSettlementAccepted && cp.EvidenceInput != nil {
				frozen <- s
				select {
				case <-release:
					return context.Canceled
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
	})
	var turns []observationFixtureTurn
	var queues []string
	var reportTask string
	for i := 0; i <= blocked; i++ {
		taskID := createRuntimeRunnerTask(t, f.app.Handler(), f.pool, fmt.Sprintf("capacity-fairness-%d", i), "Deliver independent terminal report")
		turn, callback := f.next()
		f.send(callback)
		supplementMust(t, f.binder.Deactivate(callback.correlation))
		turns = append(turns, turn)
		if i < blocked {
			if enqueue {
				queues = append(queues, f.queue(turn))
			}
		} else {
			reportTask = taskID
		}
	}
	f.startPolicy.deny.Store(true)
	var previous time.Time
	for _, turn := range turns {
		turn.finish(t)
		s := supplementReceive(t, f.ctx, frozen)
		if s.AgentTurnID != string(turn.start.Envelope.AgentTurnID) || (!previous.IsZero() && !s.AcceptedAt.After(previous)) {
			t.Fatal("terminal acceptance barriers did not freeze oldest-first ordering")
		}
		previous = s.AcceptedAt
	}
	// Pause the background scheduler, while leaving real control ownership live.
	f.app.runtime.startupReady.Store(false)
	close(release)
	// No background ticks exist in this opt-in fixture. Each initial tick was
	// joined by next(); now join every supervisor that entered the freeze hook.
	f.app.runtime.turnSupervisors.group.Wait()
	t.Log("owner trace: all entered ticks and frozen supervisors exited; scan may begin")
	f.settlements.afterFreeze = nil
	return f, turns, queues, reportTask
}

func TestProductionStartGuardCapacityWaitSettlesIndependentReport(t *testing.T) {
	f, turns, queues, reportTask := newCapacitySettlementFixture(t, 1)
	before := guardView(t, f.app, "/api/v1/views/task_summary/"+reportTask)
	if bytes.Contains(before, []byte(`"status":"delivered"`)) {
		t.Fatal("independent report was delivered before reconciliation")
	}
	if _, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx); !errors.Is(err, errRuntimeStartCapacityUnavailable) {
		t.Fatalf("pass lost capacity-wait signal: %v", err)
	}
	assertCapacitySettlementReport(t, f, turns[len(turns)-1], reportTask)
	assertCapacitySettlementWaiting(t, f, turns[:1], queues)
}

func TestProductionStartGuardCapacityWaitKeepsNormalAcceptedOrder(t *testing.T) {
	f, turns, _, taskID := newCapacitySettlementFixtureWithQueue(t, 1, false)
	if _, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx); err != nil {
		t.Fatalf("ordinary oldest settlement failed: %v", err)
	}
	s, err := f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turns[0].start.Envelope.AgentTurnID))
	supplementMust(t, err)
	var cp runtimeTurnSettlementCheckpoint
	supplementMust(t, json.Unmarshal(s.PhaseState, &cp))
	assertCapacitySettlementReport(t, f, turns[0], cp.EvidenceInput.TaskID)
	if view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID); !bytes.Contains(view, []byte(`"status":"running"`)) {
		t.Fatalf("normal scan bypassed earlier accepted terminal: %s", view)
	}
	if _, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertCapacitySettlementReport(t, f, turns[1], taskID)
}

func TestProductionStartGuardCapacityWaitScanBoundStillMakesNextPassProgress(t *testing.T) {
	f, turns, queues, taskID := newCapacitySettlementFixture(t, maxRuntimeSettlementCandidatesPerPass)
	if _, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx); !errors.Is(err, errRuntimeStartCapacityUnavailable) {
		t.Fatalf("bounded pass lost capacity wait: %v", err)
	}
	if view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID); !bytes.Contains(view, []byte(`"status":"running"`)) {
		t.Fatalf("pass exceeded its candidate bound: %s", view)
	}
	assertCapacitySettlementWaiting(t, f, turns[:len(queues)], queues)
	for _, turn := range turns[:len(queues)] {
		live := f.app.runtime.runtimes[string(turn.start.Envelope.RuntimeInstanceID)]
		if !live.mu.TryLock() {
			t.Fatal("blocked candidate retained live mutex after the pass")
		}
		live.mu.Unlock()
	}
	// The next pass first encounters the same oldest capacity-wait head. Its
	// durable applied phase lets terminal evidence beyond the bound progress.
	if _, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx); !errors.Is(err, errRuntimeStartCapacityUnavailable) {
		t.Fatalf("next pass lost capacity wait: %v", err)
	}
	assertCapacitySettlementReport(t, f, turns[len(turns)-1], taskID)
	assertCapacitySettlementWaiting(t, f, turns[:len(queues)], queues)
}

func TestProductionStartGuardCapacityWaitSkipsBusyOwnerAndForeignLease(t *testing.T) {
	for _, foreignLease := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign_lease_%t", foreignLease), func(t *testing.T) {
			f, turns, _, taskID := newCapacitySettlementFixture(t, 1)
			id := "turn-settlement/" + string(turns[0].start.Envelope.AgentTurnID)
			before, err := f.app.runtime.settlements.Load(f.ctx, id)
			supplementMust(t, err)
			if foreignLease {
				service := f.app.runtime.settlements
				service.Now = func() time.Time { return before.LeaseExpiresAt.Add(time.Microsecond) }
				before, err = service.Claim(f.ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: id, ExpectedRevision: before.Revision, OwnerID: "independent-live-owner"})
				supplementMust(t, err)
			} else {
				live := f.app.runtime.runtimes[string(turns[0].start.Envelope.RuntimeInstanceID)]
				entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
				go func() {
					live.mu.Lock()
					t.Log("owner trace: active owner entered live mutex")
					close(entered)
					<-release
					live.mu.Unlock()
					t.Log("owner trace: active owner exited live mutex")
					close(exited)
				}()
				<-entered
				defer func() { close(release); <-exited }()
			}
			t.Logf("owner trace: scanner entered after manual tick/supervisor joins; foreign lease=%t", foreignLease)
			if _, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx); err != nil {
				t.Fatalf("independent terminal failed behind owner: %v", err)
			}
			t.Log("owner trace: blocked first candidate skipped; independent second report settled")
			assertCapacitySettlementReport(t, f, turns[1], taskID)
			after, err := f.app.runtime.settlements.Load(f.ctx, id)
			supplementMust(t, err)
			if after.Revision != before.Revision || after.LeaseFence != before.LeaseFence || after.LeaseOwnerID != before.LeaseOwnerID || !bytes.Equal(before.PhaseState, after.PhaseState) {
				t.Fatal("scan changed busy/foreign-owned settlement")
			}
		})
	}
}

func TestProductionStartGuardCapacityWaitMixedBusyPrefixMakesNextPassProgress(t *testing.T) {
	f, turns, queues, taskID := newCapacitySettlementFixture(t, 2)
	id := "turn-settlement/" + string(turns[0].start.Envelope.AgentTurnID)
	before, err := f.app.runtime.settlements.Load(f.ctx, id)
	supplementMust(t, err)
	live := f.app.runtime.runtimes[string(turns[0].start.Envelope.RuntimeInstanceID)]
	live.mu.Lock()
	defer live.mu.Unlock()
	var secondPassError error
	for pass := 0; pass < 2; pass++ {
		_, found, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx)
		if !found || (pass == 0 && !errors.Is(err, errRuntimeStartCapacityUnavailable)) || (err != nil && !errors.Is(err, errRuntimeStartCapacityUnavailable)) {
			t.Fatalf("mixed prefix pass %d found=%t error=%v", pass, found, err)
		}
		if pass == 0 {
			if view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID); !bytes.Contains(view, []byte(`"status":"running"`)) {
				t.Fatalf("first pass exceeded candidate bound: %s", view)
			}
		} else {
			secondPassError = err
		}
	}
	assertCapacitySettlementReport(t, f, turns[2], taskID)
	if secondPassError != nil {
		t.Fatalf("second pass invented capacity evidence without visiting blocked continuation: %v", secondPassError)
	}
	assertCapacitySettlementWaiting(t, f, turns[1:2], queues[1:2])
	after, err := f.app.runtime.settlements.Load(f.ctx, id)
	supplementMust(t, err)
	if after.Revision != before.Revision || after.LeaseFence != before.LeaseFence || after.LeaseOwnerID != before.LeaseOwnerID || !bytes.Equal(before.PhaseState, after.PhaseState) {
		t.Fatal("mixed scan changed busy settlement")
	}
}

type initializingCapacityProvisioner struct {
	*supplementProvisioner
	calls        int
	initializing chan workerapp.RuntimeProvisionRequest
	release      <-chan struct{}
}

func (p *initializingCapacityProvisioner) Provision(ctx context.Context, request workerapp.RuntimeProvisionRequest, preStart workerapp.RuntimePreStartCheck) (workerapp.ProvisionedRuntime, error) {
	p.calls++
	if p.calls == 2 {
		p.initializing <- request
		select {
		case <-p.release:
		case <-ctx.Done():
			return workerapp.ProvisionedRuntime{}, ctx.Err()
		}
	}
	return p.supplementProvisioner.Provision(ctx, request, preStart)
}

func TestProductionStartGuardCapacityWaitInitializingRuntimeIsNotMissing(t *testing.T) {
	initializing := make(chan workerapp.RuntimeProvisionRequest, 1)
	release := make(chan struct{})
	f := newSupplementFixture(t, func(f *supplementFixture) {
		f.runtime.heartbeatStart = make(chan struct{})
		f.app.runtime.starts.Provisioner = &initializingCapacityProvisioner{supplementProvisioner: f.provisioner, initializing: initializing, release: release}
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "capacity-initializing-first", "Keep first Session running")
	_, callback := f.next()
	f.send(callback)
	supplementMust(t, f.binder.Deactivate(callback.correlation))
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "capacity-initializing-second", "Start independent Session")
	request := supplementReceive(t, f.ctx, initializing)
	// The first Session's terminal waiter runs this same global inspector while
	// the second Session is still preparing/provisioning. No inventory entry
	// exists yet; this is actual external absence, not seeded Runtime loss.
	supplementMust(t, f.app.runtime.detectRuntimeFailures(f.ctx))
	close(release)
	turn, callback := f.next()
	if string(turn.start.Envelope.RuntimeInstanceID) != request.RuntimeInstanceID {
		t.Fatal("initializing Session did not dispatch its original Runtime")
	}
	f.send(callback)
	// Live handoff must release initialization ownership: subsequent real
	// container loss still belongs to the established zero-attempt failure flow.
	f.runtime.inventory.mu.Lock()
	delete(f.runtime.inventory.requests, request.RuntimeInstanceID)
	f.runtime.inventory.mu.Unlock()
	supplementMust(t, f.app.runtime.detectRuntimeFailures(f.ctx))
	assertCapacityInitializationFailure(t, f, request)
}

func TestProductionStartGuardCapacityWaitCancelledInitializerReleasesOwnership(t *testing.T) {
	initializing := make(chan workerapp.RuntimeProvisionRequest, 1)
	release := make(chan struct{})
	f := newSupplementFixture(t, func(f *supplementFixture) {
		f.runtime.heartbeatStart = make(chan struct{})
		f.app.runtime.starts.Provisioner = &initializingCapacityProvisioner{supplementProvisioner: f.provisioner, initializing: initializing, release: release}
		f.app.runtime.reconciler.Cleaner = f.app.runtime.provisioner
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "capacity-cancel-init-first", "Keep first Session running")
	_, callback := f.next()
	f.send(callback)
	supplementMust(t, f.binder.Deactivate(callback.correlation))
	f.app.runtime.startupReady.Store(false)
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "capacity-cancel-init-second", "Cancel independent initialization")
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.app.runtime.startNextQueuedTask(ctx) }()
	request := supplementReceive(t, f.ctx, initializing)
	supplementMust(t, f.app.runtime.detectRuntimeFailures(f.ctx))
	cancel()
	if err := supplementReceive(t, f.ctx, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("initializer swallowed cancellation: %v", err)
	}
	close(release)
	// Failed initialization with a cancelled cleanup context leaves an actual
	// ownerless starting Runtime. The released marker must not mask its absence.
	supplementMust(t, f.app.runtime.detectRuntimeFailures(f.ctx))
	assertCapacityInitializationFailure(t, f, request)
}

func assertCapacityInitializationFailure(t *testing.T, f *supplementFixture, request workerapp.RuntimeProvisionRequest) {
	t.Helper()
	var turnID, taskID string
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT turn.agent_turn_id,run.task_id FROM agent_turns turn JOIN worker_runs run USING(worker_run_id) WHERE turn.worker_run_id=$1`, request.WorkerRunID).Scan(&turnID, &taskID))
	state, found, err := f.app.runtime.recoveryStore.FindTerminalRuntimeFailureForTurn(f.ctx, f.app.configuration.Profile.TenantID, request.WorkerSessionID, request.WorkerRunID, turnID, request.RuntimeInstanceID, request.Generation, 1)
	supplementMust(t, err)
	if !found || state.AttemptCount != 0 || state.OutcomeReason != "runtime_recovery_disabled" {
		t.Fatalf("initialization protection masked real missing Runtime: found=%t state=%+v", found, state)
	}
	if view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID); !bytes.Contains(view, []byte(`"status":"waiting_user"`)) {
		t.Fatalf("real missing Runtime did not surface on public Task: %s", view)
	}
}

type cancelCapacitySettlementPolicy struct{ cancel context.CancelFunc }

func (policy cancelCapacitySettlementPolicy) checkRuntimeStart(context.Context) error {
	policy.cancel()
	return errRuntimeStartCapacityUnavailable
}

func TestProductionStartGuardCapacityWaitPropagatesOrdinaryErrorAndCancellation(t *testing.T) {
	for _, cancelPass := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_pass_%t", cancelPass), func(t *testing.T) {
			f, turns, queues, taskID := newCapacitySettlementFixture(t, 1)
			ctx := f.ctx
			if cancelPass {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				f.app.runtime.startPolicy = cancelCapacitySettlementPolicy{cancel: cancel}
			} else {
				s, err := f.app.runtime.settlements.Load(ctx, "turn-settlement/"+string(turns[1].start.Envelope.AgentTurnID))
				supplementMust(t, err)
				var cp runtimeTurnSettlementCheckpoint
				supplementMust(t, json.Unmarshal(s.PhaseState, &cp))
				// Only this fixture's frozen external filesystem source is removed.
				live := f.app.runtime.runtimes[string(turns[1].start.Envelope.RuntimeInstanceID)]
				supplementMust(t, os.Remove(filepath.Join(live.storage.TraceSpool.BackendPath, cp.EvidenceInput.PatchSnapshotFile)))
			}
			_, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(ctx)
			if cancelPass {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("pass swallowed cancellation: %v", err)
				}
			} else if !errors.Is(err, os.ErrNotExist) || errors.Is(err, errRuntimeStartCapacityUnavailable) {
				t.Fatalf("pass swallowed ordinary evidence error: %v", err)
			}
			if view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID); !bytes.Contains(view, []byte(`"status":"running"`)) {
				t.Fatalf("failed/cancelled pass fabricated delivery: %s", view)
			}
			assertCapacitySettlementWaiting(t, f, turns[:1], queues)
		})
	}
}

func assertCapacitySettlementReport(t *testing.T, f *supplementFixture, turn observationFixtureTurn, taskID string) {
	t.Helper()
	view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID)
	if !bytes.Contains(view, []byte(`"status":"delivered"`)) {
		t.Fatalf("capacity-blocked older Session suppressed independent delivered report: %s", view)
	}
	s, err := f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turn.start.Envelope.AgentTurnID))
	supplementMust(t, err)
	var cp runtimeTurnSettlementCheckpoint
	supplementMust(t, json.Unmarshal(s.PhaseState, &cp))
	if s.Phase != workerapp.TurnSettlementCompleted || cp.EvidenceInput == nil {
		t.Fatalf("independent report lacks completed settlement: %s", s.Phase)
	}
	id := cp.EvidenceInput.ReportArtifactID
	task := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(task, httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/"+taskID, nil))
	worker := guardView(t, f.app, "/api/v1/views/worker_detail/"+string(turn.start.Envelope.WorkerSessionID))
	artifact := guardView(t, f.app, "/api/v1/views/artifact_detail/"+id)
	if task.Code != http.StatusOK || !bytes.Contains(task.Body.Bytes(), []byte(string(turn.start.Envelope.WorkerSessionID))) || !bytes.Contains(worker, []byte(id)) || !bytes.Contains(artifact, []byte(`"report"`)) {
		t.Fatalf("public views lost report identity: task=%s worker=%s artifact=%s", task.Body.String(), worker, artifact)
	}
	content := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(content, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+id+"/content", nil))
	if content.Code != http.StatusOK || content.Body.Len() == 0 {
		t.Fatalf("report content unavailable status=%d body=%s", content.Code, content.Body.String())
	}
	t.Logf("independent Task delivered; Task links Worker, Worker links report Artifact and readable content; report bytes=%d", content.Body.Len())
}

func assertCapacitySettlementWaiting(t *testing.T, f *supplementFixture, turns []observationFixtureTurn, queues []string, preexistingTurns ...string) {
	t.Helper()
	for i, turn := range turns {
		s, err := f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turn.start.Envelope.AgentTurnID))
		supplementMust(t, err)
		var cp runtimeTurnSettlementCheckpoint
		supplementMust(t, json.Unmarshal(s.PhaseState, &cp))
		if s.Phase != workerapp.TurnSettlementTerminalEventApplied || cp.ContinuationPlan == nil || cp.ContinuationPlan.QueueCommandID != queues[i] || cp.ContinuationResult != nil {
			t.Fatalf("capacity wait fabricated completion/continuation: phase=%s checkpoint=%s", s.Phase, s.PhaseState)
		}
		var status string
		var sequence uint64
		supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT status,queue_sequence FROM worker_user_commands WHERE command_id=$1`, queues[i]).Scan(&status, &sequence))
		if status != "accepted" || sequence != 1 {
			t.Fatalf("capacity wait changed accepted FIFO head: %s/%d", status, sequence)
		}
		if f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID)) != 0 || f.count(`SELECT count(*) FROM agent_turns WHERE worker_session_id=$1 AND agent_turn_id<>$2 AND NOT (agent_turn_id=ANY(COALESCE($3::text[],ARRAY[]::text[])))`, string(turn.start.Envelope.WorkerSessionID), string(turn.start.Envelope.AgentTurnID), preexistingTurns) != 0 {
			t.Fatal("capacity wait invented successor Run/Turn/start/deadline")
		}
		if f.count(`SELECT count(*) FROM session_dispatch_permits WHERE worker_session_id=$1 AND status='issued'`, string(turn.start.Envelope.WorkerSessionID)) != 0 {
			t.Fatal("capacity wait invented dispatch permit")
		}
	}
}

func TestProductionStartGuardOrdinaryQueueStillDeliversPreviousReport(t *testing.T) {
	f := newSupplementFixture(t, nil)
	taskID := createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "guard-ordinary-task", "Complete ordinary prior report")
	turn, callback := f.next()
	f.send(callback)
	queueID := f.queue(turn)
	f.startPolicy.deny.Store(true)
	if err := f.binder.Deactivate(callback.correlation); err != nil {
		t.Fatal(err)
	}
	// Freeze the synthetic transport before taking current graph revisions.
	// The historical legacy supervisor captured RuntimeRevision before active
	// and model events; that pre-existing mismatch is outside this guard task.
	f.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f.ctx = ctx
	live := f.app.runtime.runtimes[string(turn.start.Envelope.RuntimeInstanceID)]
	e := turn.start.Envelope
	current, err := f.app.runtime.readRestoredActiveTurn(ctx, startupRuntimeCandidate{Restore: liveRuntimeRestoreRequest{WorkerSessionID: string(e.WorkerSessionID), WorkerRunID: string(e.WorkerRunID), RuntimeInstanceID: string(e.RuntimeInstanceID), RuntimeGeneration: uint64(e.RuntimeGeneration), AgentSessionID: string(e.AgentSessionID), ControlChannelEpoch: e.ChannelEpoch}, TurnID: string(e.AgentTurnID), ContainerID: live.containerID})
	if err != nil {
		t.Fatal(err)
	}
	e.Sequence = 0
	e.MessageID = "legacy-terminal-" + string(e.AgentTurnID)
	e.IdempotencyKey = e.MessageID
	e.MessageType = "agent.update"
	e.Payload = json.RawMessage(`{"update_kind":"terminal","update":{"stop_reason":"end_turn"}}`)
	delivery := &recordingWorkerControlDelivery{}
	events := make(chan workerapp.WorkerControlEvent, 1)
	events <- workerapp.WorkerControlEvent{Envelope: e, Payload: e.Payload, Delivery: delivery}
	live.events = events
	if err := f.app.runtime.awaitAndSettleTurn(ctx, live, *current, nil); err != nil {
		t.Fatalf("settle ordinary prior Turn: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := f.app.outbox.RunOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
		view := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(view, httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/"+taskID, nil))
		if view.Code == http.StatusOK && bytes.Contains(view.Body.Bytes(), []byte(`"status":"delivered"`)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ordinary prior report suppressed status=%d body=%s", view.Code, view.Body.String())
		}
		time.Sleep(15 * time.Millisecond)
	}
	if delivery.calls != 1 || delivery.retryCalls != 0 {
		t.Fatalf("ordinary terminal acknowledgement calls=%d retry=%d", delivery.calls, delivery.retryCalls)
	}
	var status string
	var sequence uint64
	if err := f.pool.QueryRow(f.ctx, `SELECT status,queue_sequence FROM worker_user_commands WHERE command_id=$1`, queueID).Scan(&status, &sequence); err != nil {
		t.Fatal(err)
	}
	if status != "accepted" || sequence != 1 {
		t.Fatalf("ordinary queue consumed status=%s sequence=%d", status, sequence)
	}
	if n := f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID)); n != 0 {
		t.Fatalf("ordinary successor Runs=%d", n)
	}
	workerView := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(workerView, httptest.NewRequest(http.MethodGet, "/api/v1/views/worker_detail/"+string(turn.start.Envelope.WorkerSessionID), nil))
	if workerView.Code != http.StatusOK || !bytes.Contains(workerView.Body.Bytes(), []byte(`"report"`)) {
		t.Fatalf("ordinary Worker report status=%d body=%s", workerView.Code, workerView.Body.String())
	}
}

func TestProductionStartGuardLegacyDeliveredTurnDoesNotPrompt(t *testing.T) {
	f := newSupplementFixture(t, nil)
	turn, callback := f.standalone()
	f.send(callback)
	f.finish(turn, callback)
	f.settled(turn)
	f.startPolicy.deny.Store(true)
	f.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f.ctx = ctx
	s, r, a := f.revisions(turn)
	e := turn.start.Envelope
	// Construct the historical pre-upgrade row through the existing real
	// application service; production HTTP cannot use this fixture seam.
	next, err := (workerapp.SessionFollowUpService{Store: workerpostgres.NewSessionFollowUpStore(f.pool), IDs: randomIDs{}, Now: time.Now}).Execute(ctx, workerapp.StartSessionFollowUpCommand{TenantID: f.app.configuration.Profile.TenantID, UserID: f.app.configuration.Profile.ActorUserID, WorkerSessionID: string(e.WorkerSessionID), ExpectedSessionRevision: s, PreviousWorkerRunID: string(e.WorkerRunID), ExpectedRunRevision: r, PreviousAgentTurnID: string(e.AgentTurnID), ExpectedTurnRevision: a, Content: "Historical pending prompt", IdempotencyKey: "legacy-delivered-start"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.app.runtime.readPendingTurn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.app.runtime.executeNextDeliveredTurn(ctx); !errors.Is(err, errRuntimeStartCapacityUnavailable) {
			t.Fatalf("legacy delivered execution err=%v", err)
		}
	}
	live := f.app.runtime.runtimes[string(e.RuntimeInstanceID)]
	if err := f.app.runtime.superviseDeliveredTurn(ctx, live, pending); !errors.Is(err, errRuntimeStartCapacityUnavailable) {
		t.Fatalf("legacy prompt supervision err=%v", err)
	}
	if n := f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'agent_turn_id'=$1 AND envelope->>'message_type'='agent_turn.start'`, next.AgentTurnID); n != 0 {
		t.Fatalf("new prompts=%d", n)
	}
	var started, deadline time.Time
	if err := f.pool.QueryRow(ctx, `SELECT started_at,deadline_at FROM agent_turns WHERE agent_turn_id=$1`, next.AgentTurnID).Scan(&started, &deadline); err != nil {
		t.Fatal(err)
	}
	if !started.Equal(pending.StartedAt) || !deadline.Equal(pending.DeadlineAt) {
		t.Fatal("historical deadline rewritten")
	}
	for i := 0; i < 3; i++ {
		if _, err := f.app.outbox.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	view := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(view, httptest.NewRequest(http.MethodGet, "/api/v1/views/worker_detail/"+string(e.WorkerSessionID), nil))
	if view.Code != http.StatusOK || !bytes.Contains(view.Body.Bytes(), []byte(next.AgentTurnID)) {
		t.Fatalf("historical Worker view status=%d body=%s", view.Code, view.Body.String())
	}
}

func guardView(t *testing.T, app *App, path string) []byte {
	t.Helper()
	for i := 0; i < 3; i++ {
		if _, err := app.outbox.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("view status=%d body=%s", response.Code, response.Body.String())
	}
	var snapshot struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot.Data
}

func TestProductionStartGuardExistingTargetsKeepIdentityAndResult(t *testing.T) {
	f := newSupplementFixture(t, nil)
	taskID := createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "guard-existing-task", "Existing delivered report")
	turn, callback := f.next()
	f.send(callback)
	f.finish(turn, callback)
	f.settled(turn)
	f.startPolicy.deny.Store(true)
	s, r, a := f.revisions(turn)
	var revision uint64
	if err := f.pool.QueryRow(f.ctx, `SELECT revision FROM tasks WHERE task_id=$1`, taskID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	taskPath := "/api/v1/views/task_summary/" + taskID
	workerPath := "/api/v1/views/worker_detail/" + string(turn.start.Envelope.WorkerSessionID)
	taskBefore, workerBefore := guardView(t, f.app, taskPath), guardView(t, f.app, workerPath)
	for _, kind := range []string{"chat", "rework"} {
		response := guardRequest(f.app, "/api/v1/tasks/"+taskID+"/continue", map[string]any{"schema_version": "gateway.task_continuation.v1", "idempotency_key": "guard-continue-" + kind, "expected_task_revision": revision, "worker_session_id": turn.start.Envelope.WorkerSessionID, "expected_session_revision": s, "worker_run_id": turn.start.Envelope.WorkerRunID, "expected_run_revision": r, "agent_turn_id": turn.start.Envelope.AgentTurnID, "expected_turn_revision": a, "interaction_kind": kind, "message": "Continue exact delivery"})
		if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"command_conflict\"}\n" {
			t.Fatalf("continue-%s status=%d body=%s", kind, response.Code, response.Body.String())
		}
	}
	response := guardRequest(f.app, "/api/v1/tasks/"+taskID+"/rework", map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "guard-legacy-rework", "expected_task_revision": revision, "message": "Rework exact delivery"})
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"command_conflict\"}\n" {
		t.Fatalf("rework status=%d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Equal(taskBefore, guardView(t, f.app, taskPath)) || !bytes.Equal(workerBefore, guardView(t, f.app, workerPath)) {
		t.Fatal("rejected Task start changed public identity/revision/result")
	}
	// A real accept still commits under the unavailable-capacity slice.
	f.post("/api/v1/tasks/"+taskID+"/accept", map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "guard-accept-existing", "expected_task_revision": revision})
	s, r, a = f.revisions(turn)
	workerBefore = guardView(t, f.app, workerPath)
	response = guardRequest(f.app, "/api/v1/worker-sessions/"+string(turn.start.Envelope.WorkerSessionID)+"/follow-up", map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": "guard-existing-follow-up", "expected_session_revision": s, "worker_run_id": turn.start.Envelope.WorkerRunID, "expected_run_revision": r, "agent_turn_id": turn.start.Envelope.AgentTurnID, "expected_turn_revision": a, "content": "Continue exact Session"})
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"command_conflict\"}\n" {
		t.Fatalf("follow-up status=%d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Equal(workerBefore, guardView(t, f.app, workerPath)) {
		t.Fatal("rejected follow-up changed Session identity/revision/result")
	}
	if n := f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID)); n != 0 {
		t.Fatalf("HTTP created %d successor Runs", n)
	}
}

type diagnosticStorageChecker struct {
	failure             error
	reads               atomic.Int32
	waitForCancellation bool
}

func (checker *diagnosticStorageChecker) CheckWritableLayer(ctx context.Context) error {
	checker.reads.Add(1)
	if checker.waitForCancellation {
		<-ctx.Done()
		return ctx.Err()
	}
	return checker.failure
}

func TestProductionStartGuardBoundsStorageDiagnosticWait(t *testing.T) {
	app := newStartGuardApp(t)
	checker := &diagnosticStorageChecker{waitForCancellation: true}
	installRuntimeStartPolicyFixture(t, app, unavailableRuntimeStartPolicy{checker: checker})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body := map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": "slow-diagnostic", "expected_session_revision": 1, "worker_run_id": "slow-run", "expected_run_revision": 1, "agent_turn_id": "slow-turn", "expected_turn_revision": 1, "content": "Continue"}
	encoded, _ := json.Marshal(body)
	response := httptest.NewRecorder()
	started := time.Now()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/worker-sessions/slow-session/follow-up", bytes.NewReader(encoded)).WithContext(ctx))
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("optional storage diagnostic held unavailable start for %s", elapsed)
	}
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"command_conflict\"}\n" {
		t.Fatalf("slow diagnostic status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProductionStartGuardDiagnosticsNeverAuthorizeExecution(t *testing.T) {
	for _, name := range []string{"missing-checker", "Info-error", "unsupported-storage", "classic-overlay2-XFS-compatible", "canceled-before-read"} {
		t.Run(name, func(t *testing.T) {
			app := newStartGuardApp(t)
			taskID := createRuntimeRunnerTask(t, app.Handler(), app.pool, "diagnostic-create", "Remain queued without authority")
			checker := &diagnosticStorageChecker{}
			policy := unavailableRuntimeStartPolicy{checker: checker}
			switch name {
			case "missing-checker":
				policy.checker = nil
			case "Info-error":
				checker.failure = errors.New("Engine private-id /host/docker-root Info failed")
			case "unsupported-storage":
				checker.failure = errors.New("containerd snapshotter cannot prove size enforcement")
			}
			installRuntimeStartPolicyFixture(t, app, policy)
			runner := &runtimeRunner{configuration: app.configuration, pool: app.pool, startPolicy: policy}
			ctx := context.Background()
			if name == "canceled-before-read" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := runner.startNextQueuedTask(ctx)
			if name == "canceled-before-read" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled scheduling err=%v", err)
				}
			} else if !errors.Is(err, errRuntimeStartCapacityUnavailable) {
				t.Fatalf("diagnostic permitted start: %v", err)
			}
			body := map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": "diagnostic-follow-up", "expected_session_revision": 1, "worker_run_id": "diagnostic-run", "expected_run_revision": 1, "agent_turn_id": "diagnostic-turn", "expected_turn_revision": 1, "content": "Continue"}
			encoded, _ := json.Marshal(body)
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/worker-sessions/diagnostic-session/follow-up", bytes.NewReader(encoded)).WithContext(ctx))
			if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"command_conflict\"}\n" {
				t.Fatalf("diagnostic leak/allow status=%d body=%s", response.Code, response.Body.String())
			}
			if name == "canceled-before-read" && checker.reads.Load() != 0 {
				t.Fatal("canceled start read storage diagnostics")
			}
			view := guardView(t, app, "/api/v1/views/task_summary/"+taskID)
			if !bytes.Contains(view, []byte(`"status":"queued"`)) {
				t.Fatalf("diagnostic changed queued Task: %s", view)
			}
			var count int
			if err := app.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM worker_sessions)+(SELECT count(*) FROM worker_runs)+(SELECT count(*) FROM agent_turns)+(SELECT count(*) FROM session_dispatch_permits)`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("diagnostic created %d execution identities", count)
			}
		})
	}
}

func TestProductionStartGuardPassesQueueInterruptApprovalAndCancel(t *testing.T) {
	f := newSupplementFixture(t, nil)
	taskID := createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "guard-command-task", "Preserve existing control commands")
	turn, callback := f.next()
	f.send(callback)
	f.startPolicy.deny.Store(true)
	// Only this fake transport is stopped; existing active identities remain.
	// Command acceptance is observed without a synthetic Agent treating the
	// interrupt command as another prompt.
	f.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f.ctx = ctx
	s, r, a := f.revisions(turn)
	base := map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": "guard-queue-pass", "expected_session_revision": s, "worker_run_id": turn.start.Envelope.WorkerRunID, "expected_run_revision": r, "agent_turn_id": turn.start.Envelope.AgentTurnID, "expected_turn_revision": a, "content": "Enqueue without starting"}
	path := "/api/v1/worker-sessions/" + string(turn.start.Envelope.WorkerSessionID)
	response := guardRequest(f.app, path+"/queue", base)
	if response.Code != http.StatusAccepted || !bytes.Contains(response.Body.Bytes(), []byte(`"queue_sequence":1`)) {
		t.Fatalf("queue pass status=%d body=%s", response.Code, response.Body.String())
	}
	s, r, a = f.revisions(turn)
	base["expected_session_revision"] = s
	base["expected_run_revision"] = r
	base["expected_turn_revision"] = a
	base["idempotency_key"] = "guard-interrupt-pass"
	delete(base, "content")
	response = guardRequest(f.app, path+"/interrupt", base)
	if response.Code != http.StatusAccepted {
		t.Fatalf("interrupt pass status=%d body=%s", response.Code, response.Body.String())
	}
	base["idempotency_key"] = "guard-approval-pass"
	base["approval"] = map[string]any{"approval_request_id": "approval-missing", "expected_approval_revision": 1, "decision": "deny", "scope": "once", "capability": "fixture.tool", "target": "fixture-target", "effect": "read_only"}
	response = guardRequest(f.app, path+"/approval", base)
	if response.Code != http.StatusNotFound || !bytes.Contains(response.Body.Bytes(), []byte(`"target_unavailable"`)) {
		t.Fatalf("approval did not reach original service status=%d body=%s", response.Code, response.Body.String())
	}
	var revision uint64
	if err := f.pool.QueryRow(ctx, `SELECT revision FROM tasks WHERE task_id=$1`, taskID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	f.post("/api/v1/tasks/"+taskID+"/cancel", map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "guard-cancel-pass", "expected_task_revision": revision})
	view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID)
	if !bytes.Contains(view, []byte(`"status":"cancelled"`)) {
		t.Fatalf("cancel did not commit public state=%s", view)
	}
}
