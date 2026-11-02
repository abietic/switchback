# ADR 0051: Count Continuity-Holding WorkerSessions Against User Quota

- Status: Accepted
- Date: 2026-07-14

## Context

The P0 limit of 10 “active WorkerSessions per user” was ambiguous. A Session can be idle or waiting for approval while still retaining a WorkingCopy, Agent state, credentials boundary and the promise of continued interaction. Counting only Sessions with a running container would let users exceed the intended continuity and storage boundary by accumulating idle or `waiting_user` Sessions. Counting queued Tasks would have the opposite problem: a request with no allocated Session consumes no continuity resource yet.

The exact counting and full-capacity behavior must also preserve ADR 0050: capacity pressure cannot justify silently reusing, resetting or evicting an existing Session.

## Decision

P0 defines a `WorkerSessionContinuityQuota` scoped by `(tenant_id, user_id)`, with a default limit of 10.

- `allocating`, `interactive`, `running`, `recovering`, `waiting_user` and `idle` WorkerSessions each consume one continuity slot.
- A `lost` Session continues to consume one slot until reconciliation proves recovery or commits a terminal transition. This prevents an uncertain old runtime from being ignored while a replacement allocation exceeds the quota.
- `closed` and `expired` Sessions do not consume a slot after the terminal transition and quota release commit. Runtime, mount and storage cleanup may complete asynchronously and does not reopen the slot transition.
- A queued Task without `worker_session_id` consumes no continuity slot. Its execution and idle clocks have not started.
- At quota, an otherwise valid new-Session request receives a monotonic `session_allocation_sequence` in the Tenant/User `SessionAllocationQueue` but no Session queue sequence. A Task remains `queued` and unassigned with `allocation_wait_reason=session_capacity`; a post-terminal continuation remains a pending `WorkerSessionAllocationRequest` under ADR 0052.
- Capacity pressure never triggers automatic Session reuse, reset, cleanup, close or eviction. The user may explicitly target an exact existing Session under ADR 0050, close an eligible Session, or cancel the queued Task.
- Explicit reuse of an existing counted Session does not consume another continuity slot. It still obeys that Session's own FIFO execution queue and inherits its current state.
- Worker Runtime's `WorkerSessionAllocator` owns the Tenant/User allocation sequence and queue. Task Orchestration submits Task-owned requests; the continuation application submits post-terminal continuation requests. When capacity becomes available, the allocator considers the lowest legal `session_allocation_sequence`, revalidates owner revision, Workspace access and policy, then atomically reserves quota and creates one `allocating` WorkerSession. Invalid or cancelled heads are resolved before moving to the next request; a legal head is not skipped.
- The WorkerSession `allocating` transition and continuity-slot reservation commit atomically under an expected quota revision and idempotency key. Concurrent allocators cannot raise `reserved_count` above the policy limit. Response loss returns the original Session identity.
- A terminal Session transition releases the slot and emits an owner event. Allocation reconciliation repairs lost release notifications and stale reservations without creating two Sessions for one Task.
- The quota does not cap queued Task count. Task creation/rate limiting is a separate policy concern.

## Consequences

- `waiting_user` and idle Sessions consume scarce continuity capacity even when they use little CPU. This is intentional because they retain resumable state and can survive for up to the 14-day absolute limit.
- Users can submit work while full, but execution waits visibly for Session capacity.
- Product surfaces must show the current count, limit, capacity wait reason and allowed actions without implying that an unassigned Task already has a Worker.
- Per-Tenant/User scope avoids assuming that user identities or policy limits are globally shared across Tenant security boundaries.
- The close/expire terminal transition is the exact quota-release boundary. [ADR 0052](0052-make-terminal-workersessions-immutable-and-continue-by-derivation.md) makes both identities immutable and sends any continuation through a new allocation.

## Alternatives Retained

- Introduce separate CPU/container, storage and continuity quotas after observed utilization data shows that one P0 limit is too coarse.
- Add organization-level burst pools or administrator overrides without changing the per-Session counting semantics.
