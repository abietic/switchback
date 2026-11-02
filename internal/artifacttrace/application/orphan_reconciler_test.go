package application_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
)

func TestOrphanReconcilerDeletesOnlyDatabaseClaimedCandidatesAndRecordsFailures(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, time.July, 23, 11, 0, 0, 0, time.UTC)
	candidates := []artifactapp.ObjectCandidate{
		{Key: "objects/unreferenced", State: artifactapp.ObjectStateFinalized, Checksum: "sha256:aaa", Size: 10, ModifiedAt: cutoff.Add(-time.Hour)},
		{Key: "objects/referenced", State: artifactapp.ObjectStateFinalized, Checksum: "sha256:bbb", Size: 20, ModifiedAt: cutoff.Add(-time.Hour)},
		{Key: "temporary/stale", State: artifactapp.ObjectStateTemporary, Checksum: "sha256:ccc", Size: 30, ModifiedAt: cutoff.Add(-time.Hour)},
	}
	inventory := &fakeObjectInventory{
		candidates:   candidates,
		deleteErrors: map[string]error{"temporary/stale": errors.New("injected delete failure")},
	}
	metadata := &fakeOrphanMetadata{claimed: []artifactapp.ClaimedObject{candidates[0], candidates[2]}}
	reconciler := artifactapp.OrphanReconciler{
		Objects: inventory, Metadata: metadata,
		Now: func() time.Time { return cutoff.Add(time.Minute) },
	}

	result, err := reconciler.Execute(context.Background(), artifactapp.ReconcileOrphansCommand{
		OlderThan: cutoff, Limit: 100,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Observed != 3 || result.Claimed != 2 || result.Deleted != 1 || result.Failed != 1 {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(inventory.deleted, []string{"objects/unreferenced", "temporary/stale"}) {
		t.Fatalf("delete attempts = %#v", inventory.deleted)
	}
	if len(metadata.completions) != 2 || metadata.completions[0].Status != artifactapp.ReconciliationDeleted ||
		metadata.completions[1].Status != artifactapp.ReconciliationFailed ||
		metadata.completions[1].ErrorCode != "object_delete_failed" {
		t.Fatalf("completions = %#v", metadata.completions)
	}
}

func TestOrphanReconcilerRejectsInvalidSweepWithoutListingObjects(t *testing.T) {
	t.Parallel()
	inventory := &fakeObjectInventory{}
	reconciler := artifactapp.OrphanReconciler{
		Objects: inventory, Metadata: &fakeOrphanMetadata{}, Now: time.Now,
	}
	if _, err := reconciler.Execute(context.Background(), artifactapp.ReconcileOrphansCommand{}); err == nil {
		t.Fatal("invalid reconciliation command unexpectedly accepted")
	}
	if inventory.listCalls != 0 {
		t.Fatal("invalid command listed ObjectStore")
	}
}

type fakeObjectInventory struct {
	candidates   []artifactapp.ObjectCandidate
	deleteErrors map[string]error
	deleted      []string
	listCalls    int
}

func (inventory *fakeObjectInventory) ListCandidates(context.Context, time.Time, int) ([]artifactapp.ObjectCandidate, error) {
	inventory.listCalls++
	return append([]artifactapp.ObjectCandidate(nil), inventory.candidates...), nil
}

func (inventory *fakeObjectInventory) DeleteCandidate(_ context.Context, candidate artifactapp.ObjectCandidate) (artifactapp.ObjectDeletion, error) {
	inventory.deleted = append(inventory.deleted, candidate.Key)
	if err := inventory.deleteErrors[candidate.Key]; err != nil {
		return artifactapp.ObjectDeletion{}, err
	}
	return artifactapp.ObjectDeletion{Deleted: true}, nil
}

type fakeOrphanMetadata struct {
	claimed     []artifactapp.ClaimedObject
	completions []artifactapp.OrphanCompletion
}

func (metadata *fakeOrphanMetadata) ClaimOrphanObjects(
	_ context.Context,
	_ artifactapp.ClaimOrphansRecord,
) ([]artifactapp.ClaimedObject, error) {
	return append([]artifactapp.ClaimedObject(nil), metadata.claimed...), nil
}

func (metadata *fakeOrphanMetadata) CompleteOrphanObject(_ context.Context, completion artifactapp.OrphanCompletion) error {
	metadata.completions = append(metadata.completions, completion)
	return nil
}
