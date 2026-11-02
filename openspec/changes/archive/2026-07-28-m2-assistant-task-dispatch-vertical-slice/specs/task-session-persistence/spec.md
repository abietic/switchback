## MODIFIED Requirements

### Requirement: Direct TaskSpec admission is explicit and server-scoped
The system SHALL allow the SingleUserLocalProfile caller to create a Task from either an explicit direct TaskSpec or an Assistant-produced exact ready TaskDraft while requiring server-injected tenant/user identity, an exact authorized Workspace, a ready TaskReadinessAssessment, an ExplicitTaskCreationIntent bound to the exact TaskSpec digest and an idempotency key.

#### Scenario: Valid direct TaskSpec is submitted
- **WHEN** the local caller submits goal, deliverables, acceptance criteria, constraints, `workspace_id`, execution policy and an enabled AgentProfile
- **THEN** Gateway injects the fixed actor, rejects client identity overrides, produces a zero-question readiness assessment with no blocking unknown and records an intent bound to actor, TaskSpec digest, Workspace and trigger-policy revision before submitting CreateTask

#### Scenario: Valid Assistant TaskDraft is confirmed
- **WHEN** the fixed local actor confirms the exact current ready TaskDraft/readiness/Workspace/digest/policy revisions through the registered confirmation action
- **THEN** Task Orchestration revalidates the immutable creation intent and atomically creates Task, initial TaskTransition, SessionAllocationRequest, and owner-stream Outbox through the same admission path as direct TaskSpec

#### Scenario: Assistant confirmation or delivery is duplicated
- **WHEN** the same creation intent, TaskSpec digest, actor, Workspace, and idempotency key are consumed again after duplicate delivery or response loss
- **THEN** Task Orchestration returns the original Task and command identities without creating another Task, transition, allocation request, or Outbox event

#### Scenario: Workspace or TaskSpec evidence is missing
- **WHEN** `workspace_id`, readable WorkspaceRevision, required acceptance criteria, readiness, creation intent, exact Assistant draft binding, or server policy evidence is missing or stale
- **THEN** Task Orchestration rejects the command without creating Task, TaskTransition, allocation request or Outbox record
