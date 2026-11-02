# Dual-Surface Memory Governance

Switchback exposes two separate governance projections. MemoryLibrary manages active, disabled, expired, and deleted MemoryTruth by user, workspace, and project scope. ReviewInbox manages pending Memory, Procedure, Instruction, and Skill ReviewTasks in separate lanes with content diff, evidence, deterministic policy result, risks, conflicts, scope/ACL impact, reviewer requirements, and age.

Users govern their own user-scope Memory; workspace and project content requires the corresponding owner or reviewer authority. Batch reject and disable are permitted with per-item authorization and expected-revision validation. Batch approve is limited to ReviewTasks with the same scope, policy revision, low-risk Memory type, reviewer authority, and no sensitive, conflict, security, permission, or scope-expansion tags. Procedure, Instruction, Skill, cross-scope, security, and conflicting content requires individual approval.

Edit creates a new immutable ContentRevision, disable removes the active projection, delete records a tombstone, and restore creates a new active revision from selected history. ReviewBatchCommand is idempotent and returns explicit per-item outcomes; partial success is reported as `completed_with_errors`. UI writes Switchback governance APIs and never OpenViking directly.

P0 has no blocking review SLA. Pending content is visible with age and digest reminders but never enters ContextPack or blocks current Task/Worker execution. Tenant-configured reviewer SLA remains a later operational policy.

## Status

Accepted as design baseline.

## Consequences

- Users can distinguish memories already influencing Agent context from candidates that have no effect yet.
- High-risk content cannot hide inside convenient bulk approval.
- Batch operations scale review while preserving per-item authorization, concurrency checks, and failure visibility.
- Frontend projections remain decoupled from Evolution write models and OpenViking storage details.
