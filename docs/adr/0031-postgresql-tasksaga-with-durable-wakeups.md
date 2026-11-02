# PostgreSQL TaskSaga With Durable Wakeups

P0 uses PostgreSQL as the authoritative Task and TaskSaga execution store. Every valid transition atomically updates Task/TaskSaga revision, appends SagaTransition, creates/cancels SagaWakeup, and writes Outbox records; network, container, model and external-provider calls happen after commit through idempotent owner commands. Durable retry, deadline and reconciliation are represented by SagaWakeup rows claimed with short leases, `SKIP LOCKED` and monotonic fencing tokens, so process crashes and duplicate delivery converge without in-memory timers.

## Status

Partially superseded by ADR 0048. PostgreSQL state, Outbox, idempotency and fenced scheduled-command mechanics remain valid; a public TaskSaga aggregate is no longer required in P0.

## Considered Alternatives

- **Temporal from P0:** strong durable timers, signals, replay and workflow visibility, but adds infrastructure, deterministic-code/versioning discipline, Event History management and a second execution-state system before P0 needs prove that cost.
- **Generic durable-workflow port:** appears replaceable but hides material differences between database state machines and replay-based workflow engines. P0 keeps stable business commands/events and narrow persistence interfaces instead of promising adapter-level substitution.

## Consequences

- PostgreSQL owns both business Task state and TaskSaga execution revision; EventCenter remains delivery/projection and ObjectStore JSONL remains Agent evidence.
- Pollers may deliver more than once; command handlers, Worker operations, compensations and external reconciliation must remain idempotent.
- No external call runs inside the TaskSaga transaction.
- Temporal remains a future re-evaluation, not a forbidden technology; adoption requires an explicit OpenSpec/ADR and in-flight Saga migration design.
