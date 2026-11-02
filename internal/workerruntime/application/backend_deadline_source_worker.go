package application

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

// BackendDeadlineSourceWorkerOutcome describes the one durable source step
// performed by Reconcile. Source reconciliation deliberately stops before the
// general Turn-settlement/domain pipeline; that hand-off belongs to the later
// domain-commit stage.
type BackendDeadlineSourceWorkerOutcome string

const (
	BackendDeadlineSourceWorkerInterruptQueued  BackendDeadlineSourceWorkerOutcome = "interrupt_queued"
	BackendDeadlineSourceWorkerWaitForceAfter   BackendDeadlineSourceWorkerOutcome = "wait_force_after"
	BackendDeadlineSourceWorkerTerminalObserved BackendDeadlineSourceWorkerOutcome = "terminal_observed"
	BackendDeadlineSourceWorkerForceStopIntent  BackendDeadlineSourceWorkerOutcome = "force_stop_intent"
	BackendDeadlineSourceWorkerWriteFenced      BackendDeadlineSourceWorkerOutcome = "write_fenced"
	BackendDeadlineSourceWorkerCleanupConfirmed BackendDeadlineSourceWorkerOutcome = "cleanup_confirmed"
)

type BackendDeadlineSourceWorkerResult struct {
	Settlement    TurnSettlement
	Outcome       BackendDeadlineSourceWorkerOutcome
	NextAttemptAt time.Time
}

// BackendDeadlineSourceAuthority is the frozen identity and current optimistic
// authority presented to external source effects. DeadlineAt and ForceAfter
// always come from the durable source state, never from a worker-local timer.
type BackendDeadlineSourceAuthority struct {
	SettlementID        string
	TenantID            string
	WorkerSessionID     string
	WorkerRunID         string
	AgentSessionID      string
	AgentTurnID         string
	RuntimeInstanceID   string
	RuntimeGeneration   uint64
	ChannelEpoch        uint64
	ExpectedRevision    uint64
	ExpectedStateDigest string
	LeaseOwnerID        string
	LeaseFence          uint64
	DeadlineAt          time.Time
	ForceAfter          time.Time
}

// BackendDeadlineForceStopTarget is the exact mutable graph snapshot that the
// atomic force-intent transaction must compare before fencing the Runtime.
type BackendDeadlineForceStopTarget struct {
	ExpectedSessionRevision uint64
	ExpectedRuntimeRevision uint64
	ExpectedContainerID     string
}

type BackendDeadlineWriteFenceObservationRequest struct {
	Authority BackendDeadlineSourceAuthority
	ForceStop BackendDeadlineForceStopProof
}

type BackendDeadlineCleanupObservationRequest struct {
	Authority  BackendDeadlineSourceAuthority
	ForceStop  BackendDeadlineForceStopProof
	WriteFence BackendDeadlineWriteFenceProof
}

// BackendDeadlineSourceEffects is intentionally proof-oriented. Implementors
// may inspect/fence/clean infrastructure, but the worker only accepts exact
// observations and persists them through the source state machine.
type BackendDeadlineSourceEffects interface {
	LoadForceStopTarget(
		context.Context,
		BackendDeadlineSourceAuthority,
	) (BackendDeadlineForceStopTarget, error)
	ObserveWriteFence(
		context.Context,
		BackendDeadlineWriteFenceObservationRequest,
	) (BackendDeadlineWriteFenceProof, error)
	ObserveCleanup(
		context.Context,
		BackendDeadlineCleanupObservationRequest,
	) (BackendDeadlineCleanupProof, error)
}

type BackendDeadlineSourceWorker struct {
	Source  TurnSettlementSourceService
	Effects BackendDeadlineSourceEffects
	Now     func() time.Time
}

// Reconcile performs at most one durable source transition. This makes retries
// and process restarts resume from the persisted phase instead of restarting
// the deadline grace period or repeating later external effects speculatively.
func (worker BackendDeadlineSourceWorker) Reconcile(
	ctx context.Context,
	leased TurnSettlement,
) (BackendDeadlineSourceWorkerResult, error) {
	if worker.Source.Store == nil || worker.Now == nil {
		return BackendDeadlineSourceWorkerResult{}, fmt.Errorf(
			"Backend deadline source worker dependencies are required",
		)
	}
	now := normalizePostgresTimestamp(worker.Now())
	if now.IsZero() {
		return BackendDeadlineSourceWorkerResult{}, fmt.Errorf(
			"Backend deadline source worker time is required",
		)
	}
	state, authority, err := validateBackendDeadlineWorkerSettlement(
		leased,
		now,
	)
	if err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	service := worker.Source
	service.Now = func() time.Time { return now }

	switch state.Phase {
	case BackendDeadlineSourceClaimed:
		if now.Before(state.ForceAfter) {
			next, err := worker.queueInterrupt(
				ctx,
				service,
				leased,
				state,
			)
			if err != nil {
				return BackendDeadlineSourceWorkerResult{}, err
			}
			return BackendDeadlineSourceWorkerResult{
				Settlement:    next,
				Outcome:       BackendDeadlineSourceWorkerInterruptQueued,
				NextAttemptAt: state.ForceAfter,
			}, nil
		}
		return worker.recordForceStopIntent(
			ctx,
			service,
			leased,
			state,
			authority,
		)

	case BackendDeadlineSourceInterruptQueued:
		if now.Before(state.ForceAfter) {
			return BackendDeadlineSourceWorkerResult{
				Settlement:    leased,
				Outcome:       BackendDeadlineSourceWorkerWaitForceAfter,
				NextAttemptAt: state.ForceAfter,
			}, nil
		}
		return worker.recordForceStopIntent(
			ctx,
			service,
			leased,
			state,
			authority,
		)

	case BackendDeadlineSourceTerminalObserved:
		return BackendDeadlineSourceWorkerResult{
			Settlement: leased,
			Outcome:    BackendDeadlineSourceWorkerTerminalObserved,
		}, nil

	case BackendDeadlineSourceForceStopIntent:
		return worker.recordWriteFence(
			ctx,
			service,
			leased,
			authority,
			state,
		)

	case BackendDeadlineSourceWriteFenced:
		return worker.recordCleanupConfirmed(
			ctx,
			service,
			leased,
			authority,
			state,
		)

	case BackendDeadlineSourceCleanupConfirmed:
		return BackendDeadlineSourceWorkerResult{
			Settlement: leased,
			Outcome:    BackendDeadlineSourceWorkerCleanupConfirmed,
		}, nil

	default:
		return BackendDeadlineSourceWorkerResult{},
			ErrTurnSettlementSourceConflict
	}
}

func (worker BackendDeadlineSourceWorker) queueInterrupt(
	ctx context.Context,
	service TurnSettlementSourceService,
	leased TurnSettlement,
	state BackendDeadlineSourceState,
) (TurnSettlement, error) {
	if state.RuntimeGeneration > math.MaxUint32 {
		return TurnSettlement{}, ErrTurnSettlementSourceConflict
	}
	messageID := "deadline-interrupt-" + leased.AgentTurnID
	envelope, err := json.Marshal(contractsv1.RuntimeControlEnvelope{
		SchemaVersion:     contractsv1.RuntimeControlSchemaVersion,
		MessageID:         messageID,
		Kind:              "command",
		WorkerSessionID:   contractsv1.WorkerSessionID(leased.WorkerSessionID),
		WorkerRunID:       contractsv1.WorkerRunID(leased.WorkerRunID),
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(state.RuntimeInstanceID),
		RuntimeGeneration: uint32(state.RuntimeGeneration),
		AgentSessionID:    contractsv1.AgentSessionID(leased.AgentSessionID),
		AgentTurnID:       contractsv1.AgentTurnID(leased.AgentTurnID),
		ChannelEpoch:      state.ChannelEpoch,
		IdempotencyKey:    messageID,
		MessageType:       "agent_turn.interrupt",
		Payload:           json.RawMessage(`{}`),
	})
	if err != nil {
		return TurnSettlement{}, fmt.Errorf(
			"encode Backend deadline interrupt: %w",
			err,
		)
	}
	return service.AdvanceBackendDeadline(
		ctx,
		AdvanceBackendDeadlineSourceCommand{
			SettlementID:            leased.SettlementID,
			ExpectedRevision:        leased.Revision,
			ExpectedStateDigest:     leased.Source.StateDigest,
			LeaseOwnerID:            leased.LeaseOwnerID,
			LeaseFence:              leased.LeaseFence,
			ToPhase:                 BackendDeadlineSourceInterruptQueued,
			InterruptMessageID:      messageID,
			InterruptIdempotencyKey: messageID,
			InterruptEnvelope:       envelope,
		},
	)
}

func (worker BackendDeadlineSourceWorker) recordForceStopIntent(
	ctx context.Context,
	service TurnSettlementSourceService,
	leased TurnSettlement,
	state BackendDeadlineSourceState,
	authority BackendDeadlineSourceAuthority,
) (BackendDeadlineSourceWorkerResult, error) {
	if worker.Effects == nil {
		return BackendDeadlineSourceWorkerResult{}, fmt.Errorf(
			"Backend deadline force-stop target reader is required",
		)
	}
	target, err := worker.Effects.LoadForceStopTarget(ctx, authority)
	if err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	if target.ExpectedSessionRevision == 0 ||
		target.ExpectedRuntimeRevision == 0 ||
		(target.ExpectedContainerID != "" &&
			validateCanonical(
				"expected_container_id",
				target.ExpectedContainerID,
			) != nil) {
		return BackendDeadlineSourceWorkerResult{},
			ErrTurnSettlementSourceConflict
	}
	proof := BackendDeadlineForceStopProof{
		IntentID:                  "deadline-force-stop-" + leased.AgentTurnID,
		DecidedAt:                 state.ForceAfter,
		RuntimeInstanceID:         state.RuntimeInstanceID,
		RuntimeGeneration:         state.RuntimeGeneration,
		ChannelEpoch:              state.ChannelEpoch,
		ExpectedSessionRevision:   target.ExpectedSessionRevision,
		ExpectedRuntimeRevision:   target.ExpectedRuntimeRevision,
		ExpectedContainerID:       target.ExpectedContainerID,
		CleanupScheduledCommandID: "deadline-cleanup-command-" + leased.AgentTurnID,
		CleanupIdempotencyKey:     "deadline-cleanup-" + leased.AgentTurnID,
	}
	if _, err := ApplyBackendDeadlineSourceTransition(
		leased.Source,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceForceStopIntent,
			ForceStop: &proof,
		},
	); err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	advancedAt := normalizePostgresTimestamp(worker.Now())
	if advancedAt.IsZero() ||
		advancedAt.Before(proof.DecidedAt) {
		return BackendDeadlineSourceWorkerResult{},
			ErrTurnSettlementSourceConflict
	}
	service.Now = func() time.Time { return advancedAt }
	next, err := service.AdvanceBackendDeadline(
		ctx,
		AdvanceBackendDeadlineSourceCommand{
			SettlementID:        leased.SettlementID,
			ExpectedRevision:    leased.Revision,
			ExpectedStateDigest: leased.Source.StateDigest,
			LeaseOwnerID:        leased.LeaseOwnerID,
			LeaseFence:          leased.LeaseFence,
			ToPhase:             BackendDeadlineSourceForceStopIntent,
			ForceStop:           &proof,
		},
	)
	if err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	return BackendDeadlineSourceWorkerResult{
		Settlement: next,
		Outcome:    BackendDeadlineSourceWorkerForceStopIntent,
	}, nil
}

func (worker BackendDeadlineSourceWorker) recordWriteFence(
	ctx context.Context,
	service TurnSettlementSourceService,
	leased TurnSettlement,
	authority BackendDeadlineSourceAuthority,
	state BackendDeadlineSourceState,
) (BackendDeadlineSourceWorkerResult, error) {
	if worker.Effects == nil || state.ForceStop == nil {
		return BackendDeadlineSourceWorkerResult{},
			ErrTurnSettlementSourceConflict
	}
	proof, err := worker.Effects.ObserveWriteFence(
		ctx,
		BackendDeadlineWriteFenceObservationRequest{
			Authority: authority,
			ForceStop: *cloneBackendDeadlineProof(state.ForceStop),
		},
	)
	if err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	if _, err := ApplyBackendDeadlineSourceTransition(
		leased.Source,
		BackendDeadlineSourceTransition{
			ToPhase:    BackendDeadlineSourceWriteFenced,
			WriteFence: &proof,
		},
	); err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	advancedAt := normalizePostgresTimestamp(worker.Now())
	if advancedAt.IsZero() ||
		advancedAt.Before(proof.ContainerAbsentObservedAt) {
		return BackendDeadlineSourceWorkerResult{},
			ErrTurnSettlementSourceConflict
	}
	service.Now = func() time.Time { return advancedAt }
	next, err := service.AdvanceBackendDeadline(
		ctx,
		AdvanceBackendDeadlineSourceCommand{
			SettlementID:        leased.SettlementID,
			ExpectedRevision:    leased.Revision,
			ExpectedStateDigest: leased.Source.StateDigest,
			LeaseOwnerID:        leased.LeaseOwnerID,
			LeaseFence:          leased.LeaseFence,
			ToPhase:             BackendDeadlineSourceWriteFenced,
			WriteFence:          &proof,
		},
	)
	if err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	return BackendDeadlineSourceWorkerResult{
		Settlement: next,
		Outcome:    BackendDeadlineSourceWorkerWriteFenced,
	}, nil
}

func (worker BackendDeadlineSourceWorker) recordCleanupConfirmed(
	ctx context.Context,
	service TurnSettlementSourceService,
	leased TurnSettlement,
	authority BackendDeadlineSourceAuthority,
	state BackendDeadlineSourceState,
) (BackendDeadlineSourceWorkerResult, error) {
	if worker.Effects == nil || state.ForceStop == nil ||
		state.WriteFence == nil {
		return BackendDeadlineSourceWorkerResult{},
			ErrTurnSettlementSourceConflict
	}
	proof, err := worker.Effects.ObserveCleanup(
		ctx,
		BackendDeadlineCleanupObservationRequest{
			Authority:  authority,
			ForceStop:  *cloneBackendDeadlineProof(state.ForceStop),
			WriteFence: *cloneBackendDeadlineProof(state.WriteFence),
		},
	)
	if err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	if _, err := ApplyBackendDeadlineSourceTransition(
		leased.Source,
		BackendDeadlineSourceTransition{
			ToPhase: BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &proof,
		},
	); err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	advancedAt := normalizePostgresTimestamp(worker.Now())
	if advancedAt.IsZero() || advancedAt.Before(proof.ConfirmedAt) {
		return BackendDeadlineSourceWorkerResult{},
			ErrTurnSettlementSourceConflict
	}
	service.Now = func() time.Time { return advancedAt }
	next, err := service.AdvanceBackendDeadline(
		ctx,
		AdvanceBackendDeadlineSourceCommand{
			SettlementID:        leased.SettlementID,
			ExpectedRevision:    leased.Revision,
			ExpectedStateDigest: leased.Source.StateDigest,
			LeaseOwnerID:        leased.LeaseOwnerID,
			LeaseFence:          leased.LeaseFence,
			ToPhase:             BackendDeadlineSourceCleanupConfirmed,
			Cleanup:             &proof,
		},
	)
	if err != nil {
		return BackendDeadlineSourceWorkerResult{}, err
	}
	return BackendDeadlineSourceWorkerResult{
		Settlement: next,
		Outcome:    BackendDeadlineSourceWorkerCleanupConfirmed,
	}, nil
}

func validateBackendDeadlineWorkerSettlement(
	leased TurnSettlement,
	now time.Time,
) (
	BackendDeadlineSourceState,
	BackendDeadlineSourceAuthority,
	error,
) {
	if err := ValidateTurnSettlementSource(leased.Source); err != nil ||
		leased.Source.Kind != TurnSettlementSourceBackendDeadline {
		return BackendDeadlineSourceState{},
			BackendDeadlineSourceAuthority{},
			ErrTurnSettlementSourceConflict
	}
	state, err := decodeTurnSettlementSourceState(leased.Source.State)
	if err != nil {
		return BackendDeadlineSourceState{},
			BackendDeadlineSourceAuthority{},
			ErrTurnSettlementSourceConflict
	}
	for name, value := range map[string]string{
		"settlement_id":     leased.SettlementID,
		"tenant_id":         leased.TenantID,
		"worker_session_id": leased.WorkerSessionID,
		"worker_run_id":     leased.WorkerRunID,
		"agent_session_id":  leased.AgentSessionID,
		"agent_turn_id":     leased.AgentTurnID,
		"lease_owner_id":    leased.LeaseOwnerID,
	} {
		if validateCanonical(name, value) != nil {
			return BackendDeadlineSourceState{},
				BackendDeadlineSourceAuthority{},
				ErrTurnSettlementSourceConflict
		}
	}
	expectedSettlementID, idErr := TurnSettlementID(leased.AgentTurnID)
	if idErr != nil ||
		leased.SettlementID != expectedSettlementID ||
		leased.Revision == 0 ||
		!backendDeadlineWitnessMatchesState(leased, state) ||
		leased.LeaseFence == 0 ||
		leased.LeaseExpiresAt.IsZero() ||
		!leased.LeaseExpiresAt.Equal(
			normalizePostgresTimestamp(leased.LeaseExpiresAt),
		) {
		return BackendDeadlineSourceState{},
			BackendDeadlineSourceAuthority{},
			ErrTurnSettlementSourceConflict
	}
	if !now.Before(leased.LeaseExpiresAt) {
		return BackendDeadlineSourceState{},
			BackendDeadlineSourceAuthority{},
			ErrTurnSettlementLeaseStale
	}
	return state, BackendDeadlineSourceAuthority{
		SettlementID:        leased.SettlementID,
		TenantID:            leased.TenantID,
		WorkerSessionID:     leased.WorkerSessionID,
		WorkerRunID:         leased.WorkerRunID,
		AgentSessionID:      leased.AgentSessionID,
		AgentTurnID:         leased.AgentTurnID,
		RuntimeInstanceID:   state.RuntimeInstanceID,
		RuntimeGeneration:   state.RuntimeGeneration,
		ChannelEpoch:        state.ChannelEpoch,
		ExpectedRevision:    leased.Revision,
		ExpectedStateDigest: leased.Source.StateDigest,
		LeaseOwnerID:        leased.LeaseOwnerID,
		LeaseFence:          leased.LeaseFence,
		DeadlineAt:          state.DeadlineAt,
		ForceAfter:          state.ForceAfter,
	}, nil
}

func backendDeadlineWitnessMatchesState(
	settlement TurnSettlement,
	state BackendDeadlineSourceState,
) bool {
	if settlement.RuntimeInstanceID == "" &&
		settlement.RuntimeGeneration == 0 &&
		settlement.ChannelEpoch == 0 {
		return true
	}
	return settlement.RuntimeInstanceID == state.RuntimeInstanceID &&
		settlement.RuntimeGeneration == state.RuntimeGeneration &&
		settlement.ChannelEpoch == state.ChannelEpoch
}
