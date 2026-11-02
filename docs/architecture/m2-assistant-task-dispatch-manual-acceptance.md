# M2 Assistant Task Dispatch Manual Acceptance

## Purpose

This runbook proves the M2 product path with a real Assistant provider and a
real Codex Worker while keeping credentials and user/model content out of
terminal output and committed files.

The primary gate is automated. Browser inspection is a product-quality check,
not a substitute for deterministic assertions.

## Prerequisites

- Docker Engine and Compose are available.
- `deploy/local/.env` exists through `make local-env`.
- Ignored Kimi Code and DeepSeek key files named by that environment file are
  populated. Kimi Platform keys are unsupported.
- Local ports `4173`, `8080`, and `3000` are available.
- The M1 fixture under `test/fixtures/m1/workspace-repository` is unchanged.

Do not export or print provider keys. Scripts use mounted files and emit only
status, identity, checksum, and path metadata.

## Automated Real-Provider Gate

Use DeepSeek:

```bash
SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=deepseek-assistant-openai-v1 \
  make m2-real-provider-acceptance
```

Use Kimi Code explicitly (the canonical Kimi acceptance command):

```bash
SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=kimi-code-assistant-openai-v1 \
  make m2-real-provider-acceptance
```

`kimi-code-assistant-openai-v1` is local-only. It uses a Kimi Code key; Kimi
Platform is unsupported. The acceptance runner verifies that Backend selected
the requested exact profile and that `assistant-litellm` has no published host
port. There is no provider fallback or User-Agent spoofing. Historical Runs
that reference the retired profile fail closed; an explicit user Retry creates
a new Run under the configured profile.

When the already-correct stack is running:

```bash
SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=deepseek-assistant-openai-v1 \
SWITCHBACK_M2_REAL_PROVIDER_SKIP_LOCAL_UP=1 \
  make m2-real-provider-acceptance
```

The script proves:

1. the active Backend profile and private sidecar are healthy;
2. an ambiguous message produces a completed clarification decision;
3. an exact message converges to a current ready TaskDraft;
4. exact confirmation creates one Task;
5. the existing Worker path starts a real Codex Worker;
6. first delivery contains report and patch with checksum, Run and Turn
   provenance plus a complete durable Display checkpoint;
7. ordinary chat against the delivered Task creates a second Run/Turn in the
   same Task and WorkerSession;
8. explicit Request changes creates a third same-Task Run/Turn;
9. Accept changes the Task to canonical `completed`/product `Accepted`;
10. a message after acceptance creates a standalone Run/Turn in the same live
    WorkerSession without changing the accepted Task revision;
11. every new Run publishes fresh Report/Patch Artifacts and durable Agent/Tool
    display parts;
12. browser-facing WorkerDetail and Artifact responses contain no raw provider
    envelope, hidden reasoning, authorization or API-key field;
13. the source fixture remains immutable and generated acceptance files contain
    no configured known token.

Success prints only:

```text
m2_real_provider_summary=/absolute/path/to/runtime-data/.../m2-real-provider-summary.json
```

The ignored summary has schema
`switchback.m2-real-provider-acceptance.v1` and `outcome: "passed"`.

## Browser Product Check

Start the stack with the same profile used by the gate:

```bash
SWITCHBACK_ASSISTANT_MODEL_PROFILE=deepseek-assistant-openai-v1 make local-up
```

Open [Switchback Web](http://127.0.0.1:4173), then:

1. open the Assistant route and create a Conversation;
2. select the visible local Workspace;
3. send an intentionally ambiguous request and verify exactly one
   clarification question appears;
4. send a precise goal, deliverables, acceptance criteria, constraints, and
   manual acceptance request;
5. inspect seven-dimension readiness and visible assumptions;
6. confirm no raw JSON editor, provider payload, credential, or hidden
   reasoning is shown;
7. confirm the exact current TaskDraft once;
8. verify pending state remains until the causal projection arrives;
9. follow the canonical Task link, then the Worker link;
10. verify Agent output is expanded, Tool Calls are collapsible, and refresh
    preserves the same Run/Turn groups;
11. send ordinary feedback and verify a new Run/Turn appears under the same
    delivered Task and WorkerSession;
12. request changes and verify another same-Task delivery;
13. review Report Markdown and a real Diff/Files view, including provenance;
14. accept and verify the Task label becomes `Accepted`;
15. send another message and verify it is a standalone Worker follow-up while
    the old Task remains accepted.

Repeat at a narrow mobile viewport. Composer, clarification, checkpoint,
TaskDraft, and links must remain usable without overlap.

## Failure Matrix

```bash
make failure-acceptance
```

The ignored summary must report `outcome: "passed"` and cover:

- Backend restart before/after model invocation boundary;
- concurrent messages with one winning revision;
- model timeout and malformed output with sanitized diagnostics;
- stale Workspace and stale TaskDraft confirmation;
- duplicate outbox delivery;
- Task admission rollback with no partial authority;
- ObjectStore failure independent from committed state;
- Frontier replay gap and snapshot resync.

## Standard Verification

```bash
go test ./internal/interaction/... ./internal/platform/config ./internal/architecture
make verify
npm --prefix web test
npm --prefix web run build
npm --prefix web run test:browser
npx --yes @fission-ai/openspec@1.6.0 validate \
  m2-assistant-task-dispatch-vertical-slice --strict
make local-config
```

Check Compose:

```bash
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml ps
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:4173/healthz
```

Expected:

- Backend, Web, Assistant LiteLLM, PostgreSQL, and Langfuse dependencies are
  running and healthy;
- `switchback-migrate` exited successfully;
- `assistant-litellm` exposes no host port;
- Backend is the only service with the Docker Socket;
- the Assistant sidecar image reports LiteLLM `1.93.0`.

## Restart And Degraded-Mode Checks

Restart Backend while a message is queued:

```bash
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml restart switchback-backend
```

The Conversation remains durable. Recovery must either resume safe queued work
or expose a retryable unknown-effect failure; it must not silently duplicate a
provider request.

Disable the M2 route while preserving direct TaskSpec:

```bash
SWITCHBACK_M2_ASSISTANT_ENABLED=0 make local-up
```

The Assistant path is unavailable/degraded, existing M2 data remains readable,
and the direct Task form remains the supported fallback.

Stop new Display materialization while preserving execution:

```bash
SWITCHBACK_WORKER_DISPLAY_PROJECTION_ENABLED=0 make local-up
```

`SWITCHBACK_WORKER_DISPLAY_PROJECTION_ENABLED=0` stops new durable/transient Display
materialization but leaves Trace, Task, Worker and Artifact facts untouched.
Existing Display checkpoints remain readable and migration `000011` is not
reversed. Workbench is the sole production Artifact presentation owner; a Web
renderer regression uses an ordinary Git revert to a compatible Web commit and
image rebuild, not a hidden fallback UI.

## Rollback Record

Record:

- timestamp and operator;
- prior and replacement Assistant profile;
- whether new Assistant commands were disabled;
- counts of queued, claimed, failed, and completed AssistantRuns;
- any run with unknown provider effect;
- whether backend Display projection was disabled and which Web commit was deployed;
- the latest complete Display checkpoint and any `non_backfillable` Turn;
- last applied `switchback_schema_migrations` version;
- retained evidence and acceptance summary paths.

Do not remove `000010` schema objects, wipe data volumes, delete JSONL evidence,
or mutate Tasks already admitted through Assistant confirmation.

## Verification Record: 2026-07-25

Change: `worker-workbench-interaction-hardening`

Automated verification:

```bash
go test ./...
SWITCHBACK_TEST_DATABASE_URL='postgres://switchback:switchback-test@127.0.0.1:56193/switchback_test?sslmode=disable' \
  go test -tags=integration ./...
go test -race ./...
go vet ./...
make fmt-check migration-check generated-protocol-check worker-image-check secret-scan
git diff --check
sh -n scripts/run-m2-real-provider-acceptance
npm --prefix web test
npm --prefix web run build
npm --prefix web run test:browser
```

Results:

- all Go unit, PostgreSQL integration, race and vet suites passed;
- 15 Vitest files and 50 tests passed;
- 9 Playwright tests passed across desktop and mobile behavior;
- production Web build passed; Vite reported only the known Workbench bundle
  size warning;
- format, migration, generated protocol, Worker image provenance, secret scan,
  shell syntax and diff whitespace gates passed.

Real-provider verification:

```bash
SWITCHBACK_ASSISTANT_MODEL_PROFILE=deepseek-assistant-openai-v1 \
  make m2-real-provider-acceptance
```

Evidence:

- summary:
  `runtime-data/m2-real-provider/run-20260725T170100Z/m2-real-provider-summary.json`;
- Conversation:
  `assistant-conversation-af3e5e9d0d1ad726deeb64d92e40fc05`;
- Task: `task-2d07150545cb670ed5766e1f0306dfa9`;
- WorkerSession: `worker-session-05c404aa7b5bcc430cec18cfa572e395`;
- first delivery Run/Turn:
  `worker-run-f8bb0c7295df308e4f41c212ca451f29` /
  `agent-turn-337115687ef835920c4c95a422f8c083`;
- direct delivered chat Run/Turn:
  `worker-run-71b262f0c84eac4d13b4d5516b3f412e` /
  `agent-turn-b89b5b04e538bc6ba904ef6faa7631ad`;
- same-Task rework Run/Turn:
  `worker-run-4e3a84c40ced7c2ac4bc59d6b18e5cd8` /
  `agent-turn-ef6ecee3cac3a2c664d01ae93065b5e7`;
- standalone post-Accept Run/Turn:
  `worker-run-1f2262e78a847f1bbfacef61a2ed0478` /
  `agent-turn-128bee6338ddad554b140d4199fcc37c`;
- final Task revision `9` remained `completed`, rendered as `Accepted`, while
  the standalone follow-up reused the same WorkerSession without mutating the
  Task.

The acceptance script verified per-stage Run/Turn creation, report and patch
provenance, checksum-backed browser reads, durable Display parts, immutable
fixture input, and absence of known provider tokens or raw credential/provider
field patterns in persisted work data and Worker/Artifact responses.

Browser verification against the real WorkerSession confirmed:

- four durable Run/Turn groups with Agent text, Tool Calls and Artifact events;
- Workbench default selection follows the latest standalone Run/Turn;
- Report Markdown, Diff and Files render without horizontal page overflow;
- no provider token or raw authorization/provider request/response field is
  present in the rendered DOM.
