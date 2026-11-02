# Healthy Runtime Control Resume Implementation Plan

> Status: approved by the ongoing iteration goal. Implement with recovery disabled until the full fixed-image gate passes. Preserve unrelated dirty worktree changes and do not stage or commit unless explicitly requested.

**Goal:** Let the same live Worker Shim resume its exact WorkerControl channel after transport or Backend restart without changing Runtime generation/ChannelEpoch or replaying an AgentTurn prompt.

**Architecture:** PostgreSQL owns a deep `ControlJournal` containing exact control authority, fenced stream ownership, both directional message journals, cumulative cursors and command-effect state. gRPC carries an explicit `ResumeHello`/`ResumeAck` replay plan followed by a `ResumeReady`/`ResumeComplete` convergence barrier before ordinary envelopes. Shim execution and ACP/LiteLLM lifetime remain independent from transient control streams.

**Safety boundary:** This guarantees prompt non-replay only while the same live Shim process retains and reports its exact effect state. A Shim/process failure across the ACP prompt-invocation boundary remains `recovery_continuity_unproven`; the system never automatically resends the prompt.

---

## Task 1: Add explicit resume-frame contracts

**Files:**

- Modify: `internal/contracts/v1/workercontrol.go`
- Modify: `internal/contracts/v1/contracts_test.go`
- Add: `api/schema/v1/runtime-control-frame.schema.json`
- Modify: protocol-generation/schema checks if they enumerate schemas

**RED tests:**

- Reject an ordinary envelope before `ResumeHello`.
- Validate exact Runtime identity, positive stream attempt, process nonce, cursor monotonicity and cursor digests.
- Validate active-Turn effect identity and phases `received`, `prompt_invocation_intent`, `active`, `terminal`.
- Reject a frame containing more than one variant or a non-accepted `ResumeAck` without a rejection code.

**GREEN implementation:**

- Add `RuntimeControlFrame`, `RuntimeControlResumeHello`, `RuntimeControlResumeAck`, `RuntimeControlResumeReady`, `RuntimeControlResumeComplete`, cursor and active-effect types. Ordered replay followed by Ready lets Complete carry the final cumulative receipt ACK without a circular intermediate ACK frame.
- Publish one direction-separated, versioned cursor hash-chain algorithm with fixed vectors for Backend and Shim.
- Bind active-Turn start/terminal digests to `RuntimeControlEnvelopeDigest` of the exact journaled envelopes; prompt-only hashes are not authority.
- Keep `RuntimeControlEnvelope` semantics unchanged inside the envelope variant.
- Use strict JSON decoding and bounded payloads; compile and exercise the real JSON Schema while keeping Go validation authoritative for unsigned cursor arithmetic and cross-field digest equality.

**Verify:**

```bash
go test ./internal/contracts/v1 -run 'RuntimeControl(Frame|Resume)' -count=1
```

## Task 2: Persist control authority and bidirectional journal

**Files:**

- Add: `migrations/000014_runtime_control_resume.sql`
- Add: `internal/workerruntime/application/runtime_control_journal.go`
- Add: `internal/workerruntime/application/runtime_control_journal_test.go`
- Add: `internal/workerruntime/adapter/postgres/runtime_control_journal.go`
- Add: `internal/workerruntime/adapter/postgres/runtime_control_journal_integration_test.go`
- Modify: `internal/architecture/migration_schema_test.go`

**RED tests:**

- Bind the exact Runtime credential digest once; exact duplicate is idempotent and conflicting digest fails.
- Claim a newer stream attempt and prove the previous owner fence cannot append or ACK.
- Reserve commands monotonically before send and return the original sequence for an exact idempotent duplicate.
- Accept events monotonically before ACK; exact duplicate returns the stored outcome, while gaps and conflicting fingerprints fail.
- Restart the repository against the same PostgreSQL state and reconstruct the same replay plan.
- Reconcile an authenticated active-Turn effect ACK from `delivered` to `consumed` once.

**GREEN implementation:**

- `runtime_control_channels` stores exact identity, token digest, process/stream identity, owner fence, bidirectional committed/acknowledged/applied cursors, active effect identity, status and revision.
- `runtime_control_messages` stores direction, sequence, exact message/idempotency identity, fingerprint, envelope, effect status and timestamps.
- Reuse `runtime_instances.last_command_sequence` and `last_event_sequence` as aggregate high-water marks, but never as the replay source.
- All cursor mutation locks and validates the current WorkerSession/Runtime/channel authority in one PostgreSQL transaction.

**Verify:**

```bash
go test ./internal/workerruntime/application -run 'RuntimeControlJournal' -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'RuntimeControlJournal' -count=1
```

## Task 3: Make gRPC a durable resume transport

**Files:**

- Modify: `internal/workerruntime/adapter/controlgrpc/rpc.go`
- Modify: `internal/workerruntime/adapter/controlgrpc/auth.go`
- Modify: `internal/workerruntime/adapter/controlgrpc/hub.go`
- Modify: `internal/workerruntime/adapter/controlgrpc/server.go`
- Modify: `internal/workerruntime/adapter/controlgrpc/client.go`
- Modify: `internal/workerruntime/adapter/controlgrpc/control_test.go`

**RED tests:**

- A new Hub using the same journal resumes at non-zero cursors.
- A journaled-but-unacknowledged command replays with its original sequence and bytes.
- An event resent after response loss is applied once.
- Newer stream attempt fences the old stream; stale owner frames cannot mutate the journal.
- Authentication, generation, epoch, cursor digest and active-effect conflicts fail closed.

**GREEN implementation:**

- First client frame must be `ResumeHello`; first server frame must be `ResumeAck`.
- Replay traffic remains frozen from new work until client `ResumeReady` and server `ResumeComplete` prove both journal views converged.
- `Hub.Send` reserves in the journal before enqueue.
- `acceptEvent` commits the inbox journal before cumulative receipt ACK; business application advances a separate applied cursor.
- Connection-local queues transport durable records only; they do not own sequence truth.

**Verify:**

```bash
go test ./internal/workerruntime/adapter/controlgrpc -count=1
go test -race ./internal/workerruntime/adapter/controlgrpc -count=1
```

## Task 4: Keep Shim execution alive across transient disconnects

**Files:**

- Modify: `internal/workerruntime/adapter/shimruntime/runtime.go`
- Modify: `internal/workerruntime/adapter/shimruntime/runtime_test.go`
- Add helper files only if they deepen the control supervisor/execution-loop seam

**RED tests:**

- EOF/Unavailable during an active fake-Agent prompt reconnects and calls `Prompt` exactly once.
- Terminal/update events produced while disconnected are replayed once from a bounded spool.
- Reconnect does not reinitialize LiteLLM, ACP or AgentSession and does not resend `runtime.ready`.
- Unauthenticated, permission denied, schema conflict or stale identity stops without reconnect.
- Spool overflow becomes explicit continuity failure rather than dropping events.

**GREEN implementation:**

- Move ACP/LiteLLM/AgentSession lifetime outside `controlSupervisor`.
- Keep stable process nonce, stream-attempt counter, cursors and active-effect state for the Shim process lifetime.
- Retry only transient transport failures with bounded exponential backoff and jitter.
- Freeze new commands until resume replay converges; immediately resume heartbeat afterwards.

**Verify:**

```bash
go test ./internal/workerruntime/adapter/shimruntime -count=1
go test -race ./internal/workerruntime/adapter/shimruntime -count=1
```

## Task 5: Rehydrate healthy live Runtime ownership on Backend startup

**Files:**

- Add: `internal/localproduct/runtime_control_restore.go`
- Add: `internal/localproduct/runtime_control_restore_test.go`
- Modify: `internal/localproduct/runtime_environment.go`
- Modify: `internal/localproduct/runtime_runner.go`
- Modify: `internal/localproduct/app_integration_test.go`
- Modify Runtime secret materializer only if an exact read-only bootstrap reopen seam is missing

**RED tests:**

- Startup inspects an exact running container, reopens only its exact server-owned bootstrap, and awaits resume without an incident.
- Exact missing/exited container opens the existing recovery path immediately.
- A running container that does not resume crosses suspect/unhealthy scheduling and opens exactly one incident.
- Backend restart during active Turn keeps the same container, Runtime id/generation/epoch, Run and Turn.
- Start command response-loss converges to `consumed` without another `agent_turn.start`.

**GREEN implementation:**

- Restore credentials, storage, event subscriptions and `liveRuntime` state before dispatch/polling.
- Replace unconditional `openUnownedRuntimeRecoveries` with inspect-then-await-resume.
- A stream close records disconnect/reconnect evidence; it is not by itself a recovery trigger.
- Existing hard container/ACP evidence and the persisted heartbeat 45/90-second
  boundary remain authoritative. `ResumeComplete` is only a transport ownership
  barrier: it does not reset `last_received_at`. A resume at T+89 has only the
  original one-second remainder to deliver a valid sequenced heartbeat. At T+90
  the heartbeat/recovery revision CAS has one winner; a winning recovery fences
  the channel before any late heartbeat can revive the Runtime.

**Verify:**

```bash
go test ./internal/localproduct -run 'Runtime(ControlRestore|Reconnect|BackendRestart)' -count=1
go test -tags=integration ./internal/localproduct -run 'Runtime(ControlRestore|Reconnect|BackendRestart)' -count=1
```

Before production-path acceptance, close the terminal business-settlement gap.
Receipt/application cursors alone cannot recover a crash after the terminal
event is applied but before a queued continuation is committed, and
re-finalizing evidence with newly generated Artifact ids or timestamps is not
an idempotency proof.

### Task 5.5: Persist terminal Turn settlement and resume every crash point

**Files:**

- Add: `migrations/000015_worker_turn_settlement.sql`
- Add: `internal/workerruntime/application/turn_settlement.go`
- Add: `internal/workerruntime/adapter/postgres/turn_settlement.go`
- Add: `internal/localproduct/runtime_settlement.go`
- Modify: Runtime control journal, resident-stage transaction, evidence
  finalizer, queued-Turn continuation and startup wiring

**RED tests:**

- Accepting an exact `agent_turn.terminal` creates a settlement header in the
  same PostgreSQL transaction before the runner can observe it.
- CompleteStage commit/response loss advances the domain exactly once and
  returns the stored post-revisions.
- Evidence preparation retries use the same frozen trace range, patch digest,
  Artifact ids and recorded time; publication response loss creates no second
  metadata or Outbox result.
- Task transition response loss reuses one frozen action and idempotency
  digest.
- Terminal event effect, inbox cursor and settlement phase advance in one
  transaction.
- A crash after terminal Apply but before queued continuation resumes after
  Backend restart and creates exactly one next Run/Turn/deadline/Outbox.
- Continuation is rejected before terminal Apply, ordinary dispatch cannot
  overtake an incomplete settlement, and generation greater than one is not
  silently rewritten to generation one.

**GREEN implementation:**

- `worker_turn_settlements` owns the exact terminal event identity/digest,
  immutable terminal decision, trace range and evidence inputs, post-domain
  revisions, frozen Task action, optional FIFO queue head, phase/revision and a
  short fenced lease.
- Phases progress monotonically:
  `accepted -> evidence_prepared -> domain_committed ->
  evidence_published -> task_settled -> terminal_event_applied ->
  continuation_settled -> completed`; inapplicable stages are explicitly
  skipped rather than inferred.
- Accept and Apply update the settlement in their existing journal
  transactions. CompleteStage updates its phase and stored result in the same
  transaction as the domain transition.
- Evidence finalization accepts caller-frozen ids and timestamps. Recovery
  proves existing publication from its exact manifest/idempotency record
  instead of regenerating identity.
- Startup claims unfinished settlements before enabling ordinary dispatch.
  Queued continuation is a separate reconciler and is allowed only after
  `terminal_event_applied`.

**Verify:**

```bash
go test ./internal/workerruntime/application -run 'TurnSettlement' -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TurnSettlement' -count=1
go test ./internal/localproduct -run 'TurnSettlement|TerminalSettlement' -count=1
go test -race ./internal/localproduct ./internal/workerruntime/adapter/controlgrpc -count=1
```

## Task 6: Production-path fault acceptance and enablement evidence

**Files:**

- Modify: `scripts/run-runtime-recovery-acceptance`
- Modify/add: `test/acceptance/realprovider/`
- Modify: `docs/architecture/m1-worker-runtime-recovery-manual-acceptance.md`
- Modify: `openspec/changes/single-runtime-recovery/tasks.md`

**Acceptance matrix:**

- Idle resident Runtime across Backend restart.
- Active real prompt across Backend restart, prompt invocation count exactly one.
- Command-send and event-ACK response-loss windows.
- Duplicate resume, stale owner/attempt, cursor conflict and invalid credential.
- Missing container immediate recovery versus running-container reconnect grace.
- Reconnect grace expiry, failed fencing/replacement and no crash loop.

**Verify:**

```bash
make runtime-recovery-acceptance
make verify
npx --yes @fission-ai/openspec@1.6.0 validate single-runtime-recovery --strict
```

Keep Compose recovery default disabled until OpenSpec tasks 2.3, 5.7, 6.4, 8.6, 8.7 and section 9 are all proven.
