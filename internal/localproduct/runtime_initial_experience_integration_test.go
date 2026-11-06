//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	gatewaypostgres "github.com/abietic/switchback/internal/gateway/adapter/postgres"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/platform/telemetry"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// The HTTP/PG/settlement/Artifact path is production. Engine facts and ACP are
// synthetic; this is not provider, browser, retained telemetry or Linux proof.
func TestInitialExperienceFormalReportAndSameTask(t *testing.T) {
	inventory := runtimecapacity.NewFileInventory(t)
	remote := newInitialExperienceStorage(t)
	marker, err := randomIDs{}.NewID("C2B-RAW-ACP-REPORT")
	supplementMust(t, err)
	var prepared *countedInitialPreparation
	var createdCheck *countedFixtureCreatedLayerCheck
	f := newSupplementFixtureWithOptions(t, supplementFixtureOptions{
		productionInitial: true,
		configuration: func(c *Config) {
			c.LangfuseBaseURL, c.LangfuseBrowserBaseURL = remote.server.URL, remote.server.URL
			c.LangfuseProjectID, c.GrafanaBrowserBaseURL = "initial-experience", "http://localhost:3001"
		},
	}, func(f *supplementFixture) {
		installSyntheticInitialCapacitySource(f, inventory)
		f.manualInitial = true
		createdCheck = &countedFixtureCreatedLayerCheck{delegate: f.app.runtime.starts.CreatedLayerChecker}
		f.app.runtime.starts.CreatedLayerChecker = createdCheck
		f.runtime.rawResponse = func(contractsv1.RuntimeControlEnvelope) string {
			return "# Initial experience report\n\n" + marker + "\n"
		}
		f.publishInitial = func(r workerapp.RuntimeProvisionRequest) {
			inventory.Observe(r.WorkerSessionID, r.RuntimeInstanceID, "fixture-"+r.RuntimeInstanceID, r.Generation)
		}
		prepared = &countedInitialPreparation{RuntimeEnvironmentPreparer: f.app.runtime.starts.Environment}
		f.app.runtime.starts.Environment = prepared
	})
	source, ok := f.app.initialPolicy.(syntheticInitialCapacitySource)
	runnerSource, runnerOK := f.app.runtime.initialPolicy.(syntheticInitialCapacitySource)
	if !ok || !runnerOK || source.store != runnerSource.store || source.reader != runnerSource.reader || f.startPolicy != nil {
		t.Fatal("configured common source was replaced or initialized twice")
	}
	if !errors.Is(f.app.runtime.startPolicy.checkRuntimeStart(f.ctx), errRuntimeStartCapacityUnavailable) {
		t.Fatal("generic execution policy was enabled")
	}
	beforeSample := initialExperienceReadFile(t, filepath.Join(inventory.Directory, "capacity.json"))
	var createReceipt []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(recorder, r)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("create HTTP %d: %s", recorder.Code, recorder.Body.String())
		}
		createReceipt = append([]byte(nil), recorder.Body.Bytes()...)
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	})
	taskID := createRuntimeRunnerTask(t, handler, f.pool, "initial-experience", "Produce a formal report")
	t.Logf("create202 receipt=%s", createReceipt)
	turn, callback := f.next()
	e := turn.start.Envelope
	createdCheck.mu.Lock()
	checks := len(createdCheck.requests)
	if checks != 1 || len(createdCheck.identities) != 1 {
		createdCheck.mu.Unlock()
		t.Fatalf("created-layer checker calls=%d, want one actual delegate call", checks)
	}
	checkedRequest, checkedIdentity := createdCheck.requests[0], createdCheck.identities[0]
	createdCheck.mu.Unlock()
	if checkedRequest.RuntimeInstanceID != string(e.RuntimeInstanceID) || checkedRequest.WorkerSessionID != string(e.WorkerSessionID) || checkedRequest.WorkerRunID != string(e.WorkerRunID) || checkedRequest.Generation != uint64(e.RuntimeGeneration) || checkedIdentity != (workerapp.CreatedRuntimeIdentity{ContainerID: "fixture-" + string(e.RuntimeInstanceID), RuntimeInstanceID: string(e.RuntimeInstanceID), WorkerSessionID: string(e.WorkerSessionID), Generation: uint64(e.RuntimeGeneration)}) {
		t.Fatalf("created-layer check did not bind dispatched Runtime/Session/CID/generation: request=%+v identity=%+v", checkedRequest, checkedIdentity)
	}
	t.Logf("SYNTHETIC Engine created-layer delegate called once: identity=%+v", checkedIdentity)
	afterSample := initialExperienceReadFile(t, filepath.Join(inventory.Directory, "capacity.json"))
	var beforeEnvelope, afterEnvelope capacitysnapshot.Envelope
	supplementMust(t, json.Unmarshal(beforeSample, &beforeEnvelope))
	supplementMust(t, json.Unmarshal(afterSample, &afterEnvelope))
	var beforeFacts, afterFacts capacitysnapshot.Facts
	supplementMust(t, json.Unmarshal(beforeEnvelope.Payload, &beforeFacts))
	supplementMust(t, json.Unmarshal(afterEnvelope.Payload, &afterFacts))
	if len(beforeFacts.Containers) != 0 || len(afterFacts.Containers) != 1 || afterFacts.Containers[0].ContainerID != "fixture-"+string(e.RuntimeInstanceID) || afterFacts.Containers[0].SessionID != string(e.WorkerSessionID) || afterFacts.Containers[0].RuntimeID != string(e.RuntimeInstanceID) || afterFacts.Containers[0].Generation != uint64(e.RuntimeGeneration) || afterFacts.Sequence <= beforeFacts.Sequence {
		t.Fatal("signed samples did not move from pre-CID to actual CID")
	}
	if prepared.calls.Load() != 1 || f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE consumed_at IS NOT NULL AND worker_session_id=$1 AND run_id=$2 AND turn_id=$3 AND runtime_id=$4`, string(e.WorkerSessionID), string(e.WorkerRunID), string(e.AgentTurnID), string(e.RuntimeInstanceID)) != 1 || f.count(`SELECT count(*) FROM worker_runs`) != 1 || f.count(`SELECT count(*) FROM agent_turns WHERE deadline_at IS NOT NULL`) != 1 || f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 1 {
		t.Fatal("initial start duplicated Prepare/token/Run/Turn/deadline/Prompt")
	}
	t.Logf("signed synthetic samples: before_sha=%s after_sha=%s pre_CID_containers=%d actual_CID=%s sequences=%d/%d", runtimeSettlementBytesDigest(beforeSample), runtimeSettlementBytesDigest(afterSample), len(beforeFacts.Containers), afterFacts.Containers[0].ContainerID, beforeFacts.Sequence, afterFacts.Sequence)
	var deadline time.Time
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT deadline_at FROM agent_turns WHERE agent_turn_id=$1`, string(e.AgentTurnID)).Scan(&deadline))
	f.app.runtime.mu.Lock()
	live := f.app.runtime.runtimes[string(e.RuntimeInstanceID)]
	f.app.runtime.mu.Unlock()
	if live == nil {
		t.Fatal("initial Runtime not resident")
	}
	raw := initialExperienceReadFile(t, filepath.Join(live.storage.TraceSpool.BackendPath, "acp.raw.jsonl"))
	if !bytes.Contains(raw, []byte(marker)) || !bytes.Contains(raw, []byte("agent_to_client")) {
		t.Fatal("report response was not captured before terminal")
	}
	t.Logf("raw ACP captured before terminal=%s", raw)
	var terminalEvents []telemetry.Event
	observer := newWorkerTerminalObserver(f.pool, f.app.configuration.Profile.TenantID, f.app.configuration.Profile.ActorUserID, telemetry.NewMetrics(), func(event telemetry.Event) { terminalEvents = append(terminalEvents, event) }, time.Now)
	f.send(callback)
	f.finish(turn, callback)
	settlement := f.settled(turn)
	var checkpoint runtimeTurnSettlementCheckpoint
	supplementMust(t, json.Unmarshal(settlement.PhaseState, &checkpoint))
	if settlement.Phase != workerapp.TurnSettlementCompleted || checkpoint.EvidenceInput == nil || checkpoint.EvidenceInput.ReportArtifactID == "" {
		t.Fatal("missing canonical completed report checkpoint")
	}
	artifactID := checkpoint.EvidenceInput.ReportArtifactID
	// Stop and join the synthetic client/heartbeat, server and every supervisor
	// before final reads. No background owner can mutate the captured graph.
	f.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.ctx = ctx
	observer.sample(ctx)
	if len(terminalEvents) != 2 {
		t.Fatalf("committed telemetry terminals=%d", len(terminalEvents))
	}
	for _, event := range terminalEvents {
		if event.Scope.TenantID != f.app.configuration.Profile.TenantID || event.Scope.WorkspaceID != f.app.configuration.WorkspaceID || event.Scope.WorkerSessionID != string(e.WorkerSessionID) || event.Scope.WorkerRunID != string(e.WorkerRunID) || event.Code != telemetry.CodeOK || (event.Event == telemetry.EventTurnTerminal && event.Scope.AgentTurnID != string(e.AgentTurnID)) {
			t.Fatalf("terminal telemetry wrong scope: %+v", event)
		}
		encoded, _ := json.Marshal(event)
		t.Logf("production terminal telemetry=%s", encoded)
	}
	var checksum, artifactRun, artifactTurn, artifactTask string
	supplementMust(t, f.pool.QueryRow(ctx, `SELECT a.object_checksum,a.worker_run_id,a.agent_turn_id,r.task_id FROM artifact_refs a JOIN worker_runs r USING(worker_run_id) WHERE a.artifact_id=$1`, artifactID).Scan(&checksum, &artifactRun, &artifactTurn, &artifactTask))
	if artifactRun != string(e.WorkerRunID) || artifactTurn != string(e.AgentTurnID) || artifactTask != taskID {
		t.Fatal("formal Artifact->Run->Task provenance diverged")
	}
	taskPath := "/api/v1/views/task_summary/" + taskID
	workerPath := "/api/v1/views/worker_detail/" + string(e.WorkerSessionID)
	artifactPath := "/api/v1/views/artifact_detail/" + artifactID
	task := initialExperienceView(t, f, taskPath)
	worker := initialExperienceView(t, f, workerPath)
	artifact := initialExperienceView(t, f, artifactPath)
	if !bytes.Contains(task.Data, []byte(`"status":"delivered"`)) {
		t.Fatalf("Task not delivered: %s", task.Data)
	}
	initialExperienceRef(t, task.RelatedRefs, "worker_detail", string(e.WorkerSessionID), workerPath)
	initialExperienceRef(t, worker.RelatedRefs, "artifact_detail", artifactID, artifactPath)
	var detail struct {
		ArtifactID   string                             `json:"artifact_id"`
		Type         string                             `json:"type"`
		Availability string                             `json:"availability"`
		Checksum     string                             `json:"checksum"`
		Content      string                             `json:"content"`
		DownloadURL  string                             `json:"download_url"`
		Run          string                             `json:"worker_run_id"`
		Turn         string                             `json:"agent_turn_id"`
		Review       artifactapp.ArtifactReviewManifest `json:"review_manifest"`
	}
	supplementMust(t, json.Unmarshal(artifact.Data, &detail))
	if detail.ArtifactID != artifactID || detail.Type != "report" || detail.Availability != "online" || !detail.Review.Renderable || !detail.Review.DownloadAllowed || detail.Review.MediaType != "text/markdown" || detail.Run != artifactRun || detail.Turn != artifactTurn || detail.Checksum != checksum || !strings.Contains(detail.Content, marker) {
		t.Fatalf("formal preview/permission/identity mismatch: %s", artifact.Data)
	}
	initialExperienceAction(t, artifact, "artifact.download", artifactID)
	content := initialExperienceHTTP(t, f, http.MethodGet, detail.DownloadURL, nil, http.StatusOK)
	sum := sha256.Sum256(content)
	if "sha256:"+hex.EncodeToString(sum[:]) != checksum || string(content) != detail.Content || !bytes.Contains(content, []byte(marker)) {
		t.Fatal("formal download SHA or preview semantics differ")
	}
	t.Logf("formal report marker=%s artifact=%s checksum=%s bytes=%d preview=online/renderable/download_allowed", marker, artifactID, checksum, len(content))
	verifyInitialExperienceObservability(t, f, remote, e)
	acceptRevision := initialExperienceAction(t, task, "task.accept", taskID)
	preparedRuntime, ok := f.app.runtime.environment.get(string(e.RuntimeInstanceID))
	if !ok {
		t.Fatal("resident Runtime secrets unavailable before accept")
	}
	bootstrapBefore := initialExperienceReadFile(t, preparedRuntime.Secrets.Bootstrap.BackendPath)
	modelSecretBefore := initialExperienceReadFile(t, preparedRuntime.Secrets.ModelCredential.BackendPath)
	residentBefore := f.jsonRow(`SELECT jsonb_agg(to_jsonb(r) ORDER BY worker_session_id) FROM runtime_capacity_residents r`)
	graphBefore := initialExperienceGraph(t, f, taskID, artifactID)
	transitionsBefore := f.count(`SELECT count(*) FROM task_transitions`)
	outboxBefore := f.count(`SELECT count(*) FROM outbox_events`)
	stateBefore := initialExperienceState(f)
	acceptBody := func(key string, revision uint64) map[string]any {
		return map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": key, "expected_task_revision": revision, "message": "Accept formal report"}
	}
	// A separately assembled production Gateway fixes a foreign actor; it shares
	// only the real command store, not App/source construction or Runtime policy.
	foreignActors := []gatewayhttp.FixedActor{{TenantID: f.app.configuration.Profile.TenantID, UserID: "foreign-user"}, {TenantID: "foreign-tenant", UserID: f.app.configuration.Profile.ActorUserID}}
	foreignHandlers := make([]http.Handler, 0, len(foreignActors))
	for _, actor := range foreignActors {
		foreign := initialExperienceForeignHandler(t, f, actor)
		foreignHandlers = append(foreignHandlers, foreign)
		initialExperienceHTTPHandler(t, f.ctx, foreign, http.MethodPost, "/api/v1/tasks/"+taskID+"/accept", acceptBody("foreign-accept", acceptRevision), http.StatusNotFound)
	}
	initialExperienceStageReplay(t, f, checkpoint, foreignActors)
	// The exact new flow rejects stale authority before the legitimate accept.
	initialExperienceHTTP(t, f, http.MethodPost, "/api/v1/tasks/"+taskID+"/accept", acceptBody("stale-accept", acceptRevision-1), http.StatusConflict)
	if !bytes.Equal(stateBefore, initialExperienceState(f)) || f.count(`SELECT count(*) FROM task_transitions`) != transitionsBefore || f.count(`SELECT count(*) FROM outbox_events`) != outboxBefore || !bytes.Equal(graphBefore, initialExperienceGraph(t, f, taskID, artifactID)) {
		t.Fatal("stale accept mutated Task graph")
	}
	first := initialExperienceHTTP(t, f, http.MethodPost, "/api/v1/tasks/"+taskID+"/accept", acceptBody("accept-initial-report", acceptRevision), http.StatusAccepted)
	replay := initialExperienceHTTP(t, f, http.MethodPost, "/api/v1/tasks/"+taskID+"/accept", acceptBody("accept-initial-report", acceptRevision), http.StatusAccepted)
	if !bytes.Equal(first, replay) {
		t.Fatalf("same-key response-loss replay receipt differs: %s / %s", first, replay)
	}
	var receipt struct {
		CommandID string `json:"command_id"`
		Status    string `json:"status"`
		ViewRef   string `json:"view_ref"`
	}
	supplementMust(t, json.Unmarshal(first, &receipt))
	if receipt.CommandID == "" || receipt.Status != "accepted" || receipt.ViewRef != taskPath {
		t.Fatalf("invalid accept receipt %s", first)
	}
	if f.count(`SELECT count(*) FROM task_transitions WHERE task_id=$1 AND to_state='completed'`, taskID) != 1 || f.count(`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='TaskAccepted.v1'`, taskID) != 1 {
		t.Fatal("accept/replay duplicated transition or owner outbox")
	}
	// Public GETs retain the original references; the production dispatcher performs its
	// normal outbox projection rather than any fixture SQL view write.
	refreshedTask := initialExperienceView(t, f, taskPath)
	refreshedWorker := initialExperienceView(t, f, workerPath)
	refreshedArtifact := initialExperienceView(t, f, artifactPath)
	if !bytes.Contains(refreshedTask.Data, []byte(`"status":"completed"`)) || !bytes.Equal(artifact.Data, refreshedArtifact.Data) || !bytes.Contains(refreshedWorker.Data, []byte(artifactID)) || !bytes.Contains(refreshedWorker.Data, []byte(string(e.AgentTurnID))) || !bytes.Equal(graphBefore, initialExperienceGraph(t, f, taskID, artifactID)) {
		t.Fatal("public refresh changed original Task/Session/Run/Turn/Artifact/checksum")
	}
	if !bytes.Equal(residentBefore, f.jsonRow(`SELECT jsonb_agg(to_jsonb(r) ORDER BY worker_session_id) FROM runtime_capacity_residents r`)) || f.count(`SELECT count(*) FROM runtime_capacity_residents WHERE removal_sequence IS NULL`) != 1 {
		t.Fatal("accept released physical resident commitment")
	}
	var afterDeadline time.Time
	supplementMust(t, f.pool.QueryRow(ctx, `SELECT deadline_at FROM agent_turns WHERE agent_turn_id=$1`, string(e.AgentTurnID)).Scan(&afterDeadline))
	if !deadline.Equal(afterDeadline) {
		t.Fatal("accept changed original Turn deadline")
	}
	f.runtime.inventory.mu.Lock()
	provisions, removes := len(f.runtime.inventory.requests), len(f.runtime.inventory.removes)
	f.runtime.inventory.mu.Unlock()
	if provisions != 1 || removes != 0 {
		t.Fatalf("physical effects changed: provisions=%d removes=%d", provisions, removes)
	}
	if _, ok := f.app.runtime.environment.get(string(e.RuntimeInstanceID)); !ok {
		t.Fatal("accept revoked prepared Runtime ownership")
	}
	if !errors.Is(f.app.runtime.startPolicy.checkRuntimeStart(ctx), errRuntimeStartCapacityUnavailable) {
		t.Fatal("report acceptance enabled unsupported generic starts")
	}
	if !bytes.Equal(bootstrapBefore, initialExperienceReadFile(t, preparedRuntime.Secrets.Bootstrap.BackendPath)) || !bytes.Equal(modelSecretBefore, initialExperienceReadFile(t, preparedRuntime.Secrets.ModelCredential.BackendPath)) {
		t.Fatal("accept changed resident secret resources")
	}
	sessionRevision, runRevision, turnRevision := f.revisions(turn)
	initialExperienceHTTP(t, f, http.MethodPost, "/api/v1/worker-sessions/"+string(e.WorkerSessionID)+"/follow-up", map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": "unsupported-after-accept", "expected_session_revision": sessionRevision, "worker_run_id": e.WorkerRunID, "expected_run_revision": runRevision, "agent_turn_id": e.AgentTurnID, "expected_turn_revision": turnRevision, "content": "Do not derive successor authority from this report"}, http.StatusConflict)
	if len(f.runtime.starts) != 0 || f.count(`SELECT count(*) FROM worker_runs`) != 1 || f.count(`SELECT count(*) FROM agent_turns`) != 1 {
		t.Fatal("accept fabricated successor authority")
	}
	t.Logf("accept202+same-key replay=%s; refreshed identities=%s; resident commitment unchanged; all runtime owners joined", first, graphBefore)
	remote.logReceipts(t)
	// An idempotency key cannot grant a different actor access to this result.
	acceptedState := initialExperienceState(f)
	for _, foreign := range foreignHandlers {
		initialExperienceHTTPHandler(t, f.ctx, foreign, http.MethodPost, "/api/v1/tasks/"+taskID+"/accept", acceptBody("accept-initial-report", acceptRevision), http.StatusNotFound)
	}
	if !bytes.Equal(acceptedState, initialExperienceState(f)) || f.count(`SELECT count(*) FROM task_transitions`) != transitionsBefore+1 || f.count(`SELECT count(*) FROM outbox_events`) != outboxBefore+1 || !bytes.Equal(graphBefore, initialExperienceGraph(t, f, taskID, artifactID)) {
		t.Fatal("foreign replay mutated final graph/transition/outbox")
	}
	t.Log("foreign user and foreign tenant fresh/replay denied; Task stage replay retained original event; transitions/outbox once")
}

func initialExperienceReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	supplementMust(t, err)
	return b
}
func initialExperienceHTTP(t *testing.T, f *supplementFixture, method, path string, body any, want int) []byte {
	t.Helper()
	return initialExperienceHTTPHandler(t, f.ctx, f.app.Handler(), method, path, body, want)
}
func initialExperienceHTTPHandler(t *testing.T, ctx context.Context, handler http.Handler, method, path string, body any, want int) []byte {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		supplementMust(t, err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx))
	if w.Code != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
	}
	return append([]byte(nil), w.Body.Bytes()...)
}
func initialExperienceView(t *testing.T, f *supplementFixture, path string) projectionapp.ProjectionSnapshot {
	t.Helper()
	for pass := 0; pass < 4; pass++ {
		n, err := f.app.outbox.RunOnce(f.ctx)
		supplementMust(t, err)
		if n == 0 {
			break
		}
		if pass == 3 {
			t.Fatal("public projection outbox did not drain")
		}
	}
	var result projectionapp.ProjectionSnapshot
	supplementMust(t, json.Unmarshal(initialExperienceHTTP(t, f, http.MethodGet, path, nil, http.StatusOK), &result))
	return result
}
func initialExperienceAction(t *testing.T, v projectionapp.ProjectionSnapshot, action, target string) uint64 {
	t.Helper()
	for _, a := range v.AvailableActions {
		if a.Action == action && a.TargetID == target && a.ExpectedRevision > 0 {
			return a.ExpectedRevision
		}
	}
	t.Fatalf("missing exact %s action for %s", action, target)
	return 0
}
func initialExperienceRef(t *testing.T, refs []projectionapp.RelatedRef, kind, id, path string) {
	t.Helper()
	for _, r := range refs {
		if r.Type == kind && r.ID == id && r.Href == path {
			return
		}
	}
	t.Fatalf("missing related ref %s %s", kind, id)
}
func initialExperienceGraph(t *testing.T, f *supplementFixture, task, artifact string) []byte {
	t.Helper()
	return f.jsonRow(`SELECT jsonb_build_object('task',t.task_id,'session',r.worker_session_id,'run',r.worker_run_id,'turn',a.agent_turn_id,'agent',a.agent_session_id,'runtime',s.runtime_instance_id,'generation',s.runtime_generation,'artifact',x.artifact_id,'checksum',x.object_checksum) FROM tasks t JOIN worker_runs r ON r.worker_run_id=t.current_worker_run_id JOIN agent_turns a ON a.worker_run_id=r.worker_run_id JOIN worker_turn_settlements s ON s.agent_turn_id=a.agent_turn_id JOIN artifact_refs x ON x.worker_run_id=r.worker_run_id AND x.agent_turn_id=a.agent_turn_id WHERE t.task_id=$1 AND x.artifact_id=$2`, task, artifact)
}

func initialExperienceForeignHandler(t *testing.T, f *supplementFixture, actor gatewayhttp.FixedActor) http.Handler {
	t.Helper()
	c := f.app.configuration
	commands, err := gatewaypostgres.NewCommandService(gatewaypostgres.CommandServiceOptions{Pool: f.pool, IDs: randomIDs{}, Now: time.Now, ProjectID: c.ProjectID, Profiles: config.ProductionProfileRegistry(), NetworkProfile: c.NetworkProfile, Workspace: taskapp.WorkspaceRevisionEvidence{TenantID: c.Profile.TenantID, WorkspaceID: c.WorkspaceID, WorkspaceRevisionID: c.WorkspaceRevisionID, ResolutionID: "resolution-local", ResolutionRevision: 1, MembershipRevision: 1, Readable: true}})
	supplementMust(t, err)
	handler, err := gatewayhttp.NewHandlerWithOptions(gatewayhttp.HandlerOptions{Actor: actor, Commands: runtimeGuardedCommands{commands: commands, policy: unavailableRuntimeStartPolicy{}}, Assistant: commands})
	supplementMust(t, err)
	return handler
}

// Reuse the coordinator's exact frozen command and production delivery service.
// This exercises the other caller of the actor-bound replay read without
// fabricating a second stage, bypassing capacity, or creating a new Run.
func initialExperienceStageReplay(t *testing.T, f *supplementFixture, cp runtimeTurnSettlementCheckpoint, foreign []gatewayhttp.FixedActor) {
	t.Helper()
	if cp.TaskPlan == nil || cp.TaskPlan.Action != "record_stage_result" || cp.TaskResult == nil || cp.Evidence == nil {
		t.Fatal("missing original Task stage command")
	}
	before := initialExperienceState(f)
	command := taskapp.RecordStageResultCommand{Actor: taskapp.Actor{TenantID: cp.EvidenceInput.TenantID, UserID: cp.EvidenceInput.UserID}, TaskID: cp.TaskPlan.TaskID, WorkerSessionID: cp.EvidenceInput.WorkerSessionID, WorkerRunID: cp.EvidenceInput.WorkerRunID, ExpectedTaskRevision: cp.TaskPlan.ExpectedTaskRevision, ArtifactRefs: cp.Evidence.Finalized.ArtifactRefs, IdempotencyKey: cp.TaskPlan.IdempotencyKey}
	result, err := f.app.runtime.delivery.RecordStageResult(f.ctx, command)
	supplementMust(t, err)
	if result.TaskID != command.TaskID || result.Status != "delivered" || result.Revision != cp.TaskResult.Revision || result.EventID != cp.TaskResult.EventID {
		t.Fatal("authorized stage replay lost original result")
	}
	for _, actor := range foreign {
		command.Actor = taskapp.Actor{TenantID: actor.TenantID, UserID: actor.UserID}
		_, err = f.app.runtime.delivery.RecordStageResult(f.ctx, command)
		if !errors.Is(err, platformpostgres.ErrNotFound) {
			t.Fatalf("foreign stage replay error=%v", err)
		}
	}
	if !bytes.Equal(before, initialExperienceState(f)) {
		t.Fatal("stage replay changed Task/Session/Run/Turn/transition/outbox/resident rows")
	}
}

// All relevant canonical rows in this isolated one-Task schema; no Runtime
// owners remain active when it is called. JSONB plus ordered arrays is stable.
func initialExperienceState(f *supplementFixture) []byte {
	return f.jsonRow(`SELECT jsonb_build_object(
 'tasks',(SELECT jsonb_agg(to_jsonb(t) ORDER BY task_id) FROM tasks t),
 'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY worker_session_id) FROM worker_sessions s),
 'runs',(SELECT jsonb_agg(to_jsonb(r) ORDER BY worker_run_id) FROM worker_runs r),
 'turns',(SELECT jsonb_agg(to_jsonb(a) ORDER BY agent_turn_id) FROM agent_turns a),
 'transitions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY transition_id) FROM task_transitions t),
 'outbox',(SELECT jsonb_agg(to_jsonb(o) ORDER BY event_id) FROM outbox_events o),
 'residents',(SELECT jsonb_agg(to_jsonb(r) ORDER BY commitment_id) FROM runtime_capacity_residents r))`)
}

// These states are reached by initial dispatch, the control journal, settlement,
// and public acceptance. Clearing process maps grants no replay/restore rights.
func TestInitialExperienceLifecycleColdStartupHold(t *testing.T) {
	for _, phase := range []string{"consumed_running", "terminal_delivered", "accepted_completed", "failed_waiting", "interrupted_cancelled", "task_cancelled"} {
		t.Run(phase, func(t *testing.T) {
			inventory := runtimecapacity.NewFileInventory(t)
			var prepared *countedInitialPreparation
			f := newSupplementFixtureWithOptions(t, supplementFixtureOptions{
				productionInitial: true,
			}, func(f *supplementFixture) {
				installSyntheticInitialCapacitySource(f, inventory)
				f.manualInitial = true
				f.runtime.heartbeatStart = make(chan struct{})
				f.runtime.rawResponse = func(contractsv1.RuntimeControlEnvelope) string {
					return "# Durable lifecycle report\n\ninitial-cold-hold-report\n"
				}
				f.publishInitial = func(r workerapp.RuntimeProvisionRequest) {
					inventory.Observe(r.WorkerSessionID, r.RuntimeInstanceID, "fixture-"+r.RuntimeInstanceID, r.Generation)
				}
				prepared = &countedInitialPreparation{RuntimeEnvironmentPreparer: f.app.runtime.starts.Environment}
				f.app.runtime.starts.Environment = prepared
			})
			taskID := createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "lifecycle-cold-hold", "Produce a durable lifecycle report")
			turn, callback := f.next()
			e := turn.start.Envelope
			artifactID := ""
			if phase != "consumed_running" {
				f.send(callback)
				if phase == "failed_waiting" || phase == "interrupted_cancelled" {
					supplementMust(t, f.binder.Deactivate(callback.correlation))
					terminal := e
					terminal.Sequence = 0
					terminal.MessageID = "terminal-" + string(e.AgentTurnID)
					terminal.IdempotencyKey = terminal.MessageID
					terminal.MessageType = "agent_turn.terminal"
					terminal.Payload = json.RawMessage(`{"update_kind":"error","update":{"code":"synthetic_agent_error"}}`)
					if phase == "interrupted_cancelled" {
						terminal.Payload = json.RawMessage(`{"update_kind":"terminal","update":{"stop_reason":"cancelled"}}`)
					}
					supplementMust(t, turn.client.PublishTurnUpdate(turn.ctx, workerapp.WorkerControlEvent{Envelope: terminal}, true))
				} else {
					f.finish(turn, callback)
				}
				settlement := f.settled(turn)
				var cp runtimeTurnSettlementCheckpoint
				supplementMust(t, json.Unmarshal(settlement.PhaseState, &cp))
				if cp.EvidenceInput == nil || cp.EvidenceInput.ReportArtifactID == "" {
					t.Fatal("production settlement omitted report")
				}
				artifactID = cp.EvidenceInput.ReportArtifactID
			}
			f.stop()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f.ctx = ctx
			taskPath := "/api/v1/views/task_summary/" + taskID
			task := initialExperienceView(t, f, taskPath)
			if phase == "accepted_completed" {
				revision := initialExperienceAction(t, task, "task.accept", taskID)
				initialExperienceHTTP(t, f, http.MethodPost, "/api/v1/tasks/"+taskID+"/accept", map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "accept-cold-hold", "expected_task_revision": revision}, http.StatusAccepted)
				task = initialExperienceView(t, f, taskPath)
			}
			if phase == "task_cancelled" {
				revision := initialExperienceAction(t, task, "task.cancel", taskID)
				initialExperienceHTTP(t, f, http.MethodPost, "/api/v1/tasks/"+taskID+"/cancel", map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "cancel-cold-hold", "expected_task_revision": revision}, http.StatusAccepted)
				task = initialExperienceView(t, f, taskPath)
			}
			workerPath := "/api/v1/views/worker_detail/" + string(e.WorkerSessionID)
			worker := initialExperienceView(t, f, workerPath)
			var artifactBefore []byte
			var download string
			var graphBefore []byte
			if artifactID != "" {
				artifact := initialExperienceView(t, f, "/api/v1/views/artifact_detail/"+artifactID)
				var d struct {
					Content     string `json:"content"`
					Checksum    string `json:"checksum"`
					DownloadURL string `json:"download_url"`
				}
				supplementMust(t, json.Unmarshal(artifact.Data, &d))
				download = d.DownloadURL
				artifactBefore = initialExperienceHTTP(t, f, http.MethodGet, download, nil, http.StatusOK)
				sum := sha256.Sum256(artifactBefore)
				if d.Content != string(artifactBefore) || d.Checksum != "sha256:"+hex.EncodeToString(sum[:]) || !bytes.Contains(artifactBefore, []byte("initial-cold-hold-report")) {
					t.Fatal("report content/checksum mismatch")
				}
				graphBefore = initialExperienceGraph(t, f, taskID, artifactID)
				t.Logf("phase=%s original artifact=%s checksum=%s graph=%s", phase, artifactID, d.Checksum, graphBefore)
			}
			var runStatus, turnStatus, taskStatus string
			var released, activeRun, boundTask bool
			supplementMust(t, f.pool.QueryRow(ctx, `SELECT r.status,t.status,task.status,e.released_at IS NOT NULL,s.active_worker_run_id IS NOT NULL,s.bound_task_id IS NOT NULL FROM runtime_capacity_executions e JOIN worker_runs r ON r.worker_run_id=e.run_id JOIN agent_turns t ON t.agent_turn_id=e.turn_id JOIN tasks task ON task.task_id=r.task_id JOIN worker_sessions s ON s.worker_session_id=e.worker_session_id`).Scan(&runStatus, &turnStatus, &taskStatus, &released, &activeRun, &boundTask))
			wantTask, wantRun, wantTurn := "running", "running", "consumed"
			if phase != "consumed_running" {
				wantTask, wantRun, wantTurn = "delivered", "completed", "completed"
			}
			if phase == "accepted_completed" {
				wantTask = "completed"
			}
			if phase == "failed_waiting" {
				wantTask, wantRun, wantTurn = "waiting_user", "failed", "failed"
			}
			if phase == "interrupted_cancelled" {
				wantTask, wantRun, wantTurn = "waiting_user", "cancelled", "cancelled"
			}
			if phase == "task_cancelled" {
				wantTask = "cancelled"
			}
			if taskStatus != wantTask || runStatus != wantRun || turnStatus != wantTurn || released != (phase != "consumed_running") || activeRun != (phase == "consumed_running") || boundTask != (phase != "accepted_completed" && phase != "task_cancelled") {
				t.Fatalf("unexpected production phase task=%s run=%s turn=%s released=%t activeRun=%t boundTask=%t", taskStatus, runStatus, turnStatus, released, activeRun, boundTask)
			}
			r := f.app.runtime
			id := string(e.RuntimeInstanceID)
			original, ok := r.environment.get(id)
			if !ok {
				t.Fatal("initial environment absent")
			}
			r.mu.Lock()
			r.runtimes = make(map[string]*liveRuntime)
			r.pendingInitialActivations = nil
			r.mu.Unlock()
			r.environment.mu.Lock()
			r.environment.prepared = make(map[string]preparedRuntime)
			r.environment.mu.Unlock()
			f.runtime.inventory.mu.Lock()
			inspectBefore, removeBefore, provisionBefore := f.runtime.inventory.inspectCalls, len(f.runtime.inventory.removes), len(f.runtime.inventory.requests)
			f.runtime.inventory.mu.Unlock()
			before := initialOwnershipState(t, f)
			candidates, err := r.loadStartupRuntimeCandidates(ctx)
			supplementMust(t, err)
			if len(candidates) != 1 || candidates[0].Restore.RuntimeInstanceID != id {
				t.Fatalf("cold candidates=%+v", candidates)
			}
			_, _, _, directErr := r.restoreStartupRuntimeCandidate(ctx, candidates[0])
			restored, aggregateErr := r.restoreStartupRuntimes(ctx, startupSettlementQuarantine{})
			t.Logf("phase=%s Task=%s Session=%s Run=%s Turn=%s Runtime=%s generation=%d CID=%s statuses=%s/%s/%s released=%t activeRun=%t boundTask=%t direct=%v aggregate=%v", phase, taskID, e.WorkerSessionID, e.WorkerRunID, e.AgentTurnID, id, e.RuntimeGeneration, candidates[0].ContainerID, taskStatus, runStatus, turnStatus, released, activeRun, boundTask, directErr, aggregateErr)
			if singleRuntimeCause(directErr) != workerapp.InitialRuntimeStartReplayPending {
				t.Errorf("legal lifecycle direct boundary hard-refused: %v", directErr)
			}
			if aggregateErr != nil || len(restored) != 0 {
				t.Errorf("legal lifecycle aborted aggregate startup: %v restored=%d", aggregateErr, len(restored))
			}
			for _, guard := range []struct {
				name string
				run  func(context.Context) error
			}{{"failure", r.detectRuntimeFailures}, {"unowned", r.rejectUnownedRuntimes}} {
				if err := guard.run(ctx); singleRuntimeCause(err) != workerapp.InitialRuntimeStartReplayPending {
					t.Errorf("%s guard=%v", guard.name, err)
				}
			}
			adopted, err := r.restoreUnownedRuntimeByID(ctx, id)
			if singleRuntimeCause(err) != workerapp.InitialRuntimeStartReplayPending || adopted != nil {
				t.Errorf("adoption guard=%v adopted=%v", err, adopted)
			}
			if !bytes.Equal(before, initialOwnershipState(t, f)) {
				t.Error("hold changed business/control/ledger rows")
			}
			if _, ok := r.environment.get(id); ok || prepared.calls.Load() != 1 {
				t.Error("hold prepared/restored environment")
			}
			f.runtime.inventory.mu.Lock()
			inspect, remove, provision := f.runtime.inventory.inspectCalls, len(f.runtime.inventory.removes), len(f.runtime.inventory.requests)
			f.runtime.inventory.mu.Unlock()
			if inspect != inspectBefore || remove != removeBefore || provision != provisionBefore || provisionBefore != 1 || removeBefore != 0 || len(f.runtime.starts) != 0 {
				t.Errorf("hold effect deltas Inspect=%d Remove=%d Provision=%d pending Prompt=%d", inspect-inspectBefore, remove-removeBefore, provision-provisionBefore, len(f.runtime.starts))
			}
			if _, err := os.Stat(original.Storage.WorkingCopy.BackendPath); err != nil {
				t.Error("retained storage lost", err)
			}
			if after := initialExperienceView(t, f, taskPath); !bytes.Equal(task.Data, after.Data) {
				t.Error("Task history changed")
			}
			if after := initialExperienceView(t, f, workerPath); !bytes.Equal(worker.Data, after.Data) {
				t.Error("Worker history changed")
			}
			if artifactID != "" && (!bytes.Equal(artifactBefore, initialExperienceHTTP(t, f, http.MethodGet, download, nil, http.StatusOK)) || !bytes.Equal(graphBefore, initialExperienceGraph(t, f, taskID, artifactID))) {
				t.Error("Artifact content/identity changed")
			}
			if !t.Failed() {
				t.Log("read-only cold hold: resident/retained ownership unchanged; extra Prepare/Restore/Inspect/Provision/Prompt/Remove=0; public Task/Worker/Artifact reads preserved")
			}
			if phase == "consumed_running" || phase == "terminal_delivered" || phase == "accepted_completed" {
				assertInitialLifecycleCorruptionDenied(t, f, phase)
			}
		})
	}
}
