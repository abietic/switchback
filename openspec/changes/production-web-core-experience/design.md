## Context

The production `web/` application already owns snapshot identity, cursor continuity, resync, transient display, pending commands, revisioned command clients, Assistant readiness, Worker recovery, Task actions, and Artifact integrity. The approved `web-demo/` prototype contributes information architecture and visual-language evidence but has no production authority. This change must combine those strengths without moving business ownership into presentation code or creating a second frontend.

The migration crosses Eventing projection, Frontier contracts, client replica integration, application routing, shared presentation foundations, and every current core page. The archived Assistant specs and current Task, Worker, Runtime, and Artifact specs remain dependencies and define the behavior that the migration must preserve.

## Goals / Non-Goals

**Goals:**

- Add one deterministic, viewer-scoped `home_dashboard/index` projection and consume it through the existing Frontier snapshot/SSE path.
- Make `web/` the single router, shell, theme, and core-page implementation.
- Adopt the approved adaptive information architecture and complete dark/light presentation while preserving command, authorization, continuity, recovery, acceptance, and integrity contracts.
- End with automated, browser, repository, and local Compose evidence sufficient to remove all superseded production UI.

**Non-Goals:**

- Copy prototype mocks, simulated heartbeat/cost/approval data, or the prototype component catalogue into production.
- Implement Workspace, Memory/Evolution, Skills/MCP, full Observability management, Admin, Web IDE, SessionProcess, or PreviewProxy capabilities.
- Replace Frontier with page-owned fetching, duplicate existing owner specs, or move business authority into React state.
- Modify `web-demo/` or turn it into a second production bundle.

## Decisions

### 1. Add a page-shaped Home projection instead of joining domain data in the browser

Eventing projection will materialize `home_dashboard/index` from authoritative Task, WorkerSession, Artifact, and continuity-capacity state, enforce viewer scope on the server, and publish it through existing Frontier snapshot/delta schemas. This keeps authorization, stable ordering, list limits, and related references consistent across reconnects.

The current production slice runs only under `SingleUserLocalProfile`: process configuration fixes one Tenant and actor, and the profile intentionally has no dynamic Membership/RBAC or Workspace revocation store. Therefore owner-scoped rows for that fixed Tenant/User are the current authoritative Workspace-readability evidence. A future multi-user profile must add an authoritative readable-Workspace port/predicate to Eventing projection before reusing this View; client filtering is never an acceptable substitute.

Ordinary owner ProjectionEvents retain their pre-Home serialized shape, injected-clock behavior, and `event_digest`; the canonical IntegrationEvent digest is persisted only as receipt side-channel metadata and therefore does not change rollback-visible owner identity. Each relevant owner event drives a separate `local-home-dashboard-refresh/<owner-event-id>` synthetic projection. Refreshes for one Tenant/User acquire a session advisory lock before opening one REPEATABLE READ transaction; every Home Task, WorkerSession, Artifact, and quota read plus the Home View/delta/receipt write uses that snapshot. Lock wait is caller-cancellable; rollback, unlock, and discard cleanup are bounded, and any path that cannot prove the session lock was released destroys the PostgreSQL connection instead of returning it to the pool. A post-error owner-receipt check closes concurrent duplicate-delivery races.

Receipts created before the source-digest side channel existed are terminal compatibility evidence. Receipt lookup returns an explicit `missing`, `verified duplicate`, or `legacy terminal` result: when the exact source Outbox event is still retained, the materializer derives and backfills its digest and a verified duplicate may repair a missing synthetic Home refresh; when those source bytes are unavailable, the ordinary legacy receipt returns its prior success immediately without owner queries or any Home View, delta, receipt, or stream write. Migration down deletes Home bootstrap/refresh identities only when both the reserved Materializer identity and the exact single-item `home_dashboard/index` projection result prove ownership; a prefix lookalike is retained. Because the pre-Home ordinary digest includes a process-local materialization clock, down migration removes only receipts whose exact source Outbox row remains unpublished and only the ordinary delta whose receipt `projection_result`, deterministic delta ID, full View identity, epoch, cursor, base/result versions, and source reference all agree. Deltas that merely share an IntegrationEvent source reference survive. It then recomputes affected source-stream positions from retained receipts. Each affected ordinary View enters a new epoch with cursor/version zero so old clients resync and the old binary's replay starts contiguously at cursor one; acknowledged receipts and deltas remain as prior-epoch history. This allows a restarted old binary to perform a fresh replay with its new clock without weakening acknowledged history.

`recent_deliveries.acceptance_mode` is an additive Home contract field. The original `LocalHomeDashboardBootstrap.v1` identity, payload, serialized delivery shape, and digest stay immutable so an old binary can ignore the additive field after rollback. On startup, the current binary checks the persisted `home_dashboard/index` under the same viewer advisory lock and one REPEATABLE READ transaction. Only a non-empty legacy delivery list missing `acceptance_mode` is rebuilt from authoritative state and applied as a normal `LocalHomeDashboardContractUpgraded.v2` projection event; an empty or already-current Home is not rewritten. The upgrade identity includes the legacy View epoch and cursor, preserving idempotency for one base while allowing a later old-binary write at a new cursor to be upgraded after roll-forward. The View epoch remains stable and the cursor/version advance through the normal receipt/delta path. Direct JSONB mutation was rejected because it would bypass replay, receipt, and cursor continuity. Migration down recognizes the exact reserved upgrade prefix only when the receipt proves the single `home_dashboard/index` result.

Alternative considered: query several endpoints from the Home page and merge them in React. Rejected because it creates partial snapshots, leaks filtering responsibility to the client, and makes cursor-consistent refresh impossible.

### 2. Preserve the existing replica and command ownership boundary

Each migrated feature consumes `ClientViewReplica` and the existing Frontier controller, then maps authorized fields through a presentation adapter. Accepted HTTP responses create only command-scoped pending state; owner projections remain business truth. Existing TaskSpec, IntegrationEvent, Trace JSONL, ArtifactRef, Assistant ContextPack, readiness, recovery, and acceptance owners are unchanged.

Alternative considered: let each new page own fetching and optimistic reducers. Rejected because it would duplicate causality, revision, transient/durable, resync, and permission semantics already proven in production.

### 3. Use one router and one adaptive shell

One React Router tree owns every core route. One shell switches between expanded overview navigation, focused icon rail, and mobile drawer based on route class and viewport. Pages declare presentation mode but cannot create their own navigation owner. Opening `/assistant` renders an explicit start action; navigation alone has no command authority.

Alternative considered: preserve the legacy router and wrap new pages behind migration flags. Rejected because the closeout target requires one production implementation and permanent dual routing makes route and accessibility behavior ambiguous.

### 4. Keep themes semantic, complete, and local to presentation

CSS custom properties define semantic tokens shared by Tailwind composition and non-Tailwind components. Dark is the first-visit default; explicit dark, light, or system selection is stored locally in the browser. Theme state never enters domain persistence or governed Memory.

Alternative considered: store theme through backend user preferences. Rejected because no cross-device product requirement justifies adding a domain owner, projection, or privacy lifecycle for presentation-only state.

### 5. Migrate by vertical slices and delete legacy owners only after evidence

The sequence is Home projection, runtime-validated Web contract, UI/theme foundation, shell/router, Home and Tasks, Assistant, Worker/Workbench, Artifact, shared continuity/failure behavior, browser acceptance, then legacy removal. Each slice remains buildable and testable. Temporary adapters are removed unless they form a stable feature boundary.

Alternative considered: copy the whole prototype and repair production behavior afterward. Rejected because it would temporarily replace authoritative behavior with mocks and make regressions difficult to localize.

### 6. Preserve runtime observability ownership

Existing Eventing Projection, transactional Outbox, and Frontier diagnostics remain the runtime observability owners. Home materialization, query, or bootstrap failures surface through sanitized backend diagnostics carrying the affected View and source identities; the browser presents the existing reconnect, resync, and client-upgrade states and never converts missing evidence into success. This slice adds no fake telemetry and no second observability pipeline; full Observability and Admin remain future capabilities.

The approved 2026-10-11 navigation slice exposes `/observability` through the existing overview shell in expanded, rail and drawer navigation. Its page consumes only `productionViews.home()` through ViewBoundary and the existing Home adapter, lists authorized recent Worker identities, and lets the user explicitly navigate to their existing Worker detail diagnostics. It does not fetch or merge additional backend endpoints, issue commands, invent health/cost/metrics, or construct external diagnostic URLs. Grafana remains aggregate and Logs retain the selected scope; unverified Langfuse identity has no link. Empty data offers explicit Assistant/Tasks entry points. Admin remains a separate non-interactive planned item. Component/build evidence does not close real browser or provider acceptance.

The diagnostics slice defaults to Session scope and stores only an explicit presentation selection in the route: no selection, exact Artifact ID, or exact Run/optional Turn. Artifact and Run/Turn parameters are mutually exclusive. Artifact reload resolves current canonical ArtifactSummary and displays that same Artifact or unavailable; it never substitutes newest. Timeline headers expose their canonical Run/Turn, including validated older pages through the existing parser. An exact historical route can reauthorize through diagnostics without loading every timeline page. Active execution fields continue to own commands and never choose a navigation target. Keyed diagnostics immediately clear stale links and abort late responses; Session changes remount WorkerDetail. The client validates finite status, canonical requested/authorized target equality, unique loopback link kinds, and the historically_verified identity/link relation. Historical proof wording and safe summary remain explicit; current-project/health and physical readback are separate gates.

Alternative considered: add page-local metrics or prototype telemetry to make the migration appear self-observing. Rejected because those signals would not be authoritative, would duplicate existing diagnostics, and could imply successful materialization when the owner pipeline failed. Closeout evidence instead records the relevant projection/query test results, sanitized failure identities, Frontier continuity behavior, browser-state acceptance, and any exact external gate blocker.

## Risks / Trade-offs

- [Cross-page visual migration regresses owner semantics] → Strengthen feature tests before moving presentation and retain existing replica and command clients.
- [Home projection becomes a new aggregate or authorization bypass] → Keep it a derived viewer-scoped read model, refresh only from authoritative owner events, and integration-test Tenant/viewer isolation.
- [Home diagnostics leak data or imply false success] → Preserve existing observability owners, sanitize View/source identities, test failure and continuity presentation, and record exact closeout evidence without fake telemetry.
- [Dual UI survives indefinitely] → Make static repository shields and deletion of old router, shell, styles, mock imports, legacy product/prototype copy, and permanent flags explicit closeout gates.
- [Theme or responsive work hides accessibility failures] → Test focus restoration, keyboard navigation, text/icon status meaning, reduced motion, mobile drawer behavior, and bounded horizontal scrolling.
- [Browser or Compose gates fail for environment reasons] → Report credential, Docker, external-service, or infrastructure blockers separately and keep the change active; never record an unexecuted gate as passing.
- [Additional dependencies increase bundle and maintenance cost] → Admit only one router, Tailwind composition, and the accessible primitives used by this slice; keep current Markdown, Diff, icon, and testing dependencies. Security-driven exact-pin refreshes stay within that admitted set, and audit evidence distinguishes production SPA reachability from unused SSR/RSC modes.

## Migration Plan

1. Register and materialize `home_dashboard/index`, including empty bootstrap, owner-event refresh, stable ordering, limits, capacity, related refs, and viewer isolation tests.
2. Add runtime-validated TypeScript contracts and representative Frontier browser fixtures.
3. Add semantic theme tokens and one adaptive router/shell owner.
4. Migrate Home and Tasks, then Assistant, Worker/Workbench, and Artifact through feature adapters while preserving existing commands and replicas.
5. Unify pending, stale, reconnect, resync, upgrade, permission removal, recovery, integrity, empty, loading, and error presentation.
6. Run unit, build, browser, visual, OpenSpec, repository, and local Compose acceptance gates.
7. Remove superseded production UI only after replacement gates pass; retain the active change with exact evidence if an external gate is blocked.

Rollback before closeout is a commit-level revert of the affected vertical slice while the prior production owner still exists. After legacy removal, rollback is a revert of the migration commits; no runtime data migration or domain-state conversion is introduced by this change.

## Open Questions

None for the approved core slice. Out-of-scope platform modules require their own authoritative projections and capability proposals before becoming interactive.
