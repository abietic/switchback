# ADR 0059: Embed M1 RuntimeSupervisor In Switchback Backend

- Status: Accepted
- Date: 2026-07-15

## Context

M1 needs one local command to start the Switchback control plane while still creating and recovering one isolated Docker container per WorkerSession. A dedicated host-side supervisor process reduces Docker Socket exposure inside containers, but adds a second installation, configuration and lifecycle path before the Worker tracer bullet has proved its core value. Docker-in-Docker adds another daemon, nested storage, networking and cleanup semantics.

`RuntimeSupervisor` is a logical Worker Runtime responsibility, not inherently a separate service or container. Docker Compose is appropriate for long-lived application services, while WorkerSession RuntimeInstances are dynamic resources that must be created on demand.

## Decision

- Docker Compose starts the long-lived `switchback-backend` and PostgreSQL containers for M1.
- RuntimeSupervisor runs as a Go module inside `switchback-backend`; it is not a separate host process or M1 microservice.
- Only `switchback-backend` mounts the Docker Socket and calls Docker Engine API. Docker Engine creates WorkerSession RuntimeInstances as sibling containers, not nested containers.
- Worker containers never mount Docker Socket, never run privileged and cannot create or control containers.
- RuntimeSupervisor derives every WorkingCopy, agent-state and generated-config mount source from a server-configured Switchback data root. Client, TaskSpec, Agent, Skill and Worker input cannot select arbitrary host paths.
- Worker containers initiate their WorkerControlChannel to Backend over an approved Docker network. ACP remains local stdio between the Worker Shim and `codex-acp` inside each Worker container.
- RuntimeProvisioner remains an infrastructure port so a later production profile can extract a dedicated Runner service or use a Kubernetes adapter without changing WorkerSession domain semantics.

## Consequences

- Local M1 has a single Compose deployment entry point and still uses real per-Session container isolation.
- This is Docker-outside-of-Docker, not Docker-in-Docker; Backend and Worker are different sibling containers under one Docker Engine.
- Docker Socket gives `switchback-backend` root-equivalent Docker authority. Therefore this profile is acceptable only for the trusted, loopback-only SingleUserLocalProfile and must not be treated as the final multi-user production security topology.
- A Backend compromise can control local Docker resources, so Worker isolation, mount allowlisting and denial of Docker Socket/privileged mode are correctness requirements rather than optional hardening.

## Alternatives

- Dedicated host-side Go supervisor: retained as the preferred fallback if Docker Socket authority in Backend becomes unacceptable or local packaging can tolerate a second process.
- Dedicated supervisor container with Docker Socket: possible later for process separation, but it does not remove root-equivalent Socket authority and adds a service boundary.
- Docker-in-Docker: rejected for M1 because nested daemon, image cache, volume, networking and cleanup behavior add complexity without improving the tracer bullet.
- Kubernetes RuntimeProvisioner: retained for a later multi-user or production profile.
