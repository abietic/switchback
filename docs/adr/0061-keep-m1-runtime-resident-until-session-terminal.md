# ADR 0061: Keep M1 Runtime Resident Until Session Terminal

- Status: Accepted
- Date: 2026-07-16

## Context

WorkerSession is intended to feel like a directly reopenable Coding Agent rather than a batch job. Stopping the container after every AgentTurn or after a short idle period would reduce memory use, but would introduce planned checkpoint, suspend, resume, concurrent wake-up and ACP recovery behavior before normal session continuity and crash recovery are proven.

## Decision

- M1 fixes RuntimeResidencyPolicy to `m1_resident_until_session_terminal_v1`; clients cannot override it.
- After first allocation, WorkerSession in `interactive`, `running`, `waiting_user` or `idle` retains one running RuntimeInstance with Shim, Codex ACP and LiteLLM.
- AgentTurn/WorkerRun terminality, stage delivery, `waiting_user` and ordinary idle do not stop, pause, hibernate or replace the container and do not increment runtime generation.
- Residency does not authorize execution. Without an active AgentTurn and SessionDispatchPermit, Shim cannot start another ACP prompt, Tool or external action; heartbeat/control, Trace/Artifact drain and explicit SessionProcesses governed by [ADR 0063](0063-manage-cross-turn-background-processes-explicitly.md) may continue.
- Runtime stops or is replaced only for explicit close, 48-hour idle expiry, 14-day absolute expiry, authorization/security termination or RuntimeRecoveryIncident fencing.
- M1 defines no `suspending`, `suspended` or `resuming` runtime state and does not use `docker pause`.

## Consequences

- Follow-up interaction has immediate TUI-like continuity and keeps in-memory Agent/process context available.
- A `waiting_user` runtime may remain resident until the 14-day absolute boundary because only its idle clock pauses. Every resident Session continues consuming continuity quota and real host CPU, memory, PID and storage limits.
- Resource profiles, aggregate admission and manual close are required safeguards for the ten-Session continuity limit.
- Durable WorkingCopy and Agent state remain outside the container, so a later hibernation profile can be introduced without changing WorkerSession identity or ownership.

## Alternatives

- Stop and checkpoint after a short hot-idle period: retained as a future hibernation profile when measured resource pressure justifies its state and recovery complexity.
- Stop after every AgentTurn: rejected because it turns normal follow-up chat into a restore workflow.
- `docker pause`: rejected because it retains memory and complicates heartbeat, deadline and lease semantics without providing useful release.
