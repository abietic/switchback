Sections 1–4 record implemented v2 groundwork. The approved 2026-09-11 supplement design was confirmed on 2026-09-13; section 6 implements that revision. Sections 5.1/5.2 closed on 2026-09-28 using the G controlled physical evidence and fresh final native gates. See `docs/superpowers/plans/2026-09-13-immutable-model-observation-supplements.md` and `.superpowers/sdd/supplements-closeout-2026-09-28.md` for scope and verification; older dated dispositions below remain historical evidence.

## 1. Reconstructable source evidence

- [x] 1.1 Seal versioned complete Turn/model source JSONL through finalizer without changing legacy Trace bytes.
- [x] 1.2 Verify round-trip, deterministic model ordering, provenance, checksum, missing-source and credential-marker behavior.

## 2. Durable source jobs

- [x] 2.1 Add activation/exclusion/job migration and bounded discovery with exact source requests.
- [x] 2.2 Implement database-clock leases, stale completion fencing and authoritative source loading.
- [x] 2.3 Verify real PostgreSQL activation races, commit-before-enqueue recovery, restart and concurrent claim behavior.

## 3. Immutable Langfuse projection

- [x] 3.1 Build schema-namespaced immutable Run/Turn/model/event IDs and full-content batches with pre-HTTP credential checks.
- [x] 3.2 Verify source-scoped readback, duplicate replay, later Turns, late old-batch safety and lossless real-service JSON transport.

## 4. Execution independence

- [x] 4.0 Retain a Backend-only generation-bound token scanner across transient input cleanup and restart; registry failure degrades observation only.
- [x] 4.1 Freeze source snapshots from applied journal evidence in settlement and remove synchronous projection calls.
- [x] 4.2 Run independent cancellable background projection with bounded retries and safe blocked diagnostics.
- [x] 4.3 Make invalid/disabled observability configuration nonfatal and remove Backend Compose startup coupling.
- [x] 4.4 Verify runtime settlement/next-turn independence, document operations and distinguish fixture from live acceptance.

## 5. Completion gates

- [x] 5.1 After v3 tasks, run strict OpenSpec, focused/race/short-suite and non-skipped real PostgreSQL gates, preserve v2 evidence and resolve independent review findings.
- [x] 5.2 Pass v3 real Docker/Langfuse smoke, 20 healthy/outage pairs, restart/fault/cleanup gates with continuous evidence; do not mark complete from fixture-only or uncollected results.

  2026-09-28 completion: `eb5880e` independently fixes the date-rotted Assistant HTTP fixture and adds an explicit model-error assertion. Fresh whole-short1520, expanded race643 and real-PG594 tests/subtests passed with zero failure/test skip; migration, protocol and strict OpenSpec passed. Both independent review axes Approved and the original Important gate is closed. G report revalidated with unchanged production/driver/deployment inputs since `6bc6980`; controlled ACP/model + real Docker/Langfuse scope is retained. Exact temporary PostgreSQL cleanup and evidence hashes are in `.superpowers/sdd/supplements-closeout-2026-09-28.md`. No full G4/G5/YHC, remote CI or merge/release claim.

  2026-09-26 scoped disposition: the fresh G controlled ACP/model + real Docker/Langfuse claim passed cold smoke, 20 pairs, both connected-unanswered fault kinds, restart, exact source readback, continuous intervals and claim-owned cleanup; `run-matrix`, `produce-report` and strict `validate` exited0. This physical subgate is evidenced in `.superpowers/sdd/supplements-task-9-physical-run.md`, not a full G4/G5 or real Codex/provider claim. The 5.2 completion checkbox remains open under plan9.7's all-required-gates rule while5.1's whole-short Assistant baseline failure remains unresolved. Current-HEAD race575pass/0skip, realPG594pass/0skip, migration/protocol/strict OpenSpec/diff pass; whole-short1519pass/one pre-existing Assistant `policy_revision` fail/0test skip. Independent Standards review approved; Spec completion review withheld only for that gate. Do not infer overall acceptance or merge/release.

## 6. Immutable model supplements — approved v3 amendment

- [x] 6.1 Implement independent run/turn/model frames, fixed v3 compiler, original-scope frame/wire scan receipts and immutable duplicate/conflict rules.

  2026-09-14 scoped evidence: plan Task 1 frames/compiler/capture and scanner safety fixes are independently approved at `8176dd8` + `4bcef85`; Tasks 2/3 now verify immutable replay/conflict persistence and atomic journal acceptance at `ad2e697` + `d11f3a4` + `85c6c49`. Short/caller/race, adversarial scanner and real PostgreSQL regressions pass. Basis first-freeze wiring, background v3 activation/export, and physical acceptance remain separate open requirements below.
- [x] 6.2 Add additive candidate/publication/job/Run-activation/exact-request persistence with database-clock fencing and real PostgreSQL tests; protect published objects from existing orphan cleanup.

  2026-09-19 scoped evidence: Task 2 additive ledgers/activation at `ad2e697` + `d11f3a4` and Task 6 publication/orphan protection at `bc73e9d` + `299b1a3` are independently approved. Task 2 real-PG/race lease, replay/conflict and preserved-v2 tests pass; Task 6 real-PG fragment/orphan/ledger suite passes without skip, including retained temporary/finalized objects, direct-delete rejection and atomic reference publication. Request authority and production activation remain 6.7; physical acceptance remains 5.2.

- [x] 6.3 Route model facts through one application receiver before Turn waiters and atomically persist receipt with journal Apply, preserving historical start correlation, idle/replay handling and rejection cursor progress.

  2026-09-14 scoped evidence: `85c6c49` independently approved for spec and quality. Unit/architecture/race and real PG 11 model tests plus matched existing journal tests pass without skip, including no-waiter heartbeat, replay, historical authority, safe gaps, conflicts and rollback. Parent additionally ran 7 original registry tests with 20 subtests, all passing. Whole-product late-callback/old-byte and physical gates remain Tasks 6.8/6.9/5.2, not inferred from these fixtures.
- [x] 6.4 Seal Run/Turn basis beside the first frozen evidence-input checkpoint transaction, independently of legacy model completeness and without modifying checkpoint JSON.

  2026-09-14 scoped evidence: `2d785d6` plus `a3be3e8` independently approved for spec and quality. First-freeze/replay/rollback, original registry, report binding, lawful checkpoint extension, missing/ambiguous initial association and concurrent multi-Turn Run-gap reuse are covered. Final five-package race/integration gate passed; Worker PG 34 top-level tests, no skips. Parent independently reran exact original-Turn source mismatch first/replay cases and confirmed the test database's `read committed` default. This proves immutable transaction attestation, not actual final report/manifest publication or physical acceptance; those remain 6.6/6.8/6.9/5.2.
- [x] 6.5 Require proven exact durable fencing for registry cleanup across ordinary terminal/failure/restart paths; preserve forced-stop missing-proof diagnostics and avoid remote-dependent token retention.
- [x] 6.6 Publish verified immutable fragments and export/read back v3 objects through independent bounded workers, including parent waits, crash windows, wire-proof checks and late-batch invariance.

  2026-09-19 scoped evidence: `bc73e9d` + `299b1a3` independently approved across the full Task 6 base. Fresh short/race package sets pass after an environment-only Langfuse listener retry; real-PG fragment/orphan/ledger suite PASS28.510s/noSKIP. Parent independently reran core-publication/identity/lock tests and final corruption regression. Proven fragment corruption now blocks without repeated uploads; ordinary I/O/cancellation remains retryable. Original implementation RED output is unavailable; the I1 repair has genuine recorded RED/GREEN. Local HTTP exact readback is not real Langfuse acceptance or complete user queue behavior; those remain 6.8/6.9/5.2.
- [x] 6.7 Wire v3-only background execution, disable v2 automatic work, implement internal exact historical selection and version-bound coverage, and document safe disabled rollback.

  2026-09-19 scoped evidence: `43b5b1a` + `397799a` independently approved across the original Task7 range. App activation/cancellation/v2 inactivity, exact authorized historical recapture, original registry, no-content gaps and consistent version coverage are tested. A real terminal Apply/duplicate replay deadlock was reproduced and fixed using history-only nonblocking/transaction-local bounded locks, preserving exact proof and atomic rollback/retry. Final PG30top-level+25subtests PASS/noSKIP; changed integration race PASS; parent3+2focused regressions PASS. Distinct initial/target source binding is verified with explicit historical fixture limits, not user queue acceptance. Actual user history was not selected/exported. Full Runtime and physical gates remain 6.8/6.9/5.2.
- [x] 6.8 Add deterministic barrier-based settlement/idle/next-Turn/replay integration coverage, legacy byte invariance and scanner-cleanup recovery without HTTP dependencies.
  2026-09-19 scoped evidence: full96f5f67..cc5615f independently Spec compliant / Quality Approved after exact publication assertion repair. Native real-PG race3 PASS201.596s/57leaves/noSKIP, parent next-active and fault-sensitivity PASS14.379s. Real committed ACK/active effect, old bytes, failure windows and sameRun legacy marker/root source are covered with explicit fixture limits. No historical production RED is claimed; compile failures are setup only. Task8 M1 expected-error logging remains for final review. Physical continuous evidence and full completion gates remain6.9/5.1/5.2.
- [x] 6.9 Adapt controlled physical fixtures and v3 exact-set verifier; implement continuous service/fault/restart/residue evidence collection before running the completion gates.

  2026-09-26 scoped evidence: instrumentation original-range review approved through `6bc6980`, including the real HTTP ingestion-cancellation repair. Fresh G physical report strict-validates20 pairs, two connected/cancelled/recovered fault kinds, six exact recovered sources and owned residue without threshold relaxation. E failed matrix and F preflight failure remain separate failed claims; no evidence was reused as G PASS. This closes fixture/collector implementation, not completion gate5.1, whole-platform G4/G5 or real Coding Agent/provider acceptance.
