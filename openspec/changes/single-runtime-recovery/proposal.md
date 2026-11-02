## Why

The M1 Worker currently turns any lost generation 1 container into a failed Run and lost Session even though the platform contract requires one automatic recovery attempt for eligible Runtime failures. Long-lived WorkerSessions need a durable, generation-fenced recovery path that preserves the original Session, Run, WorkingCopy, Agent state and evidence lineage without creating a crash loop.

## What Changes

- Add a durable `RuntimeRecoveryIncident` lifecycle with one automatic replacement attempt per continuous eligible incident.
- Detect eligible Runtime loss from hard evidence or heartbeat-based suspect/unhealthy progression using server receive time and sequence.
- Fence the old Runtime generation, ChannelEpoch, dispatch authority and generation-scoped secrets before provisioning a replacement.
- Create generation `N+1` for the same WorkerSession and WorkerRun, remount the existing WorkingCopy and Agent state, and resume from proven control and Trace cursors.
- Preserve the fixed AgentTurn wall-clock deadline while Runtime recovery is in progress.
- Close the incident only after replacement health and stable progress evidence; otherwise move the existing work to an actionable `waiting_user` state without another automatic attempt.
- Keep user cancellation, Session expiry, authorization/policy failure, protocol incompatibility and resource exhaustion outside automatic recovery.
- Fence late events and results from the old generation while retaining them as non-authoritative Trace evidence.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `runtime-execution-core`: Replace first-slice fail-closed Runtime loss with one durable, generation-fenced automatic replacement attempt for eligible failures.
- `task-session-persistence`: Preserve Task, WorkerSession and WorkerRun identity during Runtime recovery and surface only exhausted or ineligible recovery as `waiting_user` or terminal state.

## Impact

- Affects Worker Runtime domain/application services, PostgreSQL schema and adapters, RuntimeSupervisor, Docker provisioning, WorkerControl authentication and replay, Runtime secrets, schedulers, projections and failure acceptance.
- Requires ACP session resume/load evidence for the pinned Codex adapter; inability to prove safe resume exhausts the incident instead of silently creating a fresh AgentSession.
- Changes the current generation-1-only assumptions in Runtime loss, residency, restart reconciliation and acceptance tests.
- Runtime recovery does not extend the AgentTurn deadline, revive terminal WorkerSessions, retry unknown external side effects or grant new capabilities.
