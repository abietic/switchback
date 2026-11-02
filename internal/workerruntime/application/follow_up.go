package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type StartSessionFollowUpCommand struct {
	TenantID                string
	UserID                  string
	WorkerSessionID         string
	ExpectedSessionRevision uint64
	PreviousWorkerRunID     string
	ExpectedRunRevision     uint64
	PreviousAgentTurnID     string
	ExpectedTurnRevision    uint64
	Content                 string
	IdempotencyKey          string
}

type StartSessionFollowUpRecord struct {
	Command            StartSessionFollowUpCommand
	WorkerRunID        string
	AgentTurnID        string
	ScheduledCommandID string
	EventID            string
	RequestDigest      string
	Now                time.Time
}

type StartedSessionFollowUp struct {
	WorkerSessionID   string `json:"worker_session_id"`
	WorkerRunID       string `json:"worker_run_id"`
	AgentTurnID       string `json:"agent_turn_id"`
	RuntimeInstanceID string `json:"runtime_instance_id"`
	SessionRevision   uint64 `json:"session_revision"`
	RunRevision       uint64 `json:"run_revision"`
	TurnRevision      uint64 `json:"turn_revision"`
	RuntimeRevision   uint64 `json:"runtime_revision"`
	EventID           string `json:"event_id"`
}

type SessionFollowUpStore interface {
	StartSessionFollowUp(context.Context, StartSessionFollowUpRecord) (StartedSessionFollowUp, error)
}

type SessionFollowUpService struct {
	Store SessionFollowUpStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service SessionFollowUpService) Execute(ctx context.Context, command StartSessionFollowUpCommand) (StartedSessionFollowUp, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return StartedSessionFollowUp{}, fmt.Errorf("Session follow-up dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "user_id": command.UserID,
		"worker_session_id": command.WorkerSessionID, "previous_worker_run_id": command.PreviousWorkerRunID,
		"previous_agent_turn_id": command.PreviousAgentTurnID, "idempotency_key": command.IdempotencyKey,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return StartedSessionFollowUp{}, fmt.Errorf("%s must be canonical", name)
		}
	}
	if strings.TrimSpace(command.Content) == "" || strings.ContainsRune(command.Content, '\x00') ||
		command.ExpectedSessionRevision == 0 || command.ExpectedRunRevision == 0 || command.ExpectedTurnRevision == 0 {
		return StartedSessionFollowUp{}, fmt.Errorf("Session follow-up requires content and exact revisions")
	}
	request, err := json.Marshal(command)
	if err != nil {
		return StartedSessionFollowUp{}, err
	}
	digest := sha256.Sum256(request)
	values := make([]string, 4)
	for index, kind := range []string{"worker-run", "agent-turn", "scheduled-command-turn", "integration-event"} {
		values[index], err = service.IDs.NewID(kind)
		if err != nil {
			return StartedSessionFollowUp{}, err
		}
	}
	return service.Store.StartSessionFollowUp(ctx, StartSessionFollowUpRecord{
		Command: command, WorkerRunID: values[0], AgentTurnID: values[1],
		ScheduledCommandID: values[2], EventID: values[3],
		RequestDigest: "sha256:" + hex.EncodeToString(digest[:]), Now: service.Now().UTC(),
	})
}
