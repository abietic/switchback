package application

import (
	"context"
	"fmt"
	"time"
)

const (
	ObjectStateTemporary = "temporary"
	ObjectStateFinalized = "finalized"

	ReconciliationDeleted = "deleted"
	ReconciliationFailed  = "failed"
)

type ObjectCandidate struct {
	Key        string
	State      string
	Checksum   string
	Size       int64
	ModifiedAt time.Time
}

type ClaimedObject = ObjectCandidate

type ObjectDeletion struct {
	Deleted       bool
	AlreadyAbsent bool
}

type ObjectInventory interface {
	ListCandidates(context.Context, time.Time, int) ([]ObjectCandidate, error)
	DeleteCandidate(context.Context, ObjectCandidate) (ObjectDeletion, error)
}

type ClaimOrphansRecord struct {
	Candidates     []ObjectCandidate
	ClaimedAt      time.Time
	ClaimExpiresAt time.Time
}

type OrphanCompletion struct {
	Candidate   ObjectCandidate
	Status      string
	ErrorCode   string
	CompletedAt time.Time
}

type OrphanMetadataStore interface {
	ClaimOrphanObjects(context.Context, ClaimOrphansRecord) ([]ClaimedObject, error)
	CompleteOrphanObject(context.Context, OrphanCompletion) error
}

type ReconcileOrphansCommand struct {
	OlderThan time.Time
	Limit     int
}

type OrphanReconciliationResult struct {
	Observed int
	Claimed  int
	Deleted  int
	Failed   int
}

type OrphanReconciler struct {
	Objects  ObjectInventory
	Metadata OrphanMetadataStore
	Now      func() time.Time
}

func (reconciler OrphanReconciler) Execute(
	ctx context.Context,
	command ReconcileOrphansCommand,
) (OrphanReconciliationResult, error) {
	if reconciler.Objects == nil || reconciler.Metadata == nil || reconciler.Now == nil {
		return OrphanReconciliationResult{}, fmt.Errorf("orphan reconciler dependencies are required")
	}
	if command.OlderThan.IsZero() || command.Limit <= 0 || command.Limit > 10_000 {
		return OrphanReconciliationResult{}, fmt.Errorf("orphan reconciliation cutoff and bounded limit are required")
	}
	candidates, err := reconciler.Objects.ListCandidates(ctx, command.OlderThan.UTC(), command.Limit)
	if err != nil {
		return OrphanReconciliationResult{}, fmt.Errorf("list ObjectStore orphan candidates: %w", err)
	}
	now := reconciler.Now().UTC()
	if now.IsZero() || now.Before(command.OlderThan) {
		return OrphanReconciliationResult{}, fmt.Errorf("orphan reconciliation server time is invalid")
	}
	claimed, err := reconciler.Metadata.ClaimOrphanObjects(ctx, ClaimOrphansRecord{
		Candidates: candidates, ClaimedAt: now, ClaimExpiresAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		return OrphanReconciliationResult{}, fmt.Errorf("claim ObjectStore orphans: %w", err)
	}
	result := OrphanReconciliationResult{Observed: len(candidates), Claimed: len(claimed)}
	for _, candidate := range claimed {
		deletion, deleteErr := reconciler.Objects.DeleteCandidate(ctx, candidate)
		completion := OrphanCompletion{
			Candidate: candidate, Status: ReconciliationDeleted, CompletedAt: reconciler.Now().UTC(),
		}
		if deleteErr != nil || !deletion.Deleted && !deletion.AlreadyAbsent {
			completion.Status = ReconciliationFailed
			completion.ErrorCode = "object_delete_failed"
			result.Failed++
		} else {
			result.Deleted++
		}
		if err := reconciler.Metadata.CompleteOrphanObject(ctx, completion); err != nil {
			return result, fmt.Errorf("record ObjectStore reconciliation outcome: %w", err)
		}
	}
	return result, nil
}
