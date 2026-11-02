# ADR 0039: Use A Channel-Specific DeliveryCardProjection

- Status: Accepted
- Date: 2026-07-13

## Context

Delivery must show progress, waiting-user prompts, approvals and final report/patch results inside Feishu. Web DisplayPart is richer than a collaboration card and evolves under a different rendering/runtime contract. Sending only text plus a link would remove the in-channel choices and result cards that Delivery exists to provide. Letting card payload or callback state become Task/Worker authority would create a second workflow engine with provider-specific expiry and retry semantics.

## Decision

Switchback owns `DeliveryCardProjection v1`, a channel-optimized read model derived from the same authorized owner events, normalized projection facts and durable Display checkpoints used by other presentation surfaces. It is not produced by scraping Web View JSON, rendering React components or reading raw Trace.

P0 card kinds are `task_progress`, `waiting_user`, `approval` and `result`. A projection carries canonical subject/source references, monotonic projection and render revisions, permission-filtered summary blocks, attention state, a conservative DeliveryActionDescriptor subset, a Web deep link, provider capability snapshot and expiry/degradation state. Delivery owns CardInstance/provider-message mapping, delivery attempts and render state only.

P0 inline actions are limited to `create_task`, `adopt_task_draft`, `submit_choice`, `submit_text`, `approve_once`, `approve_session`, `cancel` and `retry` when the owner projection explicitly exposes the matching command. Per ADR 0045/0046, `create_task` is restricted to a `waiting_user` projection whose subject is one exact ready TaskDraft and succeeds only for its initiator; `adopt_task_draft` forks a new actor-owned draft for another authorized participant. Neither callback claims Task success. `open_web`, `review_artifact` and `continue_worker` are links. Applying a patch, publishing a branch, creating a PR, production writes, arbitrary Tool execution and unsupported free-form Worker interaction always deep-link to the Web surface.

Per ADR 0047/0048, a shared `cancel` or `retry` descriptor represents a currently valid Task transition candidate, not authority granted to every card viewer. The callback must reauthorize the actual actor against current Task owner policy. P0 has no time-budget action; ApprovalGrant and any future TaskActionGrant management remain actor-private/Web interactions because they require exact scope constraints.

Card callback payload contains an opaque action token or references card/action/render revision; it does not contain reusable authority. Delivery verifies provider signature, actor mapping, Tenant/Workspace access, card/action freshness and replay identity, then submits an idempotent Gateway command. The provider callback receives a fast accepted/toast response; business completion is reflected only when owner evidence updates DeliveryCardProjection and the card is rendered again.

Provider limits such as callback response deadline, update window, interaction window, rate limits and card-version capability belong to a versioned DeliveryProviderCapability snapshot. Current Feishu limits inform P0 defaults but are not Switchback domain constants. Stale, expired, unsupported or complex interactions degrade to a non-misleading summary and authorized Web deep link.

Progress rendering is semantic and coalesced. P0 does not stream every Agent token into Feishu cards. Duplicate callbacks and update responses are idempotent; provider response loss triggers read/reconciliation when available rather than blind repeated commands.

## Consequences

- Web and Feishu can evolve their renderer schemas independently while sharing canonical identities and owner facts.
- Delivery preserves useful in-channel interaction without owning Task, Approval or Worker state.
- Card projector, callback adapter and renderer need explicit conformance and idempotency tests.
- Some rich interactions intentionally leave Feishu and open the Web detail view.
- Provider capability drift is localized to the Delivery adapter/policy.

## Alternatives Rejected

- Map DisplayPart directly to Feishu components: less initial code, but couples Web lifecycle and provider card constraints and makes unknown/unsupported parts unsafe.
- Send only text plus Web links: safest and simplest, but fails the required intermediate-choice, approval and result-card experience.
- Execute actions directly inside Delivery callback handlers: low latency but makes channel delivery a business-state owner and creates duplicate side-effect risk.
