//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestObservationRegistryCleanupRealJournalTerminalAndRestart(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	seedBackendDeadlineClaimGraph(t, pool, now)
	environment, _, _, _ := newDeadlineCleanupTestEnvironment(t)
	identity := controlgrpc.RuntimeIdentity{WorkerSessionID: "session-deadline-claim", RuntimeInstanceID: "runtime-deadline-claim", RuntimeGeneration: 1, ChannelEpoch: 7}
	storage, err := environment.storage.Materialize(identity.WorkerSessionID)
	if err != nil {
		t.Fatal(err)
	}
	journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return now }}
	environment.hub = controlgrpc.NewHubWithJournal(journal)
	materializeCleanupRegistry(t, environment, storage, identity)
	runner := &runtimeRunner{pool: pool, environment: environment, runtimes: make(map[string]*liveRuntime)}
	runner.configuration.Profile.TenantID = "tenant-local"
	runner.configuration.Profile.ActorUserID = "user-local"
	cleaner := &fake.RuntimeProvisioner{CleanupResult: workerapp.RuntimeCleanupObservation{Confirmed: true, ObservedAt: now}}
	runner.cleanup = workerapp.RuntimeCleanupService{Store: workerpostgres.NewRuntimeCleanupStore(pool), Cleaner: cleaner,
		Control: runtimeObservationCleanupControl{WorkerControl: environment.hub, runner: runner}, IDs: randomIDs{}, Now: func() time.Time { return now }, RetryDelay: time.Minute}
	// Ordinary Turn terminal and resident waiting_user/idle do not authorize
	// Session registry cleanup, even when the original Turn already ended.
	if _, err := pool.Exec(ctx, `UPDATE agent_turns SET status='completed',terminal_at=$1 WHERE agent_turn_id='turn-deadline-claim'`, now); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "waiting_user", "idle"} {
		if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status=$1 WHERE worker_session_id=$2`, status, identity.WorkerSessionID); err != nil {
			t.Fatal(err)
		}
		if err := runner.cleanupTerminalRuntime(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
			t.Fatalf("resident %s lost registry: %v", status, err)
		}
	}
	if len(cleaner.CleanupRequests) != 0 {
		t.Fatal("resident Runtime was cleaned")
	}
	// A real PostgreSQL trigger fails the fence transaction after identity lookup.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_cleanup_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled fence commit failure'; END $$; CREATE TRIGGER reject_cleanup_fence BEFORE UPDATE ON runtime_control_channels FOR EACH ROW WHEN (NEW.status='fenced') EXECUTE FUNCTION reject_cleanup_fence()`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status='closed',terminal_at=$2 WHERE worker_session_id=$1`, identity.WorkerSessionID, now); err != nil {
		t.Fatal(err)
	}
	if err := runner.cleanupTerminalRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	var confirmed bool
	var status string
	if err := pool.QueryRow(ctx, `SELECT status,cleanup_confirmed FROM runtime_instances WHERE runtime_instance_id=$1`, identity.RuntimeInstanceID).Scan(&status, &confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed || status != "draining" {
		t.Fatalf("uncommitted fence completed cleanup: %s %v", status, confirmed)
	}
	if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
		t.Fatal("PG fence failure destroyed registry")
	}
	if _, found := environment.get(identity.RuntimeInstanceID); !found {
		t.Fatal("PG fence failure lost retry identity")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_cleanup_fence ON runtime_control_channels; DROP FUNCTION reject_cleanup_fence()`); err != nil {
		t.Fatal(err)
	}
	// Restart discards only ephemeral maps; retry resolves durable identity and
	// never restores credentials. No publisher/provider/HTTP is installed.
	environment.hub = controlgrpc.NewHubWithJournal(journal)
	environment.prepared = make(map[string]preparedRuntime)
	var lease workerapp.RuntimeCleanupLease
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM scheduled_commands WHERE command_type='runtime_cleanup_reconcile' AND target_id=$1`, identity.RuntimeInstanceID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &lease); err != nil {
		t.Fatal(err)
	}
	result, err := runner.cleanup.Reconcile(ctx, lease)
	if err != nil || !result.Confirmed {
		t.Fatalf("restart reconciliation=%+v err=%v", result, err)
	}
	if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err == nil {
		t.Fatal("terminal registry leaked")
	}
	if _, err := os.Stat(storage.WorkingCopy.BackendPath); err != nil {
		t.Fatal("restart cleanup removed WorkingCopy")
	}
	// Fenced-channel deadline evidence may advance the current revision without
	// moving the original fence revision or owner; that proof remains valid.
	if _, err := pool.Exec(ctx, `UPDATE runtime_control_channels SET revision=revision+1 WHERE runtime_instance_id=$1`, identity.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	proof, err := runner.readObservationCleanupFence(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	// Backend deadline cleanup consumes the same durable read after restart;
	// it does not require a second Fence or an in-memory credential.
	if _, err := environment.cleanupDurablyFencedRuntime(deadlineCleanupAuthority(identity), proof); err != nil {
		t.Fatal(err)
	}
}
