# Backend Deadline Source-Union Implementation Plan

> Status: approved by the ongoing iteration goal. Implement incrementally with recovery disabled by default until fixed-image crash-window acceptance passes. Preserve unrelated dirty worktree changes and do not stage or commit unless explicitly requested.

**Goal:** Make the fixed AgentTurn hard deadline a durable terminal source that survives Backend restart and converges exactly once without requiring a Runtime terminal event.

**Architecture:** `WorkerTurnSettlement` owns one immutable `runtime_event | backend_deadline` source. Runtime recovery remains a preemptible execution claim. A Backend deadline freezes its grace deadline before external effects, journals interrupt/fencing/cleanup proof in an append-only source state, and uses the existing settlement lease to serialize evidence, domain, Task, source-apply and continuation phases.

**Safety boundary:** The first implementation supports deadline authority for the current local single-user Runtime profile. It does not claim security-termination integration, multi-Backend active-active ownership or fixed-image production acceptance.

---

## Task 1: Generalize the durable settlement header

**Files:**

- Add: `migrations/000016_turn_settlement_source_union.sql`
- Modify: `internal/architecture/migration_schema_test.go`
- Modify: `internal/workerruntime/application/turn_settlement.go`
- Add: `internal/workerruntime/application/turn_settlement_source.go`
- Modify: `internal/workerruntime/adapter/postgres/turn_settlement.go`
- Add: `internal/workerruntime/adapter/postgres/turn_settlement_source.go`
- Modify/add focused application and PostgreSQL integration tests

**RED tests:**

- Existing Runtime terminal acceptance backfills and reads an exact `runtime_event` source.
- A Backend deadline creates an accepted settlement without a Runtime-control message foreign key.
- Exact duplicate claim returns the original settlement; conflicting tenant, identity, deadline, reference or digest changes no state.
- A persisted terminal source cannot be replaced by a competing source.
- Source state and digest are canonical, append-only and restart-readable.

**GREEN implementation:**

- Add immutable `source_kind`, reason, occurrence time, reference and digest.
- Add canonical `source_state` plus digest.
- Make Runtime-event identity columns conditionally nullable while retaining their foreign keys for non-null values.
- Preserve current phase values and expose a source-applied alias rather than rewriting in-flight checkpoints.
- Backfill every existing settlement as `runtime_event` with its exact event identity.

**Verify:**

```bash
go test ./internal/architecture ./internal/workerruntime/application -run 'Migration|TurnSettlementSource' -count=1
SWITCHBACK_TEST_DATABASE_URL="$SWITCHBACK_TEST_DATABASE_URL" go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TurnSettlementSource|RuntimeControlTerminalAcceptance' -count=1
```

## Task 2: Linearize deadline, Runtime event and recovery claims

**Files:**

- Modify: `internal/workerruntime/adapter/postgres/turn_outcome_authority.go`
- Modify: `internal/workerruntime/adapter/postgres/runtime_control_journal.go`
- Modify: `internal/workerruntime/adapter/postgres/recovery.go`
- Modify: `internal/workerruntime/adapter/postgres/recovery_advance.go`
- Modify/add focused concurrency integration tests

**RED tests:**

- Runtime terminal accepted before `deadline_at` wins and a later deadline claim is a no-op.
- Exact-boundary or later terminal acceptance attaches to the existing Backend-deadline settlement without changing its source.
- Deadline-first blocks RuntimeRecovery Open for the same Turn.
- Recovery-first is atomically marked preempted and bound to the deadline settlement while Session, Run, Turn and Task remain unchanged.
- Controlled concurrent waiters prove all three paths use the same per-AgentTurn advisory transaction lock.

**GREEN implementation:**

- Let `ClaimBackendDeadline`, terminal Accept and Recovery Open share one lock before mutable graph locks.
- Treat recovery as execution ownership, never as a terminal source.
- Move recovery deadline preemption from direct aggregate mutation to source ownership transfer.
- Preserve exact late Runtime events only as cancellation evidence.

**Verify:**

```bash
SWITCHBACK_TEST_DATABASE_URL="$SWITCHBACK_TEST_DATABASE_URL" go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'Deadline.*Authority|Recovery.*Authority|Terminal.*Authority' -count=10
```

## Task 3: Persist deadline interrupt and force-stop effects

**Files:**

- Modify: `internal/localproduct/runtime_runner.go`
- Modify: `internal/localproduct/runtime_settlement.go`
- Modify: `internal/localproduct/runtime_settlement_recovery.go`
- Modify/add focused unit and PostgreSQL integration tests

**RED tests:**

- Deadline is claimed before the first interrupt send.
- `force_after = deadline_at + 30s` remains unchanged after Backend restart.
- Crash after interrupt journal reservation does not reserve or send a second identity.
- Terminal witness before force intent prevents fencing.
- Force intent before a late terminal witness keeps deadline authority and records the witness as stale evidence.
- Cleanup response loss reuses exact Runtime/generation/fence identity.

**GREEN implementation:**

- Advance source state through `claimed`, `interrupt_enqueued`, `force_stop_intent`, `write_fenced` and `cleanup_confirmed`.
- Execute no external effect before its owning state is durable.
- Reuse the Runtime-control command journal and `RuntimeCleanupService`.
- Require proven write fencing before the forced path becomes settlement-ready.

**Verify:**

```bash
go test ./internal/localproduct -run 'BackendDeadline|TurnSettlement' -count=1
go test -race ./internal/localproduct -run 'BackendDeadline|TurnSettlement' -count=1
```

## Task 4: Commit forced terminality and apply the source atomically

**Files:**

- Modify: `internal/workerruntime/application/turn_settlement_domain.go`
- Modify: `internal/workerruntime/adapter/postgres/turn_settlement_domain.go`
- Modify: `internal/workerruntime/adapter/postgres/turn_settlement.go`
- Modify: `internal/localproduct/runtime_settlement.go`
- Modify/add focused application and PostgreSQL integration tests

**RED tests:**

- Forced domain commit atomically advances Runtime, Session, Run, Turn and settlement revisions.
- An injected failure rolls back every aggregate and proof.
- Backend source without an event advances source-applied once under the exact settlement lease.
- Attached Runtime event Apply advances inbox cursor and source-applied in one transaction.
- Response loss returns the original domain/source-apply result.

**GREEN implementation:**

- Add a forced-deadline domain branch that consumes exact cleanup proof.
- Remove the post-domain `recordForcedRuntimeStop` transaction.
- Generalize source application while retaining the exact Runtime inbox transaction.
- Keep queued continuation gated after source application.

**Verify:**

```bash
SWITCHBACK_TEST_DATABASE_URL="$SWITCHBACK_TEST_DATABASE_URL" go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'ForcedDeadline|TurnSettlementDomain|TerminalApply' -count=1
```

## Task 5: Restore pending deadlines and run crash-window gates

**Files:**

- Modify: `internal/localproduct/runtime_startup.go`
- Modify: `internal/localproduct/runtime_settlement_recovery.go`
- Modify/add startup and fault-injection tests
- Modify: `openspec/changes/single-runtime-recovery/tasks.md` only after evidence passes

**RED tests:**

- Restart at every deadline source-state and settlement phase resumes the exact identity.
- A pending Backend source does not require an in-memory terminal delivery.
- Ordinary recovery, dispatch and queued continuation cannot overtake it.
- Repeated restart does not reset grace, repeat prompt, duplicate cleanup or create a crash loop.

**GREEN implementation:**

- Restore source-kind-specific settlement workers before enabling ordinary polling.
- Materialize storage and exact cleanup targets from durable checkpoint proof.
- Keep recovery disabled in Compose until fixed-image acceptance proves the complete matrix.

**Verify:**

```bash
go test ./... -count=1
go test -race ./internal/localproduct ./internal/workerruntime/application ./internal/workerruntime/adapter/controlgrpc -count=1
SWITCHBACK_TEST_DATABASE_URL="$SWITCHBACK_TEST_DATABASE_URL" go test -tags=integration ./internal/workerruntime/adapter/postgres ./internal/localproduct -count=1
npx --yes @fission-ai/openspec@1.6.0 validate single-runtime-recovery --strict
make verify
```
