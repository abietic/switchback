// Package gatewayhttp owns the loopback HTTP command and query adapter.
package gatewayhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

const maxCommandBodyBytes = 1 << 20

var (
	ErrStaleTarget          = errors.New("command target is stale")
	ErrTargetUnavailable    = errors.New("command target is unavailable")
	ErrCommandUnsupported   = errors.New("command is unsupported")
	ErrCommandConflict      = errors.New("command conflicts with current state")
	ErrCommandServiceAbsent = errors.New("command service is unavailable")
)

type FixedActor struct {
	TenantID string
	UserID   string
}

type CommandResult struct {
	CommandID     string  `json:"command_id"`
	Status        string  `json:"status"`
	QueueSequence *uint64 `json:"queue_sequence,omitempty"`
	ViewRef       string  `json:"view_ref,omitempty"`
}

type TaskCommand struct {
	Type                    string
	Actor                   FixedActor
	TaskID                  string
	ExpectedTaskRevision    uint64
	WorkerSessionID         string
	ExpectedSessionRevision uint64
	WorkerRunID             string
	ExpectedRunRevision     uint64
	AgentTurnID             string
	ExpectedTurnRevision    uint64
	InteractionKind         string
	IdempotencyKey          string
	Message                 string
}

type SessionCommand struct {
	Type                    string
	Actor                   FixedActor
	WorkerSessionID         string
	ExpectedSessionRevision uint64
	WorkerRunID             string
	ExpectedRunRevision     uint64
	AgentTurnID             string
	ExpectedTurnRevision    uint64
	IdempotencyKey          string
	Content                 string
	Approval                *ApprovalDecision
}

type ApprovalDecision struct {
	ApprovalRequestID        string `json:"approval_request_id"`
	ExpectedApprovalRevision uint64 `json:"expected_approval_revision"`
	Decision                 string `json:"decision"`
	Scope                    string `json:"scope"`
	Capability               string `json:"capability"`
	Target                   string `json:"target"`
	Effect                   string `json:"effect"`
}

type CommandService interface {
	CreateTask(context.Context, FixedActor, DirectTaskInput) (CommandResult, error)
	ExecuteTaskCommand(context.Context, TaskCommand) (CommandResult, error)
	ExecuteSessionCommand(context.Context, SessionCommand) (CommandResult, error)
}

type AssistantCommandResult struct {
	CommandID            string `json:"command_id"`
	ConversationID       string `json:"conversation_id"`
	ConversationRevision uint64 `json:"conversation_revision"`
	TurnID               string `json:"turn_id,omitempty"`
	AssistantRunID       string `json:"assistant_run_id,omitempty"`
	AssistantRunRevision uint64 `json:"assistant_run_revision,omitempty"`
	TaskID               string `json:"task_id,omitempty"`
	TaskRevision         uint64 `json:"task_revision,omitempty"`
	ProjectionRef        string `json:"projection_ref"`
	Status               string `json:"status"`
}

type AssistantCommandService interface {
	ExecuteAssistantCommand(
		context.Context,
		FixedActor,
		contractsv1.ConversationCommand,
	) (AssistantCommandResult, error)
}

type HandlerOptions struct {
	Actor     FixedActor
	Commands  CommandService
	Assistant AssistantCommandService
}

// NewHandler keeps health available before the production composition root has
// installed command dependencies. Command routes fail with a stable 503.
func NewHandler() http.Handler {
	return newHandler(HandlerOptions{
		Actor:     FixedActor{TenantID: "tenant-local", UserID: "user-local"},
		Commands:  unavailableCommands{},
		Assistant: unavailableAssistantCommands{},
	})
}

func NewHandlerWithOptions(options HandlerOptions) (http.Handler, error) {
	if !canonical(options.Actor.TenantID) || !canonical(options.Actor.UserID) || options.Commands == nil {
		return nil, fmt.Errorf("Gateway fixed actor and command service are required")
	}
	if options.Assistant == nil {
		options.Assistant = unavailableAssistantCommands{}
	}
	return newHandler(options), nil
}

func newHandler(options HandlerOptions) http.Handler {
	handler := &commandHandler{
		actor: options.Actor, commands: options.Commands, assistant: options.Assistant,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("POST /api/v1/tasks", handler.createTask)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/{action}", handler.taskCommand)
	mux.HandleFunc("POST /api/v1/worker-sessions/{worker_session_id}/{action}", handler.sessionCommand)
	mux.HandleFunc("POST /api/v1/assistant/conversations", handler.createConversation)
	mux.HandleFunc("POST /api/v1/assistant/conversations/{conversation_id}/open", handler.openConversation)
	mux.HandleFunc("POST /api/v1/assistant/conversations/{conversation_id}/workspace", handler.selectWorkspace)
	mux.HandleFunc("POST /api/v1/assistant/conversations/{conversation_id}/messages", handler.sendConversationMessage)
	mux.HandleFunc(
		"POST /api/v1/assistant/conversations/{conversation_id}/checkpoints/{checkpoint_id}/actions",
		handler.checkpointAction,
	)
	mux.HandleFunc(
		"POST /api/v1/assistant/conversations/{conversation_id}/task-drafts/{task_draft_id}/confirm",
		handler.confirmTaskDraft,
	)
	mux.HandleFunc(
		"POST /api/v1/assistant/conversations/{conversation_id}/runs/{assistant_run_id}/{action}",
		handler.assistantRunCommand,
	)
	mux.HandleFunc("/", notFound)
	return mux
}

type commandHandler struct {
	actor     FixedActor
	commands  CommandService
	assistant AssistantCommandService
}

func (handler *commandHandler) createConversation(response http.ResponseWriter, request *http.Request) {
	handler.executeAssistantCommand(response, request, "create_conversation")
}

func (handler *commandHandler) openConversation(response http.ResponseWriter, request *http.Request) {
	handler.executeAssistantCommand(response, request, "open_conversation")
}

func (handler *commandHandler) selectWorkspace(response http.ResponseWriter, request *http.Request) {
	handler.executeAssistantCommand(response, request, "select_workspace")
}

func (handler *commandHandler) sendConversationMessage(response http.ResponseWriter, request *http.Request) {
	handler.executeAssistantCommand(response, request, "send_message")
}

func (handler *commandHandler) checkpointAction(response http.ResponseWriter, request *http.Request) {
	handler.executeAssistantCommand(response, request, "checkpoint_action")
}

func (handler *commandHandler) confirmTaskDraft(response http.ResponseWriter, request *http.Request) {
	handler.executeAssistantCommand(response, request, "confirm_task")
}

func (handler *commandHandler) assistantRunCommand(response http.ResponseWriter, request *http.Request) {
	switch request.PathValue("action") {
	case "cancel":
		handler.executeAssistantCommand(response, request, "cancel_run")
	case "retry":
		handler.executeAssistantCommand(response, request, "retry_run")
	default:
		notFound(response, request)
	}
}

func (handler *commandHandler) executeAssistantCommand(
	response http.ResponseWriter,
	request *http.Request,
	expectedType string,
) {
	body, err := readBody(response, request)
	if err != nil {
		writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
		return
	}
	command, err := contractsv1.DecodeConversationCommand(body)
	if err != nil || command.CommandType != expectedType ||
		!assistantPathMatches(request, command) {
		writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
		return
	}
	result, err := handler.assistant.ExecuteAssistantCommand(
		request.Context(), handler.actor, command,
	)
	if err != nil {
		handler.writeResult(response, CommandResult{ViewRef: result.ProjectionRef}, err)
		return
	}
	if !canonical(result.CommandID) || !canonical(result.ConversationID) ||
		result.ConversationRevision == 0 ||
		!strings.HasPrefix(result.ProjectionRef, "/api/v1/views/assistant_conversation/") ||
		(result.Status != "accepted" && result.Status != "completed") {
		writeCommandError(response, http.StatusServiceUnavailable, "command_service_unavailable", "")
		return
	}
	writeJSON(response, http.StatusAccepted, result)
}

func assistantPathMatches(request *http.Request, command contractsv1.ConversationCommand) bool {
	conversationID := request.PathValue("conversation_id")
	if command.CommandType == "create_conversation" {
		return conversationID == ""
	}
	if !canonical(conversationID) || conversationID != command.ConversationID {
		return false
	}
	if checkpointID := request.PathValue("checkpoint_id"); checkpointID != "" &&
		checkpointID != command.CheckpointID {
		return false
	}
	if taskDraftID := request.PathValue("task_draft_id"); taskDraftID != "" &&
		taskDraftID != command.TaskDraftID {
		return false
	}
	if runID := request.PathValue("assistant_run_id"); runID != "" &&
		runID != command.AssistantRunID {
		return false
	}
	return true
}

func (handler *commandHandler) createTask(response http.ResponseWriter, request *http.Request) {
	body, err := readBody(response, request)
	if err != nil {
		writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
		return
	}
	input, err := DecodeDirectTaskInput(body)
	if err != nil {
		writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
		return
	}
	result, err := handler.commands.CreateTask(request.Context(), handler.actor, input)
	handler.writeResult(response, result, err)
}

func (handler *commandHandler) taskCommand(response http.ResponseWriter, request *http.Request) {
	action := request.PathValue("action")
	if action != "accept" && action != "continue" && action != "rework" && action != "cancel" {
		notFound(response, request)
		return
	}
	if !canonical(request.PathValue("task_id")) {
		writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
		return
	}
	var input taskCommandInput
	if err := decodeRequest(response, request, &input); err != nil || input.Validate(action) != nil {
		writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
		return
	}
	result, err := handler.commands.ExecuteTaskCommand(request.Context(), TaskCommand{
		Type: action, Actor: handler.actor, TaskID: request.PathValue("task_id"),
		ExpectedTaskRevision: input.ExpectedTaskRevision,
		WorkerSessionID:      input.WorkerSessionID, ExpectedSessionRevision: input.ExpectedSessionRevision,
		WorkerRunID: input.WorkerRunID, ExpectedRunRevision: input.ExpectedRunRevision,
		AgentTurnID: input.AgentTurnID, ExpectedTurnRevision: input.ExpectedTurnRevision,
		InteractionKind: input.InteractionKind,
		IdempotencyKey:  input.IdempotencyKey, Message: input.Message,
	})
	handler.writeResult(response, result, err)
}

func (handler *commandHandler) sessionCommand(response http.ResponseWriter, request *http.Request) {
	action := request.PathValue("action")
	if action != "follow-up" && action != "queue" && action != "interrupt" &&
		action != "steer" && action != "approval" {
		notFound(response, request)
		return
	}
	if !canonical(request.PathValue("worker_session_id")) {
		writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
		return
	}
	var input sessionCommandInput
	if err := decodeRequest(response, request, &input); err != nil || input.Validate(action) != nil {
		writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
		return
	}
	result, err := handler.commands.ExecuteSessionCommand(request.Context(), SessionCommand{
		Type: action, Actor: handler.actor, WorkerSessionID: request.PathValue("worker_session_id"),
		ExpectedSessionRevision: input.ExpectedSessionRevision,
		WorkerRunID:             input.WorkerRunID, ExpectedRunRevision: input.ExpectedRunRevision,
		AgentTurnID: input.AgentTurnID, ExpectedTurnRevision: input.ExpectedTurnRevision,
		IdempotencyKey: input.IdempotencyKey, Content: input.Content, Approval: input.Approval,
	})
	handler.writeResult(response, result, err)
}

func (handler *commandHandler) writeResult(response http.ResponseWriter, result CommandResult, err error) {
	if err != nil {
		switch {
		case errors.Is(err, ErrStaleTarget):
			writeCommandError(response, http.StatusConflict, "stale_target", result.ViewRef)
		case errors.Is(err, ErrTargetUnavailable):
			writeCommandError(response, http.StatusNotFound, "target_unavailable", "")
		case errors.Is(err, ErrCommandUnsupported):
			writeCommandError(response, http.StatusUnprocessableEntity, "unsupported_command", result.ViewRef)
		case errors.Is(err, ErrCommandConflict):
			writeCommandError(response, http.StatusConflict, "command_conflict", result.ViewRef)
		default:
			writeCommandError(response, http.StatusServiceUnavailable, "command_service_unavailable", "")
		}
		return
	}
	if !canonical(result.CommandID) || (result.Status != "accepted" && result.Status != "completed") {
		writeCommandError(response, http.StatusServiceUnavailable, "command_service_unavailable", "")
		return
	}
	writeJSON(response, http.StatusAccepted, result)
}

type taskCommandInput struct {
	SchemaVersion           string `json:"schema_version"`
	IdempotencyKey          string `json:"idempotency_key"`
	ExpectedTaskRevision    uint64 `json:"expected_task_revision"`
	WorkerSessionID         string `json:"worker_session_id,omitempty"`
	ExpectedSessionRevision uint64 `json:"expected_session_revision,omitempty"`
	WorkerRunID             string `json:"worker_run_id,omitempty"`
	ExpectedRunRevision     uint64 `json:"expected_run_revision,omitempty"`
	AgentTurnID             string `json:"agent_turn_id,omitempty"`
	ExpectedTurnRevision    uint64 `json:"expected_turn_revision,omitempty"`
	InteractionKind         string `json:"interaction_kind,omitempty"`
	Message                 string `json:"message,omitempty"`
}

func (input taskCommandInput) Validate(action string) error {
	if !canonical(input.IdempotencyKey) ||
		input.ExpectedTaskRevision == 0 || strings.ContainsAny(input.Message, "\x00") {
		return fmt.Errorf("invalid Task command")
	}
	if action == "continue" {
		if input.SchemaVersion != "gateway.task_continuation.v1" ||
			!canonical(input.WorkerSessionID) || input.ExpectedSessionRevision == 0 ||
			!canonical(input.WorkerRunID) || input.ExpectedRunRevision == 0 ||
			!canonical(input.AgentTurnID) || input.ExpectedTurnRevision == 0 ||
			(input.InteractionKind != "chat" && input.InteractionKind != "rework") ||
			strings.TrimSpace(input.Message) == "" {
			return fmt.Errorf("invalid Task continuation")
		}
		return nil
	}
	if input.SchemaVersion != "gateway.task_command.v1" ||
		input.WorkerSessionID != "" || input.ExpectedSessionRevision != 0 ||
		input.WorkerRunID != "" || input.ExpectedRunRevision != 0 ||
		input.AgentTurnID != "" || input.ExpectedTurnRevision != 0 ||
		input.InteractionKind != "" {
		return fmt.Errorf("invalid Task command")
	}
	return nil
}

type sessionCommandInput struct {
	SchemaVersion           string            `json:"schema_version"`
	IdempotencyKey          string            `json:"idempotency_key"`
	ExpectedSessionRevision uint64            `json:"expected_session_revision"`
	WorkerRunID             string            `json:"worker_run_id"`
	ExpectedRunRevision     uint64            `json:"expected_run_revision"`
	AgentTurnID             string            `json:"agent_turn_id"`
	ExpectedTurnRevision    uint64            `json:"expected_turn_revision"`
	Content                 string            `json:"content,omitempty"`
	Approval                *ApprovalDecision `json:"approval,omitempty"`
}

func (input sessionCommandInput) Validate(action string) error {
	if input.SchemaVersion != "gateway.worker_command.v1" || !canonical(input.IdempotencyKey) ||
		input.ExpectedSessionRevision == 0 || !canonical(input.WorkerRunID) || input.ExpectedRunRevision == 0 ||
		!canonical(input.AgentTurnID) || input.ExpectedTurnRevision == 0 || strings.ContainsAny(input.Content, "\x00") {
		return fmt.Errorf("invalid Worker command target")
	}
	if (action == "follow-up" || action == "queue" || action == "steer") && strings.TrimSpace(input.Content) == "" {
		return fmt.Errorf("Worker command content is required")
	}
	if action == "approval" {
		if input.Approval == nil || input.Approval.Validate() != nil {
			return fmt.Errorf("approval decision is required")
		}
	} else if input.Approval != nil {
		return fmt.Errorf("approval payload is not allowed")
	}
	return nil
}

func (decision ApprovalDecision) Validate() error {
	if !canonical(decision.ApprovalRequestID) || decision.ExpectedApprovalRevision == 0 ||
		(decision.Decision != "approve" && decision.Decision != "deny") ||
		(decision.Scope != "once" && decision.Scope != "session") || !canonical(decision.Capability) ||
		!canonical(decision.Target) || !canonical(decision.Effect) {
		return fmt.Errorf("approval decision is invalid")
	}
	return nil
}

func readBody(response http.ResponseWriter, request *http.Request) ([]byte, error) {
	defer request.Body.Close()
	return io.ReadAll(http.MaxBytesReader(response, request.Body, maxCommandBodyBytes))
}

func decodeRequest(response http.ResponseWriter, request *http.Request, target any) error {
	body, err := readBody(response, request)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func writeCommandError(response http.ResponseWriter, status int, code, viewRef string) {
	writeJSON(response, status, struct {
		Code    string `json:"code"`
		ViewRef string `json:"view_ref,omitempty"`
	}{Code: code, ViewRef: viewRef})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func canonical(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, struct {
		Service string `json:"service"`
		Status  string `json:"status"`
	}{Service: "switchback-backend", Status: "ok"})
}

func notFound(response http.ResponseWriter, _ *http.Request) {
	http.Error(response, "not found", http.StatusNotFound)
}

type unavailableCommands struct{}

func (unavailableCommands) CreateTask(context.Context, FixedActor, DirectTaskInput) (CommandResult, error) {
	return CommandResult{}, ErrCommandServiceAbsent
}
func (unavailableCommands) ExecuteTaskCommand(context.Context, TaskCommand) (CommandResult, error) {
	return CommandResult{}, ErrCommandServiceAbsent
}
func (unavailableCommands) ExecuteSessionCommand(context.Context, SessionCommand) (CommandResult, error) {
	return CommandResult{}, ErrCommandServiceAbsent
}

type unavailableAssistantCommands struct{}

func (unavailableAssistantCommands) ExecuteAssistantCommand(
	context.Context,
	FixedActor,
	contractsv1.ConversationCommand,
) (AssistantCommandResult, error) {
	return AssistantCommandResult{}, ErrCommandServiceAbsent
}
