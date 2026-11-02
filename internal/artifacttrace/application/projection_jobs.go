package application

import (
	"context"
	"errors"
	"time"
)

const (
	DurableProjectionSchemaVersion = "langfuse.projection.v2"
	MaxProjectionLease             = 5 * time.Minute
)

var ErrProjectionLeaseLost = errors.New("projection lease lost")

type ProjectionSourceRef struct {
	TraceStreamID    string
	ManifestChecksum string
}

type ProjectionJob struct {
	Source        ProjectionSourceRef
	SchemaVersion string
	LeaseToken    int64
	Attempts      int
	Status        string
	NextAttemptAt time.Time
	LeaseUntil    time.Time
}

type ProjectionCompletion struct {
	Status    string
	ErrorCode string
	RetryAt   time.Time
}

type ProjectionJobStore interface {
	Activate(context.Context, string) error
	Discover(context.Context, string, int) (int, error)
	Claim(context.Context, string, time.Duration) (ProjectionJob, bool, error)
	Finish(context.Context, ProjectionJob, ProjectionCompletion) error
	RequestSource(context.Context, string, ProjectionSourceRef) error
}

// SourceProjector exports one immutable source projection and verifies the
// source-scoped readback. Persistent retry scheduling remains owned by the
// projection worker.
type SourceProjector interface {
	ExportAndVerify(context.Context, SourceProjection) error
}

type ProjectionError struct {
	Code      string
	Permanent bool
}

func (err *ProjectionError) Error() string { return err.Code }

func permanentProjectionError(code string) error {
	return &ProjectionError{Code: code, Permanent: true}
}

func retryableProjectionError(code string) error {
	return &ProjectionError{Code: code, Permanent: false}
}
