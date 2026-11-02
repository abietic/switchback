package application

import (
	"encoding/json"
	"time"
)

type BackendDeadlineInterruptProof struct {
	MessageID      string    `json:"message_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Sequence       uint64    `json:"sequence"`
	EnvelopeDigest string    `json:"envelope_digest"`
	CursorDigest   string    `json:"cursor_digest"`
	JournaledAt    time.Time `json:"journaled_at"`
}

type BackendDeadlineForceStopProof struct {
	IntentID                  string    `json:"intent_id"`
	DecidedAt                 time.Time `json:"decided_at"`
	RuntimeInstanceID         string    `json:"runtime_instance_id"`
	RuntimeGeneration         uint64    `json:"runtime_generation"`
	ChannelEpoch              uint64    `json:"channel_epoch"`
	ExpectedSessionRevision   uint64    `json:"expected_session_revision"`
	ExpectedRuntimeRevision   uint64    `json:"expected_runtime_revision"`
	ExpectedContainerID       string    `json:"expected_container_id,omitempty"`
	CleanupScheduledCommandID string    `json:"cleanup_scheduled_command_id"`
	CleanupIdempotencyKey     string    `json:"cleanup_idempotency_key"`
}

type BackendDeadlineWriteFenceProof struct {
	ChannelOwnerFence         uint64    `json:"channel_owner_fence"`
	ChannelRevision           uint64    `json:"channel_revision"`
	ChannelFencedAt           time.Time `json:"channel_fenced_at"`
	ContainerRef              string    `json:"container_ref,omitempty"`
	ContainerAbsentObservedAt time.Time `json:"container_absent_observed_at"`
	ProofDigest               string    `json:"proof_digest"`
}

type BackendDeadlineCleanupProof struct {
	ContainerRef       string    `json:"container_ref,omitempty"`
	SecretVolumeRef    string    `json:"secret_volume_ref,omitempty"`
	ContainerExists    bool      `json:"container_exists"`
	SecretVolumeExists bool      `json:"secret_volume_exists"`
	ObservedAt         time.Time `json:"observed_at"`
	ObservationDigest  string    `json:"observation_digest"`
	ConfirmedAt        time.Time `json:"confirmed_at"`
}

type BackendDeadlineSourceTransition struct {
	ToPhase            BackendDeadlineSourcePhase
	TerminalObservedAt time.Time
	Interrupt          *BackendDeadlineInterruptProof
	ForceStop          *BackendDeadlineForceStopProof
	WriteFence         *BackendDeadlineWriteFenceProof
	Cleanup            *BackendDeadlineCleanupProof
}

func NewBackendDeadlineWriteFenceProof(
	channelOwnerFence uint64,
	channelRevision uint64,
	channelFencedAt time.Time,
	containerRef string,
	containerAbsentObservedAt time.Time,
) (BackendDeadlineWriteFenceProof, error) {
	proof := BackendDeadlineWriteFenceProof{
		ChannelOwnerFence: channelOwnerFence,
		ChannelRevision:   channelRevision,
		ChannelFencedAt:   normalizePostgresTimestamp(channelFencedAt),
		ContainerRef:      containerRef,
		ContainerAbsentObservedAt: normalizePostgresTimestamp(
			containerAbsentObservedAt,
		),
	}
	if proof.ChannelOwnerFence == 0 ||
		proof.ChannelRevision == 0 ||
		proof.ChannelFencedAt.IsZero() ||
		proof.ContainerAbsentObservedAt.IsZero() ||
		(proof.ContainerRef != "" &&
			validateCanonical("container_ref", proof.ContainerRef) != nil) {
		return BackendDeadlineWriteFenceProof{},
			ErrTurnSettlementSourceConflict
	}
	proof.ProofDigest = backendDeadlineWriteFenceProofDigest(proof)
	return proof, nil
}

func NewBackendDeadlineCleanupProof(
	containerRef string,
	secretVolumeRef string,
	observedAt time.Time,
	confirmedAt time.Time,
) (BackendDeadlineCleanupProof, error) {
	proof := BackendDeadlineCleanupProof{
		ContainerRef:    containerRef,
		SecretVolumeRef: secretVolumeRef,
		ObservedAt:      normalizePostgresTimestamp(observedAt),
		ConfirmedAt:     normalizePostgresTimestamp(confirmedAt),
	}
	for name, value := range map[string]string{
		"container_ref":     proof.ContainerRef,
		"secret_volume_ref": proof.SecretVolumeRef,
	} {
		if value != "" && validateCanonical(name, value) != nil {
			return BackendDeadlineCleanupProof{},
				ErrTurnSettlementSourceConflict
		}
	}
	if proof.ObservedAt.IsZero() || proof.ConfirmedAt.IsZero() ||
		proof.ConfirmedAt.Before(proof.ObservedAt) {
		return BackendDeadlineCleanupProof{},
			ErrTurnSettlementSourceConflict
	}
	proof.ObservationDigest =
		backendDeadlineCleanupObservationDigest(proof)
	return proof, nil
}

func ApplyBackendDeadlineSourceTransition(
	source TurnSettlementSource,
	transition BackendDeadlineSourceTransition,
) (TurnSettlementSource, error) {
	if err := ValidateTurnSettlementSource(source); err != nil ||
		source.Kind != TurnSettlementSourceBackendDeadline {
		return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
	}
	current, err := decodeTurnSettlementSourceState(source.State)
	if err != nil {
		return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
	}
	if err := validateBackendDeadlineTransitionShape(transition); err != nil {
		return TurnSettlementSource{}, err
	}
	if backendDeadlineTransitionAlreadyApplied(current, transition) {
		return source, nil
	}
	next := current
	switch transition.ToPhase {
	case BackendDeadlineSourceInterruptQueued:
		if current.Phase != BackendDeadlineSourceClaimed {
			return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
		}
		next.Interrupt = cloneBackendDeadlineProof(transition.Interrupt)
	case BackendDeadlineSourceTerminalObserved:
		if current.Phase != BackendDeadlineSourceClaimed &&
			current.Phase != BackendDeadlineSourceInterruptQueued {
			return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
		}
		next.TerminalObservedAt = transition.TerminalObservedAt
	case BackendDeadlineSourceForceStopIntent:
		if current.Phase != BackendDeadlineSourceClaimed &&
			current.Phase != BackendDeadlineSourceInterruptQueued {
			return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
		}
		next.ForceStop = cloneBackendDeadlineProof(transition.ForceStop)
	case BackendDeadlineSourceWriteFenced:
		if current.Phase != BackendDeadlineSourceForceStopIntent {
			return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
		}
		next.WriteFence = cloneBackendDeadlineProof(transition.WriteFence)
	case BackendDeadlineSourceCleanupConfirmed:
		if current.Phase != BackendDeadlineSourceWriteFenced {
			return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
		}
		next.Cleanup = cloneBackendDeadlineProof(transition.Cleanup)
	default:
		return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
	}
	next.Phase = transition.ToPhase
	encoded, err := json.Marshal(next)
	if err != nil {
		return TurnSettlementSource{}, err
	}
	source.State, source.StateDigest, err =
		CanonicalizeTurnSettlementSourceState(encoded)
	if err != nil {
		return TurnSettlementSource{}, err
	}
	if err := ValidateTurnSettlementSource(source); err != nil {
		return TurnSettlementSource{}, err
	}
	return source, nil
}

// ReconcileBackendDeadlineTerminalWitness advances a deadline source only when
// the terminal event wins before force-stop intent. Once force-stop intent is
// durable, a late terminal event is evidence only and cannot replace that
// branch's authority.
func ReconcileBackendDeadlineTerminalWitness(
	source TurnSettlementSource,
	observedAt time.Time,
) (TurnSettlementSource, bool, error) {
	if err := ValidateTurnSettlementSource(source); err != nil ||
		source.Kind != TurnSettlementSourceBackendDeadline ||
		observedAt.IsZero() ||
		!observedAt.Equal(normalizePostgresTimestamp(observedAt)) ||
		observedAt.Before(source.OccurredAt) {
		return TurnSettlementSource{}, false,
			ErrTurnSettlementSourceConflict
	}
	state, err := decodeTurnSettlementSourceState(source.State)
	if err != nil {
		return TurnSettlementSource{}, false,
			ErrTurnSettlementSourceConflict
	}
	switch state.Phase {
	case BackendDeadlineSourceClaimed,
		BackendDeadlineSourceInterruptQueued:
		next, err := ApplyBackendDeadlineSourceTransition(
			source,
			BackendDeadlineSourceTransition{
				ToPhase:            BackendDeadlineSourceTerminalObserved,
				TerminalObservedAt: observedAt,
			},
		)
		if err != nil {
			return TurnSettlementSource{}, false, err
		}
		return next, true, nil
	case BackendDeadlineSourceTerminalObserved:
		if !state.TerminalObservedAt.Equal(observedAt) {
			return TurnSettlementSource{}, false,
				ErrTurnSettlementSourceConflict
		}
		return source, false, nil
	case BackendDeadlineSourceForceStopIntent,
		BackendDeadlineSourceWriteFenced,
		BackendDeadlineSourceCleanupConfirmed:
		return source, false, nil
	default:
		return TurnSettlementSource{}, false,
			ErrTurnSettlementSourceConflict
	}
}

func validateBackendDeadlineSourceState(
	state BackendDeadlineSourceState,
) error {
	if state.SchemaVersion != BackendDeadlineSourceStateSchemaVersion ||
		state.DeadlineAt.IsZero() ||
		!state.DeadlineAt.Equal(normalizePostgresTimestamp(state.DeadlineAt)) ||
		!state.ForceAfter.Equal(state.DeadlineAt.Add(BackendDeadlineForceGrace)) ||
		validateCanonical("runtime_instance_id", state.RuntimeInstanceID) != nil ||
		state.RuntimeGeneration == 0 ||
		state.ChannelEpoch == 0 ||
		!validBackendDeadlineRecoveryBinding(state) {
		return ErrTurnSettlementSourceConflict
	}
	if state.Interrupt != nil &&
		validateBackendDeadlineInterruptProof(*state.Interrupt, state) != nil {
		return ErrTurnSettlementSourceConflict
	}
	if state.ForceStop != nil &&
		validateBackendDeadlineForceStopProof(*state.ForceStop, state) != nil {
		return ErrTurnSettlementSourceConflict
	}
	if state.WriteFence != nil &&
		validateBackendDeadlineWriteFenceProof(*state.WriteFence, state) != nil {
		return ErrTurnSettlementSourceConflict
	}
	if state.Cleanup != nil &&
		validateBackendDeadlineCleanupProof(*state.Cleanup, state) != nil {
		return ErrTurnSettlementSourceConflict
	}
	switch state.Phase {
	case BackendDeadlineSourceClaimed:
		if state.Interrupt != nil || state.ForceStop != nil ||
			state.WriteFence != nil || state.Cleanup != nil ||
			!state.TerminalObservedAt.IsZero() {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceInterruptQueued:
		if state.Interrupt == nil || state.ForceStop != nil ||
			state.WriteFence != nil || state.Cleanup != nil ||
			!state.TerminalObservedAt.IsZero() {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceTerminalObserved:
		if state.TerminalObservedAt.IsZero() ||
			state.TerminalObservedAt.Before(state.DeadlineAt) ||
			!state.TerminalObservedAt.Equal(
				normalizePostgresTimestamp(state.TerminalObservedAt),
			) ||
			state.ForceStop != nil || state.WriteFence != nil ||
			state.Cleanup != nil {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceForceStopIntent:
		if state.ForceStop == nil || state.WriteFence != nil ||
			state.Cleanup != nil || !state.TerminalObservedAt.IsZero() {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceWriteFenced:
		if state.ForceStop == nil || state.WriteFence == nil ||
			state.Cleanup != nil || !state.TerminalObservedAt.IsZero() {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceCleanupConfirmed:
		if state.ForceStop == nil || state.WriteFence == nil ||
			state.Cleanup == nil || !state.TerminalObservedAt.IsZero() {
			return ErrTurnSettlementSourceConflict
		}
	default:
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

func validateBackendDeadlineTransitionShape(
	transition BackendDeadlineSourceTransition,
) error {
	switch transition.ToPhase {
	case BackendDeadlineSourceInterruptQueued:
		if transition.Interrupt == nil ||
			!transition.TerminalObservedAt.IsZero() ||
			transition.ForceStop != nil || transition.WriteFence != nil ||
			transition.Cleanup != nil {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceTerminalObserved:
		if transition.TerminalObservedAt.IsZero() ||
			transition.Interrupt != nil || transition.ForceStop != nil ||
			transition.WriteFence != nil || transition.Cleanup != nil {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceForceStopIntent:
		if transition.ForceStop == nil ||
			!transition.TerminalObservedAt.IsZero() ||
			transition.Interrupt != nil || transition.WriteFence != nil ||
			transition.Cleanup != nil {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceWriteFenced:
		if transition.WriteFence == nil ||
			!transition.TerminalObservedAt.IsZero() ||
			transition.Interrupt != nil || transition.ForceStop != nil ||
			transition.Cleanup != nil {
			return ErrTurnSettlementSourceConflict
		}
	case BackendDeadlineSourceCleanupConfirmed:
		if transition.Cleanup == nil ||
			!transition.TerminalObservedAt.IsZero() ||
			transition.Interrupt != nil || transition.ForceStop != nil ||
			transition.WriteFence != nil {
			return ErrTurnSettlementSourceConflict
		}
	default:
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

func backendDeadlineTransitionAlreadyApplied(
	state BackendDeadlineSourceState,
	transition BackendDeadlineSourceTransition,
) bool {
	switch transition.ToPhase {
	case BackendDeadlineSourceInterruptQueued:
		return state.Interrupt != nil &&
			sameBackendDeadlineInterruptProof(
				*state.Interrupt,
				*transition.Interrupt,
			) &&
			(state.Phase == BackendDeadlineSourceInterruptQueued ||
				state.Phase == BackendDeadlineSourceTerminalObserved ||
				state.Phase == BackendDeadlineSourceForceStopIntent ||
				state.Phase == BackendDeadlineSourceWriteFenced ||
				state.Phase == BackendDeadlineSourceCleanupConfirmed)
	case BackendDeadlineSourceTerminalObserved:
		return state.Phase == BackendDeadlineSourceTerminalObserved &&
			state.TerminalObservedAt.Equal(transition.TerminalObservedAt)
	case BackendDeadlineSourceForceStopIntent:
		return state.ForceStop != nil &&
			sameBackendDeadlineForceStopProof(
				*state.ForceStop,
				*transition.ForceStop,
			) &&
			(state.Phase == BackendDeadlineSourceForceStopIntent ||
				state.Phase == BackendDeadlineSourceWriteFenced ||
				state.Phase == BackendDeadlineSourceCleanupConfirmed)
	case BackendDeadlineSourceWriteFenced:
		return state.WriteFence != nil &&
			sameBackendDeadlineWriteFenceProof(
				*state.WriteFence,
				*transition.WriteFence,
			) &&
			(state.Phase == BackendDeadlineSourceWriteFenced ||
				state.Phase == BackendDeadlineSourceCleanupConfirmed)
	case BackendDeadlineSourceCleanupConfirmed:
		return state.Phase == BackendDeadlineSourceCleanupConfirmed &&
			state.Cleanup != nil &&
			sameBackendDeadlineCleanupProof(
				*state.Cleanup,
				*transition.Cleanup,
			)
	default:
		return false
	}
}

func sameBackendDeadlineInterruptProof(
	left BackendDeadlineInterruptProof,
	right BackendDeadlineInterruptProof,
) bool {
	return left.MessageID == right.MessageID &&
		left.IdempotencyKey == right.IdempotencyKey &&
		left.Sequence == right.Sequence &&
		left.EnvelopeDigest == right.EnvelopeDigest &&
		left.CursorDigest == right.CursorDigest &&
		left.JournaledAt.Equal(right.JournaledAt)
}

func sameBackendDeadlineForceStopProof(
	left BackendDeadlineForceStopProof,
	right BackendDeadlineForceStopProof,
) bool {
	return left.IntentID == right.IntentID &&
		left.DecidedAt.Equal(right.DecidedAt) &&
		left.RuntimeInstanceID == right.RuntimeInstanceID &&
		left.RuntimeGeneration == right.RuntimeGeneration &&
		left.ChannelEpoch == right.ChannelEpoch &&
		left.ExpectedSessionRevision ==
			right.ExpectedSessionRevision &&
		left.ExpectedRuntimeRevision ==
			right.ExpectedRuntimeRevision &&
		left.ExpectedContainerID == right.ExpectedContainerID &&
		left.CleanupScheduledCommandID ==
			right.CleanupScheduledCommandID &&
		left.CleanupIdempotencyKey == right.CleanupIdempotencyKey
}

func sameBackendDeadlineWriteFenceProof(
	left BackendDeadlineWriteFenceProof,
	right BackendDeadlineWriteFenceProof,
) bool {
	return left.ChannelOwnerFence == right.ChannelOwnerFence &&
		left.ChannelRevision == right.ChannelRevision &&
		left.ChannelFencedAt.Equal(right.ChannelFencedAt) &&
		left.ContainerRef == right.ContainerRef &&
		left.ContainerAbsentObservedAt.Equal(
			right.ContainerAbsentObservedAt,
		) &&
		left.ProofDigest == right.ProofDigest
}

func sameBackendDeadlineCleanupProof(
	left BackendDeadlineCleanupProof,
	right BackendDeadlineCleanupProof,
) bool {
	return left.ContainerRef == right.ContainerRef &&
		left.SecretVolumeRef == right.SecretVolumeRef &&
		left.ContainerExists == right.ContainerExists &&
		left.SecretVolumeExists == right.SecretVolumeExists &&
		left.ObservedAt.Equal(right.ObservedAt) &&
		left.ObservationDigest == right.ObservationDigest &&
		left.ConfirmedAt.Equal(right.ConfirmedAt)
}

func validateBackendDeadlineInterruptProof(
	proof BackendDeadlineInterruptProof,
	state BackendDeadlineSourceState,
) error {
	if validateCanonical("message_id", proof.MessageID) != nil ||
		validateCanonical("idempotency_key", proof.IdempotencyKey) != nil ||
		proof.Sequence == 0 ||
		validateRuntimeControlCursorDigest(proof.EnvelopeDigest) != nil ||
		validateRuntimeControlCursorDigest(proof.CursorDigest) != nil ||
		proof.JournaledAt.IsZero() ||
		!proof.JournaledAt.Equal(normalizePostgresTimestamp(proof.JournaledAt)) ||
		proof.JournaledAt.Before(state.DeadlineAt) ||
		!proof.JournaledAt.Before(state.ForceAfter) {
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

func validateBackendDeadlineForceStopProof(
	proof BackendDeadlineForceStopProof,
	state BackendDeadlineSourceState,
) error {
	if validateCanonical("intent_id", proof.IntentID) != nil ||
		proof.DecidedAt.IsZero() ||
		!proof.DecidedAt.Equal(normalizePostgresTimestamp(proof.DecidedAt)) ||
		!proof.DecidedAt.Equal(state.ForceAfter) ||
		proof.RuntimeInstanceID != state.RuntimeInstanceID ||
		proof.RuntimeGeneration != state.RuntimeGeneration ||
		proof.ChannelEpoch != state.ChannelEpoch ||
		proof.ExpectedSessionRevision == 0 ||
		proof.ExpectedRuntimeRevision == 0 ||
		validateCanonical(
			"cleanup_scheduled_command_id",
			proof.CleanupScheduledCommandID,
		) != nil ||
		validateCanonical(
			"cleanup_idempotency_key",
			proof.CleanupIdempotencyKey,
		) != nil {
		return ErrTurnSettlementSourceConflict
	}
	if proof.ExpectedContainerID != "" &&
		validateCanonical(
			"expected_container_id",
			proof.ExpectedContainerID,
		) != nil {
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

func validateBackendDeadlineWriteFenceProof(
	proof BackendDeadlineWriteFenceProof,
	state BackendDeadlineSourceState,
) error {
	if state.ForceStop == nil ||
		proof.ChannelOwnerFence == 0 || proof.ChannelRevision == 0 ||
		proof.ChannelFencedAt.IsZero() ||
		!proof.ChannelFencedAt.Equal(
			normalizePostgresTimestamp(proof.ChannelFencedAt),
		) ||
		proof.ContainerAbsentObservedAt.IsZero() ||
		!proof.ContainerAbsentObservedAt.Equal(
			normalizePostgresTimestamp(proof.ContainerAbsentObservedAt),
		) ||
		proof.ContainerAbsentObservedAt.Before(
			state.ForceStop.DecidedAt,
		) ||
		proof.ContainerAbsentObservedAt.Before(proof.ChannelFencedAt) ||
		validateRuntimeControlCursorDigest(proof.ProofDigest) != nil ||
		proof.ProofDigest !=
			backendDeadlineWriteFenceProofDigest(proof) {
		return ErrTurnSettlementSourceConflict
	}
	if proof.ContainerRef != "" &&
		validateCanonical("container_ref", proof.ContainerRef) != nil {
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

func validateBackendDeadlineCleanupProof(
	proof BackendDeadlineCleanupProof,
	state BackendDeadlineSourceState,
) error {
	if state.WriteFence == nil ||
		proof.ContainerExists || proof.SecretVolumeExists ||
		proof.ObservedAt.IsZero() ||
		!proof.ObservedAt.Equal(normalizePostgresTimestamp(proof.ObservedAt)) ||
		proof.ObservedAt.Before(
			state.WriteFence.ContainerAbsentObservedAt,
		) ||
		validateRuntimeControlCursorDigest(proof.ObservationDigest) != nil ||
		proof.ObservationDigest !=
			backendDeadlineCleanupObservationDigest(proof) ||
		proof.ConfirmedAt.IsZero() ||
		!proof.ConfirmedAt.Equal(
			normalizePostgresTimestamp(proof.ConfirmedAt),
		) ||
		proof.ConfirmedAt.Before(proof.ObservedAt) {
		return ErrTurnSettlementSourceConflict
	}
	for name, value := range map[string]string{
		"container_ref":     proof.ContainerRef,
		"secret_volume_ref": proof.SecretVolumeRef,
	} {
		if value != "" && validateCanonical(name, value) != nil {
			return ErrTurnSettlementSourceConflict
		}
	}
	return nil
}

func backendDeadlineWriteFenceProofDigest(
	proof BackendDeadlineWriteFenceProof,
) string {
	encoded, _ := json.Marshal(struct {
		SchemaVersion             uint32    `json:"schema_version"`
		ChannelOwnerFence         uint64    `json:"channel_owner_fence"`
		ChannelRevision           uint64    `json:"channel_revision"`
		ChannelFencedAt           time.Time `json:"channel_fenced_at"`
		ContainerRef              string    `json:"container_ref,omitempty"`
		ContainerAbsentObservedAt time.Time `json:"container_absent_observed_at"`
	}{
		SchemaVersion:             1,
		ChannelOwnerFence:         proof.ChannelOwnerFence,
		ChannelRevision:           proof.ChannelRevision,
		ChannelFencedAt:           proof.ChannelFencedAt,
		ContainerRef:              proof.ContainerRef,
		ContainerAbsentObservedAt: proof.ContainerAbsentObservedAt,
	})
	return digestRuntimeControlBytes(encoded)
}

func backendDeadlineCleanupObservationDigest(
	proof BackendDeadlineCleanupProof,
) string {
	encoded, _ := json.Marshal(struct {
		SchemaVersion      uint32    `json:"schema_version"`
		ContainerRef       string    `json:"container_ref,omitempty"`
		SecretVolumeRef    string    `json:"secret_volume_ref,omitempty"`
		ContainerExists    bool      `json:"container_exists"`
		SecretVolumeExists bool      `json:"secret_volume_exists"`
		ObservedAt         time.Time `json:"observed_at"`
	}{
		SchemaVersion:      1,
		ContainerRef:       proof.ContainerRef,
		SecretVolumeRef:    proof.SecretVolumeRef,
		ContainerExists:    proof.ContainerExists,
		SecretVolumeExists: proof.SecretVolumeExists,
		ObservedAt:         proof.ObservedAt,
	})
	return digestRuntimeControlBytes(encoded)
}

func cloneBackendDeadlineProof[T any](proof *T) *T {
	if proof == nil {
		return nil
	}
	copy := *proof
	return &copy
}
