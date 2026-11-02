# Production Web Core Experience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the production Web core experience with the approved `web-demo`-derived information architecture and visual language while preserving the current Frontier, command, authorization, continuity, Runtime recovery, Task acceptance, and Artifact integrity owners.

**Architecture:** Keep `web/` as the only production frontend and add one viewer-scoped `home_dashboard/index` projection beside the existing Task, Assistant, Worker, and Artifact Views. Route every page through one React Router tree and one adaptive product shell, consume authoritative snapshots/SSE through the existing client replica, and translate each current production surface into feature-owned presentation modules. Complete the migration by deleting the superseded router, shell, view files, and legacy styles after browser and repository gates pass.

**Tech Stack:** Go, PostgreSQL, Eventing Projection, Frontier snapshot/SSE, TypeScript 7, React 19, React Router 7.18.1, Tailwind CSS 3.4.19, Radix Dialog 1.1.15, Vitest 4, Testing Library, Playwright 1.61, OpenSpec 1.6.0, Docker Compose.

## Global Constraints

- The approved design at `docs/superpowers/specs/2026-07-28-production-web-core-experience-design.md` is the product contract for this plan.
- `web/` remains the sole production frontend owner; `web-demo/` remains a read-only design reference.
- Never stage, edit, reset, stash, clean, or overwrite the current uncommitted `web-demo/` changes.
- Never import `web-demo`, `web-demo/src/mock`, its engine, heartbeat, fake approvals, fake telemetry, or fake domain records into `web/`.
- `clientViewReplica`, `frontierViewController`, command clients, projection revisions, `available_actions`, and server-owned identities remain authoritative.
- A `202 Accepted` response only creates pending command presentation; it never advances Task, Assistant, Worker, or Artifact business state optimistically.
- The new Home page reads only `home_dashboard/index`; it does not join multiple Views in the browser.
- Home, Tasks, Assistant, Worker, and Artifact are the complete scope. Workspace, Skills/MCP, Observability/Admin, Memory/Evolution, Web IDE, SessionProcess, and PreviewProxy remain separate changes.
- Dark is the first-visit default. Dark, light, and system modes must all work and persist only in browser-local presentation state.
- Mobile navigation is a focus-managed drawer. Assistant, Worker, and Artifact use an icon rail on desktop; Home and Tasks use the expanded sidebar.
- Existing direct Task creation remains available as an expert path.
- No permanent feature flag, legacy route owner, disabled old tree, legacy product/prototype copy, or second theme system may remain at closeout.
- Every task uses red-green-refactor: add or tighten the test first, observe the intended failure, implement the smallest production change, rerun the focused gate, then commit.
- Do not mark the OpenSpec change complete or archive it if Docker, credentials, external services, or local acceptance prevent the real product gate from running.

---

## 🧭 Target file structure

```text
web/src/
├── app/
│   ├── App.tsx
│   ├── routes.tsx
│   └── routeHandles.ts
├── api/
│   ├── contracts.ts
│   ├── homeContracts.ts
│   └── existing command and Assistant contract clients
├── components/
│   ├── layout/
│   │   ├── ProductShell.tsx
│   │   ├── ProductNavigation.tsx
│   │   └── MobileNavigation.tsx
│   └── ui/
│       ├── StatusBadge.tsx
│       ├── ViewBoundary.tsx
│       └── presentation-only primitives admitted by this slice
├── features/
│   ├── home/
│   │   ├── HomePage.tsx
│   │   └── homeAdapter.ts
│   ├── tasks/TasksPage.tsx
│   ├── assistant/AssistantConversationPage.tsx
│   ├── worker/
│   │   ├── WorkerDetailPage.tsx
│   │   ├── WorkerTimeline.tsx
│   │   └── workbench/
│   └── artifact/ArtifactDetailPage.tsx
├── providers/ThemeProvider.tsx
├── state/
│   └── existing authoritative replica/controller files
├── styles/
│   ├── tokens.css
│   ├── base.css
│   └── content.css
└── main.tsx
```

The following production files are removed by the task that replaces their last consumer:

- `web/src/app/router.ts`
- `web/src/components/AppShell.tsx`
- `web/src/views/TaskIndex.tsx`
- `web/src/views/AssistantConversation.tsx`
- `web/src/views/WorkerDetail.tsx`
- `web/src/views/WorkerTimeline.tsx`
- `web/src/views/ArtifactWorkbench.tsx`
- `web/src/views/ArtifactDetail.tsx`
- `web/src/workbench/`
- `web/src/styles.css`

---

### Task 1: Establish one OpenSpec owner for the migration

**Files:**

- Archive: `openspec/changes/m2-assistant-task-dispatch-vertical-slice/`
- Add through archive sync: `openspec/specs/assistant-conversation-dispatch/spec.md`
- Add through archive sync: `openspec/specs/assistant-web-interaction/spec.md`
- Modify through archive sync: `openspec/specs/task-session-persistence/spec.md`
- Add: `openspec/changes/production-web-core-experience/proposal.md`
- Add: `openspec/changes/production-web-core-experience/design.md`
- Add: `openspec/changes/production-web-core-experience/tasks.md`
- Add: `openspec/changes/production-web-core-experience/specs/production-web-core-experience/spec.md`

**Interfaces:**

- Consumes: completed M2 Assistant behavior and the approved Web design
- Produces: one active change that owns the Home View, shell, themes, core page migration, continuity presentation, and closeout gates
- Preserves: active `worker-web-ide-session-process`, `single-runtime-recovery`, `refine-switchback-platform-design`, and completed `trusted-full-network-profile` change state

- [ ] **Step 1: Prove the M2 baseline is complete and strictly valid**

```bash
npx --yes @fission-ai/openspec@1.6.0 list --json
npx --yes @fission-ai/openspec@1.6.0 validate m2-assistant-task-dispatch-vertical-slice --strict
```

Expected: M2 reports `34/34`; strict validation exits `0`.

- [ ] **Step 2: Archive and sync the completed M2 baseline**

```bash
npx --yes @fission-ai/openspec@1.6.0 archive m2-assistant-task-dispatch-vertical-slice --yes
```

Expected: the Assistant delta specs are synchronized into `openspec/specs/`; the change moves to `openspec/changes/archive/2026-07-28-m2-assistant-task-dispatch-vertical-slice/`.

- [ ] **Step 3: Add the production Web change artifacts**

Use `apply_patch` to create the four change artifacts. The capability spec must contain these exact requirement headings and at least the named scenarios:

```markdown
## ADDED Requirements

### Requirement: Frontier provides a viewer-scoped Home dashboard
#### Scenario: Empty local product starts
#### Scenario: Task Worker or Artifact state changes
#### Scenario: Dashboard lists exceed the display limit
#### Scenario: Another viewer owns records

### Requirement: Production Web uses one adaptive product shell
#### Scenario: User opens an overview route
#### Scenario: User opens a focused execution route
#### Scenario: User opens the product on mobile

### Requirement: Theme selection is complete and presentation-only
#### Scenario: First visit has no saved preference
#### Scenario: User selects a theme
#### Scenario: System preference changes

### Requirement: Core pages preserve authoritative interaction contracts
#### Scenario: Accepted command is awaiting its projection
#### Scenario: Projection requires resynchronization
#### Scenario: Runtime recovery is active
#### Scenario: Artifact integrity fails

### Requirement: Migration has one production implementation
#### Scenario: Core migration reaches closeout
#### Scenario: Future platform module is shown in navigation
```

`proposal.md` must list only `production-web-core-experience` as a new capability and declare the archived Assistant and current Worker/Artifact specs as dependencies rather than copying their behavior into a second owner.

- [ ] **Step 4: Validate the new owner**

```bash
npx --yes @fission-ai/openspec@1.6.0 validate production-web-core-experience --strict
npx --yes @fission-ai/openspec@1.6.0 validate --all --strict
git diff --check
```

Expected: both OpenSpec commands and the whitespace check exit `0`.

- [ ] **Step 5: Commit the ownership transition**

```bash
git add openspec/changes openspec/specs
git diff --cached --check
git commit -m "docs(openspec): define production web core migration"
```

---

### Task 2: Register `home_dashboard` as a valid Frontier View

**Files:**

- Modify: `internal/eventingprojection/application/projector.go`
- Modify: `internal/eventingprojection/application/projector_test.go`

**Interfaces:**

- Adds: `application.ViewHomeDashboard = "home_dashboard"`
- Preserves: the same projection validation, digest, store, snapshot, and SSE paths used by current View types

- [ ] **Step 1: Add the failing View validation test**

Add:

```go
func TestProjectorAcceptsHomeDashboardView(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	event := projectionEvent()
	event.Updates[0] = viewUpdate(projectionapp.ViewHomeDashboard, "index", "delta-home-index")

	if _, err := (projectionapp.Projector{Store: store}).Execute(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls=%d want=1", store.calls)
	}
}
```

- [ ] **Step 2: Run the focused test and observe the compile failure**

```bash
go test ./internal/eventingprojection/application -run TestProjectorAcceptsHomeDashboardView
```

Expected: compilation fails because `ViewHomeDashboard` does not exist.

- [ ] **Step 3: Add the constant and admit it in `validViewType`**

```go
const (
	ViewHomeDashboard          = "home_dashboard"
	ViewTaskSummary            = "task_summary"
	// existing View constants remain unchanged
)

func validViewType(value string) bool {
	return value == ViewHomeDashboard || value == ViewTaskSummary ||
		value == ViewWorkerDetail || value == ViewArtifactDetail ||
		value == ViewCommandStatus || value == ViewAssistantConversation
}
```

- [ ] **Step 4: Run the focused package gate**

```bash
go test ./internal/eventingprojection/application
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/eventingprojection/application/projector.go \
  internal/eventingprojection/application/projector_test.go
git diff --cached --check
git commit -m "feat(frontier): register home dashboard view"
```

---

### Task 3: Materialize and refresh the authoritative Home dashboard

**Files:**

- Modify: `internal/eventingprojection/adapter/postgres/materializer.go`
- Modify: `internal/eventingprojection/adapter/postgres/materializer_integration_test.go`
- Modify if bootstrap expectations require it: `internal/localproduct/app_integration_test.go`

**Interfaces:**

- Adds: `HomeDashboardViewID = "index"`
- Adds: `Materializer.homeDashboard(context.Context, tenantID) (data, refs, error)`
- Adds: an independent `local-home-dashboard-bootstrap-v1` projection event
- Refreshes: Home on every non-Conversation owner event, including the current Task and WorkerSession events that carry Artifact publication
- Reads: `tasks`, `worker_sessions`, `worker_session_continuity_quotas`, `worker_runs`, and `artifact_refs`

- [ ] **Step 1: Extend the integration test before implementation**

Add an empty-bootstrap test and extend `TestOutboxMaterializerBuildsTaskIndexAndSummaryFromCommittedState` to assert:

```go
home, err := snapshots.Get(
	ctx,
	viewer,
	projectionapp.ViewHomeDashboard,
	projectionpostgres.HomeDashboardViewID,
)
if err != nil {
	t.Fatal(err)
}
var dashboard struct {
	AttentionTasks  []map[string]any `json:"attention_tasks"`
	RecentWorkers   []map[string]any `json:"recent_workers"`
	RecentDeliveries []map[string]any `json:"recent_deliveries"`
	Capacity struct {
		Used  int `json:"used"`
		Limit int `json:"limit"`
	} `json:"capacity"`
	UpdatedAt string `json:"updated_at"`
}
if err := json.Unmarshal(home.Data, &dashboard); err != nil {
	t.Fatal(err)
}
```

The fixture assertions must prove:

- an empty bootstrap returns three non-nil empty arrays and capacity `0/10`;
- only `waiting_user` and `delivered` Tasks enter `attention_tasks`;
- only `delivered` and `completed` Tasks enter `recent_deliveries`;
- at most six records appear in each list;
- ties sort by `updated_at DESC` then stable identity `ASC`;
- another Tenant or user never appears;
- an Artifact publication refreshes the Home projection and adds its canonical Artifact href;
- quota uses `reserved_count` and `quota_limit`, not a browser calculation.

- [ ] **Step 2: Run the focused integration test and observe the failure**

```bash
env SWITCHBACK_TEST_DATABASE_URL='postgres://switchback:switchback-test@127.0.0.1:58778/switchback_test?sslmode=disable' \
  go test -tags=integration ./internal/eventingprojection/adapter/postgres \
  -run 'TestMaterializerBootstrapsEmptyHomeDashboard|TestOutboxMaterializerBuildsTaskIndexAndSummaryFromCommittedState'
```

Expected: compilation fails because the Home constant, ID, and materialization do not exist.

- [ ] **Step 3: Preserve the existing Task bootstrap identity**

Refactor `Bootstrap` into two idempotent helpers. Do not add Home to `local-task-index-bootstrap-v1`; that would change the digest of an already durable source event.

```go
const (
	TaskIndexViewID      = "index"
	HomeDashboardViewID = "index"
)

func (m Materializer) Bootstrap(ctx context.Context, tenantID string) error {
	if err := m.validateBootstrapDependencies(tenantID); err != nil {
		return err
	}
	if _, err := NewStore(m.Pool).ReconcileTerminalDisplayHistory(ctx, tenantID, m.UserID); err != nil {
		return err
	}
	if err := m.bootstrapTaskIndex(ctx, tenantID); err != nil {
		return err
	}
	return m.bootstrapHomeDashboard(ctx, tenantID)
}
```

`bootstrapHomeDashboard` uses source event, stream, trace, causation, and idempotency identity `local-home-dashboard-bootstrap-v1`, AggregateType `HomeDashboard`, and one `ViewHomeDashboard/index` update.

- [ ] **Step 4: Implement the page-shaped query**

Use typed internal rows, not untyped client-side reconstruction:

```go
type homeTaskItem struct {
	TaskID          string `json:"task_id"`
	Title           string `json:"title"`
	Workspace       string `json:"workspace"`
	Status          string `json:"status"`
	StatusTone      string `json:"status_tone"`
	WorkerSessionID string `json:"worker_session_id,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

type homeWorkerItem struct {
	WorkerSessionID string `json:"worker_session_id"`
	Workspace       string `json:"workspace"`
	Status          string `json:"status"`
	StatusTone      string `json:"status_tone"`
	RuntimeGeneration uint64 `json:"runtime_generation"`
	BoundTaskID     string `json:"bound_task_id,omitempty"`
	TaskTitle       string `json:"task_title,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}
```

The three list queries must use these predicates and ordering:

```sql
-- attention_tasks
WHERE tenant_id=$1 AND initiator_user_id=$2
  AND status IN ('waiting_user','delivered')
ORDER BY updated_at DESC, task_id ASC
LIMIT 6

-- recent_workers
WHERE session.tenant_id=$1 AND session.user_id=$2
ORDER BY session.updated_at DESC, session.worker_session_id ASC
LIMIT 6

-- recent_deliveries
WHERE tenant_id=$1 AND initiator_user_id=$2
  AND status IN ('delivered','completed')
ORDER BY updated_at DESC, task_id ASC
LIMIT 6
```

Read capacity from `worker_session_continuity_quotas`. If the quota row does not exist, return `used=0` and the current policy default `limit=10`. For each delivery, read viewer-scoped `artifact_refs` ordered by `visible_at ASC, artifact_id ASC` and emit:

```go
map[string]any{
	"artifact_id": artifactID,
	"artifact_type": artifactType,
	"href": "/api/v1/views/artifact_detail/" + artifactID,
}
```

Return deduplicated `RelatedRef` entries for every Task, Worker, and Artifact included in the page data.

- [ ] **Step 5: Refresh Home without changing Conversation-only behavior**

At the start of `buildUpdates`, preserve the current `Conversation` exclusion and append both overview updates for other owner events:

```go
if event.AggregateType != "Conversation" {
	indexData, err := m.taskIndex(ctx, event.TenantID)
	if err != nil {
		return nil, err
	}
	homeData, homeRefs, err := m.homeDashboard(ctx, event.TenantID)
	if err != nil {
		return nil, err
	}
	updates = append(updates,
		m.update(event, projectionapp.ViewTaskSummary, TaskIndexViewID, indexData, nil, nil),
		m.update(event, projectionapp.ViewHomeDashboard, HomeDashboardViewID, homeData, nil, homeRefs),
	)
}
```

- [ ] **Step 6: Run projection and composition gates**

```bash
gofmt -w \
  internal/eventingprojection/application/projector.go \
  internal/eventingprojection/application/projector_test.go \
  internal/eventingprojection/adapter/postgres/materializer.go \
  internal/eventingprojection/adapter/postgres/materializer_integration_test.go
go test ./internal/eventingprojection/application
env SWITCHBACK_TEST_DATABASE_URL='postgres://switchback:switchback-test@127.0.0.1:58778/switchback_test?sslmode=disable' \
  go test -tags=integration ./internal/eventingprojection/adapter/postgres ./internal/localproduct
```

Expected: all tests pass; the existing Task index and Assistant/Worker materialization tests remain green.

- [ ] **Step 7: Commit**

```bash
git add internal/eventingprojection internal/localproduct/app_integration_test.go
git diff --cached --check
git commit -m "feat(frontier): materialize home dashboard"
```

---

### Task 4: Add runtime-validated Home contracts

**Files:**

- Modify: `web/src/api/contracts.ts`
- Add: `web/src/api/homeContracts.ts`
- Add: `web/src/api/homeContracts.test.ts`
- Modify: `web/tests/frontierMock.ts`

**Interfaces:**

- Adds: `HomeDashboardData`, `HomeTaskItem`, `HomeWorkerItem`, `HomeDeliveryItem`
- Adds: `parseHomeSnapshot(value)` and `parseHomeDelta(value)`
- Preserves: `ProjectionSnapshot`, `ProjectionDelta`, `ActionRef`, `RelatedRef`, and replica behavior

- [ ] **Step 1: Write contract tests for valid and invalid identity/data**

Tests must cover:

```typescript
expect(parseHomeSnapshot(homeSnapshot()).data.capacity).toEqual({ used: 3, limit: 10 });
expect(() => parseHomeSnapshot({ ...homeSnapshot(), view_type: "task_summary" }))
  .toThrow("Home snapshot identity is invalid");
expect(() => parseHomeSnapshot({
  ...homeSnapshot(),
  data: { ...homeSnapshot().data, attention_tasks: [{ status: "running" }] },
})).toThrow("attention task status");
```

Add equivalent delta identity and payload-data cases.

- [ ] **Step 2: Run the failing test**

```bash
npm --prefix web test -- homeContracts.test.ts
```

Expected: compilation fails because the Home contract parser does not exist.

- [ ] **Step 3: Add the exact TypeScript contract**

Use the approved data shape. `HomeTaskItem.status` is exactly `"waiting_user" | "delivered"` and `status_tone` is exactly `"waiting"`. `HomeDeliveryItem.status` is exactly `"delivered" | "completed"`. All arrays are required; missing arrays are invalid rather than silently replaced with samples.

`parseHomeSnapshot` must require:

```typescript
snapshot.schema_version === "frontier.projection_snapshot.v1"
snapshot.view_type === "home_dashboard"
snapshot.view_id === "index"
```

`parseHomeDelta` must require the corresponding delta schema and identity. Both parsers validate every item, numeric capacity field, href, and timestamp string before returning typed data.

- [ ] **Step 4: Add a real Home fixture to the Frontier browser mock**

Extend `snapshotFor` with:

```typescript
if (viewType === "home_dashboard" && viewId === "index") {
  return baseSnapshot<HomeDashboardData>(viewType, viewId, demoHomeDashboard);
}
```

The fixture may reuse `demoTasks` and `demoWorkerSnapshot` values, but it must construct a valid `HomeDashboardData` object and may not add cost, token, heartbeat, or approval fields.

- [ ] **Step 5: Run the contract and frontend gates**

```bash
npm --prefix web test -- homeContracts.test.ts
npm --prefix web run build
```

Expected: tests and TypeScript/Vite build pass.

- [ ] **Step 6: Commit**

```bash
git add web/src/api/contracts.ts web/src/api/homeContracts.ts \
  web/src/api/homeContracts.test.ts web/tests/frontierMock.ts
git diff --cached --check
git commit -m "feat(web): add home dashboard contracts"
```

---

### Task 5: Install the minimal UI foundation and complete theme owner

**Files:**

- Modify: `web/package.json`
- Modify: `web/package-lock.json`
- Add: `web/tailwind.config.cjs`
- Add: `web/postcss.config.js`
- Add: `web/src/lib/cn.ts`
- Add: `web/src/providers/ThemeProvider.tsx`
- Add: `web/src/providers/ThemeProvider.test.tsx`
- Add: `web/src/styles/tokens.css`
- Add: `web/src/styles/base.css`
- Add: `web/src/styles/content.css`
- Modify: `web/src/main.tsx`

**Interfaces:**

- Adds dependencies: `react-router@7.18.1`, `@radix-ui/react-dialog@1.1.15`, `clsx@2.1.1`, `tailwind-merge@3.4.0`
- Adds dev dependencies: `tailwindcss@3.4.19`, `postcss@8.5.24`, `autoprefixer@10.4.23`
- Adds presentation-only preference: `"dark" | "light" | "system"` under local-storage key `switchback.theme`

The exact React Router and PostCSS pins were security-refreshed after the Task 5 audit. PostCSS 8.5.24 clears the admitted direct PostCSS advisories; React Router 7.18.1 clears the SPA-relevant advisories in the original pin. The remaining npm high advisory applies to React Router RSC mode, which this Vite SPA does not enable; it remains recorded rather than being described as zero vulnerabilities or forcing an unapproved major upgrade.

- [ ] **Step 1: Add failing theme tests**

The tests must prove:

- absent storage applies `dark` and writes nothing;
- explicit light and dark selections persist;
- system mode follows `matchMedia("(prefers-color-scheme: dark)")`;
- a system media change updates the resolved class;
- unmount removes the media listener;
- theme controls have accessible names.

- [ ] **Step 2: Run the failing test**

```bash
npm --prefix web test -- ThemeProvider.test.tsx
```

Expected: compilation fails because `ThemeProvider` does not exist.

- [ ] **Step 3: Install only the admitted dependencies**

```bash
npm --prefix web install --save-exact \
  react-router@7.18.1 \
  @radix-ui/react-dialog@1.1.15 \
  clsx@2.1.1 \
  tailwind-merge@3.4.0
npm --prefix web install --save-dev --save-exact \
  tailwindcss@3.4.19 \
  postcss@8.5.24 \
  autoprefixer@10.4.23
```

Expected: only `web/package.json` and `web/package-lock.json` change.

- [ ] **Step 4: Implement theme ownership**

`ThemeProvider` must resolve and apply the choice like this:

```typescript
export type ThemePreference = "dark" | "light" | "system";

function resolveTheme(
  preference: ThemePreference,
  systemDark: boolean,
): "dark" | "light" {
  return preference === "system" ? (systemDark ? "dark" : "light") : preference;
}

root.dataset.theme = resolved;
root.classList.toggle("dark", resolved === "dark");
root.style.colorScheme = resolved;
```

Do not persist the initial default. Persist only an explicit user selection. Do not use Task, Assistant, Memory, or backend storage.

- [ ] **Step 5: Add semantic tokens and Tailwind mapping**

`tokens.css` defines complete dark and light values for background, foreground, surface/card, muted, border/input/ring, primary, destructive, sidebar, and all five status tones. `tailwind.config.cjs` maps Tailwind colors to those CSS variables and uses `darkMode: ["class"]`.

`base.css` must include:

```css
@tailwind base;
@tailwind components;
@tailwind utilities;

html,
body,
#root {
  min-height: 100%;
}

@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
}
```

`content.css` is limited to Markdown, code, Diff, tree, and bounded-scroll integration styles that cannot be expressed cleanly at the call site.

- [ ] **Step 6: Wire the provider and run focused gates**

`main.tsx` imports `tokens.css`, `base.css`, and `content.css`, then wraps the application with `ThemeProvider`.

```bash
npm --prefix web test -- ThemeProvider.test.tsx
npm --prefix web run build
```

Expected: tests and build pass.

- [ ] **Step 7: Commit**

```bash
git add web/package.json web/package-lock.json web/tailwind.config.cjs \
  web/postcss.config.js web/src/lib web/src/providers web/src/styles web/src/main.tsx
git diff --cached --check
git commit -m "feat(web): add theme and style foundation"
```

---

### Task 6: Replace the router and shell with one adaptive owner

**Files:**

- Modify: `web/src/app/App.tsx`
- Add: `web/src/app/routes.tsx`
- Add: `web/src/app/routeHandles.ts`
- Add: `web/src/app/routes.test.tsx`
- Add: `web/src/components/layout/ProductShell.tsx`
- Add: `web/src/components/layout/ProductNavigation.tsx`
- Add: `web/src/components/layout/MobileNavigation.tsx`
- Add: `web/src/components/layout/ProductShell.test.tsx`
- Add: `web/src/components/ui/ViewBoundary.tsx`
- Delete: `web/src/app/router.ts`
- Delete: `web/src/components/AppShell.tsx`

**Interfaces:**

- Routes: `/`, `/tasks`, `/assistant`, `/assistant/:conversationId`, `/workers/:workerSessionId`, `/artifacts/:artifactId`
- Shell modes: `overview` for `/` and `/tasks`; `focused` for Assistant conversation, Worker, and Artifact
- Mobile: Radix Dialog drawer with focus trap, escape close, overlay close, focus restoration, and route-change close

- [ ] **Step 1: Add route and shell behavior tests**

Tests must prove:

```typescript
expect(routeHandle("/").shellMode).toBe("overview");
expect(routeHandle("/tasks").shellMode).toBe("overview");
expect(routeHandle("/assistant/conversation-1").shellMode).toBe("focused");
expect(routeHandle("/workers/session-1").shellMode).toBe("focused");
expect(routeHandle("/artifacts/artifact-1").shellMode).toBe("focused");
```

The shell test must also prove active navigation, drawer open/close, focus restoration, and that `平台规划` labels are non-interactive and marked `规划中`.

- [ ] **Step 2: Run the failing tests**

```bash
npm --prefix web test -- routes.test.tsx ProductShell.test.tsx
```

Expected: compilation fails because the route tree and shell components do not exist.

- [ ] **Step 3: Introduce one React Router**

`App.tsx` becomes:

```tsx
import { RouterProvider } from "react-router";
import { createAppRouter } from "./routes";

const router = createAppRouter();

export function App() {
  return <RouterProvider router={router} />;
}
```

`createAppRouter(initialEntries?)` returns a browser router in production and a memory router for tests. Route elements continue to use the existing View hooks and current feature/view components during this commit. `/` and `/tasks` may both render the current Task index until Task 7 switches `/` to Home; there is still one route owner.

`/assistant` renders a start page with a button that calls `createAssistantConversation` and navigates only after the accepted response returns the Conversation identity. Opening the route itself performs no command.

- [ ] **Step 4: Implement the adaptive shell**

The route handle is:

```typescript
export type ShellMode = "overview" | "focused";
export interface SwitchbackRouteHandle {
  shellMode: ShellMode;
  title: string;
}
```

`ProductShell` renders one `<Outlet />`, one navigation definition, one theme selector, and responsive modes:

- width `>= 1024px`: 208px overview sidebar or 64px focused rail;
- width `< 1024px`: no persistent sidebar; drawer trigger remains visible;
- drawer closes when `location.pathname` changes;
- no page implements its own product navigation.

- [ ] **Step 5: Remove the replaced owners and run gates**

```bash
npm --prefix web test -- routes.test.tsx ProductShell.test.tsx
npm --prefix web test
npm --prefix web run build
rg -n 'from "../app/router"|from "../components/AppShell"' web/src
```

Expected: tests/build pass; the final `rg` returns no matches.

- [ ] **Step 6: Commit**

```bash
git add web/src/app web/src/components/layout web/src/components/ui/ViewBoundary.tsx
git add -u web/src/app/router.ts web/src/components/AppShell.tsx
git diff --cached --check
git commit -m "feat(web): add adaptive product shell"
```

---

### Task 7: Deliver production Home and migrate Tasks

**Files:**

- Add: `web/src/features/home/homeAdapter.ts`
- Add: `web/src/features/home/homeAdapter.test.ts`
- Add: `web/src/features/home/HomePage.tsx`
- Add: `web/src/features/home/HomePage.test.tsx`
- Move: `web/src/views/TaskIndex.tsx` to `web/src/features/tasks/TasksPage.tsx`
- Add: `web/src/features/tasks/TasksPage.test.tsx`
- Modify: `web/src/app/routes.tsx`
- Modify: `web/src/components/layout/ProductNavigation.tsx`
- Delete after move: `web/src/views/TaskIndex.tsx`

**Interfaces:**

- Home consumes only `useFrontierView<HomeDashboardData>("home_dashboard", "index", parseHomeSnapshot, parseHomeDelta)`
- Tasks consumes only `task_summary/index`
- Home navigation uses canonical `/tasks`, `/workers/:id`, and `/artifacts/:id` routes
- Direct Task creation and explicit Assistant creation preserve current command clients

- [ ] **Step 1: Add failing adapter and page tests**

The adapter tests prove stable presentation grouping without new authority:

```typescript
expect(adaptHome(dashboard).attention).toHaveLength(2);
expect(adaptHome(dashboard).capacity.percent).toBe(30);
expect(adaptHome(dashboard).recentWorkers[0].href).toBe("/workers/session-1");
expect(adaptHome(dashboard).deliveries[0].artifacts[0].href)
  .toBe("/artifacts/artifact-1");
```

The Home page test covers loaded, empty, loading skeleton, and error/retry states. The Tasks page test covers active/completed/all filters, direct Task form, Assistant start, and exact Worker navigation.

- [ ] **Step 2: Run the failing tests**

```bash
npm --prefix web test -- homeAdapter.test.ts HomePage.test.tsx TasksPage.test.tsx
```

Expected: tests fail because the feature modules do not exist.

- [ ] **Step 3: Implement the Home adapter and route**

The adapter may compute display-only values such as capacity percentage and formatted timestamps. It must not infer status, permissions, missing records, or Worker health.

`HomePage` renders:

- attention Tasks with explicit waiting/delivered labels;
- recent Workers with status, workspace, generation, and optional Task;
- deliveries with acceptance mode and canonical Artifact links;
- capacity used/limit;
- explicit `Start Assistant conversation`;
- actionable empty states without sample records.

Switch `/` to `HomePage` and keep `/tasks` as the only Task index route.

- [ ] **Step 4: Move and restyle Tasks without changing commands**

Move the current Task index into `features/tasks/TasksPage.tsx`. Keep:

- active/completed/all filters;
- direct TaskSpec form and `newTaskIdempotencyKey`;
- exact `worker_session_id` navigation;
- server status and status tone;
- capacity values from the projection.

Translate layout and controls to the new tokens/shell. Do not create a Task detail route that the backend does not own.

- [ ] **Step 5: Run feature and build gates**

```bash
npm --prefix web test -- homeAdapter.test.ts HomePage.test.tsx TasksPage.test.tsx
npm --prefix web test
npm --prefix web run build
```

Expected: all tests and build pass.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/home web/src/features/tasks web/src/app/routes.tsx \
  web/src/components/layout/ProductNavigation.tsx
git add -u web/src/views/TaskIndex.tsx
git diff --cached --check
git commit -m "feat(web): deliver home and tasks experience"
```

---

### Task 8: Migrate the Assistant surface without weakening readiness

**Files:**

- Move: `web/src/views/AssistantConversation.tsx` to `web/src/features/assistant/AssistantConversationPage.tsx`
- Move: `web/src/views/AssistantConversation.test.tsx` to `web/src/features/assistant/AssistantConversationPage.test.tsx`
- Modify: `web/src/app/routes.tsx`
- Modify if layout-only styles require it: `web/src/styles/content.css`

**Interfaces:**

- Preserves: `parseAssistantSnapshot`, `parseAssistantDelta`, all Assistant command clients, `available_actions`, exact revisions, creation-intent evidence, pending command tracking, Workspace selection, clarification, checkpoint, retry, and handoff references
- Changes: presentation, responsive layout, and shell integration only

- [ ] **Step 1: Strengthen the moved test before changing presentation**

Add assertions that:

- confirmation is unavailable without the exact projected `assistant.confirm_task` action;
- checkpoint buttons are exactly the projected allowed actions;
- pending confirmation disables only the conflicting action and retains the draft;
- Workspace selection remains explicit;
- failed retry renders sanitized diagnostics;
- Task and Worker handoff links are canonical;
- mobile renders one content column without document-level horizontal overflow.

- [ ] **Step 2: Run the strengthened test against the old component**

```bash
npm --prefix web test -- AssistantConversationPage.test.tsx
```

Expected: at least the new responsive or semantic assertion fails before the migrated markup is implemented.

- [ ] **Step 3: Migrate the page**

Use the focused shell mode. Keep the thread as the primary region and render the readiness/context panel as a responsive secondary region. Preserve every current command payload and action check; presentation adapters may format only labels, sections, and timestamps.

The page must expose:

- run state and pending projection state;
- ordered visible Turns;
- message composer;
- Workspace evidence;
- clarification blocker and checkpoint actions;
- visible assumptions and exact TaskDraft preview;
- explicit exact confirmation;
- Task/Worker handoff.

- [ ] **Step 4: Run Assistant contract, component, and browser gates**

```bash
npm --prefix web test -- assistantContracts.test.ts AssistantConversationPage.test.tsx
npm --prefix web run test:browser -- tests/assistant-flow.spec.ts
npm --prefix web run build
```

Expected: all commands pass; no Assistant command request shape changes.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/assistant web/src/app/routes.tsx web/src/styles/content.css
git add -u web/src/views/AssistantConversation.tsx \
  web/src/views/AssistantConversation.test.tsx
git diff --cached --check
git commit -m "feat(web): migrate assistant experience"
```

---

### Task 9: Migrate Worker execution and Workbench as one authority-preserving slice

**Files:**

- Move: `web/src/views/WorkerDetail.tsx` to `web/src/features/worker/WorkerDetailPage.tsx`
- Move: `web/src/views/WorkerDetail.test.tsx` to `web/src/features/worker/WorkerDetailPage.test.tsx`
- Move: `web/src/views/WorkerTimeline.tsx` and test to `web/src/features/worker/`
- Move: `web/src/views/ArtifactWorkbench.tsx` and test to `web/src/features/worker/workbench/`
- Move: `web/src/workbench/ArtifactTreeAdapter.ts` to `web/src/features/worker/workbench/ArtifactTreeAdapter.ts`
- Move: `web/src/workbench/PatchReviewAdapter.ts` and test to `web/src/features/worker/workbench/`
- Move: `web/src/components/ApprovalControls.tsx` and test to `web/src/features/worker/components/`
- Move: `web/src/components/WorkerCommandComposer.tsx` and test to `web/src/features/worker/components/`
- Modify: `web/src/app/routes.tsx`
- Modify if integration styles require it: `web/src/styles/content.css`

**Interfaces:**

- Preserves: durable/transient separation, timeline cursor, runtime generation, recovery evidence, approvals, queue/interrupt/steer, Task accept/rework/continue, retry semantics, Artifact refs, tree, Diff, report, and Files
- Does not add: Web IDE, Preview, SessionProcess, fake terminal, cost, token, or heartbeat state

- [ ] **Step 1: Strengthen Worker tests before moving code**

Add assertions for:

- recovery phase, failed/replacement generation, continuity status, and sanitized outcome;
- commands absent when the exact `available_actions` entry disappears;
- stale continuation preserves draft and requires an explicit resend;
- unknown delivery retry reuses the same idempotency key;
- durable timeline and transient overlay render separately;
- Workbench Report/Diff/Files use authoritative Artifact data;
- focused rail layout leaves a bounded scrolling execution region.

- [ ] **Step 2: Run the focused tests and capture the expected presentation failure**

```bash
npm --prefix web test -- WorkerDetailPage.test.tsx WorkerTimeline.test.tsx \
  ArtifactWorkbench.test.tsx WorkerCommandComposer.test.tsx ApprovalControls.test.tsx
```

Expected: at least the new shell/layout assertion fails.

- [ ] **Step 3: Move and migrate the feature atomically**

Update imports in the same patch as each move. Keep the current `WorkerRoute` command functions and `replica.markCommandPending` calls in `routes.tsx`. The page reads only `WorkerDetailData`, transient display, projected actions, related refs, and command results.

Reserve one disabled visual seam labeled `Workbench` for future tabs only if it is a current container label. Do not render a `Web IDE` tab or route.

- [ ] **Step 4: Run Worker unit, browser, and build gates**

```bash
npm --prefix web test -- WorkerDetailPage.test.tsx WorkerTimeline.test.tsx \
  ArtifactWorkbench.test.tsx PatchReviewAdapter.test.ts \
  WorkerCommandComposer.test.tsx ApprovalControls.test.tsx
npm --prefix web run test:browser -- tests/worker-flow.spec.ts
npm --prefix web run build
```

Expected: all tests and build pass; existing browser command tuples remain unchanged.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/worker web/src/app/routes.tsx web/src/styles/content.css
git add -u web/src/views/WorkerDetail.tsx web/src/views/WorkerDetail.test.tsx \
  web/src/views/WorkerTimeline.tsx web/src/views/WorkerTimeline.test.tsx \
  web/src/views/ArtifactWorkbench.tsx web/src/views/ArtifactWorkbench.test.tsx \
  web/src/workbench web/src/components/ApprovalControls.tsx \
  web/src/components/ApprovalControls.test.tsx \
  web/src/components/WorkerCommandComposer.tsx \
  web/src/components/WorkerCommandComposer.test.tsx
git diff --cached --check
git commit -m "feat(web): migrate worker workbench experience"
```

---

### Task 10: Migrate Artifact detail and shared status presentation

**Files:**

- Move: `web/src/views/ArtifactDetail.tsx` to `web/src/features/artifact/ArtifactDetailPage.tsx`
- Move: `web/src/views/ArtifactDetail.test.tsx` to `web/src/features/artifact/ArtifactDetailPage.test.tsx`
- Move: `web/src/components/StatusBadge.tsx` to `web/src/components/ui/StatusBadge.tsx`
- Modify: all StatusBadge imports
- Modify: `web/src/app/routes.tsx`
- Modify: `web/src/styles/content.css`

**Interfaces:**

- Preserves: availability outcome, review manifest, digest, size, completeness, download permission, Markdown/patch rendering, and canonical Worker return path
- Blocks: content actions when integrity is missing, invalid, oversized, binary, or checksum-failed according to the current manifest

- [ ] **Step 1: Strengthen Artifact tests**

Tests must cover:

- online report rendering;
- patch rendering inside a bounded horizontal scroller;
- checksum failure warning with no download;
- missing and oversized outcomes;
- provenance includes Run, Turn, generation, digest, and completeness;
- Worker return navigation uses the projected related ref;
- status tone always includes visible text or an accessible label.

- [ ] **Step 2: Run the failing focused test**

```bash
npm --prefix web test -- ArtifactDetailPage.test.tsx
```

Expected: at least the new semantic or responsive assertion fails before migrated markup.

- [ ] **Step 3: Move and migrate the Artifact surface**

Keep `ArtifactDetailData` and `review_manifest` authoritative. Do not derive download permission from file extension or content. Use the focused shell and one bounded content viewport.

- [ ] **Step 4: Run Artifact and build gates**

```bash
npm --prefix web test -- ArtifactDetailPage.test.tsx SafeMarkdown.test.tsx
npm --prefix web run build
```

Expected: all tests and build pass.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/artifact web/src/components/ui/StatusBadge.tsx \
  web/src/app/routes.tsx web/src/styles/content.css
git add -u web/src/views/ArtifactDetail.tsx web/src/views/ArtifactDetail.test.tsx \
  web/src/components/StatusBadge.tsx
git diff --cached --check
git commit -m "feat(web): migrate artifact detail experience"
```

---

### Task 11: Make continuity, loading, failure, and accessibility states uniform

**Files:**

- Modify: `web/src/state/clientViewReplica.ts`
- Modify: `web/src/state/clientViewReplica.test.ts`
- Modify: `web/src/state/frontierViewController.ts`
- Modify: `web/src/state/frontierViewController.test.ts`
- Modify: `web/src/components/ui/ViewBoundary.tsx`
- Add: `web/src/components/ui/ViewBoundary.test.tsx`
- Modify: `web/src/features/home/HomePage.tsx`
- Modify: `web/src/features/tasks/TasksPage.tsx`
- Modify: `web/src/features/assistant/AssistantConversationPage.tsx`
- Modify: `web/src/features/worker/WorkerDetailPage.tsx`
- Modify: `web/src/features/artifact/ArtifactDetailPage.tsx`

**Interfaces:**

- Presents existing states: initial snapshot, reconnecting, `resync_required`, `client_upgrade_required`, accepted command pending, stale revision, command rejection, permission removal, Runtime recovery, empty View
- Preserves: last trusted snapshot during reconnect; mutation suspension during resync/upgrade

- [ ] **Step 1: Add failing state-machine and boundary tests**

Prove:

- disconnect retains `state.data` and exposes reconnecting status;
- `resync_required` disables mutations until replacement snapshot;
- `client_upgrade_required` blocks interaction and cannot be dismissed;
- action removal immediately removes or disables the corresponding control;
- error retry calls the controller refresh once;
- skeletons have `role="status"` and errors have `role="alert"`;
- drawer and page focus order are keyboard reachable;
- no color-only status meaning exists.

- [ ] **Step 2: Run the failing tests**

```bash
npm --prefix web test -- clientViewReplica.test.ts frontierViewController.test.ts \
  ViewBoundary.test.tsx
```

Expected: the new presentation assertions fail while existing replica correctness tests remain green.

- [ ] **Step 3: Add presentation metadata without moving business ownership**

The replica already exposes authoritative control state through `sync`. Add only the missing transport presentation field:

```typescript
connection: "connecting" | "connected" | "reconnecting";
```

Keep `sync: "empty" | "ready" | "resync_required" | "client_upgrade_required"` unchanged. Derive `connection` only from controller/EventSource lifecycle, and derive `interactionBlocked` at the presentation boundary as `state.sync !== "ready"`. Do not infer domain transitions or recreate removed `available_actions`.

`ViewBoundary` renders structural skeletons, reconnecting banners, blocking upgrade state, actionable errors, and empty states while leaving trusted data visible during temporary disconnect.

- [ ] **Step 4: Run all frontend unit and build gates**

```bash
npm --prefix web test
npm --prefix web run build
```

Expected: all tests and build pass.

- [ ] **Step 5: Commit**

```bash
git add web/src/state web/src/components/ui web/src/features
git diff --cached --check
git commit -m "feat(web): unify continuity and failure states"
```

---

### Task 12: Expand browser and visual acceptance across routes, themes, and widths

**Files:**

- Modify: `web/tests/frontierMock.ts`
- Add: `web/tests/home-flow.spec.ts`
- Modify: `web/tests/assistant-flow.spec.ts`
- Modify: `web/tests/worker-flow.spec.ts`
- Modify: `web/tests/visual.spec.ts`
- Add: `web/tests/continuity-flow.spec.ts`
- Modify if needed: `web/playwright.config.ts`

**Interfaces:**

- Browser matrix: Home, Tasks, Assistant start/conversation, Worker, Workbench, Artifact
- Viewports: desktop `1440x900` and mobile `390x844`
- Themes: dark and light
- Continuity: reconnect, resync, client upgrade, pending, stale, permission removal, recovery

- [ ] **Step 1: Write the new browser expectations before adjusting UI**

`home-flow.spec.ts` must:

1. open `/` and read `home_dashboard/index`;
2. navigate to `/tasks`;
3. open a Worker from a Home item;
4. open an Artifact from a delivery;
5. start Assistant only after clicking the explicit action.

`continuity-flow.spec.ts` must inject SSE control events and prove reconnect, resync, and upgrade presentation while retaining or blocking the correct interaction.

`visual.spec.ts` captures Home, Assistant, Worker, and Diff in the four desktop/mobile × dark/light combinations. Every capture checks:

```typescript
expect(document.body.scrollWidth).toBeLessThanOrEqual(
  document.documentElement.clientWidth + 1,
);
```

Diff/code regions may overflow only inside their labeled bounded scroller.

- [ ] **Step 2: Run the browser suite and observe failures**

```bash
npm --prefix web run test:browser
```

Expected: new tests fail until selectors, fixture deltas, and remaining responsive details are complete.

- [ ] **Step 3: Repair only product-code failures exposed by the tests**

Do not weaken assertions or replace product data with static DOM. Update production components only where the browser evidence proves a layout, focus, theme, routing, or continuity defect.

- [ ] **Step 4: Run the complete frontend gate**

```bash
npm --prefix web test
npm --prefix web run build
npm --prefix web run test:browser
```

Expected: all commands pass; Playwright attaches the sixteen core visual captures plus existing route-specific evidence.

- [ ] **Step 5: Commit**

```bash
git add web/tests web/playwright.config.ts web/src
git diff --cached --check
git commit -m "test(web): cover production core experience"
```

---

### Task 13: Remove legacy UI, prove repository integrity, and close the change

**Files:**

- Delete: `web/src/styles.css`
- Delete any empty superseded directories under `web/src/views/` and `web/src/workbench/`
- Modify: `openspec/changes/production-web-core-experience/tasks.md`
- Add: `docs/status/status-2026-07-28-production-web-core-experience.md`
- Modify if operational steps changed: `docs/operations/local-product.md`
- Archive after all gates pass: `openspec/changes/production-web-core-experience/`

**Interfaces:**

- Produces: one router, one shell, one theme owner, one implementation per in-scope page, repository verification evidence, and local Compose acceptance evidence
- Removes: old visual system, old root Task route, legacy style entry, dead imports, and migration-only compatibility code

- [ ] **Step 1: Delete the last superseded production artifacts**

Use `apply_patch` for remaining code/style deletions. Remove empty directories only after `rg --files` proves no tracked consumers remain.

- [ ] **Step 2: Run static migration shields**

```bash
if rg -n 'web-demo|web-demo/src/mock|mock/engine' web/src web/tests; then exit 1; fi
make publication-check
test ! -e web/src/app/router.ts
test ! -e web/src/components/AppShell.tsx
test ! -e web/src/styles.css
test ! -d web/src/views
test ! -d web/src/workbench
git diff --check
```

Expected: every command exits `0`.

- [ ] **Step 3: Run OpenSpec and complete repository gates**

```bash
npx --yes @fission-ai/openspec@1.6.0 validate production-web-core-experience --strict
npm --prefix web test
npm --prefix web run build
npm --prefix web run test:browser
env GOCACHE=/tmp/switchback-go-cache \
  SWITCHBACK_TEST_DATABASE_URL='postgres://switchback:switchback-test@127.0.0.1:58778/switchback_test?sslmode=disable' \
  make verify
```

Expected: every command exits `0`. Record command, timestamp, exit status, and concise output summary in the status document.

- [ ] **Step 4: Run local Compose acceptance**

```bash
make local-config
make local-up
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml ps
```

Perform and record this exact flow:

1. open Home and confirm empty or current authoritative data;
2. explicitly create an Assistant Conversation;
3. send an ambiguous request and observe clarification or a ready draft;
4. explicitly confirm the exact draft;
5. observe Task and Worker progress through Frontier;
6. open the produced report or patch;
7. perform an available accept or rework action;
8. return Home and observe the authoritative dashboard update;
9. repeat Home, Assistant, Worker, and Artifact smoke checks in dark/light and desktop/mobile.

If Docker, model credentials, provider access, or another external dependency blocks the flow, record the exact blocker and leave the OpenSpec change active.

- [ ] **Step 5: Complete the change checklist only from evidence**

Mark every `production-web-core-experience/tasks.md` item complete only when its matching code, test, and status evidence exists. Then run:

```bash
npx --yes @fission-ai/openspec@1.6.0 validate production-web-core-experience --strict
npx --yes @fission-ai/openspec@1.6.0 list --json
```

Expected: the change is strictly valid and reports all tasks complete.

- [ ] **Step 6: Commit closeout evidence**

```bash
git add web docs/status docs/operations \
  openspec/changes/production-web-core-experience/tasks.md
git diff --cached --check
git commit -m "chore(web): close production core migration"
```

- [ ] **Step 7: Archive only after the complete gate is green**

```bash
npx --yes @fission-ai/openspec@1.6.0 archive production-web-core-experience --yes
npx --yes @fission-ai/openspec@1.6.0 validate --all --strict
git add openspec/changes openspec/specs
git diff --cached --check
git commit -m "docs(openspec): archive production web core migration"
```

Expected: the change moves to `openspec/changes/archive/2026-07-28-production-web-core-experience/`; main specs contain the delivered contract; strict validation passes.

---

## ✅ Final acceptance checklist

- [ ] `home_dashboard/index` bootstraps empty and refreshes from authoritative owner events.
- [ ] Home ordering, limits, capacity, related refs, Tenant isolation, and viewer isolation are integration-tested.
- [ ] `/`, `/tasks`, `/assistant`, `/assistant/:id`, `/workers/:id`, and `/artifacts/:id` have one React Router owner.
- [ ] Overview, focused rail, and mobile drawer modes pass component and browser tests.
- [ ] Dark is the first-visit default; dark, light, and system modes pass persistence tests.
- [ ] Home, Tasks, Assistant, Worker, Workbench, and Artifact preserve current production contracts.
- [ ] Pending, stale, reconnect, resync, upgrade, permission removal, recovery, integrity, empty, loading, and error states are covered.
- [ ] Desktop/mobile and dark/light browser captures have no document-level horizontal overflow.
- [ ] `web/` contains no prototype mock imports, legacy product/prototype copy, legacy router, legacy shell, legacy stylesheet, permanent migration flag, or disabled old UI.
- [ ] Web unit, build, and Playwright gates pass.
- [ ] OpenSpec strict validation and `make verify` pass.
- [ ] Real local Compose Assistant → Task → Worker → Artifact → acceptance → Home flow is recorded, or the change remains active with the exact external blocker.
