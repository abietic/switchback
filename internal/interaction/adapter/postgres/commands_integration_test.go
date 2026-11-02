//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/application/testkit"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestRevisionedConversationCommandsConvergeAcrossCreateOpenSelectCancelAndRetry(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	clock := testkit.NewClock(now)
	ids := &testkit.IDs{}
	store := Store{Pool: pool}
	actor := taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}
	created, err := (interactionapp.CreateConversation{
		Transactions: store, IDs: ids, Clock: clock,
	}).Execute(ctx, interactionapp.CreateConversationCommand{
		Actor: actor, IdempotencyKey: "create-conversation-1",
	})
	if err != nil || created.ConversationRevision != 1 {
		t.Fatalf("create Conversation: result=%+v error=%v", created, err)
	}
	opened, err := (interactionapp.OpenConversation{
		Transactions: store, IDs: ids, Clock: clock,
	}).Execute(ctx, interactionapp.OpenConversationCommand{
		Actor: actor, ConversationID: created.ConversationID,
		ExpectedConversationRevision: 1, IdempotencyKey: "open-conversation-1",
	})
	if err != nil || opened.Status != "completed" || opened.ConversationRevision != 1 {
		t.Fatalf("open Conversation: result=%+v error=%v", opened, err)
	}
	clock.Advance(time.Second)
	workspace := domain.WorkspaceResolution{
		WorkspaceID: "workspace-local", WorkspaceRevisionID: "workspace-revision-local-v1",
		ResolutionID: "resolution-local", ResolutionRevision: 1, MembershipRevision: 1,
	}
	selected, err := (interactionapp.SelectConversationWorkspace{
		Transactions: store, IDs: ids, Clock: clock,
	}).Execute(ctx, interactionapp.SelectWorkspaceCommand{
		Actor: actor, ConversationID: created.ConversationID,
		ExpectedConversationRevision: 1, IdempotencyKey: "select-workspace-1",
		Workspace: workspace,
	})
	if err != nil || selected.ConversationRevision != 2 {
		t.Fatalf("select Workspace: result=%+v error=%v", selected, err)
	}
	profile, _ := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	send := interactionapp.SendConversationMessage{
		Transactions: store, IDs: ids, Clock: clock, ModelProfile: profile.Ref(),
		PolicyRevision:      config.DefaultAssistantReadinessPolicy,
		ContextBudgetTokens: 8192, RunTimeout: time.Minute,
	}
	clock.Advance(time.Second)
	first, err := send.Execute(ctx, interactionapp.SendConversationMessageCommand{
		Actor: actor, ConversationID: created.ConversationID,
		ExpectedConversationRevision: 2, IdempotencyKey: "message-cancel", Content: "cancel me",
	})
	if err != nil {
		t.Fatal(err)
	}
	exact := CommandService{
		Store: store, IDs: ids, Clock: clock,
		ModelProfile:        profile.Ref(),
		PolicyRevision:      config.DefaultAssistantReadinessPolicy,
		ContextBudgetTokens: 8192,
		RunTimeout:          time.Minute,
	}
	clock.Advance(time.Second)
	cancelled, err := exact.CancelQueuedRun(ctx, interactionapp.CancelQueuedRunCommand{
		Actor: actor, ConversationID: created.ConversationID,
		ExpectedConversationRevision: 3, AssistantRunID: first.AssistantRunID,
		AssistantRunRevision: 1, IdempotencyKey: "cancel-run-1",
	})
	if err != nil || cancelled.ConversationRevision != 4 ||
		cancelled.AssistantRunRevision != 2 {
		t.Fatalf("cancel queued Run: result=%+v error=%v", cancelled, err)
	}
	cancelledAgain, err := exact.CancelQueuedRun(ctx, interactionapp.CancelQueuedRunCommand{
		Actor: actor, ConversationID: created.ConversationID,
		ExpectedConversationRevision: 3, AssistantRunID: first.AssistantRunID,
		AssistantRunRevision: 1, IdempotencyKey: "cancel-run-1",
	})
	if err != nil || cancelledAgain != cancelled {
		t.Fatalf("idempotent cancel changed: first=%+v second=%+v error=%v",
			cancelled, cancelledAgain, err)
	}
	clock.Advance(time.Second)
	second, err := send.Execute(ctx, interactionapp.SendConversationMessageCommand{
		Actor: actor, ConversationID: created.ConversationID,
		ExpectedConversationRevision: 4, IdempotencyKey: "message-retry", Content: "retry me",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	sourceFailureAt := clock.Now().UTC()
	if _, err := pool.Exec(ctx, `
UPDATE assistant_runs
SET status='failed',retryable=true,error_code='model_timeout',completed_at=$1,
    model_profile=$2,model_profile_revision=$3,model_profile_digest=$4,
    policy_revision=$5,context_budget_tokens=$6,
    revision=revision+1,updated_at=$1
WHERE assistant_run_id=$7 AND status='queued'`,
		sourceFailureAt, config.KimiCodeAssistantOpenAIV1, "1",
		"sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c",
		"assistant-readiness-v0", 4096, second.AssistantRunID,
	); err != nil {
		t.Fatal(err)
	}
	type sourceRunSnapshot struct {
		status, errorCode                            string
		profileID, profileRevision, profileDigest    string
		policyRevision                               string
		revision                                     uint64
		contextBudget                                int
		retryable                                    bool
		completedAt, timeoutAt, createdAt, updatedAt time.Time
	}
	loadSourceRun := func() sourceRunSnapshot {
		t.Helper()
		var snapshot sourceRunSnapshot
		if err := pool.QueryRow(ctx, `
SELECT status,error_code,model_profile,model_profile_revision,model_profile_digest,
       policy_revision,revision,context_budget_tokens,retryable,
       completed_at,timeout_at,created_at,updated_at
FROM assistant_runs WHERE assistant_run_id=$1`, second.AssistantRunID).Scan(
			&snapshot.status, &snapshot.errorCode,
			&snapshot.profileID, &snapshot.profileRevision, &snapshot.profileDigest,
			&snapshot.policyRevision, &snapshot.revision, &snapshot.contextBudget,
			&snapshot.retryable, &snapshot.completedAt, &snapshot.timeoutAt,
			&snapshot.createdAt, &snapshot.updatedAt,
		); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	sourceBeforeRetry := loadSourceRun()
	retryNow := sourceFailureAt.Add(10 * time.Second)
	clock.Advance(retryNow.Sub(clock.Now()))
	retryCommand := interactionapp.RetryFailedRunCommand{
		Actor: actor, ConversationID: created.ConversationID,
		ExpectedConversationRevision: 5, AssistantRunID: second.AssistantRunID,
		AssistantRunRevision: 2, IdempotencyKey: "retry-run-1",
	}
	retried, err := exact.RetryFailedRun(ctx, retryCommand)
	if err != nil || retried.ConversationRevision != 6 ||
		retried.AssistantRunID == second.AssistantRunID ||
		retried.AssistantRunRevision != 1 {
		t.Fatalf("retry failed Run: result=%+v error=%v", retried, err)
	}
	var retriedProfileID, retriedProfileRevision, retriedProfileDigest string
	var retriedPolicyRevision string
	var retriedContextBudget int
	var retriedTimeoutAt time.Time
	sourceAfterRetry := loadSourceRun()
	if err := pool.QueryRow(ctx, `
SELECT model_profile,model_profile_revision,model_profile_digest,policy_revision,
       context_budget_tokens,timeout_at
FROM assistant_runs WHERE assistant_run_id=$1`, retried.AssistantRunID).Scan(
		&retriedProfileID, &retriedProfileRevision, &retriedProfileDigest,
		&retriedPolicyRevision, &retriedContextBudget, &retriedTimeoutAt,
	); err != nil {
		t.Fatal(err)
	}
	if sourceAfterRetry != sourceBeforeRetry {
		t.Fatalf("retry mutated source terminal evidence:\nbefore=%+v\nafter=%+v",
			sourceBeforeRetry, sourceAfterRetry)
	}
	if sourceBeforeRetry.status != "failed" || sourceBeforeRetry.revision != 2 ||
		sourceBeforeRetry.errorCode != "model_timeout" || !sourceBeforeRetry.retryable ||
		!sourceBeforeRetry.completedAt.Equal(sourceFailureAt) ||
		sourceBeforeRetry.profileID != config.KimiCodeAssistantOpenAIV1 ||
		sourceBeforeRetry.profileRevision != "1" ||
		sourceBeforeRetry.profileDigest != "sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c" ||
		sourceBeforeRetry.policyRevision != "assistant-readiness-v0" ||
		sourceBeforeRetry.contextBudget != 4096 ||
		retriedProfileID != profile.ID || retriedProfileRevision != "2" ||
		retriedProfileDigest != profile.Digest ||
		retriedPolicyRevision != config.DefaultAssistantReadinessPolicy ||
		retriedContextBudget != 8192 {
		t.Fatalf("retry migration evidence mismatch: source=%+v retry=%s/%s/%s policy=%s budget=%d",
			sourceBeforeRetry,
			retriedProfileID, retriedProfileRevision, retriedProfileDigest,
			retriedPolicyRevision, retriedContextBudget)
	}
	wantRetryTimeout := retryNow.Add(exact.RunTimeout).UTC().Truncate(time.Microsecond)
	if got := retriedTimeoutAt.UTC().Truncate(time.Microsecond); !got.Equal(wantRetryTimeout) {
		t.Fatalf("retry timeout_at = %s, want fresh retry deadline %s",
			got.Format(time.RFC3339Nano), wantRetryTimeout.Format(time.RFC3339Nano))
	}
	deepseek, _ := config.ProductionProfileRegistry().Assistant(config.DeepSeekAssistantOpenAIV1)
	exact.ModelProfile = deepseek.Ref()
	exact.PolicyRevision = "assistant-readiness-v-next"
	exact.ContextBudgetTokens = 4096
	retriedAgain, err := exact.RetryFailedRun(ctx, retryCommand)
	if err != nil || retriedAgain != retried {
		t.Fatalf("idempotent retry changed after service policy drift: first=%+v second=%+v error=%v",
			retried, retriedAgain, err)
	}

	dimensions := make(map[domain.ReadinessDimension]domain.DimensionState)
	for _, dimension := range domain.RequiredReadinessDimensions() {
		dimensions[dimension] = domain.DimensionState{
			Status: domain.DimensionKnown, Summary: "known",
		}
	}
	dimensions[domain.DimensionGoal] = domain.DimensionState{
		Status: domain.DimensionBlockingUnknown, Summary: "goal missing",
	}
	readiness := interactionapp.ReadinessRecord{
		Assessment: domain.TaskReadinessAssessment{
			ID: "assessment-command", ConversationID: created.ConversationID,
			AssistantRunID: retried.AssistantRunID, Revision: 6,
			PolicyRevision: config.DefaultAssistantReadinessPolicy, Dimensions: dimensions,
			BlockingUnknowns: []string{"goal"}, Assumptions: []string{},
			Status: domain.ReadinessClarifying, EvaluatedAt: clock.Now(),
		},
		Workspace: &workspace,
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := store.SaveReadiness(ctx, readiness); err != nil {
		t.Fatal(err)
	}
	checkpoint := domain.ClarificationCheckpoint{
		ID: "checkpoint-command", ConversationID: created.ConversationID, Revision: 1,
		WorkingDraft:          json.RawMessage(`{"goal":"partial"}`),
		ReadinessAssessmentID: readiness.Assessment.ID, Blockers: []string{"goal"},
		Assumptions: []string{}, EvidenceRefs: []string{},
		AllowedActions: []domain.CheckpointAction{
			domain.CheckpointContinue, domain.CheckpointNarrowScope, domain.CheckpointCancel,
		},
		Status: domain.CheckpointActive, CreatedAt: clock.Now(),
	}
	if err := store.SaveCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	if _, err := pool.Exec(ctx, `
UPDATE assistant_conversations
SET current_readiness_assessment_id=$1,current_checkpoint_id=$2,
    revision=revision+1,updated_at=$3
WHERE conversation_id=$4`,
		readiness.Assessment.ID, checkpoint.ID, clock.Now(), created.ConversationID,
	); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	applied, err := exact.ApplyCheckpointAction(ctx, interactionapp.CheckpointActionCommand{
		Actor: actor, ConversationID: created.ConversationID,
		ExpectedConversationRevision: 7, CheckpointID: checkpoint.ID,
		CheckpointRevision: 1, Action: domain.CheckpointNarrowScope,
		IdempotencyKey: "checkpoint-action-1",
	})
	if err != nil || applied.ConversationRevision != 8 {
		t.Fatalf("apply checkpoint action: result=%+v error=%v", applied, err)
	}
	var sequences []uint64
	if err := pool.QueryRow(ctx, `
SELECT array_agg(sequence ORDER BY sequence)
FROM outbox_events WHERE stream_id=$1`,
		"conversation/"+created.ConversationID,
	).Scan(&sequences); err != nil {
		t.Fatal(err)
	}
	for index, sequence := range sequences {
		if sequence != uint64(index+1) {
			t.Fatalf("Conversation event sequences = %v", sequences)
		}
	}
}
