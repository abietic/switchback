package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

var (
	ErrEvidenceIntegrity = errors.New("turn evidence integrity verification failed")
	ErrEvidenceIdentity  = errors.New("turn evidence identity mismatch")
)

const maxManifestBytes = 4 << 20

type PublishTurnEvidenceCommand struct {
	Evidence                FinalizedTurnEvidence
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	ExpectedRuntimeRevision uint64
	IdempotencyKey          string
}

type PublishTurnEvidenceRecord struct {
	Command PublishTurnEvidenceCommand
	EventID string
	Now     time.Time
}

type PublishedTurnEvidence struct {
	TraceStreamID string
	ArtifactIDs   []string
}

type ArtifactMetadataStore interface {
	PublishTurnEvidence(context.Context, PublishTurnEvidenceRecord) (PublishedTurnEvidence, error)
}

type SettlementArtifactMetadataStore interface {
	PublishSettlementEvidence(context.Context, workerapp.TurnSettlement, PublishTurnEvidenceRecord) (PublishedTurnEvidence, error)
}

type ArtifactMetadataPublisher struct {
	Objects  workerapp.ObjectStore
	Metadata ArtifactMetadataStore
	IDs      IDGenerator
	Now      func() time.Time
}

func (publisher ArtifactMetadataPublisher) Execute(
	ctx context.Context,
	command PublishTurnEvidenceCommand,
) (PublishedTurnEvidence, error) {
	record, err := publisher.preparePublication(ctx, command)
	if err != nil {
		return PublishedTurnEvidence{}, err
	}
	return publisher.Metadata.PublishTurnEvidence(ctx, record)
}

// ExecuteSettlement publishes only under the durable settlement's current lease.
func (publisher ArtifactMetadataPublisher) ExecuteSettlement(ctx context.Context, settlement workerapp.TurnSettlement, command PublishTurnEvidenceCommand) (PublishedTurnEvidence, error) {
	store, ok := publisher.Metadata.(SettlementArtifactMetadataStore)
	if !ok {
		return PublishedTurnEvidence{}, fmt.Errorf("settlement publication store is required")
	}
	record, err := publisher.preparePublication(ctx, command)
	if err != nil {
		return PublishedTurnEvidence{}, err
	}
	return store.PublishSettlementEvidence(ctx, settlement, record)
}

func (publisher ArtifactMetadataPublisher) preparePublication(ctx context.Context, command PublishTurnEvidenceCommand) (PublishTurnEvidenceRecord, error) {
	if publisher.Objects == nil || publisher.Metadata == nil || publisher.IDs == nil || publisher.Now == nil {
		return PublishTurnEvidenceRecord{}, fmt.Errorf("Artifact metadata publisher dependencies are required")
	}
	if command.ExpectedSessionRevision == 0 || command.ExpectedRunRevision == 0 ||
		command.ExpectedTurnRevision == 0 || command.ExpectedRuntimeRevision == 0 {
		return PublishTurnEvidenceRecord{}, fmt.Errorf("Artifact publication revisions are required")
	}
	if !canonicalPublicationValue(command.IdempotencyKey) {
		return PublishTurnEvidenceRecord{}, fmt.Errorf("Artifact publication idempotency key must be canonical")
	}
	if err := publisher.verifyEvidence(ctx, command.Evidence); err != nil {
		return PublishTurnEvidenceRecord{}, err
	}
	eventID, err := publisher.IDs.NewID("integration-event-turn-evidence")
	if err != nil {
		return PublishTurnEvidenceRecord{}, fmt.Errorf("generate evidence publication event id: %w", err)
	}
	if !canonicalPublicationValue(eventID) {
		return PublishTurnEvidenceRecord{}, fmt.Errorf("generated evidence publication event id is invalid")
	}
	now := publisher.Now().UTC()
	if now.IsZero() {
		return PublishTurnEvidenceRecord{}, fmt.Errorf("Artifact publication time is required")
	}
	return PublishTurnEvidenceRecord{
		Command: command, EventID: eventID, Now: now,
	}, nil
}

func (publisher ArtifactMetadataPublisher) verifyEvidence(
	ctx context.Context,
	evidence FinalizedTurnEvidence,
) error {
	if err := evidence.Manifest.Validate(); err != nil {
		return fmt.Errorf("%w: invalid TraceManifest: %v", ErrEvidenceIdentity, err)
	}
	manifestKey := evidence.ManifestObject.Key
	manifestChecksum := evidence.ManifestObject.Checksum
	if manifestKey == "" || evidence.ManifestObject.Size < 0 || !prefixedSHA256(manifestChecksum) {
		return fmt.Errorf("%w: invalid TraceManifest object receipt", ErrEvidenceIntegrity)
	}
	if err := publisher.Objects.Verify(ctx, manifestKey, manifestChecksum); err != nil {
		return fmt.Errorf("%w: verify TraceManifest object: %v", ErrEvidenceIntegrity, err)
	}
	reader, err := publisher.Objects.Open(ctx, manifestKey)
	if err != nil {
		return fmt.Errorf("%w: open TraceManifest object: %v", ErrEvidenceIntegrity, err)
	}
	manifestPayload, readErr := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(manifestPayload) > maxManifestBytes {
		return fmt.Errorf("%w: read TraceManifest object", ErrEvidenceIntegrity)
	}
	storedManifest, err := contractsv1.DecodeTraceManifest(manifestPayload)
	if err != nil {
		return fmt.Errorf("%w: decode TraceManifest object: %v", ErrEvidenceIntegrity, err)
	}
	if !reflect.DeepEqual(storedManifest, evidence.Manifest) {
		return fmt.Errorf("%w: stored TraceManifest differs from publication", ErrEvidenceIdentity)
	}

	for _, segment := range evidence.Manifest.Segments {
		key, err := objectKey(segment.URI)
		if err != nil {
			return fmt.Errorf("%w: Trace segment URI: %v", ErrEvidenceIdentity, err)
		}
		if err := publisher.Objects.Verify(ctx, key, "sha256:"+segment.SHA256); err != nil {
			return fmt.Errorf("%w: verify Trace segment %s: %v", ErrEvidenceIntegrity, segment.Kind, err)
		}
	}
	if err := publisher.verifyArtifactRefs(ctx, evidence); err != nil {
		return err
	}
	return nil
}

func (publisher ArtifactMetadataPublisher) verifyArtifactRefs(
	ctx context.Context,
	evidence FinalizedTurnEvidence,
) error {
	if len(evidence.ArtifactRefs) != 2 {
		return fmt.Errorf("%w: terminal Turn requires report and patch ArtifactRefs", ErrEvidenceIdentity)
	}
	seen := make(map[string]struct{}, 2)
	manifest := evidence.Manifest
	for _, artifact := range evidence.ArtifactRefs {
		if err := artifact.Validate(); err != nil {
			return fmt.Errorf("%w: invalid ArtifactRef: %v", ErrEvidenceIdentity, err)
		}
		if artifact.TenantID != manifest.TenantID || artifact.UserID != manifest.UserID ||
			artifact.WorkspaceID != manifest.WorkspaceID || artifact.WorkerSessionID != manifest.WorkerSessionID ||
			artifact.WorkerRunID != manifest.WorkerRunID || artifact.AgentTurnID != manifest.AgentTurnID ||
			artifact.RuntimeInstanceID != manifest.RuntimeInstanceID ||
			artifact.RuntimeGeneration != manifest.RuntimeGeneration {
			return fmt.Errorf("%w: Artifact %s provenance differs from TraceManifest", ErrEvidenceIdentity, artifact.ArtifactID)
		}
		if _, exists := seen[artifact.Type]; exists {
			return fmt.Errorf("%w: duplicate %s ArtifactRef", ErrEvidenceIdentity, artifact.Type)
		}
		seen[artifact.Type] = struct{}{}
		key, err := objectKey(artifact.URI)
		if err != nil {
			return fmt.Errorf("%w: Artifact URI: %v", ErrEvidenceIdentity, err)
		}
		if err := publisher.Objects.Verify(ctx, key, "sha256:"+artifact.SHA256); err != nil {
			return fmt.Errorf("%w: verify Artifact %s: %v", ErrEvidenceIntegrity, artifact.ArtifactID, err)
		}
	}
	for _, kind := range []string{"report", "patch"} {
		if _, exists := seen[kind]; !exists {
			return fmt.Errorf("%w: missing %s ArtifactRef", ErrEvidenceIdentity, kind)
		}
	}
	return nil
}

func objectKey(uri string) (string, error) {
	key, ok := strings.CutPrefix(uri, "object://")
	if !ok || key == "" || strings.TrimSpace(key) != key {
		return "", fmt.Errorf("unsupported ObjectStore URI")
	}
	return key, nil
}

func canonicalPublicationValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func prefixedSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}
