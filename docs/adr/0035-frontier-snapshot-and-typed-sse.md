# ADR 0035: Use Frontier Snapshot And Typed SSE Delta

- Status: Accepted
- Date: 2026-07-13

## Context

Switchback Frontend renders page-optimized Task, Worker, Workspace, Artifact and ActionInbox views while commands execute asynchronously. The browser needs an initial consistent state, low-latency updates, reconnect after network loss, and a deterministic escape hatch when replay history is unavailable. Raw domain events, Trace records and broker messages are not stable browser contracts.

GraphQL can provide flexible typed queries, but it does not by itself define Switchback command idempotency, Agent event taxonomy, cursor retention or resynchronization. A generic timeline-only stream would force Frontend to reconstruct owner state. A full-duplex WebSocket is unnecessary for P0 because browser commands already use HTTP and the realtime direction is primarily server to browser.

## Decision

P0 uses:

1. Gateway HTTP commands returning `accepted` plus `command_id`.
2. Frontier HTTP queries returning a complete ProjectionSnapshot.
3. Frontier typed Server-Sent Events carrying replayable ProjectionDelta and control events.
4. Separate resumable HTTP for Artifact payloads.

Each ViewProjection has an independent `view_epoch`, monotonic `view_cursor`, and `projection_version`. Snapshot creation records a cursor from the same committed view state. The browser connects with `Last-Event-ID = view_epoch:view_cursor`, so events committed after snapshot creation are replayed without a query/subscribe race.

ProjectionDelta uses versioned semantic event types, not raw IntegrationEvent, JSON Patch paths, database change events or Trace lines. A delta declares exact `base_projection_version` and `result_projection_version`; the client applies it only when epoch and base version match its local view.

Frontier keeps a policy-bounded ProjectionReplayWindow. It returns `resync_required` when the requested epoch differs, the cursor is no longer retained, a gap exists, or the client cannot safely reach the next projection version. The client discards the affected cached View and fetches a fresh snapshot. Protocol-version incompatibility is reported separately as `client_upgrade_required`; repeated refetch is not a protocol fix.

Native EventSource uses same-origin secure HttpOnly session credentials (or an equivalent same-origin BFF). Switchback does not put bearer tokens in SSE URLs. Authorization is checked for snapshot, stream establishment and emitted deltas; revocation fences future data and closes the stream without leaking inaccessible related identities.

GraphQL is not a P0 dependency. A future GraphQL read adapter may expose the same page projections if multiple clients need materially different field combinations, but it cannot replace Gateway commands, Frontier cursor semantics or owner context authority.

Fine-grained token/reasoning chunks are deliberately outside this durable ProjectionDelta decision. The next decision separates durable state/checkpoints from transient Agent streaming to avoid write amplification without producing broken reconnect behavior.

## Consequences

- Snapshot plus cursor removes the race between initial query and stream subscription.
- Frontend reducers operate on a small typed protocol and never infer business state from raw events.
- Frontier must persist or reconstruct a bounded delta replay log and monitor replay/resync rates.
- Page-specific views can use one active SSE stream each; embedded related data is already part of the page projection.
- GraphQL remains available later without becoming a prerequisite for P0 Agent UX.

## Alternatives Rejected

- GraphQL normalized cache/subscriptions for P0: useful query flexibility, but it adds schema/resolver/cache/subscription governance without removing Switchback-specific stream semantics.
- Generic timeline only: simpler backend contract, but it moves state reconstruction and version conflicts into Frontend.
- WebSocket for all traffic: unnecessary bidirectionality and a second command transport for the accepted P0 workflow.
- Generic JSON Patch: tightly couples clients to server document paths and makes semantic compatibility harder to review.
