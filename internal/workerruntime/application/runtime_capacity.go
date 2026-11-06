package application

import "time"

// CapacityWaitReason is a diagnostic, never an authorization credential.
type CapacityWaitReason string

func (r CapacityWaitReason) Error() string { return string(r) }

const (
	CapacityUninitialized       CapacityWaitReason = "capacity_uninitialized"
	CapacityEvidenceInvalid     CapacityWaitReason = "capacity_evidence_invalid"
	CapacityEvidenceStale       CapacityWaitReason = "capacity_evidence_stale"
	CapacityIdentityDrift       CapacityWaitReason = "capacity_identity_drift"
	CapacityInventoryUnknown    CapacityWaitReason = "capacity_inventory_unknown"
	CapacityExhausted           CapacityWaitReason = "runtime_capacity"
	CapacityBindingMismatch     CapacityWaitReason = "capacity_binding_mismatch"
	CapacityRequestExpired      CapacityWaitReason = "capacity_request_expired"
	CapacityPathNotIntegrated   CapacityWaitReason = "capacity_path_not_integrated"
	CapacitySecretFenceUnproven CapacityWaitReason = "capacity_secret_fence_unproven"
)

type CapacityProfile struct {
	ID       string
	Revision uint64
	Digest   string
}

// CapacityTarget contains business identity only. Generated candidate permit,
// Run and Turn IDs and sample sequence deliberately do not enter its digest.
type CapacityTarget struct {
	Kind, TenantID, ActorID, SessionID, TaskID                              string
	Generation, SessionRevision, TaskRevision, QueueSequence, QueueRevision uint64
	Profile                                                                 CapacityProfile
}
type CapacityReservation struct {
	Guarantee                     *CapacityGuaranteeRef `json:"Guarantee,omitempty"`
	Token, CommitmentID, PermitID string
	SnapshotSequence              int64
	ExpiresAt                     time.Time
	RunID, TurnID, RuntimeID      string
	Released                      bool
}
type CapacityConsumption struct {
	Token, PermitID, RunID, TurnID, RuntimeID string
	Target                                    CapacityTarget
}
type CapacityDiagnostics struct {
	Status                          CapacityWaitReason
	Revision, LatestSequence        int64
	ResidentCount, ActiveExecutions int64
}
