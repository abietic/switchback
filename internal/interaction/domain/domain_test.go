package domain

import (
	"errors"
	"testing"
	"time"
)

func TestConversationAcceptsOrderedMessagesAndSerializesClaims(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, err := NewConversation("conversation-1", "tenant-local", "user-local", now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := conversation.AcceptMessage(1, "turn-1", "run-1", "correlation-1", "first",
		"assistant-kimi", "readiness-v1", 4096, now.Add(time.Second), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	second, err := conversation.AcceptMessage(2, "turn-2", "run-2", "correlation-2", "second",
		"assistant-kimi", "readiness-v1", 4096, now.Add(2*time.Second), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.Turn.Sequence != 1 || first.Run.Sequence != 1 || second.Turn.Sequence != 2 || second.Run.Sequence != 2 {
		t.Fatalf("message ordering was not stable: first=%+v second=%+v", first, second)
	}
	if err := conversation.ClaimRun(3, "run-1", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conversation.ClaimRun(4, "run-2", now.Add(4*time.Second)); !errors.Is(err, ErrRunActive) {
		t.Fatalf("second active run was accepted: %v", err)
	}
	if err := conversation.ReleaseRun(4, "run-1", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conversation.ClaimRun(5, "run-2", now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestConversationRejectsStaleMutationAndInvalidatesDraftOnMessage(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, _ := NewConversation("conversation-1", "tenant-local", "user-local", now)
	if err := conversation.BindDerivedState(1, "draft-1", "assessment-1", "checkpoint-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.AcceptMessage(1, "turn-1", "run-1", "correlation-1", "changed",
		"assistant-kimi", "readiness-v1", 4096, now.Add(2*time.Second), now.Add(time.Minute)); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale mutation was accepted: %v", err)
	}
	if _, err := conversation.AcceptMessage(2, "turn-1", "run-1", "correlation-1", "changed",
		"assistant-kimi", "readiness-v1", 4096, now.Add(2*time.Second), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if conversation.CurrentTaskDraftID != "" || conversation.CurrentReadinessAssessmentID != "" ||
		conversation.CurrentCheckpointID != "" {
		t.Fatalf("derived state survived changed intent: %+v", conversation)
	}
}

func TestConversationCommitsAssistantDecisionAsOneRevision(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, _ := NewConversation("conversation-1", "tenant-local", "user-local", now)
	if _, err := conversation.AcceptMessage(
		1, "turn-user", "run-1", "command-user", "build it", "assistant-kimi",
		"readiness-v1", 4096, now.Add(time.Second), now.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if err := conversation.ClaimRun(2, "run-1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.CommitAssistantDecision(
		3, "run-1", "turn-assistant", "command-user", "Which Workspace?",
		"", "assessment-1", "checkpoint-1", now.Add(3*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Sequence != 2 || turn.Role != TurnAssistant || turn.SourceAssistantRunID != "run-1" {
		t.Fatalf("Assistant Turn is not server ordered: %+v", turn)
	}
	if conversation.Revision != 4 || conversation.NextTurnSequence != 3 ||
		conversation.ActiveAssistantRunID != "" ||
		conversation.CurrentReadinessAssessmentID != "assessment-1" ||
		conversation.CurrentCheckpointID != "checkpoint-1" {
		t.Fatalf("Assistant decision did not converge Conversation state: %+v", conversation)
	}
}

func TestAssistantRunLeaseFencesTerminalOutcome(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	run := AssistantRun{
		ID: "run-1", ConversationID: "conversation-1", SourceTurnID: "turn-1",
		Sequence: 1, Status: RunQueued, ModelProfile: "assistant-kimi", PolicyRevision: "readiness-v1",
		ContextBudgetTokens: 4096, TimeoutAt: now.Add(time.Minute), Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := run.Claim(1, "dispatcher-1", now.Add(20*time.Second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(2, 0, TerminalOutcome{
		Status: RunCompleted, Decision: []byte(`{"decision":"ok"}`), OccurredAt: now.Add(2 * time.Second),
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("stale fencing token finished Run: %v", err)
	}
	if err := run.Finish(2, 1, TerminalOutcome{
		Status: RunCompleted, Decision: []byte(`{"decision":"ok"}`), OccurredAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if !run.Terminal() || run.Status != RunCompleted {
		t.Fatalf("Run did not become terminal: %+v", run)
	}
}

func TestReadinessRequiresEveryDimensionAndHardBlockers(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	states := make(map[ReadinessDimension]DimensionState)
	for _, dimension := range RequiredReadinessDimensions() {
		states[dimension] = DimensionState{Status: DimensionKnown, Summary: "known"}
	}
	states[DimensionContextAccess] = DimensionState{Status: DimensionBlockingUnknown, Summary: "Workspace is unresolved"}
	assessment := TaskReadinessAssessment{
		ID: "assessment-1", ConversationID: "conversation-1", AssistantRunID: "run-1",
		Revision: 1, PolicyRevision: "readiness-v1", Dimensions: states,
		BlockingUnknowns: []string{"context_access"}, Status: ReadinessClarifying, EvaluatedAt: now,
	}
	if err := assessment.Validate(); err != nil {
		t.Fatal(err)
	}
	if assessment.Ready() {
		t.Fatal("hard blocker was treated as ready")
	}
	assessment.Status = ReadinessReadyConfirmed
	if err := assessment.Validate(); err == nil {
		t.Fatal("blocking assessment was confirmed")
	}
}

func TestTaskDraftCannotBeConfirmedAfterInvalidation(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	draft, err := NewTaskDraft("draft-1", "conversation-1", "user-local", "", "assessment-1",
		"workspace-local", "workspace-revision-local-v1", 1, []byte(`{"goal":"test"}`),
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := draft.Invalidate(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := draft.Confirm(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalidated TaskDraft was confirmed: %v", err)
	}
}
