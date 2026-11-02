# Local observability stack design

## Context

This implements slice 2 of the approved September 5 design. PostgreSQL and Trace JSONL/ObjectStore remain authoritative; durable Langfuse projection was closed at `f0f73c0`. Existing Backend readiness and execution must not depend on any telemetry backend.

## Goals / Non-Goals

Goals: real metrics/log collection, bounded resource use and cardinality, actionable local dashboards, explicit collection gaps, and G3/G4 physical evidence.

Non-goals: production HA, distributed tenancy, billing from Prometheus, full-text source/prompt indexing, raw Worker stdout export, external notification/remediation, Tool/MCP schema changes, YHC execution, and automatic recovery enablement.

## Decisions

### Snapshot metrics, not scrape-time queries

Use a private Prometheus registry and immutable snapshots. A separate PostgreSQL pool (maximum 2 connections) samples every 15 seconds with a 2-second total deadline and read-only queries. Scrapes only read the snapshot and in-memory process counters. Failures retain the last good snapshot and expose availability/last-success; they never substitute healthy zero values.

Run/Turn terminal counts and duration histograms are derived from unique committed terminal rows. Sampling replaces cumulative values rather than incrementing on load/replay. This avoids an unbounded in-process event-ID dedup map or a second durable telemetry ledger. Counters can reset when authoritative retention removes rows; audit/billing consumers must use authority, not these derived counters. SQL aggregation bounds returned series, while query deadlines bound scan cost; larger deployments may require a separately reviewed incremental projection.

Metric labels are family-specific bounded enums. Unknown values collapse to `unknown`. Harness is resolved from verified AgentProfile identity/revision/digest, not a client string. No tenant/user/workspace/session/run/turn ID, digest, revision, sequence, URI, free text or exception message is a label. Current canonical model frames have no TTFT/cost; these remain explicitly unavailable, not zero. No mutation of published frames to invent historical metadata.

### Safe logging as a separate typed interface

Do not mirror global slog or arbitrary maps. The safe event type has `ts/level/service/component/event/schema_version` plus validated scope identifiers, numeric durations/counts and enumerated codes. It cannot contain message, headers, environment, prompt, tool arguments or source. Loki stream labels are fixed service/environment plus bounded component/level; IDs live only in the JSON line. Server-known secret scanning is defense in depth; arbitrary raw values never become safe by key name alone.

The sender has a 1,024-event queue, 4 KiB maximum serialized event, maximum 64 events per request, a 1-second HTTP deadline, no redirects, no persistent spool and no retry loop holding execution. Enqueue never waits for network or capacity. Invalid, full, rejected and timed-out events increment finite-reason loss counters and first/last loss timestamps. After downstream recovery, emit a safe gap summary; acceptance by Alloy is not proof of durable Loki storage. Shutdown cancels in-flight sending and accounts for discarded queue contents. Disable/misconfiguration degrades telemetry only.

### Backend owns Worker collection

Only Backend accesses Docker Engine. Select exact managed runtime identity/generation after validating server labels; cancel readers on runtime replacement/terminality. Never export raw stdout/stderr. Unknown bytes yield bounded length/source metadata only; safe semantic events come from trusted Backend state. Do not give Alloy the Docker Socket, host log directory or provider credentials.

Worker Run/Turn terminal timestamps are authored by Backend completion services. The optional terminal observer uses that same Backend clock for its startup cutoff, 60-second lookback and query upper bound; PostgreSQL transaction time is not the event-time ceiling. Production defaults to `time.Now`, with an internal immutable clock seam for regression tests. This is an event-time-based, best-effort view of committed rows, not strict commit-order collection or a guarantee across multiple Backends with differing clocks. Startup/restart excludes history, and late commits outside the window, overflow or outages can still lose logs. Snapshot counters and the authoritative terminal writer are unchanged; no database migration or durable telemetry cursor is introduced.

### Local infrastructure and diagnostics

Prometheus scrapes every 15 seconds and retains 7 days. Loki uses filesystem TSDB/schema v13 and 168-hour compactor retention. Alloy receives the Backend push protocol through `loki.source.api`, preserves incoming timestamps, and forwards to Loki.[^1] Ports published for operator access bind loopback only; internal ingest/scrape need no host port. Backend has no `depends_on` edge to these services. All images require a verified released version, multi-architecture manifest and immutable digest.

Grafana has real Prometheus/Loki datasources and Platform Health, Worker Runtime, Harness & Model, Projection Health dashboards. Local rules cover target down, log loss, sustained projection backlog, runtime anomalies and no-data/error. Links use configured local bases and escaped scope/time parameters; target authorization still applies. Production UI gets necessary diagnostic links only; `web-demo` is not a product implementation.

The approved 2026-10-11 diagnostics slice adds a separate public loopback Langfuse browser origin and canonical project segment. Compose shares the public project and init project through one anchor. Exact current Session/Run/Turn authority is read before the ArtifactTrace navigation bridge; Session-only requests never infer a Run. Only historical source/version/digest proof returns `historically_verified`, an exact trace route, and a safe identity summary. That proof does not bind the current endpoint/project/credential or health, and the page states `身份曾核验；链接使用当前项目配置，未实时核对`. Optional config failure and identity read failure retain other legal links; no remote call, raw proof, key, exception or invented verification time enters the HTTP response. Current same-project readback remains physical acceptance work.

## Risks / Trade-offs

- Aggregation competes with core PostgreSQL → separate capped pool, read-only transactions, deadline, last-good snapshots, availability gauge.
- Best-effort logs can be lost → bounded queue and explicit loss windows, no claim of retrospective backfill.
- Snapshot state is not an event history → terminal counts from committed rows; real lifecycle events tested separately.
- Minimum retention periods make immediate expiry nontrivial → use isolated timestamped fixtures and compactor evidence; configuration alone does not close retention acceptance.
- Unavailable model metadata can mislead dashboards → show unavailable coverage explicitly, do not render missing values as measured zero.

## Migration Plan

Deploy additive telemetry disabled by default outside the local Compose configuration. No schema migration. Roll back by disabling the sender/scrape listener and stopping only the newly owned telemetry services; preserve core PostgreSQL, authoritative objects and Langfuse data. Never remove volumes as a rollback shortcut.

## Open Questions

None requiring a new product decision. Exact image digests and physical retention mechanics are verification work, not assumed evidence.

[^1]: Grafana. Alloy loki.source.api reference. https://grafana.com/docs/alloy/latest/reference/components/loki/loki.source.api/
