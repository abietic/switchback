package application

import (
	"context"
	"errors"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const ObservationFragmentObjectKind = "observation-fragment"

var (
	ErrFragmentPublicationLeaseLost = errors.New("fragment publication lease lost")
	ErrFragmentSourceConflict       = errors.New("fragment source conflict")
)

type FragmentCandidate struct {
	Source     FragmentRef
	Frame      []byte
	Receipt    []byte
	LeaseToken int64
	LeaseUntil time.Time
	// Evidence binds the exact metadata whose objects were verified outside SQL.
	Evidence *FragmentEvidence
}

type FragmentPublicationStore interface {
	ClaimCandidate(context.Context, time.Duration) (FragmentCandidate, bool, error)
	Publish(context.Context, FragmentCandidate, workerapp.StoredObject) (FragmentRef, error)
	DeferCandidate(context.Context, FragmentCandidate, string, bool) error
	ReadFragmentEvidence(context.Context, FragmentRef) (FragmentEvidence, error)
}
