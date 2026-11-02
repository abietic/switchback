# G3 correlation acceptance implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Prove that two controlled executions through the production Backend/Worker path have corresponding exact-scoped Loki events and real Langfuse v3 readbacks.

**Architecture:** Extend the existing claim-owned acceptance driver. Use its smoke/snapshot evidence and read-only PostgreSQL/ObjectStore loading for expected identities, then query the real external read APIs. Root alone owns Docker lifecycle; the verifier never inserts telemetry or re-exports Langfuse observations.

**Tech Stack:** Go, PostgreSQL, Docker Compose, existing pinned Alloy/Loki/Prometheus/Grafana/Langfuse.

## 📌 Global Constraints

- PostgreSQL and Trace JSONL/ObjectStore remain authoritative.
- Raw prompt, source, tool payload, headers, environment, credentials and arbitrary exception messages MUST NOT be exported to Loki.
- Loki stream labels are fixed service/environment plus bounded component/level; IDs live only in the JSON line.
- No real provider, YHC, automatic recovery, schema migration, guessed Langfuse UI URL, retention changes or telemetry ingestion from the acceptance verifier.
- Reuse `FragmentLoader`, `FragmentCompiler` and Langfuse `Client` to obtain exact published identities; never reproduce identity hashing.
- Physical evidence must come from fresh controlled Gateway-created Worker runs, not seeded terminal rows or fabricated safe events.
- Full G4 remains open. G3 closes only after real corresponding results are verified; prior monitoring proof remains separately attributed.
- Existing main-checkout changes, prior claims, and all data volumes are preserved. Only exact new claim-owned resources may be started/stopped.

## ✍️ Implementation

### Task 1: Claim-bound cross-backend correlation verifier

**Files:**
- Create: `test/acceptance/observability/correlation.go` and `correlation_test.go`; split focused query/readback helpers into `correlation_queries.go` if needed.
- Modify: `test/acceptance/observability/driver.go`, `environment.go`, `driver_test.go`, `scenario.go` for fresh telemetry credentials/ports and a separately named healthy correlation snapshot.
- Fix discovered native startup defect: `deploy/local/compose.yaml` explicitly runs Alloy as `473:473`, matching its data-directory owner without adding capabilities; cover with architecture and fresh-volume native regression tests.
- Report: `.superpowers/sdd/g3-correlation-task-1-report.md`.

**Interfaces:**
- Consume claim-owned `acceptance.env`, `correlation-snapshot.json`, `OBSERVABILITY_DATABASE_URL`, the exact two completed Turns and verified fragment proofs.
- Add mode `correlate` invoked after smoke/correlation-snapshot; derive all backend origins/credentials from the verified claim, not arbitrary CLI URLs. `correlation-snapshot` reuses existing source/readback checks but has its own exclusive output; the original `snapshot`/`smoke-passed.json` cold-start gates must remain unchanged.
- Produce exclusive mode-0600 `correlation-passed.json` only after every check succeeds; report identity/count/digest/time/query summaries, never raw model content or credentials.

- [x] **Step 1: Add failing read-API behavior tests.** Drive the public `correlate` helpers with `httptest` external servers and known literal responses. A valid two-scope response passes; wrong Run/Turn, extra identity labels, malformed/status-error/empty positive response, nonempty crossed-scope response, truncated response, remote redirect and Langfuse mismatched identity/metadata/parent/usage fail. Each HTTP client is bounded and rejects redirects. Record the first meaningful failing command before implementing.

```bash
GOCACHE=/private/tmp/Switchback-supplements-go-build go test ./test/acceptance/observability -run Correlation -count=1
```

- [x] **Step 2: Implement the bounded verifier.** Load exact claim via existing validation; read the two completed Turn scopes and sealed fragments using capped read-only pools and existing source verification. Compile verified frames with the production compiler to identify trace/span/generation IDs. Query existing Langfuse read APIs only, and compare exact identity/provenance, parent relationships and controlled usage against the source. Query Loki through the claim Grafana datasource proxy, authenticating with fresh synthetic Grafana credentials. For each scope require `turn_terminal` and corresponding `run_terminal` with correct success code and exact safe schema. Query both crossed Run/Turn combinations and require successful empty responses. Decode every returned JSON event strictly and validate the exact four stream labels. Use a fixed bounded query window derived from source timestamps, never a moving unlimited range. Poll eventual availability within an overall deadline, but never suppress malformed/error/collision responses as success. Query real Prometheus for `up` and sample availability plus completed Run/Turn counts covering the same fresh workload; aggregate metrics are not exact-ID labels.

- [x] **Step 3: Add claim-safe wiring.** Add freshly allocated Grafana/Prometheus ports and synthetic Grafana password to the existing fresh-environment generator. Do not publish Loki or Alloy ingress. Existing smoke/matrix behavior remains compatible. Add tests for required generated values and exact URL/claim matching. All externally sourced response data remain untrusted and bounded.

- [x] **Step 4: Run covering tests and self-review, then commit.**

```bash
GOCACHE=/private/tmp/Switchback-supplements-go-build go test ./test/acceptance/observability -count=1
GOCACHE=/private/tmp/Switchback-supplements-go-build go test -race ./test/acceptance/observability -run Correlation -count=1
git diff --check
```

### Task 2: Root-owned physical execution and review

- [x] Build fresh source-pinned Backend and controlled Worker images in a new exclusive claim. Start only the claim stack with production observability configs, standard migrations and synthetic keys; verify service ownership and loopback ports.
- [x] Execute existing smoke, separate correlation-snapshot, then correlate. Retain exact source/image IDs, command exits and evidence hashes. Do not repair a failed claim by editing authoritative rows, manually sending logs, or force-marking projection jobs.
- [x] Independently review Task 1 diff plus root physical evidence; repair findings with fresh covering tests. Run scoped production/acceptance regression and strict OpenSpec validation.
- [x] Stop/remove only exact owned containers/networks, preserving data volumes and private evidence. Update the G3 ledger and prepare evidence for the existing stage Draft PR; publish after the final local commit with an exact source/publication tree mapping. Do not claim remote CI or merge from empty checks.

## 🔗 References

- [Approved contract](../../../openspec/changes/local-observability-stack/specs/local-observability-stack/spec.md)
- [Completed monitoring evidence](../../../.superpowers/sdd/g3-final-verification.md)
