package localproduct

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	app "github.com/abietic/switchback/internal/workerruntime/application"
)

func runtimePlanFixture(t *testing.T) (*runtimeEnvironment, app.RuntimePreparationRequest, *sourcePreflightJournal) {
	t.Helper()
	journal := &sourcePreflightJournal{}
	environment := &runtimeEnvironment{
		hub: controlgrpc.NewHubWithJournal(app.RuntimeControlJournalService{Store: journal, Now: time.Now}),
		storage: localstorage.Materializer{
			BackendDataRoot: t.TempDir(), EngineDataRoot: filepath.Join(t.TempDir(), "engine"),
			Ownership: localstorage.OwnershipFunc(func(string, int, int) error { return nil }),
		},
		prepared: map[string]preparedRuntime{},
		secrets: runtimesecret.Materializer{
			Ownership:        runtimesecret.OwnershipFunc(func(string, int, int) error { return nil }),
			OwnerUID:         func(string) (uint32, error) { return 0, nil },
			OpenFileOwnerUID: func(*os.File) (uint32, error) { return 0, nil },
		},
	}
	configureCleanupPrepare(t, environment)
	profile, _ := environment.profiles.Agent(config.KimiCodexAnthropicV1)
	network, err := environment.networkProfiles.ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	request := app.RuntimePreparationRequest{Ownership: app.StartedWorkerRun{WorkerSessionID: "session-plan", WorkerRunID: "run-plan", RuntimeInstanceID: "runtime-plan", RuntimeGeneration: 1, ControlChannelEpoch: 1}, Command: app.StartWorkerRunCommand{AgentProfileID: profile.ID, AgentProfileDigest: profile.Digest, ResourceProfileID: config.LocalStandardV1, NetworkPolicyID: network.ID, NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest, NetworkEnforcementStatus: config.NetworkEnforcementActive}}
	return environment, request, journal
}
func TestRuntimePreparationPlanReadonlyAndSingleUse(t *testing.T) {
	e, r, j := runtimePlanFixture(t)
	// Planning must not even attempt to read a provider credential.
	credential := e.configuration.KimiCredentialFile
	e.configuration.KimiCredentialFile = filepath.Join(t.TempDir(), "missing")
	p, err := e.PlanRuntime(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	binding := p.SourceBinding()
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	assertSourceAbsent(t, filepath.Join(e.storage.BackendDataRoot, "sessions"))
	if j.binds != 0 || len(e.prepared) != 0 {
		t.Fatal("readonly plan created credentials/session")
	}
	e.configuration.KimiCredentialFile = credential
	prepared, err := p.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := binding.LegacyFileDigest()
	if prepared.SourcePlanDigest != binding.Digest || e.prepared[r.Ownership.RuntimeInstanceID].SourceDigest != legacy || j.binds != 1 {
		t.Fatalf("prepared=%+v legacy=%s binds=%d", prepared, legacy, j.binds)
	}
	if _, err = p.Prepare(context.Background()); err == nil {
		t.Fatal("second Prepare accepted")
	}
	if j.binds != 1 {
		t.Fatal("second credential effect")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.SourceBinding() != binding {
		t.Fatal("Close changed input facts")
	}
}
func TestRuntimePreparationPlanRejectsDriftBeforeMaterialization(t *testing.T) {
	for _, name := range []string{"file_growth", "file_mode", "empty_directory", "root_replaced", "cancelled", "closed", "failed_attempt"} {
		t.Run(name, func(t *testing.T) {
			e, r, j := runtimePlanFixture(t)
			source := e.configuration.WorkspaceSourceRoot
			if name == "file_growth" || name == "file_mode" {
				sourceWrite(t, filepath.Join(source, "source"), "old", 0o644)
			}
			p, err := e.PlanRuntime(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			binding := p.SourceBinding()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "file_growth":
				sourceWrite(t, filepath.Join(source, "source"), "new-grown", 0o644)
			case "file_mode":
				if err = os.Chmod(filepath.Join(source, "source"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty_directory":
				if err = os.Mkdir(filepath.Join(source, "new"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "root_replaced":
				if err = os.Rename(source, source+"-old"); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(source, 0o700); err != nil {
					t.Fatal(err)
				}
			case "cancelled", "failed_attempt":
				cancel()
			case "closed":
				if err = p.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, err = p.Prepare(ctx)
			if err == nil {
				t.Fatal("invalid plan accepted")
			}
			if (name == "cancelled" || name == "failed_attempt") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if name == "failed_attempt" {
				if _, err = p.Prepare(context.Background()); err == nil {
					t.Fatal("failed Prepare retried")
				}
			}
			assertSourceAbsent(t, filepath.Join(e.storage.BackendDataRoot, "sessions"))
			if j.binds != 0 || len(e.prepared) != 0 {
				t.Fatal("invalid source reached session effects")
			}
			if p.SourceBinding() != binding {
				t.Fatal("source was replanned")
			}
		})
	}
}
func TestRuntimePreparationPlanConcurrentPrepareAndClose(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate_prepare", true: "close_race"}[closeFirst], func(t *testing.T) {
			e, r, j := runtimePlanFixture(t)
			p, err := e.PlanRuntime(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					if closeFirst && i == 0 {
						results <- p.Close()
						return
					}
					_, err := p.Prepare(context.Background())
					results <- err
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				}
			}
			if !closeFirst && (success != 1 || j.binds != 1) {
				t.Fatalf("success=%d binds=%d", success, j.binds)
			}
			if j.binds > 1 {
				t.Fatalf("multiple materializations: %d", j.binds)
			}
			if _, err = p.Prepare(context.Background()); err == nil {
				t.Fatal("race left another attempt")
			}
		})
	}
}
func TestRuntimePreparationPlanRejectsInvalidInputsBeforeEffects(t *testing.T) {
	for _, name := range []string{"agent", "resource", "network", "binding"} {
		t.Run(name, func(t *testing.T) {
			e, r, j := runtimePlanFixture(t)
			switch name {
			case "agent":
				r.Command.AgentProfileDigest = "wrong"
			case "resource":
				r.Command.ResourceProfileID = "unknown"
			case "network":
				r.Command.NetworkPolicyDigest = "wrong"
			case "binding":
				r.SourcePlan = app.RuntimeSourcePlanBinding{Manifest: "invalid"}
			}
			if p, err := e.PlanRuntime(context.Background(), r); err == nil || p != nil {
				t.Fatalf("invalid plan=%v err=%v", p, err)
			}
			assertSourceAbsent(t, filepath.Join(e.storage.BackendDataRoot, "sessions"))
			if j.binds != 0 {
				t.Fatal("credential effect")
			}
		})
	}
}
