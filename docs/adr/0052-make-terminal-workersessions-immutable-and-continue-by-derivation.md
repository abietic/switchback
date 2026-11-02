# ADR 0052: Make Terminal WorkerSessions Immutable And Continue By Derivation

- Status: Accepted
- Date: 2026-07-14

## Context

WorkerSession has a 48-hour idle timeout, a 14-day absolute lifetime, continuity quota accounting, Session-scoped approvals and credentials, and a durable interaction identity. Reopening a `closed` or `expired` identity would either reset those boundaries or attach new authority to an identity whose terminal audit fact says it can no longer execute. It would also make the 14-day limit renewable in practice.

Users still need a practical way to continue useful work from a terminal Session. The continuation must preserve inspectable work without pretending that the old Runtime, AgentSession, authorization or lifetime remains valid.

## Decision

`closed` and `expired` are immutable WorkerSession terminal states.

- A terminal WorkerSession never transitions back to `allocating`, `interactive`, `running`, `recovering`, `waiting_user` or `idle`. It cannot accept AgentTurns, WorkerRuns, Runtime leases, dispatch permits, approval grants or credential leases.
- Its Worker detail remains available as a permission-filtered read-only record containing Timeline, Trace, Artifact, Task/Run lineage and durable WorkingCopy checkpoint metadata.
- Continuing work is an explicit `CreateWorkerSessionContinuation` command. It targets the exact source WorkerSession revision and an exact durable checkpoint reference, revalidates actor, Tenant, WorkspaceMembership, policy and checkpoint readability, and uses an idempotency key.
- A successful continuation always creates a new `worker_session_id`, records `continued_from_worker_session_id` and `continued_from_checkpoint_ref`, and starts new 48-hour idle and 14-day absolute clocks from the new Session creation time.
- P0 creates a new AgentSession. It does not resume or clone the old AgentSession identity. A user may explicitly include approved ContextPack or Artifact references, but old prompts, Agent state and model context are not silently imported.
- The new Session does not inherit Runtime generation, ChannelEpoch, SessionDispatchPermit, queue position, ApprovalGrant, RepositoryAccessGrant, SecretLease or any other Session-scoped authority. Every required authority is re-evaluated and newly issued for the new identity.
- The source checkpoint is immutable. Materialization creates a new isolated WorkingCopy with provenance; it never resets, cleans or mutates the terminal source. For graceful close, the platform should prepare a final durable checkpoint before terminal commit. If expiry or failure prevents that, continuation may use only the latest proven durable checkpoint and must expose its capture time, digest and coverage warning.
- P0 separates mutable live-state persistence from immutable continuation checkpoints. Non-terminal WorkingCopy、Agent state and Trace/control cursors remain in WorkspaceStorage and are reused directly for same-Session Runtime generation recovery; that path does not require a continuation checkpoint.
- Continuation checkpoints are best-effort semantic snapshots, attempted at AgentTurn terminal、entry to `waiting_user`、stage-result publication and graceful close. P0 does not promise a periodic interval, byte threshold or numeric RPO. Forced host/storage failure may preserve only the latest proven checkpoint; the UI must show its age and coverage instead of claiming uncheckpointed live state survived.
- SessionAllocationQueue orders generic `WorkerSessionAllocationRequest` records. `request_kind=task` preserves the existing queued Task flow; `request_kind=session_continuation` carries the source Session and checkpoint refs. Both use the same Tenant/User FIFO and continuity quota. A continuation cannot jump earlier legal allocation requests.
- Capacity reservation plus new `allocating` WorkerSession creation remains atomic under ADR 0051. Duplicate continuation commands or response loss return the same new Session identity. A failed or cancelled continuation request cannot produce an interactive orphan.
- [ADR 0053](0053-preserve-task-identity-across-workersession-absolute-expiry.md) defines the non-terminal Task exception: absolute expiry terminalizes the old Run, releases the current executable binding, preserves Task identity and requires explicit continuation as a new Run in a derived Session.

## Consequences

- The 14-day absolute lifetime remains a real hard boundary rather than a renewable timestamp.
- Audit readers can distinguish runtime recovery, live Session reuse and post-terminal continuation by identity alone.
- Users retain a direct continuation workflow, but must obtain fresh capacity and authorization.
- Terminal evidence can be retained independently from disposable Runtime/container cleanup.
- P0 avoids periodic snapshot/journal machinery, accepting that catastrophic host/storage failure may lose state after the latest semantic checkpoint.
- Frontend and Delivery must label the action as “Continue in new Worker”, never “Resume” or “Reopen”. They must show the source checkpoint age and any coverage warning before confirmation.

## Alternatives Retained

- Reopen `closed` but not `expired`: rejected for P0 because close would still become a way to release and reacquire quota while retaining hidden Session authority.
- Reopen the same identity with a new generation: rejected because Runtime generation is a crash-recovery fence inside one live Session, not a new lifetime or authorization boundary.
- Copy the prior AgentSession automatically: retained only as a future adapter-specific migration feature after an explicit, auditable context-transfer protocol exists.
