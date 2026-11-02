## 1. Contract And Decision Baseline

- [x] 1.1 Add a superseding ADR that selects `trusted_full_network_v1` for SingleUserLocalProfile and records the unrestricted outbound and operator-trust consequences
- [x] 1.2 Define the server-owned network profile id, revision, digest and deployment-profile compatibility in platform configuration
- [x] 1.3 Update TaskSpec contract validation to accept the new profile for current admission while retaining explicit legacy decoding
- [x] 1.4 Add contract tests that reject client-selected network expansion and future non-local deployment use
- [x] 1.5 Update generated protocol fixtures and checks without rewriting immutable historical TaskSpec examples

## 2. TaskSpec Producers And Legacy Migration

- [x] 2.1 Change direct Task admission policy and TaskSpec snapshots to resolve `trusted_full_network_v1`
- [x] 2.2 Change Assistant TaskDraft/TaskSpec builders and deterministic model fixtures to resolve the same server profile
- [x] 2.3 Preserve read compatibility for `m1_public_web_restricted_v1` and add truthful `legacy_unenforced` execution metadata
- [x] 2.4 Reject the legacy value for new admission after the new deployment profile is active
- [x] 2.5 Add migration tests proving stored TaskSpec JSON and prior Task/Run evidence are never rewritten

## 3. Runtime And Docker Configuration

- [x] 3.1 Resolve the new profile through RuntimeSupervisor and record its revision/digest in RuntimeInstance and WorkerRunStartSnapshot
- [x] 3.2 Keep the server-selected named Docker bridge with normal public, RFC 1918, VPN and host-routed outbound connectivity
- [x] 3.3 Preserve no Docker Socket, no privileged/host network, no arbitrary mount and no published Worker port invariants
- [x] 3.4 Add startup/configuration tests that fail if `trusted_full_network_v1` is enabled outside SingleUserLocalProfile
- [x] 3.5 Add Docker inspection tests for immutable profile identity and existing container configuration convergence

## 4. Projection, Evidence And Operations

- [x] 4.1 Project current versus legacy-unenforced network profile identity without emitting false per-destination enforcement claims
- [x] 4.2 Include profile id/revision/digest in Trace metadata while keeping destination and payload telemetry absent
- [x] 4.3 Verify network reachability never injects repository, database, Kubernetes or other private-service credentials
- [x] 4.4 Update requirements, technical design, local operations and manual acceptance documentation
- [x] 4.5 Document host Docker/VPN routing limitations and the risk of separately exposed unauthenticated TCP services

## 5. Acceptance And Closeout

- [x] 5.1 Add positive acceptance for public Web/package endpoints plus non-HTTP TCP/UDP services on arbitrary ports across RFC 1918, VPN, LAN and host-routed destinations
- [x] 5.2 Add negative acceptance for Docker Socket mounts, privileged/host network mode, arbitrary host paths, ambient credentials and published ports
- [x] 5.3 Exercise an already-admitted legacy Task and verify compatibility execution records `legacy_unenforced`
- [x] 5.4 Run Go unit/integration, architecture, Compose, secret, image and failure-acceptance suites plus `make verify`
- [x] 5.5 Validate the OpenSpec change strictly and record exact reachability, isolation, migration and rollback evidence before enabling the profile by default
