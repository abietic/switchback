package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/domain"
)

type ActivateResidentRuntimeCommand struct {
	TenantID                string
	WorkerSessionID         string
	WorkerRunID             string
	AgentTurnID             string
	RuntimeInstanceID       string
	ContainerID             string
	RuntimeGeneration       uint64
	ControlChannelEpoch     uint64
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	ExpectedRuntimeRevision uint64
}

type ActivateResidentRuntimeRecord struct {
	Command ActivateResidentRuntimeCommand
	Now     time.Time
}

type CompleteResidentStageCommand struct {
	TenantID                string
	WorkerSessionID         string
	WorkerRunID             string
	AgentTurnID             string
	RuntimeInstanceID       string
	RuntimeGeneration       uint64
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	ExpectedRuntimeRevision uint64
	SessionTarget           domain.WorkerSessionStatus
	RunTarget               domain.WorkerRunStatus
	TurnTarget              domain.AgentTurnStatus
	TerminalReason          string
}

type CompleteResidentStageRecord struct {
	Command CompleteResidentStageCommand
	Now     time.Time
}

type ResidentRuntimeState struct {
	WorkerSessionID   string
	WorkerRunID       string
	AgentTurnID       string
	RuntimeInstanceID string
	ContainerID       string
	RuntimeGeneration uint64
	SessionStatus     domain.WorkerSessionStatus
	RunStatus         domain.WorkerRunStatus
	TurnStatus        domain.AgentTurnStatus
	RuntimeStatus     string
	SessionRevision   uint64
	RunRevision       uint64
	TurnRevision      uint64
	RuntimeRevision   uint64
}

type RuntimeResidencyStore interface {
	ActivateResidentRuntime(context.Context, ActivateResidentRuntimeRecord) (ResidentRuntimeState, error)
	CompleteResidentStage(context.Context, CompleteResidentStageRecord) (ResidentRuntimeState, error)
}

type RuntimeResidencyService struct {
	Store RuntimeResidencyStore
	Now   func() time.Time
}

func (service RuntimeResidencyService) Activate(
	ctx context.Context,
	command ActivateResidentRuntimeCommand,
) (ResidentRuntimeState, error) {
	if service.Store == nil || service.Now == nil {
		return ResidentRuntimeState{}, fmt.Errorf("RuntimeResidency dependencies are required")
	}
	if err := validateResidentIdentity(
		command.TenantID, command.WorkerSessionID, command.WorkerRunID,
		command.AgentTurnID, command.RuntimeInstanceID,
	); err != nil {
		return ResidentRuntimeState{}, err
	}
	if err := validateCanonical("container_id", command.ContainerID); err != nil {
		return ResidentRuntimeState{}, err
	}
	if command.RuntimeGeneration == 0 || command.ControlChannelEpoch == 0 ||
		!allRevisions(command.ExpectedSessionRevision, command.ExpectedRunRevision,
			command.ExpectedTurnRevision, command.ExpectedRuntimeRevision) {
		return ResidentRuntimeState{}, fmt.Errorf("Runtime activation requires a positive generation, channel epoch, and exact revisions")
	}
	return service.Store.ActivateResidentRuntime(ctx, ActivateResidentRuntimeRecord{
		Command: command,
		Now:     service.Now().UTC(),
	})
}

func (service RuntimeResidencyService) CompleteStage(
	ctx context.Context,
	command CompleteResidentStageCommand,
) (ResidentRuntimeState, error) {
	if service.Store == nil || service.Now == nil {
		return ResidentRuntimeState{}, fmt.Errorf("RuntimeResidency dependencies are required")
	}
	if err := validateCompleteResidentStageCommand(command); err != nil {
		return ResidentRuntimeState{}, err
	}
	return service.Store.CompleteResidentStage(ctx, CompleteResidentStageRecord{
		Command: command,
		Now:     service.Now().UTC(),
	})
}

func validateCompleteResidentStageCommand(
	command CompleteResidentStageCommand,
) error {
	if err := validateResidentIdentity(
		command.TenantID, command.WorkerSessionID, command.WorkerRunID,
		command.AgentTurnID, command.RuntimeInstanceID,
	); err != nil {
		return err
	}
	if command.RuntimeGeneration == 0 ||
		!allRevisions(
			command.ExpectedSessionRevision,
			command.ExpectedRunRevision,
			command.ExpectedTurnRevision,
			command.ExpectedRuntimeRevision,
		) {
		return fmt.Errorf(
			"Runtime stage completion requires a positive generation and exact revisions",
		)
	}
	if !command.TurnTarget.IsTerminal() {
		return fmt.Errorf("stage completion requires a terminal AgentTurn")
	}
	if command.RunTarget == domain.RunWaitingUser {
		if command.SessionTarget != domain.SessionWaitingUser {
			return fmt.Errorf(
				"waiting WorkerRun requires waiting_user WorkerSession",
			)
		}
	} else if !command.RunTarget.IsTerminal() {
		return fmt.Errorf(
			"stage completion requires waiting_user or terminal WorkerRun",
		)
	}
	if command.SessionTarget != domain.SessionInteractive &&
		command.SessionTarget != domain.SessionWaitingUser &&
		command.SessionTarget != domain.SessionIdle {
		return fmt.Errorf(
			"stage completion target must keep the WorkerSession resident",
		)
	}
	return nil
}

func validateResidentIdentity(values ...string) error {
	names := []string{"tenant_id", "worker_session_id", "worker_run_id", "agent_turn_id", "runtime_instance_id"}
	for index, value := range values {
		if err := validateCanonical(names[index], value); err != nil {
			return err
		}
	}
	return nil
}

func validateCanonical(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s must be canonical", name)
	}
	return nil
}

func allRevisions(revisions ...uint64) bool {
	for _, revision := range revisions {
		if revision == 0 {
			return false
		}
	}
	return true
}
