//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

func TestInitialExecutionActivationWithoutLedgerDenies(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started := startInitialRuntimeBeforeActivation(t, pool, time.Now().UTC())
	if _, err := pool.Exec(ctx, `DELETE FROM runtime_capacity_executions`); err != nil {
		t.Fatal(err)
	}
	got, err := workerpostgres.NewRuntimeResidencyStore(pool).ActivateResidentRuntime(ctx, app.ActivateResidentRuntimeRecord{
		Command: app.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "unproven-container", RuntimeGeneration: started.RuntimeGeneration, ControlChannelEpoch: started.ControlChannelEpoch, ExpectedSessionRevision: started.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1}, Now: time.Now().UTC().Add(6 * time.Minute),
	})
	if !errors.Is(err, app.CapacityBindingMismatch) {
		t.Fatalf("unbacked activation=%+v error=%v", got, err)
	}
}

func TestInitialExecutionActivationWaitsForActualContainerAndReplays(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	started := startInitialRuntimeBeforeActivation(t, pool, now)
	store := workerpostgres.NewRuntimeResidencyStore(pool)
	record := app.ActivateResidentRuntimeRecord{Command: app.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "actual-container", RuntimeGeneration: started.RuntimeGeneration, ControlChannelEpoch: started.ControlChannelEpoch, ExpectedSessionRevision: started.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1}, Now: now.Add(6 * time.Minute)}
	if got, err := store.ActivateResidentRuntime(ctx, record); !errors.Is(err, app.CapacityContainerProofPending) {
		t.Fatalf("unobserved container activated: %+v %v", got, err)
	}
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "actual-container", started.RuntimeGeneration)
	got, err := store.ActivateResidentRuntime(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.ActivateResidentRuntime(ctx, record)
	if err != nil || got != again {
		t.Fatalf("replay changed ownership: %+v %+v %v", got, again, err)
	}
	record.Command.ContainerID = "invented-container"
	if _, err = store.ActivateResidentRuntime(ctx, record); !errors.Is(err, app.CapacityBindingMismatch) {
		t.Fatalf("CID replacement: %v", err)
	}
}

type initialExecutionEffects struct {
	prepared, provisioned int
	afterPrepare          func()
	driftResource         bool
}

func (e *initialExecutionEffects) PrepareRuntime(ctx context.Context, r app.RuntimePreparationRequest) (app.RuntimeProvisionRequest, error) {
	e.prepared++
	if e.afterPrepare != nil {
		e.afterPrepare()
	}
	p, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	out := app.RuntimeProvisionRequest{SourcePlanDigest: r.SourcePlan.Digest, WorkerSessionID: r.Ownership.WorkerSessionID, WorkerRunID: r.Ownership.WorkerRunID, RuntimeInstanceID: r.Ownership.RuntimeInstanceID, Generation: r.Ownership.RuntimeGeneration, ResourceProfile: app.ProfileRef{ID: p.ID, Revision: p.Revision, Digest: p.Digest}, NetworkProfile: app.ProfileRef{ID: r.Command.NetworkPolicyID, Revision: r.Command.NetworkPolicyRevision, Digest: r.Command.NetworkPolicyDigest}, NetworkEnforcementStatus: r.Command.NetworkEnforcementStatus}
	if e.driftResource {
		out.ResourceProfile.Digest = "wrong-profile"
	}
	return out, nil
}
func (e *initialExecutionEffects) Provision(ctx context.Context, r app.RuntimeProvisionRequest, check app.RuntimePreStartCheck) (app.ProvisionedRuntime, error) {
	e.provisioned++
	partial := app.ProvisionedRuntime{RuntimeInstanceID: r.RuntimeInstanceID, Generation: r.Generation, ContainerID: "effect-container"}
	if err := check(ctx, app.CreatedRuntimeIdentity{ContainerID: partial.ContainerID, RuntimeInstanceID: r.RuntimeInstanceID, WorkerSessionID: r.WorkerSessionID, Generation: r.Generation}); err != nil {
		return partial, err
	}
	return partial, nil
}
func (e *initialExecutionEffects) Inspect(context.Context, string) (app.RuntimeInspection, error) {
	return app.RuntimeInspection{}, nil
}

func TestInitialExecutionCoordinatorUsesDurableAuthorityAtEachEffect(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "cancel_before_prepare", "cancel_after_prepare", "resource_drift", "network_drift", "cross_graph", "released"} {
		t.Run(scenario, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Now().UTC()
			runtimecapacity.Initialize(t, pool)
			dispatch, commands, ids := initialCapacityQueueAt(t, pool, 1, now)
			permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
			if err != nil {
				t.Fatal(err)
			}
			command := residentStartCommand(permit)
			effects := &initialExecutionEffects{}
			change := func(sql string) {
				if _, err := pool.Exec(ctx, sql); err != nil {
					t.Fatal(err)
				}
			}
			afterStart := func() {
				switch scenario {
				case "missing":
					change(`DELETE FROM runtime_capacity_executions`)
				case "cancel_before_prepare":
					change(`UPDATE tasks SET status='cancelled'`)
				case "cancel_after_prepare":
					effects.afterPrepare = func() { change(`UPDATE tasks SET status='cancelled'`) }
				case "resource_drift":
					effects.driftResource = true
				case "network_drift":
					change(`UPDATE runtime_instances SET network_policy_digest='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'`)
				case "cross_graph":
					change(`UPDATE worker_sessions SET active_worker_run_id=NULL`)
				case "released":
					change(`UPDATE runtime_capacity_executions SET released_at=clock_timestamp(),release_reason='test_release'`)
				}
			}
			coordinator := app.RuntimeCoordinator{CreatedLayerChecker: eInitialCreatedLayerChecker{}, Authorizer: workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool), Starts: app.StartWorkerRunService{Store: initialExecutionAfterStart{store: workerpostgres.NewRunStore(pool), after: afterStart}, IDs: ids, Now: func() time.Time { return now.Add(5 * time.Minute) }}, Environment: effects, Provisioner: effects}
			got, err := coordinator.Start(ctx, app.StartRuntimeCommand{Run: command})
			if scenario == "valid" {
				if err != nil || effects.prepared != 1 || effects.provisioned != 1 {
					t.Fatalf("valid initial effects: %+v %v effects=%+v", got, err, effects)
				}
				return
			}
			wantPrepare := 0
			if scenario == "cancel_after_prepare" || scenario == "resource_drift" {
				wantPrepare = 1
			}
			if err == nil || effects.prepared != wantPrepare || effects.provisioned != 0 {
				t.Fatalf("%s effects=%+v result=%+v err=%v", scenario, effects, got, err)
			}
			if got.Ownership.WorkerRunID == "" {
				t.Fatalf("lost committed cleanup owner: %+v", got)
			}
		})
	}
}

func TestInitialExecutionUnsupportedStandaloneAndQueuedTurnsDenyBeforeNewOwners(t *testing.T) {
	for _, kind := range []string{"standalone", "ordinary_queue", "settlement_queue"} {
		t.Run(kind, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Now().UTC().Add(-10 * time.Minute)
			var started app.StartedWorkerRun
			var done app.ResidentRuntimeState
			var settlement app.TurnSettlement
			if kind == "settlement_queue" {
				f := prepareTerminalSettlementApplyFixture(t, pool, now, "initial-deny")
				f.Apply.TurnSettlement = turnSettlementApplyAuthority(f.Settlement)
				if _, err := (app.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return f.Settlement.UpdatedAt.Add(time.Second) }}).ApplyEvent(ctx, f.Apply); err != nil {
					t.Fatal(err)
				}
				svc := app.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: time.Now}
				var err error
				settlement, err = svc.Load(ctx, f.Settlement.SettlementID)
				if err != nil {
					t.Fatal(err)
				}
				settlement, err = svc.Claim(ctx, app.ClaimTurnSettlementCommand{SettlementID: settlement.SettlementID, ExpectedRevision: settlement.Revision, OwnerID: "initial-deny-owner"})
				if err != nil {
					t.Fatal(err)
				}
				var cp struct {
					Domain app.TurnSettlementDomainProof `json:"domain"`
				}
				if err = json.Unmarshal(settlement.PhaseState, &cp); err != nil {
					t.Fatal(err)
				}
				started = f.Started
				done = app.ResidentRuntimeState{SessionRevision: cp.Domain.SessionRevision, RunRevision: cp.Domain.RunRevision, TurnRevision: cp.Domain.TurnRevision, RuntimeRevision: cp.Domain.RuntimeRevision}
				if _, err = pool.Exec(ctx, `UPDATE worker_sessions SET workspace_id='workspace-local'; UPDATE tasks SET workspace_id='workspace-local'`); err != nil {
					t.Fatal(err)
				}
			} else {
				var active app.ResidentRuntimeState
				started, active = startActiveRecoveryFixture(t, pool, now)
				var err error
				done, err = (app.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: time.Now}).CompleteStage(ctx, app.CompleteResidentStageCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, SessionTarget: domain.SessionInteractive, RunTarget: domain.RunCompleted, TurnTarget: domain.TurnCompleted})
				if err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if kind == "standalone" {
				if _, err = pool.Exec(ctx, `UPDATE tasks SET status='completed'; UPDATE worker_sessions SET bound_task_id=NULL`); err != nil {
					t.Fatal(err)
				}
				_, err = (app.SessionFollowUpService{Store: workerpostgres.NewSessionFollowUpStore(pool), IDs: &concurrentAllocationIDs{}, Now: time.Now}).Execute(ctx, app.StartSessionFollowUpCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: started.WorkerSessionID, PreviousWorkerRunID: started.WorkerRunID, PreviousAgentTurnID: started.AgentTurnID, ExpectedSessionRevision: done.SessionRevision, ExpectedRunRevision: done.RunRevision, ExpectedTurnRevision: done.TurnRevision, Content: "follow up", IdempotencyKey: "unsupported-followup"})
			} else {
				if _, err = pool.Exec(ctx, `INSERT INTO worker_user_commands(command_id,tenant_id,user_id,worker_session_id,worker_run_id,agent_turn_id,command_type,content,expected_session_revision,expected_run_revision,expected_turn_revision,queue_sequence,status,idempotency_key,created_at,updated_at) VALUES('initial-denied-queue','tenant-local','user-local',$1,$2,$3,'queue','Continue',1,1,1,1,'accepted','initial-denied-queue',now(),now())`, started.WorkerSessionID, started.WorkerRunID, started.AgentTurnID); err != nil {
					t.Fatal(err)
				}
				rec := app.ContinueQueuedTurnRecord{Command: app.ContinueQueuedTurnCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: started.WorkerSessionID, PreviousWorkerRunID: started.WorkerRunID, PreviousAgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, QueueCommandID: "initial-denied-queue", ExpectedSessionRevision: done.SessionRevision, ExpectedRunRevision: done.RunRevision, ExpectedTurnRevision: done.TurnRevision, ExpectedRuntimeRevision: done.RuntimeRevision}, WorkerRunID: "denied-run", AgentTurnID: "denied-turn", ScheduledCommandID: "denied-deadline", EventID: "denied-event", Now: time.Now()}
				store := workerpostgres.NewQueuedTurnStore(pool)
				if kind == "settlement_queue" {
					fields := typedTurnSettlementCheckpoint(t, settlement.PhaseState)
					fields.AdditionalFields["continuation_plan"], _ = json.Marshal(map[string]any{"action": "create_queued_turn", "queue_command_id": rec.Command.QueueCommandID, "expected_session_revision": done.SessionRevision, "expected_run_revision": done.RunRevision, "expected_turn_revision": done.TurnRevision, "expected_runtime_revision": done.RuntimeRevision})
					settlement, err = (app.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: time.Now}).FreezeCheckpoint(ctx, app.FreezeTurnSettlementCheckpointCommand{SettlementID: settlement.SettlementID, ExpectedRevision: settlement.Revision, LeaseOwnerID: settlement.LeaseOwnerID, LeaseFence: settlement.LeaseFence, Checkpoint: fields})
					if err != nil {
						t.Fatal(err)
					}
					_, err = store.ContinueSettlementQueuedTurn(ctx, settlement, rec)
				} else {
					_, err = store.ContinueQueuedTurn(ctx, rec)
				}
			}
			if !errors.Is(err, app.CapacityPathNotIntegrated) {
				t.Fatalf("%s denial: %v", kind, err)
			}
			for _, table := range []string{"worker_runs", "agent_turns", "scheduled_commands"} {
				assertInitialCapacityCount(t, pool, table, 1)
			}
		})
	}
}

func TestInitialExecutionPoolPrefixAndSingleConnection(t *testing.T) {
	for _, boundary := range []string{"prepare", "activation", "start", "claim", "replay", "resume"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var pool *pgxpool.Pool
			var started app.StartedWorkerRun
			var service app.RuntimeControlJournalService
			var authority app.RuntimeControlAuthority
			var operation func(*pgxpool.Pool) error
			if boundary == "prepare" || boundary == "activation" {
				pool = postgrestest.Open(t)
				now := time.Now().UTC()
				started = startInitialRuntimeBeforeActivation(t, pool, now)
				if boundary == "prepare" {
					var permit taskapp.IssuedTaskPermit
					if err := pool.QueryRow(ctx, `SELECT permit_id,worker_session_id,target_id,expected_session_revision,runtime_generation FROM session_dispatch_permits`).Scan(&permit.PermitID, &permit.WorkerSessionID, &permit.TaskID, &permit.ExpectedSessionRevision, &permit.RuntimeGeneration); err != nil {
						t.Fatal(err)
					}
					operation = func(p *pgxpool.Pool) error {
						return workerpostgres.NewInitialRuntimeExecutionAuthorizer(p).AuthorizeInitialPreparation(ctx, app.RuntimePreparationRequest{Ownership: started, Command: residentStartCommand(permit), SourcePlan: initialSourceBinding(t, nil)})
					}
				} else {
					runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "lock-container", 1)
					record := app.ActivateResidentRuntimeRecord{Command: app.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "lock-container", RuntimeGeneration: 1, ControlChannelEpoch: 1, ExpectedSessionRevision: started.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1}, Now: now.Add(6 * time.Minute)}
					operation = func(p *pgxpool.Pool) error {
						_, err := workerpostgres.NewRuntimeResidencyStore(p).ActivateResidentRuntime(ctx, record)
						return err
					}
				}
			} else {
				pool, started, service, authority = initialControlCapacityFixture(t)
				command := initialStartCommand(started, authority)
				if boundary != "start" {
					if _, err := service.ReserveCommand(ctx, command); err != nil {
						t.Fatal(err)
					}
				}
				var ready contractsv1.RuntimeControlResumeReady
				if boundary == "resume" {
					plan, err := service.Claim(ctx, runtimeControlClaim(started, "initial-process", 2, "prefix-owner"))
					if err != nil {
						t.Fatal(err)
					}
					authority = authorityFrom(plan.Channel)
					ready = contractsv1.RuntimeControlResumeReady{ServerConnectionID: plan.Channel.OwnerID, AcknowledgedJournalRevision: plan.Channel.ResumeClaimRevision, WorkerTXReplayedThroughSequence: plan.Channel.EventCommittedSequence, WorkerTXCursorDigest: plan.Channel.EventCommittedDigest, WorkerRXReceivedThroughSequence: plan.Channel.CommandCommittedSequence, WorkerRXAppliedSequence: plan.Channel.CommandAppliedSequence, WorkerRXCursorDigest: plan.Channel.CommandCommittedDigest}
				}
				operation = func(p *pgxpool.Pool) error {
					svc := service
					svc.Store = workerpostgres.NewRuntimeControlJournal(p)
					var err error
					switch boundary {
					case "start":
						_, err = svc.ReserveCommand(ctx, command)
					case "claim":
						_, err = svc.Claim(ctx, runtimeControlClaim(started, "initial-process", 2, "prefix-owner"))
					case "replay":
						_, err = svc.ReplayPlan(ctx, authority)
					case "resume":
						_, err = svc.CompleteResume(ctx, app.CompleteRuntimeControlResumeCommand{Authority: authority, Ready: ready})
					}
					return err
				}
			}
			gatePool := lockOrderPool(t, pool, "initial-execution-gate")
			gate, err := gatePool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(ctx, `SELECT 1 FROM runtime_capacity_pools FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			waiter := lockOrderPool(t, pool, "initial-execution-waiter")
			result := make(chan error, 1)
			go func() { result <- operation(waiter) }()
			waitDispatchLockEdge(t, ctx, pool, "initial-execution-waiter", "initial-execution-gate")
			for _, table := range []string{"tasks", "worker_sessions", "worker_runs", "agent_turns", "runtime_instances", "runtime_control_channels"} {
				if _, err = gate.Exec(ctx, "SELECT 1 FROM "+table+" FOR UPDATE NOWAIT"); err != nil {
					t.Fatalf("%s acquired %s before capacity pool: %v", boundary, table, err)
				}
			}
			if err = gate.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; err != nil {
				t.Fatal(err)
			}
			cfg := pool.Config()
			cfg.MaxConns = 1
			single, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer single.Close()
			if err = operation(single); err != nil {
				t.Fatalf("single connection %s replay: %v", boundary, err)
			}
		})
	}
}

func TestInitialExecutionActivationSQLFaultRollsBackAllOwners(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	started := startInitialRuntimeBeforeActivation(t, pool, now)
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "fault-container", 1)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_initial_activation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'initial activation fault'; END $$; CREATE TRIGGER reject_initial_activation BEFORE UPDATE ON worker_runs FOR EACH ROW EXECUTE FUNCTION reject_initial_activation()`); err != nil {
		t.Fatal(err)
	}
	_, err := workerpostgres.NewRuntimeResidencyStore(pool).ActivateResidentRuntime(ctx, app.ActivateResidentRuntimeRecord{Command: app.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "fault-container", RuntimeGeneration: 1, ControlChannelEpoch: 1, ExpectedSessionRevision: started.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1}, Now: now.Add(6 * time.Minute)})
	if err == nil {
		t.Fatal("fault did not abort")
	}
	var status, cid string
	var rev uint64
	if err = pool.QueryRow(ctx, `SELECT status,COALESCE(container_id,''),revision FROM runtime_instances WHERE runtime_instance_id=$1`, started.RuntimeInstanceID).Scan(&status, &cid, &rev); err != nil {
		t.Fatal(err)
	}
	if status != "starting" || cid != "" || rev != 1 {
		t.Fatalf("partial activation %s %s %d", status, cid, rev)
	}
}

func TestInitialExecutionRecoveryCannotAllocateReplacementGeneration(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-10 * time.Minute)
	started, active := startActiveRecoveryFixture(t, pool, base)
	svc := app.RuntimeRecoveryService{Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{}, Now: time.Now}
	incident, err := svc.Open(ctx, app.OpenRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, FailedRuntimeInstanceID: started.RuntimeInstanceID, FailedGeneration: 1, FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit, EvidenceRef: "initial-inspection", EvidenceAt: time.Now(), ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, IdempotencyKey: "initial-loss"})
	if err != nil {
		t.Fatal(err)
	}
	fencing, err := svc.Advance(ctx, app.AdvanceRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryFencing, ExpectedIncidentRevision: incident.IncidentRevision, IdempotencyKey: "initial-fence"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Advance(ctx, app.AdvanceRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryProvisioning, ExpectedIncidentRevision: fencing.IncidentRevision, FenceStatus: domain.RecoveryFenceConfirmed, MountFenceToken: 2, IdempotencyKey: "initial-replacement"})
	if !errors.Is(err, app.CapacityPathNotIntegrated) {
		t.Fatalf("replacement allocation: %v", err)
	}
	assertAllocationCount(t, pool, "runtime_instances", 1)
	var phase string
	var attempts, generation uint64
	if err = pool.QueryRow(ctx, `SELECT phase,attempt_count FROM runtime_recovery_incidents WHERE incident_id=$1`, incident.IncidentID).Scan(&phase, &attempts); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT runtime_generation FROM worker_sessions WHERE worker_session_id=$1`, started.WorkerSessionID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if phase != "fencing" || attempts != 0 || generation != 1 {
		t.Fatalf("replacement allocated: %s attempts=%d generation=%d", phase, attempts, generation)
	}
}

func TestInitialExecutionTerminalSettlementCompletesWithoutNewExecution(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "no_queued_turn"
		if cancelled {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Now().UTC()
			var started app.StartedWorkerRun
			var appliedAt time.Time
			if cancelled {
				var cleaned app.TurnSettlement
				var deadlineAt time.Time
				started, cleaned, deadlineAt = prepareBackendDeadlineSourceForApply(t, pool, base, true)
				settled := seedBackendDeadlineCancelledTaskSettled(t, pool, started, cleaned, deadlineAt)
				appliedAt = settled.UpdatedAt.Add(time.Second)
				runtimecapacity.ObserveStale(t, pool)
				_, err := (app.TurnSettlementSourceService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: func() time.Time { return appliedAt }}).ApplyBackendDeadline(ctx, app.ApplyBackendDeadlineSourceCommand{
					SettlementID: settled.SettlementID, ExpectedRevision: settled.Revision,
					ExpectedSourceDigest: settled.Source.Digest, ExpectedSourceStateDigest: settled.Source.StateDigest,
					LeaseOwnerID: settled.LeaseOwnerID, LeaseFence: settled.LeaseFence,
				})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				fixture := prepareTerminalSettlementApplyFixture(t, pool, base, "initial-no-queue")
				started = fixture.Started
				appliedAt = fixture.Settlement.UpdatedAt.Add(time.Second)
				runtimecapacity.ObserveStale(t, pool)
				fixture.Apply.TurnSettlement = turnSettlementApplyAuthority(fixture.Settlement)
				if _, err := (app.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return appliedAt }}).ApplyEvent(ctx, fixture.Apply); err != nil {
					t.Fatal(err)
				}
			}
			completed := completeAppliedTerminalSettlement(t, pool, started, appliedAt.Add(time.Second))
			if completed.Phase != app.TurnSettlementCompleted {
				t.Fatalf("settlement did not complete: %s", completed.Phase)
			}
			var runs, turns int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM worker_runs), (SELECT count(*) FROM agent_turns)`).Scan(&runs, &turns); err != nil || runs != 1 || turns != 1 {
				t.Fatalf("new execution after terminal: runs=%d turns=%d error=%v", runs, turns, err)
			}
			if cancelled {
				var status string
				if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE task_id=$1`, started.TaskID).Scan(&status); err != nil || status != "cancelled" {
					t.Fatalf("cancelled task changed: %s %v", status, err)
				}
			}
		})
	}
}

func TestInitialExecutionCoordinatorHistoricalStartDoesNotRepeatEffects(t *testing.T) {
	pool := postgrestest.Open(t)
	runtimecapacity.Initialize(t, pool)
	dispatch, commands, ids := initialCapacityQueue(t, pool, 1)
	permit, err := dispatch.IssueTaskPermit(context.Background(), commands[0])
	if err != nil {
		t.Fatal(err)
	}
	effects := &initialExecutionEffects{}
	coordinator := app.RuntimeCoordinator{CreatedLayerChecker: eInitialCreatedLayerChecker{}, Authorizer: workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool), Starts: app.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: time.Now}, Environment: effects, Provisioner: effects}
	command := app.StartRuntimeCommand{Run: residentStartCommand(permit)}
	first, err := coordinator.Start(context.Background(), command)
	if err != nil || effects.prepared != 1 || effects.provisioned != 1 {
		t.Fatalf("first start=%+v error=%v effects=%+v", first, err, effects)
	}
	second, err := coordinator.Start(context.Background(), command)
	if !errors.Is(err, app.InitialRuntimeStartReplayPending) || !first.Ownership.Created || second.Ownership.Created || second.Ownership.WorkerRunID != first.Ownership.WorkerRunID || effects.prepared != 1 || effects.provisioned != 1 {
		t.Fatalf("historical start=%+v error=%v effects=%+v", second, err, effects)
	}
}

// This spy only mutates adversarial state after the real production transaction
// commits. It preserves the store's exact result and never supplies authority.
type initialExecutionAfterStart struct {
	store app.StartWorkerRunStore
	after func()
}

func (s initialExecutionAfterStart) StartWorkerRun(ctx context.Context, r app.StartWorkerRunRecord) (app.StartedWorkerRun, error) {
	result, err := s.store.StartWorkerRun(ctx, r)
	if err == nil {
		s.after()
	}
	return result, err
}

type eInitialCreatedLayerChecker struct{}

func (eInitialCreatedLayerChecker) CheckCreatedLayer(context.Context, app.RuntimeProvisionRequest, app.CreatedRuntimeIdentity) error {
	return nil
}

type initialExecutionPlan struct {
	effects *initialExecutionEffects
	request app.RuntimePreparationRequest
}

func (e *initialExecutionEffects) PlanRuntime(_ context.Context, r app.RuntimePreparationRequest) (app.RuntimePreparationPlan, error) {
	r.SourcePlan = initialSourceBinding(nil, nil)
	return &initialExecutionPlan{effects: e, request: r}, nil
}
func (p *initialExecutionPlan) SourceBinding() app.RuntimeSourcePlanBinding {
	return p.request.SourcePlan
}
func (p *initialExecutionPlan) Prepare(ctx context.Context) (app.RuntimeProvisionRequest, error) {
	return p.effects.PrepareRuntime(ctx, p.request)
}
func (*initialExecutionPlan) Close() error { return nil }
