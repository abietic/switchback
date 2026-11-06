# Native observer validation plan

> Use subagent-driven-development with one product writer and independent task reviews. No Git mutation. The user asked to continue the selected IPC route; preserve all consumed phases and frozen history.

**Goal:** Fix the now-observed Engine startup dependency and validate native IPC without starting an Engine, container, Task or model.

**Design:** The observer owns only finite observation. It must run independently of Engine lifecycle, return unavailable when the fixed socket is absent, and leave all execution authority with existing gates. Native validation uses the existing dedicated VM and new owned objects only. It cannot close real quota or full-capacity acceptance.

## Evidence and boundaries

- Prior source seal: 693e5fdce00d4ea2e02e071d3aa0ca03976e65a21cd37978ec2c52dacdf6c9cb; all19185bindings verified unchanged before this phase.
- Fresh guest audit: .scratch/runtime-native-observer-1016/guest-audit/raw.log. Stock and old consumed daemon units are masked/inactive, no dockerd/containerd PID, fixed private socket absent, observer not installed. XFS UUID8782738b-dfc2-4426-966f-6a7eb0889ddd remains mounted prjquota. VM Running and host memory pressure1 are finite observations only.
- Never unmask/start old daemon units or reuse1013/1014 permits, attempts, reservations or receipt directories. No Engine API mutation, image/container, DB, production Trust/signing, Task/ACP/model or paid acceptance in this plan.
- Backend cap_drop ALL, fixed IPC/key paths, existing quota policy, core aggregate limits, source-only history and full-capacity UNKNOWN remain intact.

## Task 1: independent observer service

- [x] Freeze858-row source baseline and three task-local before files.
- [x] Change the existing unit test to require only Description in Unit; run it against the old unit and retain the actual failure showing docker.service dependencies.
- [x] Remove After=docker.service and Requires=docker.service from the unit. No replacement Engine dependency or implicit startup. Update runbook with independent lifecycle and unavailable/fail-closed behavior.
- [x] Run focused/full asset checks and a fresh isolated absent-endpoint Compose config/merge check. Freeze three-file diff/source/raw receipts. Independent Spec+Quality review; repair any Important/Critical finding before native use.

## Task 2: bounded native IPC check, no Engine

- [x] Prepare exact source-bound observer/client binaries and a diagnostic manifest. Client uses public ReadClientKey/NewClient/Observe, fixed profile and synthetic diagnostic Binding; it expects authenticated partial collection and strict observation rejection while Engine is absent. No production Task identity or Trust is manufactured.
- [x] Review the native operation packet before effects: exact new paths, unit bytes, key metadata without key content, one start,45s RuntimeMaxSec and independent cleanup bound, existing VM/boot/mount guard, no Engine start, no overwriting occupied objects, original receipt/error preservation and no automatic actual retry.
- [ ] In the existing VM create only the reviewed new files and dedicated finite-role key, run the service under exact guarded cgroup/profile, and run the bounded client without capabilities. Read back actual cgroup/namespace/peer/finite result. Secret bytes never enter logs, env, argv or host artifacts.
- [x] Stop the exact newly owned observer, retain evidence and secure key, verify no PID/listener and no Engine state transition. Do not delete or mutate old objects. A failed actual consumes its own new attempt; keep it and stop rather than silently retry.
- [x] Freeze native receipts and publish remaining gates: fresh bounded Engine/quota positive, full-capacity/exclusive-writer/backing proof, real PG/transport, one paid report/accept/refresh/same-task observability acceptance, fake fidelity and deterministic activation-race investigation. OpenSpec6.5/8.4/9.3 remain open.

Actual packet-r4 was executed once and consumed, with original outer/nested exit1 `client_original_exit`. Observer startup/native cgroup/capability/socket readback succeeded; actual client public output, independently retrieved read-only afterward, proves authenticated partial collection, zero capabilities, NNP and strict rejection. The oneshot inner exit readback was0/0, so the overall gate remains FAILED and the native validation checkbox stays open. Cleanup and independent read-only post passed. Never retry or relabel this consumed actual.

## Task 3: source-only repair for future exit-status retention

- [x] Preserve executed R2 source/handoff bytes before changes; require only source/offline changes, no new packet or native execution.
- [x] Add phase-only client RemainAfterExit=yes and require effective Type=oneshot/RemainAfterExit=yes before Start. Preserve the strict ExecMainCode1/ExecMainStatus0 requirement; code0 remains unknown/rejected.
- [x] Record focused RED/GREEN tests and freeze the repaired source. Review the cumulative phase and classify native facts separately from the consumed failed overall result. A future independently scoped phase must validate actual exit retention; this task cannot turn the past failure green.

Final cumulative independent review: source/evidence PASS, unresolved C0/I0/M0. The one wording Minor is closed by task-2-report-r3-erratum.md without altering sealed reports/raw. R3 has38/38 offline PASS; native original-exit retention and the overall IPC acceptance remain open.
