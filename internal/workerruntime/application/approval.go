package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrApprovalDecisionIdempotencyConflict = errors.New("Approval decision idempotency content conflict")

type DecideApprovalCommand struct {
	TenantID                 string
	UserID                   string
	WorkerSessionID          string
	WorkerRunID              string
	AgentTurnID              string
	ApprovalRequestID        string
	Capability               string
	Target                   string
	Effect                   string
	Decision                 string
	Scope                    string
	ExpectedSessionRevision  uint64
	ExpectedRunRevision      uint64
	ExpectedTurnRevision     uint64
	ExpectedApprovalRevision uint64
	IdempotencyKey           string
}

type ApprovalDecisionRecord struct {
	Command       DecideApprovalCommand
	CommandID     string
	GrantID       string
	EventID       string
	RequestDigest string
	Now           time.Time
}

type ApprovalDecisionResult struct {
	CommandID string
	Status    string
	GrantID   string
}

type ApprovalDecisionStore interface {
	DecideApproval(context.Context, ApprovalDecisionRecord) (ApprovalDecisionResult, error)
}

type ApprovalDecisionService struct {
	Store ApprovalDecisionStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service ApprovalDecisionService) Execute(
	ctx context.Context,
	command DecideApprovalCommand,
) (ApprovalDecisionResult, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return ApprovalDecisionResult{}, fmt.Errorf("Approval decision dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "user_id": command.UserID,
		"worker_session_id": command.WorkerSessionID, "worker_run_id": command.WorkerRunID,
		"agent_turn_id": command.AgentTurnID, "approval_request_id": command.ApprovalRequestID,
		"capability": command.Capability, "target": command.Target, "effect": command.Effect,
		"idempotency_key": command.IdempotencyKey,
	} {
		if err := validateCanonical(name, value); err != nil {
			return ApprovalDecisionResult{}, err
		}
	}
	if command.Decision != "approve" && command.Decision != "deny" ||
		command.Scope != "once" && command.Scope != "session" ||
		!allRevisions(command.ExpectedSessionRevision, command.ExpectedRunRevision,
			command.ExpectedTurnRevision, command.ExpectedApprovalRevision) {
		return ApprovalDecisionResult{}, fmt.Errorf("Approval decision or revisions are invalid")
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	digest := sha256.Sum256(encoded)
	commandID, err := service.IDs.NewID("worker-approval-command")
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	grantID, err := service.IDs.NewID("worker-approval-grant")
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	eventID, err := service.IDs.NewID("integration-event-worker-approval")
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	return service.Store.DecideApproval(ctx, ApprovalDecisionRecord{
		Command: command, CommandID: commandID, GrantID: grantID, EventID: eventID,
		RequestDigest: "sha256:" + hex.EncodeToString(digest[:]), Now: service.Now().UTC(),
	})
}
