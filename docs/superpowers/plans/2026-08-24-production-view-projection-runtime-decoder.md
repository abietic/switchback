# Production ViewProjection Runtime Decoder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `subagent-driven-development` task-by-task. Use TDD for every behavior change and
> run the task's focused gate before committing.

**Goal:** Make every production Home, Task index, Assistant, Worker and Artifact
Snapshot/Delta/Control cross one closed runtime decoder and one bounded recovery
state machine before durable View data or command authority mutates a browser
replica.

**Architecture:** `web/src/projections` owns the five product contracts, private
decode brands and closed factory. `web/src/state` owns local command bindings,
replica commit decisions, leases and the cause-tagged Snapshot coordinator. Routes
can select only a typed product factory; they cannot provide a free generic, wire
identity or parser. One final cutover deletes every legacy raw-envelope and
ambiguous-refresh seam.

**Tech Stack:** TypeScript 7, React 19, Vitest 4, Playwright 1.61, Vite 8, Go 1.26
verification gates and PostgreSQL integration tests.

**Design authority:**
`docs/superpowers/specs/2026-08-24-production-view-projection-runtime-decoder-design.md`
at commit `93edec266ab26d0b5900bed7b0b3f5866ef8a753`.

## Global Constraints

- Do not modify Go Projection wire structs, `api/schema/v1`, migrations or
  persisted Home/Worker upgrade bytes/digests.
- Preserve the existing dirty user paths reported by `git status`; stage only the
  files named by the current task.
- Before each task, require `git diff --cached --quiet`; an unexpected staged
  change is a blocker. After exact staging, inspect the cached name/status and run
  `git diff --cached --check` before committing so user changes cannot ride along.
- Classify identity before schema. Only a matching canonical future schema enters
  `client_upgrade_required`.
- All protocol/revision numbers are safe integers; Delta result version is exactly
  `base + 1`.
- A Delta is duplicate only in the same epoch with cursor `<=` trusted cursor.
  Duplicate/discontinuous payload is never parsed or merged.
- Do not add a generic reducer registry, JSON Schema system, numeric wire v2 or
  TransientDisplayDelta decoder.
- Gateway authorization stays authoritative; browser validation is fail-closed
  defense in depth.
- Reject the preserved sparse Worker contract as `legacy_worker_contract`; never
  fabricate current fields or rewrite its history.
- Intermediate additive APIs must be deleted in Task 10. The task is incomplete
  while a raw/default parser path remains reachable.
- Every task ends with `git diff --check` and a focused test. Test claims require
  current command output.

---

### Task 1: Build the shared projection runtime trust boundary

**Files:**

- Create: `web/src/projections/viewRuntime.ts`
- Create: `web/src/projections/viewRuntime.test.ts`
- Create: `web/src/projections/viewRuntime.typecheck.ts`

**Produces:** private branded bound decoder, decoded Snapshot/Delta/Control,
`TrustedViewProjectionState<T>`, immutable request context, closed errors and a
projections-internal `ViewContract<T>` used only by closed factories.

- [ ] **Step 1: Write the failing identity/schema/number matrix**

With a minimal test-only View contract, cover non-record/array/null; missing or
foreign identity; matching canonical v2; arbitrarily large canonical schema text;
v0/v01/non-decimal/cross-family schema; fractional/negative/unsafe positions;
Delta result not exact `base + 1`; `client_command_id` without canonical
`command_id`; and safe errors that never include raw payload.

- [ ] **Step 2: Prove red**

```bash
npm --prefix web test -- src/projections/viewRuntime.test.ts
```

Expected: fail because the runtime API does not exist.

- [ ] **Step 3: Implement identity-first common decoding**

Implement strict record/string/timestamp/safe-integer validators and schema text
comparison without JavaScript number conversion. Accept additive unknown object
fields. Export no brand constructor.

- [ ] **Step 4: Add receipt, Delta-decision and SSE red tests**

Cover:

- absent query => receipts absent;
- non-empty immutable requested-id set => receipts absent or subset only;
- duplicate receipt normalization and conflicting/unrequested/bounded rejection;
- decoded Snapshot freezes the exact normalized request context;
- same-epoch duplicate returns before payload/authority decode;
- cross-epoch/cursor-gap/base mismatch returns branded closed discontinuity with
  trusted/incoming positions and safe key;
- exact-next Delta alone builds one effective state;
- Delta event name/lastEventId and Control listener/current-cursor binding;
- only canonical replayable invalid Delta exposes branded integrity failure;
- canonical ActionRef/SourceRef/RelatedRef records, positive revisions and exact
  encoded href; malformed/foreign href and non-canonical identities fail;
- `event_type` is checked against the closed allowlist supplied by the bound product
  contract, never the broad Go writer taxonomy or an arbitrary reducer name.

```bash
npm --prefix web test -- src/projections/viewRuntime.test.ts
```

Expected red on the newly added receipt/decision/SSE cases before Step 5.

- [ ] **Step 5: Implement Snapshot/Delta/Control decisions**

Construct effective data/actions/refs exactly once. `projections` imports no React,
state/controller or feature module.

- [ ] **Step 6: Prove negative types**

In `viewRuntime.typecheck.ts`, use `@ts-expect-error` for a forged decoded object,
integrity/discontinuity token, mismatched data type and arbitrary decoder callback.

```bash
npm --prefix web run build
```

- [ ] **Step 7: Run green and commit**

```bash
set -e
npm --prefix web test -- src/projections/viewRuntime.test.ts
npm --prefix web run build
git diff --check
git add -- web/src/projections/viewRuntime.ts \
  web/src/projections/viewRuntime.test.ts \
  web/src/projections/viewRuntime.typecheck.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "feat: add production projection runtime decoder"
```

---

### Task 2: Add Home, Task-index and Assistant product contracts

**Files:**

- Create: `web/src/projections/decoders/home.ts`
- Create: `web/src/projections/decoders/home.test.ts`
- Create: `web/src/projections/decoders/taskIndex.ts`
- Create: `web/src/projections/decoders/taskIndex.test.ts`
- Create: `web/src/projections/decoders/assistant.ts`
- Create: `web/src/projections/decoders/assistant.test.ts`
- Modify: `web/src/api/homeContracts.ts`
- Modify: `web/src/api/homeContracts.test.ts`
- Modify: `web/src/api/assistantContracts.ts`
- Modify: `web/src/api/assistantContracts.test.ts`

- [ ] **Step 1: Write Home red tests and run them**

Use one real Go-materializer-shaped Snapshot and one effective Delta fixture.
Assert exact `home_dashboard/index`, strict lists/capacity/timestamps, empty
actions, refs backed one-to-one by decoded identities, and only
`view.entity_upserted.v1`.

```bash
npm --prefix web test -- src/projections/decoders/home.test.ts
```

Expected: fail because the Home product contract does not exist.

- [ ] **Step 2: Extract pure Home data helpers, implement and prove green**

Preserve existing types. The old full-envelope export may temporarily delegate to
the pure helper solely for old-hook compilation; mark it for Task 10 deletion.

```bash
npm --prefix web test -- \
  src/projections/decoders/home.test.ts src/api/homeContracts.test.ts
```

- [ ] **Step 3: Write Task-index red tests, run red, implement and run green**

Use real Go-shaped Snapshot/Delta fixtures. Assert exact `task_summary/index`, Task
status/tone/capacity, optional Worker, empty actions/refs and only
`view.entity_upserted.v1`. Reject individual Task shape.

```bash
npm --prefix web test -- src/projections/decoders/taskIndex.test.ts
```

Expected red before implementation; rerun the same command green afterward.

- [ ] **Step 4: Write Assistant red tests and run them**

Use real Go-materializer-shaped Snapshot/Delta fixtures, preserving current/legacy
profile reads and only `view.entity_upserted.v1`. Validate exact action authority:

- send message => active conversation id/revision;
- confirm task => current ready draft id/revision;
- checkpoint => active checkpoint id/revision;
- retry => failed retryable Run id/revision;
- Task/Worker refs => decoded handoff identities.

Reject unknown action, metadata, target, revision or ref.

```bash
npm --prefix web test -- src/projections/decoders/assistant.test.ts
```

Expected: fail before the Assistant product contract is implemented.

- [ ] **Step 5: Extract pure Assistant helper, implement and prove green**

The temporary full-envelope export may delegate only until Task 10.

```bash
npm --prefix web test -- \
  src/projections/decoders/assistant.test.ts src/api/assistantContracts.test.ts
```

- [ ] **Step 6: Run combined green and commit**

```bash
set -e
npm --prefix web test -- \
  src/projections/decoders/home.test.ts \
  src/projections/decoders/taskIndex.test.ts \
  src/projections/decoders/assistant.test.ts \
  src/api/homeContracts.test.ts \
  src/api/assistantContracts.test.ts
npm --prefix web run build
git diff --check
git add -- web/src/projections/decoders/home.ts \
  web/src/projections/decoders/home.test.ts \
  web/src/projections/decoders/taskIndex.ts \
  web/src/projections/decoders/taskIndex.test.ts \
  web/src/projections/decoders/assistant.ts \
  web/src/projections/decoders/assistant.test.ts \
  web/src/api/homeContracts.ts web/src/api/homeContracts.test.ts \
  web/src/api/assistantContracts.ts web/src/api/assistantContracts.test.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "feat: validate core production view contracts"
```

---

### Task 3: Add strict WorkerDetail data and command authority

**Files:**

- Create: `web/src/projections/decoders/worker.ts`
- Create: `web/src/projections/decoders/worker.test.ts`
- Create: `web/src/projections/testdata/workerFixtures.ts`

**Read-only evidence:** `internal/eventingprojection/adapter/postgres/materializer.go`,
`internal/eventingprojection/adapter/postgres/worker_detail_contract_upgrade_integration_test.go`,
Web contracts and Worker command API.

- [ ] **Step 1: Write canonical Worker primitive tests**

Use one real Go-materializer-shaped Snapshot and one exact-next effective Delta.
Cover exact session, revisions/generation, closed statuses, nullable pre-Run fields,
timeline groups, artifacts, approvals, network/recovery profiles, waiting-user and
empty pre-Run `agent_profile`. Accept `view.entity_upserted.v1` and
`view.stream_checkpointed.v1`; reject every other broad-writer event name.

- [ ] **Step 2: Write the exact action/ref authority matrix**

Cover follow-up, queue, interrupt, task continue/accept/rework, approval and
Artifact refs with the design's exact target/revision/status/metadata conditions.
Mutate one authority fact per negative row.

- [ ] **Step 3: Add the exact sparse legacy rejection fixture**

Transcribe the preserved post-upgrade grouped timeline + legacy
`worker.send_prompt` + legacy Task ref. Assert
`invalid_contract/legacy_worker_contract`, not upgrade, with no decoded authority.

- [ ] **Step 4: Run the complete Worker test and prove red**

```bash
npm --prefix web test -- src/projections/decoders/worker.test.ts
```

Expected: fail because the Worker product contract is not implemented.

- [ ] **Step 5: Implement minimally and prove green**

```bash
set -e
npm --prefix web test -- src/projections/decoders/worker.test.ts
npm --prefix web run build
git diff --check
```

- [ ] **Step 6: Commit exact Task 3 files**

```bash
set -e
git add -- web/src/projections/decoders/worker.ts \
  web/src/projections/decoders/worker.test.ts \
  web/src/projections/testdata/workerFixtures.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "feat: validate WorkerDetail projection authority"
```

---

### Task 4: Add Artifact validation and the closed five-View factory

**Files:**

- Create: `web/src/projections/decoders/artifact.ts`
- Create: `web/src/projections/decoders/artifact.test.ts`
- Create: `web/src/projections/artifactAuthority.ts`
- Create: `web/src/projections/productionViewRegistry.ts`
- Create: `web/src/projections/productionViewRegistry.test.ts`
- Create: `web/src/projections/productionViewRegistry.typecheck.ts`
- Modify: `web/src/features/artifact/ArtifactDetailPage.tsx`
- Modify: `web/src/features/artifact/ArtifactDetailPage.test.tsx`
- Modify: `web/src/features/worker/workbench/ArtifactWorkbench.tsx`
- Modify: `web/src/features/worker/workbench/ArtifactWorkbench.test.tsx`
- Modify: `web/src/features/artifact/ArtifactSnapshotParser.ts` (temporary
  compatibility only; Task 10 deletes it)
- Delete: `web/src/features/artifact/ArtifactAuthority.ts`

- [ ] **Step 1: Write the Artifact decoder red matrix**

Use a current, real Go-materializer-shaped Artifact Snapshot and an exact-next
effective Delta. Assert exact bound id; strict manifest/integrity/media/checksum,
size and download fields; exact download target/revision; and empty related refs.
Accept only `view.entity_upserted.v1` and `view.artifact_ready.v1`; reject
`view.stream_checkpointed.v1` and every other broad-writer event. The historical
Worker navigation hint is an explicit rejection fixture.

- [ ] **Step 2: Prove Artifact red, implement minimally, and prove green**

```bash
npm --prefix web test -- src/projections/decoders/artifact.test.ts
```

Expected red because the bound Artifact contract does not exist. Implement the
decoder and move checksum/download authority into `projections`; update the page,
Workbench and temporary parser imports, make the temporary parser reject non-empty
Artifact refs, and delete the old authority file rather than forwarding it.

```bash
set -e
npm --prefix web test -- \
  src/projections/decoders/artifact.test.ts \
  src/features/artifact/ArtifactDetailPage.test.tsx \
  src/features/worker/workbench/ArtifactWorkbench.test.tsx
```

- [ ] **Step 3: Write the exhaustive five-kind registry red matrix**

Assert that the only factories are `home()`, `taskIndex()`, `assistant(id)`,
`worker(id)` and `artifact(id)`, each with explicit `runtimeVersion`, semantic
`contractKey` and exact bound identity. For all five factories, decode one real
Go-shaped Snapshot and exact-next Delta. Table-test the closed event cross-product:
all five admit `view.entity_upserted.v1`; only Worker additionally admits
`view.stream_checkpointed.v1`; only Artifact additionally admits
`view.artifact_ready.v1`; every other View/event pair fails closed.

In `productionViewRegistry.typecheck.ts`, prove that fake bound objects, wrong
kind/data, arbitrary View selection and cross-kind decoder use do not compile.

- [ ] **Step 4: Prove registry red, implement, and run green**

```bash
npm --prefix web test -- src/projections/productionViewRegistry.test.ts
```

Expected red because the closed registry does not exist. Implement it without a
`default`, unknown fallback or caller-provided reducer/parser, then run:

```bash
set -e
npm --prefix web test -- \
  src/projections/decoders/artifact.test.ts \
  src/projections/productionViewRegistry.test.ts \
  src/features/artifact/ArtifactDetailPage.test.tsx \
  src/features/worker/workbench/ArtifactWorkbench.test.tsx
npm --prefix web run build
git diff --check
```

- [ ] **Step 5: Commit only Task 4 files**

```bash
set -e
git add -- web/src/projections/decoders/artifact.ts \
  web/src/projections/decoders/artifact.test.ts \
  web/src/projections/artifactAuthority.ts \
  web/src/projections/productionViewRegistry.ts \
  web/src/projections/productionViewRegistry.test.ts \
  web/src/projections/productionViewRegistry.typecheck.ts \
  web/src/features/artifact/ArtifactDetailPage.tsx \
  web/src/features/artifact/ArtifactDetailPage.test.tsx \
  web/src/features/worker/workbench/ArtifactWorkbench.tsx \
  web/src/features/worker/workbench/ArtifactWorkbench.test.tsx \
  web/src/features/artifact/ArtifactSnapshotParser.ts \
  web/src/features/artifact/ArtifactAuthority.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "feat: close production view decoder registry"
```

---

### Task 5: Make decoded replica commit and command reconciliation state-owned

**Files:**

- Modify: `web/src/state/clientViewReplica.ts`
- Modify: `web/src/state/clientViewReplica.test.ts`
- Create: `web/src/state/clientViewReplica.typecheck.ts`
- Create: `web/src/state/productionViewReplicaRegistry.test.ts`

- [ ] **Step 1: Write lease/contract-key tests and prove red**

Cover same-key reuse; replacement only after final old lease release; concurrent
different-key acquire failing closed; and sequential replacement clearing only the
exact replica.

```bash
npm --prefix web test -- src/state/productionViewReplicaRegistry.test.ts
```

Expected red because bound-view leases do not exist. Implement only lease and
contract-replacement behavior, then rerun the same command green.

- [ ] **Step 2: Write branded commit/access-clear tests and prove red**

Require decoded Snapshot/Delta/Control entry points; identity and exact `base + 1`
defense in depth; immutable decode context; one effective-state commit with no
second optional-payload merge; `stale_decode_context`; and access clear of
data/actions/refs/transient/pending identities.

```bash
npm --prefix web test -- src/state/clientViewReplica.test.ts
```

Expected red on the new branded API. Implement only those decisions and rerun
green. Raw commit methods may remain as temporary old-controller shims, gain no
new caller, and are deleted in Task 10.

- [ ] **Step 3: Write the replica-owned reconciliation red matrix**

Cover Delta identity, frozen Snapshot receipt and asynchronous submission-response
conflicts; first cause => branded `command_reconciliation_required`; unresolved
second cause => `command_reconciliation_blocked`; zero partial mutation;
same-position receipt-absent Snapshot retention; unrelated valid Delta retention;
implicated settlement/removal; Delta-origin cursor coverage/epoch replacement;
Manual Retry clearing only the guard while preserving pending bindings; and access
clear/contract replacement discarding local authority.

In `clientViewReplica.typecheck.ts`, prove a structural fake reconciliation cause
cannot be passed to controller/coordinator-facing APIs.

```bash
npm --prefix web test -- src/state/clientViewReplica.test.ts
```

Expected red on first/blocked/reset behavior.

- [ ] **Step 4: Implement the private basis and run green**

Retain the implicated binding basis privately and emit only safe branded signals;
never encode a local conflict as free-form `resync_required`.

```bash
set -e
npm --prefix web test -- \
  src/state/clientViewReplica.test.ts \
  src/state/productionViewReplicaRegistry.test.ts
npm --prefix web run build
git diff --check
```

- [ ] **Step 5: Commit only Task 5 files**

```bash
set -e
git add -- web/src/state/clientViewReplica.ts \
  web/src/state/clientViewReplica.test.ts \
  web/src/state/clientViewReplica.typecheck.ts \
  web/src/state/productionViewReplicaRegistry.test.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "refactor: make projection replica commits authoritative"
```

---

### Task 6: Add the effect-free bound Snapshot transport and Artifact preview

**Files:**

- Create: `web/src/projections/snapshotTransport.ts`
- Create: `web/src/projections/snapshotTransport.test.ts`
- Create: `web/src/projections/artifactPreview.ts`
- Create: `web/src/projections/artifactPreview.test.ts`
- Modify: `web/src/features/worker/workbench/ArtifactWorkbench.tsx`
- Modify: `web/src/features/worker/workbench/ArtifactWorkbench.test.tsx`

- [ ] **Step 1: Write the closed transport outcome matrix and prove red**

Classify before body decode: 2xx decode; 401/403/404 `access_clear`; network,
408/425/429/5xx `retryable_transport`; every other final non-2xx
`terminal_http`. Assert that `Retry-After` is ignored and the transport performs no
replica, lifecycle, retry, timer or SSE effect.

```bash
npm --prefix web test -- src/projections/snapshotTransport.test.ts
```

Expected red because the bound transport does not exist.

- [ ] **Step 2: Implement the transport and prove green**

Accept only a bound production View and immutable requested client-command ids;
return a typed HTTP/decode outcome. Backoff is not part of this module.

```bash
npm --prefix web test -- src/projections/snapshotTransport.test.ts
```

- [ ] **Step 3: Write Artifact preview isolation tests and prove red**

Require a one-shot helper that accepts only a bound Artifact View and returns only
decoder-proved Artifact data. Prove it cannot mutate a replica, connect SSE, arm a
timer or touch any continuity/integrity/command guard.

```bash
npm --prefix web test -- \
  src/projections/artifactPreview.test.ts \
  src/features/worker/workbench/ArtifactWorkbench.test.tsx
```

Expected red because Workbench still calls the legacy full-envelope parser.

- [ ] **Step 4: Implement preview, run green, and commit**

```bash
set -e
npm --prefix web test -- \
  src/projections/snapshotTransport.test.ts \
  src/projections/artifactPreview.test.ts \
  src/features/worker/workbench/ArtifactWorkbench.test.tsx
npm --prefix web run build
git diff --check
git add -- web/src/projections/snapshotTransport.ts \
  web/src/projections/snapshotTransport.test.ts \
  web/src/projections/artifactPreview.ts \
  web/src/projections/artifactPreview.test.ts \
  web/src/features/worker/workbench/ArtifactWorkbench.tsx \
  web/src/features/worker/workbench/ArtifactWorkbench.test.tsx
git diff --cached --name-status
git diff --cached --check
git commit -m "feat: decode bound Artifact preview snapshots"
```

---

### Task 7: Add the serial cause-tagged Snapshot coordinator

**Files:**

- Create: `web/src/state/frontierSnapshotCoordinator.ts`
- Create: `web/src/state/frontierSnapshotCoordinator.test.ts`

- [ ] **Step 1: Write the coordinator red suite**

Use fake timers and deferred promises. Its narrow effects port includes a
synchronous, non-throwing `fenceAndCloseStream(): void`. Prove the exact attempt
order `acquire slot -> fenceAndCloseStream -> freeze generation/cause/request ids
-> fetch`; if a test double throws from the fence, fail closed without fetching.

Cover closed causes; one request/timer/queued automatic/queued Manual slot;
immutable attempt context; commit-stage serial follow-up; in-flight Manual Retry
queueing without early reset; the complete successor order; cause-preserving
retryable transport; initial/Manual one-attempt stop; stopped/upgrade/disposed
outcomes; automatic conflict; same/different cause timer coalescing/replacement;
`command_stale` non-Manual semantics; and final-only `connectWhenIdle`.

```bash
npm --prefix web test -- src/state/frontierSnapshotCoordinator.test.ts
```

Expected red because the coordinator does not exist.

- [ ] **Step 2: Implement the single successor arbiter**

`catch` records retry intent; only `finally`, after releasing the request slot,
chooses: legal queued Manual, stopped outcome, auto conflict, queued auto,
retry-current timer, then final `connectWhenIdle`. A single
`stopAutomaticRecovery(disposition)` owns stopped transitions. No branch, timer or
callback recursively starts fetch/connect.

- [ ] **Step 3: Prove green and commit**

```bash
set -e
npm --prefix web test -- src/state/frontierSnapshotCoordinator.test.ts
npm --prefix web run build
git diff --check
git add -- web/src/state/frontierSnapshotCoordinator.ts \
  web/src/state/frontierSnapshotCoordinator.test.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "feat: coordinate bounded Frontier snapshot recovery"
```

---

### Task 8: Bind the Frontier controller to decoded ingress and the coordinator

**Files:**

- Modify: `web/src/state/frontierViewController.ts`
- Modify: `web/src/state/frontierViewController.test.ts`
- Modify: `web/src/state/useFrontierView.ts` (temporary old-call adapter only)
- Modify: `web/src/state/useFrontierView.test.tsx`
- Modify: `web/tests/frontierMock.ts`
- Modify: `web/tests/continuity-flow.spec.ts`

- [ ] **Step 1: Write bound-controller integration tests and prove red**

Require decoder-before-replica effects; identity-first future-schema handling;
same-epoch duplicate no-op; branded continuity/integrity/replica causes; three
independent single-slot guards and the exact reset matrix; submission-response
subscription; access clear/read-only terminal outcomes; and explicit public
`manualRetry()` versus `reconcileCommandStale()`. Use two replicas to prove a 404
access clear removes data/actions/pending authority only from the controller's
bound replica while the unrelated replica remains byte-for-byte unchanged.

Require the controller's unique close primitive to invalidate its token, remove the
stream ref, then close. Pass only that primitive into coordinator effects. Assert
Snapshot-slot fencing happens before freeze/fetch; `onerror` fences before timer;
old Delta/Control/transient/open/error callbacks are inert; no intermediate stream
opens before queued successors; final idle commit opens exactly one.

For the temporary old hook call, add a focused compatibility test proving its
returned `refresh` invokes controller `manualRetry()` exactly once and never
`reconcileCommandStale()`; Task 10 removes both this alias and the test case.

```bash
npm --prefix web test -- \
  src/state/frontierViewController.test.ts \
  src/state/useFrontierView.test.tsx
```

Expected red on the bound constructor/coordinator wiring and on the old-call
`refresh` -> `manualRetry` compatibility assertion.

- [ ] **Step 2: Add production browser recovery tests and prove red**

Extend `installFrontierEventHarness` with `inspectSources()` returning count, URLs
and ready states plus `openLatest()`; do not infer EventSource creation from
network requests. Add two exact tests in `continuity-flow.spec.ts`:

- `SSE error followed by Snapshot 404 clears the bound View and does not reconnect`
  asserts current View data/actions/pending state are cleared, the old source is
  closed and source count does not increase. The unrelated-replica proof stays in
  the controller unit test rather than exposing registry state to Playwright.
- `SSE error followed by 503 uses one timer and one SSE reconnect` asserts initial
  200 -> SSE error -> Snapshot 503 -> sole timer/probe -> Snapshot 200 -> exactly
  one new source whose URL carries the new cursor; waiting another backoff interval
  creates neither a request nor a source.

```bash
npm --prefix web run test:browser -- \
  tests/continuity-flow.spec.ts \
  --grep 'SSE error followed by Snapshot 404 clears the bound View and does not reconnect'
npm --prefix web run test:browser -- \
  tests/continuity-flow.spec.ts \
  --grep 'SSE error followed by 503 uses one timer and one SSE reconnect'
```

Expected red because the old controller reconnects EventSource directly and does
not run the Snapshot HTTP/coordinator path after `onerror`.

- [ ] **Step 3: Implement the narrow adapter and prove green**

Controller accepts a bound decoder, leased replica and coordinator. Remove unsafe
durable casts and raw Projection `parseEvent<T>`; keep transient overlay isolated
and token-fenced. Its only public lifecycle methods are `start()`, `manualRetry()`,
`reconcileCommandStale()` and `stop()`; `requestSnapshot(cause)` is private.

Keep only the minimum temporary constructor adapter needed for the not-yet-cut-over
hook to compile; mark it for Task 10 deletion and add no caller. Update the existing
hook's temporary `refresh` return to invoke `manualRetry()` so the controller itself
has no public refresh alias while intermediate production callers still compile.

```bash
set -e
npm --prefix web test -- \
  src/state/frontierSnapshotCoordinator.test.ts \
  src/state/frontierViewController.test.ts \
  src/state/useFrontierView.test.tsx
npm --prefix web run test:browser -- \
  tests/continuity-flow.spec.ts \
  --grep 'SSE error followed by Snapshot 404 clears the bound View and does not reconnect'
npm --prefix web run test:browser -- \
  tests/continuity-flow.spec.ts \
  --grep 'SSE error followed by 503 uses one timer and one SSE reconnect'
npm --prefix web run build
git diff --check
```

- [ ] **Step 4: Prove obsolete public refresh aliases are absent and commit**

```bash
set -e
if rg -n '(^|[[:space:]])((public|private|protected)[[:space:]]+)?(async[[:space:]]+)?(refresh|requestRefresh)[[:space:]]*\(' \
  web/src/state/frontierViewController.ts; then
  echo 'obsolete FrontierViewController refresh alias remains' >&2
  exit 1
else
  projection_rg_status=$?
  if [ "$projection_rg_status" -ne 1 ]; then exit "$projection_rg_status"; fi
fi
git add -- web/src/state/frontierViewController.ts \
  web/src/state/frontierViewController.test.ts \
  web/src/state/useFrontierView.ts \
  web/src/state/useFrontierView.test.tsx \
  web/tests/frontierMock.ts \
  web/tests/continuity-flow.spec.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "refactor: bind Frontier controller recovery"
```

---

### Task 9: Add bound-view hook leases before route cutover

**Files:**

- Modify: `web/src/state/useFrontierView.ts`
- Modify: `web/src/state/useFrontierView.test.tsx`

- [ ] **Step 1: Write the bound hook lifecycle red suite**

Require the bound-call form
`useFrontierView(productionViews.worker(workerSessionId))`; one lease acquire; one
controller; stop before final lease release; safe effect replacement; same-key HMR
reuse by semantic `contractKey`; and explicit `manualRetry` plus
`reconcileCommandStale` results.

```bash
npm --prefix web test -- src/state/useFrontierView.test.tsx
```

Expected red because the hook still accepts free type/view/parser inputs.

- [ ] **Step 2: Implement the bound path and prove green**

Keep a temporary old-call overload only so the five existing consumers compile;
it must delegate through a named compatibility adapter and is deleted in Task 10.

```bash
set -e
npm --prefix web test -- \
  src/state/useFrontierView.test.tsx \
  src/state/productionViewReplicaRegistry.test.ts
npm --prefix web run build
git diff --check
git add -- web/src/state/useFrontierView.ts \
  web/src/state/useFrontierView.test.tsx
git diff --cached --name-status
git diff --cached --check
git commit -m "refactor: lease bound production Views in React"
```

---

### Task 10: Cut over five consumers, browser fixtures and every legacy seam

**Files:**

- Modify: `web/src/state/useFrontierView.ts`
- Modify: `web/src/state/useFrontierView.test.tsx`
- Modify: `web/src/state/clientViewReplica.ts`
- Modify: `web/src/state/clientViewReplica.test.ts`
- Modify: `web/src/state/frontierViewController.ts`
- Modify: `web/src/state/frontierViewController.test.ts`
- Modify: `web/src/features/home/HomePage.tsx`
- Modify: `web/src/features/home/HomePage.test.tsx`
- Modify: `web/src/features/home/HomePage.connection.test.tsx`
- Modify: `web/src/app/routes.tsx`
- Modify: `web/src/app/routes.test.tsx`
- Modify: `web/src/features/assistant/AssistantConversationPage.test.tsx`
- Modify: `web/src/features/worker/WorkerDetailPage.test.tsx`
- Modify: `web/src/features/worker/components/WorkerCommandComposer.test.tsx`
- Modify: `web/src/features/artifact/ArtifactDetailPage.tsx`
- Modify: `web/src/features/artifact/ArtifactDetailPage.test.tsx`
- Modify: `web/src/api/homeContracts.ts`
- Modify: `web/src/api/homeContracts.test.ts`
- Modify: `web/src/api/assistantContracts.ts`
- Modify: `web/src/api/assistantContracts.test.ts`
- Modify: `web/src/test/frontierMock.test.ts`
- Modify: `web/tests/frontierMock.ts`
- Modify: `web/tests/continuity-flow.spec.ts`
- Modify: `web/tests/home-flow.spec.ts`
- Modify: `web/tests/worker-flow.spec.ts`
- Delete: `web/src/features/artifact/ArtifactSnapshotParser.ts`

- [ ] **Step 1: Write production-composition red tests**

Require all five exact factories at their real production consumers; no caller may
provide `<T>`, raw View strings or a parser. Require `ViewBoundary.onRetry` to call
only `manualRetry`, and Worker `onStale` only `reconcileCommandStale`. Inject
malformed Task and Worker Snapshot/Delta fixtures and prove no command authority UI
renders or enables.

```bash
npm --prefix web test -- \
  src/state/useFrontierView.test.tsx \
  src/app/routes.test.tsx \
  src/features/home/HomePage.test.tsx \
  src/features/home/HomePage.connection.test.tsx \
  src/features/assistant/AssistantConversationPage.test.tsx \
  src/features/worker/WorkerDetailPage.test.tsx \
  src/features/worker/components/WorkerCommandComposer.test.tsx \
  src/features/artifact/ArtifactDetailPage.test.tsx
```

Expected red while consumers still use the compatibility overload.

- [ ] **Step 2: Write the browser-fixture contract red test**

Extend `frontierMock.test.ts` to require empty Artifact Snapshot `related_refs`
while preserving the currently proved Home/Worker outward refs. Update the Home
and Worker Playwright expectations so Return-to-Worker authority is absent from an
Artifact View.

```bash
npm --prefix web test -- src/test/frontierMock.test.ts
```

Expected red because the current Artifact fixture still fabricates a reverse
Worker ref.

- [ ] **Step 3: Cut over all five production Views**

- Home => `productionViews.home()`;
- Tasks => `productionViews.taskIndex()`;
- Assistant => `productionViews.assistant(conversationId)`;
- Worker => `productionViews.worker(workerSessionId)`;
- Artifact => `productionViews.artifact(artifactId)`.

Delete the hook compatibility overload, controller compatibility constructor, raw
replica commit shims, generic registry `get<T>`, public Home/Assistant full-envelope
parsers, `ArtifactSnapshotParser.ts`, optional parser options and forwarding
wrappers. Remove Artifact-to-Worker navigation based on the now-forbidden ref.

- [ ] **Step 4: Align real browser fixtures with the closed contract**

Replace `worker.runtime_recovering` and `assistant.command_projected` in
`continuity-flow.spec.ts` with `view.entity_upserted.v1`. Make Artifact Snapshot
`related_refs` empty in `frontierMock.ts`, `home-flow.spec.ts` and
`worker-flow.spec.ts`; remove the old Return-to-Worker assertion while preserving
Worker -> Artifact navigation through the Worker projection. Update
`frontierMock.test.ts` to make the empty Artifact-ref rule executable.

- [ ] **Step 5: Run focused unit and browser green gates**

```bash
set -e
npm --prefix web test -- \
  src/projections/viewRuntime.test.ts \
  src/projections/productionViewRegistry.test.ts \
  src/state/clientViewReplica.test.ts \
  src/state/frontierSnapshotCoordinator.test.ts \
  src/state/frontierViewController.test.ts \
  src/state/useFrontierView.test.tsx \
  src/app/routes.test.tsx \
  src/features/home/HomePage.connection.test.tsx \
  src/features/worker/components/WorkerCommandComposer.test.tsx \
  src/test/frontierMock.test.ts
npm --prefix web run test:browser -- tests/continuity-flow.spec.ts
npm --prefix web run test:browser -- \
  tests/home-flow.spec.ts tests/assistant-flow.spec.ts tests/worker-flow.spec.ts
npm --prefix web run build
git diff --check
```

- [ ] **Step 6: Run robust static negative gates**

Each `rg` exit 1 means no match; exit 2+ is a gate failure.

```bash
projection_assert_absent() {
  projection_pattern=$1
  shift
  if rg -n "$projection_pattern" "$@"; then
    echo "obsolete production seam remains: $projection_pattern" >&2
    exit 1
  else
    projection_rg_status=$?
    if [ "$projection_rg_status" -ne 1 ]; then exit "$projection_rg_status"; fi
  fi
}
projection_assert_absent \
  'parseSnapshot\?|parseDelta\?|value as ProjectionSnapshot|value as ProjectionDelta' \
  --glob '!**/*.test.*' web/src/state
projection_assert_absent '\brefresh\b' \
  web/src/state/useFrontierView.ts
projection_assert_absent 'onStale=\{(refresh|manualRetry)\}' \
  --glob '!**/*.test.*' web/src/app
projection_assert_absent \
  'parseHomeSnapshot|parseHomeDelta|parseAssistantSnapshot|parseAssistantDelta|ArtifactSnapshotParser' \
  --glob '!**/*.test.*' web/src
projection_assert_absent \
  '(^|[[:space:]])((public|private|protected)[[:space:]]+)?(async[[:space:]]+)?(refresh|requestRefresh)[[:space:]]*\(' \
  web/src/state/frontierViewController.ts
```

- [ ] **Step 7: Commit the explicit cutover set**

```bash
set -e
git add -- web/src/state/useFrontierView.ts \
  web/src/state/useFrontierView.test.tsx \
  web/src/state/clientViewReplica.ts \
  web/src/state/clientViewReplica.test.ts \
  web/src/state/frontierViewController.ts \
  web/src/state/frontierViewController.test.ts \
  web/src/features/home/HomePage.tsx \
  web/src/features/home/HomePage.test.tsx \
  web/src/features/home/HomePage.connection.test.tsx \
  web/src/app/routes.tsx web/src/app/routes.test.tsx \
  web/src/features/assistant/AssistantConversationPage.test.tsx \
  web/src/features/worker/WorkerDetailPage.test.tsx \
  web/src/features/worker/components/WorkerCommandComposer.test.tsx \
  web/src/features/artifact/ArtifactDetailPage.tsx \
  web/src/features/artifact/ArtifactDetailPage.test.tsx \
  web/src/api/homeContracts.ts web/src/api/homeContracts.test.ts \
  web/src/api/assistantContracts.ts web/src/api/assistantContracts.test.ts \
  web/src/test/frontierMock.test.ts \
  web/tests/frontierMock.ts web/tests/continuity-flow.spec.ts \
  web/tests/home-flow.spec.ts web/tests/worker-flow.spec.ts \
  web/src/features/artifact/ArtifactSnapshotParser.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "refactor: bind production Views to runtime decoders"
```

---

### Task 11: Prove browser/repository compatibility and close optimization #2

**Files:** No planned implementation edits. If a gate fails, return to the owning
task, add or update the focused red test, implement the minimal fix, stage the
exact files and commit that fix before restarting Task 11. Never stage the
pre-existing dirty `.superpowers/sdd/progress.md` or unrelated user paths.

- [ ] **Step 1: Require committed implementation state**

```bash
set -e
test -z "$(git status --short -- web/src web/tests)"
git diff --check
```

Do not begin fixed-range review with uncommitted implementation changes.

- [ ] **Step 2: Run the complete Web unit/browser/build gates**

```bash
set -e
npm --prefix web test
npm --prefix web run test:browser
npm --prefix web run build
```

Record exact file/test counts and skips across all Playwright specs, including
`home-tasks.spec.ts`, `product-shell.spec.ts` and `visual.spec.ts`. The browser
evidence must name the 404 clear and 503 -> timer -> 200 -> one reconnect scenarios.

- [ ] **Step 3: Prove protocol/migration/persisted-wire immutability over the fixed range**

```bash
set -e
make generated-protocol-check
make migration-check
projection_protected_base=93edec266ab26d0b5900bed7b0b3f5866ef8a753
projection_protected_paths=(
  internal/eventingprojection/application/projector.go
  internal/eventingprojection/application/snapshot.go
  internal/eventingprojection/application/replay.go
  internal/eventingprojection/application/display.go
  internal/eventingprojection/application/timeline.go
  internal/eventingprojection/application/view_continuity_reader.go
  internal/eventingprojection/adapter/postgres/continuity_reader.go
  internal/eventingprojection/adapter/postgres/materializer.go
  internal/eventingprojection/adapter/postgres/store.go
  internal/eventingprojection/adapter/postgres/display.go
  internal/eventingprojection/adapter/postgres/timeline.go
  internal/eventingprojection/adapter/postgres/home_refresh_fencing_integration_test.go
  internal/eventingprojection/adapter/postgres/worker_detail_contract_upgrade_integration_test.go
  internal/frontier/http/handler.go
  internal/frontier/sse/handler.go
  migrations
  api/schema/v1
)
git merge-base --is-ancestor "$projection_protected_base" HEAD
test -z "$(git rev-list \
  "$projection_protected_base"..HEAD -- "${projection_protected_paths[@]}")"
git diff --no-ext-diff --exit-code \
  "$projection_protected_base"..HEAD -- "${projection_protected_paths[@]}"
test -z "$(git status --porcelain=v1 --untracked-files=all -- \
  "${projection_protected_paths[@]}")"
git diff --check "$projection_protected_base"..HEAD
git diff --check
```

The range gate, not only current worktree status, proves committed history did not
change Projection wire structs, materializer payloads or persisted Home/Worker
upgrade fixtures/digests.

- [ ] **Step 4: Run repository verification with real PostgreSQL**

```bash
set -e
set -o pipefail
projection_verify_log=$(mktemp -t switchback-verify)
if ! GOFLAGS='-v' \
  SWITCHBACK_TEST_DATABASE_URL='postgresql:///postgres?host=/tmp' \
  make verify 2>&1 | tee "$projection_verify_log"; then
  echo 'repository verification failed' >&2
  exit 1
fi
if rg -n -- '--- SKIP:' "$projection_verify_log"; then
  echo 'repository verification skipped an integration test' >&2
  exit 1
else
  projection_rg_status=$?
  if [ "$projection_rg_status" -ne 1 ]; then exit "$projection_rg_status"; fi
fi
```

`pipefail` preserves a failed `make verify` through `tee`; `GOFLAGS=-v` makes Go
skip lines observable. Evidence is invalid if the URL is unset, PostgreSQL/`psql`
is unavailable, or the log contains `--- SKIP:`.

- [ ] **Step 5: Run independent final fixed-range reviews**

Review `93edec266ab26d0b5900bed7b0b3f5866ef8a753..HEAD` in parallel:

- Standards: AGENTS/repository/deep-module compliance;
- Spec: every design success criterion, compatibility boundary and non-goal.

Resolve every Critical/Important finding by returning to its focused red test,
committing the exact fix, rerunning all Task 11 gates, and re-reviewing the new
fixed range. Fix or explicitly record Minor findings with evidence.

- [ ] **Step 6: Handoff exact evidence**

Report commits/modules; focused/full/browser/real-PostgreSQL outputs; static gates;
unchanged schema/migrations/Projection wire/materializer/Home+Worker upgrade
fixtures; and pre-existing npm audit/chunk warnings separately from regressions.
