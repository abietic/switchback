# ADR 0042: Require An Explicit WorkspaceChannelShareGrant For Group Disclosure

- Status: Accepted
- Date: 2026-07-13

## Context

A user who can read or execute inside a Workspace may trigger Switchback from a Feishu group whose other members do not have WorkspaceMembership. The initiating actor's authorization therefore cannot justify posting Workspace-derived titles, summaries, approval details, repository names, failures or Artifacts to the entire group.

Checking every current group member against WorkspaceMembership is not a sufficient primary rule. Group history can remain visible to later members, provider membership and security events can be delayed or incomplete, and a check performed before rendering can race with an audience change. Authorizing only the sender is worse because it treats a private actor decision as group-wide disclosure consent.

## Decision

Identity & Access owns a revisioned `WorkspaceChannelShareGrant`. P0 requires an active Workspace owner to explicitly bind one Workspace to one verified provider audience scope and select a policy-bounded disclosure profile. A grant may restrict delivery to selected channel bindings or topics, but its audience identity is the provider group, not the initiating actor.

The grant authorizes disclosure only. It does not create TenantMembership or WorkspaceMembership, permit Web access, authorize Task/Worker execution, or grant any card action. Each card callback and Web request still maps and authorizes the acting user independently.

Every Workspace-derived card render or update requires a current `ChannelDisclosureDecision`. The decision rechecks the exact grant revision, Workspace and channel identities, provider security/audience snapshot, source classification and disclosure policy. P0 permits only facts that the owner projection has classified as `channel_shareable` and that the grant profile allows. Raw Trace, private prompts, hidden reasoning, secrets, tool stdout, internal error details, full report bodies, patch/diff content and unrestricted Artifact content remain Web-only regardless of the grant.

Without a valid grant, Delivery emits only a non-resource-identifying notification and a canonical Web deep link whose opener is authorized by Web. Provider-supported per-user ephemeral cards may carry actor-specific detail. A shared group card must not contain a personalized bearer link or imply that every channel member can open the target.

P0 uses a conservative `workspace_summary` profile. It may include an explicitly channel-shareable title, coarse stage/status, bounded safe summary and Artifact type/count. Approval or waiting-user details are included only when separately classified channel-shareable; actor-specific or sensitive interaction is sent through an ephemeral card when supported or degraded to Web.

The grant records an audience/security baseline. Known audience expansion, internal-to-external or private-to-public change, bot removal/loss of inspection capability, unknown provider state, grant expiry/revocation, stale policy or Workspace access change suspends rich disclosure. Member-removal events may update the baseline but cannot broaden disclosure. Provider events trigger fast suspension; periodic reconciliation and render-time freshness checks cover missed or delayed events. An unknown or stale snapshot fails closed.

Revocation fences future sends and updates. Delivery best-effort updates, redacts or recalls earlier bot messages when the provider supports it, but does not claim that already disclosed content, notifications, exports or screenshots can be erased. Provider limitations and any residual exposure are recorded as an auditable disclosure incident. A grant applies prospectively and never retroactively authorizes an earlier disclosure.

## Consequences

- Workspace execution authority and group disclosure authority remain independent and auditable.
- Workspace owners can deliberately share bounded progress/results in a collaboration channel without granting the group Workspace access.
- Delivery and Identity & Access need grant lifecycle, provider audience reconciliation, render-time disclosure decisions and degraded-card handling.
- Audience expansion can temporarily reduce card richness until the Workspace owner reauthorizes the changed audience.
- Historical disclosure has irreversible residual risk; prevention and bounded projection are stronger controls than recall.
- Card actions remain actor-specific even when the surrounding summary is channel-shareable.

## Alternatives Rejected

- Dynamically require every current group member to have WorkspaceMembership: expensive and race-prone, and it does not address later members or historical message visibility.
- Rely only on the initiating actor's permission: confuses execution authority with disclosure authority and leaks Workspace content to the group.
- Never show Workspace-derived group content: safest but removes too much collaboration value; explicit owner consent plus bounded projections provides a controlled middle ground.
