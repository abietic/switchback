package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type IDGenerator interface {
	NewID(kind string) (string, error)
}

type FinalizationPolicy struct {
	ArtifactPermission         contractsv1.ArtifactPermission
	ArtifactRetentionPolicyRef string
	TraceLifecyclePolicyRef    string
}

type FinalizeTurnEvidenceCommand struct {
	Identity               artifactdomain.TurnIdentity
	ServerRecordedAt       time.Time
	WorkerRecordedAt       time.Time
	RawACPJSONL            []byte
	ObservationSourceJSONL []byte
	Patch                  []byte
	NoFilesChanged         bool
	SourceCheckpointRef    contractsv1.CheckpointRef
	// FrozenArtifacts makes evidence finalization replayable after a crash.
	// A settlement owner freezes both Artifact ids and CreatedAt before the
	// first ObjectStore write; retries must reuse the exact values.
	FrozenArtifacts *FrozenArtifactMetadata
}

type FrozenArtifactMetadata = workerapp.FrozenArtifactMetadata

type FinalizedObject struct {
	Kind   string
	Stored workerapp.StoredObject
}

// FinalizedTurnEvidence is checksum-verified content pending metadata publication.
type FinalizedTurnEvidence struct {
	Manifest       contractsv1.TraceManifest
	ManifestObject workerapp.StoredObject
	ArtifactRefs   []contractsv1.ArtifactRef
	Objects        []FinalizedObject
}

type TurnEvidenceFinalizer struct {
	Store  workerapp.ObjectStore
	IDs    IDGenerator
	Now    func() time.Time
	Policy FinalizationPolicy
}

func (finalizer TurnEvidenceFinalizer) Execute(
	ctx context.Context,
	command FinalizeTurnEvidenceCommand,
) (FinalizedTurnEvidence, error) {
	if finalizer.Store == nil || finalizer.IDs == nil || finalizer.Now == nil {
		return FinalizedTurnEvidence{}, fmt.Errorf("Turn evidence finalizer dependencies are required")
	}
	if err := finalizer.Policy.validate(); err != nil {
		return FinalizedTurnEvidence{}, err
	}
	if err := validateCheckpoint(command.SourceCheckpointRef); err != nil {
		return FinalizedTurnEvidence{}, err
	}
	if len(command.ObservationSourceJSONL) != 0 {
		source, err := DecodeTurnObservationSource(command.ObservationSourceJSONL)
		if err != nil {
			return FinalizedTurnEvidence{}, err
		}
		if !reflect.DeepEqual(source.Identity, command.Identity) {
			return FinalizedTurnEvidence{}, errors.New("observation source identity mismatch")
		}
	}
	artifactMetadata, err := finalizer.resolveArtifactMetadata(command.FrozenArtifacts)
	if err != nil {
		return FinalizedTurnEvidence{}, err
	}
	bundle, err := artifactdomain.BuildTurnEvidence(artifactdomain.BuildTurnEvidenceInput{
		Identity: command.Identity, ServerRecordedAt: command.ServerRecordedAt,
		WorkerRecordedAt: command.WorkerRecordedAt, RawACPJSONL: command.RawACPJSONL,
		ObservationSourceJSONL: command.ObservationSourceJSONL,
		Patch:                  command.Patch, NoFilesChanged: command.NoFilesChanged,
	})
	if err != nil {
		return FinalizedTurnEvidence{}, err
	}

	storedByKind := make(map[string]workerapp.StoredObject, len(bundle.Objects))
	objects := make([]FinalizedObject, 0, len(bundle.Objects))
	for _, object := range bundle.Objects {
		stored, err := finalizer.finalizeObject(ctx, bundle.Identity, object.Kind, object.Payload, object.Checksum, object.SizeBytes)
		if err != nil {
			return FinalizedTurnEvidence{}, fmt.Errorf("finalize %s evidence: %w", object.Kind, err)
		}
		storedByKind[object.Kind] = stored
		objects = append(objects, FinalizedObject{Kind: object.Kind, Stored: stored})
	}

	artifactRefs, err := finalizer.buildArtifactRefs(
		bundle,
		storedByKind,
		command.SourceCheckpointRef,
		artifactMetadata,
	)
	if err != nil {
		return FinalizedTurnEvidence{}, err
	}
	manifest := finalizer.buildManifest(bundle, storedByKind)
	if err := manifest.Validate(); err != nil {
		return FinalizedTurnEvidence{}, fmt.Errorf("validate TraceManifest: %w", err)
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		return FinalizedTurnEvidence{}, fmt.Errorf("encode TraceManifest: %w", err)
	}
	manifestObject, err := finalizer.finalizeObject(
		ctx, bundle.Identity, "trace-manifest", manifestPayload,
		artifactdomain.Checksum(manifestPayload), int64(len(manifestPayload)),
	)
	if err != nil {
		return FinalizedTurnEvidence{}, fmt.Errorf("finalize TraceManifest: %w", err)
	}
	return FinalizedTurnEvidence{
		Manifest: manifest, ManifestObject: manifestObject,
		ArtifactRefs: artifactRefs, Objects: objects,
	}, nil
}

type resolvedArtifactMetadata struct {
	ids       map[string]string
	createdAt time.Time
}

func (finalizer TurnEvidenceFinalizer) resolveArtifactMetadata(
	frozen *FrozenArtifactMetadata,
) (resolvedArtifactMetadata, error) {
	if frozen != nil {
		metadata := resolvedArtifactMetadata{
			ids: map[string]string{
				artifactdomain.KindReport: frozen.ReportArtifactID,
				artifactdomain.KindPatch:  frozen.PatchArtifactID,
			},
			createdAt: frozen.CreatedAt.UTC(),
		}
		if err := validateResolvedArtifactMetadata(metadata); err != nil {
			return resolvedArtifactMetadata{}, fmt.Errorf(
				"frozen Artifact metadata is invalid: %w",
				err,
			)
		}
		return metadata, nil
	}
	reportID, err := finalizer.IDs.NewID("artifact-" + artifactdomain.KindReport)
	if err != nil {
		return resolvedArtifactMetadata{}, fmt.Errorf(
			"generate %s Artifact id: %w",
			artifactdomain.KindReport,
			err,
		)
	}
	patchID, err := finalizer.IDs.NewID("artifact-" + artifactdomain.KindPatch)
	if err != nil {
		return resolvedArtifactMetadata{}, fmt.Errorf(
			"generate %s Artifact id: %w",
			artifactdomain.KindPatch,
			err,
		)
	}
	metadata := resolvedArtifactMetadata{
		ids: map[string]string{
			artifactdomain.KindReport: reportID,
			artifactdomain.KindPatch:  patchID,
		},
		createdAt: finalizer.Now().UTC(),
	}
	if err := validateResolvedArtifactMetadata(metadata); err != nil {
		return resolvedArtifactMetadata{}, err
	}
	return metadata, nil
}

func validateResolvedArtifactMetadata(metadata resolvedArtifactMetadata) error {
	reportID := metadata.ids[artifactdomain.KindReport]
	patchID := metadata.ids[artifactdomain.KindPatch]
	for kind, artifactID := range map[string]string{
		artifactdomain.KindReport: reportID,
		artifactdomain.KindPatch:  patchID,
	} {
		if artifactID == "" ||
			strings.TrimSpace(artifactID) != artifactID ||
			strings.ContainsAny(artifactID, "\r\n\x00") {
			return fmt.Errorf("%s Artifact id is invalid", kind)
		}
	}
	if reportID == patchID {
		return fmt.Errorf("report and patch Artifact ids must differ")
	}
	if metadata.createdAt.IsZero() {
		return fmt.Errorf("finalization time is required")
	}
	return nil
}

func (policy FinalizationPolicy) validate() error {
	for name, value := range map[string]string{
		"artifact permission visibility":      policy.ArtifactPermission.Visibility,
		"artifact permission policy revision": policy.ArtifactPermission.PolicyRevision,
		"artifact retention policy":           policy.ArtifactRetentionPolicyRef,
		"Trace lifecycle policy":              policy.TraceLifecyclePolicyRef,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s must be canonical", name)
		}
	}
	return nil
}

func validateCheckpoint(checkpoint contractsv1.CheckpointRef) error {
	if checkpoint.CheckpointID == "" || strings.TrimSpace(checkpoint.CheckpointID) != checkpoint.CheckpointID {
		return fmt.Errorf("source checkpoint identity is required")
	}
	if !strings.HasPrefix(checkpoint.Digest, "sha256:") || len(checkpoint.Digest) != len("sha256:")+64 {
		return fmt.Errorf("source checkpoint digest must be SHA-256")
	}
	for _, character := range strings.TrimPrefix(checkpoint.Digest, "sha256:") {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return fmt.Errorf("source checkpoint digest must be SHA-256")
		}
	}
	return nil
}

func (finalizer TurnEvidenceFinalizer) finalizeObject(
	ctx context.Context,
	identity artifactdomain.TurnIdentity,
	kind string,
	payload []byte,
	expectedChecksum string,
	expectedSize int64,
) (workerapp.StoredObject, error) {
	objectIdentity := workerapp.ObjectIdentity{
		TenantID: identity.TenantID, UserID: identity.UserID, WorkspaceID: identity.WorkspaceID,
		WorkerSessionID: string(identity.WorkerSessionID), WorkerRunID: string(identity.WorkerRunID),
		AgentTurnID: string(identity.AgentTurnID), Kind: kind,
	}
	temporary, err := finalizer.Store.PutTemporary(ctx, objectIdentity, bytes.NewReader(payload))
	if err != nil {
		return workerapp.StoredObject{}, err
	}
	if temporary.Size != expectedSize || temporary.Checksum != expectedChecksum {
		return workerapp.StoredObject{}, fmt.Errorf("temporary ObjectStore checksum mismatch")
	}
	stored, err := finalizer.Store.Finalize(ctx, objectIdentity, temporary)
	if err != nil {
		return workerapp.StoredObject{}, err
	}
	if stored.Size != expectedSize || stored.Checksum != expectedChecksum {
		return workerapp.StoredObject{}, fmt.Errorf("final ObjectStore checksum mismatch")
	}
	if err := finalizer.Store.Verify(ctx, stored.Key, expectedChecksum); err != nil {
		return workerapp.StoredObject{}, fmt.Errorf("verify final ObjectStore content: %w", err)
	}
	return stored, nil
}

func (finalizer TurnEvidenceFinalizer) buildArtifactRefs(
	bundle artifactdomain.TurnEvidenceBundle,
	storedByKind map[string]workerapp.StoredObject,
	checkpoint contractsv1.CheckpointRef,
	metadata resolvedArtifactMetadata,
) ([]contractsv1.ArtifactRef, error) {
	artifactKinds := []string{artifactdomain.KindReport, artifactdomain.KindPatch}
	refs := make([]contractsv1.ArtifactRef, 0, len(artifactKinds))
	for _, kind := range artifactKinds {
		artifactID := metadata.ids[kind]
		stored, ok := storedByKind[kind]
		if !ok {
			return nil, fmt.Errorf("verified %s Artifact content is missing", kind)
		}
		ref := contractsv1.ArtifactRef{
			SchemaVersion: contractsv1.ArtifactRefSchemaVersion, ArtifactID: artifactID,
			TenantID: bundle.Identity.TenantID, UserID: bundle.Identity.UserID,
			WorkspaceID: bundle.Identity.WorkspaceID, TaskID: bundle.Identity.TaskID,
			WorkerSessionID: bundle.Identity.WorkerSessionID, WorkerRunID: bundle.Identity.WorkerRunID,
			AgentTurnID: bundle.Identity.AgentTurnID, RuntimeInstanceID: bundle.Identity.RuntimeInstanceID,
			RuntimeGeneration: bundle.Identity.RuntimeGeneration, Type: kind,
			URI: "object://" + stored.Key, SHA256: strings.TrimPrefix(stored.Checksum, "sha256:"),
			SizeBytes: stored.Size, Permission: finalizer.Policy.ArtifactPermission,
			RetentionPolicyRef: finalizer.Policy.ArtifactRetentionPolicyRef,
			StorageTier:        "hot", Availability: "online", Completeness: "complete",
			SourceCheckpointRef: checkpoint, AvailableActions: []string{"preview", "download"},
			CreatedAt: metadata.createdAt,
		}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("validate %s ArtifactRef: %w", kind, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (finalizer TurnEvidenceFinalizer) buildManifest(
	bundle artifactdomain.TurnEvidenceBundle,
	storedByKind map[string]workerapp.StoredObject,
) contractsv1.TraceManifest {
	segments := make([]contractsv1.TraceSegment, 0, 3)
	for _, object := range bundle.Objects {
		if object.Kind != artifactdomain.KindRawACP && object.Kind != artifactdomain.KindNormalized && object.Kind != artifactdomain.KindObservationSource {
			continue
		}
		stored := storedByKind[object.Kind]
		segments = append(segments, contractsv1.TraceSegment{
			Kind: object.Kind, URI: "object://" + stored.Key,
			FirstSequence: object.FirstSequence, LastSequence: object.LastSequence,
			SHA256: strings.TrimPrefix(stored.Checksum, "sha256:"), SizeBytes: stored.Size,
			RecordCount: object.RecordCount,
		})
	}
	schemaVersions := []string{artifactdomain.TraceRecordSchemaVersion}
	if _, ok := storedByKind[artifactdomain.KindObservationSource]; ok {
		schemaVersions = append(schemaVersions, ObservationSourceSchemaVersion)
	}
	return contractsv1.TraceManifest{
		SchemaVersion: contractsv1.TraceManifestSchemaVersion,
		TraceStreamID: bundle.Identity.TraceStreamID,
		TenantID:      bundle.Identity.TenantID, UserID: bundle.Identity.UserID,
		WorkspaceID:     bundle.Identity.WorkspaceID,
		WorkerSessionID: bundle.Identity.WorkerSessionID, WorkerRunID: bundle.Identity.WorkerRunID,
		AgentTurnID: bundle.Identity.AgentTurnID, RuntimeInstanceID: bundle.Identity.RuntimeInstanceID,
		RuntimeGeneration: bundle.Identity.RuntimeGeneration, Segments: segments,
		FirstRecordedAt: bundle.FirstRecordedAt, LastRecordedAt: bundle.LastRecordedAt,
		SchemaVersions:     schemaVersions,
		LifecyclePolicyRef: finalizer.Policy.TraceLifecyclePolicyRef,
		StorageTier:        "hot", Availability: "online",
		AnalyticsCursor: strconv.FormatUint(bundle.LastSequence, 10),
		LangfuseExport:  contractsv1.LangfuseExportRef{Status: "pending", TraceID: bundle.Identity.LangfuseTraceID},
	}
}
