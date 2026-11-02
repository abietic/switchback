// Package postgres composes Gateway commands from production application services and PostgreSQL adapters.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
	interactionpostgres "github.com/abietic/switchback/internal/interaction/adapter/postgres"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type IDGenerator interface {
	NewID(string) (string, error)
}

type CommandServiceOptions struct {
	Pool           *pgxpool.Pool
	IDs            IDGenerator
	Now            func() time.Time
	Workspace      taskapp.WorkspaceRevisionEvidence
	ProjectID      string
	Profiles       config.ProfileRegistry
	NetworkProfile config.RuntimeNetworkProfile
	Assistant      config.AssistantSettings
}

type CommandService struct {
	options            CommandServiceOptions
	tasks              *taskpostgres.Store
	delivery           taskapp.TaskDeliveryService
	taskActions        taskapp.TaskActionService
	followUps          workerapp.SessionFollowUpService
	workerCommands     workerapp.WorkerUserCommandService
	approvals          workerapp.ApprovalDecisionService
	assistantEnabled   bool
	createConversation interactionapp.CreateConversation
	openConversation   interactionapp.OpenConversation
	selectWorkspace    interactionapp.SelectConversationWorkspace
	sendMessage        interactionapp.SendConversationMessage
	checkpointCommands interactionapp.ExactCommandService
	confirmDraft       interactionapp.ConfirmTaskDraft
}

func NewCommandService(options CommandServiceOptions) (*CommandService, error) {
	currentNetwork, networkErr := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if options.Pool == nil || options.IDs == nil || options.Now == nil || options.ProjectID == "" ||
		!options.Workspace.Readable || options.Workspace.ResolutionRevision == 0 ||
		options.Workspace.MembershipRevision == 0 || networkErr != nil ||
		options.NetworkProfile != currentNetwork {
		return nil, fmt.Errorf("Gateway PostgreSQL command service configuration is incomplete")
	}
	if options.Assistant.Enabled &&
		(options.Assistant.ModelProfile.ID == "" || options.Assistant.ModelProfile.Revision == 0 ||
			options.Assistant.ModelProfile.Digest == "" ||
			options.Assistant.ReadinessPolicyRevision == "" ||
			options.Assistant.TriggerPolicyRevision == "" ||
			options.Assistant.ContextBudgetTokens <= 0 || options.Assistant.RequestTimeout <= 0) {
		return nil, fmt.Errorf("Gateway Assistant command configuration is incomplete")
	}
	assistantProfileRef := options.Assistant.ModelProfile
	if options.Assistant.Enabled {
		resolvedProfile, err := options.Profiles.ResolveAssistant(options.Assistant.ModelProfile)
		if err != nil {
			return nil, fmt.Errorf("Gateway Assistant command configuration is invalid: %w", err)
		}
		assistantProfileRef = resolvedProfile.Ref()
		options.Assistant.ModelProfile = assistantProfileRef
	}
	interactionStore := interactionpostgres.Store{Pool: options.Pool}
	return &CommandService{
		options: options,
		tasks:   taskpostgres.NewStore(options.Pool),
		delivery: taskapp.TaskDeliveryService{
			Store: taskpostgres.NewDeliveryStore(options.Pool), IDs: options.IDs, Now: options.Now,
		},
		taskActions: taskapp.TaskActionService{
			Store: taskpostgres.NewStore(options.Pool), IDs: options.IDs, Now: options.Now,
		},
		followUps: workerapp.SessionFollowUpService{
			Store: workerpostgres.NewSessionFollowUpStore(options.Pool), IDs: options.IDs, Now: options.Now,
		},
		workerCommands: workerapp.WorkerUserCommandService{
			Store: workerpostgres.NewWorkerUserCommandStore(options.Pool), IDs: options.IDs, Now: options.Now,
		},
		approvals: workerapp.ApprovalDecisionService{
			Store: workerpostgres.NewApprovalDecisionStore(options.Pool), IDs: options.IDs, Now: options.Now,
		},
		assistantEnabled: options.Assistant.Enabled,
		createConversation: interactionapp.CreateConversation{
			Transactions: interactionStore, IDs: options.IDs, Clock: functionClock{now: options.Now},
		},
		openConversation: interactionapp.OpenConversation{
			Transactions: interactionStore, IDs: options.IDs, Clock: functionClock{now: options.Now},
		},
		selectWorkspace: interactionapp.SelectConversationWorkspace{
			Transactions: interactionStore, IDs: options.IDs, Clock: functionClock{now: options.Now},
		},
		sendMessage: interactionapp.SendConversationMessage{
			Transactions: interactionStore, IDs: options.IDs, Clock: functionClock{now: options.Now},
			ModelProfile:        assistantProfileRef,
			PolicyRevision:      options.Assistant.ReadinessPolicyRevision,
			ContextBudgetTokens: options.Assistant.ContextBudgetTokens,
			RunTimeout:          options.Assistant.RequestTimeout,
		},
		checkpointCommands: interactionpostgres.CommandService{
			Store: interactionStore, IDs: options.IDs, Clock: functionClock{now: options.Now},
			ModelProfile:        assistantProfileRef,
			PolicyRevision:      options.Assistant.ReadinessPolicyRevision,
			ContextBudgetTokens: options.Assistant.ContextBudgetTokens,
			RunTimeout:          options.Assistant.RequestTimeout,
		},
		confirmDraft: interactionapp.ConfirmTaskDraft{
			States: interactionStore,
			Preparer: interactionapp.TaskConfirmationPreparer{
				IDs: options.IDs, Clock: functionClock{now: options.Now},
				TriggerPolicyRevision: options.Assistant.TriggerPolicyRevision,
				IntentTTL:             15 * time.Minute,
			},
			Admission: interactionpostgres.TaskAdmissionAdapter{
				Store: taskpostgres.NewStore(options.Pool), IDs: options.IDs, Now: options.Now,
			},
		},
	}, nil
}

type functionClock struct {
	now func() time.Time
}

func (clock functionClock) Now() time.Time { return clock.now().UTC() }

func (service *CommandService) CreateTask(ctx context.Context, actor gatewayhttp.FixedActor, input gatewayhttp.DirectTaskInput) (gatewayhttp.CommandResult, error) {
	if actor.TenantID != service.options.Workspace.TenantID || input.ProjectID != service.options.ProjectID ||
		input.WorkspaceID != service.options.Workspace.WorkspaceID ||
		input.WorkspaceRevisionID != service.options.Workspace.WorkspaceRevisionID {
		return gatewayhttp.CommandResult{}, gatewayhttp.ErrTargetUnavailable
	}
	if existing, found, err := service.readExistingCreate(ctx, actor, input); err != nil {
		return gatewayhttp.CommandResult{}, err
	} else if found {
		return existing, nil
	}
	profile, ok := service.options.Profiles.Agent(input.AgentProfileID)
	if !ok || profile.ValidateDigest() != nil {
		return gatewayhttp.CommandResult{}, gatewayhttp.ErrCommandUnsupported
	}
	admission, err := (taskapp.DirectAdmission{
		IDs: service.options.IDs, Now: service.options.Now,
		Policy: taskapp.DirectAdmissionPolicy{
			ReadinessPolicyRevision: "direct-ready-v1", TriggerPolicyRevision: "direct-create-v1",
			NetworkProfile: service.options.NetworkProfile, IntentTTL: 15 * time.Minute,
		},
	}).Admit(taskapp.DirectAdmissionCommand{
		Actor:     taskapp.Actor{TenantID: actor.TenantID, UserID: actor.UserID},
		Workspace: service.options.Workspace,
		AgentProfile: taskapp.AgentProfileEvidence{
			ProfileID: profile.ID, Revision: strconv.FormatUint(profile.Revision, 10), Enabled: true,
		},
		Request: taskapp.DirectTaskRequest{
			ProjectID: input.ProjectID, Title: input.Title, Goal: input.Goal,
			TargetKind: input.Target.Kind, TargetRef: input.Target.Ref,
			Constraints: input.Constraints, AcceptanceCriteria: input.AcceptanceCriteria,
			AcceptanceMode: input.AcceptanceMode, UserVisibleTypes: input.UserVisibleTypes,
		},
	})
	if err != nil {
		return gatewayhttp.CommandResult{}, gatewayhttp.ErrCommandConflict
	}
	if err := service.tasks.RecordDirectAdmission(ctx, admission); err != nil {
		return gatewayhttp.CommandResult{}, err
	}
	created, err := (taskapp.CreateTaskService{
		Store: service.tasks, IDs: service.options.IDs, Now: service.options.Now,
	}).Execute(ctx, taskapp.CreateTaskCommand{
		Actor:    taskapp.Actor{TenantID: actor.TenantID, UserID: actor.UserID},
		TaskSpec: admission.TaskSpec, AcceptanceMode: input.AcceptanceMode,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, taskapp.ErrIdempotencyConflict) {
			if existing, found, readErr := service.readExistingCreate(ctx, actor, input); readErr == nil && found {
				return existing, nil
			}
		}
		return gatewayhttp.CommandResult{}, mapCommandError(err)
	}
	return gatewayhttp.CommandResult{
		CommandID: created.EventID, Status: "accepted",
		ViewRef: "/api/v1/views/task_summary/" + created.TaskID,
	}, nil
}

func (service *CommandService) readExistingCreate(ctx context.Context, actor gatewayhttp.FixedActor, input gatewayhttp.DirectTaskInput) (gatewayhttp.CommandResult, bool, error) {
	var resourceID string
	var response []byte
	err := service.options.Pool.QueryRow(ctx, `
SELECT resource_id,response_body FROM idempotency_results
WHERE tenant_id=$1 AND actor_user_id=$2 AND operation='create_task' AND idempotency_key=$3`,
		actor.TenantID, actor.UserID, input.IdempotencyKey).Scan(&resourceID, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return gatewayhttp.CommandResult{}, false, nil
	}
	if err != nil {
		return gatewayhttp.CommandResult{}, false, err
	}
	var specJSON []byte
	var acceptanceMode string
	if err := service.options.Pool.QueryRow(ctx, `
SELECT task_spec_snapshot,acceptance_mode FROM tasks WHERE task_id=$1`, resourceID).Scan(&specJSON, &acceptanceMode); err != nil {
		return gatewayhttp.CommandResult{}, false, err
	}
	var spec struct {
		ProjectID   string `json:"project_id"`
		WorkspaceID string `json:"workspace_id"`
		Title       string `json:"title"`
		Goal        string `json:"goal"`
		Target      struct {
			Kind string `json:"kind"`
			Ref  string `json:"ref"`
		} `json:"target"`
		Constraints        []string `json:"constraints"`
		AcceptanceCriteria []string `json:"acceptance_criteria"`
		AgentConfig        struct {
			ProfileID string `json:"profile_id"`
		} `json:"agent_config"`
		OutputPolicy struct {
			UserVisibleTypes []string `json:"user_visible_types"`
		} `json:"output_policy"`
		WorkspaceResolutionRef struct {
			WorkspaceRevisionID string `json:"workspace_revision_id"`
		} `json:"workspace_resolution_ref"`
	}
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return gatewayhttp.CommandResult{}, false, err
	}
	matches := spec.ProjectID == input.ProjectID && spec.WorkspaceID == input.WorkspaceID &&
		spec.WorkspaceResolutionRef.WorkspaceRevisionID == input.WorkspaceRevisionID &&
		spec.Title == input.Title && spec.Goal == input.Goal && spec.Target.Kind == input.Target.Kind &&
		spec.Target.Ref == input.Target.Ref && reflect.DeepEqual(spec.Constraints, input.Constraints) &&
		reflect.DeepEqual(spec.AcceptanceCriteria, input.AcceptanceCriteria) &&
		spec.AgentConfig.ProfileID == input.AgentProfileID && acceptanceMode == input.AcceptanceMode &&
		reflect.DeepEqual(spec.OutputPolicy.UserVisibleTypes, input.UserVisibleTypes)
	if !matches {
		return gatewayhttp.CommandResult{}, false, gatewayhttp.ErrCommandConflict
	}
	var created taskapp.CreatedTask
	if err := json.Unmarshal(response, &created); err != nil {
		return gatewayhttp.CommandResult{}, false, err
	}
	return gatewayhttp.CommandResult{
		CommandID: created.EventID, Status: "accepted",
		ViewRef: "/api/v1/views/task_summary/" + created.TaskID,
	}, true, nil
}

func (service *CommandService) ExecuteTaskCommand(ctx context.Context, command gatewayhttp.TaskCommand) (gatewayhttp.CommandResult, error) {
	viewRef := "/api/v1/views/task_summary/" + command.TaskID
	if command.Type == "continue" {
		result, err := service.taskActions.ContinueTaskWithWorker(
			ctx,
			taskapp.ContinueTaskWithWorkerCommand{
				Actor: taskapp.Actor{
					TenantID: command.Actor.TenantID,
					UserID:   command.Actor.UserID,
				},
				TaskID: command.TaskID, ExpectedTaskRevision: command.ExpectedTaskRevision,
				WorkerSessionID:         command.WorkerSessionID,
				ExpectedSessionRevision: command.ExpectedSessionRevision,
				PreviousWorkerRunID:     command.WorkerRunID,
				ExpectedRunRevision:     command.ExpectedRunRevision,
				PreviousAgentTurnID:     command.AgentTurnID,
				ExpectedTurnRevision:    command.ExpectedTurnRevision,
				InteractionKind:         taskapp.TaskContinuationInteraction(command.InteractionKind),
				Message:                 command.Message, IdempotencyKey: command.IdempotencyKey,
			},
		)
		viewRef = "/api/v1/views/worker_detail/" + command.WorkerSessionID
		if err != nil {
			return gatewayhttp.CommandResult{ViewRef: viewRef}, mapCommandError(err)
		}
		return gatewayhttp.CommandResult{
			CommandID: result.EventID, Status: "accepted", ViewRef: viewRef,
		}, nil
	}
	if command.Type == "rework" {
		result, err := service.taskActions.Rework(ctx, taskapp.ReworkTaskCommand{
			Actor:  taskapp.Actor{TenantID: command.Actor.TenantID, UserID: command.Actor.UserID},
			TaskID: command.TaskID, ExpectedTaskRevision: command.ExpectedTaskRevision,
			Message: command.Message, IdempotencyKey: command.IdempotencyKey,
		})
		if err != nil {
			return gatewayhttp.CommandResult{ViewRef: viewRef}, mapCommandError(err)
		}
		return gatewayhttp.CommandResult{CommandID: result.EventID, Status: "accepted", ViewRef: viewRef}, nil
	}
	if command.Type == "cancel" {
		result, err := service.taskActions.Cancel(ctx, taskapp.CancelTaskCommand{
			Actor:  taskapp.Actor{TenantID: command.Actor.TenantID, UserID: command.Actor.UserID},
			TaskID: command.TaskID, ExpectedTaskRevision: command.ExpectedTaskRevision,
			IdempotencyKey: command.IdempotencyKey,
		})
		if err != nil {
			return gatewayhttp.CommandResult{ViewRef: viewRef}, mapCommandError(err)
		}
		return gatewayhttp.CommandResult{CommandID: result.EventID, Status: "accepted", ViewRef: viewRef}, nil
	}
	if command.Type != "accept" {
		return gatewayhttp.CommandResult{ViewRef: viewRef}, gatewayhttp.ErrCommandUnsupported
	}
	result, err := service.delivery.Accept(ctx, taskapp.AcceptTaskCommand{
		Actor:  taskapp.Actor{TenantID: command.Actor.TenantID, UserID: command.Actor.UserID},
		TaskID: command.TaskID, ExpectedTaskRevision: command.ExpectedTaskRevision,
		IdempotencyKey: command.IdempotencyKey,
	})
	if err != nil {
		return gatewayhttp.CommandResult{ViewRef: viewRef}, mapCommandError(err)
	}
	return gatewayhttp.CommandResult{
		CommandID: result.EventID, Status: "accepted",
		ViewRef: viewRef,
	}, nil
}

func (service *CommandService) ExecuteSessionCommand(ctx context.Context, command gatewayhttp.SessionCommand) (gatewayhttp.CommandResult, error) {
	if command.Type == "steer" {
		return gatewayhttp.CommandResult{ViewRef: "/api/v1/views/worker_detail/" + command.WorkerSessionID}, gatewayhttp.ErrCommandUnsupported
	}
	if command.Type == "approval" {
		if command.Approval == nil {
			return gatewayhttp.CommandResult{}, gatewayhttp.ErrCommandConflict
		}
		decision := command.Approval
		result, err := service.approvals.Execute(ctx, workerapp.DecideApprovalCommand{
			TenantID: command.Actor.TenantID, UserID: command.Actor.UserID,
			WorkerSessionID: command.WorkerSessionID, WorkerRunID: command.WorkerRunID,
			AgentTurnID: command.AgentTurnID, ApprovalRequestID: decision.ApprovalRequestID,
			Capability: decision.Capability, Target: decision.Target, Effect: decision.Effect,
			Decision: decision.Decision, Scope: decision.Scope,
			ExpectedSessionRevision:  command.ExpectedSessionRevision,
			ExpectedRunRevision:      command.ExpectedRunRevision,
			ExpectedTurnRevision:     command.ExpectedTurnRevision,
			ExpectedApprovalRevision: decision.ExpectedApprovalRevision,
			IdempotencyKey:           command.IdempotencyKey,
		})
		viewRef := "/api/v1/views/worker_detail/" + command.WorkerSessionID
		if err != nil {
			return gatewayhttp.CommandResult{ViewRef: viewRef}, mapCommandError(err)
		}
		return gatewayhttp.CommandResult{CommandID: result.CommandID, Status: result.Status, ViewRef: viewRef}, nil
	}
	if command.Type == "queue" || command.Type == "interrupt" {
		result, err := service.workerCommands.Execute(ctx, workerapp.SubmitWorkerUserCommand{
			TenantID: command.Actor.TenantID, UserID: command.Actor.UserID,
			WorkerSessionID: command.WorkerSessionID, WorkerRunID: command.WorkerRunID,
			AgentTurnID: command.AgentTurnID, CommandType: command.Type, Content: command.Content,
			ExpectedSessionRevision: command.ExpectedSessionRevision,
			ExpectedRunRevision:     command.ExpectedRunRevision,
			ExpectedTurnRevision:    command.ExpectedTurnRevision,
			IdempotencyKey:          command.IdempotencyKey,
		})
		if err != nil {
			return gatewayhttp.CommandResult{ViewRef: "/api/v1/views/worker_detail/" + command.WorkerSessionID}, mapCommandError(err)
		}
		return gatewayhttp.CommandResult{
			CommandID: result.CommandID, Status: result.Status, QueueSequence: result.QueueSequence,
			ViewRef: "/api/v1/views/worker_detail/" + command.WorkerSessionID,
		}, nil
	}
	if command.Type != "follow-up" {
		return gatewayhttp.CommandResult{ViewRef: "/api/v1/views/worker_detail/" + command.WorkerSessionID}, gatewayhttp.ErrCommandUnsupported
	}
	result, err := service.followUps.Execute(ctx, workerapp.StartSessionFollowUpCommand{
		TenantID: command.Actor.TenantID, UserID: command.Actor.UserID,
		WorkerSessionID: command.WorkerSessionID, ExpectedSessionRevision: command.ExpectedSessionRevision,
		PreviousWorkerRunID: command.WorkerRunID, ExpectedRunRevision: command.ExpectedRunRevision,
		PreviousAgentTurnID: command.AgentTurnID, ExpectedTurnRevision: command.ExpectedTurnRevision,
		Content: command.Content, IdempotencyKey: command.IdempotencyKey,
	})
	if err != nil {
		return gatewayhttp.CommandResult{ViewRef: "/api/v1/views/worker_detail/" + command.WorkerSessionID}, mapCommandError(err)
	}
	return gatewayhttp.CommandResult{
		CommandID: result.EventID, Status: "accepted",
		ViewRef: "/api/v1/views/worker_detail/" + command.WorkerSessionID,
	}, nil
}

func (service *CommandService) ExecuteAssistantCommand(
	ctx context.Context,
	actor gatewayhttp.FixedActor,
	command contractsv1.ConversationCommand,
) (gatewayhttp.AssistantCommandResult, error) {
	viewRef := ""
	if command.ConversationID != "" {
		viewRef = "/api/v1/views/assistant_conversation/" + command.ConversationID
	}
	if !service.assistantEnabled {
		return gatewayhttp.AssistantCommandResult{ProjectionRef: viewRef}, gatewayhttp.ErrCommandUnsupported
	}
	applicationActor := taskapp.Actor{TenantID: actor.TenantID, UserID: actor.UserID}
	var result interactionapp.ConversationCommandResult
	var err error
	switch command.CommandType {
	case "create_conversation":
		result, err = service.createConversation.Execute(ctx, interactionapp.CreateConversationCommand{
			Actor: applicationActor, IdempotencyKey: command.IdempotencyKey,
		})
	case "open_conversation":
		result, err = service.openConversation.Execute(ctx, interactionapp.OpenConversationCommand{
			Actor: applicationActor, ConversationID: command.ConversationID,
			ExpectedConversationRevision: command.ExpectedRevision,
			IdempotencyKey:               command.IdempotencyKey,
		})
	case "select_workspace":
		if command.WorkspaceID != service.options.Workspace.WorkspaceID ||
			command.WorkspaceRevisionID != service.options.Workspace.WorkspaceRevisionID ||
			command.WorkspaceResolutionID != service.options.Workspace.ResolutionID ||
			command.WorkspaceResolutionRevision != service.options.Workspace.ResolutionRevision ||
			command.MembershipRevision != service.options.Workspace.MembershipRevision {
			return gatewayhttp.AssistantCommandResult{ProjectionRef: viewRef}, gatewayhttp.ErrTargetUnavailable
		}
		result, err = service.selectWorkspace.Execute(ctx, interactionapp.SelectWorkspaceCommand{
			Actor: applicationActor, ConversationID: command.ConversationID,
			ExpectedConversationRevision: command.ExpectedRevision,
			IdempotencyKey:               command.IdempotencyKey,
			Workspace: domain.WorkspaceResolution{
				WorkspaceID: command.WorkspaceID, WorkspaceRevisionID: command.WorkspaceRevisionID,
				ResolutionID:       command.WorkspaceResolutionID,
				ResolutionRevision: command.WorkspaceResolutionRevision,
				MembershipRevision: command.MembershipRevision,
			},
		})
	case "send_message":
		sent, sendErr := service.sendMessage.Execute(ctx, interactionapp.SendConversationMessageCommand{
			Actor: applicationActor, ConversationID: command.ConversationID,
			ExpectedConversationRevision: command.ExpectedRevision,
			IdempotencyKey:               command.IdempotencyKey, Content: command.Content,
		})
		if sendErr != nil {
			return gatewayhttp.AssistantCommandResult{ProjectionRef: viewRef}, mapAssistantCommandError(sendErr)
		}
		result = interactionapp.ConversationCommandResult{
			CommandID: sent.CommandID, ConversationID: sent.ConversationID,
			ConversationRevision: sent.ConversationRevision, AssistantRunID: sent.AssistantRunID,
			ProjectionRef: sent.ProjectionRef, Status: sent.Status,
		}
		return mapAssistantResult(result, sent.TurnID), nil
	case "checkpoint_action":
		result, err = service.checkpointCommands.ApplyCheckpointAction(
			ctx, interactionapp.CheckpointActionCommand{
				Actor: applicationActor, ConversationID: command.ConversationID,
				ExpectedConversationRevision: command.ExpectedRevision,
				CheckpointID:                 command.CheckpointID, CheckpointRevision: command.CheckpointRevision,
				Action:         domain.CheckpointAction(command.CheckpointAction),
				IdempotencyKey: command.IdempotencyKey,
			},
		)
	case "confirm_task":
		confirmed, confirmErr := service.confirmDraft.Execute(ctx, interactionapp.ConfirmTaskDraftCommand{
			Actor: applicationActor, ConversationID: command.ConversationID,
			ExpectedConversationRevision: command.ExpectedRevision,
			TaskDraftID:                  command.TaskDraftID, TaskDraftRevision: command.TaskDraftRevision,
			ReadinessAssessmentID:       command.ReadinessAssessmentID,
			ReadinessAssessmentRevision: command.ReadinessAssessmentRevision,
			WorkspaceID:                 command.WorkspaceID, WorkspaceRevisionID: command.WorkspaceRevisionID,
			WorkspaceResolutionID:       command.WorkspaceResolutionID,
			WorkspaceResolutionRevision: command.WorkspaceResolutionRevision,
			MembershipRevision:          command.MembershipRevision, TaskSpecDigest: command.TaskSpecDigest,
			TriggerPolicyRevision: command.TriggerPolicyRevision,
			IdempotencyKey:        command.IdempotencyKey,
		})
		err = confirmErr
		result = interactionapp.ConversationCommandResult{
			CommandID: confirmed.CommandID, ConversationID: command.ConversationID,
			ConversationRevision: command.ExpectedRevision, TaskID: confirmed.TaskID,
			TaskRevision: confirmed.TaskRevision, ProjectionRef: viewRef, Status: "accepted",
		}
	case "cancel_run":
		result, err = service.checkpointCommands.CancelQueuedRun(ctx, interactionapp.CancelQueuedRunCommand{
			Actor: applicationActor, ConversationID: command.ConversationID,
			ExpectedConversationRevision: command.ExpectedRevision,
			AssistantRunID:               command.AssistantRunID,
			AssistantRunRevision:         command.AssistantRunRevision,
			IdempotencyKey:               command.IdempotencyKey,
		})
	case "retry_run":
		result, err = service.checkpointCommands.RetryFailedRun(ctx, interactionapp.RetryFailedRunCommand{
			Actor: applicationActor, ConversationID: command.ConversationID,
			ExpectedConversationRevision: command.ExpectedRevision,
			AssistantRunID:               command.AssistantRunID,
			AssistantRunRevision:         command.AssistantRunRevision,
			IdempotencyKey:               command.IdempotencyKey,
		})
	default:
		return gatewayhttp.AssistantCommandResult{ProjectionRef: viewRef}, gatewayhttp.ErrCommandUnsupported
	}
	if result.ProjectionRef == "" {
		result.ProjectionRef = viewRef
	}
	return mapAssistantResult(result, ""), mapAssistantCommandError(err)
}

func mapAssistantResult(
	result interactionapp.ConversationCommandResult,
	turnID string,
) gatewayhttp.AssistantCommandResult {
	return gatewayhttp.AssistantCommandResult{
		CommandID: result.CommandID, ConversationID: result.ConversationID,
		ConversationRevision: result.ConversationRevision, TurnID: turnID,
		AssistantRunID:       result.AssistantRunID,
		AssistantRunRevision: result.AssistantRunRevision,
		TaskID:               result.TaskID, TaskRevision: result.TaskRevision,
		ProjectionRef: result.ProjectionRef, Status: result.Status,
	}
}

func mapAssistantCommandError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, interactionapp.ErrConversationUnavailable):
		return gatewayhttp.ErrTargetUnavailable
	case errors.Is(err, domain.ErrStaleRevision),
		errors.Is(err, interactionapp.ErrConfirmationStale),
		errors.Is(err, interactionapp.ErrLeaseLost):
		return gatewayhttp.ErrStaleTarget
	case errors.Is(err, interactionapp.ErrIdempotencyConflict):
		return gatewayhttp.ErrCommandConflict
	default:
		return err
	}
}

func mapCommandError(err error) error {
	switch {
	case errors.Is(err, platformpostgres.ErrNotFound):
		return gatewayhttp.ErrTargetUnavailable
	case errors.Is(err, platformpostgres.ErrRevisionConflict):
		return gatewayhttp.ErrStaleTarget
	case errors.Is(err, taskapp.ErrIdempotencyConflict):
		return gatewayhttp.ErrCommandConflict
	case errors.Is(err, workerapp.ErrWorkerUserCommandIdempotencyConflict):
		return gatewayhttp.ErrCommandConflict
	case errors.Is(err, workerapp.ErrApprovalDecisionIdempotencyConflict):
		return gatewayhttp.ErrCommandConflict
	default:
		return err
	}
}

var _ gatewayhttp.CommandService = (*CommandService)(nil)
var _ gatewayhttp.AssistantCommandService = (*CommandService)(nil)
