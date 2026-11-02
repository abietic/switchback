# ADR 0055: Use A Fixed AgentTurn Hard Deadline

- Status: Accepted
- Date: 2026-07-15
- Supersedes: ADR 0020, ADR 0054

## Context

Switchback P0 needs a reliable way to stop one Coding Agent interaction from occupying a container and execution slot forever. The previous design expanded that requirement into pausable active-time clocks, WorkerRun and Tool budgets, user grants, Task cumulative accounting and continuation-specific budget confirmation.

Those mechanisms do not improve the core P0 interaction enough to justify their protocol, persistence, authorization and UI cost. Time is also a poor proxy for model cost; LLM rate and cost protection should remain an independent LLMProxy concern.

## Decision

Every top-level `AgentTurn` has one fixed `86400s` wall-clock hard deadline.

- The deadline starts when the backend Worker Runtime owner transaction accepts `StartAgentTurn` for dispatch, using server-observed time and a persisted `deadline_at`. The later Shim/ACP invocation timestamp is observational Trace, not deadline authority.
- The deadline never pauses. Model inference, Tool execution, `active_quiet`, approval or other `waiting_user` time, transport disconnection and Runtime recovery all consume the same 24-hour window.
- Each AgentTurn is independent. `steer` modifies the current Turn without resetting its deadline; `interrupt` ends it; an explicitly queued authenticated user message may start a later Turn only after the current Turn is terminal. Agent output, retry logic and Runtime recovery cannot mint another top-level Turn.
- At the deadline, Worker requests ACP cancellation and cancels the current Tool/process using the existing evidence and fencing rules. The AgentTurn becomes terminal `timed_out`. Results arriving after terminality are Trace-only late results and cannot rewrite state.
- Deadline enforcement is dual. The backend `ScheduledCommand` is the sole state, audit and fencing authority; the Runtime Shim arms a non-resettable local monotonic watchdog for the same AgentTurn revision and Runtime generation so a disconnected container cannot run indefinitely. Either path emits the same idempotent cancellation identity, and reconnect converges to backend terminal evidence.
- AgentTurn start uses backend-precommitted one-way dispatch. In one transaction the backend persists `started_at`、`deadline_at` and ScheduledCommand and appends the `StartAgentTurn` Outbox command. The delivered command carries exact Turn revision、Runtime generation、ChannelEpoch、deadline and shared cancellation identity; no second Ready/Permit round trip is required.
- Before calling ACP, Shim validates command freshness and arms its monotonic watchdog for a conservative remaining duration derived from backend deadline and control-channel server-time uncertainty. Transport latency therefore consumes part of the 24-hour window; uncertainty may stop locally slightly early. A stale/expired command or unverifiable time estimate fails closed. Retries cannot reset the watchdog or invoke the prompt twice.
- A confirmed cancellation does not by itself terminate the WorkerRun or Task. A Task-bound interaction moves to `waiting_user:agent_turn_timed_out`; the user may start a new independent AgentTurn in the same live WorkerSession/WorkerRun. Unconfirmed local or external cancellation follows the existing tainted-runtime or reconciliation path before more work is accepted.
- WorkerRun and Task have no time budget, cumulative time account or time-extension action. TaskSpec does not carry user-selectable duration fields.
- A Tool has no separate business-level timeout profile. It inherits the current AgentTurn deadline and can also be cancelled by the user. Network, process-shutdown and provider transport timeouts remain infrastructure safeguards, not domain budgets or user grants.
- `ContinueTaskInNewWorker` continues to require the exact actor, Task revision, expiry outcome, durable checkpoint and idempotency evidence, but carries no `ContinuationBudgetSpec`.
- WorkerSession idle timeout remains 48 hours and absolute lifetime remains 14 days. Runtime heartbeat, one-attempt recovery, resource limits, continuity quota and cancellation/fencing remain independent safeguards.
- LLMProxy may enforce tenant/user/model rate, token or cost limits independently. Those controls must not be represented as AgentTurn time extensions.

## Consequences

- P0 removes `ExecutionBudgetPolicy`, `ExecutionBudgetGrant`, `ActiveExecutionClock`, `WorkerRunDeadline`, `ToolTimeout`, `TaskCumulativeActiveBudget`, `ContinuationBudgetSpec` and their UI/Delivery flows.
- One persisted `deadline_at`, one scheduled deadline command, one matching Runtime watchdog and terminal fencing are sufficient for time enforcement.
- Dual enforcement adds server-time uncertainty handling and reconciliation cost, but prevents a control-channel partition from turning the fixed deadline into a best-effort policy.
- One-way dispatch avoids a per-Turn Ready/Permit handshake. Its accepted trade-off is that dispatch latency and clock-uncertainty margin reduce usable execution time slightly; P0 prefers this to another distributed start state.
- Waiting for approval overnight can consume the Turn deadline. If it expires, the user starts a new Turn instead of extending the old one.
- Runtime recovery time also consumes the deadline, so a long platform incident may time out a Turn. Recovery does not silently create replacement execution authority.
- Future evidence may justify per-tool or cost controls, but they must be introduced as separate revisions rather than restoring the removed P0 budget model implicitly.

## Alternatives

- Pausable 24-hour active time: rejected because it retains pause-reason accounting, recovery reconciliation and clock persistence complexity.
- WorkerSession lifetime only: rejected because one stuck Turn could occupy an execution slot until the 14-day Session limit.
- Layered WorkerRun/AgentTurn/Tool budgets: superseded because the control surface is disproportionate to P0 needs.
