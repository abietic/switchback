package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

var (
	ErrIdempotencyConflict   = errors.New("idempotency key content conflict")
	ErrCreationIntentInvalid = errors.New("creation intent is missing, stale, consumed, or mismatched")
)

type CreateTaskCommand struct {
	Actor                       Actor
	TaskSpec                    contractsv1.TaskSpec
	AcceptanceMode              string
	WorkerSessionTargetIntentID string
	IdempotencyKey              string
	AssistantAdmission          *AssistantAdmissionEvidence
}

type AssistantAdmissionEvidence struct {
	Intent                     contractsv1.AssistantCreationIntent
	Readiness                  domain.TaskReadinessAssessment
	ConversationRevision       uint64
	TaskDraftRevision          uint64
	AssistantReadinessRevision uint64
}

type CreateTaskIDs struct {
	TaskID              string
	TransitionID        string
	AllocationRequestID string
	QueueEntryID        string
	EventID             string
}

type CreateTaskRecord struct {
	IDs                         CreateTaskIDs
	Actor                       Actor
	TaskSpec                    contractsv1.TaskSpec
	TaskSpecDigest              string
	AcceptanceMode              string
	WorkerSessionTargetIntentID string
	IdempotencyKey              string
	RequestDigest               string
	Now                         time.Time
	AssistantAdmission          *AssistantAdmissionEvidence
}

type CreatedTask struct {
	TaskID                    string `json:"task_id"`
	Revision                  uint64 `json:"revision"`
	Status                    string `json:"status"`
	SessionAllocationSequence uint64 `json:"session_allocation_sequence"`
	AllocationRequestID       string `json:"allocation_request_id"`
	EventID                   string `json:"event_id"`
	WorkerSessionID           string `json:"worker_session_id,omitempty"`
	SessionQueueSequence      uint64 `json:"session_queue_sequence,omitempty"`
}

type CreateTaskStore interface {
	CreateTask(context.Context, CreateTaskRecord) (CreatedTask, error)
}

type CreateTaskService struct {
	Store CreateTaskStore
	IDs   IDGenerator
	Now   func() time.Time
}

func (service CreateTaskService) Execute(ctx context.Context, command CreateTaskCommand) (CreatedTask, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return CreatedTask{}, fmt.Errorf("CreateTask dependencies are required")
	}
	if err := command.TaskSpec.Validate(); err != nil {
		return CreatedTask{}, err
	}
	runtimeConfig := command.TaskSpec.RuntimeConfig
	if _, err := config.ProductionRuntimeNetworkProfiles().ResolveStored(
		config.RuntimeNetworkEvidence{
			ID:                runtimeConfig.NetworkPolicy,
			Revision:          runtimeConfig.NetworkPolicyRevision,
			Digest:            runtimeConfig.NetworkPolicyDigest,
			EnforcementStatus: config.NetworkEnforcementActive,
		},
	); err != nil {
		return CreatedTask{}, fmt.Errorf("admit Task runtime network policy: %w", err)
	}
	if command.Actor.TenantID != command.TaskSpec.TenantID || command.Actor.UserID != command.TaskSpec.UserID {
		return CreatedTask{}, ErrCreationIntentInvalid
	}
	if !canonical(command.IdempotencyKey) || len(command.IdempotencyKey) > 200 {
		return CreatedTask{}, fmt.Errorf("idempotency key must be canonical")
	}
	if command.WorkerSessionTargetIntentID != "" && !canonical(command.WorkerSessionTargetIntentID) {
		return CreatedTask{}, fmt.Errorf("WorkerSession target intent id must be canonical")
	}
	if command.AcceptanceMode != "auto" && command.AcceptanceMode != "manual" {
		return CreatedTask{}, fmt.Errorf("unsupported acceptance mode %q", command.AcceptanceMode)
	}
	if (command.TaskSpec.Source == "assistant") != (command.AssistantAdmission != nil) {
		return CreatedTask{}, ErrCreationIntentInvalid
	}
	for _, artifactType := range command.TaskSpec.OutputPolicy.UserVisibleTypes {
		if artifactType == "patch" && command.AcceptanceMode != "manual" {
			return CreatedTask{}, fmt.Errorf("patch Task requires manual acceptance")
		}
	}
	taskSpecDigest, err := CanonicalTaskSpecDigest(command.TaskSpec)
	if err != nil {
		return CreatedTask{}, err
	}
	if taskSpecDigest != command.TaskSpec.CreationIntentRef.TaskSpecDigest {
		return CreatedTask{}, ErrCreationIntentInvalid
	}
	idempotencyIntentID := command.TaskSpec.CreationIntentRef.IntentID
	if command.AssistantAdmission != nil {
		idempotencyIntentID = ""
	}
	requestDigest, err := canonicalDigest(struct {
		TenantID       string `json:"tenant_id"`
		ActorUserID    string `json:"actor_user_id"`
		TaskSpecDigest string `json:"task_spec_digest"`
		AcceptanceMode string `json:"acceptance_mode"`
		IntentID       string `json:"intent_id"`
		TargetIntentID string `json:"target_intent_id,omitempty"`
	}{
		TenantID: command.Actor.TenantID, ActorUserID: command.Actor.UserID,
		TaskSpecDigest: taskSpecDigest, AcceptanceMode: command.AcceptanceMode,
		IntentID:       idempotencyIntentID,
		TargetIntentID: command.WorkerSessionTargetIntentID,
	})
	if err != nil {
		return CreatedTask{}, err
	}
	ids, err := service.createIDs(command.WorkerSessionTargetIntentID != "")
	if err != nil {
		return CreatedTask{}, err
	}
	return service.Store.CreateTask(ctx, CreateTaskRecord{
		IDs: ids, Actor: command.Actor, TaskSpec: command.TaskSpec,
		TaskSpecDigest: taskSpecDigest, AcceptanceMode: command.AcceptanceMode,
		WorkerSessionTargetIntentID: command.WorkerSessionTargetIntentID,
		IdempotencyKey:              command.IdempotencyKey, RequestDigest: requestDigest,
		Now: service.Now().UTC(), AssistantAdmission: command.AssistantAdmission,
	})
}

func (service CreateTaskService) createIDs(targetExistingSession bool) (CreateTaskIDs, error) {
	values := make([]string, 4)
	thirdKind := "session-allocation-request"
	if targetExistingSession {
		thirdKind = "session-task-queue-entry"
	}
	kinds := []string{"task", "task-transition", thirdKind, "integration-event"}
	for index, kind := range kinds {
		value, err := service.IDs.NewID(kind)
		if err != nil {
			return CreateTaskIDs{}, fmt.Errorf("generate %s id: %w", kind, err)
		}
		if value == "" || strings.TrimSpace(value) != value {
			return CreateTaskIDs{}, fmt.Errorf("generated %s id is invalid", kind)
		}
		values[index] = value
	}
	ids := CreateTaskIDs{
		TaskID: values[0], TransitionID: values[1],
		EventID: values[3],
	}
	if targetExistingSession {
		ids.QueueEntryID = values[2]
	} else {
		ids.AllocationRequestID = values[2]
	}
	return ids, nil
}
