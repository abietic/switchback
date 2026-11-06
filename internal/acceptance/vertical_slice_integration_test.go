//go:build integration

package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	frontierhttp "github.com/abietic/switchback/internal/frontier/http"
	frontiersse "github.com/abietic/switchback/internal/frontier/sse"
	gatewaypostgres "github.com/abietic/switchback/internal/gateway/adapter/postgres"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
	"github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"github.com/abietic/switchback/internal/testsupport/workspacefixture"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestFakeAgentVerticalSliceDeliversAcceptsAndContinuesExactSession(t *testing.T) {
	exerciseFakeAgentVerticalSlice(t, false)
}

func TestBackendRestartKeepsDeliveryAndProjectionWithoutDuplicateExecution(t *testing.T) {
	exerciseFakeAgentVerticalSlice(t, true)
}

func exerciseFakeAgentVerticalSlice(t *testing.T, restart bool) {
	ctx := context.Background()
	now := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
	pool := postgrestest.Open(t)
	runtimecapacity.Initialize(t, pool)
	ids := &sequentialIDs{}
	fixture, err := workspacefixture.Load(filepath.Join("..", ".."), filepath.Join("..", "..", "test", "fixtures", "m1", "workspace-revision.json"))
	if err != nil {
		t.Fatal(err)
	}
	commands, err := gatewaypostgres.NewCommandService(gatewaypostgres.CommandServiceOptions{
		Pool: pool, IDs: ids, Now: func() time.Time { return now },
		NetworkProfile: trustedNetworkProfile(t),
		Workspace: taskapp.WorkspaceRevisionEvidence{
			TenantID: fixture.TenantID, WorkspaceID: fixture.WorkspaceID,
			WorkspaceRevisionID: fixture.WorkspaceRevisionID, ResolutionID: "resolution-local",
			ResolutionRevision: 1, MembershipRevision: 1, Readable: true,
		},
		ProjectID: fixture.ProjectID, Profiles: config.ProductionProfileRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := gatewayhttp.NewHandlerWithOptions(gatewayhttp.HandlerOptions{
		Actor:    gatewayhttp.FixedActor{TenantID: fixture.TenantID, UserID: fixture.ActorUserID},
		Commands: commands,
	})
	if err != nil {
		t.Fatal(err)
	}

	createInput := map[string]any{
		"schema_version": "gateway.direct_task_input.v1", "idempotency_key": "e2e-create-task",
		"project_id": fixture.ProjectID, "workspace_id": fixture.WorkspaceID,
		"workspace_revision_id": fixture.WorkspaceRevisionID, "title": "Repair counter",
		"goal":                "Repair the counter and prove it with tests.",
		"target":              map[string]any{"kind": "workspace", "ref": fixture.WorkspaceRevisionID},
		"constraints":         []string{"Do not publish remote changes."},
		"acceptance_criteria": []string{"All repository tests pass."},
		"agent_profile_id":    config.DeepSeekCodexOpenAIV1, "acceptance_mode": "manual",
		"user_visible_types": []string{"report", "patch"},
	}
	create := postJSON(t, gateway, "/api/v1/tasks", createInput)
	if create.Code != http.StatusAccepted {
		t.Fatalf("CreateTask status=%d body=%s", create.Code, create.Body.String())
	}
	created := decodeCommandResult(t, create)
	createRetry := postJSON(t, gateway, "/api/v1/tasks", createInput)
	if createRetry.Code != http.StatusAccepted || decodeCommandResult(t, createRetry) != created {
		t.Fatalf("CreateTask retry did not return original result")
	}
	taskID := strings.TrimPrefix(created.ViewRef, "/api/v1/views/task_summary/")
	if taskID == created.ViewRef || taskID == "" {
		t.Fatalf("CreateTask view_ref=%q", created.ViewRef)
	}
	var allocationRequestID string
	if err := pool.QueryRow(ctx, `SELECT allocation_request_id FROM session_allocation_requests WHERE owner_id=$1`, taskID).Scan(&allocationRequestID); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute)
	allocated, err := (workerapp.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: ids, Now: func() time.Time { return now },
	}).Execute(ctx, workerapp.AllocateWorkerSessionCommand{
		TenantID: fixture.TenantID, UserID: fixture.ActorUserID, AllocationRequestID: allocationRequestID,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := taskapp.SessionDispatchService{
		Store: taskpostgres.NewStore(pool), IDs: ids, Now: func() time.Time { return now },
	}
	enqueued, err := dispatch.EnqueueTask(ctx, taskapp.EnqueueTaskCommand{
		TenantID: fixture.TenantID, UserID: fixture.ActorUserID, TaskID: taskID,
		WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
		ExpectedSessionRevision: 1, IdempotencyKey: "e2e-enqueue-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	permit, err := dispatch.IssueTaskPermit(ctx, taskapp.IssueTaskPermitCommand{
		TenantID: fixture.TenantID, UserID: fixture.ActorUserID,
		WorkerSessionID: allocated.WorkerSessionID, RuntimeGeneration: 1,
		IdempotencyKey: "e2e-dispatch-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if permit.ExpectedSessionRevision != 2 || permit.RuntimeGeneration != 1 {
		t.Fatalf("first dispatch did not establish generation 1: %+v", permit)
	}
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	network := trustedNetworkProfile(t)
	now = now.Add(time.Minute)
	started, err := (workerapp.StartWorkerRunService{
		Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: func() time.Time { return now },
	}).Execute(ctx, workerapp.StartWorkerRunCommand{
		TenantID: fixture.TenantID, UserID: fixture.ActorUserID,
		WorkerSessionID: allocated.WorkerSessionID, TaskID: taskID, PermitID: permit.PermitID,
		ExpectedSessionRevision: permit.ExpectedSessionRevision, RuntimeGeneration: 1,
		TriggerType: "task_initial", StartSnapshot: json.RawMessage(`{"working_copy":"fixture-baseline"}`),
		AgentProfileID: profile.ID, AgentProfileRevision: "1", AgentProfileDigest: profile.Digest,
		AgentTurnContent:   json.RawMessage(`{"text":"Repair counter"}`),
		TurnIdempotencyKey: "e2e-turn-1", RuntimeImageRef: "sha256:" + strings.Repeat("a", 64),
		ResourceProfileID: config.LocalStandardV1, NetworkPolicyID: network.ID,
		NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest,
		NetworkEnforcementStatus: config.NetworkEnforcementActive, ControlChannelEpoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	residency := workerapp.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time { return now },
	}
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "fake-container-1", 1)
	active, err := residency.Activate(ctx, workerapp.ActivateResidentRuntimeCommand{
		TenantID: fixture.TenantID, WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "fake-container-1",
		RuntimeGeneration: 1, ControlChannelEpoch: 1,
		ExpectedSessionRevision: started.SessionRevision, ExpectedRunRevision: 1,
		ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	rawTrace := executeFakeAgent(t, started)
	now = now.Add(time.Minute)
	completed, err := residency.CompleteStage(ctx, workerapp.CompleteResidentStageCommand{
		TenantID: fixture.TenantID, WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1,
		ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision,
		ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision,
		SessionTarget: workerdomain.SessionInteractive, RunTarget: workerdomain.RunCompleted,
		TurnTarget: workerdomain.TurnCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}

	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: objects, IDs: ids, Now: func() time.Time { return now },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission:         contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "single-user-local-v1"},
			ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1",
		},
	}).Execute(ctx, artifactapp.FinalizeTurnEvidenceCommand{
		Identity: artifactdomain.TurnIdentity{
			TenantID: fixture.TenantID, UserID: fixture.ActorUserID, WorkspaceID: fixture.WorkspaceID,
			TaskID: contractsv1.TaskID(taskID), WorkerSessionID: contractsv1.WorkerSessionID(started.WorkerSessionID),
			WorkerRunID: contractsv1.WorkerRunID(started.WorkerRunID), AgentSessionID: contractsv1.AgentSessionID(started.AgentSessionID),
			AgentTurnID: contractsv1.AgentTurnID(started.AgentTurnID), RuntimeInstanceID: contractsv1.RuntimeInstanceID(started.RuntimeInstanceID),
			RuntimeGeneration: 1, TraceStreamID: "trace-" + started.AgentTurnID, LangfuseTraceID: "langfuse-" + started.AgentTurnID,
			RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{
				ID: network.ID, Revision: network.Revision, Digest: network.Digest,
				EnforcementStatus: config.NetworkEnforcementActive,
			},
		},
		ServerRecordedAt: now, WorkerRecordedAt: now, RawACPJSONL: rawTrace,
		Patch:               []byte("diff --git a/counter/counter.go b/counter/counter.go\n"),
		SourceCheckpointRef: contractsv1.CheckpointRef{CheckpointID: "fixture-baseline", Digest: "sha256:" + strings.Repeat("c", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := (artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool), IDs: ids,
		Now: func() time.Time { return now },
	}).Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: completed.SessionRevision,
		ExpectedRunRevision: completed.RunRevision, ExpectedTurnRevision: completed.TurnRevision,
		ExpectedRuntimeRevision: completed.RuntimeRevision, IdempotencyKey: "e2e-publish-turn-1",
	})
	if err != nil || len(published.ArtifactIDs) != 2 {
		t.Fatalf("publish evidence=%+v error=%v", published, err)
	}

	now = now.Add(time.Minute)
	delivered, err := (taskapp.TaskDeliveryService{
		Store: taskpostgres.NewDeliveryStore(pool), IDs: ids, Now: func() time.Time { return now },
	}).RecordStageResult(ctx, taskapp.RecordStageResultCommand{
		Actor:  taskapp.Actor{TenantID: fixture.TenantID, UserID: fixture.ActorUserID},
		TaskID: taskID, WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID,
		ExpectedTaskRevision: enqueued.TaskRevision + 1, ArtifactRefs: finalized.ArtifactRefs,
		IdempotencyKey: "e2e-deliver-stage",
	})
	if err != nil || delivered.Status != "delivered" {
		t.Fatalf("deliver Task=%+v error=%v", delivered, err)
	}
	projectVerticalViews(t, pool, now, taskID, started.WorkerSessionID, finalized.ArtifactRefs, delivered.Revision)
	frontierViews, err := projectionapp.NewViewContinuityReader(
		projectionpostgres.NewStore(pool),
		projectionapp.ViewContinuityReaderOptions{Now: func() time.Time { return now }},
	)
	if err != nil {
		t.Fatal(err)
	}
	frontier, err := frontierhttp.NewHandler(frontierhttp.Options{
		Viewer: projectionapp.Viewer{TenantID: fixture.TenantID, UserID: fixture.ActorUserID},
		Views:  frontierViews,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshotRequest := httptest.NewRequest(http.MethodGet, "/api/v1/views/worker_detail/"+started.WorkerSessionID, nil)
	snapshotResponse := httptest.NewRecorder()
	frontier.ServeHTTP(snapshotResponse, snapshotRequest)
	if snapshotResponse.Code != http.StatusOK || !strings.Contains(snapshotResponse.Body.String(), published.ArtifactIDs[0]) || !strings.Contains(snapshotResponse.Body.String(), `"status":"delivered"`) {
		t.Fatalf("WorkerDetail snapshot status=%d body=%s", snapshotResponse.Code, snapshotResponse.Body.String())
	}

	now = now.Add(time.Minute)
	accept := postJSON(t, gateway, "/api/v1/tasks/"+taskID+"/accept", map[string]any{
		"schema_version": "gateway.task_command.v1", "idempotency_key": "e2e-accept-task",
		"expected_task_revision": delivered.Revision,
	})
	if accept.Code != http.StatusAccepted {
		t.Fatalf("accept status=%d body=%s", accept.Code, accept.Body.String())
	}
	accepted := decodeCommandResult(t, accept)
	acceptRetry := postJSON(t, gateway, "/api/v1/tasks/"+taskID+"/accept", map[string]any{
		"schema_version": "gateway.task_command.v1", "idempotency_key": "e2e-accept-task",
		"expected_task_revision": delivered.Revision,
	})
	if acceptRetry.Code != http.StatusAccepted || decodeCommandResult(t, acceptRetry) != accepted {
		t.Fatalf("accept retry did not return original result")
	}
	taskSnapshot, err := taskpostgres.NewStore(pool).ReadTask(ctx, taskID)
	if err != nil || taskSnapshot.Status != "completed" {
		t.Fatalf("accepted Task=%+v error=%v", taskSnapshot, err)
	}
	sessionSnapshot, err := (workerpostgres.SessionReader{Pool: pool}).ReadWorkerSession(ctx, started.WorkerSessionID)
	if err != nil || sessionSnapshot.BoundTaskID != "" || sessionSnapshot.Status != "interactive" {
		t.Fatalf("Session after accept=%+v error=%v", sessionSnapshot, err)
	}

	now = now.Add(time.Minute)
	followUp := postJSON(t, gateway, "/api/v1/worker-sessions/"+started.WorkerSessionID+"/follow-up", map[string]any{
		"schema_version": "gateway.worker_command.v1", "idempotency_key": "e2e-follow-up",
		"expected_session_revision": sessionSnapshot.Revision, "worker_run_id": started.WorkerRunID,
		"expected_run_revision": completed.RunRevision, "agent_turn_id": started.AgentTurnID,
		"expected_turn_revision": completed.TurnRevision, "content": "Explain the patch and add one edge-case test.",
	})
	if followUp.Code != http.StatusAccepted {
		t.Fatalf("follow-up status=%d body=%s", followUp.Code, followUp.Body.String())
	}
	followed := decodeCommandResult(t, followUp)
	followUpRetry := postJSON(t, gateway, "/api/v1/worker-sessions/"+started.WorkerSessionID+"/follow-up", map[string]any{
		"schema_version": "gateway.worker_command.v1", "idempotency_key": "e2e-follow-up",
		"expected_session_revision": sessionSnapshot.Revision, "worker_run_id": started.WorkerRunID,
		"expected_run_revision": completed.RunRevision, "agent_turn_id": started.AgentTurnID,
		"expected_turn_revision": completed.TurnRevision, "content": "Explain the patch and add one edge-case test.",
	})
	if followUpRetry.Code != http.StatusAccepted || decodeCommandResult(t, followUpRetry) != followed {
		t.Fatalf("follow-up retry did not return original result")
	}
	var runs, runtimes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_runs WHERE worker_session_id=$1`, started.WorkerSessionID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_instances WHERE worker_session_id=$1`, started.WorkerSessionID).Scan(&runtimes); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || runtimes != 1 {
		t.Fatalf("exact Session follow-up runs=%d runtimes=%d", runs, runtimes)
	}
	if !restart {
		return
	}

	pool = postgrestest.Restart(t, pool)
	restartedTask, err := taskpostgres.NewStore(pool).ReadTask(ctx, taskID)
	if err != nil || restartedTask.Status != "completed" || restartedTask.Revision != taskSnapshot.Revision {
		t.Fatalf("Task after Backend restart=%+v error=%v", restartedTask, err)
	}
	restartedSession, err := (workerpostgres.SessionReader{Pool: pool}).ReadWorkerSession(ctx, started.WorkerSessionID)
	if err != nil || restartedSession.Status != "running" || restartedSession.RuntimeGeneration != 1 {
		t.Fatalf("Session after Backend restart=%+v error=%v", restartedSession, err)
	}
	var artifacts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artifact_refs WHERE worker_session_id=$1`, started.WorkerSessionID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if artifacts != 2 {
		t.Fatalf("Artifact count after Backend restart=%d", artifacts)
	}
	restartedProjection := projectionpostgres.NewStore(pool)
	restartedViews, err := projectionapp.NewViewContinuityReader(
		restartedProjection,
		projectionapp.ViewContinuityReaderOptions{
			Now: func() time.Time { return now }, ReplayLimit: 32,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	restartedSnapshot, err := restartedViews.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: fixture.TenantID, UserID: fixture.ActorUserID},
		ViewType: projectionapp.ViewWorkerDetail,
		ViewID:   started.WorkerSessionID,
	})
	if err != nil || restartedSnapshot.ViewCursor != 1 || restartedSnapshot.ProjectionVersion != 1 {
		t.Fatalf("WorkerDetail after Backend restart=%+v error=%v", restartedSnapshot, err)
	}
	sse, err := frontiersse.NewHandler(frontiersse.Options{
		Viewer: projectionapp.Viewer{TenantID: fixture.TenantID, UserID: fixture.ActorUserID},
		Views:  restartedViews,
	})
	if err != nil {
		t.Fatal(err)
	}
	sseMux := http.NewServeMux()
	sseMux.Handle("GET /api/v1/views/{view_type}/{view_id}/events", sse)
	replayRequest := httptest.NewRequest(http.MethodGet,
		"/api/v1/views/worker_detail/"+started.WorkerSessionID+"/events?once=1", nil)
	replayRequest.Header.Set("Last-Event-ID", "1:0")
	replayResponse := httptest.NewRecorder()
	sseMux.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusOK || !strings.Contains(replayResponse.Body.String(), "id: 1:1") ||
		!strings.Contains(replayResponse.Body.String(), "event: projection_delta") {
		t.Fatalf("SSE replay after Backend restart status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}

	restartedCommands, err := gatewaypostgres.NewCommandService(gatewaypostgres.CommandServiceOptions{
		Pool: pool, IDs: ids, Now: func() time.Time { return now },
		NetworkProfile: trustedNetworkProfile(t),
		Workspace: taskapp.WorkspaceRevisionEvidence{
			TenantID: fixture.TenantID, WorkspaceID: fixture.WorkspaceID,
			WorkspaceRevisionID: fixture.WorkspaceRevisionID, ResolutionID: "resolution-local",
			ResolutionRevision: 1, MembershipRevision: 1, Readable: true,
		},
		ProjectID: fixture.ProjectID, Profiles: config.ProductionProfileRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	restartedGateway, err := gatewayhttp.NewHandlerWithOptions(gatewayhttp.HandlerOptions{
		Actor:    gatewayhttp.FixedActor{TenantID: fixture.TenantID, UserID: fixture.ActorUserID},
		Commands: restartedCommands,
	})
	if err != nil {
		t.Fatal(err)
	}
	replayedFollowUp := postJSON(t, restartedGateway, "/api/v1/worker-sessions/"+started.WorkerSessionID+"/follow-up", map[string]any{
		"schema_version": "gateway.worker_command.v1", "idempotency_key": "e2e-follow-up",
		"expected_session_revision": sessionSnapshot.Revision, "worker_run_id": started.WorkerRunID,
		"expected_run_revision": completed.RunRevision, "agent_turn_id": started.AgentTurnID,
		"expected_turn_revision": completed.TurnRevision, "content": "Explain the patch and add one edge-case test.",
	})
	if replayedFollowUp.Code != http.StatusAccepted || decodeCommandResult(t, replayedFollowUp) != followed {
		t.Fatalf("follow-up replay after Backend restart status=%d body=%s", replayedFollowUp.Code, replayedFollowUp.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_runs WHERE worker_session_id=$1`, started.WorkerSessionID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("Backend restart duplicated execution: runs=%d", runs)
	}
}

func executeFakeAgent(t *testing.T, started workerapp.StartedWorkerRun) []byte {
	t.Helper()
	updates := make(chan workerapp.AgentUpdate, 2)
	updates <- workerapp.AgentUpdate{Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":"Implemented the counter repair."}`)}
	updates <- workerapp.AgentUpdate{Kind: "tool_call", Payload: json.RawMessage(`{"command":"go test ./...","status":"completed"}`)}
	close(updates)
	agent := &fake.AgentAdapter{
		Capabilities: workerapp.AgentCapabilities{ProtocolVersion: "1", Prompt: true, Cancel: true},
		SessionID:    started.AgentSessionID, Updates: updates,
	}
	if _, err := agent.Initialize(context.Background(), workerapp.AgentInitializeRequest{
		WorkerSessionID: started.WorkerSessionID, RuntimeID: started.RuntimeInstanceID,
		Generation: 1, ProtocolVersion: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.OpenSession(context.Background(), workerapp.AgentSessionRequest{
		WorkerSessionID: started.WorkerSessionID, AgentSessionID: started.AgentSessionID,
		WorkingCopyPath: "/workspace",
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := agent.Prompt(context.Background(), workerapp.AgentPromptRequest{
		AgentSessionID: started.AgentSessionID, AgentTurnID: started.AgentTurnID, Content: "Repair counter",
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for update := range stream {
		var frame map[string]any
		switch update.Kind {
		case "agent_message_chunk":
			var payload struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(update.Payload, &payload)
			frame = acpFrame(started.AgentSessionID, map[string]any{
				"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": payload.Text},
			})
		case "tool_call":
			var payload struct{ Command, Status string }
			_ = json.Unmarshal(update.Payload, &payload)
			frame = acpFrame(started.AgentSessionID, map[string]any{
				"sessionUpdate": "tool_call", "toolCallId": "fake-tool-1", "title": "Run tests",
				"rawInput": map[string]any{"command": payload.Command}, "status": payload.Status,
			})
		default:
			t.Fatalf("unexpected fake Agent update %q", update.Kind)
		}
		encodedFrame, _ := json.Marshal(frame)
		if err := encoder.Encode(map[string]any{"type": "acp.raw_frame", "direction": "agent_to_client", "frame": string(encodedFrame)}); err != nil {
			t.Fatal(err)
		}
	}
	return output.Bytes()
}

func acpFrame(agentSessionID string, update map[string]any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
		"sessionId": agentSessionID, "update": update,
	}}
}

func projectVerticalViews(t *testing.T, pool *pgxpool.Pool, now time.Time, taskID, sessionID string, artifacts []contractsv1.ArtifactRef, taskRevision uint64) {
	t.Helper()
	artifactData, _ := json.Marshal(artifacts)
	workerData, _ := json.Marshal(map[string]any{"status": "delivered", "task_id": taskID, "artifacts": artifacts})
	taskData, _ := json.Marshal(map[string]any{"status": "delivered", "worker_session_id": sessionID})
	updates := []projectionapp.ViewUpdate{
		verticalUpdate(projectionapp.ViewTaskSummary, taskID, "delta-task-delivered", taskData),
		verticalUpdate(projectionapp.ViewWorkerDetail, sessionID, "delta-worker-delivered", workerData),
	}
	updates[0].AvailableActions = []projectionapp.ActionRef{{Action: "accept", TargetID: taskID, ExpectedRevision: taskRevision}}
	for index, artifact := range artifacts {
		data, _ := json.Marshal(map[string]any{"artifact": artifact, "all_artifacts": json.RawMessage(artifactData)})
		update := verticalUpdate(projectionapp.ViewArtifactDetail, artifact.ArtifactID, fmt.Sprintf("delta-artifact-%d", index+1), data)
		update.EventType = "view.artifact_ready.v1"
		updates = append(updates, update)
	}
	if _, err := (projectionapp.Projector{Store: projectionpostgres.NewStore(pool)}).Execute(context.Background(), projectionapp.ProjectionEvent{
		SourceEventID: "e2e-projection-delivered", SourceStreamID: "e2e-vertical-stream",
		SourceSequence: 1, ProjectedAt: now, Updates: updates,
	}); err != nil {
		t.Fatal(err)
	}
}

func verticalUpdate(viewType, viewID, deltaID string, data json.RawMessage) projectionapp.ViewUpdate {
	return projectionapp.ViewUpdate{
		DeltaID: deltaID, TenantID: "tenant-local", UserID: "user-local", ViewType: viewType, ViewID: viewID,
		QueryFingerprint: "single-user-local-v1", Data: data,
		AvailableActions: []projectionapp.ActionRef{}, RelatedRefs: []projectionapp.RelatedRef{},
		EventType: "view.entity_upserted.v1", Critical: true,
		SourceRefs:   []projectionapp.SourceRef{{Type: "integration_event", ID: "e2e-projection-delivered"}},
		DeltaPayload: data,
	}
}

func postJSON(t *testing.T, handler http.Handler, target string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeCommandResult(t *testing.T, response *httptest.ResponseRecorder) gatewayhttp.CommandResult {
	t.Helper()
	var result gatewayhttp.CommandResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

type sequentialIDs struct {
	mu   sync.Mutex
	next uint64
}

func (ids *sequentialIDs) NewID(kind string) (string, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	return fmt.Sprintf("%s-%03d", kind, ids.next), nil
}

func trustedNetworkProfile(t *testing.T) config.RuntimeNetworkProfile {
	t.Helper()
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}
