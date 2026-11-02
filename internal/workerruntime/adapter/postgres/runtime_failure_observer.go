package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

type DetectedRuntimeFailure struct {
	WorkerSessionID   string
	RuntimeInstanceID string
	RuntimeGeneration uint64
	ChannelEpoch      uint64
	Trigger           domain.RuntimeRecoveryTrigger
	EvidenceRef       string
	EvidenceAt        time.Time
	// Inspection is populated only by the server-owned hard-container inspector.
	// Transport/Agent signals intentionally do not carry this evidence.
	Inspection *application.RuntimeInspection
}

type RuntimeFailureObserver struct {
	pool     *pgxpool.Pool
	recovery application.RuntimeRecoveryService
	enabled  bool
}

func NewRuntimeFailureObserver(
	pool *pgxpool.Pool,
	recovery application.RuntimeRecoveryService,
) *RuntimeFailureObserver {
	return &RuntimeFailureObserver{pool: pool, recovery: recovery, enabled: true}
}

func NewGatedRuntimeFailureObserver(
	pool *pgxpool.Pool,
	recovery application.RuntimeRecoveryService,
	enabled bool,
) *RuntimeFailureObserver {
	return &RuntimeFailureObserver{pool: pool, recovery: recovery, enabled: enabled}
}

func (observer *RuntimeFailureObserver) OpenDetectedRuntimeFailure(
	ctx context.Context,
	signal DetectedRuntimeFailure,
) (application.RuntimeRecoveryState, bool, error) {
	if observer == nil || observer.pool == nil {
		return application.RuntimeRecoveryState{}, false,
			fmt.Errorf("Runtime failure observer is unavailable")
	}
	if !observer.enabled && signal.Inspection == nil {
		return application.RuntimeRecoveryState{}, false, nil
	}
	if !signal.Trigger.Eligible() || signal.WorkerSessionID == "" ||
		signal.RuntimeInstanceID == "" || signal.RuntimeGeneration == 0 ||
		signal.ChannelEpoch == 0 || signal.EvidenceRef == "" || signal.EvidenceAt.IsZero() {
		return application.RuntimeRecoveryState{}, false,
			fmt.Errorf("detected Runtime failure identity is invalid")
	}
	var tenantID, activeRuntimeID, sessionStatus, runtimeStatus, containerID string
	var activeRunID, turnID *string
	var sessionRevision, runtimeRevision, runRevision, turnRevision uint64
	var runtimeGeneration, channelEpoch uint64
	err := observer.pool.QueryRow(ctx, `
SELECT session.tenant_id,COALESCE(session.active_runtime_instance_id,''),session.status,
       session.session_revision,session.active_worker_run_id,
       runtime.status,runtime.revision,runtime.generation,runtime.control_channel_epoch,
       turn.agent_turn_id,COALESCE(run.revision,0),COALESCE(turn.revision,0),COALESCE(runtime.container_id,'')
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$2
  AND runtime.worker_session_id=session.worker_session_id
LEFT JOIN worker_runs run ON run.worker_run_id=session.active_worker_run_id
LEFT JOIN LATERAL (
  SELECT candidate.agent_turn_id,candidate.revision
  FROM agent_turns candidate
  WHERE candidate.worker_run_id=run.worker_run_id
  ORDER BY candidate.created_at DESC,candidate.agent_turn_id DESC
  LIMIT 1
) turn ON true
WHERE session.worker_session_id=$1`,
		signal.WorkerSessionID, signal.RuntimeInstanceID,
	).Scan(
		&tenantID, &activeRuntimeID, &sessionStatus, &sessionRevision, &activeRunID,
		&runtimeStatus, &runtimeRevision, &runtimeGeneration, &channelEpoch,
		&turnID, &runRevision, &turnRevision, &containerID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeRecoveryState{}, false, nil
	}
	if err != nil {
		return application.RuntimeRecoveryState{}, false,
			fmt.Errorf("resolve detected Runtime failure: %w", err)
	}
	if activeRuntimeID != signal.RuntimeInstanceID ||
		runtimeGeneration != signal.RuntimeGeneration ||
		channelEpoch != signal.ChannelEpoch ||
		!detectedFailureSessionStatusAllowed(sessionStatus) ||
		(observer.enabled && !runtimeStatusCanRecover(runtimeStatus)) {
		return application.RuntimeRecoveryState{}, false, nil
	}
	command := application.OpenRuntimeRecoveryCommand{
		TenantID: tenantID, WorkerSessionID: signal.WorkerSessionID,
		FailedRuntimeInstanceID: signal.RuntimeInstanceID,
		FailedGeneration:        signal.RuntimeGeneration, FailedChannelEpoch: signal.ChannelEpoch,
		Trigger: signal.Trigger, EvidenceRef: signal.EvidenceRef, EvidenceAt: signal.EvidenceAt,
		ExpectedSessionRevision: sessionRevision, ExpectedRuntimeRevision: runtimeRevision,
		IdempotencyKey: "runtime-recovery/" + signal.WorkerSessionID + "/g" +
			strconv.FormatUint(signal.RuntimeGeneration, 10) + "/" + string(signal.Trigger),
	}
	if activeRunID != nil {
		if turnID == nil {
			return application.RuntimeRecoveryState{}, false,
				fmt.Errorf("active WorkerRun has no recoverable AgentTurn")
		}
		command.WorkerRunID = *activeRunID
		command.AgentTurnID = *turnID
		command.ExpectedRunRevision = runRevision
		command.ExpectedTurnRevision = turnRevision
	}
	var state application.RuntimeRecoveryState
	if !observer.enabled {
		store, ok := observer.recovery.Store.(application.RuntimeFailureWithoutReplacementStore)
		if !ok {
			return state, false, fmt.Errorf("Runtime failure store is unavailable")
		}
		state, err = (application.RuntimeFailureWithoutReplacementService{Store: store, IDs: observer.recovery.IDs, Now: observer.recovery.Now}).RecordFailureWithoutReplacement(ctx, application.RuntimeFailureWithoutReplacementCommand{
			OpenRuntimeRecoveryCommand: command, ExpectedContainerID: containerID, Inspection: *signal.Inspection,
		})
	} else {
		state, err = observer.recovery.Open(ctx, command)
	}
	if errors.Is(err, platformpostgres.ErrRevisionConflict) || (!observer.enabled && errors.Is(err, application.ErrTurnOutcomeAuthorityClaimed)) {
		return application.RuntimeRecoveryState{}, false, nil
	}
	return state, err == nil, err
}

func detectedFailureSessionStatusAllowed(status string) bool {
	switch status {
	case "interactive", "running", "waiting_user", "idle":
		return true
	default:
		return false
	}
}
