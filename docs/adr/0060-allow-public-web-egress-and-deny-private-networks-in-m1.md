# ADR 0060: Allow Public Web Egress And Deny Private Networks In M1

- Status: Accepted
- Date: 2026-07-15

## Context

A real Coding Agent needs package registries, CDNs and public documentation. An unrestricted Worker network would also expose the host, Docker control plane, local infrastructure and private network, while a domain allowlist would make ordinary npm, pip, Go module and system-package flows brittle before the Worker tracer bullet is proven.

## Decision

- M1 resolves Worker networking to versioned `m1_public_web_restricted_v1`; TaskSpec, Agent, Skill and MCP input may select only that policy or a stricter one.
- The policy allows the configured DNS resolver, public TCP 80/443, the co-located LiteLLM localhost endpoint and exact Backend WorkerControl/Trace/Artifact endpoints.
- Worker containers publish no inbound host/public port and initiate WorkerControlChannel themselves. [ADR 0065](0065-proxy-sessionprocess-preview-through-backend.md) adds one narrow internal exception: Backend PreviewProxy may reach an exact running SessionProcess declared HTTP/WebSocket port without exposing the container endpoint to the browser.
- The policy denies Docker API, host gateway and `host.docker.internal`, cloud metadata and link-local addresses, RFC1918, CGNAT, IPv6 private/link-local, other Worker containers and PostgreSQL/OpenViking/Langfuse or other control-plane infrastructure.
- Backend and DNS use exact endpoint-port exceptions. Every resolved IP and redirect is checked again. Worker IPv6 is disabled if equivalent filtering cannot be proven.
- SSH, non-Web protocols, private endpoints and additional destinations require an existing ApprovalGrant bound to exact destination, protocol, port, once/session scope and expiry; platform policy may still deny the exception.
- Network decisions retain policy revision, requested and resolved destination, port, decision/reason, WorkerSession, Runtime generation and ApprovalGrant reference. M1 does not require TLS interception or payload logging.

## Consequences

- Normal package installation and public documentation access work without per-domain approval.
- Worker cannot directly reach Docker, host services, private networks, sibling Workers or control-plane databases.
- This policy is not data-loss prevention: a Worker can still send WorkingCopy content to an arbitrary public Web endpoint. It is accepted only for the loopback-only SingleUserLocalProfile with no host, Docker or code-platform root credential in Worker.
- The enforcement adapter requires a Linux and Docker Desktop spike. A later multi-user profile may add an egress proxy, domain policy and content controls behind the same RuntimeNetworkPolicy port.

## Alternatives

- Domain allowlist through an egress proxy: retained for multi-user/production profiles, but rejected as the M1 default because package ecosystems depend on changing registries, mirrors and CDNs.
- Unrestricted egress: rejected because access to host, Docker and private networks is unnecessary.
- No network: rejected because it prevents normal Coding Agent dependency and research workflows.
