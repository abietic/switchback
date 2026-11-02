# G4 Full-Stack Cold-Start Acceptance Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Prove that a fresh production Backend executes two controlled Turns while all nine observation services remain absent, then verifies the original immutable sources after Langfuse returns.

**Architecture:** Add a separately named full-stack cold proof to the existing acceptance driver. Reuse the current Gateway workload, cold captures, event subscription handshake, interval derivation and immutable source verification; preserve legacy five-service proof semantics. This is one independently reviewable portion of approved OpenSpec task 3.2, not full G4 completion.

**Tech Stack:** Go, existing Docker Compose, PostgreSQL, controlled ACP/model fixture.

## 📋 Global Constraints

- Work only in `.worktrees/observability-supplements-v1`; preserve unrelated main-checkout changes.
- PostgreSQL and authoritative storage stay available and unchanged during observation faults.
- No real provider, YHC, dependency upgrade, automatic recovery enablement, schema migration or production behavior change.
- Nine required services: langfuse-web, langfuse-worker, langfuse-clickhouse, langfuse-redis, langfuse-minio, prometheus, alloy, loki, grafana.
- Keep existing five-service Langfuse reports valid and distinct. Never turn a legacy pass into full-stack evidence.
- Keep the actual 5-second settlement/next-dispatch limit. All observations must cover the exact claim, identities and time window with uninterrupted event subscription and actual probes.
- Root alone operates Docker, only on fresh claim-owned resources; retain data volumes and private evidence. Never expose generated credentials.
- OpenSpec 3.2 remains unchecked until paired matrix, loss/recovery, actual retention expiry and final review also pass.

---

### Task 1: Separate full-stack cold proof

**Files:** Create `test/acceptance/observability/full_stack_cold.go` and `full_stack_cold_test.go`; minimally modify `driver.go`, `capture.go`, `service_intervals.go`. Report `.superpowers/sdd/g4-cold-task-1-report.md`.

**Interfaces:** Consume existing `RawEvidence`, `ProjectCapture`, `IntervalEvidence`, `PhysicalEvidence.validateCold`, `supplementEvidenceSample`, `readClaim`, `writeEvidence`, `dockerIntervalCollector` and `collectIntervalsWithIO`. Produce explicit CLI modes `collect-full-stack-cold` and `verify-full-stack-cold`, writing `full-stack-cold-passed.json` exclusively after successful verification.

- [x] Write failing tests for full-stack proof validation. Build on the existing valid cold physical fixture, then require each additional service's absence/probes; reject old five-service-only evidence, running/duplicate/contradictory captures, wrong project, incomplete intervals, unexpected subscription EOF, altered source/identity, timestamps outside the startup window and latency above 5 seconds.
- [x] Run `GOCACHE=/private/tmp/Switchback-supplements-go-build go test ./test/acceptance/observability -run FullStackCold -count=1`; record expected RED.
- [x] Implement reusable service-list validation behind existing wrappers without changing legacy defaults. Full-stack cold collection probes only absence/non-running for all nine (a running service is not treated as unavailable), using exact claim Docker inspection, and reuses existing event subscription/handshake/finish protocol. Capture records the four new services as absent when absent. Parameterize only the necessary interval coverage loop, keeping legacy wrappers unchanged.

```go
// Legacy callers retain observationServices; the full-stack proof supplies nine.
func (e IntervalEvidence) validateWindowForServices(project, state string, from, to time.Time, services []string) error
// The separate verifier reuses legacy source/cold gates and adds exact full-stack coverage.
func verifyFullStackCold(project, output string) error
```

- [x] Verifier reads canonical claim-owned bounded files: `cold-before.json`, `cold-after.json`, `smoke-pending.json`, `smoke-passed.json`, and recorded interval files. Require exact original-to-recovered source comparison, original cold Turn creation/terminal timing and all-nine absence through the after-capture completion. Result explicitly states controlled cold-start-only scope, includes source/image identities from evidence and evidence digests; cannot imply paired-matrix/loss/retention completion. All pass writes are exclusive 0600. Reject symlinked inputs and arbitrary caller-provided origins/paths. Failure writes no pass file.
- [x] Run focused tests, package tests and package race once; record exact results, commit and self-review. Task review must independently approve spec and quality.

### Task 2: Physical execution and closeout

**Files:** Create `.superpowers/sdd/g4-cold-final-verification.md`; update `.superpowers/sdd/g4-progress.md` and plan checkboxes only after evidence.

- [x] Create a fresh exclusive synthetic claim with existing driver. Verify reused G3 image digests and production source equality; no need to rebuild identical binaries. Start only PostgreSQL, migration and controlled model fixture.
- [x] Start `collect-full-stack-cold`, wait for recorded readiness; capture before Backend creation. Start Backend with explicit services and `--no-deps` after migration success. Run existing `smoke` through Gateway, capture after, gracefully finish collector before restoring observation services.
- [x] Start the claim's Langfuse services, await source verification through existing `snapshot`, then run `verify-full-stack-cold`. Retain failures as failures; do not edit raw evidence to pass.
- [x] Run scoped regression, static checks and strict OpenSpec; obtain independent integration review of the new delta and relevant unchanged gates. Clean up exact test resources, retaining data and recording identities. Do not mark 3.2 complete or merge PR.

## 🔗 References

- [Approved contract](../../../openspec/changes/local-observability-stack/specs/local-observability-stack/spec.md)
- [G3 evidence](../../../.superpowers/sdd/g3-correlation-final-verification.md)
