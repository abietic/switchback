# Tiered Trace Retention And Incremental Analytics

P0 keeps ObjectStore JSONL and Artifact payloads as authoritative evidence, indexed by PostgreSQL TraceManifest. DataLifecyclePolicy defaults to hot for 30 days, warm through 180 days, then cold indefinitely without automatic physical deletion. P0 local storage may implement these as logical states; later object-store adapters may map them to real storage classes without changing domain semantics. Langfuse remains a rebuildable projection with an independent, potentially shorter retention period.

Trace and IntegrationEvent ingestion emits normalized idempotent facts into TraceAnalyticsProjection for debugging, delivery, runtime, Tool, model, Artifact, cost, and Evolution-effect dashboards. User queries never scan raw JSONL to compute dashboards. Evolution locates evidence through TraceManifest and may asynchronously restore cold segments for replay.

P0 lifecycle operations intentionally use lightweight audit rather than a complex compliance workflow. Archive, restore, quarantine, release, and delete record actor, target, reason, before/after state, time, result, and correlation id. Explicit deletion uses one normal authorization and confirmation, tombstones metadata, deletes payloads idempotently, and marks Evolution evidence `source_deleted`; no default four-eyes or ticket approval is required. Future legal hold or stricter Tenant policy may add stronger controls without changing the baseline lifecycle.

## Status

Accepted as P0 design baseline.

## Consequences

- Long-term evidence remains useful without keeping every byte in the hot query path.
- Dashboard availability is independent from ObjectStore scans and Langfuse retention.
- TraceManifest, lifecycle reconciliation, cold restore, and projection replay become required platform capabilities.
- Suspected sensitive or corrupt data can be quarantined before deletion without making redaction a P0 ingestion blocker.
- Deleted source content cannot remain silently represented as verifiable Evolution evidence.
