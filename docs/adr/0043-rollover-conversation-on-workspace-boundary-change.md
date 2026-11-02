# ADR 0043: Rollover Conversation On Workspace Boundary Change

- Status: Accepted
- Date: 2026-07-13

## Context

An Assistant Conversation contains more than a current Workspace id. Its Turns, summaries, TaskDrafts, ContextPacks and Evolution evidence may already contain Workspace-scoped repository names, documents, Truth, Artifact references and user instructions. Replacing only `default_workspace_id` while continuing the same Conversation would require every prompt compiler, summarizer, archival reader, Evolution consumer, search index and export path to filter historical content perfectly.

That is a broad, fragile security obligation. A single missed consumer could carry W1 content into a W2 prompt or derive a W2-scoped memory from W1 evidence. Requiring a new Feishu topic would avoid that ambiguity but would fragment the user-facing collaboration flow.

## Decision

The provider topic remains the same, but a Workspace boundary change rolls the backend Assistant Conversation to a new identity.

The first explicit transition from Workspace-unresolved to W1 stays in the existing Conversation. Once a Conversation has used a resolved Workspace, any transition from W1 to a different W2, from W1 to cleared/unresolved, or away from W1 because Membership/policy access is revoked creates a successor Conversation. The old Conversation becomes `superseded` and read-only for new Turns. Switching back to W1 later creates another successor; it never reopens the earlier W1 Conversation.

Interaction owns `ConversationRollover` and immutable `ConversationHandoff`. One database transaction locks and revalidates the current ChannelThreadBinding and source Conversation revisions, creates the successor Conversation and target WorkspaceContextResolution, writes the rollover/handoff records and Outbox, marks the source superseded, and advances the binding to the successor. The transaction performs no model, object-store or provider call.

Default handoff is built from structured Workspace-neutral facts such as the user-stated goal, tenant/user-level constraints, unresolved clarification dimensions and safe assumptions. It does not copy raw Turns, prior summaries, Workspace Truth, repository/file names, Task/Worker/Artifact references, Tool output or hidden model state. A model may propose a handoff but cannot classify or authorize it.

Moving selected Workspace-derived context from W1 to W2 is a separate explicit carry-over action. It requires authenticated intent, current read authorization for each W1 source, current target-context authorization for W2, policy classification, exact source references and an audited immutable handoff revision. The rollover itself remains valid if no cross-Workspace content is carried.

Concurrent inbound messages are assigned by the serialized binding transaction, not provider timestamps. A message transaction that commits against the old binding revision before rollover belongs to the source Conversation; one that observes the new revision belongs to the successor. Stale envelopes cannot be silently rerouted or duplicated.

Existing Tasks, WorkerSessions, Traces, Artifacts and WorkspaceRevisions retain their original Workspace and identities. Explicit actions on them continue to use their own Workspace and authorization. A W1 WorkspaceChannelShareGrant is not inherited by W2 output.

Turns, summaries and Evolution evidence retain the Conversation and Workspace scope snapshot under which they were produced. Evolution may derive user/tenant Memory according to its normal evidence policy, but it cannot merge Workspace-scoped evidence across rollover generations merely because they share a provider topic or lineage.

## Consequences

- Users keep one Feishu topic while backend context boundaries remain explicit.
- Prompt and Evolution consumers can treat one Conversation generation as having at most one resolved Workspace history.
- A Workspace switch creates a visible lineage and a small amount of extra Conversation metadata.
- Cross-Workspace continuity is intentionally conservative and requires explicit carry-over for protected content.
- Old Conversations remain available through authorized Web history but cannot receive new Turns.
- Membership revocation can isolate future channel messages without deleting or relabeling historical evidence.

## Alternatives Rejected

- Continue one Conversation and filter every historical item by Workspace on every read: ergonomic but distributes a difficult security invariant across all consumers.
- Force a new provider topic for every Workspace change: clear boundary but fragments collaboration, links and notification history.
- Reopen a prior Conversation when switching back to its Workspace: convenient but creates non-linear active history and makes concurrent routing, summarization and audit harder to reason about.
