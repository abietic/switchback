//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestObservationRegistryCleanupInitialStartFailureHasDurableOwner(t *testing.T) {
	for _, mode := range []string{"initial-fence-fails", "lost-before-cleanup-begin", "reject-unowned", "reject-startup"} {
		t.Run(mode, func(t *testing.T) { testInitialCleanupFailure(t, mode) })
	}
}

// These are deliberately incompatible persisted fixtures testing exclusion,
// not evidence that an arbitrary lost Session has cleanup authority.
func TestObservationRegistryCleanupLostDiscoveryRespectsExistingOwners(t *testing.T) {
	for _, boundary := range []string{"ordinary-loss", "wrong-runtime", "wrong-generation", "foreign-user", "foreign-tenant", "no-current-runtime", "active-recovery", "pending-settlement"} {
		t.Run(boundary, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Now().UTC()
			seedBackendDeadlineClaimGraph(t, pool, now)
			environment, _, _, _ := newDeadlineCleanupTestEnvironment(t)
			journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return now }}
			environment.hub = controlgrpc.NewHubWithJournal(journal)
			identity := controlgrpc.RuntimeIdentity{WorkerSessionID: "session-deadline-claim", RuntimeInstanceID: "runtime-deadline-claim", RuntimeGeneration: 1, ChannelEpoch: 7}
			storage, err := environment.storage.Materialize(identity.WorkerSessionID)
			if err != nil {
				t.Fatal(err)
			}
			materializeCleanupRegistry(t, environment, storage, identity)
			if boundary == "pending-settlement" {
				runner := startupDeadlineTestRunner(pool, func() time.Time { return now.Add(time.Second) })
				if err := drainStartupDeadlineCommands(ctx, runner.startupDeadlineScheduledCommandPoller()); err != nil {
					t.Fatal(err)
				}
				assertStartupDeadlineClaimed(t, pool)
			}
			if boundary == "active-recovery" {
				_, err := (workerapp.RuntimeRecoveryService{Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: randomIDs{}, Now: func() time.Time { return now }}).Open(ctx, workerapp.OpenRuntimeRecoveryCommand{
					TenantID: "tenant-local", WorkerSessionID: identity.WorkerSessionID, WorkerRunID: "run-deadline-claim", AgentTurnID: "turn-deadline-claim", FailedRuntimeInstanceID: identity.RuntimeInstanceID, FailedGeneration: 1, FailedChannelEpoch: 7, Trigger: domain.RecoveryTriggerContainerExit, EvidenceRef: "cleanup-boundary/fixture", EvidenceAt: now, ExpectedSessionRevision: 4, ExpectedRunRevision: 2, ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 5, IdempotencyKey: "cleanup-boundary-open",
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			key := "startup-failed/" + identity.RuntimeInstanceID
			switch boundary {
			case "ordinary-loss":
				key = "runtime-loss/" + identity.RuntimeInstanceID
			case "wrong-runtime":
				key = "startup-failed/other-runtime"
			case "wrong-generation":
				key = "backend-restart/" + identity.RuntimeInstanceID + "/2"
			}
			if _, err := pool.Exec(ctx, `UPDATE runtime_instances SET status='lost',ended_at=$2,loss_diagnostic=jsonb_build_object('idempotency_key',$1::text) WHERE runtime_instance_id='runtime-deadline-claim'`, key, now); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status='lost' WHERE worker_session_id='session-deadline-claim'`); err != nil {
				t.Fatal(err)
			}
			if boundary == "foreign-user" {
				if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET user_id='other-user' WHERE worker_session_id='session-deadline-claim'`); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "foreign-tenant" {
				if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET tenant_id='other-tenant' WHERE worker_session_id='session-deadline-claim'`); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "no-current-runtime" {
				if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET active_runtime_instance_id=NULL WHERE worker_session_id='session-deadline-claim'`); err != nil {
					t.Fatal(err)
				}
			}
			runner := &runtimeRunner{pool: pool, environment: environment, runtimes: make(map[string]*liveRuntime)}
			runner.configuration.Profile.TenantID, runner.configuration.Profile.ActorUserID = "tenant-local", "user-local"
			engine := &fake.RuntimeProvisioner{CleanupResult: workerapp.RuntimeCleanupObservation{Confirmed: true, ObservedAt: now}}
			runner.cleanup = workerapp.RuntimeCleanupService{Store: workerpostgres.NewRuntimeCleanupStore(pool), Cleaner: engine, Control: runtimeObservationCleanupControl{WorkerControl: environment.hub, runner: runner}, IDs: randomIDs{}, Now: func() time.Time { return now }, RetryDelay: time.Second}
			if err := runner.cleanupTerminalRuntime(ctx); err != nil {
				t.Fatal(err)
			}
			var pending int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_commands WHERE command_type='runtime_cleanup_reconcile'`).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending != 0 || len(engine.CleanupRequests) != 0 {
				t.Fatal("rediscovery took cleanup ownership across exclusion boundary")
			}
			if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
				t.Fatal("excluded registry was deleted")
			}
		})
	}
}

func testInitialCleanupFailure(t *testing.T, mode string) {
	t.Helper()
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	environment, _, _, _ := newDeadlineCleanupTestEnvironment(t)
	configureCleanupPrepare(t, environment)
	environment.secrets = runtimesecret.Materializer{
		Ownership: runtimesecret.OwnershipFunc(func(string, int, int) error { return nil }),
		OwnerUID:  func(string) (uint32, error) { return 0, nil }, OpenFileOwnerUID: func(*os.File) (uint32, error) { return 0, nil },
	}
	command := seedInitialCleanupStart(t, pool, environment, now)
	journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return now }}
	environment.hub = controlgrpc.NewHubWithJournal(journal)
	engine := &initialCleanupEngine{RuntimeProvisioner: fake.RuntimeProvisioner{CleanupResult: workerapp.RuntimeCleanupObservation{Confirmed: true, ObservedAt: now}}, succeed: mode == "reject-startup"}
	coordinator := workerapp.RuntimeCoordinator{Starts: workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: randomIDs{}, Now: func() time.Time { return now }}, Environment: environment, Provisioner: engine}
	started, startErr := coordinator.Start(ctx, workerapp.StartRuntimeCommand{Run: command})
	if (mode != "reject-startup" && (startErr == nil || !strings.Contains(startErr.Error(), "controlled initial container provision failure"))) || (mode == "reject-startup" && startErr != nil) || started.Ownership.RuntimeInstanceID == "" {
		t.Fatalf("fixture did not fail after actual initial ownership and preparation: %v", startErr)
	}
	identity := controlgrpc.RuntimeIdentity{WorkerSessionID: started.Ownership.WorkerSessionID, RuntimeInstanceID: started.Ownership.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: 1}
	storage, err := environment.storage.OpenExisting(identity.WorkerSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
		t.Fatal(err)
	}
	retainedPath := filepath.Join(storage.WorkingCopy.BackendPath, "keep.txt")
	if err := os.WriteFile(retainedPath, []byte("keep initial WorkingCopy"), 0o600); err != nil {
		t.Fatal(err)
	}
	siblingPath := filepath.Join(storage.BackendRoot, "runtimes", identity.RuntimeInstanceID+"-g2", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(siblingPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingPath, []byte("keep other generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mode == "reject-startup" {
		_, err := (workerapp.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time { return now }}).Activate(ctx, workerapp.ActivateResidentRuntimeCommand{
			TenantID: "tenant-local", WorkerSessionID: identity.WorkerSessionID, WorkerRunID: started.Ownership.WorkerRunID, AgentTurnID: started.Ownership.AgentTurnID,
			RuntimeInstanceID: identity.RuntimeInstanceID, ContainerID: "container-initial", RuntimeGeneration: 1, ControlChannelEpoch: 1,
			ExpectedSessionRevision: started.Ownership.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_initial_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled initial fence transaction failure'; END $$; CREATE TRIGGER reject_initial_fence BEFORE UPDATE ON runtime_control_channels FOR EACH ROW WHEN (NEW.status='fenced') EXECUTE FUNCTION reject_initial_fence()`); err != nil {
		t.Fatal(err)
	}
	newRunner := func() *runtimeRunner {
		runner := &runtimeRunner{pool: pool, environment: environment, hub: environment.hub, runtimes: make(map[string]*liveRuntime)}
		runner.configuration.Profile.TenantID, runner.configuration.Profile.ActorUserID = "tenant-local", "user-local"
		runner.reconciler = workerapp.UnownedRuntimeReconciler{Store: workerpostgres.NewRuntimeLossStore(pool), Cleaner: engine, Control: runner.hub, IDs: randomIDs{}, Now: func() time.Time { return now }}
		runner.cleanup = workerapp.RuntimeCleanupService{Store: workerpostgres.NewRuntimeCleanupStore(pool), Cleaner: engine, Control: runtimeObservationCleanupControl{WorkerControl: runner.hub, runner: runner}, IDs: randomIDs{}, Now: func() time.Time { return now }, RetryDelay: time.Second}
		return runner
	}
	runner := newRunner()
	expectedLossKey := "startup-failed/" + identity.RuntimeInstanceID
	switch mode {
	case "reject-unowned":
		if err := runner.rejectUnownedRuntimes(ctx); err != nil {
			t.Fatal(err)
		}
		expectedLossKey = "backend-restart/" + identity.RuntimeInstanceID + "/1"
	case "reject-startup":
		if err := runner.rejectStartupRuntime(ctx, startupRuntimeCandidate{Restore: liveRuntimeRestoreRequest{WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID, RuntimeGeneration: 1, ControlChannelEpoch: 1}}); err != nil {
			t.Fatal(err)
		}
		expectedLossKey = "backend-restart/" + identity.RuntimeInstanceID + "/1"
	default:
		if err := runner.reconcileFailedRuntimeStart(ctx, started.Ownership, startErr); err == nil {
			t.Fatal("startup failure was suppressed")
		}
	}
	var sessionStatus, runtimeStatus, lossKey string
	if err := pool.QueryRow(ctx, `SELECT session.status,runtime.status,runtime.loss_diagnostic->>'idempotency_key' FROM worker_sessions session JOIN runtime_instances runtime ON runtime.runtime_instance_id=session.active_runtime_instance_id WHERE session.worker_session_id=$1`, identity.WorkerSessionID).Scan(&sessionStatus, &runtimeStatus, &lossKey); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "lost" || runtimeStatus != "lost" || lossKey != expectedLossKey {
		t.Fatalf("failed-start owner not committed: %s %s %s", sessionStatus, runtimeStatus, lossKey)
	}
	readFacts := func() string {
		var facts string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(runtime.loss_diagnostic,run.start_snapshot,run.status,run.terminal_reason,turn.content,turn.status,turn.terminal_reason,turn.deadline_at,task.status,task.waiting_reason,task.result_refs)::text FROM runtime_instances runtime JOIN worker_runs run ON run.worker_run_id=runtime.worker_run_id JOIN agent_turns turn ON turn.worker_run_id=run.worker_run_id JOIN tasks task ON task.task_id=run.task_id WHERE runtime.runtime_instance_id=$1`, identity.RuntimeInstanceID).Scan(&facts); err != nil {
			t.Fatal(err)
		}
		return facts
	}
	originalFacts := readFacts()
	// Interrupt the first cleanup registration after lost has already committed.
	// No loss row is repaired; restart must rediscover it after DB recovery.
	if mode == "lost-before-cleanup-begin" {
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_initial_cleanup_begin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled cleanup Begin unavailable'; END $$; CREATE TRIGGER reject_initial_cleanup_begin BEFORE UPDATE ON runtime_instances FOR EACH ROW WHEN (NEW.status='draining') EXECUTE FUNCTION reject_initial_cleanup_begin()`); err != nil {
			t.Fatal(err)
		}
		if err := runner.cleanupTerminalRuntime(ctx); err == nil {
			t.Fatal("fixture did not fail cleanup Begin after committed loss")
		}
		var scheduled int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_commands WHERE command_type='runtime_cleanup_reconcile'`).Scan(&scheduled); err != nil {
			t.Fatal(err)
		}
		if scheduled != 0 {
			t.Fatal("failed Begin committed a cleanup command")
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER reject_initial_cleanup_begin ON runtime_instances; DROP FUNCTION reject_initial_cleanup_begin()`); err != nil {
			t.Fatal(err)
		}
	}
	// A new Backend has neither credentials nor prepared runtime identity.
	environment.hub = controlgrpc.NewHubWithJournal(journal)
	environment.prepared = make(map[string]preparedRuntime)
	runner = newRunner()
	if err := runner.cleanupTerminalRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_commands WHERE command_type='runtime_cleanup_reconcile' AND target_id=$1 AND status='pending'`, identity.RuntimeInstanceID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("failed initial startup registry has no durable cleanup owner after Backend restart: scheduled=%d", pending)
	}
	if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
		t.Fatal("unconfirmed cleanup erased original registry")
	}
	var dueBefore, dueAfter time.Time
	if err := pool.QueryRow(ctx, `SELECT due_at FROM scheduled_commands WHERE command_type='runtime_cleanup_reconcile' AND target_id=$1`, identity.RuntimeInstanceID).Scan(&dueBefore); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Millisecond)
	if err := runner.cleanupTerminalRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT due_at FROM scheduled_commands WHERE command_type='runtime_cleanup_reconcile' AND target_id=$1`, identity.RuntimeInstanceID).Scan(&dueAfter); err != nil {
		t.Fatal(err)
	}
	if !dueBefore.Equal(dueAfter) {
		t.Fatal("rediscovery changed scheduled cleanup backoff")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_initial_fence ON runtime_control_channels; DROP FUNCTION reject_initial_fence()`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	poller := platformpostgres.ScheduledCommandPoller{Pool: pool, Handler: workerpostgres.RuntimeCleanupCommandHandler{Service: runner.cleanup}, LeaseOwner: "initial-cleanup-test", LeaseDuration: time.Minute, BatchSize: 10, Now: func() time.Time { return now }, CommandTypes: []string{"runtime_cleanup_reconcile"}}
	if _, err := poller.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(storage.BackendRoot, "runtimes", identity.RuntimeInstanceID+"-g1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proven cleanup retained Runtime registry: %v", err)
	}
	if _, err := os.Stat(storage.WorkingCopy.BackendPath); err != nil {
		t.Fatal("cleanup removed WorkingCopy")
	}
	for path, expected := range map[string]string{retainedPath: "keep initial WorkingCopy", siblingPath: "keep other generation"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != expected {
			t.Fatalf("cleanup altered retained fixture %s: %v", path, err)
		}
	}
	var confirmed bool
	if err := pool.QueryRow(ctx, `SELECT session.status,runtime.status,runtime.cleanup_confirmed FROM runtime_instances runtime JOIN worker_sessions session ON session.worker_session_id=runtime.worker_session_id WHERE runtime.runtime_instance_id=$1`, identity.RuntimeInstanceID).Scan(&sessionStatus, &runtimeStatus, &confirmed); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "lost" || runtimeStatus != "terminated" || !confirmed || engine.provisions != 1 {
		t.Fatalf("cleanup changed lifecycle or started Runtime: session=%s runtime=%s confirmed=%v provisions=%d", sessionStatus, runtimeStatus, confirmed, engine.provisions)
	}
	if readFacts() != originalFacts {
		t.Fatal("cleanup changed loss, Task, Run or Turn facts")
	}
}

// Seed only pre-dispatch prerequisites; ownership and accepted preparation are
// produced by the real dispatch, StartWorkerRun and RuntimeCoordinator services.
func seedInitialCleanupStart(t *testing.T, pool *pgxpool.Pool, environment *runtimeEnvironment, now time.Time) workerapp.StartWorkerRunCommand {
	t.Helper()
	statements := []string{
		`INSERT INTO task_readiness_assessments (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at) VALUES ('assessment-initial','tenant-local','user-local','workspace-1','workspace-revision-1','api','direct-ready-v1','{}','[]','[]','ready_confirmed','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',$1)`,
		`INSERT INTO task_creation_intents (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,task_spec_digest,trigger_policy_revision,status,created_at,expires_at) VALUES ('intent-initial','tenant-local','user-local','workspace-1','workspace-revision-1','assessment-initial','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','direct-create-v1','active',$1,$1::timestamptz+interval '15 minutes')`,
		`INSERT INTO tasks (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,task_spec_digest,creation_intent_id,status,acceptance_mode,result_refs,revision,created_at,updated_at) VALUES ('task-initial','tenant-local','user-local','workspace-1','workspace-revision-1','{}','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','intent-initial','queued','manual','[]',1,$1,$1)`,
		`INSERT INTO worker_sessions (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,session_revision,runtime_generation,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at) VALUES ('session-initial','tenant-local','user-local','workspace-1','workspace-revision-1','working-copy-initial','interactive',1,1,1,$1::timestamptz+interval '48 hours',$1::timestamptz+interval '14 days',$1,$1)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(context.Background(), statement, now); err != nil {
			t.Fatal(err)
		}
	}
	dispatch := taskapp.SessionDispatchService{Store: taskpostgres.NewStore(pool), IDs: randomIDs{}, Now: func() time.Time { return now }, PermitTTL: time.Minute}
	if _, err := dispatch.EnqueueTask(context.Background(), taskapp.EnqueueTaskCommand{TenantID: "tenant-local", UserID: "user-local", TaskID: "task-initial", WorkerSessionID: "session-initial", ExpectedTaskRevision: 1, ExpectedSessionRevision: 1, IdempotencyKey: "enqueue-initial"}); err != nil {
		t.Fatal(err)
	}
	permit, err := dispatch.IssueTaskPermit(context.Background(), taskapp.IssueTaskPermitCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: "session-initial", RuntimeGeneration: 1, CapacityAdmissionRef: "capacity-initial", IdempotencyKey: "permit-initial"})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := environment.profiles.Agent(config.KimiCodexAnthropicV1)
	network, err := environment.networkProfiles.ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	return workerapp.StartWorkerRunCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: "session-initial", TaskID: "task-initial", PermitID: permit.PermitID, ExpectedSessionRevision: permit.ExpectedSessionRevision, RuntimeGeneration: 1, ControlChannelEpoch: 1, TriggerType: "task_initial", StartSnapshot: json.RawMessage(`{"working_copy":"initial-cleanup-fixture"}`), AgentTurnContent: json.RawMessage(`{"text":"initial cleanup fixture"}`), TurnIdempotencyKey: "initial-cleanup-turn", AgentProfileID: profile.ID, AgentProfileRevision: "1", AgentProfileDigest: profile.Digest, RuntimeImageRef: "sha256:" + strings.Repeat("a", 64), ResourceProfileID: config.LocalStandardV1, NetworkPolicyID: network.ID, NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest, NetworkEnforcementStatus: config.NetworkEnforcementActive}
}

type initialCleanupEngine struct {
	fake.RuntimeProvisioner
	succeed    bool
	provisions int
}

func (engine *initialCleanupEngine) Provision(_ context.Context, request workerapp.RuntimeProvisionRequest) (workerapp.ProvisionedRuntime, error) {
	engine.provisions++
	if engine.succeed {
		return workerapp.ProvisionedRuntime{RuntimeInstanceID: request.RuntimeInstanceID, Generation: request.Generation, ContainerID: "container-initial"}, nil
	}
	return workerapp.ProvisionedRuntime{}, errors.New("controlled initial container provision failure")
}
