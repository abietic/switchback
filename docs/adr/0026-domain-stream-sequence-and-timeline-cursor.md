# Domain Stream Sequence And Timeline Cursor

Each IntegrationEvent stream has one logical StreamOwner. The owning bounded context allocates the next StreamSequence in the same transaction as aggregate state and Outbox. EventCenter validates, routes, retries, detects gaps, drives DLQ/replay, and updates projections, but never allocates or rewrites domain sequence. Other contexts must send commands to the owner rather than write the owner's stream.

There is no global business sequence across streams. Correlation and causation ids preserve cross-stream relationships. A TimelineProjection independently assigns a timeline epoch and monotonic TimelineCursor when committing user-visible entries; this cursor exists only for stable append, SSE resume, and pagination. Normal replay reuses the existing cursor. A destructive rebuild that cannot preserve mappings increments the epoch and requires client resync. Occurred time is retained for display and audit, not ordering.

Identical redelivery is ignored by event id/idempotency key. A future sequence is durably buffered until the gap is repaired. Reuse of one stream/sequence by a different event id is an integrity incident and cannot be resolved by last-write-wins. A late user-visible event receives a new tail cursor and `late=true`; already published cursors are immutable.

P1 RocketMQ uses `stream_id` as FIFO message group, but the IntegrationEvent payload sequence remains authoritative. Broker retries, DLQ replay, and transport changes use the same idempotency, gap, causation, and TimelineCursor semantics as P0 PostgreSQL outbox polling.

## Status

Accepted as design baseline.

## Consequences

- Domain order remains atomic with domain state rather than depending on EventCenter availability.
- Cross-stream views stay stable without pretending distributed events have one total business order.
- Consumers need durable gap state, integrity incidents, and replay tooling.
- Timeline may display a late marker instead of silently rewriting history.
