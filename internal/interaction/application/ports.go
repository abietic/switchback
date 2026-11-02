// Package application orchestrates Assistant interaction use cases.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

var (
	ErrConversationUnavailable = errors.New("Conversation is unavailable")
	ErrIdempotencyConflict     = errors.New("idempotency key content conflict")
	ErrNoQueuedRun             = errors.New("no AssistantRun is ready to claim")
	ErrLeaseLost               = errors.New("AssistantRun lease is stale")
	ErrModelTimeout            = errors.New("Assistant model request timed out")
	ErrModelMalformed          = errors.New("Assistant model response is malformed")
	ErrModelPolicyConflict     = errors.New("Assistant model decision conflicts with policy")
	ErrModelUpstream           = errors.New("Assistant model upstream failed")
)

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID(kind string) (string, error)
}

type IdempotencyResult struct {
	RequestDigest string
	ResourceType  string
	ResourceID    string
	Status        int
	Response      json.RawMessage
}

type Transaction interface {
	LoadConversation(context.Context, string, string, string, bool) (domain.Conversation, error)
	InsertConversation(context.Context, domain.Conversation) error
	UpdateConversation(context.Context, domain.Conversation, uint64) error
	InsertTurn(context.Context, domain.Turn) error
	InsertRun(context.Context, domain.AssistantRun, string, string) error
	InsertOutboxEvent(context.Context, contractsv1.IntegrationEvent) error
	FindIdempotencyResult(context.Context, string, string, string, string) (IdempotencyResult, bool, error)
	InsertIdempotencyResult(context.Context, string, string, string, string, IdempotencyResult, time.Time) error
}

type TransactionManager interface {
	WithinTransaction(context.Context, func(Transaction) error) error
}

type RunClaim struct {
	Conversation         domain.Conversation
	Run                  domain.AssistantRun
	Turns                []domain.Turn
	ModelProfileRevision uint64
	ModelProfileDigest   string
}

type AssistantRunRepository interface {
	ClaimNextRun(context.Context, string, time.Duration, time.Time) (RunClaim, error)
	LoadRun(context.Context, string) (domain.AssistantRun, error)
	MarkInvocationStarted(context.Context, string, uint64, uint64, time.Time) (uint64, error)
	CommitDecision(context.Context, RunDecisionCommit) error
	CommitFailure(context.Context, RunFailureCommit) error
	RequeueExpired(context.Context, string, uint64, uint64, time.Time) error
	RecoverExpiredClaims(context.Context, time.Time) (RunRecoveryResult, error)
}

type RecoveredRunFailure struct {
	TenantID               string
	UserID                 string
	WorkspaceID            string
	ConversationID         string
	ConversationGeneration uint64
	AssistantRunID         string
	PolicyRevision         string
	ErrorCode              string
}

type RunRecoveryResult struct {
	Count    int
	Failures []RecoveredRunFailure
}

type RunDecisionCommit struct {
	ConversationID               string
	ExpectedConversationRevision uint64
	RunID                        string
	ExpectedRunRevision          uint64
	FencingToken                 uint64
	AssistantTurn                domain.Turn
	Decision                     contractsv1.AssistantDecision
	Readiness                    ReadinessRecord
	TaskDraft                    *TaskDraftRecord
	Checkpoint                   *domain.ClarificationCheckpoint
	Event                        contractsv1.IntegrationEvent
	OccurredAt                   time.Time
}

// RunFailureCommit is the fenced terminal record for a run that produced no
// authoritative Assistant decision. The error code is deliberately stable and
// sanitized because it is surfaced through the user projection.
type RunFailureCommit struct {
	ConversationID               string
	ExpectedConversationRevision uint64
	RunID                        string
	ExpectedRunRevision          uint64
	FencingToken                 uint64
	ErrorCode                    string
	Retryable                    bool
	Event                        contractsv1.IntegrationEvent
	OccurredAt                   time.Time
}

type ContextItem struct {
	Ref             string          `json:"ref"`
	Kind            string          `json:"kind"`
	Priority        int             `json:"priority"`
	EstimatedTokens int             `json:"estimated_tokens"`
	Content         json.RawMessage `json:"content"`
}

type DroppedContextItem struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type ContextPack struct {
	SchemaVersion   string               `json:"schema_version"`
	ContextPackID   string               `json:"context_pack_id"`
	AssistantRunID  string               `json:"assistant_run_id"`
	ConversationID  string               `json:"conversation_id"`
	PolicyRevision  string               `json:"policy_revision"`
	Selected        []ContextItem        `json:"selected"`
	Dropped         []DroppedContextItem `json:"dropped"`
	EstimatedTokens int                  `json:"estimated_tokens"`
	BudgetTokens    int                  `json:"budget_tokens"`
	Digest          string               `json:"digest"`
}

type ContextRepository interface {
	LoadContextCandidates(context.Context, RunClaim) ([]ContextItem, error)
	SaveContextPack(context.Context, ContextPack) error
}

type ReadinessRecord struct {
	Assessment domain.TaskReadinessAssessment
	Workspace  *domain.WorkspaceResolution
	Digest     string
}

type ReadinessRepository interface {
	SaveReadiness(context.Context, ReadinessRecord) error
}

type CheckpointRepository interface {
	SaveCheckpoint(context.Context, domain.ClarificationCheckpoint) error
	ApplyCheckpointAction(context.Context, string, string, uint64, domain.CheckpointAction, time.Time) (domain.ClarificationCheckpoint, error)
}

type TaskDraftRecord struct {
	Draft    domain.TaskDraft
	TaskSpec contractsv1.TaskSpec
}

type TaskDraftRepository interface {
	ReplaceCurrentDraft(context.Context, string, TaskDraftRecord, time.Time) error
}

// CurrentTaskDraftReader keeps draft lineage reconstruction inside the
// persistence boundary. A nil record means the Conversation has no current
// draft; it is not an error.
type CurrentTaskDraftReader interface {
	LoadCurrentTaskDraft(context.Context, string) (*TaskDraftRecord, error)
}

type ModelRequest struct {
	RunID           string
	ConversationID  string
	ProfileID       string
	ProfileRevision uint64
	ProfileDigest   string
	PolicyRevision  string
	Context         ContextPack
}

type ModelResult struct {
	Decision          contractsv1.AssistantDecision
	ProviderRequestID string
	Model             string
	InputTokens       int64
	OutputTokens      int64
	StartedAt         time.Time
	CompletedAt       time.Time
}

type AssistantModel interface {
	Complete(context.Context, ModelRequest) (ModelResult, error)
}

type EvidenceBoundary struct {
	TenantID               string
	UserID                 string
	WorkspaceID            string
	ConversationID         string
	ConversationGeneration uint64
	AssistantRunID         string
	BoundaryType           string
	Records                []json.RawMessage
	Metadata               map[string]string
}

type EvidenceResult struct {
	ObjectKey   string
	ManifestKey string
	Checksum    string
	ByteSize    int64
}

type ObjectStore interface {
	WriteConversationEvidence(context.Context, EvidenceBoundary) (EvidenceResult, error)
}

type AssistantTaskRequest struct {
	Actor                      taskapp.Actor
	TaskSpec                   contractsv1.TaskSpec
	AcceptanceMode             string
	IdempotencyKey             string
	AssistantIntent            contractsv1.AssistantCreationIntent
	Readiness                  domain.TaskReadinessAssessment
	AssistantReadinessRevision uint64
}

type TaskAdmission interface {
	CreateAssistantTask(context.Context, AssistantTaskRequest) (taskapp.CreatedTask, error)
}

type ConfirmationStateRepository interface {
	LoadConfirmationState(context.Context, ConfirmTaskDraftCommand) (ConfirmationState, error)
	FindConfirmationResult(context.Context, ConfirmTaskDraftCommand) (ConfirmTaskDraftResult, bool, error)
}

type ProjectionUpdate struct {
	Event contractsv1.IntegrationEvent
}

type ProjectionPort interface {
	PublishAssistantProjection(context.Context, ProjectionUpdate) error
}

type ModelObservation struct {
	TenantID          string
	UserID            string
	AssistantRunID    string
	ConversationID    string
	ProfileID         string
	ProfileRevision   uint64
	ProfileDigest     string
	ProviderRequestID string
	Model             string
	InputTokens       int64
	OutputTokens      int64
	StartedAt         time.Time
	CompletedAt       time.Time
	Status            string
	ErrorCode         string
}

type ObservabilityPort interface {
	RecordAssistantModel(context.Context, ModelObservation) error
}

type DerivedEffectRecord struct {
	AssistantRunID         string
	EvidenceStatus         string
	EvidenceErrorCode      string
	EvidenceObjectKey      string
	ObservabilityStatus    string
	ObservabilityErrorCode string
	LangfuseTraceID        string
	RecordedAt             time.Time
}

type DerivedEffectStore interface {
	RecordAssistantDerivedEffects(context.Context, DerivedEffectRecord) error
}
