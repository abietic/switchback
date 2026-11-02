## Why

The current M1 contract names `m1_public_web_restricted_v1`, but the intended SingleUserLocalProfile development experience requires Worker access to every public, private, VPN and host-routed destination that the developer machine can reach. The contract must describe that trusted full-network authority truthfully without weakening the separate container, mount, credential and inbound-port isolation controls.

## What Changes

- Add the server-owned `trusted_full_network_v1` RuntimeNetworkPolicy for SingleUserLocalProfile.
- Allow unrestricted outbound destination, protocol and port access through the configured Docker network, including public Internet, RFC 1918, VPN, LAN and host-routed development services.
- Keep Docker Socket, privileged mode, host network mode, arbitrary host mounts, ambient infrastructure credentials and published Worker ports unavailable.
- Separate network reachability from credential authority; private reachability does not inject repository, database, Kubernetes or other service credentials.
- Stop creating new TaskSpecs with the misleading `m1_public_web_restricted_v1` value while preserving immutable historical snapshots and explicit legacy-read behavior.
- Supersede the private-network-deny M1 decision and add acceptance evidence for both broad reachability and the remaining container isolation controls.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `runtime-execution-core`: Replace the M1 private-network-deny RuntimeNetworkPolicy with a server-owned trusted full-network profile for SingleUserLocalProfile while preserving non-network isolation.

## Impact

- Affects TaskSpec validation and generation, runtime profile configuration, RuntimeSupervisor Docker configuration, local Compose topology, acceptance fixtures and operational documentation.
- Requires a new superseding ADR; historical ADRs and immutable TaskSpec snapshots remain unchanged.
- The profile is intentionally unavailable to future multi-user or remotely exposed deployment profiles until those profiles define a separate network authority model.
- Existing sessions admitted with `m1_public_web_restricted_v1` require truthful legacy labeling and compatibility behavior during migration; the implementation must not pretend that historical executions had filtering that was never enforced.
