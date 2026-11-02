# Durable Worker observation projection

The Backend assembles only the `langfuse.projection.v3` fragment publisher/projector. Immutable `observation.fragment.v1` Run basis, Turn basis, and model sources share one publication owner. All accepted model observations, including callbacks after terminal, use this source path. Zero-model basis sources remain valid. Original `turn.observation.source.v1`, TraceManifest, settlement checkpoint and v2 job facts remain unchanged; v2 classes/constants remain available for read-only compatibility checks.

Configure `LANGFUSE_BASE_URL`, `LANGFUSE_PUBLIC_KEY`, and `LANGFUSE_SECRET_KEY` together. All empty disables the workers; partial, malformed, or unusable optional configuration reports only safe configuration codes. Complete configuration activates v3 synchronously in App composition, before the Handler is returned or Runtime/Assistant dispatch starts. This database-only boundary has a one-second timeout; failure disables the optional workers without a v2 fallback. Activation does not contact Langfuse or ObjectStore. Langfuse can be unavailable while Backend remains ready and independent workers retry. Object publication requires no Langfuse health check.

Rollback starts by disabling export: omit the three values and restart the **new binary**, cancel/join the active workers, and stop any old v2 worker or orphan process. Preserve schema and frozen evidence; never downgrade migrations or delete receipts, requests, exclusions, publications or checkpoints. A pre-source binary may not decode new checkpoint fields. v2 and v3 use different projection identities: adding their historical observations/counts can double count the same model call. Existing v2 job rows are not claimed, retried or rewritten by this assembly.

## Source capture and publication

Normal basis capture seals its receipt atomically with the first checkpoint freeze; model capture seals atomically with the accepted journal outcome/cursor. Input comes from the unique prompt/queue Turn bound to Run creation. All scanning and compilation happen outside SQL, using the original Runtime generation's registry plus available current Backend credentials. Missing capture dependencies produce no-content safe gaps; they never switch to v2 or borrow a new generation's registry. Publication verifies actual committed manifest/report objects, and model publication requires exact parents.

Legacy v2 source capture retains its exact terminal cut and original markers. Neither a later callback nor v3 activation rewrites those facts. A v3 candidate with a conflicting identity or a blocked/no-content result is not repaired by re-scanning.

Registry availability is separate from PostgreSQL health: failed database reads remain core errors. No secret values, raw source, or remote error bodies appear in worker diagnostics. Legacy failed/cancelled finalization without an authoritative terminal sequence freezes an incomplete marker where identity/time are valid.

## Worker lifecycle and retry

The fragment publisher and projector share App cancellation and are joined before shutdown completes. The projector discovers up to 50 publications and claims one job at a time, with a 30-second fenced lease, 15-second job context and one-second poll interval. Retry begins at five seconds and doubles to a five-minute cap. Permanent failures become `blocked`, transient failures `retryable`, and verified exact-source readback `passed`. Abandoned leases are reclaimed after expiry.

First activation records exclusions for all visible Runs, including live Runs without manifests. Restart preserves that baseline. Runs accumulated while observability was disabled are not implicitly exported on first enablement. Langfuse health does not affect this selection boundary.

## Exact historical selection and coverage

`FragmentHistory.Request` is a Backend-internal boundary, not a console, HTTP API, free SQL facility or Worker tool. Its server configuration uses the same fixed M1 tenant/actor as Gateway. An explicit request supplies that actor, the exact five-part observation scope, `langfuse.projection.v3`, initial/target Turn IDs and manifest checksums, selected model message IDs/sequences/envelope digests, and an idempotency key. Models are canonicalized as a stable set; duplicates are rejected. Same-key replay returns the original request digest without a registry scan. Changed actor/scope/version/model membership is rejected.

Existing valid receipts can be selected after registry cleanup. A legitimately excluded historical source without a candidate can be captured only while its original registry remains readable. New candidates carry `capture_kind=historical_request` and the exact immutable request digest inside the existing provenance schema: this attests to present revalidation, not a past first-freeze scan. Objects and bounded capture run outside SQL; the short transaction locks selected settlements before Session/Run rows, revalidates exact snapshots/core references and accepted model outcomes, and commits candidates plus request/source set atomically. Subsequent legitimate cumulative checkpoint extensions do not invalidate this historical attestation. Missing applicable-v3 proof, conflicts, ignored/rejected outcomes, missing original registry and changed snapshots are rejected. Existing no-content gaps may be selected for honest coverage but are never replaced.

Selection unlocks only the explicit Run/target basis and model members. It never deletes a Run exclusion or authorizes later unselected events in that Run. No historical requests are executed during startup.

`FragmentCoverageReader.Read(actor, scope, version)` reads accepted selected core sources and candidate/publication/job state in one PostgreSQL repeatable-read, read-only snapshot. It returns stable source refs, accepted/sealed/published/verified counts and safe gap codes, including no-content and missing-proof members. Verified counts use only the requested version's `passed` jobs. Other actors/scopes are rejected and unselected old-Run sources are omitted. “Accepted N / verified M” describes that explicit membership; it does not prove callback closure. There is no `all_models_complete` field.

## SQL diagnostics

The following queries contain metadata only:

```sql
SELECT version, activated_at FROM observation_fragment_activations;

SELECT status, count(*)
FROM observation_fragment_jobs
WHERE version = 'langfuse.projection.v3'
GROUP BY status ORDER BY status;

SELECT kind, source_id, frame_digest, status, attempts,
       error_code, next_attempt_at, lease_until
FROM observation_fragment_jobs
WHERE version = 'langfuse.projection.v3'
ORDER BY next_attempt_at, source_id LIMIT 50;
```

These global metadata diagnostics are for trusted local operators; use the internal exact-scope coverage reader for scoped counts. Do not reset running jobs or fencing tokens. Retryable jobs resume automatically; investigate permanent failures. No Run-level UI or new console is provided by this cutover.

## Validation boundary

The controlled localproduct fixture uses real PostgreSQL settlement/queued continuation and a channel-blocked remote projector, with Runtime/Agent I/O replaced. Its 20 healthy/outage pairs are separate evidence from physical Runtime G4. Physical G4 requires the actual Docker/model path and outage p95 increase at most 1 second, with every measured transition at most 5 seconds. Synthetic Langfuse source probes and direct journal Apply tests are not physical G4.

Fresh-volume Compose initialization was physically verified on 2026-09-10 in isolated projects `switchback-obsv-h4sqtg01` and `switchback-obsv-h4sqtg02`, each reaching migration `000023` without a manual PGUSER override. `init-postgres.sh` passes the configured `POSTGRES_USER` as `PGUSER` for first-init migrations; an already initialized volume does not verify this first-init behavior.

## Historical physical gate failure: late model observations

The controlled Docker smoke on 2026-09-10 exposed an unresolved source-contract gap. With all five Langfuse services absent, a newly created Backend returned HTTP health 200 and two Turns completed in the same Session/Runtime generation. Their terminal-receipt-to-settlement durations were 76.260 ms and 58.862 ms; the first terminal receipt to the second exact start-command ACK was 150.707 ms. That last value includes the test driver's Task accept/follow-up and is a conservative dispatch upper bound, not a prequeued timing or a p95 result.

The first Turn's terminal event had sequence 7, received at `03:07:04.272208Z`; its valid model observation had sequence 8, received at `03:07:04.276404Z` and later applied. Shim drains only observations already in its channel before publishing terminal, while the asynchronous LiteLLM callback may legally arrive later using its previously bound Turn identity. The new source query restricts membership to `sequence <= terminal cut`, so it permanently excludes this model event and freezes `source_incomplete`. The second Turn's model arrived before terminal and produced a complete source.

This was not a transient Langfuse error: resetting retry eligibility, restarting services or replaying the same checksum cannot repair a content-free immutable marker. After restoring healthy Langfuse 3.217.0, the second original source passed on natural attempt 8 at `03:17:27.550533Z`; the first remained blocked on attempt 1. No marker/cut was rewritten. The v3 immutable fragment path addresses the supplemental-source contract; its synthetic tests do not retroactively turn this physical run into a passing gate.

The Task 6 tooling is independently reviewed, including strict failure on uncollected service-interval/fault/residue evidence. Full physical 20-pair/fault/recovery acceptance and provider-specific operational acceptance remain incomplete. No `slice-ready` or complete G4/G5 claim is supported by the single controlled smoke.

## 🔧 Task 9 controlled instrumentation procedure

This procedure is prepared for the authorized physical operator, not a record of a passing run. Use the reviewed Task 9 commit in its isolated worktree. The driver adds `collect-intervals` and `run-matrix`; old modes and `produce-report -report recipe.json -output result.json` remain supported. The directory form reads only the exact claim's `matrix-recipe.json`. A recipe/pass label alone cannot satisfy validation.

### Claim and fresh images

First inspect the Docker context, existing projects and port listeners. Assign a new `switchback-obsv-<8..24 lowercase alphanumeric>` project and a new absolute private output path; do not reuse prior examples, services, keys or evidence. Recheck all claim-selected loopback ports before starting anything. Commands below are for bash, run from the reviewed worktree root. Any failed command stops the sequence.

```bash
test -n "${OBSV_PROJECT:?new independently checked project}"
test -n "${OBSV_OUTPUT:?new exact absolute private directory}"
go run ./test/acceptance/observability -mode claim -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT"
go build -o "$OBSV_OUTPUT/observability-driver" ./test/acceptance/observability
set -a
source "$OBSV_OUTPUT/acceptance.env"
set +a
export OBSV_BACKEND="http://127.0.0.1:$SWITCHBACK_BACKEND_PORT"
export OBSERVABILITY_DATABASE_URL="postgres://switchback:$SWITCHBACK_POSTGRES_PASSWORD@127.0.0.1:$OBSERVABILITY_POSTGRES_PORT/$OBSERVABILITY_DATABASE_NAME?sslmode=disable"
docker build -f images/worker/Dockerfile -t "$OBSV_PROJECT-worker-base:task9" .
docker image inspect --format '{{json .RepoDigests}}' "$OBSV_PROJECT-worker-base:task9"
```

Set `OBSV_WORKER_BASE` to the actual immutable `name@sha256:...` returned for this fresh build (not its mutable tag or an invented registry reference). Record the base digest, source commit and final image IDs without dumping environment/configuration. If the builder produces no usable local RepoDigest, stop and resolve pinning explicitly.

A Docker Engine-local RepoDigest is not necessarily resolvable by BuildKit's registry image source. If the freshly built local-only base fails remote metadata resolution, preserve the failed log. A supported local alternative is an [OCI-layout named context pinned to the same digest](https://docs.docker.com/reference/cli/docker/buildx/build/#additional-build-contexts). Export only the exact fresh base, verify the archive is an OCI layout, validate member paths before extraction into a new private directory, then check `index.json` references the original digest and every blob SHA256 matches its filename. Do not assume every Docker version/image store makes `docker image save` OCI-compatible. When those checks pass, the controlled build can use `--build-arg WORKER_BASE=workerbase --build-context "workerbase=oci-layout://${OBSV_BASE_LAYOUT}@${OBSV_WORKER_BASE##*@}"`, with `OBSV_BASE_LAYOUT` the verified absolute layout path and the inspected target platform. This changes the local source transport, not the immutable base contents. It requires no registry push, new builder, mutable tag or Docker configuration change. The first physical run's checked archive and exact invocation are recorded in `.superpowers/sdd/supplements-task-9-physical-run.md`.

```bash
test -n "${OBSV_WORKER_BASE:?verified immutable RepoDigest}"
docker build -f test/acceptance/observability/Dockerfile.worker --build-arg "WORKER_BASE=$OBSV_WORKER_BASE" -t "$SWITCHBACK_WORKER_IMAGE" .
docker image inspect --format '{{json .RepoDigests}}' "$SWITCHBACK_WORKER_IMAGE"
# Replace only SWITCHBACK_WORKER_IMAGE in this claim's private acceptance.env
# with the actual controlled image's inspected name@sha256:... reference.
# Keep permissions 0600, preserve every secret/other value, and never print the file.
set -a
source "$OBSV_OUTPUT/acceptance.env"
set +a
case "$SWITCHBACK_WORKER_IMAGE" in *@sha256:*) ;; *) echo 'exact controlled Worker digest required'; exit 1 ;; esac
docker build -f images/backend/Dockerfile -t "$OBSERVABILITY_BACKEND_IMAGE" .
compose=(docker compose --env-file "$OBSV_OUTPUT/acceptance.env" -p "$OBSV_PROJECT" -f deploy/local/compose.yaml -f test/acceptance/observability/compose.override.yaml)
"${compose[@]}" up -d --no-deps postgres observation-fake-model
```

Wait for the exact project PostgreSQL container to be healthy. Resolve exactly one ID with both project and `postgres` service labels; independently inspect those labels before using `OBSV_PG_ID`. Fresh init creates the standard databases, not the claim-specific database. Create that database once through PostgreSQL's standard utility, then run the existing migrator; this is schema setup, not business SQL repair.

```bash
test -n "${OBSV_PG_ID:?exact independently verified project postgres ID}"
docker exec "$OBSV_PG_ID" createdb --username=switchback --maintenance-db=postgres "$OBSERVABILITY_DATABASE_NAME"
"${compose[@]}" up --no-deps --abort-on-container-exit --exit-code-from switchback-migrate switchback-migrate
```

The fresh claim's initial Worker value is a build tag only. Before starting fake-model or Backend, bind the claim environment to the controlled image's **own** exact RepoDigest (not the production base digest), then independently inspect it. The production runtime rejects mutable tags. Persist this binding in the private claim environment so later matrix/restart Compose commands use the same immutable image.

Never use `make local-up` or `pin-worker-image.sh` here: they manage the default deployment environment. Do not print or archive `acceptance.env`, provider key files, registry contents, full inspect, HTTP authorization headers or raw request/response bodies in public evidence.

### Cold start, then smoke before any matrix

All five Langfuse services must still be absent/stopped. Start collection before the before-capture and Backend. In a dedicated foreground bash terminal print the shell PID and `exec` the native binary (not a background `go run` wrapper); wait for its exact `collector_ready project=...` line. Keep it running through cold smoke and the after-capture. **Before restoring any Langfuse service**, independently verify the printed PID still identifies the exact claim's native collector, then send SIGINT to that PID only. Do not use terminal Ctrl-C: it broadcasts to the foreground process group and can interrupt an in-flight Docker probe or the events reader before the collector finishes. Require the collector's final recorded probe, subscription join, persisted `intervals-*.json`, and exit 0. The collector validates the cold window against `smoke-pending.json` and `cold-before.json`; the pending source need not yet have passed readback. EOF, duration expiry, failed final probes or incomplete coverage fail closed and retain the evidence. Readiness alone is not proof of uninterrupted coverage.

```bash
echo "collector_pid=$$"
exec "$OBSV_OUTPUT/observability-driver" -mode collect-intervals -duration 30m -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT"
```

In the operator terminal, each capture prints its new exact JSON path. Copy that verified claim-owned path to the specified new alias; never choose a prior run's capture or overwrite an alias.

```bash
"$OBSV_OUTPUT/observability-driver" -mode capture -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT"
# Copy the printed exact capture path to the NEW "$OBSV_OUTPUT/cold-before.json".
"${compose[@]}" up -d --no-deps switchback-backend
# Wait for the claim's Backend /healthz to return 200; Langfuse remains absent.
"$OBSV_OUTPUT/observability-driver" -mode smoke -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT" -backend "$OBSV_BACKEND"
"$OBSV_OUTPUT/observability-driver" -mode capture -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT"
# Copy this printed exact capture path to the NEW "$OBSV_OUTPUT/cold-after.json".
# In this operator terminal, set OBSV_COLLECTOR_PID to the exact printed PID.
test -n "${OBSV_COLLECTOR_PID:?verified collector PID}"
test "$(ps -p "$OBSV_COLLECTOR_PID" -o command=)" = "$OBSV_OUTPUT/observability-driver -mode collect-intervals -duration 30m -project $OBSV_PROJECT -output $OBSV_OUTPUT"
kill -INT "$OBSV_COLLECTOR_PID"
# Require collector exit 0 BEFORE restoring LF; otherwise stop here.
"${compose[@]}" up -d langfuse-web langfuse-worker langfuse-clickhouse langfuse-redis langfuse-minio
"$OBSV_OUTPUT/observability-driver" -mode snapshot -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT" -backend "$OBSV_BACKEND"
```

The smoke driver freezes the exact IDs returned by LiteLLM's real `x-litellm-call-id` response headers after first SSE readiness and before releasing either completion. Registration alone cannot release: both exact Turns must have real terminal/settlement/idle facts. The process-owned bounded readers survive `EndTurn`; completion/error is checked in the fixture ledger. No real model/provider calls are made. `smoke-passed.json` requires exact v3 Run/Turn/model sources, passed-source readback, completed cold coverage, and finite nonnegative settlement/next-command ACK latencies at most 5000 ms matching their original timestamps. LF health alone is insufficient. A legal old `source_incomplete` v2 marker is verified through immutable manifest/segment/report bytes and retained unchanged, not forced through the v2 projector's projectability requirement.

Any failure stops here: preserve diagnostics, do not run the 20 pairs, enlarge the 10-second fixture request budget, rewrite cut/marker, reset a job, or add a production hook. The actual pinned LiteLLM streaming/callback path is a required fresh smoke test, not established by native unit tests.

### Matrix and report

After a successful smoke/snapshot and a cleanly completed cold collector, the authorized operator may run:

```bash
"$OBSV_OUTPUT/observability-driver" -mode run-matrix -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT" -backend "$OBSV_BACKEND"
"$OBSV_OUTPUT/observability-driver" -mode produce-report -output "$OBSV_OUTPUT" -report "$OBSV_OUTPUT/report.json"
"$OBSV_OUTPUT/observability-driver" -mode validate -report "$OBSV_OUTPUT/report.json"
```

`run-matrix` is a mutating physical operation: it starts/stops the exact five LF containers, issues fresh explicit follow-ups to the original resident Session, stops/restarts the exact Backend, stops the original Worker for production reconciliation, and removes exact owned Compose containers/networks at the end. It never creates additional Sessions or changes quotas. Twenty pairs contain forty distinct two-Turn measurements; fault warmups and recovery Turns are additional and never reused as latency samples. Failed matrix claims cannot be rerun; preserve their evidence and review before a fresh claim.

Each measured pair, fault, warmup and cleanup source has its own real event subscription. Initial exact-state probes must be recorded before measurement begins; a new same-state probe must close its actual endpoint and the subscription must finish before service restoration. Each finished window is validated immediately, all completed windows are checked before original Worker cleanup, and final source/coverage evidence is checked before infrastructure cleanup. These subscriptions prove only their recorded windows; gaps between measurements are never bridged. Both connected-unanswered windows require independently healthy actual Langfuse services while the separate proxy blocks requests. Additional measurements are retained in `additional_measurements` and receive the same timestamp/identity/5000 ms validation, but are excluded from the twenty pairs and p95 calculations. Collector handshake time never replaces or shifts the journal receipt, settlement, or exact command ACK timestamps.

Subscription readiness includes a fixed read-only `pg_isready` executed in the exact claim's independently revalidated `postgres` container. The collector must actually record matching `exec_create`/`exec_die` events with the same Docker exec ID, exact container/project, and current handshake time range before readiness. Starting the CLI process or seeing `pg_isready` return successfully alone is insufficient. Only safe identity/action/timestamp fields are retained; raw Docker action command strings, output, Env and credentials are not archived. This targets the fresh claim's PostgreSQL, never the separate native-test database.

A separate subscription retains the Backend restart/original Worker cleanup lifecycle after the cleanup source's latency coverage has passed and before any stop operation. It finishes after exact source recovery, before infrastructure cleanup; this extra lifecycle record is never counted as a latency measurement.

The test-only model service also proxies Backend `/api/public/*` to the fixed `http://langfuse-web:3000`; it is attached to `worker-control` and `observability`, with no host port. `fake-openai --control=status|release|fault|fault-status` uses fixed container-loopback `127.0.0.1:8080`. Release takes exact `--fixture-key` and `--evidence-digest`; fault takes `--fixture-key ingestion|query|off`. Fault state/ledger is separate from SSE/model state. Only natural production projection requests establish connected-unanswered proof. Independent LF health probes target actual services directly, never the proxy's health.

Continuous evidence uses exact Compose labels/IDs, process start identity and bounded adjacent independent probes plus a live event subscription. EOF or a gap is not inferred healthy. Worker/secret-volume/populator evidence separately uses exact database Runtime/Session/generation plus managed labels, because Compose events do not include those siblings. Registry evidence inspects only the exact original `control-plane/observation-tokens.json` file's metadata/ownership, never token values. After production reconciliation it verifies registry absence, Worker absence and managed secret resource absence independently. `CleanupConfirmedAt` describes Runtime reconciliation; Compose infrastructure deletion occurs later and has separate per-resource timestamps. Data volumes remain explicitly retained; this is not zero-residue or private credential deletion. The operator must review exact retained volumes/private synthetic key material before separately authorized cleanup.

Do not describe instrumentation readiness, a controlled ACP/model run, or one smoke as real Codex/provider acceptance, complete G4/G5, remote CI, merge or release.
