# ADR 0047: Govern Task Actions By Policy And Scoped Grants

- Status: Partially Superseded by ADR 0048; time-budget portions superseded by ADR 0055
- Date: 2026-07-13

## Context

A Task created from a shared Conversation may be visible to several participants and Workspace members. Treating visibility, Conversation control or Workspace editor role as authority to cancel or retry would let unrelated users interrupt work or increase cost. Making every action initiator-only would be clearer but would prevent deliberate delegation and leave no risk-reducing emergency path when the initiator is unavailable.

## Decision

Task Orchestration records an immutable `TaskInitiator` from the accepted creation intent and governs every lifecycle or cost command through a versioned `TaskActionPolicy`. P0 separates actions by effect:

- `cancel` is risk-reducing. The TaskInitiator may cancel, a current Workspace owner may issue an emergency cancel, and another user may cancel only when an exact TaskActionGrant includes it. The owner role alone grants no retry or delegation authority.
- `retry` may create another TaskAttempt/WorkerRun and therefore consume cost. Only the TaskInitiator or a current grantee whose `TaskActionGrant` explicitly includes retry may request it, and only while the same non-terminal TaskSaga exposes a retryable transition.

Workspace reviewer and editor roles do not grant Task lifecycle or cost actions. Conversation controller, group participation, WorkspaceChannelShareGrant, Task visibility and Artifact review also grant none of them. Any other user needs an exact TaskActionGrant for each otherwise unavailable action.

In P0, only the current TaskInitiator may issue or revoke a `TaskActionGrant`; a future organization capability may add a separate policy-authorized grantor, but Workspace owner alone is not one. A grant binds one Tenant, Workspace and Task; exact grantee; allowed action set; retry count and target constraints; source authority and policy revisions; expiry; evidence and revocation state. It cannot be delegated again or override Tenant/Workspace access, Task/Saga state, ApprovalPolicy, the fixed AgentTurn hard deadline, WorkerSession lifetime or external-provider authorization.

Every Task action command carries the actual actor, exact target, expected TaskSaga revision and idempotency key. Task Orchestration revalidates current memberships, policy, grant/source authority, action constraints and target state before one transactional transition. A terminal Task/Saga is never reopened: later retry-like work requires an explicit follow-up Task.

If TaskInitiator access is revoked, their actions and grants fail closed. A current Workspace owner may still emergency-cancel active work, but continuation or retry requires restored initiator authority, a still-valid previously issued grant whose source remains valid, or a new Task through the normal creation path. `TaskActionGrant` delegates Task commands; `ApprovalGrant` authorizes a protected Tool or external operation. Neither can extend the fixed AgentTurn deadline.

## Consequences

- Cost-increasing actions retain explicit user accountability while emergency cancellation remains available.
- Workspace collaboration and Task visibility do not silently become execution control.
- Delivery and Frontend must project actions for the current viewer and callbacks must reauthorize the clicking actor.
- P0 adds one narrowly scoped grant type and avoids a broad generic resource-role matrix.

## Alternatives Rejected

- Any Workspace editor can operate the Task: flexible but makes shared access an implicit cost and interruption authority.
- Initiator-only for every action: simple but prevents deliberate handoff and emergency risk reduction.
- Workspace owner can perform every action: provides continuity but lets a coordination role increase cost without explicit Task delegation.
