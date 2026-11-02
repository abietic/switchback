# Layered Tool Metadata Authority

Tool effect, cancellation, idempotency, and reconciliation metadata uses a layered authority model. A versioned ToolCapabilityDeclaration from a built-in Tool, Skill, or MCP manifest states producer intent but is not an authorization fact. The Tool Adapter validates and normalizes technical evidence and may raise risk or revoke unverifiable capabilities, but it cannot independently lower declared risk. Capability/Skilling owns the versioned ToolGovernancePolicy and produces the authoritative immutable ResolvedToolPolicy consumed by Worker.

TaskSpec, tenant policy, ApprovalGrant, and runtime constraints may only narrow the resolved policy. Worker validates provenance, policy revision, scope, and content hash before invocation and enforces the snapshot without reinterpreting it. Agent or model text cannot modify Tool metadata. Trusted runtime evidence may escalate one invocation but cannot lower risk or expand permissions.

Missing, untrusted, conflicting, stale, or unverifiable metadata fails closed. Effect class resolves to `unknown`, unsupported cancellation evidence resolves to `unsupported`, and idempotency or reconciliation claims cannot enable automatic retry without Adapter evidence. A platform-owned, versioned policy override may change the final decision only with an auditable reason.

## Status

Accepted as design baseline.

## Consequences

- Third-party manifests cannot grant themselves safe retry or lower approval requirements.
- Adapters remain technical verifiers rather than hidden policy owners.
- Worker behavior is deterministic and replayable from the ResolvedToolPolicy snapshot.
- Conservative defaults may create additional approval or reconciliation work for poorly described Tools.
