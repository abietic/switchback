//go:build integration

package localproduct

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/client"
)

// Only the container engine is controlled. Recovery ownership, Bind/Fence,
// exhaustion and scheduled cleanup run through their actual PostgreSQL services.
func TestObservationRegistryCleanupRecoveryFailureHasDurableOwner(t *testing.T) {
	for _, name := range []string{"bound-fence-fails", "first-bind-not-committed", "exhausted-before-cleanup-registration", "terminal-missing-channel", "first-bind-deadline"} {
		missingBind := name == "first-bind-not-committed" || name == "terminal-missing-channel" || name == "first-bind-deadline"
		terminalMissing := name == "terminal-missing-channel"
		crashWindow := name == "exhausted-before-cleanup-registration"
		t.Run(name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Now().UTC()
			seedBackendDeadlineClaimGraph(t, pool, now.Add(time.Hour))
			environment, _, _, _ := newDeadlineCleanupTestEnvironment(t)
			configureCleanupPrepare(t, environment)
			profile, _ := environment.profiles.Agent(config.KimiCodexAnthropicV1)
			network, err := environment.networkProfiles.ResolveForDeployment(config.SingleUserLocalProfileName)
			if err != nil {
				t.Fatal(err)
			}
			// Complete the seed's original runtime profile/start evidence before
			// opening recovery; no business row is repaired after a failure.
			if _, err := pool.Exec(ctx, `UPDATE worker_runs SET agent_profile_id=$1,agent_profile_digest=$2,start_snapshot=jsonb_build_object('working_copy_checkpoint_digest',$3::text) WHERE worker_run_id='run-deadline-claim'`, profile.ID, profile.Digest, "sha256:"+strings.Repeat("a", 64)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE runtime_instances SET network_policy_revision=$1,network_policy_digest=$2,network_enforcement_status='active' WHERE runtime_instance_id='runtime-deadline-claim'`, network.Revision, network.Digest); err != nil {
				t.Fatal(err)
			}
			journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return now }}
			environment.hub = controlgrpc.NewHubWithJournal(journal)
			old := controlgrpc.RuntimeIdentity{WorkerSessionID: "session-deadline-claim", RuntimeInstanceID: "runtime-deadline-claim", RuntimeGeneration: 1, ChannelEpoch: 7}
			storage, err := environment.storage.Materialize(old.WorkerSessionID)
			if err != nil {
				t.Fatal(err)
			}
			materializeCleanupRegistry(t, environment, storage, old)
			engine := &recoveryCleanupEngine{pool: pool, rejectFence: !missingBind && !crashWindow}
			engine.CleanupResult = workerapp.RuntimeCleanupObservation{Confirmed: true, ObservedAt: now}
			absent := recoveryAbsentDocker{}
			dockerCleaner, err := dockeradapter.NewProvisioner(absent, dockeradapter.Options{Image: "switchback-worker@sha256:" + strings.Repeat("1", 64), Network: "switchback-worker-control", EngineDataRoot: environment.storage.EngineDataRoot, BackendGRPCTarget: "switchback-backend:9090", BackendHTTPBaseURL: "http://switchback-backend:8080", HostPathVerifier: absent})
			if err != nil {
				t.Fatal(err)
			}
			runner := &runtimeRunner{pool: pool, environment: environment, hub: environment.hub, provisioner: dockerCleaner, runtimes: make(map[string]*liveRuntime)}
			runner.configuration.Profile.TenantID, runner.configuration.Profile.ActorUserID = "tenant-local", "user-local"
			runner.cleanup = workerapp.RuntimeCleanupService{Store: workerpostgres.NewRuntimeCleanupStore(pool), Cleaner: engine, Control: runtimeObservationCleanupControl{WorkerControl: runner.hub, runner: runner}, IDs: randomIDs{}, Now: func() time.Time { return now }, RetryDelay: time.Second}
			recoveryStore := workerpostgres.NewRuntimeRecoveryStore(pool)
			recovery := workerapp.RuntimeRecoveryService{Store: recoveryStore, IDs: randomIDs{}, Now: func() time.Time { return now }}
			coordinator := workerapp.RuntimeRecoveryCoordinator{Recovery: recovery, Effects: localRuntimeRecoveryEffects{runner: runner}, Provisioner: engine, DeadlineSources: workerapp.TurnSettlementSourceService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: func() time.Time { return now }}}
			state, err := recovery.Open(ctx, workerapp.OpenRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: old.WorkerSessionID, WorkerRunID: "run-deadline-claim", AgentTurnID: "turn-deadline-claim", FailedRuntimeInstanceID: old.RuntimeInstanceID, FailedGeneration: 1, FailedChannelEpoch: 7, Trigger: domain.RecoveryTriggerContainerExit, EvidenceRef: "runtime-inspection/fixture", EvidenceAt: now, ExpectedSessionRevision: 4, ExpectedRunRevision: 2, ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 5, IdempotencyKey: "cleanup-recovery-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if missingBind {
				code := "40001"
				if terminalMissing {
					code = "42501"
				}
				if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_replacement_bind() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled first Bind transaction failure' USING ERRCODE='`+code+`'; END $$; CREATE TRIGGER reject_replacement_bind BEFORE INSERT ON runtime_control_channels FOR EACH ROW WHEN (NEW.runtime_generation=2) EXECUTE FUNCTION reject_replacement_bind()`); err != nil {
					t.Fatal(err)
				}
			}
			if crashWindow {
				if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_cleanup_begin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled cleanup registration unavailable'; END $$; CREATE TRIGGER reject_cleanup_begin BEFORE UPDATE ON runtime_instances FOR EACH ROW WHEN (NEW.status='draining' AND NEW.generation=2) EXECUTE FUNCTION reject_cleanup_begin()`); err != nil {
					t.Fatal(err)
				}
			}
			state, _, err = recoveryStore.FindActiveRuntimeRecovery(ctx, "tenant-local")
			if err != nil {
				t.Fatal(err)
			}
			state, reconcileErr := coordinator.Reconcile(ctx, state)
			identity := controlgrpc.RuntimeIdentity{WorkerSessionID: old.WorkerSessionID, RuntimeInstanceID: state.ReplacementRuntimeInstanceID, RuntimeGeneration: 2, ChannelEpoch: state.ReplacementChannelEpoch}
			if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
				t.Fatalf("unproven fence destroyed replacement registry: %v (reconcile: %v)", err, reconcileErr)
			}
			var pending int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_commands WHERE command_type='runtime_cleanup_reconcile' AND target_id=$1 AND status='pending'`, identity.RuntimeInstanceID).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			_, active, err := recoveryStore.FindActiveRuntimeRecovery(ctx, "tenant-local")
			if err != nil {
				t.Fatal(err)
			}
			if pending != 1 && !active && !crashWindow {
				t.Fatalf("replacement registry has no durable retry owner: phase=%s scheduled=%d reconcile=%v", state.Phase, pending, reconcileErr)
			}
			if missingBind && !terminalMissing && (state.Phase != domain.RecoveryProvisioning || pending != 0 || engine.provisions != 0 || !errors.Is(reconcileErr, workerapp.ErrRuntimePreparationRetryable)) {
				t.Fatalf("temporary Bind did not preserve preparation owner: phase=%s pending=%d provisions=%d err=%v", state.Phase, pending, engine.provisions, reconcileErr)
			}
			if terminalMissing && (state.Phase != domain.RecoveryExhausted || active || pending != 1) {
				t.Fatal("deterministic Bind failure did not remain terminal with scheduled cleanup")
			}
			if crashWindow && (state.Phase != domain.RecoveryExhausted || pending != 0 || reconcileErr == nil) {
				t.Fatal("fixture did not interrupt after exhaustion before cleanup registration")
			}
			if missingBind {
				if _, err := pool.Exec(ctx, `DROP TRIGGER reject_replacement_bind ON runtime_control_channels; DROP FUNCTION reject_replacement_bind()`); err != nil {
					t.Fatal(err)
				}
			} else if crashWindow {
				if _, err := pool.Exec(ctx, `DROP TRIGGER reject_cleanup_begin ON runtime_instances; DROP FUNCTION reject_cleanup_begin()`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := pool.Exec(ctx, `DROP TRIGGER reject_replacement_fence ON runtime_control_channels; DROP FUNCTION reject_replacement_fence()`); err != nil {
					t.Fatal(err)
				}
			}
			// Backend restart loses all prepared credentials. Production owners
			// alone must discover and settle the exact original preparation.
			environment.hub = controlgrpc.NewHubWithJournal(journal)
			environment.prepared = make(map[string]preparedRuntime)
			var originalBootstrap []byte
			if missingBind {
				originalBootstrap, err = os.ReadFile(filepath.Join(storage.BackendRoot, "runtimes", identity.RuntimeInstanceID+"-g2", "secret-input", "bootstrap.json"))
				if err != nil {
					t.Fatal(err)
				}
				engine.expectedBootstrap = originalBootstrap
				// Reopening the original preparation must not reread provider config.
				if err := os.Remove(environment.configuration.KimiCredentialFile); err != nil {
					t.Fatal(err)
				}
			}
			if name == "first-bind-deadline" {
				now = state.AgentTurnDeadlineAt
				next, found, err := recoveryStore.FindActiveRuntimeRecovery(ctx, "tenant-local")
				if err != nil || !found {
					t.Fatalf("lost preparation owner: found=%v err=%v", found, err)
				}
				result, err := coordinator.Reconcile(ctx, next)
				if err != nil || result.Phase != domain.RecoveryExhausted || result.OutcomeReason != "preempted_by_backend_deadline" || engine.provisions != 0 {
					t.Fatalf("deadline did not preempt preparation: phase=%s provisions=%d err=%v", result.Phase, engine.provisions, err)
				}
				if !result.AgentTurnDeadlineAt.Equal(state.AgentTurnDeadlineAt) {
					t.Fatal("preparation retry extended deadline")
				}
				if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
					t.Fatal("unproven deadline removed registry")
				}
				var sourceRef string
				var occurredAt time.Time
				if err := pool.QueryRow(ctx, `SELECT source_ref,source_occurred_at FROM worker_turn_settlements WHERE agent_turn_id='turn-deadline-claim' AND source_kind='backend_deadline'`).Scan(&sourceRef, &occurredAt); err != nil {
					t.Fatal(err)
				}
				if sourceRef != state.AgentTurnDeadlineSourceRef || !occurredAt.Equal(state.AgentTurnDeadlineAt) {
					t.Fatal("deadline preemption lost exact durable source owner")
				}
				return
			}
			if err := runner.cleanupTerminalRuntime(ctx); err != nil {
				t.Fatal(err)
			}
			if next, found, err := recoveryStore.FindActiveRuntimeRecovery(ctx, "tenant-local"); err != nil {
				t.Fatal(err)
			} else if found {
				if _, err := coordinator.Reconcile(ctx, next); err != nil {
					t.Fatal(err)
				}
			}
			now = now.Add(2 * time.Second)
			poller := platformpostgres.ScheduledCommandPoller{Pool: pool, Handler: workerpostgres.RuntimeCleanupCommandHandler{Service: runner.cleanup}, LeaseOwner: "cleanup-recovery-test", LeaseDuration: time.Minute, BatchSize: 10, Now: func() time.Time { return now }, CommandTypes: []string{"runtime_cleanup_reconcile"}}
			if _, err := poller.RunOnce(ctx); terminalMissing && !errors.Is(err, workerpostgres.ErrRuntimeCleanupUnconfirmed) {
				t.Fatalf("missing proof retry error=%v", err)
			} else if !terminalMissing && err != nil {
				t.Fatal(err)
			}
			if terminalMissing {
				var diagnostic, status string
				var confirmed bool
				var dueAt time.Time
				if err := pool.QueryRow(ctx, `SELECT runtime.status,runtime.cleanup_confirmed,runtime.cleanup_diagnostic->>'error_code',command.due_at FROM runtime_instances runtime JOIN scheduled_commands command ON command.target_id=runtime.runtime_instance_id AND command.command_type='runtime_cleanup_reconcile' WHERE runtime.runtime_instance_id=$1`, identity.RuntimeInstanceID).Scan(&status, &confirmed, &diagnostic, &dueAt); err != nil {
					t.Fatal(err)
				}
				if status != "draining" || confirmed || diagnostic != "runtime_cleanup_fence_unconfirmed" || !dueAt.After(now) {
					t.Fatalf("missing proof was not visibly deferred: status=%s confirmed=%v diagnostic=%s due=%s", status, confirmed, diagnostic, dueAt)
				}
				before := dueAt
				if err := runner.cleanupTerminalRuntime(ctx); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT due_at FROM scheduled_commands WHERE target_id=$1 AND command_type='runtime_cleanup_reconcile'`, identity.RuntimeInstanceID).Scan(&dueAt); err != nil {
					t.Fatal(err)
				}
				if !dueAt.Equal(before) {
					t.Fatal("rediscovery overwrote pending cleanup backoff")
				}
				bootstrap, err := os.ReadFile(filepath.Join(storage.BackendRoot, "runtimes", identity.RuntimeInstanceID+"-g2", "secret-input", "bootstrap.json"))
				if err != nil || string(bootstrap) != string(originalBootstrap) {
					t.Fatal("terminal missing-proof retry altered original bootstrap")
				}
				if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
					t.Fatal("terminal missing-proof retry deleted registry")
				}
				if engine.provisions != 0 {
					t.Fatal("terminal missing-proof cleanup provisioned a Runtime")
				}
				var channels int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_control_channels WHERE runtime_instance_id=$1`, identity.RuntimeInstanceID).Scan(&channels); err != nil {
					t.Fatal(err)
				}
				if channels != 0 {
					t.Fatal("terminal cleanup invented a missing channel")
				}
				return
			}
			if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err == nil {
				t.Fatal("production recovery owners leaked replacement registry")
			}
			if _, err := os.Stat(storage.WorkingCopy.BackendPath); err != nil {
				t.Fatal("cleanup removed WorkingCopy")
			}
			if engine.provisions != 1 {
				t.Fatalf("container provisions=%d, want exactly one attempted replacement", engine.provisions)
			}
			if missingBind && !engine.bootstrapPreserved {
				t.Fatal("retry did not use the exact original bootstrap")
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_instances WHERE worker_session_id=$1`, old.WorkerSessionID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Fatalf("runtime identities=%d, want old plus one reserved replacement", count)
			}
		})
	}
}

type recoveryAbsentDocker struct{ dockeradapter.Engine }

func (recoveryAbsentDocker) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
}
func (recoveryAbsentDocker) VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
}
func (recoveryAbsentDocker) VerifyHostPaths(context.Context, string, []dockeradapter.HostPathRequirement) error {
	return nil
}

type recoveryCleanupEngine struct {
	fake.RuntimeProvisioner
	pool               *pgxpool.Pool
	rejectFence        bool
	provisions         int
	expectedBootstrap  []byte
	bootstrapPreserved bool
}

func (engine *recoveryCleanupEngine) Provision(ctx context.Context, request workerapp.RuntimeProvisionRequest) (workerapp.ProvisionedRuntime, error) {
	engine.provisions++
	if engine.expectedBootstrap != nil {
		content, err := os.ReadFile(request.Secrets.Bootstrap.BackendPath)
		engine.bootstrapPreserved = err == nil && string(content) == string(engine.expectedBootstrap)
	}
	if engine.rejectFence {
		if _, err := engine.pool.Exec(ctx, `CREATE FUNCTION reject_replacement_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled replacement fence failure'; END $$; CREATE TRIGGER reject_replacement_fence BEFORE UPDATE ON runtime_control_channels FOR EACH ROW WHEN (NEW.status='fenced' AND NEW.runtime_generation=2) EXECUTE FUNCTION reject_replacement_fence()`); err != nil {
			return workerapp.ProvisionedRuntime{}, err
		}
	}
	return workerapp.ProvisionedRuntime{}, errors.New("controlled container provision failure")
}
