package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type frozenTurnSettlementDomainPlan struct {
	SessionTarget           string `json:"session_target"`
	RunTarget               string `json:"run_target"`
	TurnTarget              string `json:"turn_target"`
	TerminalReason          string `json:"terminal_reason,omitempty"`
	ExpectedSessionRevision uint64 `json:"expected_session_revision"`
	ExpectedRunRevision     uint64 `json:"expected_run_revision"`
	ExpectedTurnRevision    uint64 `json:"expected_turn_revision"`
	ExpectedRuntimeRevision uint64 `json:"expected_runtime_revision"`
}

func (repository *TurnSettlementRepository) CommitTurnSettlementDomain(
	ctx context.Context,
	record application.CommitTurnSettlementDomainRecord,
) (application.CommitTurnSettlementDomainResult, error) {
	if repository == nil || repository.pool == nil {
		return application.CommitTurnSettlementDomainResult{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	var result application.CommitTurnSettlementDomainResult
	err := inTerminalCapacityTransaction(
		ctx,
		repository.pool,
		func() string { return record.Command.Resident.AgentTurnID },
		func(tx platformpostgres.Tx) error {
			current, err := readTurnSettlement(
				ctx,
				tx,
				record.Command.SettlementID,
				true,
			)
			if err != nil {
				return err
			}
			if err := application.ValidateTurnSettlementCheckpointProof(
				current.Phase,
				current.PhaseState,
			); err != nil {
				return err
			}
			plan, proof, err := decodeTurnSettlementDomainCheckpoint(
				current.PhaseState,
			)
			if err != nil {
				return err
			}
			if err := validateTurnSettlementDomainIdentity(
				current,
				record.Command,
				plan,
			); err != nil {
				return err
			}
			if current.Phase ==
				application.TurnSettlementDomainCommitted {
				if current.Revision !=
					record.Command.ExpectedRevision+1 ||
					current.LeaseOwnerID !=
						record.Command.LeaseOwnerID ||
					current.LeaseFence != record.Command.LeaseFence ||
					proof == nil {
					return application.ErrTurnSettlementRevisionConflict
				}
				resident, err := lockResidentRuntime(
					ctx,
					tx,
					record.Command.Resident.WorkerSessionID,
					record.Command.Resident.WorkerRunID,
					record.Command.Resident.AgentTurnID,
					record.Command.Resident.RuntimeInstanceID,
				)
				if err != nil {
					return err
				}
				state := resident.state()
				if !turnSettlementDomainProofMatchesState(
					*proof,
					state,
					record.Command,
				) {
					return application.ErrTurnSettlementConflict
				}
				if err := validateForcedDeadlineDomainReplay(
					ctx,
					tx,
					current,
					resident,
					record.Command,
					*proof,
				); err != nil {
					return err
				}
				result = application.CommitTurnSettlementDomainResult{
					Settlement: current,
					Resident:   state,
					Proof:      *proof,
				}
				return nil
			}
			if current.Phase !=
				application.TurnSettlementEvidencePrepared {
				return application.ErrTurnSettlementPhaseConflict
			}
			if current.Revision != record.Command.ExpectedRevision {
				return application.ErrTurnSettlementRevisionConflict
			}
			if current.LeaseOwnerID != record.Command.LeaseOwnerID ||
				current.LeaseFence != record.Command.LeaseFence ||
				!current.LeaseExpiresAt.After(record.CommittedAt) {
				return application.ErrTurnSettlementLeaseStale
			}
			if proof != nil {
				return application.ErrTurnSettlementCheckpointConflict
			}
			resident, err := completeTurnSettlementDomainStageTx(
				ctx,
				tx,
				current,
				application.CompleteResidentStageRecord{
					Command: record.Command.Resident,
					Now:     record.CommittedAt,
				},
			)
			if err != nil {
				return err
			}
			proofValue := application.TurnSettlementDomainProof{
				SessionRevision: resident.SessionRevision,
				RunRevision:     resident.RunRevision,
				TurnRevision:    resident.TurnRevision,
				RuntimeRevision: resident.RuntimeRevision,
			}
			checkpoint, checkpointDigest, err :=
				appendTurnSettlementDomainProof(
					current.PhaseState,
					proofValue,
				)
			if err != nil {
				return err
			}
			if err := application.ValidateTurnSettlementCheckpointProof(
				application.TurnSettlementDomainCommitted,
				checkpoint,
			); err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET phase='domain_committed',phase_state=$2,phase_state_digest=$3,
    revision=revision+1,updated_at=$4
WHERE settlement_id=$1 AND revision=$5 AND phase='evidence_prepared'
RETURNING revision`,
				current.SettlementID,
				checkpoint,
				checkpointDigest,
				record.CommittedAt,
				record.Command.ExpectedRevision,
			).Scan(&current.Revision)
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrTurnSettlementRevisionConflict
			}
			if err != nil {
				return fmt.Errorf(
					"commit Turn settlement domain phase: %w",
					err,
				)
			}
			current.Phase =
				application.TurnSettlementDomainCommitted
			current.PhaseState = append([]byte(nil), checkpoint...)
			current.PhaseStateDigest = checkpointDigest
			current.UpdatedAt = record.CommittedAt
			result = application.CommitTurnSettlementDomainResult{
				Settlement: current,
				Resident:   resident,
				Proof:      proofValue,
			}
			return nil
		},
	)
	return result, err
}

func completeTurnSettlementDomainStageTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	record application.CompleteResidentStageRecord,
) (application.ResidentRuntimeState, error) {
	if settlement.Source.Kind ==
		application.TurnSettlementSourceRuntimeEvent {
		row, err := lockResidentRuntime(
			ctx,
			tx,
			record.Command.WorkerSessionID,
			record.Command.WorkerRunID,
			record.Command.AgentTurnID,
			record.Command.RuntimeInstanceID,
		)
		if err != nil {
			return application.ResidentRuntimeState{}, err
		}
		if row.RuntimeInstanceID != settlement.RuntimeInstanceID ||
			row.RuntimeGeneration != settlement.RuntimeGeneration ||
			row.ControlChannelEpoch != settlement.ChannelEpoch ||
			row.RuntimeRevision <
				record.Command.ExpectedRuntimeRevision {
			return application.ResidentRuntimeState{},
				platformpostgres.ErrRevisionConflict
		}
		if row.TurnRevision != record.Command.ExpectedTurnRevision {
			if row.TurnStatus != "consumed" ||
				row.TurnRevision <
					record.Command.ExpectedTurnRevision ||
				row.TurnRevision-
					record.Command.ExpectedTurnRevision != 1 {
				return application.ResidentRuntimeState{},
					platformpostgres.ErrRevisionConflict
			}
			record.Command.ExpectedTurnRevision = row.TurnRevision
		}
		// Runtime-control progress and heartbeat events legitimately advance
		// the active Runtime revision after the terminal settlement freezes
		// its domain plan. A checkpoint frozen by an older Backend can also
		// precede the single delivered-to-consumed Turn transition. The
		// locked graph, exact terminal identity, generation, epoch and
		// lifecycle status remain the authority; only those two proven
		// revisions are refreshed to the values locked here.
		record.Command.ExpectedRuntimeRevision = row.RuntimeRevision
		return completeResidentStageWithLockedRuntimeTx(
			ctx,
			tx,
			row,
			record,
			false,
		)
	}
	if settlement.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline {
		return application.ResidentRuntimeState{},
			application.ErrTurnSettlementSourceConflict
	}
	source, err := application.DecodeBackendDeadlineSourceState(
		settlement.Source,
	)
	if err != nil {
		return application.ResidentRuntimeState{}, err
	}
	switch source.Phase {
	case application.BackendDeadlineSourceTerminalObserved:
		row, err := lockResidentRuntime(
			ctx,
			tx,
			record.Command.WorkerSessionID,
			record.Command.WorkerRunID,
			record.Command.AgentTurnID,
			record.Command.RuntimeInstanceID,
		)
		if err != nil {
			return application.ResidentRuntimeState{}, err
		}
		if settlement.RuntimeInstanceID != source.RuntimeInstanceID ||
			settlement.RuntimeGeneration != source.RuntimeGeneration ||
			settlement.ChannelEpoch != source.ChannelEpoch ||
			row.RuntimeInstanceID != source.RuntimeInstanceID ||
			row.RuntimeGeneration != source.RuntimeGeneration ||
			row.ControlChannelEpoch != source.ChannelEpoch ||
			source.ForceStop != nil ||
			source.WriteFence != nil ||
			source.Cleanup != nil ||
			source.RecoveryIncidentID != "" ||
			source.RecoveryIncidentRevision != 0 ||
			!source.RecoveryPreemptedAt.IsZero() {
			return application.ResidentRuntimeState{},
				application.ErrTurnSettlementConflict
		}
		return completeResidentStageWithLockedRuntimeTx(
			ctx,
			tx,
			row,
			record,
			true,
		)
	case application.BackendDeadlineSourceCleanupConfirmed:
	default:
		return application.ResidentRuntimeState{},
			application.ErrTurnSettlementSourceConflict
	}
	row, err := lockResidentRuntime(
		ctx,
		tx,
		record.Command.WorkerSessionID,
		record.Command.WorkerRunID,
		record.Command.AgentTurnID,
		record.Command.RuntimeInstanceID,
	)
	if err != nil {
		return application.ResidentRuntimeState{}, err
	}
	if row.SessionStatus == "running" &&
		row.RuntimeStatus == "active" {
		if source.RecoveryIncidentID != "" ||
			source.RecoveryIncidentRevision != 0 ||
			!source.RecoveryPreemptedAt.IsZero() {
			return application.ResidentRuntimeState{},
				platformpostgres.ErrRevisionConflict
		}
		return completeResidentStageTx(ctx, tx, record)
	}
	switch {
	case row.SessionStatus == "running" &&
		row.RuntimeStatus == "suspect":
		if source.RecoveryIncidentID != "" ||
			source.RecoveryIncidentRevision != 0 ||
			!source.RecoveryPreemptedAt.IsZero() {
			return application.ResidentRuntimeState{},
				platformpostgres.ErrRevisionConflict
		}
	case row.SessionStatus == "recovering" &&
		row.RuntimeStatus == "unhealthy":
		if err := validateExactDeadlinePreemptedRecovery(
			ctx,
			tx,
			settlement,
			source,
			row,
		); err != nil {
			return application.ResidentRuntimeState{}, err
		}
	default:
		return application.ResidentRuntimeState{},
			platformpostgres.ErrRevisionConflict
	}
	return completeForcedDeadlineStageTx(
		ctx,
		tx,
		settlement,
		source,
		row,
		record,
	)
}

func completeForcedDeadlineStageTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	source application.BackendDeadlineSourceState,
	row residentRuntimeRow,
	record application.CompleteResidentStageRecord,
) (application.ResidentRuntimeState, error) {
	command := record.Command
	if source.ForceStop == nil ||
		source.WriteFence == nil ||
		source.Cleanup == nil ||
		row.TenantID != command.TenantID ||
		row.WorkerSessionID != command.WorkerSessionID ||
		row.WorkerRunID != command.WorkerRunID ||
		row.AgentTurnID != command.AgentTurnID ||
		row.RuntimeInstanceID != command.RuntimeInstanceID ||
		row.RuntimeInstanceID != source.RuntimeInstanceID ||
		row.RuntimeGeneration != command.RuntimeGeneration ||
		row.RuntimeGeneration != source.RuntimeGeneration ||
		row.ControlChannelEpoch != source.ChannelEpoch ||
		row.ContainerID == "" ||
		row.ContainerID != source.ForceStop.ExpectedContainerID ||
		row.SessionRevision != command.ExpectedSessionRevision ||
		row.SessionRevision !=
			source.ForceStop.ExpectedSessionRevision ||
		row.RunRevision != command.ExpectedRunRevision ||
		row.TurnRevision != command.ExpectedTurnRevision ||
		row.RuntimeRevision != command.ExpectedRuntimeRevision ||
		row.RuntimeRevision !=
			source.ForceStop.ExpectedRuntimeRevision ||
		row.RunStatus != "running" ||
		row.TurnStatus != "consumed" ||
		command.SessionTarget != "waiting_user" ||
		command.RunTarget != "timed_out" ||
		command.TurnTarget != "timed_out" ||
		command.TerminalReason != "agent_turn_deadline" {
		return application.ResidentRuntimeState{},
			platformpostgres.ErrRevisionConflict
	}
	if _, err := row.SessionStatus.Transition(
		command.SessionTarget,
	); err != nil {
		return application.ResidentRuntimeState{}, err
	}
	if _, err := row.RunStatus.Transition(command.RunTarget); err != nil {
		return application.ResidentRuntimeState{}, err
	}
	if _, err := row.TurnStatus.Transition(command.TurnTarget); err != nil {
		return application.ResidentRuntimeState{}, err
	}

	var turnRevision uint64
	err := tx.QueryRow(ctx, `
UPDATE agent_turns
SET status='timed_out',revision=revision+1,terminal_at=$2,
    terminal_reason='agent_turn_deadline'
WHERE agent_turn_id=$1
  AND worker_session_id=$3
  AND worker_run_id=$4
  AND revision=$5
  AND status='consumed'
RETURNING revision`,
		command.AgentTurnID,
		record.Now,
		command.WorkerSessionID,
		command.WorkerRunID,
		command.ExpectedTurnRevision,
	).Scan(&turnRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ResidentRuntimeState{},
			platformpostgres.ErrRevisionConflict
	}
	if err != nil {
		return application.ResidentRuntimeState{},
			fmt.Errorf("complete forced deadline AgentTurn: %w", err)
	}

	var runRevision uint64
	err = tx.QueryRow(ctx, `
UPDATE worker_runs
SET status='timed_out',revision=revision+1,terminal_at=$2,
    terminal_reason='agent_turn_deadline'
WHERE worker_run_id=$1
  AND worker_session_id=$3
  AND revision=$4
  AND status='running'
RETURNING revision`,
		command.WorkerRunID,
		record.Now,
		command.WorkerSessionID,
		command.ExpectedRunRevision,
	).Scan(&runRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ResidentRuntimeState{},
			platformpostgres.ErrRevisionConflict
	}
	if err != nil {
		return application.ResidentRuntimeState{},
			fmt.Errorf("complete forced deadline WorkerRun: %w", err)
	}

	var sessionRevision uint64
	err = tx.QueryRow(ctx, `
UPDATE worker_sessions
SET status='waiting_user',
    active_worker_run_id=NULL,
    session_revision=session_revision+1,
    updated_at=$2
WHERE worker_session_id=$1
  AND tenant_id=$3
  AND session_revision=$4
  AND status=$5
  AND active_worker_run_id=$6
  AND active_runtime_instance_id=$7
  AND runtime_generation=$8
RETURNING session_revision`,
		command.WorkerSessionID,
		record.Now,
		command.TenantID,
		command.ExpectedSessionRevision,
		row.SessionStatus,
		command.WorkerRunID,
		command.RuntimeInstanceID,
		command.RuntimeGeneration,
	).Scan(&sessionRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ResidentRuntimeState{},
			platformpostgres.ErrRevisionConflict
	}
	if err != nil {
		return application.ResidentRuntimeState{},
			fmt.Errorf(
				"complete forced deadline WorkerSession: %w",
				err,
			)
	}
	if _, err := tx.Exec(ctx, `
UPDATE scheduled_commands
SET status='cancelled',updated_at=$2
WHERE target_type='AgentTurn'
  AND target_id=$1
  AND status='pending'`,
		command.AgentTurnID,
		record.Now,
	); err != nil {
		return application.ResidentRuntimeState{},
			fmt.Errorf(
				"cancel forced deadline AgentTurn command: %w",
				err,
			)
	}
	row.SessionStatus = command.SessionTarget
	row.RunStatus = command.RunTarget
	row.TurnStatus = command.TurnTarget
	row.SessionRevision = sessionRevision
	row.RunRevision = runRevision
	row.TurnRevision = turnRevision
	return row.state(), nil
}

func validateForcedDeadlineDomainReplay(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	row residentRuntimeRow,
	command application.CommitTurnSettlementDomainCommand,
	proof application.TurnSettlementDomainProof,
) error {
	if settlement.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline {
		return nil
	}
	source, err := application.DecodeBackendDeadlineSourceState(
		settlement.Source,
	)
	if err != nil {
		return err
	}
	if source.Phase !=
		application.BackendDeadlineSourceCleanupConfirmed {
		return nil
	}
	if source.ForceStop == nil ||
		source.WriteFence == nil ||
		source.Cleanup == nil ||
		row.SessionStatus != command.Resident.SessionTarget ||
		row.RunStatus != command.Resident.RunTarget ||
		row.TurnStatus != command.Resident.TurnTarget ||
		row.RuntimeInstanceID != source.RuntimeInstanceID ||
		row.RuntimeGeneration != source.RuntimeGeneration ||
		row.ControlChannelEpoch != source.ChannelEpoch ||
		row.ContainerID == "" ||
		row.ContainerID != source.ForceStop.ExpectedContainerID ||
		row.RuntimeRevision !=
			source.ForceStop.ExpectedRuntimeRevision ||
		proof.SessionRevision !=
			source.ForceStop.ExpectedSessionRevision+1 ||
		proof.RunRevision != command.Resident.ExpectedRunRevision+1 ||
		proof.TurnRevision !=
			command.Resident.ExpectedTurnRevision+1 ||
		proof.RuntimeRevision !=
			source.ForceStop.ExpectedRuntimeRevision {
		return application.ErrTurnSettlementConflict
	}
	var activeRuntimeInstanceID, activeWorkerRunID string
	err = tx.QueryRow(ctx, `
SELECT COALESCE(active_runtime_instance_id,''),
       COALESCE(active_worker_run_id,'')
FROM worker_sessions
WHERE worker_session_id=$1
  AND tenant_id=$2
  AND session_revision=$3
FOR UPDATE`,
		command.Resident.WorkerSessionID,
		command.Resident.TenantID,
		proof.SessionRevision,
	).Scan(
		&activeRuntimeInstanceID,
		&activeWorkerRunID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrTurnSettlementConflict
	}
	if err != nil {
		return fmt.Errorf(
			"validate forced deadline replay binding: %w",
			err,
		)
	}
	if activeRuntimeInstanceID != source.RuntimeInstanceID ||
		activeWorkerRunID != "" {
		return application.ErrTurnSettlementConflict
	}
	switch row.RuntimeStatus {
	case "active", "suspect":
		if source.RecoveryIncidentID != "" ||
			source.RecoveryIncidentRevision != 0 ||
			!source.RecoveryPreemptedAt.IsZero() {
			return application.ErrTurnSettlementConflict
		}
	case "unhealthy":
		return validateExactDeadlinePreemptedRecovery(
			ctx,
			tx,
			settlement,
			source,
			row,
		)
	default:
		return application.ErrTurnSettlementConflict
	}
	return nil
}

func validateExactDeadlinePreemptedRecovery(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	source application.BackendDeadlineSourceState,
	row residentRuntimeRow,
) error {
	if source.RecoveryIncidentID == "" ||
		source.RecoveryIncidentRevision == 0 ||
		source.RecoveryPreemptedAt.IsZero() {
		return application.ErrTurnSettlementConflict
	}
	var (
		tenantID, sessionID, runID, turnID, runtimeID string
		phase, outcome, outcomeReason, preemptedBy    string
		generation, channelEpoch, revision            uint64
		updatedAt, closedAt                           time.Time
	)
	err := tx.QueryRow(ctx, `
SELECT tenant_id,worker_session_id,COALESCE(worker_run_id,''),
       COALESCE(agent_turn_id,''),failed_runtime_instance_id,
       failed_generation,failed_channel_epoch,phase,COALESCE(outcome,''),
       COALESCE(outcome_reason,''),COALESCE(preempted_by_turn_settlement_id,''),
       revision,updated_at,closed_at
FROM runtime_recovery_incidents
WHERE incident_id=$1
FOR UPDATE`,
		source.RecoveryIncidentID,
	).Scan(
		&tenantID,
		&sessionID,
		&runID,
		&turnID,
		&runtimeID,
		&generation,
		&channelEpoch,
		&phase,
		&outcome,
		&outcomeReason,
		&preemptedBy,
		&revision,
		&updatedAt,
		&closedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrTurnSettlementConflict
	}
	if err != nil {
		return fmt.Errorf(
			"validate deadline-preempted Runtime recovery: %w",
			err,
		)
	}
	if tenantID != settlement.TenantID ||
		sessionID != settlement.WorkerSessionID ||
		runID != settlement.WorkerRunID ||
		turnID != settlement.AgentTurnID ||
		runtimeID != row.RuntimeInstanceID ||
		generation != row.RuntimeGeneration ||
		channelEpoch != row.ControlChannelEpoch ||
		phase != "exhausted" ||
		outcome != "exhausted" ||
		outcomeReason != "preempted_by_backend_deadline" ||
		preemptedBy != settlement.SettlementID ||
		revision != source.RecoveryIncidentRevision ||
		!updatedAt.Equal(source.RecoveryPreemptedAt) ||
		!closedAt.Equal(source.RecoveryPreemptedAt) {
		return application.ErrTurnSettlementConflict
	}
	return nil
}

func decodeTurnSettlementDomainCheckpoint(
	checkpoint json.RawMessage,
) (
	frozenTurnSettlementDomainPlan,
	*application.TurnSettlementDomainProof,
	error,
) {
	var envelope struct {
		DomainPlan *frozenTurnSettlementDomainPlan        `json:"domain_plan"`
		Domain     *application.TurnSettlementDomainProof `json:"domain"`
	}
	if err := json.Unmarshal(checkpoint, &envelope); err != nil {
		return frozenTurnSettlementDomainPlan{},
			nil,
			fmt.Errorf(
				"%w: decode frozen domain plan",
				application.ErrTurnSettlementCheckpointConflict,
			)
	}
	if envelope.DomainPlan == nil {
		return frozenTurnSettlementDomainPlan{},
			nil,
			fmt.Errorf(
				"%w: frozen domain plan is missing",
				application.ErrTurnSettlementCheckpointConflict,
			)
	}
	if envelope.Domain != nil &&
		(envelope.Domain.SessionRevision == 0 ||
			envelope.Domain.RunRevision == 0 ||
			envelope.Domain.TurnRevision == 0 ||
			envelope.Domain.RuntimeRevision == 0) {
		return frozenTurnSettlementDomainPlan{},
			nil,
			fmt.Errorf(
				"%w: durable domain proof is incomplete",
				application.ErrTurnSettlementCheckpointConflict,
			)
	}
	return *envelope.DomainPlan, envelope.Domain, nil
}

func validateTurnSettlementDomainIdentity(
	settlement application.TurnSettlement,
	command application.CommitTurnSettlementDomainCommand,
	plan frozenTurnSettlementDomainPlan,
) error {
	stage := command.Resident
	runtimeInstanceID, forceStopped, err :=
		turnSettlementDomainRuntimeAuthority(settlement)
	if err != nil {
		return err
	}
	runtimeGeneration := settlement.RuntimeGeneration
	if settlement.Source.Kind ==
		application.TurnSettlementSourceBackendDeadline {
		state, stateErr :=
			application.DecodeBackendDeadlineSourceState(
				settlement.Source,
			)
		if stateErr != nil {
			return stateErr
		}
		runtimeGeneration = state.RuntimeGeneration
		if forceStopped &&
			(state.ForceStop == nil ||
				state.ForceStop.ExpectedSessionRevision !=
					stage.ExpectedSessionRevision ||
				state.ForceStop.ExpectedRuntimeRevision !=
					stage.ExpectedRuntimeRevision) {
			return application.ErrTurnSettlementConflict
		}
	}
	if settlement.SettlementID != command.SettlementID ||
		settlement.TenantID != stage.TenantID ||
		settlement.WorkerSessionID != stage.WorkerSessionID ||
		settlement.WorkerRunID != stage.WorkerRunID ||
		settlement.AgentTurnID != stage.AgentTurnID ||
		runtimeInstanceID != stage.RuntimeInstanceID ||
		runtimeGeneration != stage.RuntimeGeneration ||
		plan.SessionTarget != string(stage.SessionTarget) ||
		plan.RunTarget != string(stage.RunTarget) ||
		plan.TurnTarget != string(stage.TurnTarget) ||
		plan.TerminalReason != stage.TerminalReason ||
		plan.ExpectedSessionRevision !=
			stage.ExpectedSessionRevision ||
		plan.ExpectedRunRevision != stage.ExpectedRunRevision ||
		plan.ExpectedTurnRevision != stage.ExpectedTurnRevision ||
		plan.ExpectedRuntimeRevision !=
			stage.ExpectedRuntimeRevision {
		return application.ErrTurnSettlementConflict
	}
	return nil
}

func turnSettlementDomainRuntimeAuthority(
	settlement application.TurnSettlement,
) (string, bool, error) {
	switch settlement.Source.Kind {
	case application.TurnSettlementSourceRuntimeEvent:
		if settlement.RuntimeInstanceID == "" ||
			settlement.RuntimeGeneration == 0 ||
			settlement.ChannelEpoch == 0 {
			return "", false,
				application.ErrTurnSettlementConflict
		}
		return settlement.RuntimeInstanceID, false, nil
	case application.TurnSettlementSourceBackendDeadline:
		state, err := application.DecodeBackendDeadlineSourceState(
			settlement.Source,
		)
		if err != nil {
			return "", false, err
		}
		hasExactWitness := settlement.RuntimeInstanceID ==
			state.RuntimeInstanceID &&
			settlement.RuntimeGeneration ==
				state.RuntimeGeneration &&
			settlement.ChannelEpoch == state.ChannelEpoch
		hasNoWitness := settlement.RuntimeInstanceID == "" &&
			settlement.RuntimeGeneration == 0 &&
			settlement.ChannelEpoch == 0
		if !hasExactWitness && !hasNoWitness {
			return "", false,
				application.ErrTurnSettlementConflict
		}
		switch state.Phase {
		case application.BackendDeadlineSourceTerminalObserved:
			if !hasExactWitness {
				return "", false,
					application.ErrTurnSettlementConflict
			}
			return state.RuntimeInstanceID, false, nil
		case application.BackendDeadlineSourceCleanupConfirmed:
			if state.ForceStop == nil ||
				state.WriteFence == nil ||
				state.Cleanup == nil {
				return "", false,
					application.ErrTurnSettlementSourceConflict
			}
			return state.RuntimeInstanceID, true, nil
		default:
			return "", false,
				application.ErrTurnSettlementSourceConflict
		}
	default:
		return "", false,
			application.ErrTurnSettlementConflict
	}
}

func appendTurnSettlementDomainProof(
	current json.RawMessage,
	proof application.TurnSettlementDomainProof,
) (json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(current, &fields); err != nil ||
		fields == nil {
		return nil, "", application.ErrTurnSettlementCheckpointConflict
	}
	if _, exists := fields["domain"]; exists {
		return nil, "", application.ErrTurnSettlementCheckpointConflict
	}
	encodedProof, err := json.Marshal(proof)
	if err != nil {
		return nil, "", fmt.Errorf(
			"encode Turn settlement domain proof: %w",
			err,
		)
	}
	fields["domain"] = encodedProof
	candidate, err := json.Marshal(fields)
	if err != nil {
		return nil, "", fmt.Errorf(
			"encode cumulative Turn settlement domain checkpoint: %w",
			err,
		)
	}
	canonical, digest, err :=
		application.CanonicalizeTurnSettlementCheckpoint(candidate)
	if err != nil {
		return nil, "", err
	}
	if err := application.ValidateTurnSettlementCheckpointExtension(
		current,
		canonical,
	); err != nil {
		return nil, "", err
	}
	return canonical, digest, nil
}

func turnSettlementDomainProofMatchesState(
	proof application.TurnSettlementDomainProof,
	state application.ResidentRuntimeState,
	command application.CommitTurnSettlementDomainCommand,
) bool {
	return proof.SessionRevision == state.SessionRevision &&
		proof.RunRevision == state.RunRevision &&
		proof.TurnRevision == state.TurnRevision &&
		proof.RuntimeRevision == state.RuntimeRevision &&
		state.RuntimeGeneration ==
			command.Resident.RuntimeGeneration &&
		state.SessionStatus == command.Resident.SessionTarget &&
		state.RunStatus == command.Resident.RunTarget &&
		state.TurnStatus == command.Resident.TurnTarget
}

var _ application.TurnSettlementDomainStore = (*TurnSettlementRepository)(nil)
