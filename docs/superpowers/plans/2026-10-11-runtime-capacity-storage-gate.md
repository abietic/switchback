# Runtime capacity and storage gate implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task-by-task. Do not commit: the user has not authorized Git mutations.

**Goal:** Reject unsupported writable-layer enforcement before Worker execution, and replace the local literal admission reference with evidence-bound capacity admission before dispatch.

**Architecture:** The Docker adapter owns Engine capability evidence and immutable container options. Local product composition owns the admission-before-permit ordering; neither client nor Task supplies resource numbers. Missing or unsupported evidence denies execution, rather than falling back to unbounded containers.

**Tech Stack:** Existing Go, Moby client, PostgreSQL test fixture and Docker Engine; no new dependency or migration.

## Global Constraints

- `local_standard_v1`: 2 vCPU, 4 GiB memory, 512 PIDs and a 10 GiB container writable layer.
- Aggregate CPU, memory and storage execution budgets are at most 80% of proven Docker Engine availability.
- Insufficient capacity preserves Session/FIFO and creates no dispatch permit, active AgentTurn, `started_at` or deadline.
- Unsupported or unknown storage enforcement fails closed. Docker create success or an echoed StorageOpt is not proof that a snapshotter enforces size.
- Preserve old services, settings, DB and evidence. No model calls, Git mutation or source edits outside the owned integration worktree.
- Public test seams: Docker `Provision`/resource preflight, and production dispatch-before-Run behavior; use exact existing fixed profile, not private-helper assertions.

---

### Task 1: Enforce the fixed writable layer

**Files:** `internal/workerruntime/adapter/docker/provisioner.go`, `provisioner_test.go`, new `writable_layer.go` and `writable_layer_test.go` in the same directory.

**Interfaces:** Keep the existing `Engine` interface compatible with cleanup-only adapters. Introduce a separate Info-capable interface using the pinned client's `Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error)`. Export `Provisioner.CheckWritableLayer(context.Context) error` for the subsequent pre-permit gate. Provision always invokes that check before inspect/start, host path verification, secret staging or create. No optional permissive configuration switch.

- [x] RED through public Provision: containerd overlayfs, unknown driver/OS, absent Info, canceled/failed Info deny with no create/start/secret volume calls. A classic Linux overlay2/XFS configuration may attempt the mandatory StorageOpt; Engine rejection must propagate and never trigger retry without size.
- [x] GREEN: set Worker `HostConfig.StorageOpt["size"]` to decimal `10737418240`, from the validated fixed profile; retain existing CPU/memory/PID isolation. Verify an existing container has the exact size option before restarting it; missing/different options reject rather than silently adopting a legacy uncapped Runtime. Supported preflight is only compatibility evidence; the Engine remains responsible for enforcing its XFS project quota and rejects unsupported mount configuration on create.
- [x] Test the above using recording Engine at the external adapter seam, one RED/GREEN cycle at a time. Include matching reuse, mismatched reuse, no numeric override, and cleanup compatibility. Full affected Docker package once, then relevant race once; capture raw logs/receipts.
- [x] Self-review, report all commands and source changes to `.scratch/runtime-writable-layer-1011-report.md`. No commit; hand the controller the exact file list for a dirty-baseline diff and independent review.

### Task 2: Capacity admission and production ordering

**Current slice (2A):** production composition rejects new execution before permit/Run/Turn/prompt while authoritative storage availability and durable aggregate reservation are missing. This is a fail-closed safety slice, not positive RuntimeCapacityAdmission. Even a compatible writable-layer check cannot authorize execution by itself.

**Files:** `internal/localproduct/app.go`, `runtime_runner.go`, `runtime_settlement.go`; new `runtime_start_guard.go`, `runtime_guarded_commands.go`, `runtime_start_guard_integration_test.go`. Existing test-only fixture paths may be adapted as listed in the independent brief. Architecture-session ownership was confirmed for these eleven paths on 2026-10-11; no application/PG adapter/schema/protocol/Dockerfile change is included.

**Brief:** `.superpowers/sdd/runtime-start-guard-1011-brief.md`, based on `.scratch/runtime-capacity-seam-1011-report.md`. Full positive accounting, reservation, release/reconciliation and UI `runtime_capacity` waiting remain pending, not checked off by 2A.

- [x] Before implementing, verify existing admission/start/recovery and queued-turn paths; record the actual public seams, serialized reservation ownership, and exact release/reconciliation behavior in a task brief. Do not broaden into schema/protocol/Dockerfile changes without coordinating the added paths. Current 2A has no durable reservation owner; positive ownership is explicitly deferred.
- [x] Prove storage availability comes from the Engine storage pool, not host APFS/backend container statfs or Docker system df usage. If that evidence cannot be obtained under the authorized deployment, deny with an explicit reason and keep actual startup blocked; do not claim positive capacity admission. Current storage evidence is unproven: only the denial branch is completed, not physical pool proof.
- [ ] Implement/read-only validate fixed-profile resource accounting and admission before permit/deadline using exact proven evidence. Preserve FIFO, idempotency and resident resources; concurrent requests cannot both spend one remaining slot. Record limitations rather than introducing a second scheduler.
- [x] Implement 2A's fail-closed production ordering without blocking prior Turn terminal evidence, artifacts, cancellation/expiry or cleanup. Keep accepted queues and existing resident Sessions intact; no production bypass switch or fake admission ref.
- [x] Parent verifies exact candidate, reviews the task-scoped diff and leaves physical gate open until current Engine capability and capacity checks both succeed.

2A closed locally on 2026-10-12 after its original task review plus the subsequent three-path settlement/init fix r2 review and fresh parent PG regression. Positive fixed-profile accounting, atomic reservation, permit consumption and capacity release remain unchecked. Evidence index: `.scratch/settlement-capacity-fairness-parent-closeout-1012.md`; next staged plan: `docs/superpowers/plans/2026-10-11-isolated-linux-experience-execution.md`. No full-package/whole-platform or physical acceptance is implied.

### Task 3: Independent physical acceptance

- [x] Run one newly named, no-start/no-mount/no-model diagnostic container create with existing immutable public image and `--storage-opt size=10G`, preserving raw create and bounded inspect evidence. It cannot by itself prove enforcement; compare current official Engine behavior and reject unproven support. Actual probe accepted/echoed StorageOpt but did not prove enforcement; exact never-started owned container was removed and old services preserved. See `.scratch/runtime-capacity-physical-preflight-1011.md`.
- [ ] Only after all safety gates succeed, freeze the exact dirty product source, build four provenance-bound images, start the new isolated experience project within proven capacity, and execute the previously authorized single small real Task.
- [ ] Keep the old failed Task/Worker and all receipts. Do not change Docker settings, remove old services, weaken 10 GiB, or spend the model task to test a known rejected environment.
