## 1. SessionProcess Contracts And Persistence

- [ ] 1.1 Define typed `web_ide` SessionProcess identity, lifecycle, revision, declared-port, Runtime-generation and idempotency contracts
- [ ] 1.2 Add PostgreSQL migrations and repositories for managed process intent, current state, access lineage and owner-stream Outbox
- [ ] 1.3 Add state-machine tests for start, ready, stop, failure, lost, Session terminality and Runtime replacement
- [ ] 1.4 Add exact actor, Session revision, process revision, generation and process-type validation tests

## 2. Worker Image And Shim Management

- [ ] 2.1 Select and pin a verified code-server release plus approved Go, Git and Markdown extensions with image provenance and license checks
- [ ] 2.2 Add `ide-user`, immutable preinstalled extensions and Session-persistent IDE settings while denying Agent writes to IDE-owned state
- [ ] 2.3 Extend the Shim managed-process supervisor to start, health-check, stop and report one `web_ide` process and declared port
- [ ] 2.4 Keep the IDE process alive across AgentTurn terminal states and stop it on explicit stop, Session terminality, security termination or hard resource exhaustion
- [ ] 2.5 Add CPU, memory and PID headroom admission that returns `ide_capacity_unavailable` without replacing or stopping the existing Runtime
- [ ] 2.6 Add image, UID/permission, process-group, health, idempotency and resource-exhaustion tests

## 3. Runtime Control And Projection

- [ ] 3.1 Extend RuntimeControlEnvelope commands/events for managed process start, ready, failure, stop and current-generation fencing
- [ ] 3.2 Implement idempotent Worker Runtime application services for open, stop and restart with no process side effect inside database transactions
- [ ] 3.3 Project SessionProcess state and exact allowed actions into WorkerDetail through owner-sequenced IntegrationEvents
- [ ] 3.4 Preserve IDE settings but mark the old process lost/stopped and avoid automatic restart after Runtime generation replacement
- [ ] 3.5 Add control-channel duplicate, late-event, old-generation, Backend restart and projection-reconciliation tests

## 4. PreviewProxy

- [ ] 4.1 Implement opaque PreviewAccessRef issuance for the exact authorized Session, process revision, Runtime generation and declared port
- [ ] 4.2 Resolve current internal endpoints through RuntimeSupervisor and reject every client-supplied host or port target
- [ ] 4.3 Proxy HTTP and WebSocket traffic while stripping Switchback cookies, authorization and internal headers before upstream forwarding
- [ ] 4.4 Close active tunnels on process/Session terminality, Runtime replacement, revocation and access expiry
- [ ] 4.5 Verify Docker publishes no Worker IDE host port and other Worker/private network peers cannot reach the IDE endpoint
- [ ] 4.6 Add HTTP, WebSocket, path-rewrite, header isolation, stale-generation, expiry and unauthorized-access tests

## 5. Shared WorkingCopy And Artifact Provenance

- [ ] 5.1 Mount the exact WorkerSession WorkingCopy for `ide-user` without adding host paths, repository credentials or Docker access
- [ ] 5.2 Mark report and patch metadata `mixed_origin_possible` when a Web IDE process ran between AgentTurn start and Artifact finalization
- [ ] 5.3 Keep IDE terminal actions outside Agent Tool Trace while recording process start, access, stop and failure lifecycle
- [ ] 5.4 Add concurrent Agent/IDE write, Turn snapshot, Artifact provenance and no-remote-credential tests

## 6. Embedded Workbench IDE

- [ ] 6.1 Add Web IDE contracts and command clients for open, pending, ready, failed, stop and restart states
- [ ] 6.2 Add the IDE tab to the resizable Worker Workbench and mount the iframe only after the current process is healthy
- [ ] 6.3 Support tab switching without process stop, explicit Stop/Restart and temporary full-screen mode with correct keyboard focus
- [ ] 6.4 Render capacity, process failure, access expiry and Runtime replacement recovery states from authoritative projection data
- [ ] 6.5 Add component and Playwright tests for iframe lifecycle, resize, full-screen, reconnect, WebSocket use and responsive layout

## 7. Acceptance And Documentation

- [ ] 7.1 Run Go, image, frontend and browser verification suites plus Docker isolation and resource checks
- [ ] 7.2 Execute manual acceptance for on-demand start, concurrent Agent editing, Turn terminal survival, explicit stop and Session terminal cleanup
- [ ] 7.3 Verify runtime extension installation is unavailable to both IDE and Agent users while approved extensions and persisted settings work
- [ ] 7.4 Document code-server primary and OpenVSCode fallback conformance requirements, operations, diagnostics and rollback
- [ ] 7.5 Validate the OpenSpec change and record exact process, proxy, isolation and browser evidence before enabling by default
