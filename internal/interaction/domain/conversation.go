// Package domain owns the Assistant conversation and dispatch invariants.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrStaleRevision = errors.New("stale aggregate revision")
	ErrTerminal      = errors.New("aggregate is terminal")
	ErrRunActive     = errors.New("an AssistantRun is already active")
	ErrInvalidState  = errors.New("invalid state transition")
)

type ConversationStatus string

const (
	ConversationActive ConversationStatus = "active"
	ConversationClosed ConversationStatus = "closed"
)

type WorkspaceResolution struct {
	WorkspaceID         string
	WorkspaceRevisionID string
	ResolutionID        string
	ResolutionRevision  uint64
	MembershipRevision  uint64
}

func (resolution WorkspaceResolution) Validate() error {
	for name, value := range map[string]string{
		"workspace id": resolution.WorkspaceID, "workspace revision id": resolution.WorkspaceRevisionID,
		"resolution id": resolution.ResolutionID,
	} {
		if !canonical(value) {
			return fmt.Errorf("%s is required", name)
		}
	}
	if resolution.ResolutionRevision == 0 || resolution.MembershipRevision == 0 {
		return fmt.Errorf("Workspace resolution revisions must be positive")
	}
	return nil
}

type Conversation struct {
	ID                           string
	TenantID                     string
	ActorUserID                  string
	Generation                   uint64
	Status                       ConversationStatus
	Workspace                    *WorkspaceResolution
	NextTurnSequence             uint64
	NextRunSequence              uint64
	Revision                     uint64
	CurrentTaskDraftID           string
	CurrentReadinessAssessmentID string
	CurrentCheckpointID          string
	ActiveAssistantRunID         string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
	TerminalAt                   time.Time
	TerminalReason               string
}

func NewConversation(id, tenantID, actorUserID string, now time.Time) (Conversation, error) {
	if !canonical(id) || !canonical(tenantID) || !canonical(actorUserID) || now.IsZero() {
		return Conversation{}, fmt.Errorf("Conversation identity and creation time are required")
	}
	return Conversation{
		ID: id, TenantID: tenantID, ActorUserID: actorUserID, Generation: 1,
		Status: ConversationActive, NextTurnSequence: 1, NextRunSequence: 1,
		Revision: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}, nil
}

func (conversation *Conversation) SelectWorkspace(expectedRevision uint64, resolution WorkspaceResolution, now time.Time) error {
	if err := conversation.requireMutable(expectedRevision, now); err != nil {
		return err
	}
	if err := resolution.Validate(); err != nil {
		return err
	}
	copy := resolution
	conversation.Workspace = &copy
	conversation.invalidateDerivedState()
	conversation.advance(now)
	return nil
}

type AcceptedMessage struct {
	Turn Turn
	Run  AssistantRun
}

func (conversation *Conversation) AcceptMessage(
	expectedRevision uint64,
	turnID, runID, correlationID, content, modelProfile, policyRevision string,
	contextBudgetTokens int,
	now, timeoutAt time.Time,
) (AcceptedMessage, error) {
	if err := conversation.requireMutable(expectedRevision, now); err != nil {
		return AcceptedMessage{}, err
	}
	for name, value := range map[string]string{
		"Turn id": turnID, "AssistantRun id": runID, "correlation id": correlationID,
		"message content": content, "model profile": modelProfile, "policy revision": policyRevision,
	} {
		if !canonical(value) {
			return AcceptedMessage{}, fmt.Errorf("%s is required", name)
		}
	}
	if contextBudgetTokens <= 0 || !timeoutAt.After(now) {
		return AcceptedMessage{}, fmt.Errorf("AssistantRun budget and timeout are invalid")
	}
	turn := Turn{
		ID: turnID, ConversationID: conversation.ID, Sequence: conversation.NextTurnSequence,
		Role: TurnUser, Content: content, Visible: true, CorrelationID: correlationID, CreatedAt: now.UTC(),
	}
	run := AssistantRun{
		ID: runID, ConversationID: conversation.ID, SourceTurnID: turnID,
		Sequence: conversation.NextRunSequence, Status: RunQueued, ModelProfile: modelProfile,
		PolicyRevision: policyRevision, ContextBudgetTokens: contextBudgetTokens,
		TimeoutAt: timeoutAt.UTC(), Revision: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	conversation.NextTurnSequence++
	conversation.NextRunSequence++
	conversation.invalidateDerivedState()
	conversation.advance(now)
	return AcceptedMessage{Turn: turn, Run: run}, nil
}

func (conversation *Conversation) ClaimRun(expectedRevision uint64, runID string, now time.Time) error {
	if err := conversation.requireMutable(expectedRevision, now); err != nil {
		return err
	}
	if !canonical(runID) {
		return fmt.Errorf("AssistantRun id is required")
	}
	if conversation.ActiveAssistantRunID != "" {
		return ErrRunActive
	}
	conversation.ActiveAssistantRunID = runID
	conversation.advance(now)
	return nil
}

func (conversation *Conversation) ReleaseRun(expectedRevision uint64, runID string, now time.Time) error {
	if err := conversation.requireMutable(expectedRevision, now); err != nil {
		return err
	}
	if conversation.ActiveAssistantRunID != runID || !canonical(runID) {
		return ErrInvalidState
	}
	conversation.ActiveAssistantRunID = ""
	conversation.advance(now)
	return nil
}

func (conversation *Conversation) BindDerivedState(
	expectedRevision uint64,
	taskDraftID, readinessAssessmentID, checkpointID string,
	now time.Time,
) error {
	if err := conversation.requireMutable(expectedRevision, now); err != nil {
		return err
	}
	if !canonical(readinessAssessmentID) {
		return fmt.Errorf("readiness assessment id is required")
	}
	for _, optional := range []string{taskDraftID, checkpointID} {
		if optional != "" && !canonical(optional) {
			return fmt.Errorf("derived state identity is invalid")
		}
	}
	conversation.CurrentTaskDraftID = taskDraftID
	conversation.CurrentReadinessAssessmentID = readinessAssessmentID
	conversation.CurrentCheckpointID = checkpointID
	conversation.advance(now)
	return nil
}

func (conversation *Conversation) CommitAssistantDecision(
	expectedRevision uint64,
	runID, turnID, correlationID, content, taskDraftID, readinessAssessmentID, checkpointID string,
	now time.Time,
) (Turn, error) {
	if err := conversation.requireMutable(expectedRevision, now); err != nil {
		return Turn{}, err
	}
	for name, value := range map[string]string{
		"AssistantRun id": runID, "Turn id": turnID, "correlation id": correlationID,
		"Assistant content": content, "readiness assessment id": readinessAssessmentID,
	} {
		if !canonical(value) {
			return Turn{}, fmt.Errorf("%s is required", name)
		}
	}
	for _, optional := range []string{taskDraftID, checkpointID} {
		if optional != "" && !canonical(optional) {
			return Turn{}, fmt.Errorf("derived state identity is invalid")
		}
	}
	if conversation.ActiveAssistantRunID != runID {
		return Turn{}, ErrInvalidState
	}
	turn := Turn{
		ID: turnID, ConversationID: conversation.ID, Sequence: conversation.NextTurnSequence,
		Role: TurnAssistant, Content: content, Visible: true, SourceAssistantRunID: runID,
		CorrelationID: correlationID, CreatedAt: now.UTC(),
	}
	conversation.NextTurnSequence++
	conversation.ActiveAssistantRunID = ""
	conversation.CurrentTaskDraftID = taskDraftID
	conversation.CurrentReadinessAssessmentID = readinessAssessmentID
	conversation.CurrentCheckpointID = checkpointID
	conversation.advance(now)
	return turn, nil
}

func (conversation *Conversation) Close(expectedRevision uint64, reason string, now time.Time) error {
	if err := conversation.requireMutable(expectedRevision, now); err != nil {
		return err
	}
	if !canonical(reason) || conversation.ActiveAssistantRunID != "" {
		return ErrInvalidState
	}
	conversation.Status = ConversationClosed
	conversation.TerminalReason = reason
	conversation.TerminalAt = now.UTC()
	conversation.advance(now)
	return nil
}

func (conversation *Conversation) requireMutable(expectedRevision uint64, now time.Time) error {
	if conversation.Status != ConversationActive {
		return ErrTerminal
	}
	if conversation.Revision != expectedRevision {
		return ErrStaleRevision
	}
	if now.IsZero() || now.Before(conversation.UpdatedAt) {
		return fmt.Errorf("mutation time is invalid")
	}
	return nil
}

func (conversation *Conversation) invalidateDerivedState() {
	conversation.CurrentTaskDraftID = ""
	conversation.CurrentReadinessAssessmentID = ""
	conversation.CurrentCheckpointID = ""
}

func (conversation *Conversation) advance(now time.Time) {
	conversation.Revision++
	conversation.UpdatedAt = now.UTC()
}

type TurnRole string

const (
	TurnUser      TurnRole = "user"
	TurnAssistant TurnRole = "assistant"
	TurnSystem    TurnRole = "system"
)

type Turn struct {
	ID                   string
	ConversationID       string
	Sequence             uint64
	Role                 TurnRole
	Content              string
	Visible              bool
	SourceAssistantRunID string
	CorrelationID        string
	CreatedAt            time.Time
}

func canonical(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
