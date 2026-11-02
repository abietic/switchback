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
	"github.com/abietic/switchback/internal/platform/secretcheck"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestObservationRegistryCleanupUnconfirmedFencePreservesRegistryAndRetry(t *testing.T) {
	for _, entry := range []string{"discard", "recovery"} {
		t.Run(entry, func(t *testing.T) {
			environment, journal, storage, identity := newDeadlineCleanupTestEnvironment(t)
			materializeCleanupRegistry(t, environment, storage, identity)
			journal.fenceErr = errors.New("controlled durable store failure")
			var err error
			if entry == "discard" {
				err = environment.discard(identity)
			} else {
				_, err = environment.fenceFailedRuntime(workerapp.RuntimeRecoveryState{WorkerSessionID: identity.WorkerSessionID, FailedRuntimeInstanceID: identity.RuntimeInstanceID, FailedGeneration: uint64(identity.RuntimeGeneration), FailedChannelEpoch: identity.ChannelEpoch})
			}
			if err == nil {
				t.Error("unconfirmed fence claimed cleanup success")
			}
			if _, ok := environment.get(identity.RuntimeInstanceID); !ok {
				t.Error("lost retry identity before proven fence")
			}
			if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
				t.Fatalf("unconfirmed fence destroyed verifiable registry: %v", err)
			}
		})
	}
}

func TestObservationRegistryCleanupScannerBarriers(t *testing.T) {
	for _, acquiredFirst := range []bool{false, true} {
		name := "cleanup-before-scanner"
		if acquiredFirst {
			name = "scanner-before-cleanup"
		}
		t.Run(name, func(t *testing.T) {
			environment, _, storage, identity := newDeadlineCleanupTestEnvironment(t)
			materializeCleanupRegistry(t, environment, storage, identity)
			ready, proceed := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				if !acquiredFirst {
					close(ready)
					<-proceed
				}
				scanner, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity))
				if acquiredFirst {
					close(ready)
					<-proceed
				}
				if err == nil {
					err = scanner.ScanJSON(context.Background(), "test", map[string]string{"value": "cleanup-fixture-provider-token-123456"})
				}
				done <- err
			}()
			<-ready
			if err := environment.discard(identity); err != nil {
				close(proceed)
				t.Fatal(err)
			}
			close(proceed)
			err := <-done
			if acquiredFirst && !secretcheck.IsExposure(err) {
				t.Fatalf("bounded original scanner lost token: %v", err)
			}
			if !acquiredFirst && (err == nil || secretcheck.IsExposure(err)) {
				t.Fatalf("missing registry supplied a scanner: %v", err)
			}
			if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err == nil {
				t.Fatal("original registry still available")
			}
			if _, err := os.Stat(storage.WorkingCopy.BackendPath); err != nil {
				t.Fatal("WorkingCopy was removed")
			}
		})
	}
}

func TestObservationRegistryCleanupDiscardAfterRestart(t *testing.T) {
	environment, journal, storage, identity := newDeadlineCleanupTestEnvironment(t)
	materializeCleanupRegistry(t, environment, storage, identity)
	environment.prepared = make(map[string]preparedRuntime)
	environment.hub = controlgrpc.NewHubWithJournal(workerapp.RuntimeControlJournalService{Store: journal, Now: time.Now})
	if err := environment.discard(identity); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err == nil {
		t.Fatal("discard lost original registry after Backend restart")
	}
}

func TestObservationRegistryCleanupRejectsIncompleteOrMismatchedProof(t *testing.T) {
	mutations := map[string]func(*workerapp.RuntimeControlChannelState){
		"empty":                 func(s *workerapp.RuntimeControlChannelState) { *s = workerapp.RuntimeControlChannelState{} },
		"session":               func(s *workerapp.RuntimeControlChannelState) { s.WorkerSessionID = "other" },
		"runtime":               func(s *workerapp.RuntimeControlChannelState) { s.RuntimeInstanceID = "other" },
		"generation":            func(s *workerapp.RuntimeControlChannelState) { s.RuntimeGeneration++ },
		"epoch":                 func(s *workerapp.RuntimeControlChannelState) { s.ChannelEpoch++ },
		"connected":             func(s *workerapp.RuntimeControlChannelState) { s.Status = "connected" },
		"time":                  func(s *workerapp.RuntimeControlChannelState) { s.FencedAt = time.Time{} },
		"revision":              func(s *workerapp.RuntimeControlChannelState) { s.FenceRevision = 0 },
		"future-fence-revision": func(s *workerapp.RuntimeControlChannelState) { s.FenceRevision++ },
		"owner":                 func(s *workerapp.RuntimeControlChannelState) { s.FenceOwnerFence = 0 },
		"stale-owner":           func(s *workerapp.RuntimeControlChannelState) { s.OwnerFence++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			environment, journal, storage, identity := newDeadlineCleanupTestEnvironment(t)
			materializeCleanupRegistry(t, environment, storage, identity)
			state := deadlineCleanupFence(t, environment, journal, identity)
			mutate(&state)
			if err := environment.cleanupFencedObservationRegistry(context.Background(), identity, state); !errors.Is(err, errObservationCleanupFenceUnproven) {
				t.Fatalf("unsafe proof error=%v", err)
			}
			if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObservationRegistryCleanupRejectsRedirectedRuntimeParent(t *testing.T) {
	environment, journal, storage, identity := newDeadlineCleanupTestEnvironment(t)
	materializeCleanupRegistry(t, environment, storage, identity)
	state := deadlineCleanupFence(t, environment, journal, identity)
	parent := filepath.Join(storage.BackendRoot, "runtimes")
	moved := filepath.Join(storage.BackendRoot, "preserved")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, parent); err != nil {
		t.Fatal(err)
	}
	if err := environment.cleanupFencedObservationRegistry(context.Background(), identity, state); err == nil {
		t.Fatal("redirected Runtime parent accepted")
	}
	if _, err := os.Stat(filepath.Join(moved, "runtime-1-g7", "control-plane", "observation-tokens.json")); err != nil {
		t.Fatalf("redirected cleanup destroyed original: %v", err)
	}
}

func materializeCleanupRegistry(t *testing.T, environment *runtimeEnvironment, storage workerapp.SessionStorage, identity controlgrpc.RuntimeIdentity) {
	t.Helper()
	environment.secrets = runtimesecret.Materializer{
		Ownership:        runtimesecret.OwnershipFunc(func(string, int, int) error { return nil }),
		OwnerUID:         func(string) (uint32, error) { return 0, nil },
		OpenFileOwnerUID: func(*os.File) (uint32, error) { return 0, nil },
	}
	credential := deadlineCleanupCredential(t, identity)
	if err := environment.hub.RegisterCredential(credential); err != nil {
		t.Fatal(err)
	}
	resources, err := environment.secrets.Materialize(runtimesecret.Request{
		WorkerSessionID: identity.WorkerSessionID, WorkerRunID: "run-1", RuntimeInstanceID: identity.RuntimeInstanceID,
		Generation: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch, WorkerControlToken: credential.Token,
		SessionStorage:     storage,
		AgentProfile:       workerapp.ProfileRef{ID: "kimi-codex-anthropic-v1", Revision: 1, Digest: "sha256:2ba4c6870eb74c52515b54de6ef89dece1955893a4c7b0efb3d632c977e6eaf2"},
		ProviderCredential: runtimesecret.ProviderCredential([]byte("cleanup-fixture-provider-token-123456")),
	})
	if err != nil {
		t.Fatal(err)
	}
	environment.prepared[identity.RuntimeInstanceID] = preparedRuntime{Storage: storage, Credential: credential, Secrets: resources}
	if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
		t.Fatal(err)
	}
}

func cleanupRegistryRequest(storage workerapp.SessionStorage, identity controlgrpc.RuntimeIdentity) runtimesecret.ObservationRegistryRequest {
	return runtimesecret.ObservationRegistryRequest{WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID, Generation: uint64(identity.RuntimeGeneration), SessionStorage: storage}
}

func TestRuntimeEnvironmentFailedPrepareMaterializationBoundary(t *testing.T) {
	for _, materialized := range []bool{false, true} {
		name := "before-materialization"
		if materialized {
			name = "after-materialization"
		}
		t.Run(name, func(t *testing.T) {
			environment, journal, existing, oldIdentity := newDeadlineCleanupTestEnvironment(t)
			materializeCleanupRegistry(t, environment, existing, oldIdentity)
			configureCleanupPrepare(t, environment)
			identity := controlgrpc.RuntimeIdentity{WorkerSessionID: "session-failed", RuntimeInstanceID: "runtime-failed", RuntimeGeneration: 1, ChannelEpoch: 1}
			profile, _ := environment.profiles.Agent(config.KimiCodexAnthropicV1)
			network, err := environment.networkProfiles.ResolveForDeployment(config.SingleUserLocalProfileName)
			if err != nil {
				t.Fatal(err)
			}
			request := workerapp.RuntimePreparationRequest{
				Ownership: workerapp.StartedWorkerRun{WorkerSessionID: identity.WorkerSessionID, WorkerRunID: "run-failed", RuntimeInstanceID: identity.RuntimeInstanceID, RuntimeGeneration: 1, ControlChannelEpoch: 1},
				Command:   workerapp.StartWorkerRunCommand{AgentProfileID: profile.ID, AgentProfileDigest: profile.Digest, ResourceProfileID: config.LocalStandardV1, NetworkPolicyID: network.ID, NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest, NetworkEnforcementStatus: config.NetworkEnforcementActive},
			}
			journal.fenceErr = errors.New("controlled fence failure")
			if materialized {
				// Duplicate prepared registration is the existing failure boundary
				// after Materialize has sealed the original scope registry.
				environment.prepared[identity.RuntimeInstanceID] = preparedRuntime{Credential: controlgrpc.Credential{Identity: identity}}
			} else {
				environment.configuration.KimiCredentialFile = filepath.Join(t.TempDir(), "missing")
			}
			if _, err := environment.PrepareRuntime(context.Background(), request); err == nil {
				t.Fatal("controlled prepare failure disappeared")
			}
			if materialized {
				storage, err := environment.storage.OpenExisting(identity.WorkerSessionID)
				if err != nil {
					t.Fatalf("outer prepare defer erased accepted registry: %v", err)
				}
				if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
					t.Fatal(err)
				}
				journal.fenceErr = nil
				if err := environment.discard(identity); err != nil {
					t.Fatal(err)
				}
				if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err == nil {
					t.Fatal("retry did not remove registry")
				}
			} else {
				if _, err := os.Lstat(filepath.Join(environment.storage.BackendDataRoot, "sessions", identity.WorkerSessionID)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unmaterialized failed Session storage remains: %v", err)
				}
				if err := environment.discard(identity); err != nil {
					t.Fatalf("nonexistent scope demanded invented proof: %v", err)
				}
			}
		})
	}
}

func TestRuntimeEnvironmentReplacementRegistrationFailureRetainsOriginalScopeForRetry(t *testing.T) {
	environment, journal, storage, oldIdentity := newDeadlineCleanupTestEnvironment(t)
	materializeCleanupRegistry(t, environment, storage, oldIdentity)
	configureCleanupPrepare(t, environment)
	profile, _ := environment.profiles.Agent(config.KimiCodexAnthropicV1)
	network, err := environment.networkProfiles.ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	state := workerapp.RuntimeRecoveryState{Phase: "provisioning", WorkerSessionID: oldIdentity.WorkerSessionID, RuntimeWorkerRunID: "run-1", AgentSessionID: "agent-session-1", FailedGeneration: 7, FailedChannelEpoch: 11,
		ReplacementRuntimeInstanceID: "runtime-replacement", ReplacementGeneration: 8, ReplacementChannelEpoch: 12,
		AgentProfileID: profile.ID, AgentProfileRevision: "1", AgentProfileDigest: profile.Digest, ResourceProfileID: config.LocalStandardV1,
		NetworkPolicyID: network.ID, NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest, NetworkEnforcementStatus: config.NetworkEnforcementActive,
		WorkingCopyCheckpointDigest: "sha256:" + strings.Repeat("a", 64)}
	journal.bindErr = errors.New("controlled Bind transaction failure")
	journal.fenceErr = errors.New("controlled missing channel after failed Bind")
	if _, err := environment.PrepareReplacementRuntime(context.Background(), state); err == nil {
		t.Fatal("failed registration accepted")
	}
	identity := controlgrpc.RuntimeIdentity{WorkerSessionID: oldIdentity.WorkerSessionID, RuntimeInstanceID: state.ReplacementRuntimeInstanceID, RuntimeGeneration: 8, ChannelEpoch: 12}
	if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, identity)); err != nil {
		t.Fatal("failed Bind discarded original materialized scope")
	}
	journal.bindErr, journal.fenceErr = nil, nil
	if _, err := environment.PrepareReplacementRuntime(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if err := environment.discard(identity); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.secrets.ObservationScanner(cleanupRegistryRequest(storage, oldIdentity)); err != nil {
		t.Fatal("replacement cleanup deleted original generation")
	}
}

func configureCleanupPrepare(t *testing.T, environment *runtimeEnvironment) {
	t.Helper()
	environment.profiles = config.ProductionProfileRegistry()
	environment.networkProfiles = config.ProductionRuntimeNetworkProfiles()
	environment.configuration.WorkspaceSourceRoot = t.TempDir()
	environment.configuration.KimiCredentialFile = filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(environment.configuration.KimiCredentialFile, []byte("cleanup-fixture-provider-token-123456"), 0o600); err != nil {
		t.Fatal(err)
	}
}
