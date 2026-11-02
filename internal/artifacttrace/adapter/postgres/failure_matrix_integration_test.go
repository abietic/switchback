//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/testsupport/failurematrix"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestObjectStoreFinalizeRetryReturnsExistingImmutableObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity := workerapp.ObjectIdentity{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1", Kind: "report",
	}
	temporary, err := store.PutTemporary(ctx, identity, strings.NewReader("stage report"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Finalize(ctx, identity, temporary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Finalize(ctx, identity, temporary)
	if err != nil {
		t.Fatalf("Finalize() retry error = %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("Finalize() retry = %#v, want %#v", second, first)
	}
}

func TestEvidencePublicationRejectsChecksumMismatchAndMissingObject(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"checksum_mismatch", "missing_object"} {
		t.Run(variant, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Date(2026, time.July, 23, 14, 0, 0, 0, time.UTC)
			seedEvidenceTarget(t, pool, now)
			store, finalized := finalizeEvidence(t, now, finalizeCommand(t, now))
			var objects workerapp.ObjectStore = store
			if variant == "checksum_mismatch" {
				objects = failingVerifyStore{ObjectStore: store}
			} else {
				var report workerapp.StoredObject
				for _, object := range finalized.Objects {
					if object.Kind == "report" {
						report = object.Stored
					}
				}
				if report.Key == "" {
					t.Fatal("report object is missing from finalized evidence")
				}
				deletion, err := store.DeleteCandidate(ctx, artifactapp.ObjectCandidate{
					Key: report.Key, State: artifactapp.ObjectStateFinalized,
					Checksum: report.Checksum, Size: report.Size, ModifiedAt: now,
				})
				if err != nil || !deletion.Deleted {
					t.Fatalf("delete report object result=%#v error=%v", deletion, err)
				}
			}
			_, err := (artifactapp.ArtifactMetadataPublisher{
				Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool),
				IDs: &sequenceIDs{values: []string{"event-failure-unused"}},
				Now: func() time.Time { return now.Add(2 * time.Minute) },
			}).Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
				Evidence: finalized, ExpectedSessionRevision: 4, ExpectedRunRevision: 3,
				ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-failure-" + variant,
			})
			if !errors.Is(err, artifactapp.ErrEvidenceIntegrity) {
				t.Fatalf("publication error = %v, want evidence integrity", err)
			}
			assertCount(t, pool, "trace_manifests", 0)
			assertCount(t, pool, "artifact_refs", 0)
			assertCount(t, pool, "outbox_events", 0)
			if variant == "checksum_mismatch" {
				result := failurematrix.Result{
					Variant: "checksum-mismatch", ExecutionOutcome: "failed", AcceptanceOutcome: "pending",
					Assertions: []string{"checksum_rejected", "artifact_not_published", "outbox_not_emitted"},
				}
				failurematrix.AssertAndRecord(t, result, result)
			}
		})
	}
}

func TestEvidencePublicationRejectsWrongWorkspaceIdentity(t *testing.T) {
	t.Parallel()
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 23, 14, 30, 0, 0, time.UTC)
	seedEvidenceTarget(t, pool, now)
	command := finalizeCommand(t, now)
	command.Identity.WorkspaceID = "workspace-wrong"
	store, finalized := finalizeEvidence(t, now, command)

	_, err := (artifactapp.ArtifactMetadataPublisher{
		Objects: store, Metadata: artifactpostgres.NewMetadataStore(pool),
		IDs: &sequenceIDs{values: []string{"event-wrong-identity"}},
		Now: func() time.Time { return now.Add(2 * time.Minute) },
	}).Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: 4, ExpectedRunRevision: 3,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-wrong-identity",
	})
	if !errors.Is(err, artifactpostgres.ErrEvidenceTargetNotFound) {
		t.Fatalf("publication error = %v, want target not found", err)
	}
	assertCount(t, pool, "trace_manifests", 0)
	assertCount(t, pool, "artifact_refs", 0)
	assertCount(t, pool, "outbox_events", 0)
}

func finalizeEvidence(
	t *testing.T,
	now time.Time,
	command artifactapp.FinalizeTurnEvidenceCommand,
) (*artifactlocal.ObjectStore, artifactapp.FinalizedTurnEvidence) {
	t.Helper()
	store, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: store, IDs: &sequenceIDs{values: []string{"artifact-failure-report", "artifact-failure-patch"}},
		Now: func() time.Time { return now.Add(time.Minute) },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission: contractsv1.ArtifactPermission{
				Visibility: "private", PolicyRevision: "single-user-local-v1",
			},
			ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1",
		},
	}).Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	return store, finalized
}

type failingVerifyStore struct {
	workerapp.ObjectStore
}

func (failingVerifyStore) Verify(context.Context, string, string) error {
	return errors.New("injected checksum mismatch")
}
