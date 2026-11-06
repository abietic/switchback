## 1. Recovery Contracts And Persistence

- [x] 1.1 Define RuntimeRecoveryIncident identity, trigger, phase, revision, failed/replacement generation, attempt and terminal outcome contracts
- [x] 1.2 Add PostgreSQL migrations and repositories for incidents, heartbeat observations, replacement intents, mount fencing and idempotency results
- [x] 1.3 Add domain state-machine tests for open, fencing, provisioning, reattaching, verifying, recovered and exhausted transitions
- [x] 1.4 Generalize generation-1-only validators, RuntimeInstance identity and Runtime secrets to monotonic generation `N`
- [x] 1.5 Define owner-sequenced recovery IntegrationEvents and projection payloads with safe evidence references
- [x] 1.6 Add duplicate trigger, conflicting idempotency, concurrent incident and Backend restart persistence tests

## 2. Liveness Detection

- [x] 2.1 Persist authenticated RuntimeHeartbeat receive time and monotonic sequence independently from model/Tool output
- [x] 2.2 Implement 45-second suspect and 90-second unhealthy evaluation through durable scheduled/reconciliation commands
- [ ] 2.3 Open incidents immediately for confirmed container/process exit, missing container or authenticated ACP EOF; treat WorkerControl transport close as bounded reconnect evidence rather than process failure
  - Current: the close observer/trigger path has been removed; transport close only disconnects the durable journal and `worker_control_closed` remains decode/reconcile compatibility for historical incidents.
  - Remaining: complete production/fixed-image acceptance for each eligible hard signal and prove a transport-only disconnect/reconnect creates no incident.
  - Disabled admission clarification (2026-10-11): exact server-owned exited/missing inspection commits directly to zero-attempt exhausted Worker failure; no automatic replacement, prompt replay or fabricated delivery evidence. Worker transaction, exact waiter handoff and existing cleanup rediscovery have focused PostgreSQL/fake-Docker coverage. Task-owner consumption and production/fixed-image acceptance remain separate gates.
- [x] 2.4 Keep `active_quiet`, provider latency and missing token output nonterminal and outside recovery
- [x] 2.5 Add stale heartbeat, duplicate sequence, clock skew, Backend pause and hard-exit tests

## 3. Incident Ownership And Transaction Boundaries

- [x] 3.1 Implement idempotent incident creation that marks WorkerSession recovering and revokes new dispatch without changing Task/Run identity
- [x] 3.2 Persist phase intent and Outbox before every Docker, filesystem, secret or ACP side effect
- [x] 3.3 Reconcile each external phase by exact server-owned resource identity after response loss or Backend restart
- [x] 3.4 Enforce one replacement attempt per continuous incident and create a new incident only after prior stable recovery closed
- [x] 3.5 Add transaction rollback and outbox failure tests proving no partial replacement authority is published

## 4. Old-Generation Fencing And Replacement

- [x] 4.1 Revoke old dispatch permits, ChannelEpoch, WorkerControl credential and generation-scoped SecretLease
- [x] 4.2 Stop or isolate the old container and prove exclusive WorkingCopy/Agent-state/Trace-spool write authority
- [x] 4.3 Exhaust recovery without provisioning when cleanup or mount fencing cannot be proven
- [x] 4.4 Transactionally reserve generation `N+1`, new RuntimeInstance/ChannelEpoch and fresh generation-scoped secrets
- [x] 4.5 Provision the replacement with the same WorkerSession storage and server-owned runtime profiles
- [x] 4.6 Add duplicate provisioning, stale generation, failed cleanup, split-brain mount and secret-rotation tests

## 5. ACP And AgentSession Reattachment

- [x] 5.1 Initialize the pinned ACP stack and verify protocol version plus required resume/load capabilities
- [x] 5.2 Prefer `session/resume` for the exact AgentSession and fall back to capability-declared `session/load` only before new work
- [x] 5.3 Tag load-replayed updates with replacement generation and deduplicate DisplayCheckpoint/Trace source identities
- [x] 5.4 Reconstruct exact cwd, additional directories, model profile, MCP configuration and control/Trace cursors from durable state
- [x] 5.5 Classify active AgentTurn continuity without automatically resending its prompt or Tool invocation
- [x] 5.6 Enter `recovery_continuity_unproven` waiting-user state when exact turn/effect continuity cannot be established
- [ ] 5.7 Add fixed-image resume, load, restart-during-turn, unknown-update and no-prompt-replay conformance tests
  - Remaining: fixed-image fault injection while an AgentTurn is active; resume/load, unknown-update tolerance and no-prompt-replay coverage are implemented.

## 6. Health Verification And Incident Closure

- [x] 6.1 Require authenticated WorkerControl, independent heartbeat, LiteLLM/ACP health, WorkingCopy identity and cursor convergence
- [x] 6.2 Keep the incident verifying until durable post-attachment progress or a server-owned stability window proves health
- [x] 6.3 Preserve the original AgentTurn `started_at`, `deadline_at` and local watchdog identity through every recovery phase
- [ ] 6.4 Let AgentTurn deadline, WorkerSession expiry and security termination preempt and fence recovery
  - Remaining: add an authenticated system-owned security-termination command with monotonic authority revision and two-phase fencing/cleanup. Deadline and absolute-expiry preemption, including exact cleanup when provision returns after the lifecycle owner has won the incident revision, are implemented and tested; the user Gateway is not a valid security-owner seam.
- [x] 6.5 Exhaust the incident when the replacement fails before stability evidence and prevent a second automatic attempt
- [x] 6.6 Add stability-window, deadline-during-recovery, expiry-during-recovery and replacement-regression tests

## 7. Task, Session And User Projection

- [x] 7.1 Keep a bound nonterminal Task running with the same WorkerSession and WorkerRun while recovery is eligible and active
- [x] 7.2 Project `recovering_runtime`, failed/replacement generations, attempt and evidence time into WorkerDetail
- [x] 7.3 Move WorkerSession/WorkerRun and a nonterminal bound Task to `waiting_user:runtime_recovery_failed` when recovery exhausts
- [x] 7.4 Preserve delivered and terminal Task state while retaining recovery evidence in WorkerSession lineage
- [x] 7.5 Fence late old-generation events, Display mutations and Artifact finalization while retaining allowed Trace-only evidence
- [x] 7.6 Add projection replay, duplicate event, late event, stale action and reconnect convergence tests

## 8. Eligibility, Evidence And Acceptance

- [x] 8.1 Exclude user cancellation, idle/absolute expiry, authorization, policy, schema, protocol and known resource-exhaustion outcomes from auto recovery
- [x] 8.2 Route non-idempotent or unknown external-effect failures to reconciliation/waiting-user without retrying the operation
- [x] 8.3 Preserve proven TraceManifest and ArtifactRef records with exact generation provenance across recovery
- [x] 8.4 Update requirements, architecture, operations, failure matrix, manual acceptance and rollback documentation
- [x] 8.5 Run Go unit/race/integration, Docker, ACP, WorkerControl, projection, secret, deadline and failure-acceptance suites plus `make verify`
- [ ] 8.6 Execute real fault acceptance for idle loss, active loss, Backend restart, duplicate triggers, failed fencing, failed replacement and stale events
  - Remaining: Docker/fixed-image variants through the production recovery path for idle/active loss, Backend restart, duplicate triggers, failed fencing/replacement and stale events. Deterministic PostgreSQL coverage plus one `idle_between_turns_manual_replacement` fixed-image case exist, but that driver-managed case records `production_recovery_path=false` and does not satisfy this checkbox.
- [ ] 8.7 Validate the OpenSpec change strictly and record exact incident, generation, continuity, deadline, evidence and no-crash-loop proof before enabling recovery
  - Remaining: keep the Compose default disabled until 2.3, 5.7, 6.4, 8.6 and section 9 are complete and the full fixed-image fault matrix records Backend reconnect, active-turn continuity, lifecycle preemption and no-crash-loop evidence.

## 9. Healthy WorkerControl Resume

- [ ] 9.1 Persist exact Runtime control authority, token digest, owner fence, process/stream identity and bidirectional committed/acknowledged/applied cursors
- [ ] 9.2 Persist every command before send and every event before ACK with exact sequence, message/idempotency identity, full envelope digest/bytes and separate receipt/application status
- [ ] 9.3 Add explicit `ResumeHello`/`ResumeAck` plus `ResumeReady`/`ResumeComplete` replay-barrier frames, versioned cursor hash-chain vectors, replay planning and stale-owner/stream fencing without resetting generation or ChannelEpoch
  - Current: `ResumeComplete` atomically arms an immutable first-heartbeat watchdog when no real observation exists; reconnect reuses its original anchor, and a real first heartbeat atomically satisfies it. T+45/T+90 evaluation repairs every strictly decoded, pre-boundary heartbeat accepted in the durable journal without advancing the ordered applied cursor: the first freezes watchdog proof and the latest advances ordinary liveness; later event replay is duplicate-safe. Heartbeat/liveness writers now share the AgentTurn advisory-before-graph lock order with terminal/deadline authority, so even an earlier uncommitted owner wins; outcome-owned T+90 heartbeat remains audit-only instead of rolling back/closing control, and `terminal_observed` can settle an exact suspect Runtime without rewriting its status or revision.
  - Remaining: broader section 9 production/fixed-image conformance evidence.
- [ ] 9.4 Keep Shim ACP, LiteLLM, active prompt and bounded event spool alive across transient control disconnects; fail closed on authentication, schema or identity errors
- [ ] 9.5 Add exact-envelope `agent_turn.start` received/intent/active/terminal effect acknowledgement, advance the original Turn to consumed without prompt replay and permit a higher start sequence after terminal
- [ ] 9.6 Rehydrate eligible live Runtime credentials, storage, event subscription and journal cursors at Backend startup before enabling dispatch
  - Current: startup drains due AgentTurn deadline and first-heartbeat watchdog commands before readiness, and restores liveness from heartbeat receive time or the original watchdog anchor rather than restart time. A future `force_after` owned by Backend-deadline settlement no longer blocks the global restore group: startup validates the exact environment/container/credential identity, defers that candidate immediately, and leaves durable deadline processing plus post-ready adoption to converge it.
- [ ] 9.7 Inspect the exact container and wait through the persisted reconnect/liveness boundary before opening one recovery incident
  - Current: WorkerControl close is no longer an eligible trigger; the first-heartbeat path persists T+45/T+90 boundaries and prevents reconnect from resetting them.
  - Remaining: production/fixed-image proof of exact-container reconnect and boundary races.
- [ ] 9.8 Add PostgreSQL, cross-Hub, Shim fake-Agent and local-product restart tests for response-loss windows, cursor conflicts, active Turn exactly-once and stale owner fencing
- [ ] 9.9 Add fixed-image Backend-restart acceptance proving the same container, Runtime generation/epoch, Run, Turn and single prompt invocation converge
- [ ] 9.10 Persist and reconcile terminal Turn settlement across every Backend crash point so evidence publication, Task settlement, terminal event application and queued continuation are each exactly-once and ordinary dispatch cannot overtake an unfinished settlement
