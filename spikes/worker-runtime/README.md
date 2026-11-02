# Worker Runtime Spike

This isolated Go module contains the M1 Worker Runtime Spike foundation, a pinned Docker/Compose topology, and a deliberately broken Go repository fixture.

Architecture and result semantics are documented in [M1 Worker Runtime Spike execution architecture](../../docs/architecture/m1-worker-runtime-spike-execution.md). Promotion boundaries and the recommended next product slice are documented in [the post-Spike roadmap](../../docs/architecture/m1-worker-runtime-post-spike-roadmap.md).

## Local deployment

The deployment is local-only. It publishes Backend HTTP, Backend gRPC, Langfuse Web, and Langfuse Worker health on `127.0.0.1`; PostgreSQL, Redis, ClickHouse, MinIO, and dynamic WorkerSession containers have no host ports. `spike-backend` is the only Compose service with the Docker socket, and it uses the Engine API to create sibling Workers on `switchback-spike-control`. Workers do not receive the Docker socket, Langfuse credentials, or the upstream model key as top-level environment variables.

Set the three required upstream values in the current shell, then run:

```bash
export SWITCHBACK_SPIKE_UPSTREAM_MODEL='provider/model'
export SWITCHBACK_SPIKE_UPSTREAM_API_BASE='https://models.example.invalid/v1'
export SWITCHBACK_SPIKE_UPSTREAM_API_KEY='replace-me'
make lock-images
make preflight
make build
docker compose --env-file .spike-data/env/compose.env config --quiet
make compose-up
```

`scripts/generate-env` writes `.spike-data/env/compose.env` and the root-only `.spike-data/env/upstream-model.key` with mode `0600`. It reuses generated Langfuse and storage secrets unless the ignored data root is explicitly removed. `image-lock.json` records source and official Langfuse v3.217.0 Compose provenance; every deployed reference is digest-only.

`make preflight` is intentionally before any image build or Compose create. It requires a local Unix Docker context, Compose v2+, 6 CPUs, 12 GiB Docker memory, 25 GiB free backing storage, free loopback ports, and a structurally valid `linux/arm64` image lock. On a native Linux Engine, storage is measured at Docker's reported root. On Docker Desktop for Mac, it measures the conservative minimum of `Docker.raw` sparse-image headroom and its host-volume free space; an operator may set `SWITCHBACK_SPIKE_DOCKER_DISK_IMAGE` to the actual regular, non-symlink disk image path. Docker Desktop currently needs its allocation raised to 12 GiB before this topology can start.

Use `make compose-down` for a normal shutdown. It preserves the Langfuse volumes and the ignored data root. `make purge` is the explicit destructive reset and runs `docker compose down -v`.

## Real two-Turn acceptance

The real acceptance uses the pinned Codex ACP stack and the full Langfuse
topology. Supply the upstream route only through the current process
environment:

```bash
export SWITCHBACK_SPIKE_UPSTREAM_MODEL='provider/model'
export SWITCHBACK_SPIKE_UPSTREAM_API_BASE='https://models.example.invalid/v1'
export SWITCHBACK_SPIKE_UPSTREAM_API_KEY='replace-me'
make spike-e2e
```

`make spike-e2e` is intended to run preflight before any build or container
create, build the Backend and Worker, start the storage/Langfuse/Backend
topology, and ask the real Backend Docker provisioner to create the sibling
Worker. A successful real run validates the exact two-Turn Codex
flow, incremental production/test patches, typed ACP/evidence provenance and
checksums, exact Langfuse cardinality and runtime metadata, known-token
absence, fixture immutability, and fail-closed dynamic Worker cleanup.

A successful run finishes with:

```text
execution_outcome=completed
spike_acceptance=passed
acceptance_summary=/absolute/ignored/data/root/acceptance/summary.json
```

The success path was verified on 2026-07-22 with the pinned Codex ACP stack
and LiteLLM `1.93.0` against both supported upstream profiles:

```text
deepseek-codex-openai-v1
  SWITCHBACK_SPIKE_UPSTREAM_MODEL=deepseek/deepseek-v4-pro
  SWITCHBACK_SPIKE_UPSTREAM_API_BASE=https://api.deepseek.com

kimi-codex-anthropic-v1
  SWITCHBACK_SPIKE_UPSTREAM_MODEL=anthropic/kimi-for-coding
  SWITCHBACK_SPIKE_UPSTREAM_API_BASE=https://api.kimi.com/coding
```

Each run completed both AgentTurns in one ACP session, produced independent
production/test patches, verified the exact Langfuse Trace and runtime
metadata, preserved the Fixture digest, found no known token in evidence, and
confirmed cleanup. DeepSeek invoked Codex's native `apply_patch` tool; the Kimi
regression used `exec_command` for its edits. The ignored per-variant
`acceptance/summary.json` files are the machine-readable proof.

The two names above are deployment profiles, not LiteLLM fallbacks. The
Backend selects exactly one server-owned profile before a WorkerRun starts and
the Runtime receives only that profile's model, API base, and credential. A
failed request or AgentTurn does not switch providers automatically. The Spike
still exposes the generic three-variable contract instead of a public profile
selection API.

The executable failure variants are:

```bash
SWITCHBACK_SPIKE_E2E_VARIANT=invalid-upstream-credential make spike-e2e
SWITCHBACK_SPIKE_E2E_VARIANT=langfuse-unavailable make spike-e2e
SWITCHBACK_SPIKE_E2E_VARIANT=checksum-mismatch make spike-e2e
SWITCHBACK_SPIKE_E2E_VARIANT=cleanup-failure make spike-e2e
SWITCHBACK_SPIKE_E2E_REPEAT=2 make spike-e2e
```

The invalid-credential variant creates an isolated data root and injects a
fixed invalid key before build without printing it. The checksum variant runs
an ephemeral proxy container only on `switchback-spike-control`, with no host port
or host-gateway dependency and a fixed upstream of
`http://spike-backend:8080`. It accepts only an exact evidence `PUT`, requires
an Authorization header without recording its value, and marks the fault
proven only after the Backend returns HTTP `400` with the normalized
`evidence_checksum_mismatch` code for a legal original checksum and a
different mutated body digest. Other methods and paths are rejected locally.
The cleanup variant uses a Unix-socket Docker Engine proxy to reject the exact
dynamic Worker DELETE; the harness reports that identity, uses the real daemon
for final test cleanup, and still exits non-zero with
`cleanup_confirmed=false`.

Runtime data remains under `${SWITCHBACK_SPIKE_HOST_DATA_ROOT:-.spike-data}`:
session WorkingCopies and trace spools are under `sessions/`, authoritative
Turn objects and the latest manifest are under `objects/`, generated
configuration is under `env/`, and the machine-readable result is
`acceptance/summary.json`. The harness deletes its temporary exact-token scan
file but preserves evidence and Langfuse volumes. Its EXIT trap attempts
explicit Session cleanup and verifies the cleanup POST, dynamic Worker
enumeration, Compose shutdown, and post-shutdown static/dynamic container
enumeration. Any unprovable step is non-zero and reports the known
Session/Run/container identity.

`make compose-down` and the E2E EXIT trap preserve volumes and ignored runtime
data. `make purge` is the explicit destructive operation that also deletes
Compose volumes.

Compose volumes are shared by the project name, while separate variant data
roots generate different database credentials. Run `make purge` (or the
equivalent `docker compose ... down -v`) before switching between credential
sets; otherwise the preserved PostgreSQL volume can reject the newly generated
password even though the selected model route is valid.

The Backend/root-init boundary is trusted: only Backend receives the Docker
socket and Langfuse credentials. Worker root-init receives a read-only,
root-owned per-runtime model-secret mount and passes the upstream key only to
the UID `10001` LiteLLM child; UID `10002` is the Codex/Tool identity. A
successful real run verifies the secret is `root:root 0400`, both
UIDs receive permission denied when directly reading the file, every live
Codex/Tool child environment lacks upstream/Langfuse names and known values,
and persisted evidence lacks every known token. The LiteLLM child necessarily
retains its upstream key in its private environment in the current in-container
topology. Live child environment inspection runs separately as UID
`10001:10001` and `10002:10002`, filters to the corresponding LiteLLM and
Agent children, excludes the inspector process, and returns only role/hash
labels for host-side validation; it does not add `CAP_SYS_PTRACE`.

This M1 Spike does not prove recovery or production readiness. Docker Desktop
still lacks an Engine-enforced equivalent of the intended full Linux egress
policy. A successful real run verifies the named-network, socket,
mount, privilege, port, and credential boundaries that the current topology
implements. A future deployment may move LiteLLM into a sidecar, but this Spike
deliberately keeps it in the Worker container.

## Verification

Run:

```bash
make fmt-check
make proto-check
make test
make spike-integration
make spike-e2e
go vet ./...
```

`make test-scripts` exercises deployment/preflight failure paths, while the
E2E package also tests preflight ordering, fail-closed cleanup, proxy/helper
lifecycle, identity replacement, wrong-Trace rejection, checksum mismatch,
zero-delta mutation strength, patch repetition, Langfuse exactness, and
secret-safe diagnostics without invoking a model. `make spike-integration`
also drives the cleanup proxy against the real Docker daemon, verifies its
fallback leaves no managed container, and injects a one-shot checksum fault
through the fake-profile Backend/Worker upload path. Its focused transport
check proves the checksum proxy has no host binding, is attached only to
`switchback-spike-control`, rejects wrong routes, and removes its container, state,
program, and override. Because the path uses a Docker service name instead of
`host.docker.internal`, it has the same transport shape on Docker Desktop and
native Linux. The focused secret-boundary proof exercises the production
runtime-secret path: Backend stages bootstrap and model credentials through an
isolated root populator into the exact labeled Docker-managed volume, removes
the transient session files, and mounts only that volume read-only into the
Worker. Acceptance verifies both files are `root:root 0400`, neither Worker
child UID can read them, and the populator and volume are removed at their
respective lifecycle boundaries.
Run
`./scripts/check-fixture-baseline` to confirm the source fixture still contains
its intentional defect.
