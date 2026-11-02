# Proxy SessionProcess preview through Backend

## Decision

M1 exposes a running `preview_server` or `dev_server` only through a Backend-managed HTTP/WebSocket PreviewProxy. Worker containers publish no host or public port. An explicit `OpenSessionProcessPreview` validates the server-configured actor, exact WorkerSession, SessionProcess revision, Runtime generation and declared port, then returns a temporary `SessionProcessPreviewAccess` route reference.

RuntimeSupervisor resolves the current container endpoint from trusted runtime state. Client, Agent, TaskSpec and preview content cannot provide a URL, IP, host, undeclared port, Docker mapping or `CONNECT` target. PreviewProxy is the only P0 inbound caller and reaches only the selected process port over the internal Docker network. This narrows ADR 0060's inbound denial rather than replacing its public/host-port boundary.

PreviewProxy is a direct data plane. HTTP bodies and WebSocket frames do not pass through EventCenter, SSE, Trace JSONL or ObjectStore. Gateway records only bounded access issuance/revocation, decision, status, duration and byte-count metadata, strips Switchback cookies, authorization and internal identity headers, and never forwards control-plane credentials to Worker-generated content.

Every request and WebSocket upgrade revalidates access audience/expiry, SessionProcess status and Runtime generation. Process stop/fail/lost, WorkerSession terminality, Runtime fencing/replacement, authorization revocation or access expiry rejects new traffic and closes indexed active tunnels. Explicit OpenPreview may count as one user interaction; resource loads, polling, HMR, WebSocket frames, health checks and process traffic do not renew the 48-hour idle clock.

## Why

Live previews are a primary reason to preserve explicit SessionProcesses across AgentTurn boundaries. Publishing a random Docker host port bypasses Gateway lifecycle and authorization, exposes container services directly and makes revocation difficult. Artifact-only screenshots cannot support interactive preview, HMR or later user inspection. A Backend proxy keeps the useful data path while preserving one control point and avoiding Worker host-port exposure.

## Alternatives

- Docker random host-port mapping: rejected because it expands host exposure and bypasses Backend authorization, header isolation and lifecycle revocation.
- Screenshot/Artifact-only preview: retained as a fallback when live proxying is unsupported, but not the only P0 interaction.

## Consequences

- RuntimeNetworkPolicy must permit one exact Backend-to-Worker internal path for declared HTTP/WebSocket ports while continuing to deny host/public publishing and arbitrary Worker ingress.
- Frontend receives an opaque PreviewAccess route and never receives the container address.
- PreviewProxy must support WebSocket upgrade, bounded request/connection limits, lifecycle-driven tunnel closure and state revalidation.
- M1 does not create a PreviewOriginPolicy or freeze URL topology. The loopback-only implementation chooses the simplest workable route and records compatibility/security limits in its spike. Origin isolation is reviewed before non-local exposure or an authenticated multi-user profile.
