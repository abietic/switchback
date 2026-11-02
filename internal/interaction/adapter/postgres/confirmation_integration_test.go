//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/application/testkit"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestAssistantConfirmationCreatesTaskAtomicallyAndIsIdempotent(t *testing.T) {
	pool := postgrestest.Open(t)
	store, command, now := seedReadyConfirmation(t, pool, "success")
	taskIDs := &testkit.IDs{}
	service := interactionapp.ConfirmTaskDraft{
		States: store,
		Preparer: interactionapp.TaskConfirmationPreparer{
			IDs: &testkit.IDs{}, Clock: testkit.NewClock(now),
			TriggerPolicyRevision: config.DefaultAssistantTriggerPolicy, IntentTTL: 15 * time.Minute,
		},
		Admission: TaskAdmissionAdapter{
			Store: taskpostgres.NewStore(pool), IDs: taskIDs, Now: func() time.Time { return now },
		},
	}
	first, err := service.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.TaskID == "" || first.Status != "queued" {
		t.Fatalf("confirmation response did not converge: first=%+v second=%+v", first, second)
	}
	for table, count := range map[string]int{
		"tasks": 1, "task_transitions": 1, "session_allocation_requests": 1,
		"task_creation_intents": 1, "task_readiness_assessments": 1,
		"assistant_creation_intent_bindings": 1, "idempotency_results": 1,
	} {
		assertCount(t, pool, table, count)
	}
	var trigger, draftStatus string
	if err := pool.QueryRow(context.Background(), `
SELECT transition.trigger,draft.status
FROM task_transitions transition
JOIN tasks task ON task.task_id=transition.task_id
JOIN assistant_task_drafts draft ON draft.task_draft_id=$1`,
		command.TaskDraftID).Scan(&trigger, &draftStatus); err != nil {
		t.Fatal(err)
	}
	if trigger != "assistant_confirm" || draftStatus != "confirmed" {
		t.Fatalf("Assistant handoff provenance: trigger=%s draft=%s", trigger, draftStatus)
	}
	effectsResult, err := (interactionapp.PublishAssistantDerivedEffects{
		Objects: &testkit.ObjectStore{Err: errors.New("ObjectStore unavailable")},
		Store:   store, Clock: testkit.NewClock(now),
	}).Execute(context.Background(), interactionapp.PublishAssistantDerivedEffectsCommand{
		Evidence: &interactionapp.EvidenceBoundary{
			TenantID: command.Actor.TenantID, UserID: command.Actor.UserID,
			WorkspaceID: command.WorkspaceID, ConversationID: command.ConversationID,
			ConversationGeneration: 1, AssistantRunID: "run-success",
			BoundaryType: "task_created",
			Records:      []json.RawMessage{json.RawMessage(`{"task_id":"committed"}`)},
			Metadata:     map[string]string{"task_id": first.TaskID},
		},
	})
	if err != nil || effectsResult.EvidenceStatus != interactionapp.DerivedEffectDegraded {
		t.Fatalf("Task evidence degradation: result=%+v error=%v", effectsResult, err)
	}
	var taskStatus string
	if err := pool.QueryRow(context.Background(), `
SELECT status FROM tasks WHERE task_id=$1`, first.TaskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "queued" {
		t.Fatalf("ObjectStore failure rewrote committed Task status to %s", taskStatus)
	}
	projectionStore := projectionpostgres.NewStore(pool)
	dispatcher := platformpostgres.OutboxDispatcher{
		Pool: pool,
		Publisher: projectionpostgres.Materializer{
			Pool: pool, Projector: projectionapp.Projector{Store: projectionStore},
			UserID: command.Actor.UserID, Now: func() time.Time { return now },
		},
		LeaseOwner: "assistant-handoff-projection", LeaseDuration: time.Minute,
		BatchSize: 8, Now: func() time.Time { return now },
	}
	if published, err := dispatcher.RunOnce(context.Background()); err != nil || published != 1 {
		t.Fatalf("publish Assistant Task handoff: count=%d error=%v", published, err)
	}
	reader, err := projectionapp.NewViewContinuityReader(
		projectionStore,
		projectionapp.ViewContinuityReaderOptions{Now: func() time.Time { return now }},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.ReadSnapshot(context.Background(), projectionapp.SnapshotQuery{
		Viewer: projectionapp.Viewer{
			TenantID: command.Actor.TenantID, UserID: command.Actor.UserID,
		},
		ViewType: projectionapp.ViewAssistantConversation,
		ViewID:   command.ConversationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var conversationView struct {
		Handoffs []struct {
			TaskID string `json:"task_id"`
			Status string `json:"status"`
		} `json:"handoffs"`
	}
	if err := json.Unmarshal(snapshot.Data, &conversationView); err != nil ||
		len(conversationView.Handoffs) != 1 ||
		conversationView.Handoffs[0].TaskID != first.TaskID ||
		conversationView.Handoffs[0].Status != "queued" {
		t.Fatalf("Assistant Task handoff projection=%s error=%v", snapshot.Data, err)
	}

	reused := command
	reused.IdempotencyKey = "different-confirmation-key"
	if _, err := service.Execute(context.Background(), reused); !errors.Is(err, interactionapp.ErrConfirmationStale) {
		t.Fatalf("consumed Draft/intent was reused: %v", err)
	}
	assertCount(t, pool, "tasks", 1)

	stale := command
	stale.TaskSpecDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := service.Execute(context.Background(), stale); !errors.Is(err, interactionapp.ErrConfirmationStale) {
		t.Fatalf("stale duplicate confirmation = %v", err)
	}
	unauthorized := command
	unauthorized.Actor.UserID = "other-user"
	if _, err := service.Execute(context.Background(), unauthorized); !errors.Is(err, interactionapp.ErrConfirmationStale) {
		t.Fatalf("unauthorized confirmation = %v", err)
	}
}

func TestAssistantTaskAdmissionRollbackLeavesNoPartialTaskAuthority(t *testing.T) {
	pool := postgrestest.Open(t)
	store, command, now := seedReadyConfirmation(t, pool, "rollback")
	if _, err := pool.Exec(context.Background(), `DROP TABLE session_allocation_queue_positions`); err != nil {
		t.Fatal(err)
	}
	service := interactionapp.ConfirmTaskDraft{
		States: store,
		Preparer: interactionapp.TaskConfirmationPreparer{
			IDs: &testkit.IDs{}, Clock: testkit.NewClock(now),
			TriggerPolicyRevision: config.DefaultAssistantTriggerPolicy, IntentTTL: 15 * time.Minute,
		},
		Admission: TaskAdmissionAdapter{
			Store: taskpostgres.NewStore(pool), IDs: &testkit.IDs{}, Now: func() time.Time { return now },
		},
	}
	if _, err := service.Execute(context.Background(), command); err == nil {
		t.Fatal("Task admission unexpectedly succeeded without allocation storage")
	}
	for _, table := range []string{
		"tasks", "task_creation_intents", "task_readiness_assessments",
		"assistant_creation_intent_bindings", "idempotency_results",
	} {
		assertCount(t, pool, table, 0)
	}
	var draftStatus string
	if err := pool.QueryRow(context.Background(), `
SELECT status FROM assistant_task_drafts WHERE task_draft_id=$1`, command.TaskDraftID).Scan(&draftStatus); err != nil {
		t.Fatal(err)
	}
	if draftStatus != "current" {
		t.Fatalf("rolled-back confirmation changed Draft status to %s", draftStatus)
	}
}

func seedReadyConfirmation(
	t *testing.T,
	pool *pgxpool.Pool,
	suffix string,
) (Store, interactionapp.ConfirmTaskDraftCommand, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 2, 0, 0, 0, time.UTC)
	store := Store{Pool: pool}
	conversation, err := domain.NewConversation(
		"conversation-"+suffix, "tenant-local", "user-local", now.Add(-time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.SelectWorkspace(1, domain.WorkspaceResolution{
		WorkspaceID: "workspace-local", WorkspaceRevisionID: "workspace-revision-local-v1",
		ResolutionID: "resolution-local", ResolutionRevision: 1, MembershipRevision: 1,
	}, now.Add(-50*time.Second)); err != nil {
		t.Fatal(err)
	}
	accepted, err := conversation.AcceptMessage(
		2, "turn-"+suffix, "run-"+suffix, "command-"+suffix, "Build a health endpoint.",
		config.KimiAssistantOpenAIV1, config.DefaultAssistantReadinessPolicy, 8192,
		now.Add(-40*time.Second), now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithinTransaction(ctx, func(tx interactionapp.Transaction) error {
		if err := tx.InsertConversation(ctx, conversation); err != nil {
			return err
		}
		if err := tx.InsertTurn(ctx, accepted.Turn); err != nil {
			return err
		}
		return tx.InsertRun(ctx, accepted.Run, "1",
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE assistant_runs
SET status='completed',completed_at=$1,updated_at=$1
WHERE assistant_run_id=$2`, now.Add(-30*time.Second), accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	dimensions := make(map[domain.ReadinessDimension]domain.DimensionState)
	for _, dimension := range domain.RequiredReadinessDimensions() {
		dimensions[dimension] = domain.DimensionState{
			Status: domain.DimensionKnown, Summary: "known", EvidenceRefs: []string{"turn-" + suffix},
		}
	}
	readiness := domain.TaskReadinessAssessment{
		ID: "assessment-" + suffix, ConversationID: conversation.ID, AssistantRunID: accepted.Run.ID,
		Revision: 1, PolicyRevision: config.DefaultAssistantReadinessPolicy, Dimensions: dimensions,
		BlockingUnknowns: []string{}, Assumptions: []string{},
		Status: domain.ReadinessReadyConfirmed, EvaluatedAt: now.Add(-30 * time.Second),
	}
	readinessDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := store.SaveReadiness(ctx, interactionapp.ReadinessRecord{
		Assessment: readiness, Workspace: conversation.Workspace, Digest: readinessDigest,
	}); err != nil {
		t.Fatal(err)
	}
	workerProfile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	proposal := contractsv1.AssistantDecision{
		SchemaVersion: contractsv1.AssistantDecisionSchemaVersion, DecisionType: "ready_draft",
		UserVisibleContent: "Ready.", Readiness: readinessContract(readiness),
		TaskDraft: &contractsv1.AssistantTaskDraft{
			SchemaVersion: contractsv1.TaskDraftSchemaVersion, TaskDraftID: "model-draft",
			Revision: 1, InitiatorUserID: "user-local", ConversationID: conversation.ID,
			WorkspaceID:           conversation.Workspace.WorkspaceID,
			WorkspaceRevisionID:   conversation.Workspace.WorkspaceRevisionID,
			ReadinessAssessmentID: readiness.ID,
			TaskSpecDigest:        "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			Title:                 "Health endpoint", Goal: "Add a health endpoint.",
			Target:      json.RawMessage(`{"kind":"repository","ref":"repo-local"}`),
			Constraints: []string{}, AcceptanceCriteria: []string{"tests pass"},
			Assumptions: []string{}, TaskSpec: json.RawMessage(`{"model_owned":true}`),
			CreatedAt: now.Add(-20 * time.Second),
		},
	}
	built, err := (interactionapp.TaskDraftBuilder{
		IDs: &testkit.IDs{}, Clock: testkit.NewClock(now.Add(-20 * time.Second)),
		Policy: interactionapp.TaskDraftPolicy{
			ProjectID: "project-local", TriggerPolicyRevision: config.DefaultAssistantTriggerPolicy,
			NetworkProfile: trustedNetworkProfile(t), AgentProfile: workerProfile,
			UserVisibleTypes: []string{"report"}, ContextRefs: []contractsv1.ContextRef{},
			SkillRefs: []string{}, MCPRefs: []string{}, ToolRefs: []string{},
		},
	}).Build(interactionapp.RunClaim{Conversation: conversation, Run: accepted.Run},
		interactionapp.ValidatedDecision{Decision: proposal, Readiness: readiness, Digest: readinessDigest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceCurrentDraft(ctx, "", interactionapp.TaskDraftRecord{
		Draft: built.Draft, TaskSpec: built.TaskSpec,
	}, now.Add(-20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE assistant_conversations
SET current_task_draft_id=$1,current_readiness_assessment_id=$2,revision=revision+1,updated_at=$3
WHERE conversation_id=$4`,
		built.Draft.ID, readiness.ID, now.Add(-10*time.Second), conversation.ID); err != nil {
		t.Fatal(err)
	}
	conversation.Revision++
	command := interactionapp.ConfirmTaskDraftCommand{
		Actor:          taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
		ConversationID: conversation.ID, ExpectedConversationRevision: conversation.Revision,
		TaskDraftID: built.Draft.ID, TaskDraftRevision: built.Draft.Revision,
		ReadinessAssessmentID: readiness.ID, ReadinessAssessmentRevision: readiness.Revision,
		WorkspaceID:                 conversation.Workspace.WorkspaceID,
		WorkspaceRevisionID:         conversation.Workspace.WorkspaceRevisionID,
		WorkspaceResolutionID:       conversation.Workspace.ResolutionID,
		WorkspaceResolutionRevision: conversation.Workspace.ResolutionRevision,
		MembershipRevision:          conversation.Workspace.MembershipRevision,
		TaskSpecDigest:              built.Draft.TaskSpecDigest,
		TriggerPolicyRevision:       config.DefaultAssistantTriggerPolicy,
		IdempotencyKey:              "confirm-" + suffix,
	}
	return store, command, now
}

func readinessContract(readiness domain.TaskReadinessAssessment) contractsv1.AssistantReadinessAssessment {
	dimensions := make([]contractsv1.ReadinessDimensionState, 0, len(readiness.Dimensions))
	for _, dimension := range domain.RequiredReadinessDimensions() {
		state := readiness.Dimensions[dimension]
		dimensions = append(dimensions, contractsv1.ReadinessDimensionState{
			Dimension: string(dimension), Status: string(state.Status), Summary: state.Summary,
			EvidenceRefs: append([]string{}, state.EvidenceRefs...),
		})
	}
	return contractsv1.AssistantReadinessAssessment{
		SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion,
		AssessmentID:  readiness.ID, Revision: readiness.Revision,
		PolicyRevision: readiness.PolicyRevision, Dimensions: dimensions,
		BlockingUnknowns:       append([]string{}, readiness.BlockingUnknowns...),
		Assumptions:            append([]string{}, readiness.Assumptions...),
		ClarificationQuestions: readiness.ClarificationQuestions,
		Status:                 string(readiness.Status), EvaluatedAt: readiness.EvaluatedAt,
	}
}

func trustedNetworkProfile(t *testing.T) config.RuntimeNetworkProfile {
	t.Helper()
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}
