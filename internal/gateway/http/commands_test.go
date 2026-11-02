package gatewayhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
)

func TestGatewayExposesCreateTaskAndExactTaskCommands(t *testing.T) {
	commands := &recordingCommands{result: gatewayhttp.CommandResult{CommandID: "command-1", Status: "accepted"}}
	handler := commandHandler(t, commands)

	createResponse := serveJSON(t, handler, "/api/v1/tasks", directTaskInput(t, nil))
	if createResponse.Code != http.StatusAccepted || commands.createCalls != 1 ||
		commands.actor != (gatewayhttp.FixedActor{TenantID: "tenant-fixed", UserID: "user-fixed"}) {
		t.Fatalf("create response=%d calls=%d actor=%#v", createResponse.Code, commands.createCalls, commands.actor)
	}

	for _, action := range []string{"accept", "rework", "cancel"} {
		commands.result.CommandID = "command-" + action
		body := marshalJSON(t, map[string]any{
			"schema_version": "gateway.task_command.v1", "idempotency_key": "idem-" + action,
			"expected_task_revision": 7, "message": "reviewed",
		})
		response := serveJSON(t, handler, "/api/v1/tasks/task-1/"+action, body)
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s status=%d body=%s", action, response.Code, response.Body.String())
		}
		got := commands.taskCommands[len(commands.taskCommands)-1]
		if got.Type != action || got.TaskID != "task-1" || got.ExpectedTaskRevision != 7 ||
			got.IdempotencyKey != "idem-"+action || got.Actor.UserID != "user-fixed" {
			t.Fatalf("%s command = %#v", action, got)
		}
	}
}

func TestGatewayExposesExactWorkerCommands(t *testing.T) {
	commands := &recordingCommands{result: gatewayhttp.CommandResult{CommandID: "command-worker", Status: "accepted"}}
	handler := commandHandler(t, commands)
	for _, action := range []string{"follow-up", "queue", "interrupt", "steer", "approval"} {
		input := map[string]any{
			"schema_version": "gateway.worker_command.v1", "idempotency_key": "idem-" + action,
			"expected_session_revision": 9, "worker_run_id": "run-1", "expected_run_revision": 5,
			"agent_turn_id": "turn-1", "expected_turn_revision": 3,
		}
		if action == "follow-up" || action == "queue" || action == "steer" {
			input["content"] = "continue with focused tests"
		}
		if action == "approval" {
			input["approval"] = map[string]any{
				"approval_request_id": "approval-1", "expected_approval_revision": 2,
				"decision": "approve", "scope": "session", "capability": "external-write",
				"target": "repository-demo", "effect": "push-branch",
			}
		}
		response := serveJSON(t, handler, "/api/v1/worker-sessions/session-1/"+action, marshalJSON(t, input))
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s status=%d body=%s", action, response.Code, response.Body.String())
		}
		got := commands.sessionCommands[len(commands.sessionCommands)-1]
		if got.Type != action || got.WorkerSessionID != "session-1" || got.ExpectedSessionRevision != 9 ||
			got.WorkerRunID != "run-1" || got.ExpectedRunRevision != 5 || got.AgentTurnID != "turn-1" ||
			got.ExpectedTurnRevision != 3 || got.Actor.UserID != "user-fixed" {
			t.Fatalf("%s command = %#v", action, got)
		}
	}
}

func TestGatewayExposesExactTaskContinuationAndKeepsReworkRoute(t *testing.T) {
	commands := &recordingCommands{result: gatewayhttp.CommandResult{
		CommandID: "command-continue", Status: "accepted",
		ViewRef: "/api/v1/views/worker_detail/session-1",
	}}
	handler := commandHandler(t, commands)
	body := marshalJSON(t, map[string]any{
		"schema_version":            "gateway.task_continuation.v1",
		"idempotency_key":           "continue-task-1",
		"expected_task_revision":    7,
		"worker_session_id":         "session-1",
		"expected_session_revision": 9,
		"worker_run_id":             "run-1",
		"expected_run_revision":     5,
		"agent_turn_id":             "turn-1",
		"expected_turn_revision":    3,
		"interaction_kind":          "chat",
		"message":                   "Continue from the delivered result.",
	})
	response := serveJSON(t, handler, "/api/v1/tasks/task-1/continue", body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("continue status=%d body=%s", response.Code, response.Body.String())
	}
	got := commands.taskCommands[len(commands.taskCommands)-1]
	if got.Type != "continue" || got.TaskID != "task-1" || got.ExpectedTaskRevision != 7 ||
		got.WorkerSessionID != "session-1" || got.ExpectedSessionRevision != 9 ||
		got.WorkerRunID != "run-1" || got.ExpectedRunRevision != 5 ||
		got.AgentTurnID != "turn-1" || got.ExpectedTurnRevision != 3 ||
		got.InteractionKind != "chat" || got.Message != "Continue from the delivered result." {
		t.Fatalf("continue command = %#v", got)
	}

	legacy := marshalJSON(t, map[string]any{
		"schema_version":         "gateway.task_command.v1",
		"idempotency_key":        "rework-task-1",
		"expected_task_revision": 7,
		"message":                "Keep the established Request Changes route.",
	})
	response = serveJSON(t, handler, "/api/v1/tasks/task-1/rework", legacy)
	if response.Code != http.StatusAccepted ||
		commands.taskCommands[len(commands.taskCommands)-1].Type != "rework" {
		t.Fatalf("legacy rework status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGatewayExposesRevisionedAssistantCommands(t *testing.T) {
	assistant := &recordingAssistantCommands{result: gatewayhttp.AssistantCommandResult{
		CommandID: "assistant-command-1", ConversationID: "conversation-1",
		ConversationRevision: 2,
		ProjectionRef:        "/api/v1/views/assistant_conversation/conversation-1",
		Status:               "accepted",
	}}
	handler := assistantCommandHandler(t, assistant)
	base := contractsv1.ConversationCommand{
		SchemaVersion:  contractsv1.ConversationCommandSchemaVersion,
		ConversationID: "conversation-1", ExpectedRevision: 1,
		IdempotencyKey: "assistant-idempotency-1",
	}
	tests := []struct {
		commandType string
		path        string
		apply       func(*contractsv1.ConversationCommand)
	}{
		{
			commandType: "create_conversation", path: "/api/v1/assistant/conversations",
			apply: func(command *contractsv1.ConversationCommand) {
				command.ConversationID = ""
				command.ExpectedRevision = 0
			},
		},
		{commandType: "open_conversation", path: "/api/v1/assistant/conversations/conversation-1/open"},
		{
			commandType: "select_workspace", path: "/api/v1/assistant/conversations/conversation-1/workspace",
			apply: func(command *contractsv1.ConversationCommand) {
				command.WorkspaceID = "workspace-local"
				command.WorkspaceRevisionID = "workspace-revision-local-v1"
				command.WorkspaceResolutionID = "resolution-local"
				command.WorkspaceResolutionRevision = 1
				command.MembershipRevision = 1
			},
		},
		{
			commandType: "send_message", path: "/api/v1/assistant/conversations/conversation-1/messages",
			apply: func(command *contractsv1.ConversationCommand) { command.Content = "Build it." },
		},
		{
			commandType: "checkpoint_action",
			path:        "/api/v1/assistant/conversations/conversation-1/checkpoints/checkpoint-1/actions",
			apply: func(command *contractsv1.ConversationCommand) {
				command.CheckpointID = "checkpoint-1"
				command.CheckpointRevision = 1
				command.CheckpointAction = "continue"
			},
		},
		{
			commandType: "confirm_task",
			path:        "/api/v1/assistant/conversations/conversation-1/task-drafts/draft-1/confirm",
			apply: func(command *contractsv1.ConversationCommand) {
				command.TaskDraftID = "draft-1"
				command.TaskDraftRevision = 1
				command.ReadinessAssessmentID = "assessment-1"
				command.ReadinessAssessmentRevision = 1
				command.WorkspaceID = "workspace-local"
				command.WorkspaceRevisionID = "workspace-revision-local-v1"
				command.WorkspaceResolutionID = "resolution-local"
				command.WorkspaceResolutionRevision = 1
				command.MembershipRevision = 1
				command.TaskSpecDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				command.TriggerPolicyRevision = "assistant-trigger-v1"
			},
		},
		{
			commandType: "cancel_run",
			path:        "/api/v1/assistant/conversations/conversation-1/runs/run-1/cancel",
			apply: func(command *contractsv1.ConversationCommand) {
				command.AssistantRunID = "run-1"
				command.AssistantRunRevision = 1
			},
		},
		{
			commandType: "retry_run",
			path:        "/api/v1/assistant/conversations/conversation-1/runs/run-1/retry",
			apply: func(command *contractsv1.ConversationCommand) {
				command.AssistantRunID = "run-1"
				command.AssistantRunRevision = 1
			},
		},
	}
	for index, test := range tests {
		command := base
		command.CommandType = test.commandType
		command.IdempotencyKey = fmt.Sprintf("assistant-idempotency-%d", index+1)
		if test.apply != nil {
			test.apply(&command)
		}
		response := serveJSON(t, handler, test.path, marshalJSON(t, command))
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s status=%d body=%s", test.commandType, response.Code, response.Body.String())
		}
		got := assistant.commands[len(assistant.commands)-1]
		if got.CommandType != test.commandType ||
			(test.commandType != "create_conversation" && got.ConversationID != "conversation-1") {
			t.Fatalf("%s command = %+v", test.commandType, got)
		}
	}
}

func TestGatewayRejectsAssistantPathIdentityMismatch(t *testing.T) {
	assistant := &recordingAssistantCommands{result: gatewayhttp.AssistantCommandResult{
		CommandID: "unused", ConversationID: "conversation-1", ConversationRevision: 1,
		ProjectionRef: "/api/v1/views/assistant_conversation/conversation-1", Status: "accepted",
	}}
	handler := assistantCommandHandler(t, assistant)
	command := contractsv1.ConversationCommand{
		SchemaVersion: contractsv1.ConversationCommandSchemaVersion, CommandType: "send_message",
		ConversationID: "conversation-other", ExpectedRevision: 1,
		IdempotencyKey: "assistant-mismatch", Content: "Do not route this.",
	}
	response := serveJSON(
		t, handler, "/api/v1/assistant/conversations/conversation-1/messages", marshalJSON(t, command),
	)
	if response.Code != http.StatusBadRequest || len(assistant.commands) != 0 {
		t.Fatalf("path mismatch status=%d commands=%d", response.Code, len(assistant.commands))
	}
}

func TestGatewayAssistantCommandsConvergePendingRetryAndSanitizeFailures(t *testing.T) {
	result := gatewayhttp.AssistantCommandResult{
		CommandID: "assistant-command-original", ConversationID: "conversation-1",
		ConversationRevision: 2, TurnID: "turn-1", AssistantRunID: "assistant-run-1",
		ProjectionRef: "/api/v1/views/assistant_conversation/conversation-1",
		Status:        "accepted",
	}
	assistant := &recordingAssistantCommands{result: result}
	handler := assistantCommandHandler(t, assistant)
	command := contractsv1.ConversationCommand{
		SchemaVersion: contractsv1.ConversationCommandSchemaVersion, CommandType: "send_message",
		ConversationID: "conversation-1", ExpectedRevision: 1,
		IdempotencyKey: "assistant-response-loss", Content: "Build it.",
	}
	body := marshalJSON(t, command)
	first := serveJSON(t, handler, "/api/v1/assistant/conversations/conversation-1/messages", body)
	second := serveJSON(t, handler, "/api/v1/assistant/conversations/conversation-1/messages", body)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted ||
		first.Body.String() != second.Body.String() || len(assistant.commands) != 2 ||
		strings.Contains(first.Body.String(), "queued") ||
		strings.Contains(first.Body.String(), "TaskDraft") {
		t.Fatalf("pending retry did not converge: first=%q second=%q calls=%d",
			first.Body.String(), second.Body.String(), len(assistant.commands))
	}

	assistant.err = fmt.Errorf("%w: provider leaked sk-never-return", gatewayhttp.ErrStaleTarget)
	assistant.result = gatewayhttp.AssistantCommandResult{
		ProjectionRef: "/api/v1/views/assistant_conversation/conversation-1",
	}
	stale := serveJSON(t, handler, "/api/v1/assistant/conversations/conversation-1/messages", body)
	if stale.Code != http.StatusConflict ||
		!strings.Contains(stale.Body.String(), `"code":"stale_target"`) ||
		!strings.Contains(stale.Body.String(), `"view_ref":"/api/v1/views/assistant_conversation/conversation-1"`) ||
		strings.Contains(stale.Body.String(), "sk-never-return") {
		t.Fatalf("Assistant stale response=%d body=%s", stale.Code, stale.Body.String())
	}
	assistant.err = fmt.Errorf("%w: conversation-secret", gatewayhttp.ErrTargetUnavailable)
	unavailable := serveJSON(t, handler, "/api/v1/assistant/conversations/conversation-1/messages", body)
	if unavailable.Code != http.StatusNotFound ||
		unavailable.Body.String() != "{\"code\":\"target_unavailable\"}\n" {
		t.Fatalf("Assistant unavailable response=%d body=%q", unavailable.Code, unavailable.Body.String())
	}
}

func TestGatewayRejectsAssistantIdentityOverrideFields(t *testing.T) {
	assistant := &recordingAssistantCommands{result: gatewayhttp.AssistantCommandResult{
		CommandID: "unused", ConversationID: "conversation-1", ConversationRevision: 1,
		ProjectionRef: "/api/v1/views/assistant_conversation/conversation-1", Status: "accepted",
	}}
	handler := assistantCommandHandler(t, assistant)
	override := marshalJSON(t, map[string]any{
		"schema_version": "assistant.conversation_command.v1",
		"command_type":   "send_message", "conversation_id": "conversation-1",
		"expected_revision": 1, "idempotency_key": "assistant-override",
		"content": "Do work.", "tenant_id": "tenant-attacker", "actor_user_id": "user-attacker",
	})
	response := serveJSON(
		t, handler, "/api/v1/assistant/conversations/conversation-1/messages", override,
	)
	if response.Code != http.StatusBadRequest || len(assistant.commands) != 0 {
		t.Fatalf("Assistant identity override status=%d commands=%d",
			response.Code, len(assistant.commands))
	}
}

func TestGatewayRejectsIdentityOverrideAndIncompleteExactTarget(t *testing.T) {
	commands := &recordingCommands{result: gatewayhttp.CommandResult{CommandID: "unused", Status: "accepted"}}
	handler := commandHandler(t, commands)
	override := marshalJSON(t, map[string]any{
		"schema_version": "gateway.task_command.v1", "idempotency_key": "idem-1",
		"expected_task_revision": 2, "tenant_id": "tenant-attacker",
	})
	if response := serveJSON(t, handler, "/api/v1/tasks/task-secret/accept", override); response.Code != http.StatusBadRequest {
		t.Fatalf("identity override status=%d", response.Code)
	}
	incomplete := marshalJSON(t, map[string]any{
		"schema_version": "gateway.worker_command.v1", "idempotency_key": "idem-2",
		"expected_session_revision": 1, "worker_run_id": "run-1",
	})
	if response := serveJSON(t, handler, "/api/v1/worker-sessions/session-secret/interrupt", incomplete); response.Code != http.StatusBadRequest {
		t.Fatalf("incomplete target status=%d", response.Code)
	}
	if len(commands.taskCommands) != 0 || len(commands.sessionCommands) != 0 {
		t.Fatal("invalid commands reached application service")
	}
}

func TestGatewayMapsStaleAndUnavailableErrorsWithoutDisclosure(t *testing.T) {
	commands := &recordingCommands{
		result: gatewayhttp.CommandResult{ViewRef: "/api/v1/views/worker/session-1"},
		err:    fmt.Errorf("%w: provider returned known-value-never-expose", gatewayhttp.ErrStaleTarget),
	}
	handler := commandHandler(t, commands)
	body := marshalJSON(t, map[string]any{
		"schema_version": "gateway.task_command.v1", "idempotency_key": "idem-stale",
		"expected_task_revision": 7,
	})
	response := serveJSON(t, handler, "/api/v1/tasks/task-1/accept", body)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"stale_target"`) ||
		strings.Contains(response.Body.String(), "known-value-never-expose") {
		t.Fatalf("stale response status=%d body=%s", response.Code, response.Body.String())
	}

	commands.err = fmt.Errorf("%w: task-secret exists but actor is wrong", gatewayhttp.ErrTargetUnavailable)
	response = serveJSON(t, handler, "/api/v1/tasks/task-secret/accept", body)
	if response.Code != http.StatusNotFound || response.Body.String() != "{\"code\":\"target_unavailable\"}\n" {
		t.Fatalf("unavailable response status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestGatewayRetryReturnsOriginalCommandResult(t *testing.T) {
	sequence := uint64(14)
	commands := &recordingCommands{result: gatewayhttp.CommandResult{
		CommandID: "command-original", Status: "accepted", QueueSequence: &sequence,
	}}
	handler := commandHandler(t, commands)
	body := marshalJSON(t, map[string]any{
		"schema_version": "gateway.worker_command.v1", "idempotency_key": "same-key",
		"expected_session_revision": 9, "worker_run_id": "run-1", "expected_run_revision": 5,
		"agent_turn_id": "turn-1", "expected_turn_revision": 3, "content": "queued content",
	})
	first := serveJSON(t, handler, "/api/v1/worker-sessions/session-1/queue", body)
	second := serveJSON(t, handler, "/api/v1/worker-sessions/session-1/queue", body)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted || first.Body.String() != second.Body.String() ||
		!reflect.DeepEqual(commands.sessionCommands[0], commands.sessionCommands[1]) {
		t.Fatalf("retry first=%q second=%q commands=%#v", first.Body.String(), second.Body.String(), commands.sessionCommands)
	}
}

func commandHandler(t *testing.T, commands gatewayhttp.CommandService) http.Handler {
	t.Helper()
	handler, err := gatewayhttp.NewHandlerWithOptions(gatewayhttp.HandlerOptions{
		Actor: gatewayhttp.FixedActor{TenantID: "tenant-fixed", UserID: "user-fixed"}, Commands: commands,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func assistantCommandHandler(t *testing.T, assistant gatewayhttp.AssistantCommandService) http.Handler {
	t.Helper()
	handler, err := gatewayhttp.NewHandlerWithOptions(gatewayhttp.HandlerOptions{
		Actor: gatewayhttp.FixedActor{TenantID: "tenant-fixed", UserID: "user-fixed"},
		Commands: &recordingCommands{result: gatewayhttp.CommandResult{
			CommandID: "unused", Status: "accepted",
		}},
		Assistant: assistant,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveJSON(t *testing.T, handler http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type recordingCommands struct {
	result          gatewayhttp.CommandResult
	err             error
	actor           gatewayhttp.FixedActor
	createCalls     int
	taskCommands    []gatewayhttp.TaskCommand
	sessionCommands []gatewayhttp.SessionCommand
}

type recordingAssistantCommands struct {
	result   gatewayhttp.AssistantCommandResult
	err      error
	actor    gatewayhttp.FixedActor
	commands []contractsv1.ConversationCommand
}

func (commands *recordingAssistantCommands) ExecuteAssistantCommand(
	_ context.Context,
	actor gatewayhttp.FixedActor,
	command contractsv1.ConversationCommand,
) (gatewayhttp.AssistantCommandResult, error) {
	commands.actor = actor
	commands.commands = append(commands.commands, command)
	return commands.result, commands.err
}

func (commands *recordingCommands) CreateTask(_ context.Context, actor gatewayhttp.FixedActor, _ gatewayhttp.DirectTaskInput) (gatewayhttp.CommandResult, error) {
	commands.actor = actor
	commands.createCalls++
	return commands.result, commands.err
}

func (commands *recordingCommands) ExecuteTaskCommand(_ context.Context, command gatewayhttp.TaskCommand) (gatewayhttp.CommandResult, error) {
	commands.taskCommands = append(commands.taskCommands, command)
	return commands.result, commands.err
}

func (commands *recordingCommands) ExecuteSessionCommand(_ context.Context, command gatewayhttp.SessionCommand) (gatewayhttp.CommandResult, error) {
	commands.sessionCommands = append(commands.sessionCommands, command)
	return commands.result, commands.err
}
