# Structured Task Readiness Checkpoints

Assistant clarification uses a versioned TaskReadinessPolicy rather than a model confidence threshold or a mandatory form. Each TaskReadinessAssessment classifies goal, deliverable/acceptance, scope/target, constraints, context/access, risk/approval, and execution policy as `known`, `assumed`, `blocking_unknown`, or `not_applicable`, with evidence. Task Orchestration accepts a TaskSpec only when no blocking unknown remains and deterministic validation succeeds.

Low-risk, reversible gaps may use policy-catalogued defaults recorded as TaskSpec assumptions. Unverifiable goals, conflicting constraints, ambiguous targets that could affect the wrong resource, production/external/irreversible effects, and missing required identity, access, credentials, or source context cannot be defaulted. A user instruction to proceed does not waive these blockers.

Assistant asks one highest-priority blocking question per clarification message. After three consecutive clarification questions, it must emit a ClarificationCheckpoint showing the current TaskDraft, assumptions, blockers, and evidence. When no hard blocker remains, the TaskDraft initiator may accept permitted assumptions and create from the exact revision; this action generates a new readiness assessment plus explicit creation intent before dispatch. Other users must first adopt their own draft per ADR 0046. Otherwise the user may continue clarification, narrow scope, or cancel. Continuing resets the next three-question budget; there is no hard lifetime limit on clarification rounds.

## Status

Accepted as design baseline.

## Consequences

- Dispatch decisions are explainable and replayable without trusting a floating model confidence score.
- Simple requests can proceed with explicit safe defaults instead of filling every field manually.
- Long clarification loops surface a decision checkpoint while still allowing genuinely complex work to continue.
- The platform must version its blocker rules and default catalog and keep frontend checkpoint rendering aligned with the protocol.
