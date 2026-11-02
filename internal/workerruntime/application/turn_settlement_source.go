package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

var ErrTurnSettlementSourceConflict = errors.New(
	"Turn settlement source conflicts",
)

const RuntimeEventSourceStateSchemaVersion uint32 = 1
const BackendDeadlineSourceStateSchemaVersion uint32 = 2
const BackendDeadlineForceGrace = 30 * time.Second

type TurnSettlementSourceKind string

const (
	TurnSettlementSourceRuntimeEvent    TurnSettlementSourceKind = "runtime_event"
	TurnSettlementSourceBackendDeadline TurnSettlementSourceKind = "backend_deadline"
)

type BackendDeadlineSourcePhase string

const (
	BackendDeadlineSourceClaimed          BackendDeadlineSourcePhase = "claimed"
	BackendDeadlineSourceInterruptQueued  BackendDeadlineSourcePhase = "interrupt_enqueued"
	BackendDeadlineSourceTerminalObserved BackendDeadlineSourcePhase = "terminal_observed"
	BackendDeadlineSourceForceStopIntent  BackendDeadlineSourcePhase = "force_stop_intent"
	BackendDeadlineSourceWriteFenced      BackendDeadlineSourcePhase = "write_fenced"
	BackendDeadlineSourceCleanupConfirmed BackendDeadlineSourcePhase = "cleanup_confirmed"
)

type TurnSettlementSource struct {
	Kind        TurnSettlementSourceKind
	Reason      string
	OccurredAt  time.Time
	Ref         string
	Digest      string
	State       json.RawMessage
	StateDigest string
}

type BackendDeadlineSourceState struct {
	SchemaVersion            uint32                          `json:"schema_version"`
	Phase                    BackendDeadlineSourcePhase      `json:"phase"`
	DeadlineAt               time.Time                       `json:"deadline_at"`
	ForceAfter               time.Time                       `json:"force_after"`
	RuntimeInstanceID        string                          `json:"runtime_instance_id"`
	RuntimeGeneration        uint64                          `json:"runtime_generation"`
	ChannelEpoch             uint64                          `json:"channel_epoch"`
	Interrupt                *BackendDeadlineInterruptProof  `json:"interrupt,omitempty"`
	TerminalObservedAt       time.Time                       `json:"terminal_observed_at,omitempty"`
	ForceStop                *BackendDeadlineForceStopProof  `json:"force_stop,omitempty"`
	WriteFence               *BackendDeadlineWriteFenceProof `json:"write_fence,omitempty"`
	Cleanup                  *BackendDeadlineCleanupProof    `json:"cleanup,omitempty"`
	RecoveryIncidentID       string                          `json:"recovery_incident_id,omitempty"`
	RecoveryIncidentRevision uint64                          `json:"recovery_incident_revision,omitempty"`
	RecoveryPreemptedAt      time.Time                       `json:"recovery_preempted_at,omitempty"`
}

type ClaimBackendDeadlineCommand struct {
	TenantID          string
	WorkerSessionID   string
	WorkerRunID       string
	AgentSessionID    string
	AgentTurnID       string
	RuntimeInstanceID string
	RuntimeGeneration uint64
	ChannelEpoch      uint64
	DeadlineAt        time.Time
	SourceRef         string
}

type ClaimBackendDeadlineRecord struct {
	Command      ClaimBackendDeadlineCommand
	SettlementID string
	Source       TurnSettlementSource
	ClaimedAt    time.Time
}

type AdvanceBackendDeadlineSourceCommand struct {
	SettlementID        string
	ExpectedRevision    uint64
	ExpectedStateDigest string
	LeaseOwnerID        string
	LeaseFence          uint64
	ToPhase             BackendDeadlineSourcePhase

	InterruptMessageID      string
	InterruptIdempotencyKey string
	InterruptEnvelope       json.RawMessage
	ForceStop               *BackendDeadlineForceStopProof
	WriteFence              *BackendDeadlineWriteFenceProof
	Cleanup                 *BackendDeadlineCleanupProof
}

type AdvanceBackendDeadlineSourceRecord struct {
	Command                  AdvanceBackendDeadlineSourceCommand
	Transition               BackendDeadlineSourceTransition
	InterruptLogicalEnvelope *contractsv1.RuntimeControlEnvelope
	AdvancedAt               time.Time
}

type ApplyBackendDeadlineSourceCommand struct {
	SettlementID              string
	ExpectedRevision          uint64
	ExpectedSourceDigest      string
	ExpectedSourceStateDigest string
	LeaseOwnerID              string
	LeaseFence                uint64
}

type ApplyBackendDeadlineSourceRecord struct {
	Command   ApplyBackendDeadlineSourceCommand
	AppliedAt time.Time
}

type TurnSettlementSourceStore interface {
	ClaimBackendDeadlineTurnSettlement(
		context.Context,
		ClaimBackendDeadlineRecord,
	) (TurnSettlement, error)
	AdvanceBackendDeadlineSource(
		context.Context,
		AdvanceBackendDeadlineSourceRecord,
	) (TurnSettlement, error)
	ApplyBackendDeadlineTurnSettlement(
		context.Context,
		ApplyBackendDeadlineSourceRecord,
	) (TurnSettlement, error)
}

type TurnSettlementSourceService struct {
	Store TurnSettlementSourceStore
	Now   func() time.Time
}

// DecodeBackendDeadlineSourceState is the single strict read seam for the
// Runtime identity and proof chain owned by a Backend deadline source. The
// nullable top-level Runtime/event witness on TurnSettlement must not be used
// as a fallback for this source kind.
func DecodeBackendDeadlineSourceState(
	source TurnSettlementSource,
) (BackendDeadlineSourceState, error) {
	if source.Kind != TurnSettlementSourceBackendDeadline ||
		ValidateTurnSettlementSource(source) != nil {
		return BackendDeadlineSourceState{},
			ErrTurnSettlementSourceConflict
	}
	state, err := decodeTurnSettlementSourceState(source.State)
	if err != nil {
		return BackendDeadlineSourceState{},
			ErrTurnSettlementSourceConflict
	}
	return state, nil
}

func NewRuntimeEventTurnSettlementSource(
	occurredAt time.Time,
	sourceRef string,
	sourceDigest string,
) (TurnSettlementSource, error) {
	occurredAt = normalizePostgresTimestamp(occurredAt)
	state, stateDigest, err := CanonicalizeTurnSettlementSourceState(
		json.RawMessage(
			`{"phase":"terminal_observed","schema_version":1}`,
		),
	)
	if err != nil {
		return TurnSettlementSource{}, err
	}
	source := TurnSettlementSource{
		Kind:        TurnSettlementSourceRuntimeEvent,
		Reason:      "runtime_terminal",
		OccurredAt:  occurredAt,
		Ref:         sourceRef,
		Digest:      sourceDigest,
		State:       state,
		StateDigest: stateDigest,
	}
	if err := ValidateTurnSettlementSource(source); err != nil {
		return TurnSettlementSource{}, err
	}
	return source, nil
}

func (service TurnSettlementSourceService) ClaimBackendDeadline(
	ctx context.Context,
	command ClaimBackendDeadlineCommand,
) (TurnSettlement, error) {
	if service.Store == nil || service.Now == nil {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement source dependencies are required",
		)
	}
	record, err := NewBackendDeadlineClaimRecord(
		command,
		normalizePostgresTimestamp(service.Now()),
	)
	if err != nil {
		return TurnSettlement{}, err
	}
	return service.Store.ClaimBackendDeadlineTurnSettlement(ctx, record)
}

func (service TurnSettlementSourceService) AdvanceBackendDeadline(
	ctx context.Context,
	command AdvanceBackendDeadlineSourceCommand,
) (TurnSettlement, error) {
	if service.Store == nil || service.Now == nil {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement source dependencies are required",
		)
	}
	for name, value := range map[string]string{
		"settlement_id":  command.SettlementID,
		"lease_owner_id": command.LeaseOwnerID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return TurnSettlement{}, err
		}
	}
	if command.ExpectedRevision == 0 || command.LeaseFence == 0 ||
		validateRuntimeControlCursorDigest(
			command.ExpectedStateDigest,
		) != nil {
		return TurnSettlement{}, ErrTurnSettlementSourceConflict
	}
	record := AdvanceBackendDeadlineSourceRecord{
		Command: command,
		Transition: BackendDeadlineSourceTransition{
			ToPhase:    command.ToPhase,
			ForceStop:  cloneBackendDeadlineProof(command.ForceStop),
			WriteFence: cloneBackendDeadlineProof(command.WriteFence),
			Cleanup:    cloneBackendDeadlineProof(command.Cleanup),
		},
		AdvancedAt: normalizePostgresTimestamp(service.Now()),
	}
	if record.AdvancedAt.IsZero() {
		return TurnSettlement{}, ErrTurnSettlementSourceConflict
	}
	switch command.ToPhase {
	case BackendDeadlineSourceInterruptQueued:
		if command.ForceStop != nil || command.WriteFence != nil ||
			command.Cleanup != nil {
			return TurnSettlement{}, ErrTurnSettlementSourceConflict
		}
		envelope, err := decodeRuntimeControlCommandTemplate(
			command.InterruptMessageID,
			command.InterruptIdempotencyKey,
			command.InterruptEnvelope,
		)
		if err != nil || envelope.Kind != "command" ||
			envelope.MessageType != "agent_turn.interrupt" {
			return TurnSettlement{}, ErrTurnSettlementSourceConflict
		}
		payload, _, err := CanonicalizeTurnSettlementCheckpoint(
			envelope.Payload,
		)
		if err != nil || string(payload) != "{}" {
			return TurnSettlement{}, ErrTurnSettlementSourceConflict
		}
		record.InterruptLogicalEnvelope = &envelope
	case BackendDeadlineSourceForceStopIntent,
		BackendDeadlineSourceWriteFenced,
		BackendDeadlineSourceCleanupConfirmed:
		if command.InterruptMessageID != "" ||
			command.InterruptIdempotencyKey != "" ||
			len(command.InterruptEnvelope) != 0 ||
			validateBackendDeadlineTransitionShape(record.Transition) != nil {
			return TurnSettlement{}, ErrTurnSettlementSourceConflict
		}
	default:
		return TurnSettlement{}, ErrTurnSettlementSourceConflict
	}
	return service.Store.AdvanceBackendDeadlineSource(ctx, record)
}

func (service TurnSettlementSourceService) ApplyBackendDeadline(
	ctx context.Context,
	command ApplyBackendDeadlineSourceCommand,
) (TurnSettlement, error) {
	if service.Store == nil || service.Now == nil {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement source dependencies are required",
		)
	}
	for name, value := range map[string]string{
		"settlement_id":  command.SettlementID,
		"lease_owner_id": command.LeaseOwnerID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return TurnSettlement{}, err
		}
	}
	if command.ExpectedRevision == 0 ||
		command.LeaseFence == 0 ||
		validateRuntimeControlCursorDigest(
			command.ExpectedSourceDigest,
		) != nil ||
		validateRuntimeControlCursorDigest(
			command.ExpectedSourceStateDigest,
		) != nil {
		return TurnSettlement{}, ErrTurnSettlementSourceConflict
	}
	appliedAt := normalizePostgresTimestamp(service.Now())
	record := ApplyBackendDeadlineSourceRecord{
		Command:   command,
		AppliedAt: appliedAt,
	}
	if err := ValidateApplyBackendDeadlineSourceRecord(record); err != nil {
		return TurnSettlement{}, err
	}
	return service.Store.ApplyBackendDeadlineTurnSettlement(ctx, record)
}

func NewBackendDeadlineClaimRecord(
	command ClaimBackendDeadlineCommand,
	claimedAt time.Time,
) (ClaimBackendDeadlineRecord, error) {
	for name, value := range map[string]string{
		"tenant_id":           command.TenantID,
		"worker_session_id":   command.WorkerSessionID,
		"worker_run_id":       command.WorkerRunID,
		"agent_session_id":    command.AgentSessionID,
		"agent_turn_id":       command.AgentTurnID,
		"runtime_instance_id": command.RuntimeInstanceID,
		"source_ref":          command.SourceRef,
	} {
		if err := validateCanonical(name, value); err != nil {
			return ClaimBackendDeadlineRecord{}, err
		}
	}
	if command.RuntimeGeneration == 0 || command.ChannelEpoch == 0 ||
		command.DeadlineAt.IsZero() ||
		!command.DeadlineAt.Equal(
			normalizePostgresTimestamp(command.DeadlineAt),
		) {
		return ClaimBackendDeadlineRecord{},
			ErrTurnSettlementSourceConflict
	}
	claimedAt = normalizePostgresTimestamp(claimedAt)
	if claimedAt.IsZero() || claimedAt.Before(command.DeadlineAt) {
		return ClaimBackendDeadlineRecord{},
			ErrTurnSettlementSourceConflict
	}
	command.DeadlineAt = command.DeadlineAt.UTC()
	settlementID, err := TurnSettlementID(command.AgentTurnID)
	if err != nil {
		return ClaimBackendDeadlineRecord{}, err
	}
	state, stateDigest, err := initialBackendDeadlineSourceState(command)
	if err != nil {
		return ClaimBackendDeadlineRecord{}, err
	}
	sourceDigest, err := backendDeadlineSourceDigest(command)
	if err != nil {
		return ClaimBackendDeadlineRecord{}, err
	}
	source := TurnSettlementSource{
		Kind:        TurnSettlementSourceBackendDeadline,
		Reason:      "agent_turn_deadline",
		OccurredAt:  command.DeadlineAt,
		Ref:         command.SourceRef,
		Digest:      sourceDigest,
		State:       state,
		StateDigest: stateDigest,
	}
	if err := ValidateTurnSettlementSource(source); err != nil {
		return ClaimBackendDeadlineRecord{}, err
	}
	record := ClaimBackendDeadlineRecord{
		Command:      command,
		SettlementID: settlementID,
		Source:       source,
		ClaimedAt:    claimedAt,
	}
	if err := ValidateClaimBackendDeadlineRecord(record); err != nil {
		return ClaimBackendDeadlineRecord{}, err
	}
	return record, nil
}

func ValidateClaimBackendDeadlineRecord(
	record ClaimBackendDeadlineRecord,
) error {
	for name, value := range map[string]string{
		"tenant_id":           record.Command.TenantID,
		"worker_session_id":   record.Command.WorkerSessionID,
		"worker_run_id":       record.Command.WorkerRunID,
		"agent_session_id":    record.Command.AgentSessionID,
		"agent_turn_id":       record.Command.AgentTurnID,
		"runtime_instance_id": record.Command.RuntimeInstanceID,
		"source_ref":          record.Command.SourceRef,
	} {
		if err := validateCanonical(name, value); err != nil {
			return ErrTurnSettlementSourceConflict
		}
	}
	expectedSettlementID, err := TurnSettlementID(
		record.Command.AgentTurnID,
	)
	if err != nil || record.SettlementID != expectedSettlementID ||
		record.Command.RuntimeGeneration == 0 ||
		record.Command.ChannelEpoch == 0 ||
		record.Command.DeadlineAt.IsZero() ||
		!record.Command.DeadlineAt.Equal(
			normalizePostgresTimestamp(record.Command.DeadlineAt),
		) ||
		record.ClaimedAt.IsZero() ||
		!record.ClaimedAt.Equal(
			normalizePostgresTimestamp(record.ClaimedAt),
		) ||
		record.ClaimedAt.Before(record.Command.DeadlineAt) {
		return ErrTurnSettlementSourceConflict
	}
	if err := ValidateTurnSettlementSource(record.Source); err != nil ||
		record.Source.Kind != TurnSettlementSourceBackendDeadline ||
		record.Source.Ref != record.Command.SourceRef ||
		!record.Source.OccurredAt.Equal(record.Command.DeadlineAt) {
		return ErrTurnSettlementSourceConflict
	}
	expectedDigest, err := backendDeadlineSourceDigest(record.Command)
	if err != nil || record.Source.Digest != expectedDigest {
		return ErrTurnSettlementSourceConflict
	}
	expectedState, expectedStateDigest, err :=
		initialBackendDeadlineSourceState(record.Command)
	if err != nil ||
		record.Source.StateDigest != expectedStateDigest ||
		string(record.Source.State) != string(expectedState) {
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

func BindBackendDeadlineRecoveryPreemption(
	source TurnSettlementSource,
	incidentID string,
	incidentRevision uint64,
	preemptedAt time.Time,
) (TurnSettlementSource, error) {
	if err := ValidateTurnSettlementSource(source); err != nil ||
		source.Kind != TurnSettlementSourceBackendDeadline ||
		validateCanonical("recovery_incident_id", incidentID) != nil ||
		incidentRevision == 0 ||
		preemptedAt.IsZero() ||
		!preemptedAt.Equal(normalizePostgresTimestamp(preemptedAt)) {
		return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
	}
	state, err := decodeTurnSettlementSourceState(source.State)
	if err != nil {
		return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
	}
	if state.RecoveryIncidentID != "" {
		if state.RecoveryIncidentID == incidentID &&
			state.RecoveryIncidentRevision == incidentRevision &&
			state.RecoveryPreemptedAt.Equal(preemptedAt) {
			return source, nil
		}
		return TurnSettlementSource{}, ErrTurnSettlementSourceConflict
	}
	state.RecoveryIncidentID = incidentID
	state.RecoveryIncidentRevision = incidentRevision
	state.RecoveryPreemptedAt = preemptedAt
	encoded, err := json.Marshal(state)
	if err != nil {
		return TurnSettlementSource{}, fmt.Errorf(
			"encode Backend deadline recovery binding: %w",
			err,
		)
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

func CanonicalizeTurnSettlementSourceState(
	state json.RawMessage,
) (json.RawMessage, string, error) {
	canonical, digest, err := CanonicalizeTurnSettlementCheckpoint(state)
	if err != nil {
		return nil, "", fmt.Errorf(
			"%w: source state must be one JSON object",
			ErrTurnSettlementSourceConflict,
		)
	}
	return canonical, digest, nil
}

func ValidateTurnSettlementSource(source TurnSettlementSource) error {
	if source.OccurredAt.IsZero() ||
		!source.OccurredAt.Equal(
			normalizePostgresTimestamp(source.OccurredAt),
		) ||
		validateCanonical("source_reason", source.Reason) != nil ||
		validateCanonical("source_ref", source.Ref) != nil ||
		validateRuntimeControlCursorDigest(source.Digest) != nil ||
		validateRuntimeControlCursorDigest(source.StateDigest) != nil {
		return ErrTurnSettlementSourceConflict
	}
	canonical, digest, err := CanonicalizeTurnSettlementSourceState(
		source.State,
	)
	if err != nil || digest != source.StateDigest ||
		string(canonical) != string(source.State) {
		return ErrTurnSettlementSourceConflict
	}
	state, err := decodeTurnSettlementSourceState(canonical)
	if err != nil {
		return ErrTurnSettlementSourceConflict
	}
	switch source.Kind {
	case TurnSettlementSourceBackendDeadline:
		if source.Reason != "agent_turn_deadline" ||
			!source.OccurredAt.Equal(state.DeadlineAt) ||
			validateBackendDeadlineSourceState(state) != nil {
			return ErrTurnSettlementSourceConflict
		}
	case TurnSettlementSourceRuntimeEvent:
		if state.SchemaVersion != RuntimeEventSourceStateSchemaVersion ||
			state.RecoveryIncidentID != "" ||
			state.RecoveryIncidentRevision != 0 ||
			!state.RecoveryPreemptedAt.IsZero() ||
			source.Reason != "runtime_terminal" ||
			state.Phase != BackendDeadlineSourceTerminalObserved {
			return ErrTurnSettlementSourceConflict
		}
	default:
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

func decodeTurnSettlementSourceState(
	state json.RawMessage,
) (BackendDeadlineSourceState, error) {
	decoder := json.NewDecoder(bytes.NewReader(state))
	decoder.DisallowUnknownFields()
	var decoded BackendDeadlineSourceState
	if err := decoder.Decode(&decoded); err != nil {
		return BackendDeadlineSourceState{},
			ErrTurnSettlementSourceConflict
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BackendDeadlineSourceState{},
			ErrTurnSettlementSourceConflict
	}
	return decoded, nil
}

func initialBackendDeadlineSourceState(
	command ClaimBackendDeadlineCommand,
) (json.RawMessage, string, error) {
	encoded, err := json.Marshal(BackendDeadlineSourceState{
		SchemaVersion:     BackendDeadlineSourceStateSchemaVersion,
		Phase:             BackendDeadlineSourceClaimed,
		DeadlineAt:        command.DeadlineAt,
		ForceAfter:        command.DeadlineAt.Add(BackendDeadlineForceGrace),
		RuntimeInstanceID: command.RuntimeInstanceID,
		RuntimeGeneration: command.RuntimeGeneration,
		ChannelEpoch:      command.ChannelEpoch,
	})
	if err != nil {
		return nil, "", fmt.Errorf(
			"encode Backend deadline source state: %w",
			err,
		)
	}
	return CanonicalizeTurnSettlementSourceState(encoded)
}

func backendDeadlineSourceDigest(
	command ClaimBackendDeadlineCommand,
) (string, error) {
	encoded, err := json.Marshal(struct {
		SchemaVersion     uint32                   `json:"schema_version"`
		Kind              TurnSettlementSourceKind `json:"kind"`
		Reason            string                   `json:"reason"`
		TenantID          string                   `json:"tenant_id"`
		WorkerSessionID   string                   `json:"worker_session_id"`
		WorkerRunID       string                   `json:"worker_run_id"`
		AgentSessionID    string                   `json:"agent_session_id"`
		AgentTurnID       string                   `json:"agent_turn_id"`
		RuntimeInstanceID string                   `json:"runtime_instance_id"`
		RuntimeGeneration uint64                   `json:"runtime_generation"`
		ChannelEpoch      uint64                   `json:"channel_epoch"`
		OccurredAt        time.Time                `json:"occurred_at"`
		SourceRef         string                   `json:"source_ref"`
	}{
		SchemaVersion:     BackendDeadlineSourceStateSchemaVersion,
		Kind:              TurnSettlementSourceBackendDeadline,
		Reason:            "agent_turn_deadline",
		TenantID:          command.TenantID,
		WorkerSessionID:   command.WorkerSessionID,
		WorkerRunID:       command.WorkerRunID,
		AgentSessionID:    command.AgentSessionID,
		AgentTurnID:       command.AgentTurnID,
		RuntimeInstanceID: command.RuntimeInstanceID,
		RuntimeGeneration: command.RuntimeGeneration,
		ChannelEpoch:      command.ChannelEpoch,
		OccurredAt:        command.DeadlineAt,
		SourceRef:         command.SourceRef,
	})
	if err != nil {
		return "", fmt.Errorf(
			"encode Backend deadline source digest input: %w",
			err,
		)
	}
	return digestRuntimeControlBytes(encoded), nil
}

func validBackendDeadlineRecoveryBinding(
	state BackendDeadlineSourceState,
) bool {
	if state.RecoveryIncidentID == "" {
		return state.RecoveryIncidentRevision == 0 &&
			state.RecoveryPreemptedAt.IsZero()
	}
	return validateCanonical(
		"recovery_incident_id",
		state.RecoveryIncidentID,
	) == nil &&
		state.RecoveryIncidentRevision > 0 &&
		!state.RecoveryPreemptedAt.IsZero() &&
		state.RecoveryPreemptedAt.Equal(
			normalizePostgresTimestamp(state.RecoveryPreemptedAt),
		) &&
		!state.RecoveryPreemptedAt.Before(state.DeadlineAt)
}

func validBackendDeadlineSourcePhase(
	phase BackendDeadlineSourcePhase,
) bool {
	switch phase {
	case BackendDeadlineSourceClaimed,
		BackendDeadlineSourceInterruptQueued,
		BackendDeadlineSourceTerminalObserved,
		BackendDeadlineSourceForceStopIntent,
		BackendDeadlineSourceWriteFenced,
		BackendDeadlineSourceCleanupConfirmed:
		return true
	default:
		return false
	}
}
