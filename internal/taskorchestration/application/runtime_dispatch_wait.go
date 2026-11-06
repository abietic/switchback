package application

import (
	"context"
	"fmt"
	"time"
)

// RuntimeDispatchWait is diagnostic evidence, never execution authority.
// Its binding is internal; public projections expose only reason and observed_at.
type RuntimeDispatchWait struct {
	Reason          string    `json:"reason"`
	ObservedAt      time.Time `json:"observed_at"`
	TaskRevision    uint64    `json:"task_revision"`
	WorkerSessionID string    `json:"worker_session_id,omitempty"`
	QueueSequence   uint64    `json:"queue_sequence,omitempty"`
	QueueRevision   uint64    `json:"queue_revision,omitempty"`
}

func ValidRuntimeDispatchWaitReason(reason string) bool {
	switch reason {
	case "capacity_uninitialized", "capacity_evidence_invalid", "capacity_evidence_stale",
		"capacity_identity_drift", "capacity_inventory_unknown", "capacity_exhausted", "capacity_path_not_integrated":
		return true
	}
	return false
}

func (wait RuntimeDispatchWait) ValidateBinding() error {
	if !ValidRuntimeDispatchWaitReason(wait.Reason) || wait.TaskRevision == 0 {
		return fmt.Errorf("invalid runtime dispatch wait reason or revision")
	}
	if wait.WorkerSessionID == "" {
		if wait.QueueSequence != 0 || wait.QueueRevision != 0 {
			return fmt.Errorf("unassigned wait has queue binding")
		}
	} else {
		if err := validateDispatchStrings(map[string]string{"worker_session_id": wait.WorkerSessionID}); err != nil {
			return err
		}
		if wait.QueueSequence == 0 || wait.QueueRevision == 0 {
			return fmt.Errorf("assigned wait requires exact queue binding")
		}
	}
	return nil
}

type RuntimeDispatchWaitObservation struct {
	// ExpectedPermitIDs is the immutable issuance history captured with the
	// owner snapshot, in UTF-8 byte order. Empty means never issued, not merely
	// no active permit. It is private expected binding, never public diagnostics.
	ExpectedPermitIDs []string
	TenantID          string
	UserID            string
	TaskID            string
	Wait              RuntimeDispatchWait
}

type RuntimeDispatchWaitRecord struct {
	Observation RuntimeDispatchWaitObservation
	EventID     string
}

type RuntimeDispatchWaitStore interface {
	RecordRuntimeDispatchWait(context.Context, RuntimeDispatchWaitRecord) (RuntimeDispatchWait, error)
}

type RuntimeDispatchWaitService struct {
	Store RuntimeDispatchWaitStore
	IDs   IDGenerator
}

func (service RuntimeDispatchWaitService) Record(ctx context.Context, observation RuntimeDispatchWaitObservation) (RuntimeDispatchWait, error) {
	if service.Store == nil || service.IDs == nil {
		return RuntimeDispatchWait{}, fmt.Errorf("runtime dispatch wait dependencies required")
	}
	eventID, err := service.IDs.NewID("integration-event")
	if err != nil {
		return RuntimeDispatchWait{}, err
	}
	return service.Store.RecordRuntimeDispatchWait(ctx, RuntimeDispatchWaitRecord{Observation: observation, EventID: eventID})
}

func (observation RuntimeDispatchWaitObservation) Validate() error {
	if err := validateDispatchStrings(map[string]string{"tenant_id": observation.TenantID, "user_id": observation.UserID, "task_id": observation.TaskID}); err != nil {
		return err
	}
	if err := observation.Wait.ValidateBinding(); err != nil {
		return err
	}
	if observation.Wait.WorkerSessionID == "" && len(observation.ExpectedPermitIDs) > 0 {
		return fmt.Errorf("unassigned wait has issuance history")
	}
	for i, id := range observation.ExpectedPermitIDs {
		if err := validateDispatchStrings(map[string]string{"permit_id": id}); err != nil {
			return err
		}
		if i > 0 && observation.ExpectedPermitIDs[i-1] >= id {
			return fmt.Errorf("issuance history must be strictly ordered")
		}
	}
	return nil
}
