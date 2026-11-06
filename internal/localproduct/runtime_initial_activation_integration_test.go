//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// Publishes the actual CID only after runtime.ready has been bound, i.e. after
// Provision returned. This cannot be passed by a second scheduling tick.
func TestInitialActivationLateSignedCIDSameInitializer(t *testing.T) {
	var files *runtimecapacity.FileInventory
	var request workerapp.RuntimeProvisionRequest
	f := newSupplementFixture(t, func(f *supplementFixture) {
		files = enableInitialCapacityFixture(f)
		f.publishInitial = func(r workerapp.RuntimeProvisionRequest) { request = r }
		f.runtime.heartbeatStart = make(chan struct{})
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "late-signed-cid", "Use actual signed CID")
	original := f.app.runtime.residency.Store
	f.app.runtime.residency.Store = &afterInitialActivation{RuntimeResidencyStore: original, after: func(err error) {
		if !errors.Is(err, workerapp.CapacityContainerProofPending) {
			t.Fatalf("expected original Activate missing CID: %v", err)
		}
		files.Observe(request.WorkerSessionID, request.RuntimeInstanceID, "fixture-"+request.RuntimeInstanceID, request.Generation)
	}}
	supplementMust(t, f.app.runtime.startNextQueuedTask(f.ctx))
	if f.count(`SELECT count(*) FROM runtime_instances WHERE status='active'`) != 1 {
		t.Fatal("same initializer did not activate")
	}
	if f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 0 {
		t.Fatal("Prompt occurred during initialization")
	}
}

type afterInitialActivation struct {
	workerapp.RuntimeResidencyStore
	after func(error)
}

func (s *afterInitialActivation) ActivateResidentRuntime(ctx context.Context, r workerapp.ActivateResidentRuntimeRecord) (workerapp.ResidentRuntimeState, error) {
	v, err := s.RuntimeResidencyStore.ActivateResidentRuntime(ctx, r)
	if s.after != nil {
		f := s.after
		s.after = nil
		f(err)
	}
	return v, err
}

func TestInitialActivationPendingExplicitRetrySameOwner(t *testing.T) {
	var files *runtimecapacity.FileInventory
	var request workerapp.RuntimeProvisionRequest
	f := newSupplementFixture(t, func(f *supplementFixture) {
		files = enableInitialCapacityFixture(f)
		f.publishInitial = func(r workerapp.RuntimeProvisionRequest) { request = r }
		f.runtime.heartbeatStart = make(chan struct{})
	})
	prepared := &countedInitialPreparation{RuntimeEnvironmentPreparer: f.app.runtime.starts.Environment}
	f.app.runtime.starts.Environment = prepared
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "pending-signed-cid", "Retain original owner")
	if err := f.app.runtime.startNextQueuedTask(f.ctx); !errors.Is(err, workerapp.CapacityContainerProofPending) {
		t.Fatal(err)
	}
	before := initialOwnershipState(t, f)
	r := f.app.runtime
	r.mu.Lock()
	pending := r.pendingInitialActivations[request.RuntimeInstanceID]
	count := len(r.initializingSessionGenerations)
	r.mu.Unlock()
	if pending == nil || count != 0 {
		t.Fatal("pending owner or initializer handoff missing")
	}
	original := pending.command
	for _, scan := range []func(context.Context) error{r.detectRuntimeFailures, r.rejectUnownedRuntimes} {
		if err := scan(f.ctx); !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(before, initialOwnershipState(t, f)) {
		t.Fatal("pending scan mutated original identities/deadline")
	}
	files.Observe(request.WorkerSessionID, request.RuntimeInstanceID, "fixture-"+request.RuntimeInstanceID, request.Generation)
	// Block the first owner inside real Activate IO, then prove a competing
	// explicit phase can take runner.mu and returns without another Activate.
	entered, release := make(chan struct{}), make(chan struct{})
	r.residency.Store = &blockedInitialActivation{RuntimeResidencyStore: r.residency.Store, entered: entered, release: release}
	done := make(chan error, 1)
	go func() { done <- r.reconcileInitialActivation(f.ctx) }()
	supplementReceive(t, f.ctx, entered)
	secondErr := r.reconcileInitialActivation(f.ctx)
	close(release)
	firstErr := supplementReceive(t, f.ctx, done)
	supplementMust(t, secondErr)
	supplementMust(t, firstErr)
	if prepared.calls.Load() != 1 {
		t.Fatal("prepared original owner more than once")
	}
	if pending.command != original {
		t.Fatal("retry changed original activation revisions")
	}
	r.mu.Lock()
	remaining := len(r.pendingInitialActivations)
	live := r.runtimes[request.RuntimeInstanceID]
	r.mu.Unlock()
	if remaining != 0 || live == nil {
		t.Fatal("pending handoff did not finish")
	}
	if _, ok := r.environment.get(request.RuntimeInstanceID); !ok {
		t.Fatal("prepared owner discarded")
	}
	f.runtime.inventory.mu.Lock()
	inspects, removes, provisions := f.runtime.inventory.inspectCalls, len(f.runtime.inventory.removes), len(f.runtime.inventory.requests)
	f.runtime.inventory.mu.Unlock()
	if inspects != 0 || removes != 0 || provisions != 1 {
		t.Fatalf("repeated effects %d %d %d", inspects, removes, provisions)
	}
	if f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE consumed_at IS NOT NULL AND released_at IS NULL`) != 1 || f.count(`SELECT count(*) FROM agent_turns`) != 1 || f.count(`SELECT count(*) FROM worker_runs`) != 1 {
		t.Fatal("original execution was duplicated")
	}
	turn, callback := f.next()
	f.send(callback)
	if f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 1 {
		t.Fatal("not one logical Prompt")
	}
	f.finish(turn, callback)
	f.settled(turn)
}

// Inject a hard binding/cancellation failure only after real synthetic
// Provision and valid runtime.ready. No container absence can be inferred here.
type failedInitialBinding struct {
	workerapp.AgentSessionBindingStore
	fail func() error
}

func (s failedInitialBinding) BindAgentSession(context.Context, workerapp.BindAgentSessionRecord) (workerapp.BoundAgentSession, error) {
	return workerapp.BoundAgentSession{}, s.fail()
}

func TestInitialActivationPostProvisionHardFailureRetainsCommitment(t *testing.T) {
	for _, name := range []string{"binding", "cancellation", "ready"} {
		t.Run(name, func(t *testing.T) {
			f := newSupplementFixture(t, func(f *supplementFixture) {
				enableInitialCapacityFixture(f)
				f.runtime.heartbeatStart = make(chan struct{})
			})
			r := f.app.runtime
			createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "held-"+name, "Hold physical owner on hard failure")
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			wanted := errors.New("synthetic hard binding failure")
			r.binding.Store = failedInitialBinding{AgentSessionBindingStore: r.binding.Store, fail: func() error {
				if name == "cancellation" {
					cancel()
					return context.Canceled
				}
				return wanted
			}}
			if name == "ready" {
				f.runtime.readyTransform = func(p *runtimeReadyPayload) { p.AgentProfileID = "wrong-profile" }
			}
			err := r.startNextQueuedTask(ctx)
			if name == "cancellation" {
				wanted = context.Canceled
			}
			if name == "ready" {
				if err == nil || !strings.Contains(err.Error(), "runtime.ready identity or profile is invalid") || runtimeStagingWait(err) {
					t.Fatalf("invalid ready not held: %v", err)
				}
			} else if !errors.Is(err, wanted) || runtimeStagingWait(err) {
				t.Fatalf("hard failure demoted: %v", err)
			}
			var runtime string
			supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT runtime_instance_id FROM runtime_instances`).Scan(&runtime))
			if _, ok := r.environment.get(runtime); !ok {
				t.Fatal("post-Provision owner discarded")
			}
			supplementReceive(t, f.ctx, f.provisioner.stop())
			before := initialOwnershipState(t, f)
			for _, scan := range []func(context.Context) error{r.detectRuntimeFailures, r.rejectUnownedRuntimes} {
				if err := scan(f.ctx); !errors.Is(err, workerapp.InitialRuntimeStartReplayPending) {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(before, initialOwnershipState(t, f)) {
				var a, b map[string]any
				json.Unmarshal(before, &a)
				json.Unmarshal(initialOwnershipState(t, f), &b)
				var changed []string
				for key, v := range a {
					if !reflect.DeepEqual(v, b[key]) {
						changed = append(changed, key)
					}
				}
				t.Fatalf("hard hold changed durable sections: %v", changed)
			}
			f.runtime.inventory.mu.Lock()
			inspect, remove, provision := f.runtime.inventory.inspectCalls, len(f.runtime.inventory.removes), len(f.runtime.inventory.requests)
			f.runtime.inventory.mu.Unlock()
			if inspect != 0 || remove != 0 || provision != 1 || f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE consumed_at IS NOT NULL AND released_at IS NULL`) != 1 || f.count(`SELECT count(*) FROM runtime_capacity_residents WHERE removal_sequence IS NULL`) != 1 {
				t.Fatal("hard failure released/repeated physical commitment")
			}
		})
	}
}

func TestInitialActivationCorruptOriginalGraphHardRefused(t *testing.T) {
	var files *runtimecapacity.FileInventory
	var request workerapp.RuntimeProvisionRequest
	f := newSupplementFixture(t, func(f *supplementFixture) {
		files = enableInitialCapacityFixture(f)
		f.publishInitial = func(r workerapp.RuntimeProvisionRequest) { request = r }
		f.runtime.heartbeatStart = make(chan struct{})
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "corrupt-pending", "Original graph remains authority")
	r := f.app.runtime
	if err := r.startNextQueuedTask(f.ctx); !errors.Is(err, workerapp.CapacityContainerProofPending) {
		t.Fatal(err)
	}
	files.Observe(request.WorkerSessionID, request.RuntimeInstanceID, "fixture-"+request.RuntimeInstanceID, request.Generation)
	_, err := f.pool.Exec(f.ctx, `UPDATE worker_runs SET start_snapshot=start_snapshot-'runtime_capacity'`)
	supplementMust(t, err)
	err = r.reconcileInitialActivation(f.ctx)
	if !errors.Is(err, workerapp.CapacityBindingMismatch) || runtimeStagingWait(err) {
		t.Fatalf("corrupt graph classified as wait: %v", err)
	}
	if _, ok := r.environment.get(request.RuntimeInstanceID); !ok {
		t.Fatal("corrupt graph caused disposal")
	}
	if f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 0 {
		t.Fatal("corrupt graph acquired Prompt")
	}
}

type activationResponseLoss struct {
	workerapp.RuntimeResidencyStore
	lost  bool
	calls int
}

func (s *activationResponseLoss) ActivateResidentRuntime(ctx context.Context, r workerapp.ActivateResidentRuntimeRecord) (workerapp.ResidentRuntimeState, error) {
	s.calls++
	v, err := s.RuntimeResidencyStore.ActivateResidentRuntime(ctx, r)
	if err == nil && !s.lost {
		s.lost = true
		return workerapp.ResidentRuntimeState{}, workerapp.CapacityContainerProofPending
	}
	return v, err
}
func TestInitialActivationResponseLossReplaysExactActivate(t *testing.T) {
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		f.runtime.heartbeatStart = make(chan struct{})
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "activation-response-loss", "Same original activation")
	store := &activationResponseLoss{RuntimeResidencyStore: f.app.runtime.residency.Store}
	f.app.runtime.residency.Store = store
	turn, callback := f.next()
	f.send(callback)
	if store.calls != 2 || f.count(`SELECT count(*) FROM worker_runs`) != 1 || f.count(`SELECT count(*) FROM agent_turns`) != 1 || f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 1 {
		t.Fatal("response loss duplicated original work")
	}
	f.finish(turn, callback)
	f.settled(turn)
}

func TestInitialActivationReadyAndStartResponseLossSingleLogicalPrompt(t *testing.T) {
	var readyDuplicates atomic.Int32
	var startLoss atomic.Bool
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		f.runtime.heartbeatStart = make(chan struct{})
		f.journal.afterApply = func(ctx context.Context, r workerapp.ApplyRuntimeControlEventRecord, v workerapp.RuntimeControlEventAcceptance) error {
			if v.Message.MessageID != "ready-observation-fixture" {
				return nil
			}
			_, err := f.journal.RuntimeControlJournal.ApplyRuntimeControlEvent(ctx, r)
			if err == nil {
				readyDuplicates.Add(1)
			}
			return err
		}
		f.journal.afterReserve = func(_ context.Context, r workerapp.ReserveRuntimeControlCommandRecord, _ workerapp.RuntimeControlMessage) error {
			if r.LogicalEnvelope.MessageType == "agent_turn.start" && !startLoss.Swap(true) {
				return errors.New("SYNTHETIC committed start response loss")
			}
			return nil
		}
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "start-response-loss", "One logical Prompt despite reserve response loss")
	r := f.app.runtime
	supplementMust(t, r.startNextQueuedTask(f.ctx))
	var before string
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT jsonb_build_object('runs',(SELECT jsonb_agg(to_jsonb(r)) FROM worker_runs r),'turns',(SELECT jsonb_agg(to_jsonb(t)) FROM agent_turns t))::text`).Scan(&before))
	supplementMust(t, r.executeNextDeliveredTurn(f.ctx))
	r.turnSupervisors.group.Wait()
	if !startLoss.Load() || len(f.runtime.starts) != 0 || f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 1 {
		t.Fatal("lost response delivered/duplicated start")
	}
	var after string
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT jsonb_build_object('runs',(SELECT jsonb_agg(to_jsonb(r)) FROM worker_runs r),'turns',(SELECT jsonb_agg(to_jsonb(t)) FROM agent_turns t))::text`).Scan(&after))
	if before != after {
		t.Fatal("reserve retry changed Run/Turn clocks")
	}
	turn, callback := f.next()
	f.send(callback)
	if readyDuplicates.Load() != 1 || f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 1 {
		t.Fatal("duplicate ready/retry added logical command")
	}
	f.finish(turn, callback)
	f.settled(turn)
}

type countedInitialPreparation struct {
	workerapp.RuntimeEnvironmentPreparer
	calls atomic.Int32
}

func (p *countedInitialPreparation) PlanRuntime(ctx context.Context, r workerapp.RuntimePreparationRequest) (workerapp.RuntimePreparationPlan, error) {
	plan, err := p.RuntimeEnvironmentPreparer.PlanRuntime(ctx, r)
	if err != nil || plan == nil {
		return plan, err
	}
	return &countedInitialPreparationPlan{RuntimePreparationPlan: plan, calls: &p.calls}, nil
}

type countedInitialPreparationPlan struct {
	workerapp.RuntimePreparationPlan
	calls *atomic.Int32
}

func (p *countedInitialPreparationPlan) Prepare(ctx context.Context) (workerapp.RuntimeProvisionRequest, error) {
	p.calls.Add(1)
	return p.RuntimePreparationPlan.Prepare(ctx)
}

type blockedInitialActivation struct {
	workerapp.RuntimeResidencyStore
	entered, release chan struct{}
}

func (s *blockedInitialActivation) ActivateResidentRuntime(ctx context.Context, r workerapp.ActivateResidentRuntimeRecord) (workerapp.ResidentRuntimeState, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return workerapp.ResidentRuntimeState{}, ctx.Err()
	}
	return s.RuntimeResidencyStore.ActivateResidentRuntime(ctx, r)
}

func TestInitialActivationBadSourceBeforeFirstPrompt(t *testing.T) {
	var files *runtimecapacity.FileInventory
	f := newSupplementFixture(t, func(f *supplementFixture) {
		files = enableInitialCapacityFixture(f)
		f.runtime.heartbeatStart = make(chan struct{})
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "bad-before-prompt", "Reconcile at first start boundary")
	r := f.app.runtime
	supplementMust(t, r.startNextQueuedTask(f.ctx))
	supplementMust(t, os.WriteFile(filepath.Join(files.Directory, "capacity.json"), []byte("invalid after activation"), 0600))
	if err := r.executeNextDeliveredTurn(f.ctx); !errors.Is(err, workerapp.CapacityEvidenceInvalid) {
		t.Fatal(err)
	}
	if f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 0 {
		t.Fatal("stale oldgood sample authorized Prompt")
	}
	if f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE consumed_at IS NOT NULL AND released_at IS NULL`) != 1 {
		t.Fatal("denied first Prompt lost original token")
	}
	files.Publish(nil)
	turn, callback := f.next()
	f.send(callback)
	f.finish(turn, callback)
	f.settled(turn)
}

// These three startup fixtures model historical active owners, before source
// pinning existed. Real Start/AgentSession binding/Activate transactions and
// signed SYNTHETIC CID facts establish the graph; no current launch is authorized.
func seedHistoricalActiveInitialOwner(t *testing.T, f *supplementFixture) {
	t.Helper()
	preparation, request := seedHistoricalInitialOwner(t, f)
	r := f.app.runtime
	// Record one already-existing synthetic provision for the restore assertion.
	// There is no new Provision/client start and no permissive pre-Start check.
	f.runtime.inventory.mu.Lock()
	f.runtime.inventory.requests[request.RuntimeInstanceID] = request
	f.runtime.inventory.mu.Unlock()
	f.publishInitial(request)
	actualAgentSessionID := "historical-agent-" + request.RuntimeInstanceID
	bound, err := r.bindInitialAgentSession(f.ctx, preparation.Ownership, actualAgentSessionID)
	supplementMust(t, err)
	supplementMust(t, r.checkInitialCapacity(f.ctx))
	_, err = r.residency.Activate(f.ctx, workerapp.ActivateResidentRuntimeCommand{
		TenantID: preparation.Command.TenantID, WorkerSessionID: request.WorkerSessionID,
		WorkerRunID: request.WorkerRunID, AgentTurnID: preparation.Ownership.AgentTurnID,
		RuntimeInstanceID: request.RuntimeInstanceID, ContainerID: "fixture-" + request.RuntimeInstanceID,
		RuntimeGeneration: request.Generation, ControlChannelEpoch: preparation.Ownership.ControlChannelEpoch,
		ExpectedSessionRevision: bound.SessionRevision, ExpectedRunRevision: bound.RunRevision,
		ExpectedTurnRevision: bound.TurnRevision, ExpectedRuntimeRevision: bound.RuntimeRevision,
	})
	supplementMust(t, err)
	assertInitialSourceUnpinned(t, f, request.RuntimeInstanceID)
	if f.count(`SELECT count(*) FROM runtime_instances i JOIN agent_turns t ON t.worker_run_id=i.worker_run_id WHERE i.runtime_instance_id=$1 AND i.status='active' AND i.container_id=$2 AND t.agent_session_id=$3`, request.RuntimeInstanceID, "fixture-"+request.RuntimeInstanceID, actualAgentSessionID) != 1 {
		t.Fatal("historical active/CID/AgentSession binding was not established")
	}
}

// Current-source cases use actual signed facts, source pinning and the original
// Activate transaction, then fail only the subsequent ready commit. Historical
// cases explicitly seed an active, never-pinned graph before their ledger fault.
func TestInitialActivationCommittedOwnerStartupHold(t *testing.T) {
	for _, scenario := range []string{"pending", "maplost", "missing_snapshot", "null_snapshot", "permit_only", "missing_execution", "wrong_token", "wrong_target", "legacy", "already_live", "ready_committed_maplost"} {
		t.Run(scenario, func(t *testing.T) {
			historical := scenario == "missing_execution" || scenario == "permit_only" || scenario == "legacy"
			var env *recordedInitialEnvironment
			f := newSupplementFixture(t, func(f *supplementFixture) {
				enableInitialCapacityFixture(f)
				env = &recordedInitialEnvironment{RuntimeEnvironmentPreparer: f.app.runtime.starts.Environment}
				f.app.runtime.starts.Environment = env
				f.runtime.heartbeatStart = make(chan struct{})
				f.journal.beforeApply = func(_ context.Context, r workerapp.ApplyRuntimeControlEventRecord) error {
					if r.Command.MessageID == "ready-observation-fixture" && scenario != "already_live" && scenario != "ready_committed_maplost" {
						return errors.New("SYNTHETIC ready commit unavailable")
					}
					return nil
				}
			})
			r := f.app.runtime
			createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "committed-unhanded", "Hold original active owner")
			if historical {
				seedHistoricalActiveInitialOwner(t, f)
			} else {
				startErr := r.startNextQueuedTask(f.ctx)
				if scenario == "already_live" || scenario == "ready_committed_maplost" {
					supplementMust(t, startErr)
				} else if startErr == nil || !strings.Contains(startErr.Error(), "ready commit unavailable") {
					supplementReceive(t, f.ctx, f.provisioner.stop())
					r.mu.Lock()
					for _, pending := range r.pendingInitialActivations {
						t.Logf("failed initial Activate command=%+v blocked=%v", pending.command, pending.blocked)
					}
					r.mu.Unlock()
					var revisions []byte
					if err := f.pool.QueryRow(f.ctx, `SELECT jsonb_build_object('sessions',(SELECT jsonb_agg(jsonb_build_object('status',status,'revision',session_revision)) FROM worker_sessions),'runs',(SELECT jsonb_agg(jsonb_build_object('status',status,'revision',revision)) FROM worker_runs),'runtimes',(SELECT jsonb_agg(jsonb_build_object('status',status,'revision',revision,'has_cid',container_id IS NOT NULL)) FROM runtime_instances),'turns',(SELECT jsonb_agg(jsonb_build_object('status',status,'revision',revision)) FROM agent_turns))`).Scan(&revisions); err == nil {
						t.Logf("failed initialization revisions=%s", revisions)
					}
					t.Fatalf("ready commit failure=%v", startErr)
				}
				assertInitialSourcePinned(t, f, env.request.Ownership.RuntimeInstanceID, env.request.SourcePlan)
			}
			if f.count(`SELECT count(*) FROM runtime_instances WHERE status='active' AND container_id IS NOT NULL`) != 1 {
				t.Fatal("original Activate did not commit")
			}
			supplementReceive(t, f.ctx, f.provisioner.stop())
			candidates, err := r.loadStartupRuntimeCandidates(f.ctx)
			supplementMust(t, err)
			if len(candidates) != 1 {
				t.Fatalf("candidates=%d", len(candidates))
			}
			id := candidates[0].Restore.RuntimeInstanceID
			preparedBefore, ok := r.environment.get(id)
			if !ok {
				t.Fatal("initial preparation missing")
			}
			mapLost := scenario != "pending" && scenario != "already_live"
			if mapLost {
				r.mu.Lock()
				r.pendingInitialActivations = nil
				r.runtimes = make(map[string]*liveRuntime)
				r.mu.Unlock()
				r.environment.mu.Lock()
				r.environment.prepared = make(map[string]preparedRuntime)
				r.environment.mu.Unlock()
			}
			// Current-source corruption retains its immutable pin. Only the three
			// historical genesis cases may delete a never-pinned execution; the
			// legacy case also removes every ledger marker.
			faults := map[string]string{
				"missing_execution": `DELETE FROM runtime_capacity_executions`,
				"wrong_token":       `UPDATE worker_runs SET start_snapshot=jsonb_set(start_snapshot,'{runtime_capacity,Reservation,Token}','"wrong-token"')`,
				"wrong_target":      `UPDATE runtime_capacity_executions SET target_digest='wrong'`,
				"missing_snapshot":  `UPDATE worker_runs SET start_snapshot=start_snapshot-'runtime_capacity'`,
				"null_snapshot":     `UPDATE worker_runs SET start_snapshot=jsonb_set(start_snapshot,'{runtime_capacity}','null')`,
				"permit_only":       `DELETE FROM runtime_capacity_executions; DELETE FROM runtime_capacity_residents; UPDATE worker_runs SET start_snapshot=start_snapshot-'runtime_capacity'`,
				"legacy":            `DELETE FROM runtime_capacity_executions; DELETE FROM runtime_capacity_residents; UPDATE worker_runs SET start_snapshot=start_snapshot-'runtime_capacity'; UPDATE session_dispatch_permits SET capacity_admission_ref='legacy-opaque-admission'`,
			}
			if historical {
				assertInitialSourceUnpinned(t, f, id)
			}
			if sql := faults[scenario]; sql != "" {
				_, err := f.pool.Exec(f.ctx, sql)
				supplementMust(t, err)
			}
			policyChecks := 0
			if scenario == "legacy" {
				// Explicit SYNTHETIC policy evidence for this legacy restore only;
				// the production owner lookup and subsequent Inspect remain in place.
				expected := workerapp.CreatedRuntimeIdentity{ContainerID: "fixture-" + id, RuntimeInstanceID: id, WorkerSessionID: candidates[0].Restore.WorkerSessionID, Generation: candidates[0].Restore.RuntimeGeneration}
				r.workerPolicy.inspect = func(ctx context.Context, target workerapp.CreatedRuntimeIdentity) error {
					policyChecks++
					if target != expected {
						return fmt.Errorf("synthetic legacy policy identity mismatch: %+v", target)
					}
					return ctx.Err()
				}
			}
			f.runtime.inventory.inspectionError = errors.New("SYNTHETIC forbidden restore reached Inspect")
			before := initialOwnershipState(t, f)
			hardGraph := scenario == "missing_snapshot" || scenario == "null_snapshot" || scenario == "permit_only" || scenario == "missing_execution" || scenario == "wrong_token" || scenario == "wrong_target"
			if scenario != "legacy" && scenario != "already_live" {
				_, _, _, directErr := r.restoreStartupRuntimeCandidate(f.ctx, candidates[0])
				wantErr := error(workerapp.InitialRuntimeStartReplayPending)
				if hardGraph {
					wantErr = workerapp.CapacityBindingMismatch
				}
				if !errors.Is(directErr, wantErr) {
					t.Fatalf("direct restore boundary=%v want=%v", directErr, wantErr)
				}
			}
			restored, err := r.restoreStartupRuntimes(f.ctx, startupSettlementQuarantine{})
			if len(restored) != 0 {
				t.Error("held owner became restored Turn")
			}
			f.runtime.inventory.mu.Lock()
			inspect, removes, provisions := f.runtime.inventory.inspectCalls, len(f.runtime.inventory.removes), len(f.runtime.inventory.requests)
			f.runtime.inventory.mu.Unlock()
			_, restoredCache := r.environment.get(id)
			if scenario == "legacy" {
				if err == nil || !restoredCache || inspect != 1 || policyChecks != 1 {
					t.Errorf("legacy restore blocked: err=%v restoredCache=%t Inspect=%d policyChecks=%d", err, restoredCache, inspect, policyChecks)
				}
			} else {
				if (hardGraph && !errors.Is(err, workerapp.CapacityBindingMismatch)) || (!hardGraph && err != nil) || (mapLost && restoredCache) || inspect != 0 {
					t.Errorf("startup aggregate err=%v hardGraph=%t restoredCache=%t Inspect=%d", err, hardGraph, restoredCache, inspect)
				}
			}
			if removes != 0 || provisions != 1 {
				t.Errorf("Remove=%d Provision=%d", removes, provisions)
			}
			if !bytes.Equal(before, initialOwnershipState(t, f)) {
				t.Error("startup changed original graph/token/deadline/commitment")
			}
			if !mapLost && !restoredCache {
				t.Error("startup discarded prepared owner")
			}
			if _, err := os.Stat(preparedBefore.Storage.WorkingCopy.BackendPath); err != nil {
				t.Error("prepared storage lost", err)
			}
			if len(f.runtime.starts) != 0 || f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 0 {
				t.Error("startup created Prompt")
			}
		})
	}
}

func TestInitialActivationBusyCommittedOwnerExcludesAdoption(t *testing.T) {
	f := newSupplementFixture(t, func(f *supplementFixture) {
		enableInitialCapacityFixture(f)
		f.runtime.heartbeatStart = make(chan struct{})
	})
	r := f.app.runtime
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "busy-active-owner", "Preserve single handoff owner")
	entered, release := make(chan struct{}), make(chan struct{})
	r.residency.Store = &afterInitialActivation{RuntimeResidencyStore: r.residency.Store, after: func(err error) {
		if err != nil {
			t.Errorf("Activate=%v", err)
		}
		close(entered)
		<-release
	}}
	done := make(chan error, 1)
	go func() { done <- r.startNextQueuedTask(f.ctx) }()
	supplementReceive(t, f.ctx, entered)
	candidates, err := r.loadStartupRuntimeCandidates(f.ctx)
	if err != nil || len(candidates) != 1 {
		close(release)
		<-done
		t.Fatalf("active candidates=%d err=%v", len(candidates), err)
	}
	id := candidates[0].Restore.RuntimeInstanceID
	owned := r.runtimeOwnedOrAdopting(id)
	acquired := r.beginRuntimeAdoption(id)
	if acquired {
		r.finishRuntimeAdoption(id)
	}
	close(release)
	supplementMust(t, supplementReceive(t, f.ctx, done))
	if !owned || acquired {
		t.Fatalf("busy active owner exposed to adoption: owned=%t acquired=%t", owned, acquired)
	}
	r.mu.Lock()
	live, pending := r.runtimes[id], r.pendingInitialActivations[id]
	r.mu.Unlock()
	if live == nil || pending != nil {
		t.Fatal("original initializer failed handoff")
	}
}
