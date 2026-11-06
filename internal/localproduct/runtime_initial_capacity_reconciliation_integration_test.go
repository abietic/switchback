//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type pendingInitialProvisioner struct {
	*supplementProvisioner
	calls   int
	request workerapp.RuntimeProvisionRequest
}

func (p *pendingInitialProvisioner) Provision(_ context.Context, r workerapp.RuntimeProvisionRequest, _ workerapp.RuntimePreStartCheck) (workerapp.ProvisionedRuntime, error) {
	p.calls++
	p.request = r
	return workerapp.ProvisionedRuntime{}, fmt.Errorf("SYNTHETIC interrupted owner: %w", workerapp.InitialRuntimeStartReplayPending)
}
func newInterruptedInitialFixture(t *testing.T) (*supplementFixture, *pendingInitialProvisioner) {
	t.Helper()
	var p *pendingInitialProvisioner
	var env *recordedInitialEnvironment
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		env = &recordedInitialEnvironment{RuntimeEnvironmentPreparer: f.app.runtime.starts.Environment}
		f.app.runtime.starts.Environment = env
		f.runtime.heartbeatStart = make(chan struct{})
		p = &pendingInitialProvisioner{supplementProvisioner: f.provisioner}
		f.app.runtime.starts.Provisioner = p
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "interrupted-initial", "Keep exact original owner pending")
	err := f.app.runtime.startNextQueuedTask(f.ctx)
	if !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) {
		t.Fatalf("interrupted start=%v", err)
	}
	if p.calls != 1 || len(f.app.runtime.initializingSessionGenerations) != 0 {
		t.Fatal("initializer did not return and release process-local marker")
	}
	assertInitialSourcePinned(t, f, p.request.RuntimeInstanceID, env.request.SourcePlan)
	return f, p
}

// Historical fixtures stop the real coordinator before planning/authorization.
// They seed old storage/business state explicitly; they do not model an
// authorized current-source launch, and never erase a source pin.
type historicalInitialGenesis struct {
	t       *testing.T
	fixture *supplementFixture
	request workerapp.RuntimePreparationRequest
	calls   int
}

func (g *historicalInitialGenesis) PlanRuntime(_ context.Context, request workerapp.RuntimePreparationRequest) (workerapp.RuntimePreparationPlan, error) {
	assertInitialSourceUnpinned(g.t, g.fixture, request.Ownership.RuntimeInstanceID)
	g.request = request
	g.calls++
	assertInitialSourceUnpinned(g.t, g.fixture, request.Ownership.RuntimeInstanceID)
	return nil, workerapp.InitialRuntimeStartReplayPending
}

func assertInitialSourceUnpinned(t *testing.T, f *supplementFixture, id string) {
	t.Helper()
	if f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE runtime_id=$1 AND consumed_at IS NOT NULL AND source_plan_bytes IS NULL AND source_plan_digest IS NULL AND source_plan_bound_at IS NULL`, id) != 1 {
		t.Fatal("historical fixture must have one consumed execution with all source-pin columns NULL")
	}
}

func assertInitialSourcePinned(t *testing.T, f *supplementFixture, id string, binding workerapp.RuntimeSourcePlanBinding) {
	t.Helper()
	supplementMust(t, binding.Validate())
	if f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE runtime_id=$1 AND consumed_at IS NOT NULL AND source_plan_bytes=$2::bytea AND source_plan_digest=$3 AND source_plan_bound_at IS NOT NULL`, id, []byte(binding.Manifest), binding.Digest) != 1 {
		t.Fatal("current-source preparation did not pin the exact captured source manifest/digest")
	}
}

func seedHistoricalInitialOwner(t *testing.T, f *supplementFixture) (workerapp.RuntimePreparationRequest, workerapp.RuntimeProvisionRequest) {
	t.Helper()
	r := f.app.runtime
	genesis := &historicalInitialGenesis{t: t, fixture: f}
	original := r.starts.Environment
	r.starts.Environment = genesis
	err := r.startNextQueuedTask(f.ctx)
	r.starts.Environment = original
	if !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) || genesis.calls != 1 || !genesis.request.Ownership.Created {
		t.Fatalf("historical genesis did not stop before authorization: calls=%d owner=%+v err=%v", genesis.calls, genesis.request.Ownership, err)
	}
	assertInitialSourceUnpinned(t, f, genesis.request.Ownership.RuntimeInstanceID)
	// The private convenience builds only fixture storage/control credentials.
	// It is not installed as a coordinator or execution-authority substitute.
	request, err := r.environment.PrepareRuntime(f.ctx, genesis.request)
	supplementMust(t, err)
	assertInitialSourceUnpinned(t, f, request.RuntimeInstanceID)
	return genesis.request, request
}

func newHistoricalInterruptedInitialFixture(t *testing.T) (*supplementFixture, workerapp.RuntimeProvisionRequest) {
	t.Helper()
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		f.runtime.heartbeatStart = make(chan struct{})
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "historical-interrupted", "Inspect an explicitly unpinned historical owner")
	_, request := seedHistoricalInitialOwner(t, f)
	return f, request
}

func makeInitialOwnerHistorical(t *testing.T, f *supplementFixture, request workerapp.RuntimeProvisionRequest) {
	t.Helper()
	// Only a never-pinned execution can become a historical no-ledger graph.
	assertInitialSourceUnpinned(t, f, request.RuntimeInstanceID)
	_, err := f.pool.Exec(f.ctx, `DELETE FROM runtime_capacity_executions WHERE runtime_id=$1`, request.RuntimeInstanceID)
	supplementMust(t, err)
	_, err = f.pool.Exec(f.ctx, `DELETE FROM runtime_capacity_residents WHERE runtime_instance_id=$1`, request.RuntimeInstanceID)
	supplementMust(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE worker_runs SET start_snapshot=start_snapshot-'runtime_capacity' WHERE worker_run_id=$1`, request.WorkerRunID)
	supplementMust(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE session_dispatch_permits SET capacity_admission_ref='legacy-opaque-admission' WHERE consumed_by_worker_run_id=$1`, request.WorkerRunID)
	supplementMust(t, err)
}

func initialOwnershipState(t *testing.T, f *supplementFixture) []byte {
	t.Helper()
	var raw []byte
	// Complete business/control/ledger rows include original IDs, revisions,
	// deadline, permit, token, generation, resident charge and release markers.
	err := f.pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
 'tasks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY task_id) FROM tasks x),
 'sessions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY worker_session_id) FROM worker_sessions x),
 'runs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY worker_run_id) FROM worker_runs x),
 'turns',(SELECT jsonb_agg(to_jsonb(x) ORDER BY agent_turn_id) FROM agent_turns x),
 'runtimes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY runtime_instance_id) FROM runtime_instances x),
 'permits',(SELECT jsonb_agg(to_jsonb(x) ORDER BY permit_id) FROM session_dispatch_permits x),
 'executions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY token) FROM runtime_capacity_executions x),
 'residents',(SELECT jsonb_agg(to_jsonb(x) ORDER BY commitment_id) FROM runtime_capacity_residents x),
 'queues',(SELECT jsonb_agg(to_jsonb(x) ORDER BY task_id) FROM session_task_queue_entries x),
 'control',(SELECT jsonb_agg(to_jsonb(x) ORDER BY message_id) FROM runtime_control_messages x))`).Scan(&raw)
	supplementMust(t, err)
	return raw
}
func TestInterruptedInitialCapacityOwnerSurvivesDetectionAndStartup(t *testing.T) {
	f, p := newInterruptedInitialFixture(t)
	before := initialOwnershipState(t, f)
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{{"normal", f.app.runtime.detectRuntimeFailures}, {"startup", f.app.runtime.rejectUnownedRuntimes}} {
		err := step.run(f.ctx)
		if !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) {
			t.Fatalf("%s owner was not pending: %v", step.name, err)
		}
		if after := initialOwnershipState(t, f); !bytes.Equal(before, after) {
			t.Fatalf("%s changed original ownership", step.name)
		}
	}
	if f.runtime.inventory.inspectCalls != 0 || len(f.runtime.inventory.removes) != 0 || p.calls != 1 {
		t.Fatalf("pending caused external effects: inspections=%d removes=%v provisions=%d", f.runtime.inventory.inspectCalls, f.runtime.inventory.removes, p.calls)
	}
	if _, ok := f.app.runtime.environment.get(p.request.RuntimeInstanceID); !ok {
		t.Fatal("pending discarded prepared environment")
	}
	t.Log("lost initializing marker: normal+startup pending; exact Task/Session/Run/Turn/deadline/token/permit/generation/resident unchanged; zero Inspect/Remove/reprovision")
}
func TestInterruptedInitialCapacityCorruptBindingFailsClosed(t *testing.T) {
	for _, scenario := range []string{"missing_execution", "missing_snapshot", "wrong_runtime", "wrong_target"} {
		t.Run(scenario, func(t *testing.T) {
			var f *supplementFixture
			if scenario == "missing_execution" {
				var historical workerapp.RuntimeProvisionRequest
				f, historical = newHistoricalInterruptedInitialFixture(t)
				assertInitialSourceUnpinned(t, f, historical.RuntimeInstanceID)
			} else {
				f, _ = newInterruptedInitialFixture(t)
			}
			sql := map[string]string{"missing_execution": `DELETE FROM runtime_capacity_executions`, "missing_snapshot": `UPDATE worker_runs SET start_snapshot=start_snapshot-'runtime_capacity'`, "wrong_runtime": `UPDATE runtime_capacity_executions SET runtime_id='other-runtime'`, "wrong_target": `UPDATE runtime_capacity_executions SET target_digest='wrong'`}[scenario]
			_, err := f.pool.Exec(f.ctx, sql)
			supplementMust(t, err)
			for _, step := range []func(context.Context) error{f.app.runtime.detectRuntimeFailures, f.app.runtime.rejectUnownedRuntimes} {
				if err := step(f.ctx); !errors.Is(err, workerapp.CapacityBindingMismatch) {
					t.Fatalf("corrupt owner downgraded: %v", err)
				}
			}
			if f.runtime.inventory.inspectCalls != 0 || len(f.runtime.inventory.removes) != 0 {
				t.Fatal("corrupt owner acquired cleanup authority")
			}
		})
	}
}
func TestInterruptedInitialCapacityLegacyStillInspects(t *testing.T) {
	f, historical := newHistoricalInterruptedInitialFixture(t)
	// Explicit historical no-ledger business graph, never verified Engine facts.
	makeInitialOwnerHistorical(t, f, historical)
	sentinel := errors.New("SYNTHETIC legacy Inspect reached")
	f.runtime.inventory.inspectionError = sentinel
	if err := f.app.runtime.detectRuntimeFailures(f.ctx); !errors.Is(err, sentinel) {
		t.Fatalf("legacy did not reach existing inspection: %v", err)
	}
	if f.runtime.inventory.inspectCalls != 1 {
		t.Fatal("legacy inspection count changed")
	}
}

type fixedStagingWaitPolicy struct{ err error }

func (p fixedStagingWaitPolicy) checkRuntimeStart(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return p.err
}
func TestTypedCapacityWaitScannerDeliversUnrelatedReport(t *testing.T) {
	f, turns, queues, task := newCapacitySettlementFixture(t, 1)
	cause := fmt.Errorf("next admission pending: %w", workerapp.CapacityPathNotIntegrated)
	f.app.runtime.startPolicy = fixedStagingWaitPolicy{cause}
	_, found, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx)
	if !found || err != cause {
		t.Fatalf("scan lost cause: found=%t error=%v", found, err)
	}
	assertCapacitySettlementReport(t, f, turns[1], task)
	assertCapacitySettlementWaiting(t, f, turns[:1], queues)
	// The existing source checkpoint still owns its queue head; no successor.
	s, err := f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turns[0].start.Envelope.AgentTurnID))
	supplementMust(t, err)
	var cp runtimeTurnSettlementCheckpoint
	supplementMust(t, json.Unmarshal(s.PhaseState, &cp))
	if cp.ContinuationResult != nil {
		t.Fatal("wait fabricated continuation")
	}
}

func TestActivatedInitialCapacityStillConvergesRealInspectedLoss(t *testing.T) {
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		f.runtime.heartbeatStart = make(chan struct{})
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "activated-loss", "Observe established synthetic container loss")
	turn, callback := f.next()
	f.send(callback)
	id := string(turn.start.Envelope.RuntimeInstanceID)
	f.runtime.inventory.mu.Lock()
	request := f.runtime.inventory.requests[id]
	delete(f.runtime.inventory.requests, id)
	f.runtime.inventory.mu.Unlock()
	supplementMust(t, f.app.runtime.detectRuntimeFailures(f.ctx))
	assertCapacityInitializationFailure(t, f, request)
	f.runtime.inventory.mu.Lock()
	inspects := f.runtime.inventory.inspectCalls
	f.runtime.inventory.mu.Unlock()
	if inspects == 0 {
		t.Fatal("activated owner loss was masked")
	}
	var released bool
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT released_at IS NOT NULL FROM runtime_capacity_executions WHERE runtime_id=$1`, id).Scan(&released))
	if !released {
		t.Fatal("real no-recovery failure did not settle initial execution")
	}
	if n := f.count(`SELECT count(*) FROM runtime_instances WHERE runtime_instance_id<>$1`, id); n != 0 {
		t.Fatal("loss created replacement")
	}
	t.Log("signed actual CID activated via production path; synthetic Inspect missing converged no-replacement failure; resident retained")
}

func TestInterruptedInitialCapacityDoesNotStopUnrelatedDetection(t *testing.T) {
	f, p := newInterruptedInitialFixture(t)
	first := p.request.RuntimeInstanceID
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "second-owner", "Establish independent legacy inspection")
	_, historical := seedHistoricalInitialOwner(t, f)
	second := historical.RuntimeInstanceID
	if first == second {
		t.Fatal("fixture did not establish independent owner")
	}
	// Only the new, never-pinned owner is historical; the first stays pinned.
	makeInitialOwnerHistorical(t, f, historical)
	sentinel := errors.New("unrelated legacy inspection reached after pending owner")
	f.runtime.inventory.inspectionError = sentinel
	if err := f.app.runtime.detectRuntimeFailures(f.ctx); !errors.Is(err, sentinel) {
		t.Fatalf("pending stopped unrelated observer: %v", err)
	}
	if f.runtime.inventory.inspectCalls != 1 {
		t.Fatalf("inspection calls=%d", f.runtime.inventory.inspectCalls)
	}
	if err := f.app.runtime.interruptedInitialRuntime(f.ctx, first); !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) {
		t.Fatalf("first owner changed: %v", err)
	}
}

type recordedInitialEnvironment struct {
	workerapp.RuntimeEnvironmentPreparer
	request workerapp.RuntimePreparationRequest
	calls   int
}

func (e *recordedInitialEnvironment) PlanRuntime(ctx context.Context, r workerapp.RuntimePreparationRequest) (workerapp.RuntimePreparationPlan, error) {
	plan, err := e.RuntimeEnvironmentPreparer.PlanRuntime(ctx, r)
	if err != nil || plan == nil {
		return plan, err
	}
	return &recordedInitialPreparationPlan{RuntimePreparationPlan: plan, environment: e, request: r}, nil
}

type recordedInitialPreparationPlan struct {
	workerapp.RuntimePreparationPlan
	environment *recordedInitialEnvironment
	request     workerapp.RuntimePreparationRequest
}

func (p *recordedInitialPreparationPlan) Prepare(ctx context.Context) (workerapp.RuntimeProvisionRequest, error) {
	p.environment.calls++
	p.environment.request = p.request
	p.environment.request.SourcePlan = p.SourceBinding()
	return p.RuntimePreparationPlan.Prepare(ctx)
}
func TestInterruptedInitialCoordinatorReplayRetainsRunnerEnvironment(t *testing.T) {
	var p *pendingInitialProvisioner
	var env *recordedInitialEnvironment
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		env = &recordedInitialEnvironment{RuntimeEnvironmentPreparer: f.app.runtime.starts.Environment}
		f.app.runtime.starts.Environment = env
		p = &pendingInitialProvisioner{supplementProvisioner: f.provisioner}
		f.app.runtime.starts.Provisioner = p
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "coordinator-replay", "Retain original owner")
	if err := f.app.runtime.startNextQueuedTask(f.ctx); !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) {
		t.Fatal(err)
	}
	before := initialOwnershipState(t, f)
	replay, err := f.app.runtime.starts.Start(f.ctx, workerapp.StartRuntimeCommand{Run: env.request.Command})
	if !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) || replay.Ownership.Created || replay.Ownership.RuntimeInstanceID != p.request.RuntimeInstanceID || env.calls != 1 || p.calls != 1 {
		t.Fatalf("replay=%+v error=%v prepares=%d provisions=%d", replay, err, env.calls, p.calls)
	}
	if got := f.app.runtime.reconcileFailedRuntimeStart(f.ctx, replay.Ownership, err); got != err {
		t.Fatalf("replay cause changed: %v", got)
	}
	if !bytes.Equal(before, initialOwnershipState(t, f)) {
		t.Fatal("replay changed original ledger or ownership")
	}
	if _, ok := f.app.runtime.environment.get(p.request.RuntimeInstanceID); !ok {
		t.Fatal("replay discarded original prepared state")
	}
}

func TestInitialCapacityMissingCIDProofRetainsPreparedOwner(t *testing.T) {
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		// Provision still produces an actual synthetic CID and runtime.ready.
		// Deliberately leave the signed inventory without that CID.
		f.publishInitial = nil
		f.runtime.heartbeatStart = make(chan struct{})
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "missing-cid-proof", "Retain original prepared owner until evidence exists")
	if err := f.app.runtime.startNextQueuedTask(f.ctx); !errors.Is(err, workerapp.CapacityContainerProofPending) {
		t.Fatalf("missing actual CID proof=%v", err)
	}
	var runtimeID string
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT runtime_instance_id FROM runtime_instances`).Scan(&runtimeID))
	before := initialOwnershipState(t, f)
	for _, step := range []func(context.Context) error{f.app.runtime.detectRuntimeFailures, f.app.runtime.rejectUnownedRuntimes} {
		if err := step(f.ctx); !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) {
			t.Fatalf("missing CID evidence acquired failure authority: %v", err)
		}
		if !bytes.Equal(before, initialOwnershipState(t, f)) {
			t.Fatal("missing CID evidence changed original ownership or deadline")
		}
	}
	f.runtime.inventory.mu.Lock()
	inspects, removals, provisions := f.runtime.inventory.inspectCalls, len(f.runtime.inventory.removes), len(f.runtime.inventory.requests)
	f.runtime.inventory.mu.Unlock()
	if inspects != 0 || removals != 0 || provisions != 1 {
		t.Fatalf("missing proof caused effects: inspections=%d removals=%d provisioned=%d", inspects, removals, provisions)
	}
	if _, ok := f.app.runtime.environment.get(runtimeID); !ok {
		t.Fatal("missing CID proof discarded prepared WorkingCopy/control state")
	}
	if f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 0 {
		t.Fatal("missing CID proof dispatched a Prompt")
	}
	t.Log("actual synthetic CID/ready retained without signed CID inventory; original graph/deadline preserved, one Provision, zero Inspect/Remove/Prompt")
}

type refusedInitialCleaner struct {
	calls     int
	err       error
	runtimeID string
}

func (c *refusedInitialCleaner) Cleanup(_ context.Context, r workerapp.RuntimeCleanupRequest) (workerapp.RuntimeCleanupObservation, error) {
	c.calls++
	c.runtimeID = r.RuntimeInstanceID
	return workerapp.RuntimeCleanupObservation{}, c.err
}

func TestInterruptedInitialStartupStillAttemptsUnrelatedLegacyCleanup(t *testing.T) {
	f, p := newInterruptedInitialFixture(t)
	held := p.request.RuntimeInstanceID
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "unrelated-startup", "Keep startup reconciliation independent")
	_, historical := seedHistoricalInitialOwner(t, f)
	legacy := historical.RuntimeInstanceID
	if held == legacy {
		t.Fatal("fixture did not establish independent owner")
	}
	makeInitialOwnerHistorical(t, f, historical)
	sentinel := errors.New("SYNTHETIC unrelated legacy cleanup reached")
	cleaner := &refusedInitialCleaner{err: sentinel}
	f.app.runtime.reconciler.Cleaner = cleaner
	if err := f.app.runtime.rejectUnownedRuntimes(f.ctx); !errors.Is(err, sentinel) {
		t.Fatalf("startup swallowed hard legacy failure: %v", err)
	}
	if cleaner.calls != 1 || cleaner.runtimeID != legacy {
		t.Fatalf("startup cleaned wrong owner: %+v", cleaner)
	}
	if err := f.app.runtime.interruptedInitialRuntime(f.ctx, held); !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) {
		t.Fatalf("held owner changed: %v", err)
	}
}

func TestTypedCapacityWaitScannerKeepsLaterSameSessionBehindHead(t *testing.T) {
	f, turns, queues, task := newCapacitySettlementFixture(t, 1)
	headTurn := string(turns[0].start.Envelope.AgentTurnID)
	// Historical terminal Turn and a strictly later pending deadline settlement.
	// This ordering-only fixture grants no execution or physical authority. Its
	// unusable phase evidence must never be inspected ahead of this Session's head.
	_, err := f.pool.Exec(f.ctx, `INSERT INTO agent_turns SELECT (jsonb_populate_record(NULL::agent_turns,to_jsonb(t)||jsonb_build_object('agent_turn_id','later-same-session','idempotency_key','later-same-session','created_at',created_at+interval '1 microsecond','status','completed','terminal_at',clock_timestamp(),'terminal_reason','completed'))).* FROM agent_turns t WHERE agent_turn_id=$1`, headTurn)
	supplementMust(t, err)
	_, err = f.pool.Exec(f.ctx, `WITH ordered AS (
 SELECT head.*,head.accepted_at+(report.accepted_at-head.accepted_at)/2 AS later_at
 FROM worker_turn_settlements head CROSS JOIN worker_turn_settlements report
 WHERE head.agent_turn_id=$1 AND report.agent_turn_id=$2)
 INSERT INTO worker_turn_settlements SELECT (jsonb_populate_record(NULL::worker_turn_settlements,to_jsonb(s)||jsonb_build_object(
 'settlement_id','later-same-session','agent_turn_id','later-same-session',
 'source_kind','backend_deadline','source_ref','SYNTHETIC/later-same-session',
 'runtime_instance_id',NULL,'runtime_generation',NULL,'channel_epoch',NULL,
 'terminal_event_direction',NULL,'terminal_event_sequence',NULL,'terminal_message_id',NULL,
 'terminal_idempotency_key',NULL,'terminal_envelope_digest',NULL,
 'phase','accepted','phase_state','{}'::jsonb,'revision',1,
 'lease_owner_id',NULL,'lease_fence',0,'lease_expires_at',NULL,
 'terminal_event_disposition',NULL,'terminal_apply_outcome_digest',NULL,
 'terminal_event_applied_at',NULL,'completed_at',NULL,
 'accepted_at',later_at,'updated_at',later_at))).* FROM ordered s`, headTurn, string(turns[1].start.Envelope.AgentTurnID))
	supplementMust(t, err)
	var ordered bool
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT head.accepted_at<later.accepted_at AND later.accepted_at<report.accepted_at
 FROM worker_turn_settlements head CROSS JOIN worker_turn_settlements later CROSS JOIN worker_turn_settlements report
 WHERE head.agent_turn_id=$1 AND later.settlement_id='later-same-session' AND report.agent_turn_id=$2`, headTurn, string(turns[1].start.Envelope.AgentTurnID)).Scan(&ordered))
	if !ordered {
		t.Fatal("fixture did not establish head < later same-Session settlement < unrelated report")
	}
	t.Log("owner trace: strict acceptance order head < later same-Session settlement < unrelated report")
	var before []byte
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT jsonb_build_object('settlement',to_jsonb(s),'turn',to_jsonb(t)) FROM worker_turn_settlements s JOIN agent_turns t USING(agent_turn_id) WHERE settlement_id='later-same-session'`).Scan(&before))
	cause := fmt.Errorf("admission staging: %w", workerapp.CapacityEvidenceStale)
	f.app.runtime.startPolicy = fixedStagingWaitPolicy{cause}
	_, _, err = f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx)
	if err != cause {
		t.Fatalf("same Session overtook head or cause changed: %v", err)
	}
	assertCapacitySettlementReport(t, f, turns[1], task)
	assertCapacitySettlementWaiting(t, f, turns[:1], queues, "later-same-session")
	var after []byte
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT jsonb_build_object('settlement',to_jsonb(s),'turn',to_jsonb(t)) FROM worker_turn_settlements s JOIN agent_turns t USING(agent_turn_id) WHERE settlement_id='later-same-session'`).Scan(&after))
	if !bytes.Equal(before, after) {
		t.Fatal("later same-Session settlement changed ahead of its head")
	}
	t.Log("typed wait retained original head; later same-Session settlement whole row unchanged; unrelated report delivered")
}

// Each fault changes exactly one relationship in an otherwise production-reached
// graph, then restores it. The helper must refuse effects, not call it legacy.
func assertInitialLifecycleCorruptionDenied(t *testing.T, f *supplementFixture, phase string) {
	t.Helper()
	type fault struct{ name, breakSQL, repairSQL string }
	faults := []fault{
		{"wrong_tenant", `UPDATE runtime_capacity_executions SET tenant_id='wrong-tenant'`, `UPDATE runtime_capacity_executions e SET tenant_id=s.tenant_id FROM worker_sessions s WHERE s.worker_session_id=e.worker_session_id`},
		{"wrong_actor", `UPDATE worker_sessions SET user_id='wrong-actor'`, `UPDATE worker_sessions s SET user_id=t.initiator_user_id FROM tasks t WHERE t.worker_session_id=s.worker_session_id`},
		{"wrong_cid", `UPDATE runtime_capacity_residents SET container_id='wrong-cid'`, `UPDATE runtime_capacity_residents c SET container_id=i.container_id FROM runtime_instances i WHERE i.runtime_instance_id=c.runtime_instance_id`},
		{"wrong_generation", `UPDATE runtime_capacity_residents SET generation=generation+1`, `UPDATE runtime_capacity_residents SET generation=generation-1`},
	}
	if phase == "consumed_running" {
		faults = append(faults, fault{"idle_session_running", `UPDATE worker_sessions SET status='idle'`, `UPDATE worker_sessions SET status='running'`}, fault{"released_nonterminal", `UPDATE runtime_capacity_executions SET released_at=clock_timestamp(),release_reason='turn_terminal'`, `UPDATE runtime_capacity_executions SET released_at=NULL,release_reason=NULL`},
			fault{"unbound_running", `UPDATE worker_sessions SET bound_task_id=NULL`, `UPDATE worker_sessions s SET bound_task_id=t.task_id FROM tasks t WHERE t.worker_session_id=s.worker_session_id`})
	} else {
		faults = append(faults, fault{"nonterminal_released_turn", `UPDATE agent_turns SET status='delivered',terminal_at=NULL`, `UPDATE agent_turns t SET status='completed',terminal_at=r.terminal_at FROM worker_runs r WHERE r.worker_run_id=t.worker_run_id`},
			fault{"running_session_terminal", `UPDATE worker_sessions SET status='running'`, `UPDATE worker_sessions SET status='interactive'`},
			fault{"wrong_release_reason", `UPDATE runtime_capacity_executions SET release_reason='permit_expired'`, `UPDATE runtime_capacity_executions SET release_reason='turn_terminal'`},
			fault{"active_terminal_run", `UPDATE worker_sessions s SET active_worker_run_id=t.current_worker_run_id FROM tasks t WHERE t.worker_session_id=s.worker_session_id`, `UPDATE worker_sessions SET active_worker_run_id=NULL`})
		if phase == "terminal_delivered" {
			faults = append(faults, fault{"unbound_delivered", `UPDATE worker_sessions SET bound_task_id=NULL`, `UPDATE worker_sessions s SET bound_task_id=t.task_id FROM tasks t WHERE t.worker_session_id=s.worker_session_id`})
		}
		if phase == "accepted_completed" {
			faults = append(faults, fault{"bound_manual_completed", `UPDATE worker_sessions s SET bound_task_id=t.task_id FROM tasks t WHERE t.worker_session_id=s.worker_session_id`, `UPDATE worker_sessions SET bound_task_id=NULL`})
		}
	}
	var runtimeID string
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT runtime_instance_id FROM runtime_instances`).Scan(&runtimeID))
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			before := initialOwnershipState(t, f)
			_, err := f.pool.Exec(f.ctx, fault.breakSQL)
			supplementMust(t, err)
			corrupt := initialOwnershipState(t, f)
			got := f.app.runtime.interruptedInitialRuntime(f.ctx, runtimeID)
			if !errors.Is(got, workerapp.CapacityBindingMismatch) {
				t.Errorf("phase=%s fault=%s got=%v", phase, fault.name, got)
			}
			if !bytes.Equal(corrupt, initialOwnershipState(t, f)) {
				t.Error("corrupt hold changed graph")
			}
			_, err = f.pool.Exec(f.ctx, fault.repairSQL)
			supplementMust(t, err)
			if !bytes.Equal(before, initialOwnershipState(t, f)) {
				t.Fatal("fault repair did not restore exact production rows")
			}
		})
	}
}

// The production source has already committed ObservePool before this store
// boundary. Refusing Activate models the persistent transaction gap without
// fabricating a resident pin or granting an execution recovery capability.
type stopBeforeInitialActivation struct {
	workerapp.RuntimeResidencyStore
	calls int
}

func (s *stopBeforeInitialActivation) ActivateResidentRuntime(context.Context, workerapp.ActivateResidentRuntimeRecord) (workerapp.ResidentRuntimeState, error) {
	s.calls++
	return workerapp.ResidentRuntimeState{}, workerapp.InitialRuntimeStartReplayPending
}

func TestInitialCapacityObservedPinBeforeActivationColdHold(t *testing.T) {
	inventory := runtimecapacity.NewFileInventory(t)
	var prepared *countedInitialPreparation
	var activation *stopBeforeInitialActivation
	f := newSupplementFixtureWithOptions(t, supplementFixtureOptions{
		productionInitial: true,
	}, func(f *supplementFixture) {
		installSyntheticInitialCapacitySource(f, inventory)
		f.manualInitial = true
		f.runtime.heartbeatStart = make(chan struct{})
		f.publishInitial = func(r workerapp.RuntimeProvisionRequest) {
			inventory.Observe(r.WorkerSessionID, r.RuntimeInstanceID, "fixture-"+r.RuntimeInstanceID, r.Generation)
		}
		prepared = &countedInitialPreparation{RuntimeEnvironmentPreparer: f.app.runtime.starts.Environment}
		f.app.runtime.starts.Environment = prepared
		activation = &stopBeforeInitialActivation{RuntimeResidencyStore: f.app.runtime.residency.Store}
		f.app.runtime.residency.Store = activation
	})
	r := f.app.runtime
	if _, ok := r.initialPolicy.(syntheticInitialCapacitySource); !ok || f.startPolicy != nil || r.configuration.RuntimeRecoveryEnabled {
		t.Fatal("fixture did not keep explicit synthetic source and default recovery-disabled path")
	}
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "observed-before-activate", "Retain original signed resident pin")
	if err := r.startNextQueuedTask(f.ctx); singleRuntimeCause(err) != workerapp.InitialRuntimeStartReplayPending {
		t.Fatalf("pre-Activate boundary=%v", err)
	}
	if activation.calls != 1 || prepared.calls.Load() != 1 {
		t.Fatalf("unexpected initial calls: Activate=%d Prepare=%d", activation.calls, prepared.calls.Load())
	}
	// Join every fixture owner before checking immutable state after map loss.
	f.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.ctx = ctx
	var id, residentCID string
	var pinnedStarting bool
	supplementMust(t, f.pool.QueryRow(ctx, `SELECT i.runtime_instance_id,c.container_id,
 i.status='starting' AND i.container_id IS NULL AND c.origin='reserved'
 AND c.container_id='fixture-'||i.runtime_instance_id AND c.removal_sequence IS NULL
 AND c.layer_project_id IS NOT NULL AND e.consumed_at IS NOT NULL AND e.released_at IS NULL
 AND r.status='starting' AND t.status='delivered'
 FROM runtime_instances i JOIN runtime_capacity_residents c USING(runtime_instance_id)
 JOIN runtime_capacity_executions e ON e.commitment_id=c.commitment_id
 JOIN worker_runs r ON r.worker_run_id=e.run_id JOIN agent_turns t ON t.agent_turn_id=e.turn_id`).Scan(&id, &residentCID, &pinnedStarting))
	if !pinnedStarting {
		t.Fatal("production ObservePool did not commit exact pin before Runtime Activate")
	}
	original, ok := r.environment.get(id)
	if !ok {
		t.Fatal("initial preparation missing")
	}
	r.mu.Lock()
	r.runtimes = make(map[string]*liveRuntime)
	r.pendingInitialActivations = nil
	r.initializingSessionGenerations = nil
	r.mu.Unlock()
	r.environment.mu.Lock()
	r.environment.prepared = make(map[string]preparedRuntime)
	r.environment.mu.Unlock()
	before := initialOwnershipState(t, f)
	businessBefore := initialExperienceState(f)
	t.Logf("production ObservePool committed; stopped before Activate; Runtime=%s residentCID=%s original graph=%s", id, residentCID, before)
	f.runtime.inventory.mu.Lock()
	inspectBefore, removeBefore, provisionBefore := f.runtime.inventory.inspectCalls, len(f.runtime.inventory.removes), len(f.runtime.inventory.requests)
	f.runtime.inventory.mu.Unlock()
	for _, guard := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"helper", func(ctx context.Context) error { return r.interruptedInitialRuntime(ctx, id) }},
		{"unowned", r.rejectUnownedRuntimes},
		{"failure", r.detectRuntimeFailures},
	} {
		err := guard.run(ctx)
		t.Logf("%s boundary=%v", guard.name, err)
		if singleRuntimeCause(err) != workerapp.InitialRuntimeStartReplayPending || !runtimeStagingWait(err) {
			t.Errorf("legitimate observed starting owner hard-refused by %s: %v", guard.name, err)
		}
	}
	// Starting/no Runtime CID is deliberately ineligible for active restore.
	candidates, err := r.loadStartupRuntimeCandidates(ctx)
	supplementMust(t, err)
	if len(candidates) != 0 {
		t.Errorf("starting owner became active restore candidate: %+v", candidates)
	}
	restored, err := r.restoreStartupRuntimes(ctx, startupSettlementQuarantine{})
	if err != nil || len(restored) != 0 {
		t.Errorf("aggregate startup=%v restored=%d", err, len(restored))
	}
	adopted, err := r.restoreUnownedRuntimeByID(ctx, id)
	if err != nil || adopted != nil {
		t.Errorf("starting adoption=%v adopted=%v", err, adopted)
	}
	if !bytes.Equal(before, initialOwnershipState(t, f)) || !bytes.Equal(businessBefore, initialExperienceState(f)) {
		t.Error("hold changed original business/control/ledger/queue/owner rows")
	}
	if _, ok := r.environment.get(id); ok || prepared.calls.Load() != 1 || activation.calls != 1 {
		t.Error("hold prepared/restored/activated original owner")
	}
	f.runtime.inventory.mu.Lock()
	inspect, remove, provision := f.runtime.inventory.inspectCalls, len(f.runtime.inventory.removes), len(f.runtime.inventory.requests)
	f.runtime.inventory.mu.Unlock()
	if inspect != inspectBefore || remove != removeBefore || provision != provisionBefore || provisionBefore != 1 || removeBefore != 0 || len(f.runtime.starts) != 0 {
		t.Errorf("hold effect deltas Inspect=%d Remove=%d Provision=%d pending Prompt=%d", inspect-inspectBefore, remove-removeBefore, provision-provisionBefore, len(f.runtime.starts))
	}
	if f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 0 {
		t.Error("hold dispatched Prompt")
	}
	if _, err := os.Stat(original.Storage.WorkingCopy.BackendPath); err != nil {
		t.Error("hold lost retained WorkingCopy", err)
	}
	if !t.Failed() {
		t.Log("read-only hold after map loss: original IDs/token/generation/permit/deadline/slot/queue/resident/retained unchanged; extra Prepare/Restore/Activate/Inspect/Provision/Prompt/Remove/release=0; no physical restart or execution recovery claimed")
	}
}
