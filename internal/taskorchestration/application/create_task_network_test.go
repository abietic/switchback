package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/taskorchestration/application"
)

func TestCreateTaskRejectsNetworkProfileThatIsNotCurrentServerProfile(t *testing.T) {
	current, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]contractsv1.RuntimeConfig{
		"legacy profile": {
			NetworkPolicy: config.LegacyPublicWebRestrictedV1,
		},
		"stale revision": {
			NetworkPolicy:         current.ID,
			NetworkPolicyRevision: current.Revision + 1,
			NetworkPolicyDigest:   current.Digest,
		},
		"stale digest": {
			NetworkPolicy:         current.ID,
			NetworkPolicyRevision: current.Revision,
			NetworkPolicyDigest:   "sha256:" + strings.Repeat("0", 64),
		},
	}

	for name, runtimeConfig := range tests {
		t.Run(name, func(t *testing.T) {
			spec := createTaskSpecWithRuntimeConfig(t, runtimeConfig)
			store := &recordingCreateTaskStore{}

			_, err := newCreateTaskService(store).Execute(
				context.Background(),
				application.CreateTaskCommand{
					Actor: application.Actor{
						TenantID: spec.TenantID,
						UserID:   spec.UserID,
					},
					TaskSpec:       spec,
					AcceptanceMode: "manual",
					IdempotencyKey: "create-network-policy",
				},
			)
			if err == nil {
				t.Fatalf("runtime config %+v was accepted", runtimeConfig)
			}
			if store.calls != 0 {
				t.Fatalf("CreateTask store called %d times after admission rejection", store.calls)
			}
		})
	}
}

func TestCreateTaskAcceptsCurrentServerNetworkProfile(t *testing.T) {
	current, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	spec := createTaskSpecWithRuntimeConfig(t, contractsv1.RuntimeConfig{
		NetworkPolicy:         current.ID,
		NetworkPolicyRevision: current.Revision,
		NetworkPolicyDigest:   current.Digest,
	})
	store := &recordingCreateTaskStore{}

	if _, err := newCreateTaskService(store).Execute(
		context.Background(),
		application.CreateTaskCommand{
			Actor: application.Actor{
				TenantID: spec.TenantID,
				UserID:   spec.UserID,
			},
			TaskSpec:       spec,
			AcceptanceMode: "manual",
			IdempotencyKey: "create-network-policy",
		},
	); err != nil {
		t.Fatalf("create Task with current server network profile: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("CreateTask store called %d times, want 1", store.calls)
	}
}

func createTaskSpecWithRuntimeConfig(
	t *testing.T,
	runtimeConfig contractsv1.RuntimeConfig,
) contractsv1.TaskSpec {
	t.Helper()
	admission, err := newAdmissionService().Admit(directAdmissionCommand())
	if err != nil {
		t.Fatal(err)
	}
	spec := admission.TaskSpec
	spec.RuntimeConfig = runtimeConfig
	digest, err := application.CanonicalTaskSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.CreationIntentRef.TaskSpecDigest = digest
	return spec
}

func newCreateTaskService(store application.CreateTaskStore) application.CreateTaskService {
	return application.CreateTaskService{
		Store: store,
		IDs: &fixedIDs{values: []string{
			"task-network",
			"transition-network",
			"allocation-network",
			"event-network",
		}},
		Now: func() time.Time {
			return time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
		},
	}
}

type recordingCreateTaskStore struct {
	calls int
}

func (store *recordingCreateTaskStore) CreateTask(
	context.Context,
	application.CreateTaskRecord,
) (application.CreatedTask, error) {
	store.calls++
	return application.CreatedTask{
		TaskID:   "task-network",
		Revision: 1,
		Status:   "queued",
	}, nil
}
