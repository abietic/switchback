# Fenced Terminal Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use TDD and implement each task as one red-green vertical slice.

**Goal:** Preserve an exact late `agent_turn.terminal` as cancellation evidence after Backend deadline force-stop wins, without weakening fencing or changing the authoritative Backend deadline source.

**Architecture:** A Runtime-control channel freezes an immutable fence snapshot when it first enters `fenced`. The exact pre-fence stream may append only its terminal event after that point; normal events remain rejected. Mutable inbox cursors may continue to advance, while Backend deadline write-fence and cleanup proofs consume only the immutable snapshot.

**Tech Stack:** Go, PostgreSQL migrations, pgx integration tests.

## Global Constraints

- `force_stop_intent` and the durable channel fence remain one transaction.
- The relaxed path accepts only an exact `event/agent_turn.terminal`.
- The late event cannot mutate `runtime_instances` or any Backend deadline source field.
- Existing shared Backend source Apply behavior must be preserved.
- All race outcomes remain serialized by the AgentTurn outcome advisory lock.

---

### Task 1: Prove the real post-fence failure

**Files:**

- Modify: `internal/workerruntime/adapter/postgres/turn_settlement_source_integration_test.go`

**Interfaces:**

- Consumes: `TurnSettlementSourceService.AdvanceBackendDeadline`, `RuntimeControlJournalService.AcceptEvent`.
- Produces: a real PostgreSQL regression test using the pre-fence authority after a committed `force_stop_intent`.

- [ ] Replace the raw-SQL fake force fixture with the public force transition.
- [ ] Assert the channel is durably fenced and the source is `force_stop_intent`.
- [ ] Accept the exact terminal with the saved old authority.
- [ ] Run the test and record the current `ErrRuntimeControlStaleOwner` RED.

### Task 2: Freeze immutable channel fence evidence

**Files:**

- Add: `migrations/000017_runtime_control_fence_snapshot.sql`
- Modify: `internal/architecture/migration_schema_test.go`
- Modify: `internal/workerruntime/application/runtime_control_journal.go`
- Modify: `internal/workerruntime/adapter/postgres/runtime_control_journal.go`

**Interfaces:**

- Produces: nullable `fenced_at` and `fence_revision` populated together exactly once when channel status first becomes `fenced`.
- Preserves: mutable channel `revision` and `updated_at` for later journal cursor changes.

- [ ] Add migration schema RED assertions.
- [ ] Add upgrade backfill and consistency constraints plus reversible down migration.
- [ ] Expose the snapshot on `RuntimeControlChannelState`.
- [ ] Make first fence atomically set snapshot; replayed fence leaves it unchanged.
- [ ] Run architecture and Runtime-control tests GREEN.

### Task 3: Consume the immutable snapshot in deadline proofs

**Files:**

- Modify: `internal/workerruntime/adapter/postgres/turn_settlement_source.go`
- Modify: `internal/localproduct/runtime_deadline_source.go`
- Modify/add focused PostgreSQL and localproduct tests.

**Interfaces:**

- Consumes: channel `FencedAt` and `FenceRevision`.
- Produces: write-fence and cleanup verification independent of later cursor revisions.

- [ ] Add a RED showing a post-fence cursor advance no longer invalidates cleanup.
- [ ] Read and validate the immutable snapshot instead of channel `updated_at/current revision`.
- [ ] Run write-fence/cleanup tests GREEN.

### Task 4: Accept exact fenced terminal evidence

**Files:**

- Modify: `internal/workerruntime/adapter/postgres/runtime_control_journal.go`
- Modify: `internal/workerruntime/adapter/postgres/turn_settlement.go`
- Modify: `internal/workerruntime/adapter/postgres/turn_settlement_source_integration_test.go`

**Interfaces:**

- Consumes: old `RuntimeControlAuthority`, immutable channel fence snapshot, exact forced Backend deadline settlement.
- Produces: journaled terminal message, advanced channel event cursor, and exact settlement witness.

- [ ] Under the AgentTurn outcome lock, fall back from normal authority only for an exact fenced terminal.
- [ ] Require `status=fenced`, exact process nonce/stream attempt, and `current owner_fence=old+1`.
- [ ] Require exact settlement identity and source phase `force_stop_intent|write_fenced|cleanup_confirmed`.
- [ ] Journal the event and advance only channel cursor state; do not update Runtime aggregate.
- [ ] Attach witness with a source-preserving compare-and-swap update.
- [ ] Return exact duplicate replay without mutation.
- [ ] Reject ordinary events and all wrong authority/settlement combinations.

### Task 5: Prove end-to-end convergence

**Files:**

- Modify/add focused tests in the files above.

**Interfaces:**

- Consumes: fenced terminal acceptance and existing Backend source Apply.
- Produces: force → late terminal → write fence → cleanup → ignore Apply convergence.

- [ ] Assert immutable fence snapshot remains stable across Accept and Apply.
- [ ] Assert Runtime revision/last-event remain frozen at Accept.
- [ ] Assert source bytes and digest remain unchanged.
- [ ] Assert write-fence, cleanup and late ignore Apply complete.
- [ ] Run formatting, architecture/application tests, and focused real PostgreSQL integration tests.
