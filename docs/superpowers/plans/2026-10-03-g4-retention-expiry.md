# G4 retention expiry implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Finish the approved seven-day retention gate with old-record query exclusion and deletion of the original Loki chunks and Prometheus blocks.

**Architecture:** Use a new, synthetic-only, private acceptance claim. Retain raw HTTP responses, exact Docker identities, complete storage inventories and command receipts; verify these independently before changing completion status. No production behavior is added.

**Tech Stack:** Digest-pinned Loki 3.7.8, Prometheus 3.15.0, official promtool backfill, Node evidence verifier and Go read-only storage inventory helper.

## 📋 Global constraints

- PostgreSQL and Trace JSONL/ObjectStore remain authoritative; derived telemetry must not gate execution.
- Seven days means `604800` seconds, Loki `168h`, and Prometheus `7d`; never shorten the retention policy to make a test pass.
- Only root performs Docker operations. Use a fresh project and fresh owned storage, never delete or modify previous acceptance volumes or evidence.
- Use eight-day-old records and a recent positive control. Prove both records were persisted and queryable before enabling expiry, old records disappear after expiry, and original recent storage/query values survive.
- Baseline expiry may be disabled while seeding. Any accelerated compaction/delete scheduling is test-only, must be explicit in evidence, and does not prove the production default two-hour deletion delay.
- Evidence is canonical claim-owned bounded regular files, read once and digest-bound; reject symlink/path escape, substituted inputs, nonfinite timestamps, empty inventories, altered controls and partial deletion.
- No paid provider, YHC, real Codex, production rollout, Ready transition or merge is authorized by this plan. Do not archive OpenSpec automatically.
- Existing cold, twenty-pair matrix and logging recovery acceptance remain separate completed evidence. OpenSpec 3.2 is unchecked until actual retention, final regression and independent review pass.

## 📦 Files and evidence protocol

Private claim: `.superpowers/private-observability-retention1003a`; project: `switchback-obsv-retention1003a`. Private scripts are operator evidence, not production APIs or tracked secrets. Tracked outputs are this plan, the concise retention report, the existing progress ledger and the OpenSpec task checkbox only after all gates pass.

Input `retention-fixture.json` contains `schema_version=observability.retention_fixture.v1`, project, exact output root, `created_at`, `old_at`, `fresh_at`, metric `switchback_retention_probe`, old value `41`, fresh value `42`, and exact synthetic log strings `retention1003a-old` / `retention1003a-fresh`. Labels use exact `claim=<project>,age=old|fresh`.

Physical protocol clarification: fixed origins are `http://127.0.0.1:19291` (Prometheus) and `http://127.0.0.1:19292` (Loki); old/fresh timestamps equal the whole-second creation time minus eight days / four hours. Volume names are `<project>-prometheus-data` / `<project>-loki-data`, with exact `com.switchback.observability-claim` and `com.switchback.observability-service` labels. Config copies are `<root>/prometheus.yaml` / `<root>/loki.yaml`. Inventories additionally require `scan_complete:true`, empty `errors` / `symlinks`, and complete relative `directories`. Loki uses test-only store-only queries to establish persisted-store behavior rather than in-memory ingester behavior; the live default query path, WAL/cache/backup erasure and production two-hour scheduler timing are not asserted. Exact native command arrays and receipt shapes are recorded in the Task 1 report before physical use.

Four fixed snapshot files are `prometheus-before.json`, `prometheus-after.json`, `loki-before.json`, `loki-after.json`. Each contains `schema_version=observability.retention_snapshot.v1`, project, service, phase, `captured_at`, `completed_at`, unmodified Docker `inspect` array, unmodified `volume_inspect` array, `old_query` and `fresh_query` receipts (`url`, status, parsed body, started/completed timestamps), `inventory` from an independently executed read-only helper, and native `runtime_config` HTTP receipt. Inventory contains exact project/service/volume, `observed_at`, a complete regular-file list (`path`, `bytes`, `sha256`; Prometheus meta.json also has parsed `meta`), and no symlinks or omitted scan errors. Operation receipts are retained separately in `operations.jsonl` with argv, start/end times, exit code and stdout/stderr, without secrets.

The verifier derives Prometheus block IDs/ranges from original `meta.json`, checks all original old block paths (including deletion-temporary suffixes) absent afterward, and exact fresh block files unchanged. For Loki v13 filesystem, decode the base64 filename tail into hexadecimal `from:through:checksum`; derive original old/fresh chunk sets from those timestamps, reject mixed or unclassified chunks, require all old objects absent and exact fresh objects unchanged. Do not infer deletion from smaller disk usage or a query filter. Verify policy from native effective configuration and exact runtime commands. Summarize only primary TSDB block / Loki chunk-object expiry; do not claim secure erasure of caches, WAL, backups or authoritative traces.[^1][^2]

### Task 1: Independent private retention verifier

**Files:** Create private `retention-evidence.mjs`, `verify-retention.mjs`, `retention-evidence.test.mjs`; report `.superpowers/sdd/retention-verifier-report.md`.

**Interfaces:** `verifyRetention(project, root)` reads only the fixed fixture and four snapshots and returns a digest-bound result. CLI `node verify-retention.mjs <project> <root>` writes exclusive `retention-passed.json` only on success, schema `observability.retention_result.v1`, scope `aged_synthetic_primary_store_expiry_only`. Reuse the reviewed `safeReader` implementation from the previous private loss helper by copying just that generic utility into this claim; keep the input cache and digest map together.

- [x] Write a valid persisted fixture builder and a regression that changes only the after-query to empty while retaining old chunk/block files. The public verifier must reject this query-only pseudo-proof. Start with this behavioral RED test:

```javascript
test('query exclusion alone is not deletion proof', async () => {
  const {project, root} = await retentionFixture({retainExpiredFiles: true});
  await assert.rejects(() => verifyRetention(project, root), /expired.*storage|old.*remain/i);
});
```

- [x] Run `node --test .superpowers/private-observability-retention1003a/retention-evidence.test.mjs`; retain actual expected failure before the validator is implemented.
- [x] Implement the fixed-file validator using the evidence protocol above. Reject wrong claim/schema/origin/endpoint, HTTP errors, malformed successful API responses, timestamp drift, altered policy, wrong image/volume labels or mounts, missing before old/control records, before/after ordering errors, partial old-object deletion, changed fresh controls and unexpected chunk time ranges. Bind fixture time/labels/value to exact historical query evaluation, not current-time instant queries. Accept only exact pinned runtime image references and the allowed baseline/expiry commands. Final queries must still be valid and successful.
- [ ] Add behavioral negative cases one at a time: wrong claim, missing old baseline, old files retained, one of multiple old files retained, changed fresh bytes/query value, 7-minute policy, image or volume substitution, query time mismatch, HTTP failure, symlink/oversize/FIFO input, invalid timestamp, empty inventory, duplicate paths, CLI nonzero/no PASS on invalid input and exclusive-output refusal. Keep tests synthetic and do not invoke Docker.
- [x] Run the focused Node tests, syntax checks and `git diff --check`. Write a report with RED/GREEN commands/output, file hashes and explicit limitations; private files are not staged. Independent spec and quality review is required before physical verification.

Task 1 functional verifier and independent review are complete (119/119). The negative-coverage item above remains unchecked as a process exception: coverage exists, but some supplemental cases were batched rather than added one at a time; strict per-case RED/GREEN is not claimed. Core pseudo-proof and review-fix regressions have actual RED/GREEN evidence.

### Task 2: Root physical expiry and merge-readiness closeout

**Files:** Private `capture-retention.mjs`, `inventory.go`, synthetic fixtures, native config copies, operation receipts and snapshots; tracked `.superpowers/sdd/g4-retention-report.md`, `.superpowers/sdd/g4-progress.md`, this plan and `openspec/changes/local-observability-stack/tasks.md`.

- [x] Create the claim exclusively using the existing acceptance `claim` mode. Verify pinned local images, command help, empty new project/storage and loopback-only disposable endpoints. Build a static Linux/arm64 inventory helper with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build`; it walks only `/store`, rejects nonregular/symlink entries, bounds files, hashes complete contents, emits complete inventory JSON and never writes the store. Use helper containers with the exact owned volume mounted read-only.
- [x] Generate old/recent OpenMetrics fixtures; use the pinned image's `/bin/promtool tsdb create-blocks-from openmetrics` to create actual TSDB blocks. Run an isolated baseline retaining both with `14d`, record native flags, complete inventory and exact timestamp-bound successful queries; stop the exact baseline container and start the same volume/image with `7d`. Retain the real obsolete-block deletion log and after snapshot.
- [x] Copy the deployed Loki config unchanged except explicit baseline/test controls. Seed separate old/recent streams with old ingestion allowed and baseline compactor retention disabled. Flush via the official endpoint, ensure shipped TSDB indexes/chunks are queried from persisted storage, and capture the before snapshot. Enable `168h` expiry with explicit faster compaction/delete scheduling only in this claim. Wait boundedly for original old chunk objects to disappear, query exclusion and fresh-control survival, retaining real sweeper metrics/logs and after snapshot. Preserve every failed attempt; no manual deletion of tested chunk/block objects.
- [x] Run the independent verifier on actual captures, hash its PASS receipt and raw inputs, and perform an independent physical-evidence review. If it fails, preserve raw inputs and diagnose before retrying; no rewritten historical queries.
- [ ] Run fresh short Go suite, scoped race, vet, fmt/migration/generated protocol/worker-image/secret gates, pinned OpenSpec 1.6 strict validation and diff check. Review the actual PR/base diff with strongest available independent reviewer, resolving all Critical/Important findings before recommending readiness.
- [ ] Stop/remove only exact owned disposable containers/helper networks after checking identities; preserve new data volumes, configs and all evidence. Record counts and hashes; update OpenSpec 3.2 only after all scoped gates pass. Report current PR head/mergeability/reviews/checks and remaining blockers without turning Ready or merging.

October 4 checkpoint: physical PASS and independent physical approval; root cleanup complete for five exact containers and two networks, preserving three volumes and frozen evidence. Formal Go gates, production Web 517 tests/build/47 Chromium cases and strict OpenSpec passed. Whole-PR risk-oriented review withheld readiness due undisposed combined integration-race failures and explicitly limited historical coverage. The last two items stay open; no OpenSpec completion or risk waiver. Targeted final-review diagnosis uses `integration-race-fix-brief.md`, a new isolated PostgreSQL claim and unchanged production policy/assertions.

Final bounded diagnosis: exact three targets passed ten times each, complete localproduct passed once, and original full `go test -race -tags=integration ./... -count=1 -p 2` passed once on the fresh PostgreSQL. Original symptoms did not recur, so this is not a fix or a closed finding. Narrow instrumentation or a captured reproducing artifact is the next checkpoint; no speculative source change. The new exact PostgreSQL container is removed and its data volume/logs retained; total this phase six containers/two owned networks removed, four volumes retained. Whole-branch approval and OpenSpec closeout remain open.

## 🔗 References

[^1]: Grafana Loki v3.7.8 filesystem chunk key encoder. https://raw.githubusercontent.com/grafana/loki/v3.7.8/pkg/storage/chunk/client/object_client.go
[^2]: Prometheus v3.15.0 time retention and real block deletion. https://raw.githubusercontent.com/prometheus/prometheus/v3.15.0/tsdb/db.go
