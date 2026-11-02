# G3 Monitoring Acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the delivered Prometheus/Grafana scrape, dashboard queries and alert firing/recovery independently repeatable against native pinned services.

**Architecture:** Extend the existing architecture acceptance tests with an explicit opt-in isolated fixture. Exercise the production telemetry registry and delivered provisioning, not a second implementation of metrics or alert rules. This is the monitoring portion of G3; complete workload-scoped Loki/Langfuse correlation and G4 remain separate acceptance gates.

**Tech Stack:** Go testing, production telemetry registry, digest-pinned Docker images from local Compose, Prometheus and Grafana HTTP APIs.

## Global Constraints

- PostgreSQL and TraceManifest/ObjectStore remain authoritative; telemetry is derived and never gates execution.
- Preserve production rule expressions, evaluation interval and `for` durations. Do not shorten them to claim physical acceptance.
- No external notifications, paid provider requests, YHC enablement, migrations or production runtime behavior changes.
- Native fixtures use unique task-owned containers/network, loopback-only host ports, synthetic credentials, no ambient credentials and no user volumes. Delete only fixture-owned containers/network; preserve any created data volumes.
- HTTP clients have bounded timeouts and response sizes; poll under a bounded context. Fail on absent, malformed or unexpected data rather than treating it as healthy zero.
- Missing metrics and actual zero remain distinct. `NoData`/`Error` must not be reported as Normal.
- Keep OpenSpec 3.1 and 3.2 unchecked: this increment alone does not prove full G3/G4, Langfuse navigation or retention expiry.
- Work only in `.worktrees/observability-supplements-v1`; preserve unrelated main-checkout changes. Use `GOCACHE=/private/tmp/Switchback-supplements-go-build`.

### Task 1: Native monitoring acceptance and evidence

**Files:**

- Create `internal/architecture/local_observability_monitoring_test.go`: opt-in native integration, bounded HTTP/evidence assertions and small unit tests of negative response handling. If size warrants a fixture helper, use `internal/architecture/local_observability_monitoring_fixture_test.go` for owned Docker lifecycle only.
- Read `internal/platform/telemetry/metrics.go`, existing architecture observability tests and `deploy/local/observability/` provisioning.
- Create `.superpowers/sdd/g3-monitoring-task-1-report.md`: exact commands, source, outcomes and limitations; raw service responses stay in ignored/private evidence files.
- Production configs may change only for a reproduced acceptance defect, with regression coverage and unchanged approved semantics; report such a finding before expanding the code scope.

**Interfaces:** Consume the production telemetry registry's actual constructor/update/handler API discovered from source. Serve that registry on the fixture network using a test-owned endpoint, without adding a product endpoint. Read exact image pins and dashboard/rule queries from delivered files. Expose `SWITCHBACK_OBSERVABILITY_MONITORING_NATIVE_TEST=1` as the explicit native opt-in; ordinary tests skip only this physical test, not response-validator tests.

- [x] Step 1: Inspect current registry API and native fixture patterns; record selected seams in the report. Add focused negative tests for missing scrape samples, mismatched target identity, failed dashboard query frames and alert state mismatches. Assertions must reject empty results, HTTP errors and Normal-before-firing as a completed recovery proof.
- [x] Step 2: Run `GOCACHE=/private/tmp/Switchback-supplements-go-build go test ./internal/architecture -run Monitoring -count=1 -v`; retain expected RED evidence before implementation.
- [x] Step 3: Implement the smallest fixture and validators. Native test must start cached/pinned Prometheus and Grafana, serve a real production registry snapshot, verify successful scrape and current sample availability, load all four provisioned dashboards and execute their actual datasource query models through Grafana. Explicitly classify panels whose data is unavailable; never count HTTP 200 alone as valid rendered data. Verify all five rule definitions are loaded. Drive Backend target healthy -> unreachable -> firing -> reachable -> Normal with observed post-fault evaluation timestamps; don't accept a pre-fault Normal as recovery. Also demonstrate a delivered domain alert's missing-data state, separately from legitimate zero. Preserve production evaluation timing; maximum test duration 20 minutes with bounded per-request timeout. No actual browser rendering claim follows API query evidence.
- [x] Step 4: Run focused unit tests to GREEN, then native acceptance and `go test -race ./internal/architecture ./internal/platform/telemetry -count=1`. Record exact fixture IDs and safe cleanup. If native execution cannot complete, report the actual blocker rather than weakening assertions or writing a success artifact.
- [x] Step 5: Self-review, `git diff --check`, commit exact task files, and report status plus commit/test summary. Parent will independently review spec and code quality, run relevant final gates, and decide staged PR publication.

## Deferred acceptance

The registry snapshot is controlled input, not proof of a real Worker workload or the PostgreSQL sampler. Existing native Worker evidence is retained but not silently combined into full G3. Remaining work includes production workload -> scoped Loki -> exact Langfuse readback, all five alerts' scenario coverage where required, all-services-absent cold start, paired full-stack failures, loss/recovery, and actual seven-day expiry. This plan does not relabel earlier controlled G results as full G4.
