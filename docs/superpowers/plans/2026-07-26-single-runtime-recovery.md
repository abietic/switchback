# Single Runtime Recovery Implementation Plan

> Source of truth: `openspec/changes/single-runtime-recovery/`. This plan maps the approved design to the current production modules and keeps automatic recovery disabled until the fixed-image fault gate passes.

**Goal:** 对 eligible Runtime loss 只执行一次持久、generation-fenced 的自动 replacement，保留 WorkerSession、WorkerRun、AgentSession、WorkingCopy、Trace lineage 和原 AgentTurn deadline；无法证明 fencing 或 continuity 时进入可操作的 `waiting_user`，绝不 crash loop 或自动重放 prompt。

**Architecture:** Worker Runtime domain owns `RuntimeRecoveryIncident`; PostgreSQL commits every phase intent before Docker/filesystem/ACP effects. RuntimeSupervisor reconciles exact old/replacement identities. Control, secrets, mount authority and late events are fenced by generation plus ChannelEpoch. Task only consumes Worker-owned recovery events and never owns replacement.

**Non-goals:** multi-host failover、planned migration、第二次自动 attempt、自动重放 active prompt、未知外部副作用 retry、deadline 延期、long-poll fallback。

---

## Phase 1: Durable recovery contracts

### Task 1: Incident state machine

**Files**

- Create `internal/workerruntime/domain/recovery.go`
- Create `internal/workerruntime/domain/recovery_test.go`

**Steps**

1. Write failing tests for `open -> fencing -> provisioning -> reattaching -> verifying -> recovered|exhausted`.
2. Define trigger, phase, terminal outcome, continuity and fencing enums.
3. Require exact failed Runtime identity, positive generation/revision, one attempt maximum and monotonic phase transitions.
4. Reject reopening terminal incidents and any second replacement inside one incident.
5. Run `go test ./internal/workerruntime/domain -run Recovery`.

### Task 2: Additive persistence

**Files**

- Create `migrations/000013_single_runtime_recovery.sql`
- Create `internal/workerruntime/adapter/postgres/recovery.go`
- Create `internal/workerruntime/adapter/postgres/recovery_integration_test.go`
- Modify `internal/workerruntime/application/read_models.go`

**Steps**

1. Add tables for incidents, heartbeat observations, replacement intents, mount fences and idempotency results.
2. Add partial uniqueness for one active incident per Session and unique `(worker_session_id, failed_generation)`.
3. Persist phase/revision, previous Session state, exact failed/replacement identities, evidence refs, attempt, fencing/continuity outcome and timestamps.
4. Make duplicate same-digest command return the prior result and conflicting content fail closed.
5. Prove rollback/outbox failure cannot expose partial replacement authority.
6. Run migration checks and focused PostgreSQL integration tests.

### Task 3: Generation-N baseline

**Files**

- Modify `internal/workerruntime/application/runtime_loss.go`
- Modify `internal/workerruntime/application/runtime_residency.go`
- Modify `internal/workerruntime/application/runtime_cleanup.go`
- Modify `internal/workerruntime/application/agent_session_binding.go`
- Modify `internal/localproduct/runtime_runner.go`
- Modify affected tests

**Steps**

1. Add failing tests using generation 2 or 3.
2. Replace generation-1 equality checks with positive-generation and exact-current-generation checks.
3. Derive ChannelEpoch from persisted ownership; never hard-code `1` for replacement paths.
4. Keep historical generation-1 fixtures readable.
5. Run all Worker Runtime unit and integration tests.

## Phase 2: Liveness and incident opening

### Task 4: Durable independent heartbeat

**Files**

- Create `internal/workerruntime/application/runtime_heartbeat.go`
- Create `internal/workerruntime/application/runtime_heartbeat_test.go`
- Create `internal/workerruntime/adapter/postgres/runtime_heartbeat.go`
- Create `internal/workerruntime/adapter/postgres/runtime_heartbeat_integration_test.go`
- Modify `internal/workerruntime/adapter/controlgrpc/hub.go`
- Modify `internal/localproduct/runtime_runner.go`

**Steps**

1. Persist authenticated heartbeat server receive time and monotonic heartbeat sequence.
2. Accept duplicates idempotently, reject stale sequence mutation and ignore container wall clock.
3. Evaluate `suspect` at 45 seconds and `unhealthy` at 90 seconds through durable reconciliation commands.
4. Keep `active_quiet`, provider latency and missing token output nonterminal.
5. Open incidents immediately only for hard container/process/ACP/control loss evidence.

### Task 5: Transactional incident creation

**Files**

- Create `internal/workerruntime/application/runtime_recovery.go`
- Create `internal/workerruntime/application/runtime_recovery_test.go`
- Extend PostgreSQL recovery adapter
- Modify Task/Worker projection event contracts

**Steps**

1. Persist one incident, mark Session `recovering`, retain Task/Run/Turn identities and revoke dispatch permits in one transaction.
2. Emit owner-sequenced `RuntimeRecoveryStarted.v1` after state commit.
3. Converge duplicate heartbeat/control/Docker triggers on the same incident.
4. Exclude cancellation, expiry, authorization, policy, schema, protocol and resource exhaustion.

## Phase 3: Fencing and one replacement

### Task 6: Fence old authority

**Files**

- Extend `internal/workerruntime/application/runtime_recovery.go`
- Extend `internal/workerruntime/adapter/postgres/recovery.go`
- Modify `internal/workerruntime/adapter/controlgrpc/hub.go`
- Modify `internal/workerruntime/adapter/runtimesecret/*`
- Modify `internal/workerruntime/adapter/docker/provisioner.go`

**Steps**

1. Commit fencing intent before control, cleanup or secret effects.
2. Revoke dispatch, ChannelEpoch/control credential and generation-scoped secrets.
3. Stop/remove or isolate the old container.
4. Persist exclusive mount fencing proof for WorkingCopy, Agent state and Trace spool.
5. Exhaust without provisioning when write exclusivity is unproven.

### Task 7: Reserve and provision generation N+1

**Files**

- Extend PostgreSQL recovery adapter
- Modify `internal/localproduct/runtime_environment.go`
- Modify `internal/workerruntime/application/runtime_coordinator.go`
- Modify Docker/fake adapters and tests

**Steps**

1. Transactionally reserve one replacement RuntimeInstance, generation `N+1`, ChannelEpoch and idempotent replacement intent.
2. Reuse existing Session storage without recopying or cleaning the WorkingCopy.
3. Materialize fresh generation-scoped control/model secrets.
4. Provision the same image, resource profile and network profile under the committed identity.
5. Reconcile response loss by exact container labels and never reserve `N+2`.

## Phase 4: ACP continuity and health

### Task 8: Exact AgentSession reattachment

**Files**

- Modify `internal/workerruntime/application/ports.go`
- Modify `internal/workerruntime/adapter/codexacp/adapter.go`
- Modify `internal/workerruntime/adapter/shimruntime/runtime.go`
- Add fixed-image ACP recovery tests

**Steps**

1. Persist capability snapshot and exact cwd/MCP/model/cursor attachment inputs.
2. Prefer capability-declared resume; use `session/load` only before any new prompt.
3. Preserve AgentSession id and tag load replay with replacement generation.
4. Deduplicate replay against durable DisplayCheckpoint and Trace source ids.
5. Never resend the original prompt.
6. If active-turn effect continuity is unproven, terminate only that Turn with `recovery_continuity_unproven` and leave Session/Run waiting.

### Task 9: Verify before closing

**Files**

- Extend application/PostgreSQL recovery modules
- Modify deadline and expiry handlers
- Add verification tests

**Steps**

1. Require authenticated control, independent heartbeat, LiteLLM/ACP health, mount identity and cursor convergence.
2. Keep incident `verifying` until durable progress or the measured server-owned stability window.
3. Preserve original `started_at`, `deadline_at` and watchdog identity.
4. Let deadline, expiry and security termination preempt recovery.
5. A replacement failure before stability exhausts the sole attempt.

## Phase 5: Task projection, evidence and closeout

### Task 10: Project and fence late events

**Files**

- Modify `internal/eventingprojection/adapter/postgres/materializer.go`
- Modify `internal/workerruntime/application/read_models.go`
- Modify `web/src/api/contracts.ts`
- Modify `web/src/views/WorkerDetail.tsx`
- Modify Artifact/Display generation checks and tests

**Steps**

1. Project `recovering_runtime`, generations, attempt, evidence time and allowed action.
2. Keep Task running during eligible recovery.
3. On exhausted recovery, move nonterminal Task/Run/Session to `waiting_user:runtime_recovery_failed`; do not regress delivered/terminal Task.
4. Reject old-generation aggregate, Display and Artifact mutation while retaining Trace-only late evidence.
5. Prove projection replay and reconnect convergence.

### Task 11: Real fault acceptance and enablement

**Files**

- Extend `scripts/run-failure-acceptance`
- Extend `test/acceptance/failurematrix`
- Create recovery operations/manual acceptance docs
- Update requirements/architecture/OpenSpec tasks

**Steps**

1. Exercise idle loss, active loss, Backend restart, duplicate triggers, failed fencing, failed replacement, stale events and deadline preemption.
2. Measure stability window and cancellation grace with the pinned image.
3. Run `make verify`, frontend tests/build and Docker/fixed-image acceptance.
4. Run `openspec validate single-runtime-recovery --strict`.
5. Enable the server-owned one-attempt flag only after all conformance evidence passes.
6. Record rollback: disable new incidents, reconcile already-open incidents to recovered/exhausted, delete no state.
