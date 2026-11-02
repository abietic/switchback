## ADDED Requirements

### Requirement: Frontier provides a viewer-scoped Home dashboard
The system SHALL provide an authoritative `home_dashboard/index` Frontier View containing at most six attention Tasks, six recent WorkerSessions, six recent deliveries, the viewer's continuity-capacity usage and limit, server update time, and canonical related Task, Worker, and Artifact references. The materializer MUST enforce Tenant, viewer, and Workspace readability, order each list by server `updated_at` descending and stable identity ascending for ties, and MUST NOT project prototype-only cost, token, routing, or simulated-health data.

For the current `SingleUserLocalProfile`, the server-fixed Tenant/User identity and owner-scoped Task, WorkerSession, and Artifact rows are the authoritative Workspace-readability boundary because this profile has no dynamic Membership/RBAC or revocation state. Any future profile that introduces dynamic Workspace membership MUST provide its authoritative readable-Workspace predicate to this server-side materializer before enabling Home; the browser MUST NOT infer that predicate.

#### Scenario: Empty local product starts
- **WHEN** the local product starts before any readable Task or WorkerSession exists
- **THEN** Eventing projection deterministically bootstraps `home_dashboard/index` with empty lists, authoritative continuity capacity, and no sample records

#### Scenario: Task Worker or Artifact state changes
- **WHEN** a readable Task, WorkerSession, delivery, or Artifact owner event changes data represented by the Home View
- **THEN** the materializer refreshes the viewer-scoped projection and Frontier publishes the resulting snapshot or owner-sequenced delta, while Conversation-only events do not refresh Home until they affect a Task

#### Scenario: Owner event delivery is duplicated
- **WHEN** the same owner IntegrationEvent is delivered concurrently, after another owner stream changes Home, or against a pre-digest projection receipt whose exact source Outbox bytes are still retained
- **THEN** the materializer checks durable source identity before mutable queries, applies at most one refresh, returns the prior success for a verified semantic duplicate, may repair its missing synthetic Home refresh, and rejects a proven source-content collision

#### Scenario: Terminal legacy owner receipt has no retained source bytes
- **WHEN** an ordinary owner receipt has no source digest, its exact source Outbox bytes are unavailable, and no synthetic Home refresh exists
- **THEN** the materializer returns the prior success immediately without querying mutable owners or creating a Home View, delta, receipt, or source stream, and it never invents a digest

#### Scenario: Different owner events refresh Home concurrently
- **WHEN** two different owner events for one viewer interleave while authoritative Task, WorkerSession, Artifact, or capacity state changes
- **THEN** Home refreshes serialize by Tenant/User, each dashboard is built and committed from one consistent snapshot, and the later refresh cannot be overwritten by an earlier stale or mixed snapshot

#### Scenario: Home migration rolls back before Outbox acknowledgement
- **WHEN** new code commits an ordinary owner projection but crashes before marking its exact Outbox event published and migration 000019 is rolled back
- **THEN** rollback removes that unpublished ordinary receipt and only the delta proven by its exact projection result, deterministic delta ID, full View identity, epoch/cursor/version, and source reference; recomputes its projection source-stream position; moves affected ordinary Views to a new zero-cursor epoch; removes only exact reserved Home identities whose projection result is `home_dashboard/index`; preserves acknowledged history, unrelated deltas that merely share a source reference, and reserved-prefix lookalikes; requires old clients to resync; and permits the old binary to replay contiguously with a different materialization clock

#### Scenario: Dashboard lists exceed the display limit
- **WHEN** more than six eligible records exist for a Home dashboard list
- **THEN** the View contains the six newest records by server `updated_at` with stable identity ordering for ties

#### Scenario: Another viewer owns records
- **WHEN** Tasks, WorkerSessions, deliveries, or Artifacts are not readable by the requesting viewer in the current Tenant and Workspace scope
- **THEN** the server excludes those records and their derived counts and references from the Home View

### Requirement: Production Web uses one adaptive product shell
The system SHALL route `/`, `/tasks`, `/assistant`, `/assistant/:conversationId`, `/workers/:workerSessionId`, and `/artifacts/:artifactId` through one production React Router tree and one adaptive product shell. The `/assistant` route MUST require an explicit start-conversation command rather than creating a Conversation during navigation.

#### Scenario: User opens an overview route
- **WHEN** the user opens Home or Tasks at a desktop width
- **THEN** the one product shell renders expanded navigation and a scrollable overview canvas backed by the route's authoritative Frontier View

#### Scenario: User opens a focused execution route
- **WHEN** the user opens an Assistant Conversation, Worker detail, or Artifact detail route
- **THEN** the same product shell renders an icon rail and gives the authoritative focused surface its required execution or review workspace

#### Scenario: User opens the product on mobile
- **WHEN** the viewport requires the mobile layout
- **THEN** the same navigation is available through an accessible drawer, focus is restored after navigation, and page layout introduces no document-level horizontal scrolling

### Requirement: Theme selection is complete and presentation-only
The production Web SHALL support dark, light, and system theme modes through shared semantic surface and status tokens, SHALL persist an explicit selection only in browser-local presentation state, and MUST NOT write theme state to Task, Conversation, Workspace, governed Memory, or any domain projection. Status meaning MUST remain available through text or iconography, and reduced-motion preference MUST suppress non-essential transitions.

#### Scenario: First visit has no saved preference
- **WHEN** the browser opens the product without a stored theme selection
- **THEN** the production Web renders the dark theme as the first-visit default

#### Scenario: User selects a theme
- **WHEN** the user explicitly selects dark, light, or system mode
- **THEN** the product applies the complete semantic token set and persists only that presentation preference for later visits

#### Scenario: System preference changes
- **WHEN** the saved selection is system mode and the operating-system color preference changes
- **THEN** the product updates between its complete dark and light presentations without changing business state

### Requirement: Core pages preserve authoritative interaction contracts
The migrated Home, Tasks, Assistant, Worker, Workbench, and Artifact pages SHALL consume authoritative Frontier snapshots and SSE through the existing client replica and SHALL issue revisioned commands through their current Gateway clients. Presentation adapters MAY group, sort, label, and format authorized fields but MUST NOT manufacture domain state, infer permissions, merge durable and transient truth, or take ownership from Assistant, Task, Worker, Runtime, or Artifact contracts.

#### Scenario: Accepted command is awaiting its projection
- **WHEN** Gateway accepts a command but its causal owner event has not reached the current Frontier replica
- **THEN** the page renders command-scoped pending state, disables only the conflicting action, and preserves the last trusted business projection without optimistic state advancement

#### Scenario: Projection requires resynchronization
- **WHEN** Frontier emits `resync_required` because the epoch, cursor, gap, or base projection version cannot be replayed
- **THEN** the page suspends affected mutation commands and atomically reloads only the authoritative View instead of reconstructing state from raw events

#### Scenario: Runtime recovery is active
- **WHEN** Worker Runtime recovery replaces or fences a generation while preserving the current Session, Run, Turn, and continuity lineage
- **THEN** the Worker surface renders the authoritative failed and replacement generation evidence and keeps all commands permission- and revision-gated

#### Scenario: Artifact integrity fails
- **WHEN** an Artifact is missing, unavailable, or fails digest or integrity verification
- **THEN** Workbench and Artifact detail refuse unsafe content rendering, show the authoritative integrity outcome, and retain canonical navigation and permitted recovery actions

### Requirement: Migration has one production implementation
The completed migration SHALL keep `web/` as the only production frontend and MUST remove superseded production routing, shell, theme, view, and style owners after replacement gates pass. Production code MUST NOT import `web-demo/`, prototype mock records, simulated domain state, legacy product/prototype copy, permanent migration flags, or disabled legacy UI paths.

#### Scenario: Core migration reaches closeout
- **WHEN** Home, Tasks, Assistant, Worker, Workbench, and Artifact have authoritative data paths, automated coverage, and recorded manual acceptance evidence
- **THEN** repository checks prove one router, one shell, one theme owner, no prototype imports or mock domain records, and no superseded production implementation before the change is archived

#### Scenario: Future platform module is shown in navigation
- **WHEN** navigation presents an out-of-scope Workspace, Memory/Evolution, Skills/MCP, Observability, Admin, Web IDE, or SessionProcess module
- **THEN** it appears only in a collapsed `平台规划` group with a non-interactive `规划中` label and does not expose a fake route or imply production availability
