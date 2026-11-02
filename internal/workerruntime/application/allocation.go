package application

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type AllocationIDGenerator interface {
	NewID(kind string) (string, error)
}

type AllocateWorkerSessionCommand struct {
	TenantID            string
	UserID              string
	AllocationRequestID string
}

type AllocationIDs struct {
	WorkerSessionID   string
	WorkingCopyID     string
	EventID           string
	IdleCommandID     string
	AbsoluteCommandID string
}

type AllocationRecord struct {
	Command AllocateWorkerSessionCommand
	IDs     AllocationIDs
	Now     time.Time
}

type AllocationResult struct {
	AllocationRequestID       string
	SessionAllocationSequence uint64
	WorkerSessionID           string
	QuotaRevision             uint64
	CapacityFull              bool
}

type WorkerSessionAllocationStore interface {
	AllocateWorkerSession(context.Context, AllocationRecord) (AllocationResult, error)
}

type WorkerSessionAllocator struct {
	Store WorkerSessionAllocationStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (allocator WorkerSessionAllocator) Execute(
	ctx context.Context,
	command AllocateWorkerSessionCommand,
) (AllocationResult, error) {
	if allocator.Store == nil || allocator.IDs == nil || allocator.Now == nil {
		return AllocationResult{}, fmt.Errorf("WorkerSession allocator dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "user_id": command.UserID,
		"allocation_request_id": command.AllocationRequestID,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return AllocationResult{}, fmt.Errorf("%s must be canonical", name)
		}
	}
	ids, err := allocator.createIDs()
	if err != nil {
		return AllocationResult{}, err
	}
	return allocator.Store.AllocateWorkerSession(ctx, AllocationRecord{
		Command: command, IDs: ids, Now: allocator.Now().UTC(),
	})
}

func (allocator WorkerSessionAllocator) createIDs() (AllocationIDs, error) {
	kinds := []string{
		"worker-session", "working-copy", "integration-event",
		"scheduled-command-idle", "scheduled-command-absolute",
	}
	values := make([]string, len(kinds))
	for index, kind := range kinds {
		value, err := allocator.IDs.NewID(kind)
		if err != nil {
			return AllocationIDs{}, fmt.Errorf("generate %s id: %w", kind, err)
		}
		if value == "" || strings.TrimSpace(value) != value {
			return AllocationIDs{}, fmt.Errorf("generated %s id is invalid", kind)
		}
		values[index] = value
	}
	return AllocationIDs{
		WorkerSessionID: values[0], WorkingCopyID: values[1], EventID: values[2],
		IdleCommandID: values[3], AbsoluteCommandID: values[4],
	}, nil
}
