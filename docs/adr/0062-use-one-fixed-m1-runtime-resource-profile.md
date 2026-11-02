# ADR 0062: Use One Fixed M1 Runtime Resource Profile

- Status: Accepted
- Date: 2026-07-16

## Context

M1 keeps live Worker runtimes resident and permits up to ten continuity-holding WorkerSessions. Docker host defaults would allow one build or several concurrent AgentTurns to exhaust the machine. Multiple user-selectable profiles would improve flexibility but introduce resource authorization, selection, fairness and upgrade semantics before actual workload measurements exist.

## Decision

- M1 exposes one server-owned `RuntimeResourceProfile=local_standard_v1`: 2 vCPU, 4 GiB memory, 512 PIDs and a 10 GiB container writable layer.
- RuntimeLease snapshots the exact profile revision. TaskSpec, Frontend, Assistant, Agent, Skill and MCP cannot submit arbitrary resource values.
- WorkingCopy, cache and Agent state use separately governed host-managed persistent storage and do not rely on the container writable layer.
- RuntimeCapacityAdmission defaults aggregate CPU, memory and storage execution budgets to at most 80% of Docker Engine availability. WorkerSessionContinuityQuota remains 10 and does not promise ten simultaneous heavy AgentTurns.
- Backend admits the legal Session queue head or standalone start before SessionDispatchPermit. Insufficient capacity preserves resident Session and FIFO position with `runtime_capacity`; it creates no active AgentTurn, `started_at` or 24-hour deadline.
- AgentTurn terminal releases capacity idempotently and wakes the next legal head without preemption or reordering.
- CPU excess is throttled. Confirmed OOM, PID exhaustion or writable-layer exhaustion records `resource_exhausted` and enters an explainable waiting path instead of opening ordinary automatic recovery under the same limit.

## Consequences

- M1 has deterministic per-container safety limits and a small product surface.
- Some builds may not fit 4 GiB; deployment configuration can publish a new profile revision, but M1 does not offer per-Task numeric tuning or profile selection.
- Persistent WorkingCopy/cache/state quotas remain a separate decision because Docker writable-layer limits do not govern bind-mounted host storage.
- Docker Desktop/Linux enforcement parity, storage-driver support and aggregate reservation/sampling require implementation spikes.

## Alternatives

- Server-owned `small/standard/large` profiles: retained for later workloads after observed demand justifies selection and scheduling rules.
- Docker host defaults with no hard limits: rejected because failure can affect Switchback and unrelated local work.
- Treat continuity quota 10 as execution concurrency 10: rejected because resumable identity and active compute are different scarce resources.
