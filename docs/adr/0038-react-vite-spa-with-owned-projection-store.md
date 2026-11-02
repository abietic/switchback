# ADR 0038: Use A React Vite SPA With An Owned Projection Store

- Status: Accepted
- Date: 2026-07-13

## Context

Switchback is an authenticated internal work application with Go-owned Gateway commands, Frontier snapshots/SSE, resumable Artifact transfer and page-optimized read models. It does not need SEO or server-rendered public pages. The browser must apply exact projection versions and transient Agent deltas without allowing a React framework, query cache or chat component library to become the authority for Task, Worker, Approval or Artifact state.

## Decision

P0 Frontend is a React + TypeScript single-page application built with Vite. Node.js is a development/build dependency, not a required production application runtime. Production serves the compiled static bundle behind the same origin as Gateway and Frontier through the Go edge/service or an equivalent reverse proxy. API, SSE and Artifact routes remain backend-owned.

React Router owns route and shareable filter identity. TanStack Query owns HTTP request lifecycle, caching and refetch policy for snapshots, lists and command-status queries. A dedicated Switchback `ViewProjectionStore` owns each active browser View replica: snapshot data, view epoch/cursor/version, typed reducer state, transient Display overlays and accepted-command convergence. Components do not independently merge raw SSE into TanStack Query objects.

Snapshot query success atomically replaces the affected View replica. ProjectionDelta advances it only after epoch/base-version/schema checks. TransientDisplayDelta updates a bounded overlay and never advances durable state. An accepted Gateway response creates a `PendingCommand` keyed by command id; only a causally related ProjectionDelta, command-status result or replacement Snapshot resolves it. HTTP timeout or SSE disconnect does not become an authoritative failure.

assistant-ui may be used inside Worker Chat through its ExternalStore adapter. Switchback converts canonical DisplayPart/message state and exposes only supported command callbacks. assistant-ui does not own persistence, thread identity, Tool/Approval authority, SSE replay or the Task/Worker page shell. AG-UI, AI SDK and other runtimes remain optional adapters behind the same Switchback contracts.

The browser uses same-origin Secure/HttpOnly session credentials. State-changing Gateway commands also require the platform CSRF strategy and idempotency keys. Client-visible build variables contain no secrets. Hashed assets are immutable; `index.html` and compatibility metadata must permit safe rollout and `client_upgrade_required` recovery.

## Consequences

- Production keeps one backend runtime family and one same-origin security boundary.
- Snapshot/SSE correctness is isolated from React components and third-party chat runtimes.
- TanStack Query remains useful without becoming a second projection reducer.
- The application must implement a small typed ViewProjectionStore, protocol decoders and adapter tests.
- SSR, React Server Components and Next.js server features are deferred unless a concrete route later needs them.

## Alternatives Rejected

- Next.js full-stack/Node BFF: provides SSR and server features that P0 does not need while adding another production runtime, proxy/auth boundary and deployment lifecycle.
- Static-exported Next.js: removes most server benefits and leaves more framework surface than the Vite SPA needs.
- Agent UI framework as the whole application runtime: accelerates chat prototypes but cannot own Task/Worker dual-primary navigation, Workspace/Artifact views or Switchback projection recovery semantics.
- Put live projection state directly in TanStack Query cache: possible for simple invalidation streams, but exact base/result reducers, transient overlays and command convergence deserve a narrow explicit owner.
