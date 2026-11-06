package application

import "errors"

var ErrRuntimeFailureCleanupPending = errors.New("failed Runtime cleanup is not proven")

// RuntimeFailureCancellationCommand is an internal owner command, never a new
// client wire action. The caller supplies the authenticated Task initiator and
// exact target/revisions; the Worker owner revalidates all of them in storage.
type RuntimeFailureCancellationCommand struct {
	TenantID, UserID, TaskID, WorkerSessionID, WorkerRunID string
	ExpectedTaskRevision, ExpectedSessionRevision          uint64
	IdempotencyKey                                         string
}

type RuntimeFailureCancellationResult struct {
	SessionRevision uint64
}
