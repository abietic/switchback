# Local Backend telemetry

PostgreSQL and Trace JSONL/ObjectStore remain authoritative. Telemetry is optional derived data; a successful Alloy push proves receiver acceptance only, not Loki durability.

## Configuration and isolation

- `SWITCHBACK_TELEMETRY_ADDRESS` defaults to empty (disabled). Set an explicit host and numeric port, e.g. `0.0.0.0:9091` inside Compose. The separate listener serves only `GET /metrics`; the product router does not expose it. Do not publish this internal port or route it through the product web proxy.
- `SWITCHBACK_ALLOY_PUSH_URL` defaults to empty (disabled). Supply the exact HTTP(S) push path, e.g. `http://alloy:12345/loki/api/v1/push`. Userinfo, query strings, fragments and invalid ports disable the sender. Redirects are not followed.
- Invalid listener/sender configuration, a bind failure or unavailable known-token scanner disables only that telemetry component. None is a readiness dependency or an App error. Diagnostics contain fixed codes, never the address, DSN, provider errors or secrets. A working listener survives sender/scanner failure.
- Runner construction and scrapes perform no external I/O. When either component is active, a background sampler owns a separate PostgreSQL pool (`MaxConns=2`, `MinConns=0`, `MinIdleConns=0`), samples immediately and every 15 seconds, and uses a two-second total deadline and read-only repeatable-read transaction. Core dispatch does not call the sampler or sender.
- Cancellation stops the sample loop, closes its pool, cancels sender work, and closes the listener. HTTP headers have a two-second deadline; shutdown permits one second before forcing HTTP connections closed.

## Metric contract

All metrics include constant `service="switchback-backend"` and `environment="local"`. No tenant/user/workspace/session/run/turn IDs, profile IDs/revisions/digests, raw errors, URIs or free text are labels. SQL normalizes dimensions before grouping and returns at most 20 terminal groups and 28 state groups; exceeding those bounds rejects the entire sample.

| Family | Unit and ownership | Finite dimensions |
| --- | --- | --- |
| `switchback_worker_run_terminal` / `switchback_agent_turn_terminal` | Absolute count of committed terminal primary-key rows; Run ownership comes from WorkerSession, Turn must match its owning Run and Session | `status=completed,failed,cancelled,timed_out,unknown`; `harness=codex,unknown` for current registry |
| `switchback_worker_run_duration_seconds` / `switchback_agent_turn_duration_seconds` | Cumulative histogram of `terminal_at - started_at` in seconds, only when both timestamps exist and are ordered | Same status/harness; bucket bounds are 0.1, 0.5, 1, 2, 5, 10, 30, 60, 300, 1800, 86400 seconds |
| `switchback_session_state` | Session count in configured tenant/user scope | `status=allocating,interactive,running,recovering,waiting_user,idle,lost,expired,closed,unknown` |
| `switchback_runtime_state` | All persisted RuntimeInstance rows belonging to scoped Sessions, including historical generations | `status=starting,active,suspect,unhealthy,draining,terminated,lost,unknown`; this is lifecycle state, not a derived health assessment |
| `switchback_queue_state` | Queued Task count owned by configured tenant/initiator; two disjoint groups | `status=capacity_wait` means `allocation_wait_reason=session_capacity`; `queued` is every other queued Task. Not continuity quota slots, free capacity or SessionProcess count |
| `switchback_candidate_projection` | Candidate primary-key count, scoped by its tenant/user and verified Session/Run joins | `status=pending` combines pending/running/retryable; `projected` means published; `failed` means blocked; otherwise unknown |
| `switchback_fragment_projection` | Fragment job primary-key count, scoped through its exact candidate identity/digest; each projection version is a separate job | `status=pending` combines pending/running/retryable; `projected` means passed; `failed` means blocked; otherwise unknown |
| `switchback_candidate_oldest_pending_age_seconds` / `switchback_fragment_oldest_pending_age_seconds` | Age of oldest created pending/running/retryable row; absent if there is no such row | No variable labels |

Only an exact AgentProfile ID/revision/digest matching `ProductionProfileRegistry` resolves a known harness. Both current Kimi and DeepSeek model profiles use `codex-acp`, so both are `codex`. Unknown, historical or mismatched profiles are `unknown`; model vendor is not harness identity. The core API reserves other finite harness kinds but this sampler does not infer them.

Run counts do not join Turns or projection jobs, so multiple Turns, model calls, replayed evidence and multiple projection versions cannot inflate Run counts. Repeated samples replace absolute values; they never increment on replay. Terminal counts are gauges reconstructed from retained authority. Duration histograms are also reconstructed: authoritative retention, database reset or changed scope can reduce counts/sums. Do not treat them as durable billing counters or infer activity solely from their rate across resets. A Backend restart reconstructs the same retained values.

No quota, model cost, token totals or TTFT are invented. `switchback_model_measurement_available{measurement="ttft|cost|usage"}=0` explicitly marks unsupported canonical coverage. Missing starts do not become zero-duration observations. Empty queue aggregates are measured zeros; absent duration/backlog observations remain absent.

## Staleness and log loss

Before the first successful sample, `switchback_telemetry_sample_available=0`; sampled domain families are absent. Failure retains the entire last successful snapshot, sets `switchback_telemetry_sample_current=0` and increments `switchback_telemetry_sample_failures_total`. Use `switchback_telemetry_sample_last_success_timestamp_seconds` to determine age. A live `/metrics` target does not prove that authority is currently readable. Successful sampling restores current=1; partial results are never published.

Only typed `backend_started`, `backend_stopped`, `sample_failed` and `sample_recovered` events originate from this runner. Sample events occur on failure/recovery transitions; raw SQL/provider exceptions and arbitrary slog are not forwarded. The sender reuses the runtime known-token scanner in its background worker, with no scanner/network call during enqueue. Empty or unusable scanner registries disable logs while leaving metrics available. Backend stop is best effort and may be counted as shutdown loss.

The sender has a 1,024-record queue, 4 KiB maximum event, at most 64 events per HTTP push, a one-second send deadline, no disk spool and no retry. Loki stream labels are only service/environment/component/level. Inspect `switchback_telemetry_dropped_total{reason="invalid|queue_full|scan_failed|remote_rejected|transport_failed|shutdown"}`, first/last loss timestamps, queue depth and receiver-accepted counts. After recovery, `log_gap` summarizes loss without reconstructing missing events. These in-process health/loss counters reset on Backend restart; silence never proves that nothing happened.

## Local stack operations

### Worker diagnostic navigation

The production Worker Runtime sidebar provides read-only diagnostic links independently of command controls. `SWITCHBACK_GRAFANA_BROWSER_BASE_URL` is empty/disabled outside Compose; Compose defaults to `http://localhost:${SWITCHBACK_GRAFANA_PORT:-3001}` and honors an explicitly empty value. Only an HTTP(S) loopback origin (`localhost`, IPv4 loopback, or IPv6 `::1`), optionally with a port, is accepted. IPv4-mapped IPv6 spellings such as `[::ffff:127.0.0.1]` and `[::ffff:7f00:1]` are rejected to keep server availability consistent with the browser client. Paths, credentials, query strings, fragments, remote hosts and internal service names such as `grafana` are rejected. Invalid configuration disables links with `invalid_config`; it never prevents Backend startup/readiness. `deploy/local/bootstrap-env.sh` generates the ignored `.env`; no credential or extra env template is needed for these optional values.

`GET /api/v1/views/worker_detail/{session}/diagnostics` accepts optional `worker_run_id` and `agent_turn_id` (Turn requires Run). It reads current Session/Run/Turn rows together, verifies the fixed Frontier Tenant/User plus the current server-configured Workspace, and verifies both relationships. Unknown or out-of-scope targets return 404 even if a projection still exists; authorized closed/expired Sessions remain readable. Duplicate, unknown, malformed or incomplete query parameters return 400. Responses use `private, no-store`. This is M1 fixed-profile authorization, not dynamic Membership/RBAC revocation; ADR 0058 defers that service.

The response contains the canonical authorized `target`, `links: [{kind,url}]`, `grafana_status: configured|disabled|invalid_config` and `langfuse_status: disabled|invalid_config|identity_unavailable|pending|blocked|unavailable|historically_verified`. Disabled/invalid Grafana removes only Grafana/Logs links. `configured` means a usable browser origin, not verified service health. Link construction makes no Grafana/Loki/Langfuse network requests. If a service is stopped, navigation may fail while Worker execution remains available.

The Grafana link opens the existing **Worker Runtime aggregate overview**, with the authorized target's stored start/end window padded by one minute (ongoing targets end at database current time). Metrics have no Session/Run/Turn ID labels; this is not an exact Run metrics view. Logs open Grafana Explore with JSON-path aliases for the verified Tenant/Workspace/Session and optional Run/Turn. User ownership is checked in PostgreSQL; safe log envelopes deliberately have no `user_id`. Session-wide suppressed container output has no Run/Turn attribution and therefore does not appear under a Run/Turn filter. These links follow the checked-in dashboard Explore format (`schemaVersion=1`, JSON `panes`, datasource `switchback-loki`).

Langfuse navigation uses `SWITCHBACK_LANGFUSE_BROWSER_BASE_URL` and server-owned `SWITCHBACK_LANGFUSE_PROJECT_ID`, both empty outside Compose. The origin follows the same loopback validator; the project is one canonical `[A-Za-z0-9_-]+` path segment, at most 512 characters. Compose shares one `langfuse-project-id` YAML anchor with `LANGFUSE_INIT_PROJECT_ID`, whose single default is `${LANGFUSE_INIT_PROJECT_ID:-switchback-m1}`. Change that init value and public project together; an existing Langfuse database is not reconfigured or proven by changing this value. The browser default is `http://localhost:${SWITCHBACK_LANGFUSE_PORT:-3000}`, honoring an explicitly empty browser value; clear both public values to disable links. Existing `LANGFUSE_BASE_URL` and API keys are internal ingest configuration and never enter this DTO.

After exact Session/Run/Turn authorization, the ArtifactTrace-owned read-only bridge proves the current source/digest and v3 passed job. A Session-only request does not guess the latest Run. Only `historically_verified` returns a Langfuse link and safe identity summary (version, trace ID, optional selected observation ID and source references/digests). The selected Turn requires its own passed proof. The proven browser route is `/project/{project}/traces/{trace}`; the Turn observation ID remains in the summary for inspection inside that trace, with no unverified query parameter. The UI states `身份曾核验；链接使用当前项目配置，未实时核对`: historical proof contains no persistent remote endpoint/project/credential identity and no fabricated verification time. `unavailable` means identity dependency read failure, not remote offline; Grafana/Logs remain usable and exceptions are not exposed.

Diagnostics default to Session scope. Explicit Report/Diff/Files Artifact selection reloads its canonical summary by `artifact_id`; a Turn-header action selects its canonical Run/Turn. The route retains either `artifact_id` or `worker_run_id` plus optional `agent_turn_id`, never both. Missing/malformed selections show unavailable, never latest or active fallback. Reload restores the selected Artifact or reauthorizes the exact historical Run/Turn even outside the first timeline page; URL references carry no permission. Target changes immediately discard old links and fence aborted/late replies. No link or proof is persisted in browser storage.

Grafana/Langfuse are local operator tools with their own authentication. Generated links authorize the Switchback target read; they are not row-level authorization inside Grafana. Do not expose these loopback operator interfaces as a multi-tenant product surface. The UI opens links with `noopener noreferrer` and no referrer, rejects unsafe external destinations, and immediately discards links when Session/Run/Turn changes. Native query results, alerts, Langfuse correlation, all-services-absent cold start and physical retention expiry remain separate acceptance work.

Run `make local-env` first. Bootstrap adds `SWITCHBACK_GRAFANA_PASSWORD` to the ignored local `.env` only when absent; an existing password is preserved. Grafana consumes it only when initially creating its admin account. Changing the environment later does not reset an existing Grafana database password.

```sh
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml config --quiet
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml up -d prometheus loki alloy grafana
```

Grafana is at [localhost:3001](http://localhost:3001), user `admin`; retrieve its password privately from the ignored env file. Prometheus is at [localhost:9091](http://localhost:9091). Override host ports with `SWITCHBACK_GRAFANA_PORT` / `SWITCHBACK_PROMETHEUS_PORT`. Only these operator endpoints publish loopback ports. Loki and Alloy remain internal; Backend's metrics listener on container port 9091 is separate from Prometheus's host port.

Backend has no telemetry `depends_on` or readiness edge. Its optional metrics/sender endpoints are wired into the observability network; core dispatch, settlement, PostgreSQL and Trace authority do not depend on these services. To disable an endpoint explicitly, use a Compose override with an empty value (the base Compose `:-` defaults otherwise enable it).

Each service owns a named data volume and has dropped capabilities / no-new-privileges; only Backend owns the Docker Socket. None receives provider, PostgreSQL or Langfuse credentials. Prometheus scrapes every 15 seconds and retains seven days. Loki uses filesystem TSDB v13 with a 24-hour index period and 168-hour compactor retention; the persistent Loki volume includes compactor markers. Retention is asynchronous, not a disk quota or a seven-day deletion SLA. Do not delete existing data volumes to upgrade or diagnose telemetry.

Alloy receives pushes at `:12345/loki/api/v1/push`; its separate diagnostics listener is `:12346/metrics`. Incoming event timestamps are preserved. The writer has 64 active streams maximum, 256 KiB batches, a one-shard 1 MiB queue, drop-on-overflow, two retries with 100–500 ms backoff, a two-second request deadline and a three-second shutdown drain. The pinned Alloy version requires `--stability.level=experimental` for queue_config; revalidate this contract before upgrading. Its volume is runtime storage, not a configured WAL: queued events can be lost on restart. Monitor `loki_write_dropped_entries_total` and `loki_write_dropped_bytes_total` alongside Backend loss counters. HTTP 204 proves receiver acceptance; a later Loki query proves queryability at that instant, not durable authority.

## Dashboards and local alert semantics

Provisioning is read-only for the two datasource UIDs `switchback-prometheus` and `switchback-loki`, and for four dashboards: Platform Health, Worker Runtime, Harness & Model, Projection Health. Model TTFT/cost/usage remain explicitly unavailable. Terminal totals are absolute retained-row gauges; dashboards do not apply rate() to them. The duration means describe retained terminal observations, not current request latency or TTFT.

Dashboard Explore links carry the selected time range and explicitly extract the typed Event field with `| json run_id="scope.worker_run_id" | run_id = "${run_id}"`. Here `run_id` is a query-time alias for the nested `scope.worker_run_id`, never an indexed identity label or a top-level Event field. Enter only safe IDs containing letters, digits, underscores and hyphens. Committed Run/Turn terminal events carry their exact persisted Run identity. Container output metadata carries only Session/Runtime scope, so a Run query deliberately excludes it. Loki automatic service-name discovery is disabled with `limits_config.discover_service_name: []`, keeping exactly service/environment/component/level indexed. Query-extracted JSON fields are not indexed labels.

| Rule | Meaning and pending window |
| --- | --- |
| Backend target down | Missing/down scrape target for 2 minutes; does not diagnose the underlying cause |
| Telemetry records dropped | Combined Backend/Alloy loss in the last 5 minutes, pending 1 minute; requires both scrape targets up and both counter families present with enough samples. Missing collection yields NoData, never an invented zero; reset/restart can hide earlier loss |
| Authority sample stale or unavailable | Never sampled, latest attempt failed, or last success older than 60 seconds, pending 2 minutes |
| Projection backlog sustained | Either oldest pending family exceeds 300 seconds for 5 minutes; absent ages with a successful sample mean no pending rows, but absent sampling remains unavailable |
| Runtime or session needs inspection | Lost current Sessions or suspect/unhealthy Runtime rows for 5 minutes; historical lost/terminated Runtime generations are excluded |

All rules explicitly use `noDataState=NoData` and `execErrState=Error`; they do not convert query failure to success. Grafana 13.2.2's built-in `empty` receiver has no notification integrations; the provisioned policy routes there, with no external contact point or automatic remediation. If an operator deletes that receiver, restore the local Grafana configuration deliberately; do not add a placeholder external destination. These dashboards are local single-user operational aids, not production HA, SLO, billing or complete model observability.

Projection and runtime alerts require sample_available=1, sample_current=1 and a last-success timestamp at most 60 seconds old. Retained measurements remain useful for dashboard diagnosis, but cannot complete an alert's pending window or establish current health after sampling fails. A fresh successful empty sample is a legitimate zero; missing, failed or stale sampling becomes NoData.

Reproduce alert semantics without starting the product:

```sh
SWITCHBACK_OBSERVABILITY_NATIVE_TEST=1 GOCACHE=/private/tmp/Switchback-supplements-go-build go test ./internal/architecture -run TestLocalObservabilityPromQL -count=1 -v
```

This explicit native gate uses the cached digest-pinned Prometheus image, no network, read-only generated fixtures and temporary storage. It extracts actual provisioned expressions and checks 23 scenarios. Without the opt-in environment variable, ordinary Go tests report this test skipped and need no Docker daemon. There are no new Go dependencies.

The always-on `TestLocalObservabilityExploreEnvelopeContract` decodes all four delivered URLs against the real `telemetry.Event` JSON shape. With an already running isolated Alloy/Loki fixture and a task-owned wget-capable client on that network, run:

```sh
SWITCHBACK_OBSERVABILITY_LOG_CLIENT=switchback-obsv-stack-0929-prometheus-1 GOCACHE=/private/tmp/Switchback-supplements-go-build go test ./internal/architecture -run TestLocalObservabilityExploreNative -count=1 -v
```

The opt-in native test marshals two actual `telemetry.Event` values with distinct `Scope.WorkerRunID` fields, pushes them through Alloy, executes each delivered Explore query and verifies the exact matching timestamp/body while excluding the other Run. It also checks exactly four indexed labels. These are synthetic typed-envelope fixtures, not evidence of production Worker log wiring. Without the client variable, this native test explicitly skips.

## Pinned upstream evidence and validation boundary

Verified releases on 2026-09-29, Docker Hub multiarch index digests (linux/amd64 and linux/arm64 children), and native linux/arm64 versions:

| Release | Multiarch index digest |
| --- | --- |
| [Prometheus v3.15.0](https://github.com/prometheus/prometheus/releases/tag/v3.15.0) | `sha256:efd719c99d83b060d9daefdcf00360461adf279f45ef5391f8d111892118753e` |
| [Loki 3.7.8](https://github.com/grafana/loki/releases/tag/v3.7.8) | `sha256:1107dd5274e0ada47e42472b7a7e71f3b2a2fe878878108f3e2f9e51528f0193` |
| [Grafana 13.2.2](https://github.com/grafana/grafana/releases/tag/v13.2.2) | `sha256:ac461fb352abc50da10a51c7d02462e9c05488f11f53f14b3ad79a8145f638a0` |
| [Alloy v1.20.0](https://github.com/grafana/alloy/releases/tag/v1.20.0) | `sha256:f111cce835516c5f99166342be7038496b52ced16667be5a11e19258a3e4cd30` |

Registry evidence: [Prometheus tag metadata](https://hub.docker.com/v2/repositories/prom/prometheus/tags/v3.15.0), [Loki](https://hub.docker.com/v2/repositories/grafana/loki/tags/3.7.8), [Grafana](https://hub.docker.com/v2/repositories/grafana/grafana/tags/13.2.2), [Alloy](https://hub.docker.com/v2/repositories/grafana/alloy/tags/v1.20.0). Configuration references: [pinned Alloy writer](https://github.com/grafana/alloy/blob/v1.20.0/docs/sources/reference/components/loki/loki.write.md), [Loki retention](https://grafana.com/docs/loki/latest/operations/storage/retention/), [Grafana empty default receiver](https://github.com/grafana/grafana/blob/v13.2.2/pkg/setting/setting_unified_alerting.go).

Native acceptance runs `promtool check config`, Loki `-verify-config`, Alloy `validate --stability.level=experimental`, and isolated Grafana startup plus health/datasource/dashboard/rule APIs. Use synthetic fixture envs and task-owned tmpfs overrides; start only the four monitoring services with `--no-deps`. Preserve existing volumes. An infrastructure-only run intentionally has a down Backend target and must not be reported as healthy business execution.

G3 collection/query/dashboard/alert and exact scoped correlation are recorded in the completed change tasks. G4 has separate cold-start, paired outage, loss/recovery and actual retention gates; see the [current implementation tasks](../../openspec/changes/local-observability-stack/tasks.md). The controlled Worker output collection described below does not by itself close these gates, real aged-data deletion, or long-term durability.

## Backend-owned Worker observations

When the safe sender is enabled, the optional telemetry runner uses the same private maximum-two-connection pool for Worker discovery and committed terminal observations. No execution-side hook, readiness dependency, settlement query, domain counter increment or durable telemetry ledger is added. PostgreSQL operations use read-only transactions and a two-second deadline including pool acquisition. The Backend alone accesses Docker; neither Alloy nor a Worker needs the Socket.

Discovery runs every five seconds, selecting at most eleven configured tenant/user candidates to detect excess, admitting at most ten concurrent readers (closing readers retain their slots). It requires the Session's current Runtime identity/generation, a nonterminal unexpired Session, active Runtime, exact server-recorded container ID, verified server Agent/resource/network profiles and matching immutable Docker labels. The Runtime's Run reference must belong to that Session; it is used only for immutable AgentProfile verification. The container's creation-time Run label is ignored because a resident container can serve later Runs. All output events therefore have Session/Runtime ID and generation, never guessed Run/Turn attribution.

Each real Docker HTTP stream follows with `Tail="0"`, stdout and stderr enabled, timestamps/details disabled. Authority is fresh for at most seven seconds from discovery start. A separate expiry timer cancels and closes the real stream even if a later discovery is blocked. Replacement, terminality, label mismatch or failed authority cancels old readers. Retry starts at the current tail, without replay. Shutdown allows two seconds for all readers together; a boundary that fails to close is disclosed as a shutdown gap. No reader slot is reused before its old reader completes.

Raw bytes never leave the fixed discard buffer. Multiplex headers are eight bytes; declared frames over 1 MiB, unknown sources, reserved-header changes and truncated frames stop collection. TTY bytes use the finite `combined` source. Buffers are 16 KiB; rate debt includes headers and caps consumption at 256 KiB/s per reader with a single fixed-chunk burst. Rate waits are cancellable. Numeric byte totals are flushed every second (and once on termination) as `runtime_output_suppressed`, with only `stdout`, `stderr` or `combined` source. These totals describe consumed bytes, not full process output or a complete byte-loss estimate.

Run/Turn semantic events are read every five seconds from committed terminal rows. They include finite outcomes and duration only when both valid timestamps exist; content, terminal reason/error strings and model fields are never selected. Startup excludes older history. The lookback is sixty seconds, returning at most 1,025 rows to detect overflow and emitting at most 1,024 with a maximum-1,024-entry in-memory dedup window. Late commits are observable only while their terminal timestamp remains in that window. Restart, overflow, a longer outage or a queue drop can lose events; there is no durable backfill or exactly-once log guarantee. Terminal metrics continue to replace aggregate values from unique committed rows, independently of logging or replay.

`switchback_telemetry_worker_readers` and `switchback_telemetry_worker_authority_current` show admission/authority health. `switchback_telemetry_worker_gaps_total` uses only `authority`, `capacity`, `stream`, `frame`, `shutdown`, `lifecycle_overflow`, `lifecycle`, `unavailable`; each value counts a collection gap, not lost bytes. Expected cancellations can count stream gaps. Sender queue/drop/timestamp measurements remain separate. The existing sender remains 1,024 events, 4 KiB/event, 64/request, one-second HTTP timeout, no spool and no retry/backfill guarantee. An empty successful discovery means no eligible readers, not proof that every process is covered.

Controlled native verification (explicit opt-in; ordinary test runs skip it):

```sh
SWITCHBACK_TEST_DATABASE_URL='<owned-test-database-url>' \
SWITCHBACK_TEST_WORKER_TELEMETRY_DOCKER=1 \
SWITCHBACK_TEST_WORKER_ALLOY_URL=http://127.0.0.1:59632/loki/api/v1/push \
SWITCHBACK_TEST_WORKER_LOKI_URL=http://127.0.0.1:59631 \
DOCKER_HOST='<local-docker-endpoint>' \
GOCACHE=/private/tmp/Switchback-supplements-go-build \
go test -race -tags=integration ./internal/localproduct -run '^TestTelemetryWorkerDockerNative' -count=1 -v
```

Start owned digest-pinned Alloy/Loki fixtures with the checked-in configs first; Alloy needs the checked-in `--stability.level=experimental` argument. The test uses the existing pinned Alloy image solely as a shell output fixture, with no network, mounts, privileges, provider or Agent. It creates and removes only its exact output-container IDs and never removes volumes. The production collector and sender must deliver numeric stdout/stderr observations from two real Runtime generations to Loki, with the sentinel absent and the old real HTTP reader closed. This proves this collection seam only; no real model, alerts/Langfuse correlation, cold-start outage matrix or seven-day expiry is exercised.

## 🔍 Controlled full-stack paired outage acceptance

The additive `test/acceptance/observability` modes `run-full-stack-matrix` and `verify-full-stack-matrix` test nine services, including Prometheus, Alloy, Loki, Grafana and the five Langfuse services. They do not replace the legacy five-service report or the cold-only verifier.

Only the operator owning a fresh synthetic claim runs the matrix. Prepare its private `acceptance.env`, exact Backend/controlled Worker images, dedicated migrated database and verified `smoke-passed.json`; start all nine services before invoking the runner. Use the fixed base Compose file plus the acceptance override. The Backend URL and database credentials/port/name must match that claim. Do not use a normal development or production project.

```sh
# Variables refer to the exact newly prepared claim, never a shared environment.
go build -o "$obsv_root/driver" ./test/acceptance/observability
"$obsv_root/driver" -mode run-full-stack-matrix \
  -project "$obsv_project" -output "$obsv_root" -backend "$obsv_backend"
"$obsv_root/driver" -mode verify-full-stack-matrix \
  -project "$obsv_project" -output "$obsv_root"
```

The runner uses real Gateway commands for twenty healthy/outage pairs, two Turns per measurement, with a five-minute bound per state. It stops only the nine observation services, restores them and verifies the original immutable sources. Each measurement has one continuous Docker event subscription covering the exact project and server-verified resident Worker; readiness is independently probed. Initial/final Backend, PostgreSQL, fixture and Worker identities are retained. The verifier rejects impossible creation/ACK ordering, lifecycle changes, missing services, stitched subscriptions, duplicate Turns, changed sources, any transition over five seconds or per-metric outage p95 increase over one second.

A failure leaves partial evidence and the start marker; use a fresh claim rather than overwrite or resume it as a successful run. The runner never cleans up resources automatically. After separately collecting loss/recovery queries, stop and remove only the exact owned containers/networks and synthetic secret volume; preserve data volumes and private evidence. `full-stack-matrix-passed.json` is explicitly `paired_outage_only`: it does not certify log backfill, retention expiry, real Codex/provider or YHC execution.
