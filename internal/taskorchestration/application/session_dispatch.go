package application

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type EnqueueTaskCommand struct {
	TenantID                string
	UserID                  string
	TaskID                  string
	WorkerSessionID         string
	ExpectedTaskRevision    uint64
	ExpectedSessionRevision uint64
	IdempotencyKey          string
}

type EnqueueTaskRecord struct {
	Command      EnqueueTaskCommand
	QueueEntryID string
	EventID      string
	Now          time.Time
}

type EnqueuedTask struct {
	TaskID               string
	WorkerSessionID      string
	SessionQueueSequence uint64
	QueueRevision        uint64
	TaskRevision         uint64
}

type IssueTaskPermitCommand struct {
	TenantID             string
	UserID               string
	WorkerSessionID      string
	RuntimeGeneration    uint64
	CapacityAdmissionRef string
	IdempotencyKey       string
}

type IssueTaskPermitRecord struct {
	Command   IssueTaskPermitCommand
	PermitID  string
	Now       time.Time
	ExpiresAt time.Time
}

type IssuedTaskPermit struct {
	PermitID                string
	WorkerSessionID         string
	TaskID                  string
	SessionQueueSequence    uint64
	QueueRevision           uint64
	ExpectedSessionRevision uint64
	RuntimeGeneration       uint64
}

type SessionDispatchStore interface {
	EnqueueTask(context.Context, EnqueueTaskRecord) (EnqueuedTask, error)
	IssueTaskPermit(context.Context, IssueTaskPermitRecord) (IssuedTaskPermit, error)
}

type SessionDispatchService struct {
	Store     SessionDispatchStore
	IDs       IDGenerator
	Now       func() time.Time
	PermitTTL time.Duration
}

func (service SessionDispatchService) EnqueueTask(
	ctx context.Context,
	command EnqueueTaskCommand,
) (EnqueuedTask, error) {
	if err := service.validate(); err != nil {
		return EnqueuedTask{}, err
	}
	if err := validateDispatchStrings(map[string]string{
		"tenant_id": command.TenantID, "user_id": command.UserID, "task_id": command.TaskID,
		"worker_session_id": command.WorkerSessionID, "idempotency_key": command.IdempotencyKey,
	}); err != nil {
		return EnqueuedTask{}, err
	}
	if command.ExpectedTaskRevision == 0 || command.ExpectedSessionRevision == 0 {
		return EnqueuedTask{}, fmt.Errorf("Task and Session expected revisions are required")
	}
	queueEntryID, err := service.IDs.NewID("session-task-queue-entry")
	if err != nil {
		return EnqueuedTask{}, err
	}
	eventID, err := service.IDs.NewID("integration-event")
	if err != nil {
		return EnqueuedTask{}, err
	}
	return service.Store.EnqueueTask(ctx, EnqueueTaskRecord{
		Command: command, QueueEntryID: queueEntryID, EventID: eventID, Now: service.Now().UTC(),
	})
}

func (service SessionDispatchService) IssueTaskPermit(
	ctx context.Context,
	command IssueTaskPermitCommand,
) (IssuedTaskPermit, error) {
	if err := service.validate(); err != nil {
		return IssuedTaskPermit{}, err
	}
	if err := validateDispatchStrings(map[string]string{
		"tenant_id": command.TenantID, "user_id": command.UserID,
		"worker_session_id":      command.WorkerSessionID,
		"capacity_admission_ref": command.CapacityAdmissionRef,
		"idempotency_key":        command.IdempotencyKey,
	}); err != nil {
		return IssuedTaskPermit{}, err
	}
	if command.RuntimeGeneration == 0 {
		return IssuedTaskPermit{}, fmt.Errorf("runtime generation is required")
	}
	permitID, err := service.IDs.NewID("session-dispatch-permit")
	if err != nil {
		return IssuedTaskPermit{}, err
	}
	now := service.Now().UTC()
	ttl := service.PermitTTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if ttl < 0 {
		return IssuedTaskPermit{}, fmt.Errorf("permit TTL must be positive")
	}
	return service.Store.IssueTaskPermit(ctx, IssueTaskPermitRecord{
		Command: command, PermitID: permitID, Now: now, ExpiresAt: now.Add(ttl),
	})
}

func (service SessionDispatchService) validate() error {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return fmt.Errorf("Session dispatch dependencies are required")
	}
	return nil
}

func validateDispatchStrings(values map[string]string) error {
	for name, value := range values {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s must be canonical", name)
		}
	}
	return nil
}
