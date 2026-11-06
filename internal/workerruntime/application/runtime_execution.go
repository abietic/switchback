package application

import "context"

// InitialRuntimeExecutionAuthorizer checks the committed initial execution at
// each external-effect boundary. It does not reserve another capacity token.
// This check precedes IO; it does not claim a database/IO atomic transaction.
type InitialRuntimeExecutionAuthorizer interface {
	AuthorizeInitialPreparation(context.Context, RuntimePreparationRequest) error
	AuthorizeInitialProvision(context.Context, RuntimePreparationRequest, RuntimeProvisionRequest) error
}

// CapacityContainerProofPending means the initial container has not yet appeared
// in fresh, trusted inventory. The caller must wait without advancing ownership.
const CapacityContainerProofPending CapacityWaitReason = "capacity_container_proof_pending"

// InitialRuntimeStartReplayPending retains historical ownership for conservative
// reconciliation; it never grants another environment or provisioning effect.
const InitialRuntimeStartReplayPending CapacityWaitReason = "initial_runtime_start_replay_pending"

// RejectUnintegratedExecution is the unconditional staging gate at new effect
// boundaries that have no capacity ledger integration. It has no allow branch.
func RejectUnintegratedExecution() error { return CapacityPathNotIntegrated }
