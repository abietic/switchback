package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestArtifactMetadataPublisherVerifiesEveryVisibleObjectBeforePersistence(t *testing.T) {
	t.Parallel()
	objectStore, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := validFinalizer(objectStore).Execute(context.Background(), validFinalizeCommand(t))
	if err != nil {
		t.Fatal(err)
	}
	trackingStore := &trackingObjectStore{ObjectStore: objectStore}
	metadataStore := &recordingMetadataStore{}
	now := time.Date(2026, time.July, 23, 9, 31, 0, 0, time.UTC)
	publisher := artifactapp.ArtifactMetadataPublisher{
		Objects: trackingStore, Metadata: metadataStore,
		IDs: &sequenceIDs{values: []string{"event-evidence-1"}}, Now: func() time.Time { return now },
	}
	command := artifactapp.PublishTurnEvidenceCommand{
		Evidence: evidence, ExpectedSessionRevision: 4, ExpectedRunRevision: 3,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2,
		IdempotencyKey: "publish-turn-1",
	}

	result, err := publisher.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.TraceStreamID != evidence.Manifest.TraceStreamID || len(result.ArtifactIDs) != 2 {
		t.Fatalf("publication result = %#v", result)
	}
	if trackingStore.verifyCalls != 5 {
		t.Fatalf("ObjectStore Verify calls = %d, want manifest + two segments + two Artifacts", trackingStore.verifyCalls)
	}
	if metadataStore.publishCalls != 1 {
		t.Fatalf("metadata publish calls = %d, want 1", metadataStore.publishCalls)
	}
	record := metadataStore.record
	if record.EventID != "event-evidence-1" || record.Now != now ||
		record.Command.ExpectedSessionRevision != 4 || record.Command.ExpectedRunRevision != 3 ||
		record.Command.ExpectedTurnRevision != 2 || record.Command.ExpectedRuntimeRevision != 2 {
		t.Fatalf("publication record = %#v", record)
	}
}

func TestArtifactMetadataPublisherRejectsObjectVerificationFailure(t *testing.T) {
	t.Parallel()
	objectStore, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := validFinalizer(objectStore).Execute(context.Background(), validFinalizeCommand(t))
	if err != nil {
		t.Fatal(err)
	}
	metadataStore := &recordingMetadataStore{}
	publisher := artifactapp.ArtifactMetadataPublisher{
		Objects:  &trackingObjectStore{ObjectStore: objectStore, verifyErr: errors.New("checksum mismatch")},
		Metadata: metadataStore, IDs: &sequenceIDs{values: []string{"event-unused"}},
		Now: func() time.Time { return time.Now() },
	}

	result, err := publisher.Execute(context.Background(), artifactapp.PublishTurnEvidenceCommand{
		Evidence: evidence, ExpectedSessionRevision: 1, ExpectedRunRevision: 1,
		ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1, IdempotencyKey: "publish-failed",
	})
	if err == nil || !errors.Is(err, artifactapp.ErrEvidenceIntegrity) {
		t.Fatalf("Execute() error = %v, want ErrEvidenceIntegrity", err)
	}
	if metadataStore.publishCalls != 0 || result.TraceStreamID != "" {
		t.Fatalf("integrity failure persisted metadata: calls=%d result=%#v", metadataStore.publishCalls, result)
	}
}

func TestArtifactMetadataPublisherRejectsCrossRuntimeArtifactIdentity(t *testing.T) {
	t.Parallel()
	objectStore, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := validFinalizer(objectStore).Execute(context.Background(), validFinalizeCommand(t))
	if err != nil {
		t.Fatal(err)
	}
	evidence.ArtifactRefs[0].RuntimeGeneration++
	metadataStore := &recordingMetadataStore{}
	publisher := artifactapp.ArtifactMetadataPublisher{
		Objects: objectStore, Metadata: metadataStore,
		IDs: &sequenceIDs{values: []string{"event-unused"}}, Now: func() time.Time { return time.Now() },
	}

	_, err = publisher.Execute(context.Background(), artifactapp.PublishTurnEvidenceCommand{
		Evidence: evidence, ExpectedSessionRevision: 1, ExpectedRunRevision: 1,
		ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1, IdempotencyKey: "publish-wrong-runtime",
	})
	if err == nil || !errors.Is(err, artifactapp.ErrEvidenceIdentity) {
		t.Fatalf("Execute() error = %v, want ErrEvidenceIdentity", err)
	}
	if metadataStore.publishCalls != 0 {
		t.Fatal("cross-Runtime Artifact became visible")
	}
}

type trackingObjectStore struct {
	workerapp.ObjectStore
	verifyCalls int
	verifyErr   error
}

func (store *trackingObjectStore) Verify(ctx context.Context, key, checksum string) error {
	store.verifyCalls++
	if store.verifyErr != nil {
		return store.verifyErr
	}
	return store.ObjectStore.Verify(ctx, key, checksum)
}

type recordingMetadataStore struct {
	publishCalls int
	record       artifactapp.PublishTurnEvidenceRecord
}

func (store *recordingMetadataStore) PublishTurnEvidence(
	_ context.Context,
	record artifactapp.PublishTurnEvidenceRecord,
) (artifactapp.PublishedTurnEvidence, error) {
	store.publishCalls++
	store.record = record
	ids := make([]string, 0, len(record.Command.Evidence.ArtifactRefs))
	for _, ref := range record.Command.Evidence.ArtifactRefs {
		ids = append(ids, ref.ArtifactID)
	}
	return artifactapp.PublishedTurnEvidence{
		TraceStreamID: record.Command.Evidence.Manifest.TraceStreamID,
		ArtifactIDs:   ids,
	}, nil
}
