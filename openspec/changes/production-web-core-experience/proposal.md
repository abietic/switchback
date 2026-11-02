## Why

The production `web/` application already owns authoritative Frontier, command, continuity, Runtime recovery, Task acceptance, and Artifact integrity behavior, while the approved `web-demo/` information architecture and visual language exist only as a prototype. We need one production migration owner that adopts that presentation direction without duplicating or weakening the existing domain owners.

## What Changes

- Add a viewer-scoped `home_dashboard/index` Frontier View for attention Tasks, recent Workers, recent deliveries, and continuity capacity.
- Replace the production router and shell with one adaptive product shell for overview, focused execution, and mobile routes.
- Expose `/observability` as a read-only overview using authorized Home recent Workers and their existing exact Worker diagnostics; keep Admin separately planned.
- Add complete dark, light, and system theme presentation with browser-local preference and semantic tokens.
- Migrate Home, Tasks, Assistant, Worker, Workbench, and Artifact presentation inside `web/` while preserving authoritative snapshots, SSE, commands, permissions, continuity, recovery, acceptance, and integrity contracts.
- Establish responsive, accessibility, browser, repository, and local Compose closeout gates, then remove the superseded production visual system.
- Keep `web/` as the only production frontend; retain `web-demo/` as a read-only design reference and exclude its mock engine from production.

## Capabilities

### New Capabilities

- `production-web-core-experience`: Owns the viewer-scoped Home dashboard, adaptive production shell, theme presentation, authority-preserving core page migration, continuity/failure presentation, and migration closeout gates.

### Modified Capabilities

None.

## Impact

- Affects the Eventing projection and Frontier registration needed for `home_dashboard/index`, plus the production `web/` router, shell, feature presentation, styles, contracts, and tests.
- Depends on the archived `assistant-conversation-dispatch` and `assistant-web-interaction` contracts now synchronized to main specs; their readiness, confirmation, pending-command, and handoff behavior remains authoritative.
- Depends on current `task-session-persistence`, `worker-detail-interaction`, and `runtime-execution-core` specs for Task admission and acceptance, Worker continuity/recovery, and Artifact evidence/integrity; this change does not create a second owner for those behaviors.
- Adds minimal production Web dependencies for one router, layout composition, and accessible primitives. Exact pins may be refreshed within that admitted set when a package audit identifies a supported security fix; any remaining advisory must be classified against the production SPA runtime rather than hidden. It does not import `web-demo/`, prototype mocks, or incomplete `worker-web-ide-session-process` behavior.
