package application

import (
	"encoding/json"
	"fmt"
	"time"
)

const backendDeadlineSourceAppliedField = "backend_deadline_source_applied"

type backendDeadlineSourceAppliedProof struct {
	Kind                     TurnSettlementSourceKind `json:"kind"`
	Reason                   string                   `json:"reason"`
	OccurredAt               time.Time                `json:"occurred_at"`
	SourceRef                string                   `json:"source_ref"`
	SourceDigest             string                   `json:"source_digest"`
	SourceStateDigest        string                   `json:"source_state_digest"`
	CleanupConfirmedAt       time.Time                `json:"cleanup_confirmed_at"`
	CleanupObservationDigest string                   `json:"cleanup_observation_digest"`
	AppliedFromRevision      uint64                   `json:"applied_from_revision"`
	LeaseOwnerID             string                   `json:"lease_owner_id"`
	LeaseFence               uint64                   `json:"lease_fence"`
	RuntimeInstanceID        string                   `json:"runtime_instance_id"`
	RuntimeGeneration        uint64                   `json:"runtime_generation"`
	SessionStatus            string                   `json:"session_status"`
	SessionRevision          uint64                   `json:"session_revision"`
	RuntimeRevision          uint64                   `json:"runtime_revision"`
}

type BackendDeadlineSourceApplyTargetAuthority struct {
	ExpectedSessionStatus   string
	SessionStatus           string
	ExpectedSessionRevision uint64
	SessionRevision         uint64
	ExpectedRuntimeRevision uint64
	RuntimeRevision         uint64
}

func ValidateApplyBackendDeadlineSourceRecord(
	record ApplyBackendDeadlineSourceRecord,
) error {
	for name, value := range map[string]string{
		"settlement_id":  record.Command.SettlementID,
		"lease_owner_id": record.Command.LeaseOwnerID,
	} {
		if validateCanonical(name, value) != nil {
			return ErrTurnSettlementSourceConflict
		}
	}
	if record.Command.ExpectedRevision == 0 ||
		record.Command.LeaseFence == 0 ||
		validateRuntimeControlCursorDigest(
			record.Command.ExpectedSourceDigest,
		) != nil ||
		validateRuntimeControlCursorDigest(
			record.Command.ExpectedSourceStateDigest,
		) != nil ||
		record.AppliedAt.IsZero() ||
		!record.AppliedAt.Equal(
			normalizePostgresTimestamp(record.AppliedAt),
		) {
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

// BuildBackendDeadlineSourceAppliedCheckpoint extends a task-settled
// checkpoint with an exact proof that the Backend deadline source, rather than
// a Runtime terminal event, owns terminal application. The returned outcome
// digest is over the source-specific proof object and is stable across
// response-loss retries.
func BuildBackendDeadlineSourceAppliedCheckpoint(
	checkpoint json.RawMessage,
	source TurnSettlementSource,
	authority ApplyBackendDeadlineSourceCommand,
) (
	json.RawMessage,
	string,
	string,
	error,
) {
	if err := ValidateTurnSettlementCheckpointProof(
		TurnSettlementTaskSettled,
		checkpoint,
	); err != nil {
		return nil, "", "", err
	}
	target, err := ResolveBackendDeadlineSourceApplyTarget(
		checkpoint,
		source,
	)
	if err != nil {
		return nil, "", "", err
	}
	proof, err := backendDeadlineSourceAppliedProofFromSource(
		source,
		authority,
		target,
	)
	if err != nil {
		return nil, "", "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(checkpoint, &fields); err != nil ||
		fields == nil {
		return nil, "", "",
			ErrTurnSettlementCheckpointConflict
	}
	if _, exists := fields["terminal_source_applied"]; exists {
		return nil, "", "",
			ErrTurnSettlementCheckpointConflict
	}
	if _, exists := fields[backendDeadlineSourceAppliedField]; exists {
		return nil, "", "",
			ErrTurnSettlementCheckpointConflict
	}
	encodedProof, err := json.Marshal(proof)
	if err != nil {
		return nil, "", "", fmt.Errorf(
			"encode Backend deadline source-applied proof: %w",
			err,
		)
	}
	canonicalProof, outcomeDigest, err :=
		CanonicalizeTurnSettlementCheckpoint(encodedProof)
	if err != nil {
		return nil, "", "", err
	}
	fields["terminal_source_applied"] = json.RawMessage(`true`)
	fields[backendDeadlineSourceAppliedField] = canonicalProof
	candidate, err := json.Marshal(fields)
	if err != nil {
		return nil, "", "", fmt.Errorf(
			"encode Backend deadline source-applied checkpoint: %w",
			err,
		)
	}
	canonical, digest, err :=
		CanonicalizeTurnSettlementCheckpoint(candidate)
	if err != nil {
		return nil, "", "", err
	}
	if err := ValidateTurnSettlementCheckpointExtension(
		checkpoint,
		canonical,
	); err != nil {
		return nil, "", "", err
	}
	if err := ValidateTurnSettlementCheckpointProof(
		TurnSettlementTerminalEventApplied,
		canonical,
	); err != nil {
		return nil, "", "", err
	}
	if err := ValidateBackendDeadlineSourceAppliedCheckpoint(
		source,
		canonical,
		outcomeDigest,
		authority,
	); err != nil {
		return nil, "", "", err
	}
	return canonical, digest, outcomeDigest, nil
}

func ValidateBackendDeadlineSourceAppliedCheckpoint(
	source TurnSettlementSource,
	checkpoint json.RawMessage,
	outcomeDigest string,
	authority ApplyBackendDeadlineSourceCommand,
) error {
	expectedOutcomeDigest, err :=
		validateBackendDeadlineSourceAppliedCheckpointProof(
			source,
			checkpoint,
			authority,
		)
	if err != nil ||
		validateRuntimeControlCursorDigest(outcomeDigest) != nil ||
		outcomeDigest != expectedOutcomeDigest {
		return ErrTurnSettlementSourceConflict
	}
	return nil
}

// ValidateBackendDeadlineSourceAppliedCheckpointProof verifies the durable
// source-specific checkpoint independently from a Runtime event's own Apply
// outcome digest. A forced late terminal event records its ignore outcome in
// the inbox while this proof continues to attest the selected Backend source.
func ValidateBackendDeadlineSourceAppliedCheckpointProof(
	source TurnSettlementSource,
	checkpoint json.RawMessage,
	authority ApplyBackendDeadlineSourceCommand,
) error {
	_, err := validateBackendDeadlineSourceAppliedCheckpointProof(
		source,
		checkpoint,
		authority,
	)
	return err
}

func validateBackendDeadlineSourceAppliedCheckpointProof(
	source TurnSettlementSource,
	checkpoint json.RawMessage,
	authority ApplyBackendDeadlineSourceCommand,
) (string, error) {
	if err := ValidateTurnSettlementCheckpointProof(
		TurnSettlementTerminalEventApplied,
		checkpoint,
	); err != nil {
		return "", err
	}
	target, err := ResolveBackendDeadlineSourceApplyTarget(
		checkpoint,
		source,
	)
	if err != nil {
		return "", err
	}
	expected, err := backendDeadlineSourceAppliedProofFromSource(
		source,
		authority,
		target,
	)
	if err != nil {
		return "", err
	}
	var proof turnSettlementCheckpointProof
	if err := json.Unmarshal(checkpoint, &proof); err != nil ||
		proof.BackendDeadlineSourceApplied == nil ||
		*proof.BackendDeadlineSourceApplied != expected {
		return "", ErrTurnSettlementSourceConflict
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		return "", fmt.Errorf(
			"encode expected Backend deadline source-applied proof: %w",
			err,
		)
	}
	_, expectedOutcomeDigest, err :=
		CanonicalizeTurnSettlementCheckpoint(encoded)
	if err != nil {
		return "", ErrTurnSettlementSourceConflict
	}
	return expectedOutcomeDigest, nil
}

func ResolveBackendDeadlineSourceApplyTarget(
	checkpoint json.RawMessage,
	source TurnSettlementSource,
) (BackendDeadlineSourceApplyTargetAuthority, error) {
	if err := ValidateTurnSettlementCheckpointProof(
		TurnSettlementTaskSettled,
		checkpoint,
	); err != nil {
		return BackendDeadlineSourceApplyTargetAuthority{}, err
	}
	state, err := DecodeBackendDeadlineSourceState(source)
	if err != nil ||
		state.Phase != BackendDeadlineSourceCleanupConfirmed ||
		state.ForceStop == nil {
		return BackendDeadlineSourceApplyTargetAuthority{},
			ErrTurnSettlementSourceConflict
	}
	if state.ForceStop.ExpectedSessionRevision == ^uint64(0) ||
		state.ForceStop.ExpectedRuntimeRevision == ^uint64(0) {
		return BackendDeadlineSourceApplyTargetAuthority{},
			ErrTurnSettlementCheckpointConflict
	}
	var checkpointProof turnSettlementCheckpointProof
	if err := json.Unmarshal(
		checkpoint,
		&checkpointProof,
	); err != nil ||
		checkpointProof.Domain == nil ||
		checkpointProof.DomainPlan == nil ||
		checkpointProof.TaskPlan == nil ||
		checkpointProof.TaskResult == nil ||
		checkpointProof.DomainPlan.SessionTarget != "waiting_user" ||
		checkpointProof.DomainPlan.RunTarget != "timed_out" ||
		checkpointProof.DomainPlan.TurnTarget != "timed_out" ||
		checkpointProof.DomainPlan.TerminalReason !=
			"agent_turn_deadline" ||
		checkpointProof.Domain.SessionRevision !=
			state.ForceStop.ExpectedSessionRevision+1 ||
		checkpointProof.Domain.RuntimeRevision !=
			state.ForceStop.ExpectedRuntimeRevision {
		return BackendDeadlineSourceApplyTargetAuthority{},
			ErrTurnSettlementCheckpointConflict
	}
	target := BackendDeadlineSourceApplyTargetAuthority{
		ExpectedSessionStatus:   "waiting_user",
		SessionStatus:           "waiting_user",
		ExpectedSessionRevision: checkpointProof.Domain.SessionRevision,
		ExpectedRuntimeRevision: checkpointProof.Domain.RuntimeRevision,
	}
	if checkpointProof.TaskPlan.Action == "settle_cancelled" {
		if checkpointProof.Domain.SessionRevision == ^uint64(0) ||
			checkpointProof.TaskResult.Revision !=
				checkpointProof.Domain.SessionRevision+1 {
			return BackendDeadlineSourceApplyTargetAuthority{},
				ErrTurnSettlementCheckpointConflict
		}
		target.ExpectedSessionStatus = "interactive"
		target.ExpectedSessionRevision =
			checkpointProof.TaskResult.Revision
	}
	if target.ExpectedSessionRevision == ^uint64(0) ||
		target.ExpectedRuntimeRevision == ^uint64(0) {
		return BackendDeadlineSourceApplyTargetAuthority{},
			ErrTurnSettlementCheckpointConflict
	}
	target.SessionRevision = target.ExpectedSessionRevision + 1
	target.RuntimeRevision = target.ExpectedRuntimeRevision + 1
	return target, nil
}

func backendDeadlineSourceAppliedProofFromSource(
	source TurnSettlementSource,
	authority ApplyBackendDeadlineSourceCommand,
	target BackendDeadlineSourceApplyTargetAuthority,
) (backendDeadlineSourceAppliedProof, error) {
	state, err := DecodeBackendDeadlineSourceState(source)
	if err != nil ||
		state.Phase != BackendDeadlineSourceCleanupConfirmed ||
		state.Cleanup == nil ||
		state.ForceStop == nil ||
		state.Cleanup.ConfirmedAt.IsZero() ||
		state.Cleanup.ObservationDigest == "" ||
		target.ExpectedSessionStatus == "" ||
		target.SessionStatus == "" ||
		target.ExpectedSessionRevision == 0 ||
		target.SessionRevision == 0 ||
		target.ExpectedRuntimeRevision == 0 ||
		target.RuntimeRevision == 0 {
		return backendDeadlineSourceAppliedProof{},
			ErrTurnSettlementSourceConflict
	}
	proof := backendDeadlineSourceAppliedProof{
		Kind:                     source.Kind,
		Reason:                   source.Reason,
		OccurredAt:               source.OccurredAt,
		SourceRef:                source.Ref,
		SourceDigest:             source.Digest,
		SourceStateDigest:        source.StateDigest,
		CleanupConfirmedAt:       state.Cleanup.ConfirmedAt,
		CleanupObservationDigest: state.Cleanup.ObservationDigest,
		AppliedFromRevision:      authority.ExpectedRevision,
		LeaseOwnerID:             authority.LeaseOwnerID,
		LeaseFence:               authority.LeaseFence,
		RuntimeInstanceID:        state.RuntimeInstanceID,
		RuntimeGeneration:        state.RuntimeGeneration,
		SessionStatus:            target.SessionStatus,
		SessionRevision:          target.SessionRevision,
		RuntimeRevision:          target.RuntimeRevision,
	}
	if err := validateBackendDeadlineSourceAppliedProof(proof); err != nil {
		return backendDeadlineSourceAppliedProof{}, err
	}
	return proof, nil
}

func validateBackendDeadlineSourceAppliedProof(
	proof backendDeadlineSourceAppliedProof,
) error {
	for name, value := range map[string]string{
		"source reason":              proof.Reason,
		"source ref":                 proof.SourceRef,
		"source digest":              proof.SourceDigest,
		"source state digest":        proof.SourceStateDigest,
		"cleanup observation digest": proof.CleanupObservationDigest,
		"lease owner id":             proof.LeaseOwnerID,
		"runtime instance id":        proof.RuntimeInstanceID,
		"session status":             proof.SessionStatus,
	} {
		if value == "" {
			return fmt.Errorf(
				"%w: %s is required",
				ErrTurnSettlementCheckpointConflict,
				name,
			)
		}
	}
	if proof.Kind != TurnSettlementSourceBackendDeadline ||
		proof.Reason != "agent_turn_deadline" ||
		proof.OccurredAt.IsZero() ||
		proof.CleanupConfirmedAt.IsZero() ||
		proof.CleanupConfirmedAt.Before(proof.OccurredAt) ||
		proof.AppliedFromRevision == 0 ||
		proof.LeaseFence == 0 ||
		proof.RuntimeGeneration == 0 ||
		proof.SessionRevision == 0 ||
		proof.RuntimeRevision == 0 ||
		(proof.SessionStatus != "waiting_user" &&
			proof.SessionStatus != "interactive") ||
		validateCanonical("lease owner id", proof.LeaseOwnerID) != nil ||
		validateCanonical(
			"runtime instance id",
			proof.RuntimeInstanceID,
		) != nil ||
		validateRuntimeControlCursorDigest(proof.SourceDigest) != nil ||
		validateRuntimeControlCursorDigest(proof.SourceStateDigest) != nil ||
		validateRuntimeControlCursorDigest(
			proof.CleanupObservationDigest,
		) != nil {
		return ErrTurnSettlementCheckpointConflict
	}
	return nil
}
