# ADR 0040: Default Channel Threads To The Assistant Conversation

- Status: Accepted
- Date: 2026-07-13

## Context

A Feishu topic may contain one Assistant conversation, several Tasks, several WorkerSessions, approval cards and result cards. Ordinary replies do not reliably identify which execution object the user intends to operate. This ambiguity is worse when Tasks run in parallel, a completed WorkerSession remains interactive, or an old card receives a late reply. Feishu topic replies also point `root_id` and `parent_id` to the topic root, so the provider reply tree cannot safely encode Switchback Task/Worker ownership.

Switchback needs a predictable default without forcing one provider topic per Task or silently sending natural-language messages to the most recently active Worker.

## Decision

One active `ChannelThreadBinding` revision maps a provider channel scope to one current primary Assistant `Conversation`. An ordinary inbound message with no validated explicit target creates an Assistant Turn in that Conversation. Creating a Task, receiving a result card or directly interacting with a WorkerSession does not mutate the binding's default route. Per [ADR 0043](0043-rollover-conversation-on-workspace-boundary-change.md), leaving a resolved Workspace is the only accepted operation that replaces the primary Conversation under a new binding revision.

TaskDraft, Task, WorkerSession, ApprovalRequest and Artifact operations require an explicit `InteractionTargetRef` produced by a current card action, a validated structured command or another immutable Switchback interaction context. A TaskDraft target is restricted by ADR 0045/0046 to structured creation, exact initiator confirmation or authenticated fork-based adoption. Direct Worker interaction requires an authorized `worker_session_id`; Task commands require `task_id`; approval and artifact actions require their canonical owner reference and expected revision where applicable. A natural-language request such as "continue Worker X" still enters Assistant first; Assistant may clarify or submit an explicit command but Delivery does not infer the target.

Per ADR 0047, an explicit Task target is necessary but not sufficient for cancel, retry or budget action. Task Orchestration reauthorizes the real actor through TaskActionPolicy and any exact TaskActionGrant; Conversation controller, Workspace editor/reviewer and card visibility do not supply that authority.

`ChannelThreadBinding` may retain related resource references for navigation and projection, but it has no mutable `current_focus`. Delivery must not route from latest activity, latest card, most recent Task, a model guess, or provider `parent_id` alone. Replying to an immutable Switchback bot message may count as an explicit target only when that message is registered with unambiguous routing metadata and the interaction contract makes the target clear.

The binding is scoped by provider, provider tenant, chat and stable thread identity. When the provider cannot create or expose a topic, Delivery uses a capability-specific fallback identity such as chat plus root message while preserving the same routing semantics. A binding relates provider context to Switchback context; it does not grant every channel participant identical permissions. Every inbound actor and target is re-authorized.

Inbound messages are idempotent by provider message identity plus provider content revision when available. The binding revision and explicit target evidence are recorded in `InboundInteractionEnvelope`. Closing or revoking a binding stops channel routing but does not close the Conversation, Task or WorkerSession it references.

## Consequences

- Ordinary replies remain predictable even with parallel Tasks and long-lived WorkerSessions.
- Direct Worker/Task operations require one visible targeting action, command or Web detail surface.
- Delivery stores related refs and provider mapping but does not become an execution router with hidden focus state.
- A future channel profile may create separate Task/Worker topics without changing the explicit-target contract.
- Group participants require individual identity mapping and authorization on every interaction.

## Alternatives Rejected

- Maintain a mutable current focus: this feels conversational but becomes unsafe under parallel work, late messages, retries and focus recovery after process restart.
- Create one provider topic per Task or WorkerSession: targeting is clear, but Assistant context fragments, channel noise grows and some provider/group configurations do not support topic replies.
- Infer targets from natural language or provider reply ancestry: this reduces clicks but makes model/provider heuristics an authorization and execution boundary.
