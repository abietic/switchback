//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionpostgres "github.com/abietic/switchback/internal/interaction/adapter/postgres"
	"github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerfake "github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRequireSchemaRequiresDurableWorkerTurnSettlement(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	if err := requireSchema(ctx, pool, false); err != nil {
		t.Fatalf("complete schema rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `
ALTER TABLE runtime_recovery_incidents
    DROP CONSTRAINT IF EXISTS runtime_recovery_incidents_deadline_settlement_check,
    DROP COLUMN IF EXISTS preempted_by_turn_settlement_id;
DROP TABLE worker_turn_settlements`); err != nil {
		t.Fatal(err)
	}
	if err := requireSchema(ctx, pool, false); err == nil ||
		!strings.Contains(err.Error(), "schema is incomplete") {
		t.Fatalf("missing Turn settlement schema error=%v", err)
	}
}

func TestProductCompositionServesGatewayAndLiveFrontierViews(t *testing.T) {
	pool := postgrestest.Open(t)
	databaseURL := os.Getenv("SWITCHBACK_TEST_DATABASE_URL")
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := newWithPool(context.Background(), Config{
		Profile: profile, NetworkProfile: integrationNetworkProfile(t),
		DatabaseURL: databaseURL, DataRoot: t.TempDir(),
		ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID,
		WorkspaceRevisionID: defaultWorkspaceRevisionID, GRPCAddress: defaultGRPCAddress,
	}, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Run(ctx) }()
	empty := httptest.NewRecorder()
	app.Handler().ServeHTTP(empty, httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/index", nil))
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"tasks":[]`) {
		t.Fatalf("empty Task index status=%d body=%s", empty.Code, empty.Body.String())
	}

	body := map[string]any{
		"schema_version": "gateway.direct_task_input.v1", "idempotency_key": "composition-create",
		"project_id": defaultProjectID, "workspace_id": defaultWorkspaceID,
		"workspace_revision_id": defaultWorkspaceRevisionID, "title": "Use the product composition",
		"goal":                "Create a Task through the default Backend entry.",
		"target":              map[string]string{"kind": "workspace", "ref": defaultWorkspaceRevisionID},
		"constraints":         []string{"Keep identity server-owned."},
		"acceptance_criteria": []string{"Frontier index becomes readable."},
		"agent_profile_id":    "deepseek-codex-openai-v1", "acceptance_mode": "manual",
		"user_visible_types": []string{"report", "patch"},
	}
	encoded, _ := json.Marshal(body)
	create := httptest.NewRecorder()
	app.Handler().ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(encoded)))
	if create.Code != http.StatusAccepted {
		t.Fatalf("CreateTask status=%d body=%s", create.Code, create.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		index := httptest.NewRecorder()
		app.Handler().ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/index", nil))
		if index.Code == http.StatusOK && strings.Contains(index.Body.String(), "Use the product composition") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Frontier index status=%d body=%s", index.Code, index.Body.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestProductCompositionProcessesAssistantRunThroughLiteLLMAndFrontier(t *testing.T) {
	proxyKey := "assistant-proxy-test-key"
	model := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" ||
			request.Header.Get("Authorization") != "Bearer "+proxyKey ||
			request.Header.Get("X-Switchback-Assistant-Run-ID") == "" {
			http.Error(response, "invalid trusted Assistant request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || bytes.Contains(body, []byte(proxyKey)) {
			http.Error(response, "invalid Assistant body", http.StatusBadRequest)
			return
		}
		decision := assistantClarificationDecision(time.Now().UTC())
		decisionJSON, _ := json.Marshal(decision)
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("X-Request-ID", "assistant-provider-request-1")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "completion-1", "model": "kimi-for-coding",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": string(decisionJSON)},
			}},
			"usage": map[string]int{"prompt_tokens": 30, "completion_tokens": 20},
		})
	}))
	t.Cleanup(model.Close)

	pool := postgrestest.Open(t)
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.LoadAssistantSettings(mapLookup(map[string]string{
		"SWITCHBACK_M2_ASSISTANT_ENABLED": "1",
	}), config.ProductionProfileRegistry())
	if err != nil {
		t.Fatal(err)
	}
	app, err := newWithPool(context.Background(), Config{
		Profile: profile, NetworkProfile: integrationNetworkProfile(t), Assistant: settings,
		DatabaseURL: os.Getenv("SWITCHBACK_TEST_DATABASE_URL"), DataRoot: t.TempDir(),
		ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID,
		WorkspaceRevisionID: defaultWorkspaceRevisionID, GRPCAddress: defaultGRPCAddress,
		AssistantLiteLLMBaseURL: model.URL, AssistantLiteLLMAPIKey: proxyKey,
	}, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Run(ctx) }()

	created := postAssistantCommand(t, app.Handler(), "/api/v1/assistant/conversations", map[string]any{
		"schema_version": contractsv1.ConversationCommandSchemaVersion,
		"command_type":   "create_conversation", "idempotency_key": "assistant-create-composition",
	})
	selected := postAssistantCommand(t, app.Handler(),
		"/api/v1/assistant/conversations/"+created.ConversationID+"/workspace", map[string]any{
			"schema_version": contractsv1.ConversationCommandSchemaVersion,
			"command_type":   "select_workspace", "conversation_id": created.ConversationID,
			"expected_revision": created.ConversationRevision, "idempotency_key": "assistant-workspace-composition",
			"workspace_id": defaultWorkspaceID, "workspace_revision_id": defaultWorkspaceRevisionID,
			"workspace_resolution_id": "resolution-local", "workspace_resolution_revision": 1,
			"membership_revision": 1,
		})
	sent := postAssistantCommand(t, app.Handler(),
		"/api/v1/assistant/conversations/"+created.ConversationID+"/messages", map[string]any{
			"schema_version": contractsv1.ConversationCommandSchemaVersion,
			"command_type":   "send_message", "conversation_id": created.ConversationID,
			"expected_revision": selected.ConversationRevision, "idempotency_key": "assistant-message-composition",
			"content": "Please improve this workspace.",
		})
	if sent.AssistantRunID == "" || sent.TurnID == "" || sent.Status != "accepted" {
		t.Fatalf("Assistant message acceptance=%+v", sent)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		view := httptest.NewRecorder()
		app.Handler().ServeHTTP(view, httptest.NewRequest(
			http.MethodGet, "/api/v1/views/assistant_conversation/"+created.ConversationID, nil,
		))
		if view.Code == http.StatusOK &&
			strings.Contains(view.Body.String(), "What concrete outcome should this work achieve?") &&
			strings.Contains(view.Body.String(), `"status":"completed"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Assistant projection did not converge: status=%d body=%s", view.Code, view.Body.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestAssistantClarificationConfirmsTaskAndDispatchesFakeWorker(t *testing.T) {
	var calls atomic.Uint64
	proxyKey := "assistant-proxy-test-key"
	model := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var decision contractsv1.AssistantDecision
		if calls.Add(1) == 1 {
			decision = assistantClarificationDecision(time.Now().UTC())
		} else {
			decision = assistantReadyDecision(time.Now().UTC())
		}
		decisionJSON, _ := json.Marshal(decision)
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("X-Request-ID", "assistant-provider-request")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "completion", "model": "kimi-for-coding",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": string(decisionJSON)},
			}},
			"usage": map[string]int{"prompt_tokens": 30, "completion_tokens": 20},
		})
	}))
	t.Cleanup(model.Close)

	pool := postgrestest.Open(t)
	runtimecapacity.Initialize(t, pool)
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.LoadAssistantSettings(mapLookup(map[string]string{
		"SWITCHBACK_M2_ASSISTANT_ENABLED": "1",
	}), config.ProductionProfileRegistry())
	if err != nil {
		t.Fatal(err)
	}
	app, err := newWithPool(context.Background(), Config{
		Profile: profile, NetworkProfile: integrationNetworkProfile(t), Assistant: settings,
		DatabaseURL: os.Getenv("SWITCHBACK_TEST_DATABASE_URL"), DataRoot: t.TempDir(),
		ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID,
		WorkspaceRevisionID: defaultWorkspaceRevisionID, GRPCAddress: defaultGRPCAddress,
		AssistantLiteLLMBaseURL: model.URL, AssistantLiteLLMAPIKey: proxyKey,
	}, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Run(ctx) }()

	created := postAssistantCommand(t, app.Handler(), "/api/v1/assistant/conversations", map[string]any{
		"schema_version": contractsv1.ConversationCommandSchemaVersion,
		"command_type":   "create_conversation", "idempotency_key": "assistant-e2e-create",
	})
	selected := postAssistantCommand(t, app.Handler(),
		"/api/v1/assistant/conversations/"+created.ConversationID+"/workspace", map[string]any{
			"schema_version": contractsv1.ConversationCommandSchemaVersion,
			"command_type":   "select_workspace", "conversation_id": created.ConversationID,
			"expected_revision": created.ConversationRevision, "idempotency_key": "assistant-e2e-workspace",
			"workspace_id": defaultWorkspaceID, "workspace_revision_id": defaultWorkspaceRevisionID,
			"workspace_resolution_id": "resolution-local", "workspace_resolution_revision": 1,
			"membership_revision": 1,
		})
	postAssistantCommand(t, app.Handler(),
		"/api/v1/assistant/conversations/"+created.ConversationID+"/messages", map[string]any{
			"schema_version": contractsv1.ConversationCommandSchemaVersion,
			"command_type":   "send_message", "conversation_id": created.ConversationID,
			"expected_revision": selected.ConversationRevision, "idempotency_key": "assistant-e2e-ambiguous",
			"content": "Please improve this workspace.",
		})
	clarifying := waitAssistantProjection(t, app.Handler(), created.ConversationID, func(data assistantProjectionData) bool {
		return data.Readiness != nil && data.Readiness.Status == "clarifying" &&
			data.AssistantRun != nil && data.AssistantRun.Status == "completed"
	})
	postAssistantCommand(t, app.Handler(),
		"/api/v1/assistant/conversations/"+created.ConversationID+"/messages", map[string]any{
			"schema_version": contractsv1.ConversationCommandSchemaVersion,
			"command_type":   "send_message", "conversation_id": created.ConversationID,
			"expected_revision": clarifying.Revision, "idempotency_key": "assistant-e2e-answer",
			"content": "Repair the counter and prove all repository tests pass.",
		})
	ready := waitAssistantProjection(t, app.Handler(), created.ConversationID, func(data assistantProjectionData) bool {
		return data.Readiness != nil && data.Readiness.Status == "ready_confirmed" &&
			data.TaskDraft != nil && data.TaskDraft.Status == "current"
	})
	draft := ready.TaskDraft
	readiness := ready.Readiness
	if draft == nil || readiness == nil {
		t.Fatal("ready Assistant projection omitted exact confirmation evidence")
	}
	confirmed := postAssistantCommand(t, app.Handler(),
		"/api/v1/assistant/conversations/"+created.ConversationID+"/task-drafts/"+draft.TaskDraftID+"/confirm",
		map[string]any{
			"schema_version": contractsv1.ConversationCommandSchemaVersion,
			"command_type":   "confirm_task", "conversation_id": created.ConversationID,
			"expected_revision": ready.Revision, "idempotency_key": "assistant-e2e-confirm",
			"task_draft_id": draft.TaskDraftID, "task_draft_revision": draft.Revision,
			"readiness_assessment_id":       readiness.AssessmentID,
			"readiness_assessment_revision": readiness.Revision,
			"workspace_id":                  ready.WorkspaceID,
			"workspace_revision_id":         draft.TaskSpec.WorkspaceResolutionRef.WorkspaceRevisionID,
			"workspace_resolution_id":       draft.TaskSpec.WorkspaceResolutionRef.ResolutionID,
			"workspace_resolution_revision": draft.TaskSpec.WorkspaceResolutionRef.Revision,
			"membership_revision":           draft.TaskSpec.WorkspaceResolutionRef.MembershipRevision,
			"task_spec_digest":              draft.TaskSpec.CreationIntentRef.TaskSpecDigest,
			"trigger_policy_revision":       draft.TaskSpec.CreationIntentRef.TriggerPolicyRevision,
		})
	if confirmed.TaskID == "" || confirmed.TaskRevision != 1 {
		t.Fatalf("Assistant Task confirmation = %+v", confirmed)
	}

	var allocationRequestID string
	if err := pool.QueryRow(ctx, `
SELECT allocation_request_id FROM session_allocation_requests WHERE owner_id=$1`,
		confirmed.TaskID,
	).Scan(&allocationRequestID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ids := randomIDs{}
	allocated, err := (workerapp.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: ids, Now: func() time.Time { return now },
	}).Execute(ctx, workerapp.AllocateWorkerSessionCommand{
		TenantID: profile.TenantID, UserID: profile.ActorUserID, AllocationRequestID: allocationRequestID,
	})
	if err != nil {
		t.Fatal(err)
	}
	enqueued, err := (taskapp.SessionDispatchService{
		Store: taskpostgres.NewStore(pool), IDs: ids, Now: func() time.Time { return now },
	}).EnqueueTask(ctx, taskapp.EnqueueTaskCommand{
		TenantID: profile.TenantID, UserID: profile.ActorUserID, TaskID: confirmed.TaskID,
		WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: confirmed.TaskRevision,
		ExpectedSessionRevision: 1, IdempotencyKey: "assistant-e2e-enqueue",
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := taskapp.SessionDispatchService{
		Store: taskpostgres.NewStore(pool), IDs: ids, Now: func() time.Time { return now },
	}
	permit, err := dispatch.IssueTaskPermit(ctx, taskapp.IssueTaskPermitCommand{
		TenantID: profile.TenantID, UserID: profile.ActorUserID,
		WorkerSessionID: allocated.WorkerSessionID, RuntimeGeneration: 1,
		IdempotencyKey: "assistant-e2e-dispatch",
	})
	if err != nil {
		t.Fatal(err)
	}
	workerProfile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	started, err := (workerapp.StartWorkerRunService{
		Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: func() time.Time { return now },
	}).Execute(ctx, workerapp.StartWorkerRunCommand{
		TenantID: profile.TenantID, UserID: profile.ActorUserID,
		WorkerSessionID: allocated.WorkerSessionID, TaskID: confirmed.TaskID, PermitID: permit.PermitID,
		ExpectedSessionRevision: permit.ExpectedSessionRevision, RuntimeGeneration: 1,
		TriggerType: "task_initial", StartSnapshot: json.RawMessage(`{"working_copy":"assistant-e2e"}`),
		AgentProfileID: workerProfile.ID, AgentProfileRevision: "1", AgentProfileDigest: workerProfile.Digest,
		AgentTurnContent:   json.RawMessage(`{"text":"Repair the counter"}`),
		TurnIdempotencyKey: "assistant-e2e-turn", RuntimeImageRef: "sha256:" + strings.Repeat("a", 64),
		ResourceProfileID: config.LocalStandardV1, NetworkPolicyID: config.LegacyPublicWebRestrictedV1,
		NetworkEnforcementStatus: config.NetworkEnforcementLegacy, ControlChannelEpoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan workerapp.AgentUpdate, 1)
	updates <- workerapp.AgentUpdate{Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":"Started the accepted Task."}`)}
	close(updates)
	agent := &workerfake.AgentAdapter{
		Capabilities: workerapp.AgentCapabilities{ProtocolVersion: "1", Prompt: true, Cancel: true},
		SessionID:    started.AgentSessionID, Updates: updates,
	}
	if _, err := agent.Initialize(ctx, workerapp.AgentInitializeRequest{
		WorkerSessionID: started.WorkerSessionID, RuntimeID: started.RuntimeInstanceID,
		Generation: 1, ProtocolVersion: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if opened, err := agent.OpenSession(ctx, workerapp.AgentSessionRequest{
		WorkerSessionID: started.WorkerSessionID, AgentSessionID: started.AgentSessionID,
		WorkingCopyPath: "/workspace",
	}); err != nil || opened != started.AgentSessionID {
		t.Fatalf("fake Agent session=%s error=%v", opened, err)
	}
	stream, err := agent.Prompt(ctx, workerapp.AgentPromptRequest{
		AgentSessionID: started.AgentSessionID, AgentTurnID: started.AgentTurnID,
		Content: "Repair the counter",
	})
	if err != nil {
		t.Fatal(err)
	}
	if update, ok := <-stream; !ok || update.Kind != "agent_message_chunk" {
		t.Fatalf("fake Agent did not receive dispatched Turn: update=%+v open=%v", update, ok)
	}
	task, err := taskpostgres.NewStore(pool).ReadTask(ctx, confirmed.TaskID)
	if err != nil || task.Status != "running" || task.WorkerSessionID != started.WorkerSessionID ||
		enqueued.TaskRevision+1 != task.Revision {
		t.Fatalf("Assistant-dispatched Task=%+v error=%v", task, err)
	}
}

func TestRuntimeRunnerAllocatesIndependentTaskWhileExitedRuntimeHasNoTerminal(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	workspaceSource := t.TempDir()
	credentialPath := filepath.Join(t.TempDir(), "provider-key")
	if err := os.WriteFile(credentialPath, []byte("test-provider-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	grpcAddress := reserveRuntimeRunnerAddress(t)
	app, err := newWithPool(context.Background(), Config{
		Profile: profile, NetworkProfile: integrationNetworkProfile(t),
		DatabaseURL: os.Getenv("SWITCHBACK_TEST_DATABASE_URL"),
		DataRoot:    dataRoot, EngineDataRoot: dataRoot,
		WorkspaceSourceRoot: workspaceSource,
		ProjectID:           defaultProjectID, WorkspaceID: defaultWorkspaceID,
		WorkspaceRevisionID: defaultWorkspaceRevisionID,
		WorkerImage:         "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkerNetwork:       "switchback-task18-test",
		GRPCAddress:         grpcAddress, BackendGRPCTarget: grpcAddress,
		BackendHTTPBaseURL:     "http://127.0.0.1:8080",
		DeepSeekCredentialFile: credentialPath,
		LangfuseBaseURL:        "http://127.0.0.1:1",
		LangfusePublicKey:      "test-langfuse-public-key",
		LangfuseSecretKey:      "test-langfuse-secret-key",
		ExecutionEnabled:       true,
		RuntimeRecoveryEnabled: false,
	}, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.runtime.environment.storage.Ownership = localstorage.OwnershipFunc(
		func(string, int, int) error { return nil },
	)
	app.runtime.environment.secrets.Ownership = runtimesecret.OwnershipFunc(
		func(string, int, int) error { return nil },
	)
	app.runtime.environment.secrets.OwnerUID = func(string) (uint32, error) {
		return 0, nil
	}

	runnerCtx, cancelRunner := context.WithCancel(context.Background())
	t.Cleanup(cancelRunner)
	fakeRuntime := newExitedRuntimeProvisioner(
		runnerCtx,
		app.runtime,
		grpcAddress,
	)
	app.runtime.starts.CreatedLayerChecker = syntheticInitialCreatedLayerChecker{}
	app.runtime.starts.Provisioner = fakeRuntime
	allowSyntheticRuntimeStarts(t, app)
	runResult := make(chan error, 1)
	go func() { runResult <- app.runtime.Run(runnerCtx) }()
	t.Cleanup(func() {
		cancelRunner()
		select {
		case err := <-runResult:
			if err != nil {
				t.Errorf("runtimeRunner.Run stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("runtimeRunner.Run did not stop")
		}
	})

	taskA := createRuntimeRunnerTask(
		t,
		app.Handler(),
		pool,
		"runtime-starvation-task-a",
		"Keep the first Runtime active.",
	)
	select {
	case err := <-fakeRuntime.failures:
		t.Fatal(err)
	case <-fakeRuntime.turnActive:
	case <-time.After(5 * time.Second):
		t.Fatal("Task A did not reach an active AgentTurn")
	}
	taskAActive := waitRuntimeRunnerTaskState(t, pool, taskA, func(state runtimeRunnerTaskState) bool {
		return state.TaskStatus == "running" &&
			state.WorkerSessionID != "" &&
			state.WorkerRunID != "" &&
			state.AgentTurnStatus == "consumed" &&
			state.RuntimeStatus == "active"
	})

	close(fakeRuntime.exitFirstRuntime)
	select {
	case err := <-fakeRuntime.failures:
		t.Fatal(err)
	case <-fakeRuntime.firstRuntimeExited:
	case <-time.After(3 * time.Second):
		t.Fatal("Task A fake Runtime did not exit")
	}

	taskB := createRuntimeRunnerTask(
		t,
		app.Handler(),
		pool,
		"runtime-starvation-task-b",
		"Allocate an independent WorkerSession.",
	)
	deadline := time.Now().Add(5 * time.Second)
	var taskBState runtimeRunnerTaskState
	for time.Now().Before(deadline) {
		taskBState = readRuntimeRunnerTaskState(t, pool, taskB)
		if taskBState.WorkerSessionID != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	taskAAfterExit := waitRuntimeRunnerTaskState(t, pool, taskA, func(state runtimeRunnerTaskState) bool {
		return state.AgentTurnStatus == "failed" && state.RuntimeStatus == "terminated"
	})
	if taskAAfterExit.WorkerSessionID != taskAActive.WorkerSessionID ||
		taskAAfterExit.WorkerRunID != taskAActive.WorkerRunID ||
		taskAAfterExit.AgentTurnID != taskAActive.AgentTurnID ||
		taskAAfterExit.RuntimeInstanceID != taskAActive.RuntimeInstanceID ||
		taskAAfterExit.RuntimeGeneration != taskAActive.RuntimeGeneration {
		t.Fatalf(
			"Task A identity changed after Runtime exit: active=%+v after=%+v",
			taskAActive,
			taskAAfterExit,
		)
	}
	if taskBState.WorkerSessionID == "" {
		var allocationStatus string
		var reservedCount, quotaLimit uint64
		if err := pool.QueryRow(context.Background(), `
SELECT request.status,quota.reserved_count,quota.quota_limit
FROM session_allocation_requests request
JOIN worker_session_continuity_quotas quota
  ON quota.tenant_id=request.tenant_id AND quota.user_id=request.user_id
WHERE request.owner_id=$1`,
			taskB,
		).Scan(&allocationStatus, &reservedCount, &quotaLimit); err != nil {
			t.Fatal(err)
		}
		t.Fatalf(
			"Task B remained pending without WorkerSession while continuity quota had capacity: allocation=%s quota=%d/%d",
			allocationStatus,
			reservedCount,
			quotaLimit,
		)
	}
	if taskBState.WorkerSessionID == taskAActive.WorkerSessionID {
		t.Fatalf(
			"Task B reused Task A WorkerSession %s instead of allocating an independent Session",
			taskBState.WorkerSessionID,
		)
	}
}

func TestAssistantBackendRestartMakesUnknownModelEffectVisibleWithoutRetry(t *testing.T) {
	var modelCalls atomic.Uint64
	model := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		modelCalls.Add(1)
		http.Error(response, "model must not be retried", http.StatusInternalServerError)
	}))
	t.Cleanup(model.Close)
	pool := postgrestest.Open(t)
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.LoadAssistantSettings(mapLookup(map[string]string{
		"SWITCHBACK_M2_ASSISTANT_ENABLED": "1",
	}), config.ProductionProfileRegistry())
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	configuration := Config{
		Profile: profile, NetworkProfile: integrationNetworkProfile(t), Assistant: settings,
		DatabaseURL: os.Getenv("SWITCHBACK_TEST_DATABASE_URL"), DataRoot: dataRoot,
		ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID,
		WorkspaceRevisionID: defaultWorkspaceRevisionID, GRPCAddress: defaultGRPCAddress,
		AssistantLiteLLMBaseURL: model.URL, AssistantLiteLLMAPIKey: "assistant-proxy-test-key",
	}
	beforeRestart, err := newWithPool(context.Background(), configuration, pool)
	if err != nil {
		t.Fatal(err)
	}
	created := postAssistantCommand(t, beforeRestart.Handler(), "/api/v1/assistant/conversations", map[string]any{
		"schema_version": contractsv1.ConversationCommandSchemaVersion,
		"command_type":   "create_conversation", "idempotency_key": "assistant-restart-create",
	})
	sent := postAssistantCommand(t, beforeRestart.Handler(),
		"/api/v1/assistant/conversations/"+created.ConversationID+"/messages", map[string]any{
			"schema_version": contractsv1.ConversationCommandSchemaVersion,
			"command_type":   "send_message", "conversation_id": created.ConversationID,
			"expected_revision": created.ConversationRevision, "idempotency_key": "assistant-restart-message",
			"content": "Explain the current project.",
		})
	now := time.Now().UTC()
	store := interactionpostgres.Store{Pool: pool}
	claim, err := store.ClaimNextRun(context.Background(), "assistant-before-restart", 10*time.Millisecond, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkInvocationStarted(
		context.Background(), claim.Run.ID, claim.Run.Revision, claim.Run.FencingToken, now,
	); err != nil {
		t.Fatal(err)
	}
	beforeRestart.Close()
	time.Sleep(20 * time.Millisecond)

	afterRestart, err := newWithPool(context.Background(), configuration, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(afterRestart.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = afterRestart.Run(ctx) }()
	failed := waitAssistantProjection(t, afterRestart.Handler(), created.ConversationID, func(data assistantProjectionData) bool {
		return data.AssistantRun != nil && data.AssistantRun.Status == "failed" &&
			data.AssistantRun.ErrorCode == "model_effect_unknown"
	})
	if failed.AssistantRun == nil || failed.AssistantRun.Retryable || modelCalls.Load() != 0 {
		t.Fatalf("restart recovery projection=%+v model_calls=%d", failed.AssistantRun, modelCalls.Load())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var evidenceStatus string
		var recoveryEvents int
		err := pool.QueryRow(context.Background(), `
SELECT evidence_status,
       (SELECT count(*) FROM outbox_events
        WHERE aggregate_id=$2 AND event_type='AssistantRunRecoveryFailed.v1' AND published_at IS NOT NULL)
FROM assistant_runs WHERE assistant_run_id=$1`,
			sent.AssistantRunID, created.ConversationID,
		).Scan(&evidenceStatus, &recoveryEvents)
		if err == nil && evidenceStatus == "available" && recoveryEvents == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart evidence/outbox did not converge: status=%s events=%d error=%v",
				evidenceStatus, recoveryEvents, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

type exitedRuntimeProvisioner struct {
	ctx                context.Context
	runner             *runtimeRunner
	grpcAddress        string
	firstProvision     sync.Once
	turnActive         chan struct{}
	exitFirstRuntime   chan struct{}
	firstRuntimeExited chan struct{}
	failures           chan error
}

func newExitedRuntimeProvisioner(
	ctx context.Context,
	runner *runtimeRunner,
	grpcAddress string,
) *exitedRuntimeProvisioner {
	return &exitedRuntimeProvisioner{
		ctx:                ctx,
		runner:             runner,
		grpcAddress:        grpcAddress,
		turnActive:         make(chan struct{}),
		exitFirstRuntime:   make(chan struct{}),
		firstRuntimeExited: make(chan struct{}),
		failures:           make(chan error, 1),
	}
}

func (provisioner *exitedRuntimeProvisioner) Provision(
	ctx context.Context,
	request workerapp.RuntimeProvisionRequest,
	check workerapp.RuntimePreStartCheck,
) (workerapp.ProvisionedRuntime, error) {
	partial := workerapp.ProvisionedRuntime{RuntimeInstanceID: request.RuntimeInstanceID, ContainerID: "container-" + request.RuntimeInstanceID, Generation: request.Generation}
	if err := publishSyntheticFixtureContainer(ctx, provisioner.runner, request, partial.ContainerID); err != nil {
		return partial, err
	}
	if err := fixtureRuntimePreStart(ctx, request, partial, check); err != nil {
		return partial, err
	}
	provisioner.firstProvision.Do(func() {
		go provisioner.runFirstRuntime(request)
	})
	return workerapp.ProvisionedRuntime{
		RuntimeInstanceID: request.RuntimeInstanceID,
		ContainerID:       "container-" + request.RuntimeInstanceID,
		Generation:        request.Generation,
		StartedAt:         time.Now().UTC(),
	}, nil
}

func (*exitedRuntimeProvisioner) Inspect(
	_ context.Context,
	_ string,
) (workerapp.RuntimeInspection, error) {
	return workerapp.RuntimeInspection{}, nil
}

func (provisioner *exitedRuntimeProvisioner) runFirstRuntime(
	request workerapp.RuntimeProvisionRequest,
) {
	prepared, ok := provisioner.runner.environment.get(request.RuntimeInstanceID)
	if !ok {
		provisioner.fail(errors.New("fake Runtime could not load prepared environment"))
		return
	}
	dialCtx, cancelDial := context.WithTimeout(provisioner.ctx, 3*time.Second)
	connection, err := grpc.DialContext(
		dialCtx,
		provisioner.grpcAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	cancelDial()
	if err != nil {
		provisioner.fail(err)
		return
	}
	defer connection.Close()
	client, err := controlgrpc.NewClient(
		controlgrpc.NewWorkerControlRPCClient(connection),
		prepared.Credential,
	)
	if err != nil {
		provisioner.fail(err)
		return
	}
	runtimeCtx, cancelRuntime := context.WithCancel(provisioner.ctx)
	defer cancelRuntime()
	clientResult := make(chan error, 1)
	go func() { clientResult <- client.Run(runtimeCtx) }()
	select {
	case transition := <-client.ReadyTransitions():
		if !transition.Ready {
			provisioner.fail(errors.New("fake Runtime control did not become ready"))
			return
		}
	case err := <-clientResult:
		provisioner.fail(err)
		return
	case <-time.After(3 * time.Second):
		provisioner.fail(errors.New("fake Runtime control handshake timed out"))
		return
	}

	const agentSessionID = "agent-session-runtime-starvation-a"
	readyPayload, _ := json.Marshal(runtimeReadyPayload{
		AgentSessionID:       agentSessionID,
		AgentProfileID:       request.AgentProfile.ID,
		AgentProfileRevision: request.AgentProfile.Revision,
		AgentProfileDigest:   request.AgentProfile.Digest,
		Capabilities: workerapp.AgentCapabilities{
			ProtocolVersion: "1",
			Prompt:          true,
			Cancel:          true,
		},
	})
	if err := client.Publish(runtimeCtx, workerapp.WorkerControlEvent{
		Envelope: contractsv1.RuntimeControlEnvelope{
			MessageID:         "runtime-ready-starvation-a",
			WorkerSessionID:   contractsv1.WorkerSessionID(request.WorkerSessionID),
			WorkerRunID:       contractsv1.WorkerRunID(request.WorkerRunID),
			RuntimeInstanceID: contractsv1.RuntimeInstanceID(request.RuntimeInstanceID),
			RuntimeGeneration: uint32(request.Generation),
			AgentSessionID:    contractsv1.AgentSessionID(agentSessionID),
			AgentTurnID:       "runtime-startup",
			ChannelEpoch:      prepared.Credential.Identity.ChannelEpoch,
			IdempotencyKey:    "runtime-ready-starvation-a",
			MessageType:       "runtime.ready",
			Payload:           readyPayload,
		},
	}); err != nil {
		provisioner.fail(err)
		return
	}

	var start workerapp.WorkerControlCommand
	select {
	case start = <-client.Commands():
	case err := <-clientResult:
		provisioner.fail(err)
		return
	case <-time.After(3 * time.Second):
		provisioner.fail(errors.New("fake Runtime did not receive AgentTurn start"))
		return
	}
	if err := client.BeginTurn(start); err != nil {
		provisioner.fail(err)
		return
	}
	agentTurnID := string(start.Envelope.AgentTurnID)
	if err := client.MarkPromptIntent(agentTurnID); err != nil {
		provisioner.fail(err)
		return
	}
	if err := client.MarkPromptActive(agentTurnID); err != nil {
		provisioner.fail(err)
		return
	}
	startEnvelope := start.Envelope
	if len(start.Payload) != 0 {
		startEnvelope.Payload = append(json.RawMessage(nil), start.Payload...)
	}
	startDigest, err := contractsv1.RuntimeControlEnvelopeDigest(startEnvelope)
	if err != nil {
		provisioner.fail(err)
		return
	}
	activePayload, _ := json.Marshal(struct {
		ActiveTurn contractsv1.RuntimeControlActiveTurn `json:"active_turn"`
	}{ActiveTurn: contractsv1.RuntimeControlActiveTurn{
		WorkerRunID:          start.Envelope.WorkerRunID,
		AgentSessionID:       start.Envelope.AgentSessionID,
		AgentTurnID:          start.Envelope.AgentTurnID,
		StartCommandSequence: start.Envelope.Sequence,
		StartMessageID:       start.Envelope.MessageID,
		StartIdempotencyKey:  start.Envelope.IdempotencyKey,
		StartEnvelopeDigest:  startDigest,
		Phase:                contractsv1.RuntimeControlTurnEffectActive,
	}})
	if err := client.PublishTurnUpdate(runtimeCtx, workerapp.WorkerControlEvent{
		Envelope: contractsv1.RuntimeControlEnvelope{
			MessageID:         "active-effect-" + agentTurnID,
			WorkerSessionID:   start.Envelope.WorkerSessionID,
			WorkerRunID:       start.Envelope.WorkerRunID,
			RuntimeInstanceID: start.Envelope.RuntimeInstanceID,
			RuntimeGeneration: start.Envelope.RuntimeGeneration,
			AgentSessionID:    start.Envelope.AgentSessionID,
			AgentTurnID:       start.Envelope.AgentTurnID,
			ChannelEpoch:      start.Envelope.ChannelEpoch,
			IdempotencyKey:    "active-effect-" + agentTurnID,
			MessageType:       "agent_turn.effect",
			Payload:           activePayload,
		},
	}, false); err != nil {
		provisioner.fail(err)
		return
	}
	close(provisioner.turnActive)

	select {
	case <-provisioner.exitFirstRuntime:
		cancelRuntime()
		err := <-clientResult
		if err != nil && !errors.Is(err, context.Canceled) {
			provisioner.fail(err)
			return
		}
		close(provisioner.firstRuntimeExited)
	case err := <-clientResult:
		provisioner.fail(err)
	case <-provisioner.ctx.Done():
	}
}

func (provisioner *exitedRuntimeProvisioner) fail(err error) {
	if err == nil {
		err = errors.New("fake Runtime stopped unexpectedly")
	}
	select {
	case provisioner.failures <- err:
	default:
	}
}

type runtimeRunnerTaskState struct {
	TaskStatus        string
	WorkerSessionID   string
	WorkerRunID       string
	AgentTurnID       string
	AgentTurnStatus   string
	RuntimeInstanceID string
	RuntimeStatus     string
	RuntimeGeneration uint64
}

func createRuntimeRunnerTask(
	t *testing.T,
	handler http.Handler,
	pool *pgxpool.Pool,
	idempotencyKey string,
	title string,
) string {
	t.Helper()
	body := map[string]any{
		"schema_version":  "gateway.direct_task_input.v1",
		"idempotency_key": idempotencyKey,
		"project_id":      defaultProjectID, "workspace_id": defaultWorkspaceID,
		"workspace_revision_id": defaultWorkspaceRevisionID,
		"title":                 title, "goal": title,
		"target":              map[string]string{"kind": "workspace", "ref": defaultWorkspaceRevisionID},
		"constraints":         []string{"Keep Runtime identities server-owned."},
		"acceptance_criteria": []string{"The Task receives one WorkerSession."},
		"agent_profile_id":    "deepseek-codex-openai-v1",
		"acceptance_mode":     "manual",
		"user_visible_types":  []string{"report"},
	}
	encoded, _ := json.Marshal(body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(encoded)),
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("CreateTask status=%d body=%s", response.Code, response.Body.String())
	}
	var taskID string
	if err := pool.QueryRow(
		context.Background(),
		`SELECT resource_id FROM idempotency_results
WHERE operation='create_task' AND idempotency_key=$1`,
		idempotencyKey,
	).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	return taskID
}

func waitRuntimeRunnerTaskState(
	t *testing.T,
	pool *pgxpool.Pool,
	taskID string,
	ready func(runtimeRunnerTaskState) bool,
) runtimeRunnerTaskState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state := readRuntimeRunnerTaskState(t, pool, taskID)
		if ready(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("Task %s state did not converge: %+v", taskID, state)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func readRuntimeRunnerTaskState(
	t *testing.T,
	pool *pgxpool.Pool,
	taskID string,
) runtimeRunnerTaskState {
	t.Helper()
	var state runtimeRunnerTaskState
	err := pool.QueryRow(context.Background(), `
SELECT task.status,
       COALESCE(task.worker_session_id,''),
       COALESCE(task.current_worker_run_id,''),
       COALESCE(turn.agent_turn_id,''),
       COALESCE(turn.status,''),
       COALESCE(runtime.runtime_instance_id,''),
       COALESCE(runtime.status,''),
       COALESCE(runtime.generation,0)
FROM tasks task
LEFT JOIN worker_runs run
  ON run.worker_run_id=task.current_worker_run_id
LEFT JOIN agent_turns turn
  ON turn.worker_run_id=run.worker_run_id
LEFT JOIN runtime_instances runtime
  ON runtime.worker_run_id=run.worker_run_id
WHERE task.task_id=$1`,
		taskID,
	).Scan(
		&state.TaskStatus,
		&state.WorkerSessionID,
		&state.WorkerRunID,
		&state.AgentTurnID,
		&state.AgentTurnStatus,
		&state.RuntimeInstanceID,
		&state.RuntimeStatus,
		&state.RuntimeGeneration,
	)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func reserveRuntimeRunnerAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func integrationNetworkProfile(t *testing.T) config.RuntimeNetworkProfile {
	t.Helper()
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func postAssistantCommand(
	t *testing.T,
	handler http.Handler,
	path string,
	body map[string]any,
) gatewayAssistantResult {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("Assistant command %s status=%d body=%s", path, response.Code, response.Body.String())
	}
	var result gatewayAssistantResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type gatewayAssistantResult struct {
	ConversationID       string `json:"conversation_id"`
	ConversationRevision uint64 `json:"conversation_revision"`
	TurnID               string `json:"turn_id"`
	AssistantRunID       string `json:"assistant_run_id"`
	TaskID               string `json:"task_id"`
	TaskRevision         uint64 `json:"task_revision"`
	Status               string `json:"status"`
}

type assistantProjectionData struct {
	Revision     uint64 `json:"revision"`
	WorkspaceID  string `json:"workspace_id"`
	AssistantRun *struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		Retryable bool   `json:"retryable"`
	} `json:"assistant_run"`
	Readiness *struct {
		AssessmentID string `json:"assessment_id"`
		Revision     uint64 `json:"revision"`
		Status       string `json:"status"`
	} `json:"readiness"`
	TaskDraft *struct {
		TaskDraftID string `json:"task_draft_id"`
		Revision    uint64 `json:"revision"`
		Status      string `json:"status"`
		TaskSpec    struct {
			WorkspaceResolutionRef contractsv1.WorkspaceResolutionRef `json:"workspace_resolution_ref"`
			CreationIntentRef      contractsv1.CreationIntentRef      `json:"creation_intent_ref"`
		} `json:"task_spec"`
	} `json:"task_draft"`
}

func waitAssistantProjection(
	t *testing.T,
	handler http.Handler,
	conversationID string,
	ready func(assistantProjectionData) bool,
) assistantProjectionData {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/views/assistant_conversation/"+conversationID, nil,
		))
		if response.Code == http.StatusOK {
			var snapshot struct {
				Data assistantProjectionData `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err == nil && ready(snapshot.Data) {
				return snapshot.Data
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Assistant projection did not reach expected state: status=%d", response.Code)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func assistantClarificationDecision(now time.Time) contractsv1.AssistantDecision {
	dimensions := make([]contractsv1.ReadinessDimensionState, 0, 7)
	for _, dimension := range []string{
		"goal", "deliverable_acceptance", "scope_target", "constraints",
		"context_access", "risk_approval", "execution_policy",
	} {
		status := "known"
		if dimension == "goal" {
			status = "blocking_unknown"
		}
		dimensions = append(dimensions, contractsv1.ReadinessDimensionState{
			Dimension: dimension, Status: status, Summary: "composition test evidence",
			EvidenceRefs: []string{},
		})
	}
	return contractsv1.AssistantDecision{
		SchemaVersion: contractsv1.AssistantDecisionSchemaVersion,
		DecisionType:  "clarification", UserVisibleContent: "Please clarify the outcome.",
		ClarificationQuestion: "Please clarify the outcome.",
		Readiness: contractsv1.AssistantReadinessAssessment{
			SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion,
			AssessmentID:  "model-assessment-1", Revision: 1,
			PolicyRevision: config.DefaultAssistantReadinessPolicy,
			Dimensions:     dimensions, BlockingUnknowns: []string{"goal"},
			Assumptions: []string{}, ClarificationQuestions: 1,
			Status: "clarifying", EvaluatedAt: now,
		},
	}
}

func assistantReadyDecision(now time.Time) contractsv1.AssistantDecision {
	dimensions := make([]contractsv1.ReadinessDimensionState, 0, 7)
	for _, dimension := range []string{
		"goal", "deliverable_acceptance", "scope_target", "constraints",
		"context_access", "risk_approval", "execution_policy",
	} {
		dimensions = append(dimensions, contractsv1.ReadinessDimensionState{
			Dimension: dimension, Status: "known", Summary: "confirmed by the scripted acceptance",
			EvidenceRefs: []string{},
		})
	}
	return contractsv1.AssistantDecision{
		SchemaVersion: contractsv1.AssistantDecisionSchemaVersion,
		DecisionType:  "ready_draft", UserVisibleContent: "The exact Task draft is ready.",
		Readiness: contractsv1.AssistantReadinessAssessment{
			SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion,
			AssessmentID:  "model-assessment-ready", Revision: 1,
			PolicyRevision: config.DefaultAssistantReadinessPolicy,
			Dimensions:     dimensions, BlockingUnknowns: []string{}, Assumptions: []string{},
			Status: "ready_confirmed", EvaluatedAt: now,
		},
		TaskDraft: &contractsv1.AssistantTaskDraft{
			SchemaVersion: contractsv1.TaskDraftSchemaVersion,
			TaskDraftID:   "model-draft", Revision: 1, InitiatorUserID: "model-user",
			ConversationID: "model-conversation", WorkspaceID: "model-workspace",
			WorkspaceRevisionID:   "model-workspace-revision",
			ReadinessAssessmentID: "model-assessment-ready",
			TaskSpecDigest:        "sha256:" + strings.Repeat("a", 64),
			Title:                 "Repair counter", Goal: "Repair the counter and prove it with tests.",
			Target:             json.RawMessage(`{"kind":"workspace","ref":"workspace-revision-local-v1"}`),
			Constraints:        []string{"Do not publish remote changes."},
			AcceptanceCriteria: []string{"All repository tests pass."},
			Assumptions:        []string{}, TaskSpec: json.RawMessage(`{"schema_version":"task.spec.v1"}`),
			CreatedAt: now,
		},
	}
}
