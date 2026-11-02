# ADR 0037: Own A Stable DisplayPart Schema Behind Protocol Adapters

- Status: Accepted
- Date: 2026-07-13

## Context

Switchback must render Codex/ACP output in Worker detail, Task detail and Delivery channels while preserving stable identities across durable checkpoints. Candidate frontend protocols such as AG-UI and Vercel AI SDK expose useful event or message-part models, but they have different lifecycle, identity, approval, artifact and compatibility semantics. Making either external model canonical would leak a library release cycle into Frontier projections and force Switchback authority concepts into provider-specific fields.

## Decision

Switchback owns `DisplayPart v1` as a stable materialized union. P0 defines eight part types: `text`, `reasoning_summary`, `tool`, `activity`, `approval`, `artifact`, `source` and `error`.

Every part has a stable `part_id`, monotonic `part_revision`, stable `ordinal`, lifecycle status, criticality, permission-filtered visibility, provenance, canonical available-action references and a versioned typed payload. A part represents current user-visible state, not a raw event. Durable DisplayCheckpoint contains the complete ordered part set; TransientDisplayDelta may update a compatible temporary buffer between checkpoints.

ACP/Codex, future AG-UI and Vercel AI SDK integrations enter through protocol adapters. Adapters map source identities and lifecycle updates to canonical Switchback identities and retain source metadata in provenance and Trace. Source `toolCallId` or message-part identity cannot be the sole canonical identity.

Display payloads cannot contain executable frontend code, arbitrary React components, unsanitized HTML or provider credentials. `available_actions` contains canonical Gateway command references; it never authorizes or executes an action by itself. The owner context and Gateway revalidate actor, revision, policy and idempotency on every command.

Unknown optional source progress becomes a restricted generic `activity` plus telemetry while the raw source frame remains in Trace. Unknown source events that may affect permission, Tool lifecycle, approval or security fail closed. On the browser side, an unknown non-critical DisplayPart uses a safe fallback renderer with no privileged action; an unknown critical part produces `client_upgrade_required`.

The frontend uses a renderer registry keyed by `(schema_version, part_type)`. Additive optional fields remain compatible within v1. Changed meaning, a new required field, or new permission/security semantics requires a new schema version and explicit adapter/reducer support.

## Consequences

- Switchback pages and Delivery channels share one durable user-visible language.
- External agent/frontend protocols remain replaceable adapters instead of domain dependencies.
- Stable part identity and revision make checkpoint replacement, transient convergence and renderer testing deterministic.
- Adapters require explicit mapping tables and conformance tests for lifecycle, permissions and unknown events.
- Frontend can evolve its component library without changing backend authority contracts.

## Alternatives Rejected

- Use AG-UI events as canonical: useful interoperability, but raw events are not Switchback's materialized read model and do not own Switchback Task, Approval or Artifact authority.
- Use Vercel AI SDK UIMessage parts as canonical: convenient for React chat rendering, but couples backend contracts to a frontend SDK and does not cover all Switchback page/read-model semantics.
- Pass provider-native payloads through to components: fastest prototype, but creates incompatible identities, unsafe fallbacks and provider-specific business logic in the browser.
