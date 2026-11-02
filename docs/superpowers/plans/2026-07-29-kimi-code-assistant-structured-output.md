# Kimi Code Assistant Strict Structured Output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `subagent-driven-development` to implement this plan one task at a time. Every
> implementation task uses TDD, is committed independently, and receives a fresh
> independent review before the next task starts.

**Goal:** Evolve `kimi-code-assistant-openai-v1` from historical revision 1 to
immutable revision 2 so every new Kimi Code AssistantRun requests and validates
the exact `assistant.decision.v1` JSON decision through provider-enforced strict
JSON Schema, without introducing Kimi Platform, hidden retries, fallback, or
lenient decoding.

**Architecture:** `internal/contracts/v1` owns one embedded, fully closed
provider schema and its canonical digest. The production profile registry binds
that schema identity and digest into revision 2. The LiteLLM adapter serializes
the policy into the OpenAI-compatible `response_format.json_schema` request;
the existing strict response-envelope decoder, `DecodeAssistantDecision`, and
domain validators remain authoritative after provider output returns. DeepSeek
and historical profile revisions retain their current behavior and identity.

**Tech Stack:** Go 1.26, embedded JSON Schema, `jsonschema/v6`, `httptest`,
PostgreSQL integration tests, LiteLLM 1.93.0, Docker Compose, OpenSpec 1.6.0,
Vitest, Playwright.

**Approved design:** `docs/superpowers/specs/2026-07-29-kimi-code-assistant-profile-design.md`
at commit `51ed11c`.

---

## Global constraints

- Keep profile ID `kimi-code-assistant-openai-v1`; new runs select revision `2`.
- Revision 1 is historical and immutable. Never rewrite stored AssistantRun
  profile ID, revision, digest, request, result, or failure.
- Keep LiteLLM alias `switchback-assistant-kimi-code`, provider model
  `kimi-for-coding`, endpoint `https://api.kimi.com/coding/v1`, deployment scope
  `single_user_local_v1`, and `temperature=1`.
- Kimi Platform, `https://api.moonshot.cn/v1`, Platform credentials, billing
  claims, multi-user enablement, and User-Agent spoofing remain out of scope.
- The strict structured-output policy is typed and digest-bound. Arbitrary
  response-format JSON, provider parameters, headers, or passthrough maps are
  forbidden.
- The exact decision contract is `assistant.decision.v1`.
- The provider schema identity is
  `assistant-decision-strict-json-schema-v1`.
- The provider schema digest is SHA-256 over canonical minified UTF-8 JSON. The
  implementation exposes the digest as `sha256:<lowercase hex>`.
- The provider schema is dedicated to the provider boundary. Do not reuse
  `api/schema/v1/assistant-decision.schema.json`, whose documentation metadata
  and open nested objects are rejected by the Kimi Code strict-schema boundary.
- Every object reachable in the provider schema is closed with
  `additionalProperties: false`; all declared properties are required.
- Provider output must set `checkpoint` to `null`. `ClarificationPlanner`
  remains the sole owner of server-generated `ClarificationCheckpoint`.
- Strict JSON Schema is defense in depth, not a replacement for the existing
  strict LiteLLM envelope decoder, `DecodeAssistantDecision`, readiness
  validation, TaskDraft validation, creation-intent checks, or transaction
  boundaries.
- If the provider ignores/rejects the schema, returns malformed content, or
  returns a decision that fails the domain contract, record a sanitized failure
  against the same exact profile. Do not retry the request, omit
  `response_format`, switch provider, or fall back to a lenient decoder.
- `deepseek-assistant-openai-v1` sends neither `temperature` nor
  `response_format`; its current profile digest must remain exact.
- `AutomaticFallbackProfileID` remains empty.
- Preserve the user's existing uncommitted `web-demo/` files. Do not edit or
  stage them:
  - `web-demo/src/App.tsx`
  - `web-demo/src/views/ObservabilityView.tsx`
  - `web-demo/src/components/charts.tsx`
  - `web-demo/src/mock/observability.ts`
  - `web-demo/src/views/admin/`
- Preserve and finish the already-started Task 18 changes in
  `internal/architecture/local_compose_test.go` and
  `scripts/run-m2-real-provider-acceptance`; do not discard or hide them.
- Do not sync or archive either active OpenSpec change without a new explicit
  user confirmation after implementation and acceptance evidence is presented.
- Before code navigation run `make codegraph-status`, then use CodeGraph for
  indexed source and call-path inspection.
- Never print, persist, commit, or attach API keys, authorization headers,
  environment values, raw provider bodies, hidden reasoning, or unrestricted
  upstream errors.

---

## Target interfaces

```go
type AssistantStructuredOutputPolicy struct {
	Mode                 string `json:"mode"`
	DecisionSchema       string `json:"decision_schema"`
	ProviderSchemaID     string `json:"provider_schema_id"`
	ProviderSchemaDigest string `json:"provider_schema_digest"`
}

type AssistantModelRequestPolicy struct {
	Temperature     *float64                        `json:"temperature,omitempty"`
	StructuredOutput *AssistantStructuredOutputPolicy `json:"structured_output,omitempty"`
}
```

The exact field names may be adjusted to match established local Go naming,
but the serialized profile contract and meaning must remain exact.

The outbound Kimi Code request must contain:

```json
{
  "model": "switchback-assistant-kimi-code",
  "messages": [],
  "temperature": 1,
  "response_format": {
    "type": "json_schema",
    "json_schema": {
      "name": "assistant_decision_v1",
      "strict": true,
      "schema": {}
    }
  }
}
```

The schema object is the embedded canonical provider schema, not a caller-owned
value and not a loose copy constructed by the adapter.

---

## Provider schema

Implement the following semantic schema as canonical minified JSON. Property
ordering is not a semantic JSON Schema requirement, but the embedded bytes used
for the digest must be stable.

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": [
    "schema_version",
    "decision_type",
    "user_visible_content",
    "clarification_question",
    "readiness",
    "task_draft",
    "checkpoint"
  ],
  "properties": {
    "schema_version": {
      "type": "string",
      "const": "assistant.decision.v1"
    },
    "decision_type": {
      "type": "string",
      "enum": ["clarification", "ready_draft", "informational"]
    },
    "user_visible_content": {
      "type": "string",
      "minLength": 1
    },
    "clarification_question": {
      "type": "string"
    },
    "readiness": {
      "type": "object",
      "additionalProperties": false,
      "required": [
        "schema_version",
        "assessment_id",
        "revision",
        "policy_revision",
        "dimensions",
        "blocking_unknowns",
        "assumptions",
        "clarification_questions",
        "status",
        "evaluated_at"
      ],
      "properties": {
        "schema_version": {
          "type": "string",
          "const": "assistant.readiness_assessment.v1"
        },
        "assessment_id": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "type": "integer",
          "minimum": 1
        },
        "policy_revision": {
          "type": "string",
          "minLength": 1
        },
        "dimensions": {
          "type": "array",
          "minItems": 7,
          "maxItems": 7,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["dimension", "status", "summary", "evidence_refs"],
            "properties": {
              "dimension": {
                "type": "string",
                "enum": [
                  "goal",
                  "deliverable_acceptance",
                  "scope_target",
                  "constraints",
                  "context_access",
                  "risk_approval",
                  "execution_policy"
                ]
              },
              "status": {
                "type": "string",
                "enum": ["known", "assumed", "blocking_unknown", "not_applicable"]
              },
              "summary": {
                "type": "string",
                "minLength": 1
              },
              "evidence_refs": {
                "type": "array",
                "items": {
                  "type": "string"
                }
              }
            }
          }
        },
        "blocking_unknowns": {
          "type": "array",
          "items": {
            "type": "string"
          }
        },
        "assumptions": {
          "type": "array",
          "items": {
            "type": "string"
          }
        },
        "clarification_questions": {
          "type": "integer",
          "minimum": 0
        },
        "status": {
          "type": "string",
          "enum": ["clarifying", "ready_confirmed", "ready_with_assumptions"]
        },
        "evaluated_at": {
          "type": "string"
        }
      }
    },
    "task_draft": {
      "anyOf": [
        {
          "type": "null"
        },
        {
          "type": "object",
          "additionalProperties": false,
          "required": [
            "schema_version",
            "task_draft_id",
            "revision",
            "initiator_user_id",
            "conversation_id",
            "workspace_id",
            "workspace_revision_id",
            "readiness_assessment_id",
            "task_spec_digest",
            "title",
            "goal",
            "target",
            "constraints",
            "acceptance_criteria",
            "assumptions",
            "task_spec",
            "created_at"
          ],
          "properties": {
            "schema_version": {
              "type": "string",
              "const": "assistant.task_draft.v1"
            },
            "task_draft_id": {
              "type": "string",
              "minLength": 1
            },
            "revision": {
              "type": "integer",
              "minimum": 1
            },
            "initiator_user_id": {
              "type": "string",
              "minLength": 1
            },
            "conversation_id": {
              "type": "string",
              "minLength": 1
            },
            "workspace_id": {
              "type": "string",
              "minLength": 1
            },
            "workspace_revision_id": {
              "type": "string",
              "minLength": 1
            },
            "readiness_assessment_id": {
              "type": "string",
              "minLength": 1
            },
            "task_spec_digest": {
              "type": "string",
              "minLength": 1
            },
            "title": {
              "type": "string",
              "minLength": 1
            },
            "goal": {
              "type": "string",
              "minLength": 1
            },
            "target": {
              "type": "object",
              "additionalProperties": false,
              "required": ["kind", "ref"],
              "properties": {
                "kind": {
                  "type": "string",
                  "const": "workspace"
                },
                "ref": {
                  "type": "string",
                  "minLength": 1
                }
              }
            },
            "constraints": {
              "type": "array",
              "items": {
                "type": "string"
              }
            },
            "acceptance_criteria": {
              "type": "array",
              "minItems": 1,
              "items": {
                "type": "string"
              }
            },
            "assumptions": {
              "type": "array",
              "items": {
                "type": "string"
              }
            },
            "task_spec": {
              "type": "object",
              "additionalProperties": false
            },
            "created_at": {
              "type": "string"
            }
          }
        }
      ]
    },
    "checkpoint": {
      "type": "null"
    }
  }
}
```

This shape is reconciled against the current Go JSON tags, constants, and
validators in `internal/contracts/v1/assistant.go`. Model-proposed readiness
identity/time and most TaskDraft identity/spec fields are required placeholders
because strict decoding happens before the server replaces them with governed
values. The provider schema may constrain the documented placeholder literals
more tightly during Task 21, but it must not remove any current Go field or
make the runtime decoder more permissive.

---

## Task dependencies

Task 20 freezes the active OpenSpec contract. Task 21 owns the provider schema
and immutable profile revision. Task 22 consumes those interfaces to serialize
the exact LiteLLM request. Task 23 proves the real product path and incorporates
the existing Task 18 acceptance-harness fixes. OpenSpec Task 19 remains a
separately authorized closeout after Task 23.

---

### Task 20: Amend the active OpenSpec change without rewriting history

**Files:**

- Modify: `openspec/changes/kimi-code-assistant-profile/proposal.md`
- Modify: `openspec/changes/kimi-code-assistant-profile/design.md`
- Modify: `openspec/changes/kimi-code-assistant-profile/specs/assistant-conversation-dispatch/spec.md`
- Modify: `openspec/changes/kimi-code-assistant-profile/tasks.md`
- Reference: `docs/superpowers/specs/2026-07-29-kimi-code-assistant-profile-design.md`
- Test: strict OpenSpec validation

**Interfaces:**

- Consumes: approved design commit `51ed11c`
- Produces: apply-ready revision-2 strict-output contract and auditable tasks 20–23

- [ ] **Step 1: Inspect the current change and main requirement**

Run:

```bash
npx --yes @fission-ai/openspec@1.6.0 status \
  --change kimi-code-assistant-profile --json
npx --yes @fission-ai/openspec@1.6.0 show \
  kimi-code-assistant-profile --json
```

Expected: tasks 15–18 remain historical completed work and Task 19 remains
unchecked.

- [ ] **Step 2: Write the revision-2 contract**

Update the proposal and design to record:

- revision 1 solved `temperature=1` but did not provide provider-enforced
  structure;
- revision 2 binds strict structured output to the same profile ID;
- the dedicated provider schema ID/digest and `assistant.decision.v1`;
- fully closed nested objects and `checkpoint=null`;
- no hidden retry/fallback/lenient decode;
- the existing domain validators remain authoritative;
- Kimi Platform remains unsupported.

Do not say revision 2 is deployed or accepted before Tasks 21–23 pass.

- [ ] **Step 3: Add exact requirements and scenarios**

Extend the delta spec with scenarios for:

1. Kimi revision 2 serializes exact strict `json_schema`.
2. Schema identity or digest mismatch fails before HTTP.
3. DeepSeek omits both temperature and response format.
4. Provider schema rejection or malformed output fails the same Run with no
   hidden retry/fallback.
5. Historical revision 1 exact-profile requests fail closed; explicit Retry
   creates a new Run using current revision 2.
6. Provider checkpoint is null and the server planner remains the only
   checkpoint owner.

- [ ] **Step 4: Preserve completed history and add new tasks**

Keep completed tasks 15–18 intact. Append unchecked Tasks 20–23 matching this
plan. Keep Task 19 synchronization/archive unchecked and explicitly dependent
on Task 23 evidence plus new user approval.

- [ ] **Step 5: Validate RED contract state**

Run:

```bash
npx --yes @fission-ai/openspec@1.6.0 validate \
  kimi-code-assistant-profile --strict
git diff --check
```

Expected: validation passes. Runtime code still lacks the revision-2 behavior,
which is intentionally delivered by Tasks 21–22.

- [ ] **Step 6: Commit**

Before staging, check only the matching OpenSpec Task 20; do not change the
status of later implementation tasks.

```bash
git add openspec/changes/kimi-code-assistant-profile
git commit -m "docs: specify strict Kimi Assistant output"
```

---

### Task 21: Own the closed provider schema and immutable profile revision 2

**Files:**

- Create: `internal/contracts/v1/assistant_decision_provider_schema.json`
- Create: `internal/contracts/v1/assistant_response_schema.go`
- Modify: `internal/contracts/v1/contracts_test.go`
- Modify: `internal/platform/config/runtime_profiles.go`
- Modify: `internal/platform/config/runtime_profiles_test.go`
- Modify: `internal/localproduct/config_test.go`
- Modify if required by `go mod tidy`: `go.mod`
- Modify if required by `go mod tidy`: `go.sum`
- Modify: `openspec/changes/kimi-code-assistant-profile/tasks.md`

**Interfaces:**

- Consumes: Task 20 contract and current Go decision types
- Produces: embedded schema bytes, stable schema ID/digest, defensive schema
  access, typed structured-output policy, and current profile revision 2

- [ ] **Step 1: Reconcile the plan schema with current Go types**

Use CodeGraph to inspect the full decision/readiness/TaskDraft types and
validators. Record any contract mismatch in the approved design and this plan
before implementation.

- [ ] **Step 2: Write failing provider-schema contract tests**

Tests must prove:

- embedded JSON parses and contains no `$schema`, `$id`, or `title`;
- every reachable object is closed;
- clarification, informational, and ready TaskDraft decisions validate;
- `DecodeAssistantDecision` accepts each valid fixture;
- extra top-level, readiness, dimension, target, TaskDraft, or task-spec
  properties are rejected;
- non-null provider checkpoint is rejected;
- wrong schema versions/enums and malformed dimensions are rejected;
- the exported schema accessor returns a defensive copy;
- canonical bytes and digest are stable and exact.

Use the existing `jsonschema/v6` dependency for test validation. Do not add a
second JSON Schema engine.

- [ ] **Step 3: Run RED schema tests**

Run:

```bash
go test ./internal/contracts/v1
```

Expected: FAIL because the embedded schema and exported contract do not exist.

- [ ] **Step 4: Implement the provider-schema owner**

Use `go:embed` to own the canonical schema in `internal/contracts/v1`. Expose
only typed constants and a defensive-copy accessor. Compute or verify the
digest from the embedded canonical bytes; never accept a caller-provided schema
or digest.

- [ ] **Step 5: Run GREEN schema tests**

Run:

```bash
go test ./internal/contracts/v1
```

Expected: PASS.

- [ ] **Step 6: Write failing profile revision/policy tests**

Add assertions that:

- `kimi-code-assistant-openai-v1` resolves to revision 2;
- temperature is exactly 1;
- structured-output mode, decision schema, provider schema ID, and provider
  schema digest match the contract owner;
- schema identity/digest participates in the profile digest;
- registry clones do not share structured-policy pointers;
- wrong mode, decision schema, provider schema ID, or digest fails validation;
- revision-1 exact refs fail current-profile matching;
- `deepseek-assistant-openai-v1` has no structured-output policy and retains its
  current exact digest;
- local-product validation accepts the valid Kimi revision-2 profile and rejects
  tampered policy/digest/scope.

- [ ] **Step 7: Run RED profile tests**

Run:

```bash
go test ./internal/platform/config ./internal/localproduct
```

Expected: FAIL because current Kimi profile is revision 1 and the structured
policy is absent.

- [ ] **Step 8: Implement the typed revision-2 policy**

Extend profile cloning, canonical digest calculation, and validation. The
registry must construct Kimi policy only from `internal/contracts/v1`
constants/accessors. DeepSeek serialization and digest must remain unchanged.

- [ ] **Step 9: Run GREEN focused tests and hygiene**

Run:

```bash
go test ./internal/contracts/v1 ./internal/platform/config ./internal/localproduct
go test -race ./internal/contracts/v1 ./internal/platform/config
go mod tidy
git diff --check
```

Expected: all tests pass; `go mod tidy` makes no unrelated dependency changes.

- [ ] **Step 10: Mark Task 21 and commit**

Check only the matching OpenSpec Task 21.

```bash
git add internal/contracts/v1 internal/platform/config/runtime_profiles.go \
  internal/platform/config/runtime_profiles_test.go \
  internal/localproduct/config_test.go go.mod go.sum \
  openspec/changes/kimi-code-assistant-profile/tasks.md
git commit -m "feat(assistant): bind strict output schema to Kimi profile"
```

If `go.mod` or `go.sum` did not change, omit them from `git add`.

---

### Task 22: Serialize exact strict JSON Schema through LiteLLM

**Files:**

- Modify: `internal/interaction/adapter/litellm/client.go`
- Modify: `internal/interaction/adapter/litellm/client_test.go`
- Add:
  `internal/interaction/adapter/litellm/testdata/assistant_decision_provider_schema.json`
- Modify: `internal/interaction/adapter/postgres/commands.go`
- Modify: `internal/interaction/adapter/postgres/commands_integration_test.go`
- Modify: `internal/gateway/adapter/postgres/commands.go`
- Add: `internal/gateway/adapter/postgres/commands_integration_test.go`
- Modify as required by fixture profile refs:
  `internal/interaction/application/*_test.go`
- Modify as required by fixture profile refs:
  `internal/frontier/http/*_test.go`, `internal/frontier/sse/*_test.go`
- Modify: `openspec/changes/kimi-code-assistant-profile/tasks.md`
- Modify:
  `docs/superpowers/plans/2026-07-29-kimi-code-assistant-structured-output.md`

**Interfaces:**

- Consumes: Task 21 typed policy and provider schema
- Produces: exact OpenAI-compatible `response_format` request and fail-closed
  pre-HTTP policy validation

- [ ] **Step 1: Write failing exact-request tests**

Through `httptest`, assert the decoded outbound JSON:

- Kimi has exactly `messages`, `model`, `response_format`, and `temperature`;
- `response_format.type` is exactly `json_schema`;
- JSON-schema name is exactly `assistant_decision_v1`;
- `strict` is true;
- schema equals the contract owner's embedded schema semantically and by
  canonical bytes after normalization;
- temperature is numeric 1;
- DeepSeek has exactly `messages` and `model`;
- no authorization/header assertion logs secret values.

- [ ] **Step 2: Write failing preflight and failure tests**

Tests must prove:

- wrong structured-output mode/schema ID/schema digest fails before HTTP;
- historical revision 1 exact profile ref fails before HTTP;
- explicit Retry preserves the historical failed Run and creates a new Run
  with the Gateway's current server-owned profile, readiness policy, context
  budget, and fresh timeout; same-key replay returns the original new Run even
  if later service configuration changes;
- provider non-2xx, malformed envelope, non-JSON content, schema-invalid
  content, and domain-invalid decisions produce sanitized failure;
- each failed call makes exactly one provider request;
- no retry without `response_format`, no provider fallback, and no permissive
  decoder path exists;
- valid clarification, informational, and ready decisions still pass the
  existing decoder and validators.

- [ ] **Step 3: Run RED adapter tests**

Run:

```bash
go test ./internal/interaction/adapter/litellm
```

Expected: FAIL because `response_format` is absent.

- [ ] **Step 4: Implement typed request serialization**

Add private request DTOs matching:

```go
type completionResponseFormat struct {
	Type       string                       `json:"type"`
	JSONSchema completionResponseJSONSchema `json:"json_schema"`
}

type completionResponseJSONSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}
```

The adapter may decode a defensive copy of the embedded schema into the private
DTO at client construction or request construction, but it must verify the
profile's exact schema identity/digest first. Keep `response_format` nil for
profiles without the structured-output policy.

- [ ] **Step 5: Preserve strict inbound ownership**

Do not change the success path to trust provider schema enforcement. Continue
using the bounded LiteLLM envelope, one exact content string,
`DecodeAssistantDecision`, and current application validators.

- [ ] **Step 6: Run GREEN focused and integration tests**

Run:

```bash
go test ./internal/interaction/adapter/litellm \
  ./internal/interaction/application \
  ./internal/frontier/http \
  ./internal/frontier/sse
go test -race ./internal/interaction/adapter/litellm \
  ./internal/interaction/application
go test -race -tags=integration \
  ./internal/interaction/adapter/postgres \
  ./internal/gateway/adapter/postgres \
  -run 'TestRevisionedConversationCommandsConvergeAcrossCreateOpenSelectCancelAndRetry|TestGatewayExplicitRetryUsesCurrentServerOwnedAssistantProfile' \
  -count=1
git diff --check
```

Expected: all tests pass and existing non-Kimi behavior remains unchanged.

- [ ] **Step 7: Run static forbidden-surface checks**

Run:

```bash
if git grep -n -E 'api\.moonshot\.cn|KIMI_PLATFORM' -- \
  ':!docs/superpowers/specs/2026-07-29-kimi-code-assistant-profile-design.md' \
  ':!docs/superpowers/plans/2026-07-29-kimi-code-assistant-*.md' \
  ':!openspec/changes/kimi-code-assistant-profile/**'; then
  exit 1
fi
git diff --check
```

Expected: no unsupported runtime/configuration surface is found.

- [ ] **Step 8: Mark Task 22 and commit**

Check only the matching OpenSpec Task 22 and stage only the files actually
changed:

```bash
git add internal/interaction/adapter/litellm \
  internal/interaction/adapter/postgres/commands.go \
  internal/interaction/adapter/postgres/commands_integration_test.go \
  internal/gateway/adapter/postgres/commands.go \
  internal/gateway/adapter/postgres/commands_integration_test.go \
  openspec/changes/kimi-code-assistant-profile/tasks.md \
  docs/superpowers/plans/2026-07-29-kimi-code-assistant-structured-output.md
git commit -m "feat(assistant): request strict Kimi decisions"
```

---

### Task 23: Prove real revision-2 Assistant-to-Worker behavior

**Files:**

- Modify: `internal/architecture/local_compose_test.go`
- Modify: `scripts/run-m2-real-provider-acceptance`
- Modify as evidence justifies:
  `docs/status/status-2026-07-28-production-web-core-experience.md`
- Modify: `openspec/changes/kimi-code-assistant-profile/tasks.md`
- Modify only after exact evidence:
  `openspec/changes/production-web-core-experience/tasks.md`
- Modify: `.superpowers/sdd/progress.md`

**Interfaces:**

- Consumes: reviewed Tasks 20–22 and existing uncommitted Task 18 harness fixes
- Produces: deterministic summary assertions, real-provider evidence, full
  repository/Web gates, and an explicit sync/archive approval boundary

- [ ] **Step 1: Review and test the existing uncommitted harness changes**

Treat the current changes in
`internal/architecture/local_compose_test.go` and
`scripts/run-m2-real-provider-acceptance` as Task 23 input. They must assert
canonical direct summary fields for:

- alias and exact-one-Task convergence;
- Home projection;
- Artifact projection;
- Retry idempotency key bound to exact `assistant_run_id` and revision.

Do not replace them with weaker nested-summary checks.

- [ ] **Step 2: Close the two runtime-review minors if still valid**

Re-evaluate against current source:

1. Widen the Task B convergence timeout from 1 second to a bounded 3–5 seconds
   if the full-flow test can legitimately race asynchronous convergence.
2. Add deterministic tests for RuntimeSupervisor cleanup/restart and
   `stopAndWait` rejecting new starts if those paths remain untested.

Only implement a minor if source/test evidence confirms the gap; otherwise
record why it is not applicable in the review report.

- [ ] **Step 3: Run focused automated gates**

Run:

```bash
go test ./internal/contracts/v1 \
  ./internal/platform/config \
  ./internal/localproduct \
  ./internal/interaction/adapter/litellm \
  ./internal/interaction/application \
  ./internal/eventingprojection/application \
  ./internal/frontier/http \
  ./internal/frontier/sse \
  ./internal/architecture
go test -race ./internal/interaction/adapter/litellm \
  ./internal/interaction/application
sh -n scripts/run-m2-real-provider-acceptance
npx --yes @fission-ai/openspec@1.6.0 validate \
  kimi-code-assistant-profile --strict
git diff --check
```

Expected: all focused gates pass.

- [ ] **Step 4: Rebuild the exact local runtime**

Build the Assistant LiteLLM sidecar and Backend/runtime images through the
repository-owned Compose/Make targets. Verify, without printing secrets:

- active Backend profile is `kimi-code-assistant-openai-v1` revision 2;
- profile digest matches the registry;
- sidecar alias is `switchback-assistant-kimi-code`;
- sidecar has no published host port;
- only the Kimi Code key path is mounted/used.

- [ ] **Step 5: Run the focused real-provider acceptance**

Run:

```bash
SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=kimi-code-assistant-openai-v1 \
  make m2-real-provider-acceptance
```

Expected: one summary path is printed and the ignored summary reports
`schema_version=switchback.m2-real-provider-acceptance.v1`,
`outcome=passed`, exact alias/profile, one Task after confirmation, delivered
Report/Patch artifacts with checksum/provenance, rework/accept behavior, and
current Home/Worker/Artifact projections.

If the real provider rejects the schema or returns invalid content, stop the
evidence update. Preserve sanitized failure classification and do not add a
fallback or claim acceptance.

- [ ] **Step 6: Run full repository and Web gates**

Start a uniquely named disposable PostgreSQL 16 container on an unused
loopback port. Export only its `SWITCHBACK_TEST_DATABASE_URL`, then run:

```bash
make verify
npm --prefix web test
npm --prefix web run build
npm --prefix web run test:browser
```

Stop and remove only that exact disposable database container.

Expected: formatting, migrations, generated protocols, image checks, unit,
race, vet, integration, secret, OpenSpec, Web unit/build, and Playwright gates
pass. Report any non-blocking bundle advisory separately from failures.

- [ ] **Step 7: Prove privacy and worktree boundaries**

Run sanitized checks that prove:

- no provider envelope, authorization/API-key field, hidden reasoning, or raw
  provider body was persisted;
- no Kimi Platform endpoint or credential surface exists;
- no Task 23 committed diff touches `web-demo/`;
- the user's five original `web-demo/` dirty paths remain present and
  unchanged.

Use a pre-task checksum or exact diff snapshot for the user-owned files; do not
stage them.

- [ ] **Step 8: Update evidence truth only after all matching gates pass**

Preserve the original HTTP 400 as historical evidence. Add a dated resolution
that records sanitized:

- before/after profile revisions and digests;
- exact verification commands and exit codes;
- runtime image digest;
- Conversation, AssistantRun, Task, WorkerSession, WorkerRun, AgentTurn, and
  Artifact references;
- acceptance summary path;
- confirmation that only Kimi Code, not Kimi Platform, was used;
- remaining non-blocking advisories.

Check OpenSpec Task 23. Check production Web 6.3 only if every original 6.3
acceptance condition is now evidenced. Leave Kimi Task 19 and production Web
6.5 unchecked.

- [ ] **Step 9: Validate evidence and commit**

Run:

```bash
npx --yes @fission-ai/openspec@1.6.0 validate \
  kimi-code-assistant-profile --strict
npx --yes @fission-ai/openspec@1.6.0 validate \
  production-web-core-experience --strict
npx --yes @fission-ai/openspec@1.6.0 list --json
git diff --check
git diff --quiet 51ed11c..HEAD -- web-demo
```

Append the durable ledger only when all matching evidence exists:

```text
Task 23: complete (Kimi Code Assistant revision 2 strict JSON Schema, real provider flow, canonical summary projections, and full product gates passed; OpenSpec synchronization/archive remains pending explicit approval).
```

Commit only Task 23-owned files:

```bash
git add internal/architecture/local_compose_test.go \
  scripts/run-m2-real-provider-acceptance \
  docs/status/status-2026-07-28-production-web-core-experience.md \
  openspec/changes/kimi-code-assistant-profile/tasks.md \
  openspec/changes/production-web-core-experience/tasks.md \
  .superpowers/sdd/progress.md
git commit -m "test(assistant): prove strict Kimi product flow"
```

Omit any unchanged optional evidence file from staging.

---

## Independent review protocol

After each implementation task:

1. The implementer runs the task-local tests and commits only task-owned files.
2. A fresh reviewer inspects the fixed base/head range for both OpenSpec
   compliance and repository standards.
3. Important findings return to a new fix task, followed by re-review.
4. Minor findings are either fixed before Task 23 or explicitly triaged with
   source/test evidence.
5. The controller records commit range, tests, review verdict, and ownership in
   `.superpowers/sdd/progress.md`.

Before any completion claim, run the `verification-before-completion` workflow
against the final HEAD and current dirty worktree. Passing focused tests does
not substitute for the real-provider or full product gates.

---

## Deferred Task 19 boundary

After Task 23 passes, present the exact OpenSpec sync impact:

- `assistant-conversation-dispatch` gains revision-2 strict structured-output
  policy and fail-closed scenarios;
- `production-web-core-experience` becomes a main capability spec only if its
  original closeout gates passed;
- no unrelated main spec changes.

Wait for a new explicit user confirmation before checking Task 19/6.5, syncing,
or archiving. Implementation approval for this plan is not archive approval.
