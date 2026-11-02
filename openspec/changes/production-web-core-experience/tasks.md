## 1. Authoritative Home Projection

- [x] 1.1 Add failing Frontier View validation tests for `home_dashboard/index`, then register the View type without changing existing Task bootstrap identity.
- [x] 1.2 Add integration tests for deterministic empty bootstrap, attention Tasks, recent WorkerSessions, recent deliveries, continuity capacity, related references, stable ordering, and six-item limits.
- [x] 1.3 Implement the viewer-scoped Home query and materialization from current Task, WorkerSession, Artifact, and continuity-capacity owners.
- [x] 1.4 Refresh Home idempotently from relevant owner events while proving Conversation-only events, duplicate delivery, Tenant scope, and viewer isolation behavior.
- [x] 1.5 Run focused projection, Frontier snapshot/SSE, composition, race, and security tests.

## 2. Web Contracts and Presentation Foundation

- [x] 2.1 Add failing runtime-validation tests for Home View identity, data shape, status values, canonical references, and rejected malformed or out-of-scope data.
- [x] 2.2 Add the typed Home contracts, presentation adapter, and authoritative Frontier browser fixture without prototype mock imports.
- [x] 2.3 Add failing tests for dark first visit, explicit dark/light/system persistence, system preference changes, reduced motion, and presentation-only storage.
- [x] 2.4 Install only the admitted router, Tailwind composition, and accessible primitive dependencies; implement shared semantic tokens and complete dark/light themes.
- [x] 2.5 Run Web contract, theme, unit, and production-build gates.

## 3. One Router Shell and Overview Pages

- [x] 3.1 Add component and browser tests for the six core route patterns, active navigation, overview/focused shell modes, mobile drawer, focus restoration, and explicit Assistant creation.
- [x] 3.2 Replace the production router and shell with one adaptive owner and remove the superseded router/shell in the same tested slice.
- [x] 3.3 Implement production Home from `home_dashboard/index`, including actionable empty, loading, reconnecting, and error states.
- [x] 3.4 Move and restyle Tasks within feature boundaries while preserving direct TaskSpec creation, filters, capacity, revisions, acceptance, and exact Worker navigation.
- [x] 3.5 Run route, shell, Home, Tasks, responsive, accessibility, and build gates.

## 4. Authority-Preserving Focused Pages

- [x] 4.1 Strengthen Assistant tests for projected questions, readiness, Workspace evidence, checkpoint actions, exact draft confirmation, pending commands, handoff, stale targets, and resync.
- [x] 4.2 Migrate the Assistant presentation into its feature boundary without changing `assistant-conversation-dispatch`, `assistant-web-interaction`, or Task admission ownership.
- [x] 4.3 Strengthen Worker and Workbench tests for durable/transient separation, grouped history, command authority, approvals, recovery generations, Task actions, and integrity-gated Report/Diff/Files.
- [x] 4.4 Migrate Worker and Workbench atomically while retaining a non-interactive future Web IDE seam and current `worker-detail-interaction` and `runtime-execution-core` ownership.
- [x] 4.5 Strengthen and migrate Artifact detail tests and presentation for availability, digest/integrity, Markdown or patch content, bounded code scrolling, canonical links, and Worker return navigation.
- [x] 4.6 Run focused Assistant, Worker, Artifact, browser, and production-build gates.

## 5. Continuity Failure and Browser Acceptance

- [x] 5.1 Add shared presentation coverage for skeletons, reconnect, accepted-command pending, stale revision, `resync_required`, `client_upgrade_required`, permission removal, Runtime recovery, Artifact integrity, empty Views, and actionable errors.
- [x] 5.2 Verify presentation metadata never manufactures permissions, business state, Runtime health, cost, token, model-routing, or simulated records.
- [x] 5.3 Expand Playwright coverage across Home-to-Task/Worker navigation, explicit Assistant creation and dispatch, Worker commands/recovery, Artifact review, Task accept/rework, reconnect, resync, and client upgrade.
- [x] 5.4 Capture Home, Assistant, Worker, and Diff at desktop and mobile widths in dark and light themes, and verify no document-level horizontal overflow.
- [x] 5.5 Run the complete Web unit, build, browser, visual, responsive, and accessibility gates.

## 6. Single-Owner Closeout

- [x] 6.1 Remove the last superseded production router, shell, theme, view, style, migration flag, disabled old UI path, and legacy product/prototype copy.
- [x] 6.2 Add and run repository shields proving `web/` has no `web-demo/`, prototype mock, simulated-domain, or incomplete future-module imports and only one production implementation.
- [x] 6.3 Run strict OpenSpec validation, generated-contract checks, `make verify`, and the documented real local Compose Assistant-to-Task-to-Worker-to-Artifact-to-acceptance-to-Home flow.
- [x] 6.4 Record exact automated and manual acceptance evidence, separating environment or external-service blockers from product failures.
- [ ] 6.5 Mark tasks complete only from matching code, test, repository, and acceptance evidence; synchronize and archive this change only after every closeout gate passes.
