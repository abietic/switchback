# ADR 0066: Trust Full Worker Network In SingleUserLocalProfile

- Status: Accepted
- Date: 2026-07-26
- Supersedes: ADR 0060 for SingleUserLocalProfile

## Context

The M1 Docker adapter attaches each Worker to a server-selected named bridge but does not implement the destination filtering claimed by `m1_public_web_restricted_v1`. The required local development experience also needs the Worker to reach public package infrastructure, private repositories and services, VPN and LAN endpoints, and services routed through the developer host.

SingleUserLocalProfile is a loopback-only, trusted local deployment controlled by one operator. Its network authority can therefore be broader than a future multi-user or remotely exposed deployment without changing the separate container, mount, credential and inbound-port controls.

## Decision

- SingleUserLocalProfile resolves exactly one server-owned RuntimeNetworkPolicy: `trusted_full_network_v1`, revision 1.
- The policy permits unrestricted outbound destinations, protocols and ports through the server-configured named Docker bridge. This includes public Internet, RFC 1918, LAN, VPN and host-routed development services when the host Docker environment exposes those routes.
- The deployment profile is the network grant. TaskSpec, Assistant output, Agent, Skill and MCP input cannot select, promote or widen the policy.
- RuntimeSupervisor keeps the Worker on a named Docker bridge and adds the platform-supported `host.docker.internal:host-gateway` Docker Engine API alias. It does not use host network mode.
- Full network reachability does not add Docker Socket access, privileged mode, arbitrary client-provided host mounts, ambient infrastructure credentials or published Worker host ports.
- Network reachability does not imply authentication authority. Repository, database, Kubernetes and platform credentials require separate server-owned capability and credential paths.
- Runtime evidence records profile id, revision, digest and enforcement status. It does not record destination-by-destination decisions or traffic payloads.
- New admission accepts only `trusted_full_network_v1`. Immutable TaskSpec snapshots containing `m1_public_web_restricted_v1` remain readable; compatibility execution records `legacy_unenforced` because private-network filtering was not enforced.
- This decision does not modify ADR 0060 as historical evidence. ADR 0060 remains applicable only as superseded history and cannot be used to claim that earlier executions enforced destination filtering.

## Consequences

- Worker development can use public, private, VPN, LAN and host-routed dependencies without per-target ApprovalGrant.
- A compromised Agent can reach any unauthenticated service exposed on a routable address. SingleUserLocalProfile operators own that risk and must not expose privileged TCP services such as an unauthenticated Docker daemon.
- Docker Desktop and Linux VPN route propagation differ. Acceptance records the actual host, Docker and VPN matrix instead of claiming universal routing.
- The named bridge preserves the Worker network namespace and no-published-port rule, leaving PreviewProxy as the only supported inbound path.
- The policy is not DLP. Future multi-user or remote deployments must define another RuntimeNetworkPolicy and may add egress proxy, domain, CIDR and content controls behind that separate profile.

## Alternatives

- Keep the `m1_public_web_restricted_v1` name without enforcement: rejected because it falsifies the security boundary.
- Implement a local allow-all proxy: rejected because it adds protocol incompatibility and another failure point without enforcing a destination decision.
- Let TaskSpec select full network: rejected because user/model content cannot grant deployment authority.
- Use host network mode: rejected because it collapses namespace and port ownership and conflicts with PreviewProxy isolation.
