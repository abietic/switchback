# ADR 0041: Require Explicit Workspace Resolution Before Execution

- Status: Accepted
- Date: 2026-07-13

## Context

An Assistant Conversation may start as brainstorming, research or requirement clarification before the user knows which Workspace should own later work. Switchback nevertheless needs one exact Workspace before it can create a Task, start or reuse a WorkerSession, load Workspace-scoped context, or issue an execution authorization. Silently selecting a user default or asking a model to infer Workspace from links and recent activity can send code, documents, Truth or Artifacts into the wrong authorization and collaboration boundary.

The prior TaskSpec draft allowed Task Orchestration to create or resolve a missing Workspace. That makes an execution target implicit at the last possible boundary and conflicts with explicit WorkspaceMembership and readiness rules.

## Decision

A Conversation may remain workspace-unresolved while Assistant performs tenant/user-scoped conversation and clarification. Before TaskSpec dispatch, WorkerSession creation/reuse or any Workspace-scoped read/action, Switchback requires an explicit, authorized `WorkspaceContextResolution`.

The user selects an existing authorized Workspace through a Web selector, current card/structured command, or explicitly creates a new Workspace and selects it. Assistant may propose permission-filtered candidates, and a user-global default may be preselected in UI, but neither is execution consent. Model inference, repository URLs, recent Tasks, related refs and single-candidate discovery cannot silently resolve the Workspace.

Interaction records the resolution against `Conversation`. `ChannelThreadBinding` references its optional `default_workspace_id`, monotonic workspace-resolution revision and selection evidence for channel routing. Setting, clearing or replacing the default requires an authenticated command with expected Conversation revision, expected binding revision when channel-originated, and an active TenantMembership plus WorkspaceMembership. Selection itself is not a durable authorization grant; every protected read, Task creation, Worker command and lease issuance rechecks current membership and policy.

An accepted TaskSpec must contain one explicit `workspace_id` and the resolution/membership evidence used at acceptance. Task Orchestration validates and snapshots it; it does not create, replace or infer a missing Workspace. If a new Workspace is needed, the Web form may keep one continuous interaction while a separate idempotent Workspace bootstrap performs `CreateWorkspace` and, for repository-backed M1, `RepositoryImport`. TaskSpec submission remains blocked until the first readable WorkspaceRevision exists and the user explicitly selects the exact result; Task creation never contains clone/import network work.

Changing the binding default affects only future Assistant context selection and untargeted Task drafts. Per [ADR 0043](0043-rollover-conversation-on-workspace-boundary-change.md), leaving a previously resolved Workspace creates a successor Conversation under the same provider topic instead of reusing mixed history. It does not migrate, relabel or override existing Tasks, WorkerSessions, Traces or Artifacts. An explicit Task/Worker/Artifact target always uses the resource's own Workspace and authorization checks; it does not mutate the binding default. Membership revocation fences the old context and creates/requires an unresolved successor rather than falling back to another default.

Workspace selection does not by itself authorize publishing Workspace-derived content to every member of a provider group. That separate boundary is resolved by [ADR 0042](0042-explicit-workspace-channel-share-grant.md): rich group output requires an explicit WorkspaceChannelShareGrant and per-render ChannelDisclosureDecision.

## Consequences

- Users can brainstorm before choosing an execution context.
- Every Task and WorkerSession has an auditable Workspace target before side effects begin.
- The platform needs a small selection/create/rebind interaction and stale-resolution handling.
- User defaults improve ergonomics only as suggestions, not hidden authority.
- Existing resource ownership remains stable when channel context changes.
- Group-channel output remains conservative unless ADR 0042 audience authorization is active and current.

## Alternatives Rejected

- Automatically use the user's global default Workspace: convenient for common cases but unsafe when the request concerns another Workspace or the default is stale.
- Infer Workspace for each message from model output, links or recent activity: flexible but non-deterministic and unsuitable as an authorization/execution boundary.
- Require a Workspace before any conversation: simple state model but prevents useful discovery, brainstorming and no-repository planning before execution scope is known.
