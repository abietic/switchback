# Local observability stack

## Why

The durable Langfuse projection is complete, but operators still lack production metrics, safe searchable runtime diagnostics, and real local dashboards/alerts. This implements slice 2 of the user-approved [observability design](../../../docs/superpowers/specs/2026-09-05-observability-and-yhc-harness-design.md), without changing execution authority.

## What Changes

- Add background-sampled, bounded-label Prometheus metrics and an internal scrape endpoint.
- Add typed safe JSON events, a bounded asynchronous Alloy sender, explicit loss counters/windows, and Backend-owned sanitized Worker output collection.
- Provision pinned local Prometheus, Alloy, Loki and Grafana services, four dashboards and local alert rules.
- Verify collection/query, scope-preserving diagnostic links, outage isolation, and seven-day retention with physical evidence.

## Capabilities

### New Capabilities

- `local-observability-stack`: derived metrics/logs/dashboards and non-blocking local collection contracts.

### Modified Capabilities

None. Task, WorkerRun, AgentTurn, Trace, ArtifactRef and governed context authority remain unchanged.

## Impact

Touches `internal/platform/telemetry`, `internal/localproduct`, Backend-owned runtime log collection, `deploy/local`, acceptance tooling and necessary production `web/` diagnostic links. Adds the released Prometheus Go client. No database migration or raw prompt/tool/source logging is planned; existing durable observation frames remain immutable. Docker Socket remains Backend-only. Tool/MCP schema expansion and optional YHC ACP integration remain subsequent slices, not acceptance claims of this change.
