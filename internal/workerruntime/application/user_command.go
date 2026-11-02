package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrWorkerUserCommandIdempotencyConflict = errors.New("Worker user command idempotency content conflict")

type SubmitWorkerUserCommand struct {
	TenantID                string
	UserID                  string
	WorkerSessionID         string
	WorkerRunID             string
	AgentTurnID             string
	CommandType             string
	Content                 string
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	IdempotencyKey          string
}

type WorkerUserCommandRecord struct {
	Command       SubmitWorkerUserCommand
	CommandID     string
	EventID       string
	RequestDigest string
	Now           time.Time
}

type AcceptedWorkerUserCommand struct {
	CommandID       string  `json:"command_id"`
	Status          string  `json:"status"`
	QueueSequence   *uint64 `json:"queue_sequence,omitempty"`
	WorkerSessionID string  `json:"worker_session_id"`
}

type WorkerUserCommandStore interface {
	SubmitWorkerUserCommand(context.Context, WorkerUserCommandRecord) (AcceptedWorkerUserCommand, error)
}

type WorkerUserCommandService struct {
	Store WorkerUserCommandStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service WorkerUserCommandService) Execute(
	ctx context.Context,
	command SubmitWorkerUserCommand,
) (AcceptedWorkerUserCommand, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return AcceptedWorkerUserCommand{}, fmt.Errorf("Worker user command dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "user_id": command.UserID,
		"worker_session_id": command.WorkerSessionID, "worker_run_id": command.WorkerRunID,
		"agent_turn_id": command.AgentTurnID, "idempotency_key": command.IdempotencyKey,
	} {
		if err := validateCanonical(name, value); err != nil {
			return AcceptedWorkerUserCommand{}, err
		}
	}
	if command.CommandType != "queue" && command.CommandType != "interrupt" {
		return AcceptedWorkerUserCommand{}, fmt.Errorf("Worker user command type is unsupported")
	}
	if command.CommandType == "queue" && strings.TrimSpace(command.Content) == "" {
		return AcceptedWorkerUserCommand{}, fmt.Errorf("queued Worker message is empty")
	}
	if strings.ContainsRune(command.Content, '\x00') || len(command.Content) > 256<<10 ||
		!allRevisions(command.ExpectedSessionRevision, command.ExpectedRunRevision, command.ExpectedTurnRevision) {
		return AcceptedWorkerUserCommand{}, fmt.Errorf("Worker user command content or revisions are invalid")
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return AcceptedWorkerUserCommand{}, err
	}
	digest := sha256.Sum256(encoded)
	commandID, err := service.IDs.NewID("worker-user-command")
	if err != nil {
		return AcceptedWorkerUserCommand{}, err
	}
	eventID, err := service.IDs.NewID("integration-event-worker-user-command")
	if err != nil {
		return AcceptedWorkerUserCommand{}, err
	}
	return service.Store.SubmitWorkerUserCommand(ctx, WorkerUserCommandRecord{
		Command: command, CommandID: commandID, EventID: eventID,
		RequestDigest: "sha256:" + hex.EncodeToString(digest[:]), Now: service.Now().UTC(),
	})
}
