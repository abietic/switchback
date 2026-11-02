package application

import "time"

type WorkerSessionSnapshot struct {
	WorkerSessionID   string
	TenantID          string
	UserID            string
	WorkspaceID       string
	WorkingCopyID     string
	Status            string
	ActiveWorkerRunID string
	BoundTaskID       string
	Revision          uint64
	RuntimeGeneration uint64
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

type RuntimeInstanceSnapshot struct {
	RuntimeInstanceID        string
	WorkerSessionID          string
	WorkerRunID              string
	Generation               uint64
	Status                   string
	ResourceProfileID        string
	NetworkProfile           ProfileRef
	NetworkEnforcementStatus string
	Revision                 uint64
	CreatedAt                time.Time
}
