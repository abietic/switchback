package application

import (
	"context"
	"fmt"
	"time"
)

type BindAgentSessionCommand struct {
	TenantID                string
	WorkerSessionID         string
	WorkerRunID             string
	AgentTurnID             string
	RuntimeInstanceID       string
	ReservedAgentSessionID  string
	ActualAgentSessionID    string
	RuntimeGeneration       uint64
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	ExpectedRuntimeRevision uint64
	IdempotencyKey          string
}

type BindAgentSessionRecord struct {
	Command BindAgentSessionCommand
	EventID string
	Now     time.Time
}

type BoundAgentSession struct {
	AgentSessionID  string
	SessionRevision uint64
	RunRevision     uint64
	TurnRevision    uint64
	RuntimeRevision uint64
	EventID         string
}

type AgentSessionBindingStore interface {
	BindAgentSession(context.Context, BindAgentSessionRecord) (BoundAgentSession, error)
}

// AgentSessionBindingService replaces the pre-provisioning reservation with
// the real ACP session identity before the first prompt is dispatched.
type AgentSessionBindingService struct {
	Store AgentSessionBindingStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service AgentSessionBindingService) Execute(
	ctx context.Context,
	command BindAgentSessionCommand,
) (BoundAgentSession, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return BoundAgentSession{}, fmt.Errorf("AgentSession binding dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "worker_session_id": command.WorkerSessionID,
		"worker_run_id": command.WorkerRunID, "agent_turn_id": command.AgentTurnID,
		"runtime_instance_id":       command.RuntimeInstanceID,
		"reserved_agent_session_id": command.ReservedAgentSessionID,
		"actual_agent_session_id":   command.ActualAgentSessionID,
		"idempotency_key":           command.IdempotencyKey,
	} {
		if err := validateCanonical(name, value); err != nil {
			return BoundAgentSession{}, err
		}
	}
	if command.RuntimeGeneration == 0 ||
		!allRevisions(command.ExpectedSessionRevision, command.ExpectedRunRevision,
			command.ExpectedTurnRevision, command.ExpectedRuntimeRevision) {
		return BoundAgentSession{}, fmt.Errorf("AgentSession binding requires a positive generation and exact revisions")
	}
	eventID, err := service.IDs.NewID("integration-event-agent-session-bound")
	if err != nil {
		return BoundAgentSession{}, err
	}
	return service.Store.BindAgentSession(ctx, BindAgentSessionRecord{
		Command: command, EventID: eventID, Now: service.Now().UTC(),
	})
}
