# ADR 0044: Channel-Scoped Shared Conversation With Explicit Controllers

- Status: Accepted
- Date: 2026-07-13

## Context

One Feishu group topic can contain messages and actions from multiple mapped Switchback users. Existing routing already binds the topic to one primary Assistant Conversation, but the earlier domain language described a Conversation as if it belonged to one user. Maintaining a separate Conversation per sender would give each user a different hidden history while all Assistant replies still appear in one public topic. The resulting context would be difficult for participants to predict, and a Workspace rebind by one user could not be explained consistently to the others.

Making every group participant an equal Conversation administrator would avoid hidden histories but would let any newly added group member switch the shared Workspace, roll over the Conversation or close the channel interaction. Provider group membership is too broad and too volatile to grant those control operations. At the same time, a Conversation role must not become a shortcut around Workspace, Task, Worker, Approval, Artifact or external-provider authorization.

## Decision

P0 supports two Conversation scopes:

- `user`: one authenticated user is the interaction principal, used for Web or P2P flows.
- `channel`: one provider channel scope, normally a Feishu group topic, shares one current primary Conversation generation.

An active group `ChannelThreadBinding` points to one `channel`-scoped Conversation. All accepted ordinary messages enter that shared history and retain the real actor identity on every Turn. A group participant never impersonates the binding creator or another participant.

Interaction owns a revisioned `ConversationParticipant` relation with two P0 roles:

- `participant` may submit ordinary Assistant Turns and read the channel-safe projection allowed for that actor and audience.
- `controller` also may manage the channel Conversation context and lifecycle through explicit commands.

Creating a group binding requires an authenticated, provider-mapped user and current channel-binding authorization. That creator becomes the first controller. A controller may delegate or revoke controller status using expected revision and audit evidence, but cannot remove the last active controller. Provider group membership or Workspace membership alone never grants controller status.

A provider-mapped active group member may be materialized as a participant on first accepted interaction according to the versioned `ConversationParticipationPolicy`. Losing provider membership or Switchback tenant access revokes future participation but does not rewrite historical Turns.

Controller operations include selecting or clearing the default Workspace, triggering an explicit Conversation rollover, closing the channel Conversation and delegating controller status. Every operation is reauthorized for the actual actor and target:

- selecting or rolling to a Workspace still requires current target Workspace access and policy approval;
- protected cross-Workspace handoff still requires source read and target context authorization;
- Task, WorkerSession, Approval, Artifact and external-provider actions still use their own actor-specific permissions;
- `WorkspaceChannelShareGrant` independently controls what Workspace-derived content may be disclosed to the whole provider audience.

Conversation role is therefore coordination authority, not data or execution authority.

If provider or Switchback access revocation removes the last valid controller, the Conversation enters `control_orphaned`. Existing safe ordinary dialogue may continue only if policy permits, but Workspace context changes, lifecycle changes and controller delegation are fenced. Recovery requires an explicit `RecoverConversationControl` action by an Identity & Access policy-authorized channel administrator who is also a currently mapped provider participant. No ordinary participant is automatically promoted.

When Workspace rollover creates a successor Conversation generation, Interaction projects only source participants/controllers that remain eligible under current policy. The projection records source role revisions and never creates or promotes a controller. If no controller remains eligible, the successor starts as `control_orphaned`.

An explicit controller `CloseChannelConversation` command may close both the channel Conversation and its binding. A provider-side channel disappearance or binding revocation only fences the binding; it does not silently terminate Tasks, WorkerSessions, Artifacts or other resources created from the Conversation.

P0 does not create private per-user sidecar Conversations inside a group topic. Actor-private approvals, protected details and unrestricted Worker chat use provider ephemeral interaction when supported or an individually authorized Web route. Private sidecars remain a P1 extension if real usage proves that public shared context is insufficient.

## Consequences

- Everyone in a group topic sees one understandable Assistant history and one current Workspace context.
- Every Turn and command remains attributable to the real actor.
- Workspace context coordination is stable even as provider group membership changes.
- Controller management adds a small revisioned relation and an orphan-recovery path.
- A controller can coordinate the Conversation but cannot use that role to view, execute or disclose protected Workspace resources.
- P0 avoids the routing and UI complexity of mixing public and private Assistant histories in one provider topic.

## Alternatives Rejected

- One Conversation per user in the same group topic: preserves private histories but produces public replies from divergent hidden context and makes shared Workspace state incoherent.
- All mapped group members are controllers: simple but turns volatile provider membership into powerful lifecycle authority.
- The binding creator is the permanent sole controller: predictable but creates an avoidable single-person bottleneck and no safe ownership-transfer path.
- Shared Conversation plus per-user private sidecars in P0: flexible, but introduces target ambiguity, context synchronization and visibility mistakes before the basic shared flow has been validated.
