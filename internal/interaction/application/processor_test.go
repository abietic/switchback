package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
)

type processorRuns struct {
	invocationCalls int
	decision        *RunDecisionCommit
	failure         *RunFailureCommit
}

func (runs *processorRuns) ClaimNextRun(context.Context, string, time.Duration, time.Time) (RunClaim, error) {
	return RunClaim{}, ErrNoQueuedRun
}
func (*processorRuns) LoadRun(context.Context, string) (domain.AssistantRun, error) {
	return domain.AssistantRun{}, nil
}
func (runs *processorRuns) MarkInvocationStarted(_ context.Context, _ string, revision, _ uint64, _ time.Time) (uint64, error) {
	runs.invocationCalls++
	return revision + 1, nil
}
func (runs *processorRuns) CommitDecision(_ context.Context, commit RunDecisionCommit) error {
	runs.decision = &commit
	return nil
}
func (runs *processorRuns) CommitFailure(_ context.Context, commit RunFailureCommit) error {
	runs.failure = &commit
	return nil
}
func (*processorRuns) RequeueExpired(context.Context, string, uint64, uint64, time.Time) error {
	return nil
}
func (*processorRuns) RecoverExpiredClaims(context.Context, time.Time) (RunRecoveryResult, error) {
	return RunRecoveryResult{}, nil
}

type processorContext struct {
	candidates []ContextItem
	err        error
	packs      []ContextPack
}

func (repository *processorContext) LoadContextCandidates(context.Context, RunClaim) ([]ContextItem, error) {
	if repository.err != nil {
		return nil, repository.err
	}
	return append([]ContextItem(nil), repository.candidates...), nil
}
func (repository *processorContext) SaveContextPack(_ context.Context, pack ContextPack) error {
	repository.packs = append(repository.packs, pack)
	return nil
}

type processorDrafts struct{ current *TaskDraftRecord }

func (drafts processorDrafts) LoadCurrentTaskDraft(context.Context, string) (*TaskDraftRecord, error) {
	return drafts.current, nil
}

type processorEffects struct{ records []DerivedEffectRecord }

func (effects *processorEffects) RecordAssistantDerivedEffects(_ context.Context, record DerivedEffectRecord) error {
	effects.records = append(effects.records, record)
	return nil
}

type processorIDs struct{ next int }

func (ids *processorIDs) NewID(kind string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", kind, ids.next), nil
}

type processorClock struct{ now time.Time }

func (clock processorClock) Now() time.Time { return clock.now }

type processorModel struct {
	result ModelResult
	err    error
	calls  int
}

func (model *processorModel) Complete(_ context.Context, _ ModelRequest) (ModelResult, error) {
	model.calls++
	return model.result, model.err
}

func TestAssistantRunProcessorCommitsClarificationAndCheckpoint(t *testing.T) {
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	clock := processorClock{now: now}
	ids := &processorIDs{}
	claim := processorClaim(now)
	contextRepository := &processorContext{candidates: []ContextItem{
		{Ref: "turn/user-1", Kind: "conversation_turn", Priority: 100, EstimatedTokens: 1, Content: json.RawMessage(`{"content":"build it"}`)},
	}}
	runs := &processorRuns{}
	effects := &processorEffects{}
	model := &processorModel{result: ModelResult{Decision: processorClarificationDecision(now)}}
	processor := processorFixture(runs, contextRepository, processorDrafts{}, model, effects, ids, clock)
	if err := processor.ProcessAssistantRun(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if runs.invocationCalls != 1 || runs.decision == nil || runs.failure != nil || len(contextRepository.packs) != 1 {
		t.Fatalf("processor did not commit exactly one clarification: runs=%+v packs=%d", runs, len(contextRepository.packs))
	}
	if runs.decision.Decision.ClarificationQuestion != "What concrete outcome should this work achieve?" ||
		runs.decision.Checkpoint == nil || runs.decision.Decision.Checkpoint == nil ||
		runs.decision.Event.EventType != "AssistantDecisionCommitted.v1" {
		t.Fatalf("clarification commit = %+v", runs.decision)
	}
	if len(effects.records) != 1 || effects.records[0].EvidenceStatus != DerivedEffectDegraded {
		t.Fatalf("checkpoint effects = %+v", effects.records)
	}
}

func TestAssistantRunProcessorFailsClosedBeforeAnyTaskAuthority(t *testing.T) {
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		contextErr     error
		modelResult    ModelResult
		modelErr       error
		wantCode       string
		wantInvocation int
	}{
		{name: "context", contextErr: errors.New("database unavailable"), wantCode: AssistantFailureContextCompile},
		{name: "timeout", modelErr: ErrModelTimeout, wantCode: AssistantFailureModelTimeout, wantInvocation: 1},
		{name: "malformed", modelErr: ErrModelMalformed, wantCode: AssistantFailureModelMalformed, wantInvocation: 1},
		{name: "policy", modelErr: ErrModelPolicyConflict, wantCode: AssistantFailureModelPolicy, wantInvocation: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := processorClock{now: now}
			ids := &processorIDs{}
			runs := &processorRuns{}
			effects := &processorEffects{}
			contextRepository := &processorContext{err: test.contextErr, candidates: []ContextItem{
				{Ref: "turn/user-1", Kind: "conversation_turn", Priority: 100, EstimatedTokens: 1, Content: json.RawMessage(`{"content":"build it"}`)},
			}}
			model := &processorModel{result: test.modelResult, err: test.modelErr}
			processor := processorFixture(runs, contextRepository, processorDrafts{}, model, effects, ids, clock)
			if err := processor.ProcessAssistantRun(context.Background(), processorClaim(now)); err != nil {
				t.Fatal(err)
			}
			if runs.decision != nil || runs.failure == nil || runs.failure.ErrorCode != test.wantCode ||
				runs.invocationCalls != test.wantInvocation || runs.failure.Event.EventType != "AssistantRunFailed.v1" {
				t.Fatalf("failure convergence = runs=%+v", runs)
			}
		})
	}
}

func TestAssistantRunProcessorRecordsExpiredRunWithoutCallingModel(t *testing.T) {
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	clock := processorClock{now: now}
	ids := &processorIDs{}
	runs := &processorRuns{}
	effects := &processorEffects{}
	contexts := &processorContext{candidates: []ContextItem{{Ref: "turn/user-1", Kind: "conversation_turn", Priority: 100, EstimatedTokens: 1, Content: json.RawMessage(`{"content":"build it"}`)}}}
	model := &processorModel{}
	claim := processorClaim(now)
	claim.Run.TimeoutAt = now
	processor := processorFixture(runs, contexts, processorDrafts{}, model, effects, ids, clock)
	if err := processor.ProcessAssistantRun(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if model.calls != 0 || runs.invocationCalls != 0 || runs.failure == nil || runs.failure.ErrorCode != AssistantFailureModelTimeout {
		t.Fatalf("expired run did not converge without a model call: model=%d runs=%+v", model.calls, runs)
	}
}

func TestAssistantRunProcessorCommitsServerBuiltReadyDraft(t *testing.T) {
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	clock := processorClock{now: now}
	ids := &processorIDs{}
	claim := processorClaim(now)
	claim.Conversation.Workspace = &domain.WorkspaceResolution{WorkspaceID: "workspace-local", WorkspaceRevisionID: "workspace-revision-v1", ResolutionID: "resolution-1", ResolutionRevision: 1, MembershipRevision: 1}
	runs := &processorRuns{}
	effects := &processorEffects{}
	contexts := &processorContext{candidates: []ContextItem{{Ref: "turn/user-1", Kind: "conversation_turn", Priority: 100, EstimatedTokens: 1, Content: json.RawMessage(`{"content":"implement it"}`)}}}
	processor := processorFixture(runs, contexts, processorDrafts{}, &processorModel{result: ModelResult{Decision: processorReadyDecision(now)}}, effects, ids, clock)
	agent, ok := config.ProductionProfileRegistry().Agent(config.KimiCodexAnthropicV1)
	if !ok {
		t.Fatal("server agent profile missing")
	}
	processor.DraftBuilder.Policy = TaskDraftPolicy{ProjectID: "project-local", TriggerPolicyRevision: config.DefaultAssistantTriggerPolicy,
		NetworkProfile: currentNetworkProfile(), AgentProfile: agent, UserVisibleTypes: []string{"report"},
		ContextRefs: []contractsv1.ContextRef{}, SkillRefs: []string{}, MCPRefs: []string{}, ToolRefs: []string{}}
	if err := processor.ProcessAssistantRun(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if runs.failure != nil || runs.decision == nil || runs.decision.TaskDraft == nil ||
		runs.decision.Decision.TaskDraft == nil || runs.decision.Decision.TaskDraft.TaskDraftID != runs.decision.TaskDraft.Draft.ID {
		t.Fatalf("ready draft was not server-built and committed: %+v", runs)
	}
	if err := runs.decision.Decision.Validate(); err != nil {
		t.Fatalf("server-built ready decision is not wire-valid: %v", err)
	}
}

func processorFixture(runs *processorRuns, contexts *processorContext, drafts processorDrafts, model AssistantModel, effects *processorEffects, ids IDGenerator, clock Clock) AssistantRunProcessor {
	policy := ReadinessPolicy{Revision: "assistant-readiness-v1", AssumableDimensions: map[domain.ReadinessDimension]bool{domain.DimensionConstraints: true}}
	return AssistantRunProcessor{
		Runs: runs, Context: ContextCompiler{Repository: contexts, IDs: ids, Budget: 1024}, CurrentDrafts: drafts,
		Model: model, Validator: DecisionValidator{IDs: ids, Clock: clock, Policy: policy},
		DraftBuilder: TaskDraftBuilder{IDs: ids, Clock: clock}, Clarifications: ClarificationPlanner{IDs: ids, Clock: clock},
		Effects: PublishAssistantDerivedEffects{Store: effects, Clock: clock}, IDs: ids, Clock: clock,
	}
}

func processorClaim(now time.Time) RunClaim {
	return RunClaim{Conversation: domain.Conversation{ID: "conversation-1", TenantID: "tenant-local", ActorUserID: "user-local", Generation: 1, Revision: 5},
		Run: domain.AssistantRun{ID: "run-1", ConversationID: "conversation-1", ModelProfile: "assistant-profile", PolicyRevision: "assistant-readiness-v1",
			Revision: 2, FencingToken: 1, TimeoutAt: now.Add(time.Minute), LeaseExpiresAt: now.Add(time.Minute)},
		ModelProfileRevision: 1, ModelProfileDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
}

func processorClarificationDecision(now time.Time) contractsv1.AssistantDecision {
	dimensions := make([]contractsv1.ReadinessDimensionState, 0, 7)
	for _, dimension := range domain.RequiredReadinessDimensions() {
		status := domain.DimensionKnown
		if dimension == domain.DimensionGoal || dimension == domain.DimensionContextAccess {
			status = domain.DimensionBlockingUnknown
		}
		dimensions = append(dimensions, contractsv1.ReadinessDimensionState{Dimension: string(dimension), Status: string(status), Summary: "needs review", EvidenceRefs: []string{"turn/user-1"}})
	}
	return contractsv1.AssistantDecision{SchemaVersion: contractsv1.AssistantDecisionSchemaVersion, DecisionType: "clarification",
		UserVisibleContent: "model question", ClarificationQuestion: "model question",
		Readiness: contractsv1.AssistantReadinessAssessment{SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion,
			AssessmentID: "model-controlled", Revision: 99, PolicyRevision: "assistant-readiness-v1", Dimensions: dimensions,
			BlockingUnknowns: []string{"goal", "context_access"}, Assumptions: []string{}, ClarificationQuestions: 3, Status: "clarifying", EvaluatedAt: now}}
}

func processorReadyDecision(now time.Time) contractsv1.AssistantDecision {
	dimensions := make([]contractsv1.ReadinessDimensionState, 0, 7)
	for _, dimension := range domain.RequiredReadinessDimensions() {
		dimensions = append(dimensions, contractsv1.ReadinessDimensionState{Dimension: string(dimension), Status: "known", Summary: "known", EvidenceRefs: []string{"turn/user-1"}})
	}
	return contractsv1.AssistantDecision{SchemaVersion: contractsv1.AssistantDecisionSchemaVersion, DecisionType: "ready_draft", UserVisibleContent: "Ready.",
		Readiness: contractsv1.AssistantReadinessAssessment{SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion, AssessmentID: "model-assessment", Revision: 1,
			PolicyRevision: "assistant-readiness-v1", Dimensions: dimensions, BlockingUnknowns: []string{}, Assumptions: []string{}, Status: "ready_confirmed", EvaluatedAt: now},
		TaskDraft: &contractsv1.AssistantTaskDraft{SchemaVersion: contractsv1.TaskDraftSchemaVersion, TaskDraftID: "model-draft", Revision: 1,
			InitiatorUserID: "model-user", ConversationID: "model-conversation", WorkspaceID: "model-workspace", WorkspaceRevisionID: "model-revision",
			ReadinessAssessmentID: "model-assessment", TaskSpecDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Title: "Implement feature", Goal: "Implement the requested feature", Target: json.RawMessage(`{"kind":"repository","ref":"repo/local"}`),
			Constraints: []string{}, AcceptanceCriteria: []string{"tests pass"}, Assumptions: []string{}, TaskSpec: json.RawMessage(`{"schema_version":"task.spec.v1"}`), CreatedAt: now}}
}
