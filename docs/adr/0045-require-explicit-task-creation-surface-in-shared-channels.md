# ADR 0045: Require Explicit Task Creation Surface In Shared Channels

- Status: Accepted
- Date: 2026-07-13

## Context

A TaskReadinessAssessment determines whether a TaskSpec is executable. It does not prove that a user intends to create a tracked Task now. In a shared Feishu topic, ordinary natural-language messages can be exploratory, quoted, hypothetical or written by another participant. Treating a ready ordinary message as authorization would let model interpretation create cost-bearing execution and persistent Task state without an unambiguous user action.

Requiring every request to pass through a long Assistant round trip would avoid accidental creation but would make complete structured commands and forms unnecessarily slow. The design therefore needs separate gates for semantic readiness and authenticated creation intent.

## Decision

P0 introduces a versioned `TaskCreationTriggerPolicy` and an immutable `ExplicitTaskCreationIntent`.

In a channel-scoped shared Conversation:

- every `ordinary_message` first becomes an Assistant Turn, even when its content is sufficient to produce a ready TaskDraft;
- direct Task creation is allowed only from a validated `/task` or equivalent structured task command, a create-task form/button, or a registered confirmation action bound to one exact ready TaskDraft;
- free text such as "确认" or "直接做" without an immutable TaskDraft target remains an ordinary Assistant Turn and cannot directly create a Task;
- a complete structured request may create a zero-question readiness assessment and proceed directly;
- an incomplete structured request may retain evidence that the actor wants a Task, but after clarification or any material draft change the actor must confirm the final ready revision.

For user-scoped P2P/private interaction and authenticated API calls, policy may accept an unambiguous natural-language create/execute instruction as `explicit_private_request`. Generic consultation, brainstorming and ambiguous requests still enter Assistant clarification.

Creation intent binds the actual actor, interaction scope, source evidence, channel binding and Conversation generation when applicable, exact TaskDraft revision, canonical TaskSpec creation-payload digest, readiness assessment, Workspace resolution, trigger-policy revision, expiry and idempotency key. The versioned digest excludes `creation_intent_ref`, not-yet-assigned Task identity, transport metadata and server timestamps so it has no circular reference. A change to any bound draft/readiness/Workspace/generation fact makes the old intent stale. Conversation rollover never carries an old confirmation into the successor.

Task Orchestration creates Task, its unique TaskSaga and Outbox only after revalidating both independent gates plus actor authorization and current policy. Repeated commands, callbacks or response loss return the original Task identity. Creating a Task does not change the primary Assistant Conversation, default Workspace or any implicit current focus.

Assistant may expose a ready TaskDraft confirmation through the existing `waiting_user` card kind with `subject_ref=task_draft`; this does not add a fifth Task lifecycle card. The action must carry an opaque reference to the exact draft/render revision. Provider reply ancestry or visual proximity is not target evidence.

Conversation controller status does not affect Task creation authority. [ADR 0046](0046-bind-taskdraft-confirmation-to-initiator-and-fork-on-adoption.md) resolves actor ownership: only the draft initiator may confirm it, while another participant must explicitly fork an actor-owned draft through AdoptTaskDraft and confirm the new revision.

## Consequences

- Readiness and user intent become independently testable and auditable.
- Ordinary shared conversation cannot accidentally start cost-bearing execution.
- Complete `/task` commands and structured forms retain a zero-question fast path.
- Assistant must render a precise ready-draft confirmation state and handle stale revisions explicitly.
- Users may need one extra confirmation after an ordinary group request or materially changed draft.
- P2P/private/API surfaces remain more flexible because their actor and audience are less ambiguous.

## Alternatives Rejected

- Automatically create a Task from any ready ordinary group message: shortest path, but model interpretation becomes execution authorization and quoted/hypothetical group text can create unintended work.
- Route every channel request through Assistant and forbid direct structured creation: simple and safe, but wastes an extra round trip for complete `/task` commands and forms.
- Let a Conversation controller confirm any draft by default: conflates shared-context coordination with execution ownership and actor-specific cost/permission authority.
