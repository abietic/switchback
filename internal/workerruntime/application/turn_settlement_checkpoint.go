package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

type turnSettlementCheckpointProof struct {
	SchemaVersion uint32 `json:"schema_version"`

	FrozenArtifacts              *FrozenArtifactMetadata                `json:"frozen_artifacts"`
	Source                       *turnSettlementSourceProof             `json:"source"`
	EvidenceInput                *turnSettlementEvidenceInputProof      `json:"evidence_input"`
	DomainPlan                   *turnSettlementDomainPlanProof         `json:"domain_plan"`
	Evidence                     *turnSettlementEvidenceProof           `json:"evidence"`
	Domain                       *turnSettlementDomainProof             `json:"domain"`
	Publication                  *turnSettlementPublicationProof        `json:"publication"`
	TaskPlan                     *turnSettlementTaskPlanProof           `json:"task_plan"`
	TaskResult                   *turnSettlementTaskResultProof         `json:"task_result"`
	SourceApplied                bool                                   `json:"terminal_source_applied"`
	BackendDeadlineSourceApplied *backendDeadlineSourceAppliedProof     `json:"backend_deadline_source_applied"`
	ContinuationPlan             *turnSettlementContinuationPlanProof   `json:"continuation_plan"`
	ContinuationResult           *turnSettlementContinuationResultProof `json:"continuation_result"`
}

type turnSettlementSourceProof struct {
	Kind       string    `json:"kind"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
	TimedOut   bool      `json:"timed_out"`
	ForcedStop bool      `json:"forced_stop"`
}

type turnSettlementEvidenceInputProof struct {
	TenantID                      string    `json:"tenant_id"`
	UserID                        string    `json:"user_id"`
	WorkspaceID                   string    `json:"workspace_id"`
	TaskID                        string    `json:"task_id"`
	WorkerSessionID               string    `json:"worker_session_id"`
	WorkerRunID                   string    `json:"worker_run_id"`
	AgentSessionID                string    `json:"agent_session_id"`
	AgentTurnID                   string    `json:"agent_turn_id"`
	RuntimeInstanceID             string    `json:"runtime_instance_id"`
	RuntimeGeneration             uint64    `json:"runtime_generation"`
	NetworkPolicyID               string    `json:"network_policy_id"`
	NetworkPolicyRevision         uint64    `json:"network_policy_revision"`
	NetworkPolicyDigest           string    `json:"network_policy_digest"`
	NetworkEnforcementStatus      string    `json:"network_enforcement_status"`
	TraceStartOffset              int64     `json:"trace_start_offset"`
	TraceEndOffset                int64     `json:"trace_end_offset"`
	TraceDigest                   string    `json:"trace_digest"`
	PatchSnapshotFile             string    `json:"patch_snapshot_file"`
	PatchSize                     int64     `json:"patch_size"`
	PatchDigest                   string    `json:"patch_digest"`
	ObservationSourceSnapshotFile string    `json:"observation_source_snapshot_file,omitempty"`
	ObservationSourceSize         int64     `json:"observation_source_size,omitempty"`
	ObservationSourceDigest       string    `json:"observation_source_digest,omitempty"`
	NoFilesChanged                bool      `json:"no_files_changed"`
	ServerRecordedAt              time.Time `json:"server_recorded_at"`
	WorkerRecordedAt              time.Time `json:"worker_recorded_at"`
	ReportArtifactID              string    `json:"report_artifact_id"`
	PatchArtifactID               string    `json:"patch_artifact_id"`
	ArtifactCreatedAt             time.Time `json:"artifact_created_at"`
	SourceCheckpointID            string    `json:"source_checkpoint_id"`
	SourceCheckpointDigest        string    `json:"source_checkpoint_digest"`
}

type turnSettlementDomainPlanProof struct {
	SessionTarget           string `json:"session_target"`
	RunTarget               string `json:"run_target"`
	TurnTarget              string `json:"turn_target"`
	TerminalReason          string `json:"terminal_reason"`
	ExpectedSessionRevision uint64 `json:"expected_session_revision"`
	ExpectedRunRevision     uint64 `json:"expected_run_revision"`
	ExpectedTurnRevision    uint64 `json:"expected_turn_revision"`
	ExpectedRuntimeRevision uint64 `json:"expected_runtime_revision"`
}

type turnSettlementEvidenceProof struct {
	ManifestDigest string                               `json:"manifest_digest"`
	Finalized      turnSettlementFinalizedEvidenceProof `json:"finalized"`
}

type turnSettlementFinalizedEvidenceProof struct {
	Manifest       contractsv1.TraceManifest            `json:"Manifest"`
	ManifestObject StoredObject                         `json:"ManifestObject"`
	ArtifactRefs   []contractsv1.ArtifactRef            `json:"ArtifactRefs"`
	Objects        []turnSettlementFinalizedObjectProof `json:"Objects"`
}

type turnSettlementFinalizedObjectProof struct {
	Kind   string       `json:"Kind"`
	Stored StoredObject `json:"Stored"`
}

type turnSettlementDomainProof struct {
	SessionRevision uint64 `json:"session_revision"`
	RunRevision     uint64 `json:"run_revision"`
	TurnRevision    uint64 `json:"turn_revision"`
	RuntimeRevision uint64 `json:"runtime_revision"`
}

type turnSettlementPublicationProof struct {
	TraceStreamID string   `json:"trace_stream_id"`
	ArtifactIDs   []string `json:"artifact_ids"`
}

type turnSettlementTaskPlanProof struct {
	Action               string `json:"action"`
	TaskID               string `json:"task_id"`
	ExpectedTaskRevision uint64 `json:"expected_task_revision"`
	IdempotencyKey       string `json:"idempotency_key"`
	Reason               string `json:"reason"`
}

type turnSettlementTaskResultProof struct {
	Status   string `json:"status"`
	Revision uint64 `json:"revision"`
	EventID  string `json:"event_id"`
}

type turnSettlementContinuationPlanProof struct {
	Action                  string `json:"action"`
	Reason                  string `json:"reason"`
	QueueCommandID          string `json:"queue_command_id"`
	ExpectedSessionRevision uint64 `json:"expected_session_revision"`
	ExpectedRunRevision     uint64 `json:"expected_run_revision"`
	ExpectedTurnRevision    uint64 `json:"expected_turn_revision"`
	ExpectedRuntimeRevision uint64 `json:"expected_runtime_revision"`
}

type turnSettlementContinuationResultProof struct {
	Status      string `json:"status"`
	WorkerRunID string `json:"worker_run_id"`
	AgentTurnID string `json:"agent_turn_id"`
}

// ValidateTurnSettlementCheckpointProof verifies that a cumulative checkpoint
// contains the exact durable proof required by phase. It intentionally owns a
// stable contract/scalar JSON projection rather than importing Artifact/Trace
// application types, so settlement persistence cannot acquire an import cycle.
func ValidateTurnSettlementCheckpointProof(
	phase TurnSettlementPhase,
	checkpoint json.RawMessage,
) error {
	if !phase.valid() {
		return turnSettlementCheckpointProofError("phase %q is unsupported", phase)
	}
	decoder := json.NewDecoder(bytes.NewReader(checkpoint))
	decoder.DisallowUnknownFields()
	var proof turnSettlementCheckpointProof
	if err := decoder.Decode(&proof); err != nil {
		return turnSettlementCheckpointProofError("decode proof: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return turnSettlementCheckpointProofError("proof must contain one JSON object")
	}
	if proof.SchemaVersion != TurnSettlementCheckpointSchemaVersion {
		return turnSettlementCheckpointProofError("schema version is unsupported")
	}
	if err := ValidateInitialTurnSettlementCheckpoint(checkpoint); err != nil {
		return turnSettlementCheckpointProofError(
			"initial frozen Artifact proof is invalid",
		)
	}
	if err := validateTurnSettlementFrozenProof(proof); err != nil {
		return err
	}
	if turnSettlementPhaseAtLeast(phase, TurnSettlementEvidencePrepared) {
		if err := validateTurnSettlementEvidenceProof(proof); err != nil {
			return err
		}
	}
	if turnSettlementPhaseAtLeast(phase, TurnSettlementDomainCommitted) {
		if err := validateTurnSettlementDomainProof(proof); err != nil {
			return err
		}
	}
	if turnSettlementPhaseAtLeast(phase, TurnSettlementEvidencePublished) {
		if err := validateTurnSettlementPublicationProof(proof); err != nil {
			return err
		}
	}
	if turnSettlementPhaseAtLeast(phase, TurnSettlementTaskSettled) {
		if err := validateTurnSettlementTaskProof(proof); err != nil {
			return err
		}
	}
	if turnSettlementPhaseAtLeast(
		phase,
		TurnSettlementTerminalEventApplied,
	) && !proof.SourceApplied {
		return turnSettlementCheckpointProofError(
			"terminal source-applied proof is missing",
		)
	}
	if proof.BackendDeadlineSourceApplied != nil {
		if proof.Source == nil ||
			proof.Source.Kind != string(TurnSettlementSourceBackendDeadline) {
			return turnSettlementCheckpointProofError(
				"Backend deadline source-applied proof has a different source",
			)
		}
		if err := validateBackendDeadlineSourceAppliedProof(
			*proof.BackendDeadlineSourceApplied,
		); err != nil {
			return turnSettlementCheckpointProofError(
				"Backend deadline source-applied proof is invalid",
			)
		}
	}
	if turnSettlementPhaseAtLeast(phase, TurnSettlementContinuationSettled) {
		if err := validateTurnSettlementContinuationProof(proof); err != nil {
			return err
		}
	}
	return nil
}

func validateTurnSettlementFrozenProof(proof turnSettlementCheckpointProof) error {
	if proof.FrozenArtifacts == nil ||
		proof.Source == nil ||
		proof.EvidenceInput == nil ||
		proof.DomainPlan == nil {
		return turnSettlementCheckpointProofError("frozen proof is incomplete")
	}
	for name, value := range map[string]string{
		"source kind":                proof.Source.Kind,
		"source reason":              proof.Source.Reason,
		"tenant id":                  proof.EvidenceInput.TenantID,
		"user id":                    proof.EvidenceInput.UserID,
		"workspace id":               proof.EvidenceInput.WorkspaceID,
		"worker session id":          proof.EvidenceInput.WorkerSessionID,
		"worker run id":              proof.EvidenceInput.WorkerRunID,
		"agent session id":           proof.EvidenceInput.AgentSessionID,
		"agent turn id":              proof.EvidenceInput.AgentTurnID,
		"runtime instance id":        proof.EvidenceInput.RuntimeInstanceID,
		"network policy id":          proof.EvidenceInput.NetworkPolicyID,
		"network enforcement status": proof.EvidenceInput.NetworkEnforcementStatus,
		"patch snapshot file":        proof.EvidenceInput.PatchSnapshotFile,
		"report artifact id":         proof.EvidenceInput.ReportArtifactID,
		"patch artifact id":          proof.EvidenceInput.PatchArtifactID,
		"source checkpoint id":       proof.EvidenceInput.SourceCheckpointID,
		"terminal reason":            proof.DomainPlan.TerminalReason,
	} {
		if err := validateCanonical(name, value); err != nil {
			return turnSettlementCheckpointProofError("%v", err)
		}
	}
	if proof.Source.OccurredAt.IsZero() ||
		proof.EvidenceInput.RuntimeGeneration == 0 ||
		proof.EvidenceInput.NetworkPolicyRevision == 0 ||
		proof.EvidenceInput.TraceStartOffset < 0 ||
		proof.EvidenceInput.TraceEndOffset <= proof.EvidenceInput.TraceStartOffset ||
		proof.EvidenceInput.PatchSize < 0 ||
		proof.EvidenceInput.ServerRecordedAt.IsZero() ||
		proof.EvidenceInput.WorkerRecordedAt.IsZero() ||
		proof.EvidenceInput.ArtifactCreatedAt.IsZero() ||
		proof.DomainPlan.ExpectedSessionRevision == 0 ||
		proof.DomainPlan.ExpectedRunRevision == 0 ||
		proof.DomainPlan.ExpectedTurnRevision == 0 ||
		proof.DomainPlan.ExpectedRuntimeRevision == 0 {
		return turnSettlementCheckpointProofError("frozen scalar proof is incomplete")
	}
	if proof.DomainPlan.TerminalReason != proof.Source.Reason {
		return turnSettlementCheckpointProofError(
			"domain plan terminal reason differs from frozen source",
		)
	}
	switch proof.Source.Kind {
	case string(TurnOutcomeSourceRuntimeEvent):
		if proof.Source.TimedOut {
			return turnSettlementCheckpointProofError(
				"Runtime event source cannot carry deadline authority",
			)
		}
	case string(TurnOutcomeSourceBackendDeadline):
		if !proof.Source.TimedOut ||
			proof.Source.Reason != "agent_turn_deadline" {
			return turnSettlementCheckpointProofError(
				"backend deadline source proof is inconsistent",
			)
		}
	default:
		return turnSettlementCheckpointProofError(
			"terminal source kind %q is unsupported",
			proof.Source.Kind,
		)
	}
	if filepath.Base(proof.EvidenceInput.PatchSnapshotFile) !=
		proof.EvidenceInput.PatchSnapshotFile {
		return turnSettlementCheckpointProofError("patch snapshot reference is invalid")
	}
	if err := validateTurnSettlementObservationSourceInput(*proof.EvidenceInput); err != nil {
		return err
	}
	for name, digest := range map[string]string{
		"network policy digest":    proof.EvidenceInput.NetworkPolicyDigest,
		"trace digest":             proof.EvidenceInput.TraceDigest,
		"patch digest":             proof.EvidenceInput.PatchDigest,
		"source checkpoint digest": proof.EvidenceInput.SourceCheckpointDigest,
	} {
		if err := validateRuntimeControlCursorDigest(digest); err != nil {
			return turnSettlementCheckpointProofError("%s must use sha256", name)
		}
	}
	if proof.EvidenceInput.NoFilesChanged !=
		(proof.EvidenceInput.PatchSize == 0) {
		return turnSettlementCheckpointProofError(
			"patch size and no-files-changed proof disagree",
		)
	}
	if proof.FrozenArtifacts.ReportArtifactID !=
		proof.EvidenceInput.ReportArtifactID ||
		proof.FrozenArtifacts.PatchArtifactID !=
			proof.EvidenceInput.PatchArtifactID ||
		proof.FrozenArtifacts.ReportArtifactID ==
			proof.FrozenArtifacts.PatchArtifactID ||
		!proof.FrozenArtifacts.CreatedAt.Equal(
			proof.EvidenceInput.ArtifactCreatedAt,
		) {
		return turnSettlementCheckpointProofError(
			"frozen Artifact proof is inconsistent",
		)
	}
	switch proof.DomainPlan.SessionTarget {
	case "interactive", "waiting_user", "idle":
	default:
		return turnSettlementCheckpointProofError("Session target is unsupported")
	}
	switch proof.DomainPlan.RunTarget {
	case "waiting_user", "completed", "failed", "cancelled", "timed_out":
	default:
		return turnSettlementCheckpointProofError("Run target is unsupported")
	}
	switch proof.DomainPlan.TurnTarget {
	case "completed", "failed", "cancelled", "timed_out":
	default:
		return turnSettlementCheckpointProofError("Turn target is unsupported")
	}
	if proof.DomainPlan.RunTarget == "waiting_user" &&
		proof.DomainPlan.SessionTarget != "waiting_user" {
		return turnSettlementCheckpointProofError(
			"waiting Run requires waiting_user Session",
		)
	}
	return nil
}

func validateTurnSettlementObservationSourceInput(input turnSettlementEvidenceInputProof) error {
	present := input.ObservationSourceSnapshotFile != "" ||
		input.ObservationSourceSize != 0 ||
		input.ObservationSourceDigest != ""
	if !present {
		return nil
	}
	if input.ObservationSourceSnapshotFile == "" ||
		input.ObservationSourceSize <= 0 ||
		input.ObservationSourceDigest == "" {
		return turnSettlementCheckpointProofError(
			"observation source snapshot proof is incomplete",
		)
	}
	name := input.ObservationSourceSnapshotFile
	if name == "." || name == ".." || strings.ContainsRune(name, '\x00') ||
		filepath.IsAbs(name) ||
		filepath.Base(name) != name || strings.Contains(name, `\`) {
		return turnSettlementCheckpointProofError(
			"observation source snapshot reference is invalid",
		)
	}
	if err := validateRuntimeControlCursorDigest(input.ObservationSourceDigest); err != nil {
		return turnSettlementCheckpointProofError(
			"observation source digest must use sha256",
		)
	}
	return nil
}

func validateTurnSettlementEvidenceProof(proof turnSettlementCheckpointProof) error {
	if proof.Evidence == nil {
		return turnSettlementCheckpointProofError("evidence proof is missing")
	}
	if err := validateRuntimeControlCursorDigest(
		proof.Evidence.ManifestDigest,
	); err != nil {
		return turnSettlementCheckpointProofError(
			"evidence manifest digest must use sha256",
		)
	}
	finalized := proof.Evidence.Finalized
	if err := finalized.Manifest.Validate(); err != nil {
		return turnSettlementCheckpointProofError(
			"finalized Trace manifest is invalid: %v",
			err,
		)
	}
	input := proof.EvidenceInput
	if finalized.Manifest.TraceStreamID == "" ||
		finalized.Manifest.TenantID != input.TenantID ||
		finalized.Manifest.UserID != input.UserID ||
		finalized.Manifest.WorkspaceID != input.WorkspaceID ||
		string(finalized.Manifest.WorkerSessionID) != input.WorkerSessionID ||
		string(finalized.Manifest.WorkerRunID) != input.WorkerRunID ||
		string(finalized.Manifest.AgentTurnID) != input.AgentTurnID ||
		string(finalized.Manifest.RuntimeInstanceID) != input.RuntimeInstanceID ||
		uint64(finalized.Manifest.RuntimeGeneration) != input.RuntimeGeneration ||
		!finalized.Manifest.FirstRecordedAt.Equal(input.ServerRecordedAt) ||
		!finalized.Manifest.LastRecordedAt.Equal(input.ServerRecordedAt) {
		return turnSettlementCheckpointProofError(
			"finalized Trace manifest differs from frozen evidence input",
		)
	}
	if err := validateTurnSettlementStoredObject(
		"Trace manifest",
		finalized.ManifestObject,
	); err != nil ||
		finalized.ManifestObject.Size == 0 ||
		finalized.ManifestObject.Checksum != proof.Evidence.ManifestDigest {
		return turnSettlementCheckpointProofError(
			"finalized Trace manifest object is invalid",
		)
	}
	if err := validateTurnSettlementFinalizedObjects(
		finalized,
		proof,
	); err != nil {
		return err
	}
	if err := validateTurnSettlementArtifactRefs(
		finalized,
		proof,
	); err != nil {
		return err
	}
	return nil
}

func validateTurnSettlementFinalizedObjects(
	finalized turnSettlementFinalizedEvidenceProof,
	proof turnSettlementCheckpointProof,
) error {
	hasObservationSource := proof.EvidenceInput.ObservationSourceSnapshotFile != ""
	expectedObjectCount := 4
	expectedSegmentCount := 2
	if hasObservationSource {
		expectedObjectCount++
		expectedSegmentCount++
	}
	if len(finalized.Objects) != expectedObjectCount {
		return turnSettlementCheckpointProofError(
			"finalized evidence contains an unexpected number of verified objects",
		)
	}
	objects := make(map[string]StoredObject, len(finalized.Objects))
	for _, object := range finalized.Objects {
		switch object.Kind {
		case "trace-raw-acp", "trace-normalized", "report", "patch":
		case "turn-observation-source":
			if !hasObservationSource {
				return turnSettlementCheckpointProofError(
					"finalized observation source object has no frozen proof",
				)
			}
		default:
			return turnSettlementCheckpointProofError(
				"finalized evidence object kind %q is unsupported",
				object.Kind,
			)
		}
		if _, exists := objects[object.Kind]; exists {
			return turnSettlementCheckpointProofError(
				"finalized evidence contains duplicate object %q",
				object.Kind,
			)
		}
		if err := validateTurnSettlementStoredObject(
			object.Kind,
			object.Stored,
		); err != nil {
			return err
		}
		objects[object.Kind] = object.Stored
	}
	if objects["patch"].Checksum != proof.EvidenceInput.PatchDigest ||
		objects["patch"].Size != proof.EvidenceInput.PatchSize {
		return turnSettlementCheckpointProofError(
			"finalized patch object differs from frozen evidence input",
		)
	}
	if hasObservationSource {
		source := objects["turn-observation-source"]
		if source.Checksum != proof.EvidenceInput.ObservationSourceDigest ||
			source.Size != proof.EvidenceInput.ObservationSourceSize {
			return turnSettlementCheckpointProofError(
				"finalized observation source object differs from frozen evidence input",
			)
		}
	}
	if len(finalized.Manifest.Segments) != expectedSegmentCount {
		return turnSettlementCheckpointProofError(
			"finalized Trace manifest contains an unexpected number of segments",
		)
	}
	segments := make(map[string]contractsv1.TraceSegment, expectedSegmentCount)
	for _, segment := range finalized.Manifest.Segments {
		switch segment.Kind {
		case "trace-raw-acp", "trace-normalized":
		case "turn-observation-source":
			if !hasObservationSource {
				return turnSettlementCheckpointProofError(
					"finalized observation source segment has no frozen proof",
				)
			}
		default:
			return turnSettlementCheckpointProofError(
				"finalized Trace segment kind %q is unsupported",
				segment.Kind,
			)
		}
		if _, exists := segments[segment.Kind]; exists {
			return turnSettlementCheckpointProofError(
				"finalized Trace manifest contains duplicate segment %q",
				segment.Kind,
			)
		}
		stored := objects[segment.Kind]
		if segment.URI != "object://"+stored.Key ||
			segment.SHA256 != strings.TrimPrefix(
				stored.Checksum,
				"sha256:",
			) ||
			segment.SizeBytes != stored.Size {
			return turnSettlementCheckpointProofError(
				"finalized Trace segment %q differs from stored object proof",
				segment.Kind,
			)
		}
		segments[segment.Kind] = segment
	}
	return nil
}

func validateTurnSettlementArtifactRefs(
	finalized turnSettlementFinalizedEvidenceProof,
	proof turnSettlementCheckpointProof,
) error {
	if len(finalized.ArtifactRefs) != 2 {
		return turnSettlementCheckpointProofError(
			"finalized evidence must contain both frozen Artifact refs",
		)
	}
	artifacts := make(map[string]string, len(finalized.ArtifactRefs))
	for _, artifact := range finalized.ArtifactRefs {
		if err := artifact.Validate(); err != nil {
			return turnSettlementCheckpointProofError(
				"finalized Artifact ref is invalid: %v",
				err,
			)
		}
		var expectedID string
		switch artifact.Type {
		case "report":
			expectedID = proof.FrozenArtifacts.ReportArtifactID
		case "patch":
			expectedID = proof.FrozenArtifacts.PatchArtifactID
		default:
			return turnSettlementCheckpointProofError(
				"finalized Artifact type %q is unsupported",
				artifact.Type,
			)
		}
		if _, exists := artifacts[artifact.Type]; exists {
			return turnSettlementCheckpointProofError(
				"finalized evidence contains duplicate %s Artifact",
				artifact.Type,
			)
		}
		input := proof.EvidenceInput
		stored := finalizedObjectByKind(finalized.Objects, artifact.Type)
		if artifact.ArtifactID != expectedID ||
			artifact.TenantID != input.TenantID ||
			artifact.UserID != input.UserID ||
			artifact.WorkspaceID != input.WorkspaceID ||
			string(artifact.TaskID) != input.TaskID ||
			string(artifact.WorkerSessionID) != input.WorkerSessionID ||
			string(artifact.WorkerRunID) != input.WorkerRunID ||
			string(artifact.AgentTurnID) != input.AgentTurnID ||
			string(artifact.RuntimeInstanceID) != input.RuntimeInstanceID ||
			uint64(artifact.RuntimeGeneration) != input.RuntimeGeneration ||
			artifact.URI != "object://"+stored.Key ||
			artifact.SHA256 != strings.TrimPrefix(stored.Checksum, "sha256:") ||
			artifact.SizeBytes != stored.Size ||
			artifact.SourceCheckpointRef.CheckpointID != input.SourceCheckpointID ||
			artifact.SourceCheckpointRef.Digest !=
				input.SourceCheckpointDigest ||
			!artifact.CreatedAt.Equal(input.ArtifactCreatedAt) {
			return turnSettlementCheckpointProofError(
				"finalized %s Artifact differs from frozen evidence input",
				artifact.Type,
			)
		}
		artifacts[artifact.Type] = artifact.ArtifactID
	}
	return nil
}

func finalizedObjectByKind(
	objects []turnSettlementFinalizedObjectProof,
	kind string,
) StoredObject {
	for _, object := range objects {
		if object.Kind == kind {
			return object.Stored
		}
	}
	return StoredObject{}
}

func validateTurnSettlementStoredObject(name string, object StoredObject) error {
	if err := validateCanonical(name+" object key", object.Key); err != nil {
		return turnSettlementCheckpointProofError("%v", err)
	}
	if object.Size < 0 {
		return turnSettlementCheckpointProofError(
			"%s object size is invalid",
			name,
		)
	}
	if err := validateRuntimeControlCursorDigest(object.Checksum); err != nil {
		return turnSettlementCheckpointProofError(
			"%s object checksum must use sha256",
			name,
		)
	}
	return nil
}

func validateTurnSettlementDomainProof(proof turnSettlementCheckpointProof) error {
	if proof.Domain == nil {
		return turnSettlementCheckpointProofError("domain proof is missing")
	}
	turnRevisionMatchesPlan :=
		proof.Domain.TurnRevision ==
			proof.DomainPlan.ExpectedTurnRevision+1
	runtimeRevisionMatchesPlan :=
		proof.Domain.RuntimeRevision ==
			proof.DomainPlan.ExpectedRuntimeRevision
	if proof.Source != nil &&
		proof.Source.Kind == string(TurnSettlementSourceRuntimeEvent) {
		turnRevisionMatchesPlan =
			proof.Domain.TurnRevision >
				proof.DomainPlan.ExpectedTurnRevision &&
				proof.Domain.TurnRevision-
					proof.DomainPlan.ExpectedTurnRevision <= 2
		// Runtime-control progress and heartbeat cursor commits may advance
		// the same active Runtime after the domain plan is frozen. The
		// atomic domain commit also recognizes at most one prior
		// delivered-to-consumed Turn transition. It validates the exact
		// graph and lifecycle state before recording the newer revisions.
		runtimeRevisionMatchesPlan =
			proof.Domain.RuntimeRevision >=
				proof.DomainPlan.ExpectedRuntimeRevision
	}
	if proof.Domain.SessionRevision == 0 ||
		proof.Domain.RunRevision == 0 ||
		proof.Domain.TurnRevision == 0 ||
		proof.Domain.RuntimeRevision == 0 ||
		proof.Domain.SessionRevision !=
			proof.DomainPlan.ExpectedSessionRevision+1 ||
		proof.Domain.RunRevision != proof.DomainPlan.ExpectedRunRevision+1 ||
		!turnRevisionMatchesPlan ||
		!runtimeRevisionMatchesPlan {
		return turnSettlementCheckpointProofError(
			"domain post revisions do not match the frozen plan",
		)
	}
	return nil
}

func validateTurnSettlementPublicationProof(
	proof turnSettlementCheckpointProof,
) error {
	if proof.Publication == nil {
		return turnSettlementCheckpointProofError("publication proof is missing")
	}
	if err := validateCanonical(
		"trace stream id",
		proof.Publication.TraceStreamID,
	); err != nil {
		return turnSettlementCheckpointProofError("%v", err)
	}
	if proof.Publication.TraceStreamID !=
		proof.Evidence.Finalized.Manifest.TraceStreamID {
		return turnSettlementCheckpointProofError(
			"publication Trace differs from finalized evidence",
		)
	}
	if len(proof.Publication.ArtifactIDs) != 2 {
		return turnSettlementCheckpointProofError(
			"publication must contain both frozen Artifacts",
		)
	}
	published := make(map[string]struct{}, len(proof.Publication.ArtifactIDs))
	for _, artifactID := range proof.Publication.ArtifactIDs {
		if err := validateCanonical("published artifact id", artifactID); err != nil {
			return turnSettlementCheckpointProofError("%v", err)
		}
		published[artifactID] = struct{}{}
	}
	if len(published) != 2 {
		return turnSettlementCheckpointProofError(
			"publication contains duplicate Artifact proof",
		)
	}
	for _, artifactID := range []string{
		proof.FrozenArtifacts.ReportArtifactID,
		proof.FrozenArtifacts.PatchArtifactID,
	} {
		if _, exists := published[artifactID]; !exists {
			return turnSettlementCheckpointProofError(
				"publication differs from frozen Artifact proof",
			)
		}
	}
	return nil
}

func validateTurnSettlementTaskProof(proof turnSettlementCheckpointProof) error {
	if proof.TaskPlan == nil || proof.TaskResult == nil {
		return turnSettlementCheckpointProofError("Task proof is missing")
	}
	plan := proof.TaskPlan
	result := proof.TaskResult
	switch plan.Action {
	case "none":
		if proof.EvidenceInput.TaskID != "" ||
			plan.TaskID != "" ||
			plan.ExpectedTaskRevision != 0 ||
			plan.IdempotencyKey != "" ||
			validateCanonical("Task no-op reason", plan.Reason) != nil {
			return turnSettlementCheckpointProofError(
				"no-op Task plan contains effect authority",
			)
		}
		if result.Status != "not_applicable" ||
			result.Revision != 0 ||
			result.EventID != "" {
			return turnSettlementCheckpointProofError(
				"no-op Task result is invalid",
			)
		}
		return nil
	case "settle_cancelled", "record_stage_result", "pause":
	default:
		return turnSettlementCheckpointProofError(
			"Task action %q is unsupported",
			plan.Action,
		)
	}
	if validateCanonical("Task id", plan.TaskID) != nil ||
		plan.TaskID != proof.EvidenceInput.TaskID ||
		plan.ExpectedTaskRevision == 0 ||
		validateCanonical("Task idempotency key", plan.IdempotencyKey) != nil {
		return turnSettlementCheckpointProofError(
			"Task plan lacks exact effect authority",
		)
	}
	if plan.Action == "pause" {
		if validateCanonical("Task pause reason", plan.Reason) != nil ||
			!turnSettlementTaskPauseMatchesSource(plan.Reason, proof.Source) {
			return turnSettlementCheckpointProofError(
				"Task pause reason differs from the frozen source",
			)
		}
	} else if plan.Reason != "" {
		return turnSettlementCheckpointProofError(
			"Task effect plan contains an unexpected reason",
		)
	}
	if result.Status != "applied" ||
		result.Revision == 0 ||
		validateCanonical("Task event id", result.EventID) != nil {
		return turnSettlementCheckpointProofError(
			"Task effect result is incomplete",
		)
	}
	return nil
}

func turnSettlementTaskPauseMatchesSource(
	reason string,
	source *turnSettlementSourceProof,
) bool {
	if source == nil {
		return false
	}
	switch {
	case source.TimedOut:
		return reason == "agent_turn_deadline"
	case source.Reason == "agent_error":
		return reason == "agent_error"
	case source.Reason == "cancelled" ||
		source.Reason == "user_interrupt" ||
		source.Reason == "cancel":
		return reason == "user_interrupt"
	default:
		return false
	}
}

func validateTurnSettlementContinuationProof(
	proof turnSettlementCheckpointProof,
) error {
	if proof.ContinuationPlan == nil || proof.ContinuationResult == nil {
		return turnSettlementCheckpointProofError(
			"continuation proof is missing",
		)
	}
	plan := proof.ContinuationPlan
	result := proof.ContinuationResult
	switch plan.Action {
	case "none":
		if validateCanonical("continuation no-op reason", plan.Reason) != nil ||
			plan.QueueCommandID != "" ||
			plan.ExpectedSessionRevision != 0 ||
			plan.ExpectedRunRevision != 0 ||
			plan.ExpectedTurnRevision != 0 ||
			plan.ExpectedRuntimeRevision != 0 {
			return turnSettlementCheckpointProofError(
				"no-op continuation plan contains effect authority",
			)
		}
		if result.Status != "not_applicable" ||
			result.WorkerRunID != "" ||
			result.AgentTurnID != "" {
			return turnSettlementCheckpointProofError(
				"no-op continuation result is invalid",
			)
		}
		return nil
	case "create_queued_turn":
	default:
		return turnSettlementCheckpointProofError(
			"continuation action %q is unsupported",
			plan.Action,
		)
	}
	if plan.Reason != "" ||
		validateCanonical(
			"queued continuation command id",
			plan.QueueCommandID,
		) != nil ||
		proof.Domain == nil ||
		plan.ExpectedSessionRevision != proof.Domain.SessionRevision ||
		plan.ExpectedRunRevision != proof.Domain.RunRevision ||
		plan.ExpectedTurnRevision != proof.Domain.TurnRevision ||
		plan.ExpectedRuntimeRevision != proof.Domain.RuntimeRevision {
		return turnSettlementCheckpointProofError(
			"queued continuation plan lacks exact domain authority",
		)
	}
	if result.Status != "created" ||
		validateCanonical(
			"continued WorkerRun id",
			result.WorkerRunID,
		) != nil ||
		validateCanonical(
			"continued AgentTurn id",
			result.AgentTurnID,
		) != nil ||
		result.WorkerRunID == proof.EvidenceInput.WorkerRunID ||
		result.AgentTurnID == proof.EvidenceInput.AgentTurnID {
		return turnSettlementCheckpointProofError(
			"queued continuation result is incomplete",
		)
	}
	return nil
}

func turnSettlementCheckpointProofError(format string, arguments ...any) error {
	return fmt.Errorf(
		"%w: %s",
		ErrTurnSettlementCheckpointConflict,
		fmt.Sprintf(format, arguments...),
	)
}

func turnSettlementPhaseAtLeast(
	current TurnSettlementPhase,
	required TurnSettlementPhase,
) bool {
	return turnSettlementPhaseRank(current) >= turnSettlementPhaseRank(required)
}

func turnSettlementPhaseRank(phase TurnSettlementPhase) int {
	switch phase {
	case TurnSettlementAccepted:
		return 1
	case TurnSettlementEvidencePrepared:
		return 2
	case TurnSettlementDomainCommitted:
		return 3
	case TurnSettlementEvidencePublished:
		return 4
	case TurnSettlementTaskSettled:
		return 5
	case TurnSettlementTerminalEventApplied:
		return 6
	case TurnSettlementContinuationSettled:
		return 7
	case TurnSettlementCompleted:
		return 8
	default:
		return 0
	}
}
