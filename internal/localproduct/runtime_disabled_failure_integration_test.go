//go:build integration

package localproduct

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestDisabledRunnerDetectsMissingRuntime(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedBackendDeadlineClaimGraph(t, pool, now.Add(time.Hour))
	absent := &disabledFailureDocker{missing: true}
	provisioner, err := dockeradapter.NewProvisioner(absent, dockeradapter.Options{Image: "switchback-worker@sha256:" + strings.Repeat("1", 64), Network: "switchback-worker-control", EngineDataRoot: t.TempDir(), BackendGRPCTarget: "switchback-backend:9090", BackendHTTPBaseURL: "http://switchback-backend:8080", HostPathVerifier: absent})
	if err != nil {
		t.Fatal(err)
	}
	store := workerpostgres.NewRuntimeRecoveryStore(pool)
	runner := &runtimeRunner{pool: pool, provisioner: provisioner, recoveryStore: store, failures: workerpostgres.NewGatedRuntimeFailureObserver(pool, workerapp.RuntimeRecoveryService{Store: store, IDs: randomIDs{}, Now: time.Now}, false)}
	runner.configuration.Profile.TenantID, runner.configuration.Profile.ActorUserID = "tenant-local", "user-local"
	if err := runner.detectRuntimeFailures(ctx); err != nil {
		t.Fatal(err)
	}
	_, found, err := store.FindTerminalRuntimeFailureForTurn(ctx, "tenant-local", "session-deadline-claim", "run-deadline-claim", "turn-deadline-claim", "runtime-deadline-claim", 1, 7)
	if err != nil || !found {
		t.Fatalf("disabled runner lost hard-missing evidence: found=%t err=%v", found, err)
	}
	// A restarted waiter only has durable state and a closed transport. It must
	// hand off to the Worker failure owner, without inventing Runtime terminality.
	runner.settlements = workerapp.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: time.Now}
	events := make(chan workerapp.WorkerControlEvent)
	close(events)
	live := &liveRuntime{identity: controlgrpc.RuntimeIdentity{WorkerSessionID: "session-deadline-claim", RuntimeInstanceID: "runtime-deadline-claim", RuntimeGeneration: 1, ChannelEpoch: 7}, events: events}
	turn := pendingTurn{TenantID: "tenant-local", WorkerSessionID: "session-deadline-claim", WorkerRunID: "run-deadline-claim", AgentTurnID: "turn-deadline-claim", RuntimeInstanceID: "runtime-deadline-claim", RuntimeGeneration: 1}
	result, err := runner.waitTurnTerminal(ctx, live, turn, nil)
	if !errors.Is(err, errRuntimeRecoverySettled) || result.sourceEvent != nil {
		t.Fatalf("restarted waiter lost durable failure: result=%+v err=%v", result, err)
	}
	if err := runner.dispatchPendingInterrupt(ctx, live, turn); !errors.Is(err, errRuntimeRecoverySettled) {
		t.Fatalf("dead interrupt did not hand off: %v", err)
	}
	// Rediscover the committed incident after losing in-memory state. A missing
	// channel must not be fabricated into confirmed external fencing.
	runner.hub = controlgrpc.NewHubWithJournal(workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: time.Now})
	runner.cleanup = workerapp.RuntimeCleanupService{Store: workerpostgres.NewRuntimeCleanupStore(pool), Cleaner: provisioner, Control: runner.hub, IDs: randomIDs{}, Now: time.Now, RetryDelay: time.Millisecond}
	if err := runner.cleanupTerminalRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	var scheduledID string
	var confirmed bool
	if err := pool.QueryRow(ctx, `SELECT command.scheduled_command_id,runtime.cleanup_confirmed FROM scheduled_commands command JOIN runtime_instances runtime ON runtime.runtime_instance_id=command.target_id WHERE command.command_type='runtime_cleanup_reconcile' AND runtime.runtime_instance_id='runtime-deadline-claim' AND command.status='pending'`).Scan(&scheduledID, &confirmed); err != nil || confirmed {
		t.Fatalf("cleanup lost uncertainty/intent: confirmed=%t err=%v", confirmed, err)
	}
	poller := platformpostgres.ScheduledCommandPoller{Pool: pool, Handler: workerpostgres.RuntimeCleanupCommandHandler{Service: runner.cleanup}, LeaseOwner: "disabled-failure-restart", LeaseDuration: time.Minute, BatchSize: 1, Now: func() time.Time { return time.Now().Add(time.Second) }, CommandTypes: []string{"runtime_cleanup_reconcile"}}
	if _, err := poller.RunOnce(ctx); !errors.Is(err, workerpostgres.ErrRuntimeCleanupUnconfirmed) {
		t.Fatalf("unconfirmed retry=%v", err)
	}
	var count, runtimes int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM scheduled_commands WHERE command_type='runtime_cleanup_reconcile' AND scheduled_command_id=$1),(SELECT count(*) FROM runtime_instances)`, scheduledID).Scan(&count, &runtimes); err != nil || count != 1 || runtimes != 1 || absent.creates != 0 {
		t.Fatalf("restart created new authority: intents=%d runtimes=%d creates=%d err=%v", count, runtimes, absent.creates, err)
	}
}

func TestDisabledRunnerRejectsNonFailureInspection(t *testing.T) {
	for _, scenario := range []string{"alive-transport-closed", "inspection-error", "wrong-container", "wrong-generation"} {
		t.Run(scenario, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			seedBackendDeadlineClaimGraph(t, pool, time.Now().Add(time.Hour))
			engine := &disabledFailureDocker{running: scenario == "alive-transport-closed", containerID: "container-deadline-claim", generation: "1"}
			if scenario == "inspection-error" {
				engine.err = errors.New("controlled Docker inspection unavailable")
			}
			if scenario == "wrong-container" {
				engine.containerID = "other-container"
			}
			if scenario == "wrong-generation" {
				engine.generation = "2"
			}
			provisioner, err := dockeradapter.NewProvisioner(engine, dockeradapter.Options{Image: "switchback-worker@sha256:" + strings.Repeat("1", 64), Network: "switchback-worker-control", EngineDataRoot: t.TempDir(), BackendGRPCTarget: "switchback-backend:9090", BackendHTTPBaseURL: "http://switchback-backend:8080", HostPathVerifier: engine})
			if err != nil {
				t.Fatal(err)
			}
			store := workerpostgres.NewRuntimeRecoveryStore(pool)
			runner := &runtimeRunner{pool: pool, provisioner: provisioner, recoveryStore: store, failures: workerpostgres.NewGatedRuntimeFailureObserver(pool, workerapp.RuntimeRecoveryService{Store: store, IDs: randomIDs{}, Now: time.Now}, false)}
			runner.configuration.Profile.TenantID, runner.configuration.Profile.ActorUserID = "tenant-local", "user-local"
			err = runner.detectRuntimeFailures(ctx)
			if (err != nil) != (scenario != "alive-transport-closed") {
				t.Fatalf("inspection error=%v", err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_recovery_incidents`).Scan(&count); err != nil || count != 0 || engine.creates != 0 {
				t.Fatalf("nonfailure became incident: count=%d creates=%d err=%v", count, engine.creates, err)
			}
			if scenario == "alive-transport-closed" {
				runner.settlements = workerapp.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: time.Now}
				events := make(chan workerapp.WorkerControlEvent)
				close(events)
				_, err := runner.waitTurnTerminal(ctx, &liveRuntime{identity: controlgrpc.RuntimeIdentity{ChannelEpoch: 7}, events: events}, pendingTurn{TenantID: "tenant-local", WorkerSessionID: "session-deadline-claim", WorkerRunID: "run-deadline-claim", AgentTurnID: "turn-deadline-claim", RuntimeInstanceID: "runtime-deadline-claim", RuntimeGeneration: 1}, nil)
				if err == nil || errors.Is(err, errRuntimeRecoverySettled) {
					t.Fatalf("transport close was terminal failure: %v", err)
				}
			}
		})
	}
}

type disabledFailureDocker struct {
	recoveryAbsentDocker
	missing, running        bool
	containerID, generation string
	err                     error
	creates                 int
}

func (engine *disabledFailureDocker) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if engine.err != nil {
		return client.ContainerInspectResult{}, engine.err
	}
	if engine.missing {
		return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{ID: engine.containerID, Config: &container.Config{Labels: map[string]string{"com.switchback.runtime-instance-id": "runtime-deadline-claim", "com.switchback.runtime-generation": engine.generation}}, State: &container.State{Running: engine.running, ExitCode: 1}}}, nil
}
func (engine *disabledFailureDocker) ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	engine.creates++
	return client.ContainerCreateResult{}, errors.New("unexpected replacement provision")
}
