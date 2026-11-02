package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
)

type StartWorkerRunCommand struct {
	TenantID                 string
	UserID                   string
	WorkerSessionID          string
	TaskID                   string
	PermitID                 string
	ExpectedSessionRevision  uint64
	RuntimeGeneration        uint64
	TriggerType              string
	StartSnapshot            json.RawMessage
	AgentProfileID           string
	AgentProfileRevision     string
	AgentProfileDigest       string
	AgentTurnContent         json.RawMessage
	TurnIdempotencyKey       string
	RuntimeImageRef          string
	ResourceProfileID        string
	NetworkPolicyID          string
	NetworkPolicyRevision    uint64
	NetworkPolicyDigest      string
	NetworkEnforcementStatus string
	ControlChannelEpoch      uint64
}

type StartWorkerRunRecord struct {
	Command            StartWorkerRunCommand
	WorkerRunID        string
	RuntimeInstanceID  string
	AgentSessionID     string
	AgentTurnID        string
	ScheduledCommandID string
	TaskTransitionID   string
	EventID            string
	Now                time.Time
}

type StartedWorkerRun struct {
	// Created is local transaction outcome metadata, never persisted or sent on
	// the wire. Only the transaction that inserts ownership may set it. A
	// historical result (including after response loss) cannot repeat effects.
	Created             bool `json:"-"`
	WorkerRunID         string
	WorkerSessionID     string
	TaskID              string
	RuntimeInstanceID   string
	AgentSessionID      string
	AgentTurnID         string
	RuntimeGeneration   uint64
	ControlChannelEpoch uint64
	Status              string
	RunRevision         uint64
	SessionRevision     uint64
}

type StartWorkerRunStore interface {
	StartWorkerRun(context.Context, StartWorkerRunRecord) (StartedWorkerRun, error)
}

type StartWorkerRunService struct {
	Store StartWorkerRunStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service StartWorkerRunService) Execute(
	ctx context.Context,
	command StartWorkerRunCommand,
) (StartedWorkerRun, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return StartedWorkerRun{}, fmt.Errorf("StartWorkerRun dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "user_id": command.UserID,
		"worker_session_id": command.WorkerSessionID, "task_id": command.TaskID,
		"permit_id": command.PermitID, "agent_profile_id": command.AgentProfileID,
		"agent_profile_revision": command.AgentProfileRevision,
		"agent_profile_digest":   command.AgentProfileDigest,
		"turn_idempotency_key":   command.TurnIdempotencyKey,
		"runtime_image_ref":      command.RuntimeImageRef,
		"resource_profile_id":    command.ResourceProfileID,
		"network_policy_id":      command.NetworkPolicyID,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return StartedWorkerRun{}, fmt.Errorf("%s must be canonical", name)
		}
	}
	if command.ExpectedSessionRevision == 0 || command.RuntimeGeneration == 0 ||
		command.ControlChannelEpoch == 0 || command.TriggerType != "task_initial" {
		return StartedWorkerRun{}, fmt.Errorf("StartWorkerRun revisions and trigger are invalid")
	}
	var snapshot map[string]any
	if err := json.Unmarshal(command.StartSnapshot, &snapshot); err != nil || snapshot == nil {
		return StartedWorkerRun{}, fmt.Errorf("start snapshot must be a JSON object")
	}
	networkEvidence, err := config.ProductionRuntimeNetworkProfiles().ResolveStored(
		config.RuntimeNetworkEvidence{
			ID:                command.NetworkPolicyID,
			Revision:          command.NetworkPolicyRevision,
			Digest:            command.NetworkPolicyDigest,
			EnforcementStatus: command.NetworkEnforcementStatus,
		},
	)
	if err != nil {
		return StartedWorkerRun{}, fmt.Errorf("runtime network evidence is invalid: %w", err)
	}
	if command.NetworkEnforcementStatus != networkEvidence.EnforcementStatus {
		return StartedWorkerRun{}, fmt.Errorf("runtime network enforcement status is required")
	}
	snapshot["runtime_network_profile"] = networkEvidence
	command.StartSnapshot, err = json.Marshal(snapshot)
	if err != nil {
		return StartedWorkerRun{}, fmt.Errorf("encode start snapshot: %w", err)
	}
	var turnContent map[string]any
	if err := json.Unmarshal(command.AgentTurnContent, &turnContent); err != nil || turnContent == nil {
		return StartedWorkerRun{}, fmt.Errorf("AgentTurn content must be a JSON object")
	}
	runID, err := service.IDs.NewID("worker-run")
	if err != nil {
		return StartedWorkerRun{}, err
	}
	runtimeID, err := service.IDs.NewID("runtime-instance")
	if err != nil {
		return StartedWorkerRun{}, err
	}
	agentSessionID, err := service.IDs.NewID("agent-session")
	if err != nil {
		return StartedWorkerRun{}, err
	}
	agentTurnID, err := service.IDs.NewID("agent-turn")
	if err != nil {
		return StartedWorkerRun{}, err
	}
	scheduledCommandID, err := service.IDs.NewID("scheduled-command-turn")
	if err != nil {
		return StartedWorkerRun{}, err
	}
	taskTransitionID, err := service.IDs.NewID("task-transition")
	if err != nil {
		return StartedWorkerRun{}, err
	}
	eventID, err := service.IDs.NewID("integration-event")
	if err != nil {
		return StartedWorkerRun{}, err
	}
	return service.Store.StartWorkerRun(ctx, StartWorkerRunRecord{
		Command: command, WorkerRunID: runID, RuntimeInstanceID: runtimeID,
		AgentSessionID: agentSessionID, AgentTurnID: agentTurnID,
		ScheduledCommandID: scheduledCommandID, TaskTransitionID: taskTransitionID,
		EventID: eventID, Now: service.Now().UTC(),
	})
}
