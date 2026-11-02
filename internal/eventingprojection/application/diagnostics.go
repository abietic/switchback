package application

import (
	"context"
	"time"
)

// WorkerDiagnosticQuery binds the caller to the current server-owned M1 scope.
// WorkspaceID is never taken from request input.
type WorkerDiagnosticQuery struct {
	Viewer          Viewer
	WorkspaceID     string
	WorkerSessionID string
	WorkerRunID     string
	AgentTurnID     string
}

type WorkerDiagnosticTarget struct {
	StartedAt time.Time
	EndedAt   time.Time
}

type WorkerDiagnosticReader interface {
	ReadWorkerDiagnosticTarget(context.Context, WorkerDiagnosticQuery) (WorkerDiagnosticTarget, error)
}
