# ADR 0046: Bind TaskDraft Confirmation To Initiator And Fork On Adoption

- Status: Accepted
- Date: 2026-07-13

## Context

A shared Conversation lets several participants contribute requirements and clarification. A ready TaskDraft can therefore be visible to users other than the person who initiated it. Allowing any authorized participant to click create would make a different user silently assume cost, audit and execution ownership. Allowing Conversation controllers to confirm would also turn narrow context coordination into Task authority.

Restricting confirmation to the initiator is clear, but the platform still needs a continuity path when that person is unavailable or another participant intentionally wants to run the same work. Mutating or transferring the original draft would make its evidence and prior confirmation semantics ambiguous.

## Decision

Every TaskDraft lineage has an immutable `initiator_user_id`, derived from the authenticated Turn, structured command, form or API request that created the draft. Other users may contribute shared Conversation Turns that change the draft revision, but contribution, WorkspaceRole and Conversation controller status do not change the initiator.

Only the initiator may confirm an exact ready revision and generate `ExplicitTaskCreationIntent`. Intent actor, TaskDraft initiator and accepted TaskSpec `user_id` must be the same user.

A different participant must execute an explicit `AdoptTaskDraft` command through a registered bot action, structured `/task adopt` command or Web action. Adoption creates a new TaskDraft identity with the adopter as initiator and an immutable `derived_from_task_draft_ref`; it never transfers or mutates the source draft.

Adoption revalidates current Conversation/binding generation, source visibility, Tenant/Workspace access and every copied context reference. It does not inherit the source user's creation intent, approvals, secrets, repository grants, private evidence or actor-specific assumptions. The new draft receives a new actor-specific readiness assessment and canonical digest. The adopter must separately confirm that new ready draft before Task creation.

The source draft remains available to its initiator unless independently stale, consumed or cancelled. Different actors may intentionally create separate Tasks from related drafts. Cross-actor equal TaskSpec digests are lineage/duplicate-warning evidence only, not idempotency: Switchback does not merge them automatically. Duplicate adoption by the same actor against the same source revision returns the original adopted draft.

Conversation rollover, Workspace mismatch, consumed/cancelled source, inaccessible context or an uninterpretable schema/policy makes adoption fail closed. A source whose readiness or original initiator authorization is stale may still be forked only when the exact payload remains readable in the current generation/scope; the new draft then re-evaluates everything for the adopter. No draft or confirmation is silently carried into a successor generation.

## Consequences

- Task creation actor, cost attribution and audit ownership remain explicit.
- Conversation controller stays a context/lifecycle role rather than an execution proxy.
- Work can continue when the initiator is unavailable through an explicit, reviewable fork.
- Adoption adds one extra confirmation step and may create parallel related drafts.
- UI and projections need clear initiator, derived-from and possible-duplicate indicators.

## Alternatives Rejected

- Any authorized participant can confirm the same draft: flexible but silently changes actor and cost ownership.
- Initiator or Conversation controller can confirm: operationally convenient but expands controller beyond its accepted boundary.
- Transfer the existing draft to another user: loses stable evidence ownership and makes old actions/intents difficult to fence.
- Initiator-only with no adoption path: simplest but blocks legitimate continuity when the initiator is absent.
