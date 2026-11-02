package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	ErrTurnSettlementNotFound           = errors.New("Turn settlement not found")
	ErrTurnSettlementConflict           = errors.New("Turn settlement identity conflicts")
	ErrTurnSettlementRevisionConflict   = errors.New("Turn settlement revision conflicts")
	ErrTurnSettlementPhaseConflict      = errors.New("Turn settlement phase conflicts")
	ErrTurnSettlementLeaseHeld          = errors.New("Turn settlement lease is held")
	ErrTurnSettlementLeaseStale         = errors.New("Turn settlement lease is stale")
	ErrTurnSettlementCheckpointConflict = errors.New(
		"Turn settlement checkpoint conflicts",
	)
)

const TurnSettlementLeaseDuration = 30 * time.Second
const TurnSettlementCheckpointSchemaVersion uint32 = 1

type TurnSettlementPhase string

const (
	TurnSettlementAccepted             TurnSettlementPhase = "accepted"
	TurnSettlementEvidencePrepared     TurnSettlementPhase = "evidence_prepared"
	TurnSettlementDomainCommitted      TurnSettlementPhase = "domain_committed"
	TurnSettlementEvidencePublished    TurnSettlementPhase = "evidence_published"
	TurnSettlementTaskSettled          TurnSettlementPhase = "task_settled"
	TurnSettlementTerminalEventApplied TurnSettlementPhase = "terminal_event_applied"
	TurnSettlementContinuationSettled  TurnSettlementPhase = "continuation_settled"
	TurnSettlementCompleted            TurnSettlementPhase = "completed"
)

type TurnSettlement struct {
	SettlementID               string
	TenantID                   string
	WorkerSessionID            string
	WorkerRunID                string
	AgentSessionID             string
	AgentTurnID                string
	RuntimeInstanceID          string
	RuntimeGeneration          uint64
	ChannelEpoch               uint64
	TerminalEventSequence      uint64
	TerminalMessageID          string
	TerminalIdempotencyKey     string
	TerminalEnvelopeDigest     string
	TerminalEventDisposition   RuntimeControlEffectDisposition
	TerminalApplyOutcomeDigest string
	Source                     TurnSettlementSource
	Phase                      TurnSettlementPhase
	PhaseState                 json.RawMessage
	PhaseStateDigest           string
	Revision                   uint64
	LeaseOwnerID               string
	LeaseFence                 uint64
	LeaseExpiresAt             time.Time
	AcceptedAt                 time.Time
	UpdatedAt                  time.Time
	TerminalEventAppliedAt     time.Time
	CompletedAt                time.Time
}

type ClaimTurnSettlementCommand struct {
	SettlementID     string
	ExpectedRevision uint64
	OwnerID          string
}

type ClaimTurnSettlementRecord struct {
	Command        ClaimTurnSettlementCommand
	ClaimedAt      time.Time
	LeaseExpiresAt time.Time
}

type ClaimNextPendingTurnSettlementCommand struct {
	OwnerID string
}

type ClaimNextPendingTurnSettlementRecord struct {
	Command        ClaimNextPendingTurnSettlementCommand
	ClaimedAt      time.Time
	LeaseExpiresAt time.Time
}

type FrozenArtifactMetadata struct {
	ReportArtifactID string    `json:"report_artifact_id"`
	PatchArtifactID  string    `json:"patch_artifact_id"`
	CreatedAt        time.Time `json:"created_at"`
}

type TurnSettlementCheckpoint struct {
	SchemaVersion    uint32
	FrozenArtifacts  *FrozenArtifactMetadata
	AdditionalFields map[string]json.RawMessage
}

type FreezeTurnSettlementCheckpointCommand struct {
	SettlementID       string
	ExpectedRevision   uint64
	LeaseOwnerID       string
	LeaseFence         uint64
	Checkpoint         TurnSettlementCheckpoint
	ObservationCapture *RuntimeObservationCapture `json:"-"`
}

type FreezeTurnSettlementCheckpointRecord struct {
	Command             FreezeTurnSettlementCheckpointCommand
	CanonicalCheckpoint json.RawMessage
	CheckpointDigest    string
	FrozenAt            time.Time
}

type AdvanceTurnSettlementCommand struct {
	SettlementID     string
	ExpectedRevision uint64
	LeaseOwnerID     string
	LeaseFence       uint64
	FromPhase        TurnSettlementPhase
	ToPhase          TurnSettlementPhase
	Checkpoint       json.RawMessage
}

type AdvanceTurnSettlementRecord struct {
	Command          AdvanceTurnSettlementCommand
	CheckpointDigest string
	AdvancedAt       time.Time
}

type TurnSettlementStore interface {
	LoadTurnSettlement(context.Context, string) (TurnSettlement, error)
	ClaimTurnSettlement(context.Context, ClaimTurnSettlementRecord) (TurnSettlement, error)
	ClaimNextPendingTurnSettlement(context.Context, ClaimNextPendingTurnSettlementRecord) (TurnSettlement, error)
	FreezeTurnSettlementCheckpoint(
		context.Context,
		FreezeTurnSettlementCheckpointRecord,
	) (TurnSettlement, error)
	AdvanceTurnSettlement(context.Context, AdvanceTurnSettlementRecord) (TurnSettlement, error)
}

type TurnSettlementService struct {
	Store TurnSettlementStore
	Now   func() time.Time
}

func TurnSettlementID(agentTurnID string) (string, error) {
	if err := validateCanonical("agent_turn_id", agentTurnID); err != nil {
		return "", err
	}
	return "turn-settlement/" + agentTurnID, nil
}

func (service TurnSettlementService) Load(
	ctx context.Context,
	settlementID string,
) (TurnSettlement, error) {
	if service.Store == nil {
		return TurnSettlement{}, fmt.Errorf("Turn settlement store is required")
	}
	if err := validateCanonical("settlement_id", settlementID); err != nil {
		return TurnSettlement{}, err
	}
	return service.Store.LoadTurnSettlement(ctx, settlementID)
}

func (service TurnSettlementService) Claim(
	ctx context.Context,
	command ClaimTurnSettlementCommand,
) (TurnSettlement, error) {
	if service.Store == nil || service.Now == nil {
		return TurnSettlement{}, fmt.Errorf("Turn settlement dependencies are required")
	}
	if err := validateCanonical("settlement_id", command.SettlementID); err != nil {
		return TurnSettlement{}, err
	}
	if err := validateCanonical("owner_id", command.OwnerID); err != nil {
		return TurnSettlement{}, err
	}
	if command.ExpectedRevision == 0 {
		return TurnSettlement{}, fmt.Errorf("Turn settlement expected revision must be positive")
	}
	now := normalizePostgresTimestamp(service.Now())
	if now.IsZero() {
		return TurnSettlement{}, fmt.Errorf("Turn settlement claim time is required")
	}
	return service.Store.ClaimTurnSettlement(ctx, ClaimTurnSettlementRecord{
		Command: command, ClaimedAt: now,
		LeaseExpiresAt: now.Add(TurnSettlementLeaseDuration),
	})
}

func (service TurnSettlementService) ClaimNextPending(
	ctx context.Context,
	command ClaimNextPendingTurnSettlementCommand,
) (TurnSettlement, error) {
	if service.Store == nil || service.Now == nil {
		return TurnSettlement{}, fmt.Errorf("Turn settlement dependencies are required")
	}
	if err := validateCanonical("owner_id", command.OwnerID); err != nil {
		return TurnSettlement{}, err
	}
	now := normalizePostgresTimestamp(service.Now())
	if now.IsZero() {
		return TurnSettlement{}, fmt.Errorf("Turn settlement claim time is required")
	}
	return service.Store.ClaimNextPendingTurnSettlement(
		ctx,
		ClaimNextPendingTurnSettlementRecord{
			Command: command, ClaimedAt: now,
			LeaseExpiresAt: now.Add(TurnSettlementLeaseDuration),
		},
	)
}

func (service TurnSettlementService) FreezeCheckpoint(
	ctx context.Context,
	command FreezeTurnSettlementCheckpointCommand,
) (TurnSettlement, error) {
	if service.Store == nil || service.Now == nil {
		return TurnSettlement{}, fmt.Errorf("Turn settlement dependencies are required")
	}
	for name, value := range map[string]string{
		"settlement_id":  command.SettlementID,
		"lease_owner_id": command.LeaseOwnerID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return TurnSettlement{}, err
		}
	}
	if command.ExpectedRevision == 0 || command.LeaseFence == 0 {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement revision and lease fence must be positive",
		)
	}
	canonicalCheckpoint, checkpointDigest, err :=
		canonicalizeTypedTurnSettlementCheckpoint(command.Checkpoint)
	if err != nil {
		return TurnSettlement{}, err
	}
	if command.Checkpoint.FrozenArtifacts != nil {
		command.Checkpoint.FrozenArtifacts.CreatedAt =
			normalizePostgresTimestamp(command.Checkpoint.FrozenArtifacts.CreatedAt)
	}
	now := normalizePostgresTimestamp(service.Now())
	if now.IsZero() {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement checkpoint freeze time is required",
		)
	}
	if command.ObservationCapture != nil {
		capture := command.ObservationCapture.Clone()
		command.ObservationCapture = &capture
	}
	return service.Store.FreezeTurnSettlementCheckpoint(
		ctx,
		FreezeTurnSettlementCheckpointRecord{
			Command: command, CanonicalCheckpoint: canonicalCheckpoint,
			CheckpointDigest: checkpointDigest, FrozenAt: now,
		},
	)
}

func (service TurnSettlementService) Advance(
	ctx context.Context,
	command AdvanceTurnSettlementCommand,
) (TurnSettlement, error) {
	if service.Store == nil || service.Now == nil {
		return TurnSettlement{}, fmt.Errorf("Turn settlement dependencies are required")
	}
	for name, value := range map[string]string{
		"settlement_id":  command.SettlementID,
		"lease_owner_id": command.LeaseOwnerID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return TurnSettlement{}, err
		}
	}
	if command.ExpectedRevision == 0 || command.LeaseFence == 0 {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement revision and lease fence must be positive",
		)
	}
	if !command.FromPhase.valid() || !command.ToPhase.valid() ||
		!turnSettlementTransitionAllowed(command.FromPhase, command.ToPhase) {
		return TurnSettlement{}, ErrTurnSettlementPhaseConflict
	}
	// The event inbox, active-effect state and this phase must advance in one
	// PostgreSQL transaction. The generic phase API cannot cross that seam.
	if command.ToPhase == TurnSettlementDomainCommitted ||
		command.ToPhase == TurnSettlementTerminalEventApplied {
		return TurnSettlement{}, ErrTurnSettlementPhaseConflict
	}
	checkpoint, checkpointDigest, err := CanonicalizeTurnSettlementCheckpoint(
		command.Checkpoint,
	)
	if err != nil {
		return TurnSettlement{}, err
	}
	if err := validateTurnSettlementCheckpointSchema(checkpoint); err != nil {
		return TurnSettlement{}, err
	}
	if err := ValidateTurnSettlementCheckpointProof(
		command.ToPhase,
		checkpoint,
	); err != nil {
		return TurnSettlement{}, err
	}
	now := normalizePostgresTimestamp(service.Now())
	if now.IsZero() {
		return TurnSettlement{}, fmt.Errorf("Turn settlement advance time is required")
	}
	return service.Store.AdvanceTurnSettlement(ctx, AdvanceTurnSettlementRecord{
		Command: AdvanceTurnSettlementCommand{
			SettlementID:     command.SettlementID,
			ExpectedRevision: command.ExpectedRevision,
			LeaseOwnerID:     command.LeaseOwnerID,
			LeaseFence:       command.LeaseFence,
			FromPhase:        command.FromPhase,
			ToPhase:          command.ToPhase,
			Checkpoint:       checkpoint,
		},
		CheckpointDigest: checkpointDigest,
		AdvancedAt:       now,
	})
}

func canonicalizeTypedTurnSettlementCheckpoint(
	checkpoint TurnSettlementCheckpoint,
) (json.RawMessage, string, error) {
	if checkpoint.SchemaVersion != TurnSettlementCheckpointSchemaVersion {
		return nil, "", ErrTurnSettlementCheckpointConflict
	}
	fields := make(map[string]json.RawMessage, len(checkpoint.AdditionalFields)+2)
	for name, value := range checkpoint.AdditionalFields {
		if name == "" || name == "schema_version" || name == "frozen_artifacts" {
			return nil, "", fmt.Errorf(
				"%w: additional checkpoint field %q is reserved",
				ErrTurnSettlementCheckpointConflict,
				name,
			)
		}
		canonicalValue, err := canonicalizeJSONValue(value)
		if err != nil {
			return nil, "", fmt.Errorf(
				"%w: additional checkpoint field %q is invalid",
				ErrTurnSettlementCheckpointConflict,
				name,
			)
		}
		fields[name] = canonicalValue
	}
	fields["schema_version"] = json.RawMessage(`1`)
	if checkpoint.FrozenArtifacts != nil {
		for name, value := range map[string]string{
			"report_artifact_id": checkpoint.FrozenArtifacts.ReportArtifactID,
			"patch_artifact_id":  checkpoint.FrozenArtifacts.PatchArtifactID,
		} {
			if err := validateCanonical(name, value); err != nil {
				return nil, "", fmt.Errorf("%w: %v", ErrTurnSettlementCheckpointConflict, err)
			}
		}
		if checkpoint.FrozenArtifacts.CreatedAt.IsZero() {
			return nil, "", fmt.Errorf(
				"%w: frozen Artifact creation time is required",
				ErrTurnSettlementCheckpointConflict,
			)
		}
		frozen := *checkpoint.FrozenArtifacts
		frozen.CreatedAt = normalizePostgresTimestamp(frozen.CreatedAt)
		encodedFrozen, err := json.Marshal(frozen)
		if err != nil {
			return nil, "", fmt.Errorf(
				"%w: encode frozen Artifact metadata: %v",
				ErrTurnSettlementCheckpointConflict,
				err,
			)
		}
		fields["frozen_artifacts"] = encodedFrozen
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, "", fmt.Errorf(
			"%w: encode typed checkpoint: %v",
			ErrTurnSettlementCheckpointConflict,
			err,
		)
	}
	return CanonicalizeTurnSettlementCheckpoint(encoded)
}

func CanonicalizeTurnSettlementCheckpoint(
	checkpoint json.RawMessage,
) (json.RawMessage, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(checkpoint))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, "", fmt.Errorf(
			"Turn settlement checkpoint must be a JSON object",
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, "", fmt.Errorf(
			"Turn settlement checkpoint must contain one JSON object",
		)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, "", fmt.Errorf(
			"canonicalize Turn settlement checkpoint: %w",
			err,
		)
	}
	return canonical, digestRuntimeControlBytes(canonical), nil
}

func canonicalizeJSONValue(value json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("JSON value contains trailing content")
	}
	return json.Marshal(decoded)
}

func ValidateTurnSettlementCheckpointExtension(
	current json.RawMessage,
	next json.RawMessage,
) error {
	currentCanonical, _, err := CanonicalizeTurnSettlementCheckpoint(current)
	if err != nil {
		return fmt.Errorf("%w: current checkpoint is invalid", ErrTurnSettlementCheckpointConflict)
	}
	nextCanonical, _, err := CanonicalizeTurnSettlementCheckpoint(next)
	if err != nil {
		return fmt.Errorf("%w: next checkpoint is invalid", ErrTurnSettlementCheckpointConflict)
	}
	var currentFields, nextFields map[string]json.RawMessage
	if err := json.Unmarshal(currentCanonical, &currentFields); err != nil {
		return fmt.Errorf("%w: current checkpoint is invalid", ErrTurnSettlementCheckpointConflict)
	}
	if err := json.Unmarshal(nextCanonical, &nextFields); err != nil {
		return fmt.Errorf("%w: next checkpoint is invalid", ErrTurnSettlementCheckpointConflict)
	}
	if len(currentFields) == 0 ||
		validateTurnSettlementCheckpointSchema(currentCanonical) != nil ||
		validateTurnSettlementCheckpointSchema(nextCanonical) != nil {
		return ErrTurnSettlementCheckpointConflict
	}
	for name, currentValue := range currentFields {
		nextValue, exists := nextFields[name]
		if !exists || !bytes.Equal(currentValue, nextValue) {
			return fmt.Errorf(
				"%w: top-level field %q changed or disappeared",
				ErrTurnSettlementCheckpointConflict,
				name,
			)
		}
	}
	return nil
}

func ValidateInitialTurnSettlementCheckpoint(checkpoint json.RawMessage) error {
	canonical, _, err := CanonicalizeTurnSettlementCheckpoint(checkpoint)
	if err != nil || validateTurnSettlementCheckpointSchema(canonical) != nil {
		return ErrTurnSettlementCheckpointConflict
	}
	var envelope struct {
		FrozenArtifacts *FrozenArtifactMetadata `json:"frozen_artifacts"`
	}
	if err := json.Unmarshal(canonical, &envelope); err != nil ||
		envelope.FrozenArtifacts == nil {
		return fmt.Errorf(
			"%w: accepted checkpoint requires frozen Artifact metadata",
			ErrTurnSettlementCheckpointConflict,
		)
	}
	for name, value := range map[string]string{
		"report_artifact_id": envelope.FrozenArtifacts.ReportArtifactID,
		"patch_artifact_id":  envelope.FrozenArtifacts.PatchArtifactID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrTurnSettlementCheckpointConflict, err)
		}
	}
	if envelope.FrozenArtifacts.CreatedAt.IsZero() ||
		!envelope.FrozenArtifacts.CreatedAt.Equal(
			normalizePostgresTimestamp(envelope.FrozenArtifacts.CreatedAt),
		) {
		return fmt.Errorf(
			"%w: frozen Artifact timestamp must use PostgreSQL microsecond precision",
			ErrTurnSettlementCheckpointConflict,
		)
	}
	return nil
}

func validateTurnSettlementCheckpointSchema(checkpoint json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		return ErrTurnSettlementCheckpointConflict
	}
	var schemaVersion uint32
	if err := json.Unmarshal(fields["schema_version"], &schemaVersion); err != nil ||
		schemaVersion != TurnSettlementCheckpointSchemaVersion {
		return ErrTurnSettlementCheckpointConflict
	}
	return nil
}

func normalizePostgresTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func (phase TurnSettlementPhase) valid() bool {
	switch phase {
	case TurnSettlementAccepted,
		TurnSettlementEvidencePrepared,
		TurnSettlementDomainCommitted,
		TurnSettlementEvidencePublished,
		TurnSettlementTaskSettled,
		TurnSettlementTerminalEventApplied,
		TurnSettlementContinuationSettled,
		TurnSettlementCompleted:
		return true
	default:
		return false
	}
}

func turnSettlementTransitionAllowed(from, to TurnSettlementPhase) bool {
	switch from {
	case TurnSettlementAccepted:
		return to == TurnSettlementEvidencePrepared
	case TurnSettlementEvidencePrepared:
		return to == TurnSettlementDomainCommitted
	case TurnSettlementDomainCommitted:
		return to == TurnSettlementEvidencePublished
	case TurnSettlementEvidencePublished:
		return to == TurnSettlementTaskSettled
	case TurnSettlementTaskSettled:
		return to == TurnSettlementTerminalEventApplied
	case TurnSettlementTerminalEventApplied:
		return to == TurnSettlementContinuationSettled
	case TurnSettlementContinuationSettled:
		return to == TurnSettlementCompleted
	default:
		return false
	}
}
