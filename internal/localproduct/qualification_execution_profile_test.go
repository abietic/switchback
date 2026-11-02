package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

var errQualificationProfileFixtureProvision = errors.New("fixture stopped before physical provisioning")

type qualificationProfileStartStore struct{ committed bool }

func (s *qualificationProfileStartStore) StartWorkerRun(_ context.Context, record workerapp.StartWorkerRunRecord) (workerapp.StartedWorkerRun, error) {
	s.committed = true
	return workerapp.StartedWorkerRun{Created: true, WorkerSessionID: record.Command.WorkerSessionID, WorkerRunID: record.WorkerRunID,
		TaskID: record.Command.TaskID, RuntimeInstanceID: record.RuntimeInstanceID, AgentSessionID: record.AgentSessionID, AgentTurnID: record.AgentTurnID,
		RuntimeGeneration: record.Command.RuntimeGeneration, ControlChannelEpoch: record.Command.ControlChannelEpoch}, nil
}

type qualificationProfileIDs struct{}

func (qualificationProfileIDs) NewID(kind string) (string, error) {
	return kind + "-profile-fixture", nil
}

type qualificationProfileEffects struct{ provisions, physicalChecks int }

func (*qualificationProfileEffects) AuthorizeInitialPreparation(context.Context, workerapp.RuntimePreparationRequest) error {
	return nil
}
func (*qualificationProfileEffects) AuthorizeInitialProvision(context.Context, workerapp.RuntimePreparationRequest, workerapp.RuntimeProvisionRequest) error {
	return nil
}
func (e *qualificationProfileEffects) CheckCreatedLayer(context.Context, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity) error {
	e.physicalChecks++
	return errors.New("unexpected physical checker")
}
func (e *qualificationProfileEffects) Provision(context.Context, workerapp.RuntimeProvisionRequest, workerapp.RuntimePreStartCheck) (workerapp.ProvisionedRuntime, error) {
	e.provisions++
	return workerapp.ProvisionedRuntime{}, errQualificationProfileFixtureProvision
}
func (*qualificationProfileEffects) Inspect(context.Context, string) (workerapp.RuntimeInspection, error) {
	return workerapp.RuntimeInspection{}, errors.New("unexpected Engine inspect")
}

func TestQualificationExecutionProfileCredentialBoundaryUsesRealEnvironment(t *testing.T) {
	for _, profileID := range []string{config.KimiCodexAnthropicV1, config.DeepSeekCodexOpenAIV1} {
		for _, scenario := range []string{"selected_missing", "selected_unsafe_mode", "selected_symlink", "unselected_missing"} {
			t.Run(profileID+"/"+scenario, func(t *testing.T) {
				environment, preparation, _ := runtimePlanFixture(t)
				selected := environment.configuration.KimiCredentialFile
				missing := filepath.Join(t.TempDir(), "never-created")
				if profileID == config.KimiCodexAnthropicV1 {
					environment.configuration.DeepSeekCredentialFile = missing
				} else {
					environment.configuration.KimiCredentialFile = missing
					environment.configuration.DeepSeekCredentialFile = selected
				}
				setSelected := func(path string) {
					if profileID == config.KimiCodexAnthropicV1 {
						environment.configuration.KimiCredentialFile = path
					} else {
						environment.configuration.DeepSeekCredentialFile = path
					}
				}
				switch scenario {
				case "selected_missing":
					setSelected(missing)
				case "selected_unsafe_mode":
					if err := os.Chmod(selected, 0644); err != nil {
						t.Fatal(err)
					}
				case "selected_symlink":
					link := filepath.Join(t.TempDir(), "credential-link")
					if err := os.Symlink(selected, link); err != nil {
						t.Fatal(err)
					}
					setSelected(link)
				}
				profile, ok := environment.profiles.Agent(profileID)
				if !ok {
					t.Fatal("fixture profile missing")
				}
				command := preparation.Command
				command.AgentProfileID, command.AgentProfileDigest = profile.ID, profile.Digest
				command.AgentProfileRevision = strconv.FormatUint(profile.Revision, 10)
				command.TenantID, command.UserID = "tenant-local", "user-local"
				command.WorkerSessionID, command.TaskID, command.PermitID = "session-profile", "task-profile", "permit-profile"
				command.ExpectedSessionRevision, command.RuntimeGeneration, command.ControlChannelEpoch = 1, 1, 1
				command.TriggerType, command.TurnIdempotencyKey = "task_initial", "profile-fixture-once"
				command.RuntimeImageRef = "sha256:" + strings.Repeat("a", 64)
				command.AgentTurnContent = json.RawMessage(`{"text":"report"}`)
				digest, err := workspaceSourceDigest(environment.configuration.WorkspaceSourceRoot)
				if err != nil {
					t.Fatal(err)
				}
				command.StartSnapshot, err = json.Marshal(map[string]string{"working_copy_checkpoint_digest": "sha256:" + digest})
				if err != nil {
					t.Fatal(err)
				}
				store := &qualificationProfileStartStore{}
				effects := &qualificationProfileEffects{}
				coordinator := workerapp.RuntimeCoordinator{Starts: workerapp.StartWorkerRunService{Store: store, IDs: qualificationProfileIDs{}, Now: time.Now},
					Environment: environment, Authorizer: effects, CreatedLayerChecker: effects, Provisioner: effects}
				_, err = coordinator.Start(context.Background(), workerapp.StartRuntimeCommand{Run: command})
				if !store.committed || effects.physicalChecks != 0 {
					t.Fatalf("wrong boundary: committed=%v physical_checks=%d err=%v", store.committed, effects.physicalChecks, err)
				}
				if scenario == "unselected_missing" {
					if !errors.Is(err, errQualificationProfileFixtureProvision) || effects.provisions != 1 {
						t.Fatalf("unselected credential became required: provisions=%d err=%v", effects.provisions, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "provider credential file is invalid") || effects.provisions != 0 || len(environment.prepared) != 0 {
					t.Fatalf("selected credential did not fail before provisioning/Start/Prompt: provisions=%d prepared=%d err=%v", effects.provisions, len(environment.prepared), err)
				}
				t.Logf("provision_calls=%d physical_checks=%d; no Engine or Agent client is present", effects.provisions, effects.physicalChecks)
			})
		}
	}
}
