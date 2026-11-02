# Durable First-Heartbeat Watchdog Implementation Plan

> Status: approved by the ongoing reliability iteration. Preserve unrelated dirty worktree changes; do not stage or commit.

**Goal:** Bound the gap between the first authenticated `ResumeComplete` commit and the first real persisted Runtime heartbeat. Reconnects must not refresh that 45/90-second window, Backend restarts must not erase it, and ordinary transport close must never be treated as proof of Runtime failure.

**Architecture:** Add a deep PostgreSQL-backed `RuntimeFirstHeartbeatWatchdog` module. The immutable anchor is created in the same transaction that commits the first successful Runtime control resume. The first real heartbeat satisfies it in the existing heartbeat transaction. A fenced ScheduledCommand evaluator advances `awaiting -> suspect -> unhealthy` against the original deadlines and reuses the existing `heartbeat_unhealthy` recovery path. Startup drains overdue watchdog commands before Runtime execution authority is enabled and uses the durable anchor when no heartbeat observation exists.

**Safety boundary:**

- Never create a fake `runtime_heartbeat_observations` row with sequence zero.
- Never refresh `anchor_at`, `suspect_due_at`, or `unhealthy_due_at` on reconnect.
- A late heartbeat cannot revive a watchdog or Runtime generation already classified `unhealthy`.
- A leased evaluator is not cancelled behind its back; it completes as a CAS no-op after a heartbeat wins.
- Backend deadline/terminal settlement ownership remains authoritative and suppresses competing recovery.
- `worker_control_closed` remains readable as historical database data but is ineligible for newly opened recovery incidents.

## Task 1: Freeze the durable schema and application contract

**Files:**

- Add: `migrations/000018_runtime_first_heartbeat_watchdog.sql`
- Add: `internal/workerruntime/application/runtime_first_heartbeat.go`
- Add: `internal/workerruntime/application/runtime_first_heartbeat_test.go`
- Modify: `internal/architecture/migration_schema_test.go`

**RED tests:**

- The schema rejects sequence zero, inconsistent status proof, and mutable/deadline-inconsistent anchors.
- Application validation rejects incomplete identity, invalid stage, zero revisions, and caller-supplied wall-clock evaluation time.
- Classification is exact at T+45 and T+90.

**GREEN implementation:**

- Persist one watchdog per Runtime generation/channel with an immutable anchor.
- Represent absence of a heartbeat with nullable proof fields.
- Use strict stage and status enums and an original-anchor deadline check.

## Task 2: Arm on first authenticated ResumeComplete

**Files:**

- Modify: `internal/workerruntime/application/runtime_control_journal.go`
- Modify: `internal/workerruntime/adapter/postgres/runtime_control_journal.go`
- Modify: `internal/workerruntime/adapter/postgres/runtime_control_journal_integration_test.go`

**RED tests:**

- First Complete atomically commits channel state, watchdog, and the T+45 ScheduledCommand.
- Failed credential/owner/cursor/settlement admission creates no watchdog.
- Duplicate Complete and a later reconnect preserve the original anchor and one suspect command.
- A heartbeat observation committed before Complete prevents arming.
- An already-unhealthy watchdog makes a later Complete fail closed.

**GREEN implementation:**

- Generate the first ScheduledCommand ID before entering the store transaction.
- Insert the watchdog and command after the connected channel update, in the same transaction.
- Use insert-once semantics; never update the anchor on conflict.

## Task 3: Satisfy on the first real heartbeat

**Files:**

- Modify: `internal/workerruntime/adapter/postgres/runtime_heartbeat.go`
- Modify: `internal/workerruntime/adapter/postgres/runtime_heartbeat_integration_test.go`

**RED tests:**

- Sequence one satisfies `awaiting` or `suspect` with real heartbeat proof.
- Duplicate and stale heartbeats cannot replace the first proof.
- A heartbeat restores a suspect Runtime to active.
- Pending watchdog commands are cancelled; a leased evaluator later completes as a safe no-op.
- A heartbeat after the immutable unhealthy boundary cannot revive that generation.

**GREEN implementation:**

- Lock Session, Runtime, watchdog, then heartbeat observation.
- Satisfy the watchdog and persist the real observation in one transaction.
- Cancel only pending watchdog evaluations and clear lease fields when cancelling existing heartbeat liveness commands.

## Task 4: Evaluate T+45/T+90 and converge to recovery

**Files:**

- Add: `internal/workerruntime/adapter/postgres/runtime_first_heartbeat.go`
- Add: `internal/workerruntime/adapter/postgres/runtime_first_heartbeat_handler.go`
- Add: `internal/workerruntime/adapter/postgres/runtime_first_heartbeat_integration_test.go`
- Modify: `internal/workerruntime/adapter/postgres/recovery.go`
- Modify: `internal/workerruntime/adapter/postgres/runtime_liveness_handler.go`

**RED tests:**

- T+44.999 and T+89.999 are no-ops.
- T+45 marks ordinary active Runtime suspect and schedules exactly one T+90 evaluation at the original boundary.
- T+45 does not break a replacement Runtime's `starting -> active` recovery CAS.
- T+90 freezes an unhealthy Runtime revision and opens exactly one `heartbeat_unhealthy` recovery.
- Heartbeat-vs-evaluator, deadline-vs-watchdog, and command retry races converge to one owner/outcome.

**GREEN implementation:**

- Evaluate against persisted deadlines and expected watchdog revision.
- Preserve stable recovery evidence and timestamp across retries.
- Reuse `RuntimeRecoveryService.Open`; never create a nested recovery for an existing replacement incident.

## Task 5: Make startup and transport semantics truthful

**Files:**

- Modify: `internal/localproduct/runtime_runner.go`
- Modify: `internal/localproduct/runtime_startup.go`
- Modify: `internal/localproduct/runtime_startup_restore_test.go`
- Modify: `internal/localproduct/runtime_startup_integration_test.go`
- Modify: `internal/workerruntime/application/runtime_heartbeat.go`
- Modify: `internal/workerruntime/adapter/postgres/runtime_failure_observer.go`
- Modify: `internal/workerruntime/domain/recovery.go`
- Modify affected tests.

**RED tests:**

- Startup at T+60 drains the original suspect transition before enabling supervision or dispatch.
- Startup after T+90 opens the original generation's single recovery incident.
- Startup candidate timing prefers heartbeat, then watchdog anchor, then explicit legacy Runtime fallback.
- A running, disconnected but otherwise unproven Runtime stays quarantined; it is not labelled `worker_control_closed`.
- Confirmed container exit/missing and durable ACP EOF remain eligible immediate triggers.

**GREEN implementation:**

- Drain and continuously poll first-heartbeat commands across startup handoff.
- Add the watchdog anchor to startup candidate hydration.
- Remove the control-close lifecycle observer/API and the startup shortcut that opens recovery from lack of in-memory ownership.
- Keep the historical trigger constant/database value for old incident reads, but make it ineligible for new opens.

## Verification

```bash
go test ./internal/workerruntime/application ./internal/workerruntime/domain -count=1
go test -race ./internal/workerruntime/application ./internal/workerruntime/domain ./internal/localproduct -count=1
SWITCHBACK_TEST_DATABASE_URL="$SWITCHBACK_TEST_DATABASE_URL" go test -tags=integration ./internal/workerruntime/adapter/postgres ./internal/localproduct -run 'FirstHeartbeat|RuntimeControlResume|RuntimeHeartbeat|StartupRuntime' -count=1
make verify
openspec validate --all --strict
```
