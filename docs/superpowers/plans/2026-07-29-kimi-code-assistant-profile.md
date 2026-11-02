# Kimi Code Assistant Profile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the existing Kimi Code key complete the real loopback Assistant-to-Task-to-Worker flow through an immutable, profile-owned request policy.

**Architecture:** Add a new `kimi-code-assistant-openai-v1` profile whose digest owns `temperature=1` and `single_user_local_v1` eligibility. The Backend serializes only typed profile parameters, while the private LiteLLM sidecar continues to own the Kimi Code endpoint and mounted key. Historical AssistantRuns remain immutable and provider failure remains fail-closed without fallback.

**Tech Stack:** Go 1.26, PostgreSQL integration tests, LiteLLM 1.93.0, Docker Compose, shell acceptance harnesses, OpenSpec 1.6.0, Vitest, Playwright.

## 📋 Global constraints

- `kimi-code-assistant-openai-v1` is the only new Kimi Assistant profile identity.
- The profile uses revision `1`, LiteLLM alias `switchback-assistant-kimi-code`, model `kimi-for-coding`, endpoint `https://api.kimi.com/coding/v1`, deployment scope `single_user_local_v1`, and `temperature=1`.
- Kimi Platform, `https://api.moonshot.cn/v1`, Platform credentials, billing claims, and multi-user support are out of scope.
- `AssistantModelProfile` accepts only typed request policy; arbitrary JSON parameters, headers, and provider passthrough are forbidden.
- `deepseek-assistant-openai-v1` sends no `temperature`, and its existing digest must remain stable.
- `AutomaticFallbackProfileID` remains empty; no provider switching or hidden request retry is added.
- Historical AssistantRun profile ID, revision, and digest are never rewritten.
- Provider keys, authorization headers, environment values, raw hidden reasoning, and unrestricted provider bodies never enter logs, Trace, status documents, profile JSON, or git.
- Do not modify the Worker `kimi-codex-anthropic-v1` profile or its credential path.
- Preserve all existing uncommitted files under `web-demo/`; never stage or modify them.
- Run `make codegraph-status` before code navigation, then use CodeGraph before raw code reads.
- The current `production-web-core-experience` change stays active until its real 6.3 gate passes and the user approves delta synchronization.
- OpenSpec delta synchronization and archive require an explicit impact summary and user confirmation after implementation evidence exists.

---

## 📦 File structure

| File | Responsibility |
| --- | --- |
| `openspec/changes/kimi-code-assistant-profile/` | Provider-profile proposal, design, delta spec, and task truth |
| `internal/platform/config/runtime_profiles.go` | Immutable Assistant profile and typed request-policy registry |
| `internal/platform/config/assistant.go` | Default Assistant profile selection |
| `internal/localproduct/config.go` | Deployment-scope enforcement |
| `internal/interaction/adapter/litellm/client.go` | Exact profile-driven OpenAI request serialization |
| `images/assistant-litellm/` | Private Kimi Code alias, endpoint, and key-file loading |
| `deploy/local/compose.yaml` | Single-user local profile and sidecar wiring |
| `scripts/run-m2-real-provider-acceptance` | Automated real Assistant-to-Worker acceptance |
| `docs/architecture/m2-assistant-task-dispatch*.md` | Operator contract and acceptance instructions |
| `docs/status/status-2026-07-28-production-web-core-experience.md` | Historical blocker plus final resolution evidence |

## 🔄 Task dependencies

Task 14 defines the OpenSpec contract. Task 15 produces the profile interface consumed by
Task 16. Task 17 wires the interface into the local deployment and acceptance harness.
Task 18 runs the complete evidence gates. Task 19 is a separately authorized
synchronization and archive closeout.

---

### Task 14: Create the Kimi Code Assistant OpenSpec change

**Files:**
- Create: `openspec/changes/kimi-code-assistant-profile/proposal.md`
- Create: `openspec/changes/kimi-code-assistant-profile/design.md`
- Create: `openspec/changes/kimi-code-assistant-profile/specs/assistant-conversation-dispatch/spec.md`
- Create: `openspec/changes/kimi-code-assistant-profile/tasks.md`
- Reference: `docs/superpowers/specs/2026-07-29-kimi-code-assistant-profile-design.md`
- Test: OpenSpec strict validation

**Interfaces:**
- Consumes: approved design commit `b77360b`
- Produces: apply-ready OpenSpec change `kimi-code-assistant-profile`

- [ ] **Step 1: Verify the change is absent and inspect OpenSpec instructions**

Run:

```bash
test ! -e openspec/changes/kimi-code-assistant-profile
npx --yes @fission-ai/openspec@1.6.0 new change kimi-code-assistant-profile
npx --yes @fission-ai/openspec@1.6.0 status --change kimi-code-assistant-profile --json
```

Expected: the new change exists and the status JSON reports the artifact dependency order.

- [ ] **Step 2: Write the proposal**

Create `proposal.md` with this decision content:

```markdown
# Change: Support Kimi Code Assistant profile

## Why

The loopback Assistant currently sends a client-owned `temperature=0` to the
Kimi Code OpenAI endpoint, which rejects the request because that model accepts
only `temperature=1`. Request policy must be immutable, server-owned, and
auditable through the Assistant profile.

## What changes

- Add `kimi-code-assistant-openai-v1` for `single_user_local_v1`
- Add typed optional Assistant request policy with `temperature`
- Send exact profile-owned parameters through LiteLLM
- Retire the old Kimi Assistant profile from new local configuration
- Preserve historical Run identity and fail closed without fallback
- Prove the full real Assistant-to-Worker acceptance flow

## Capabilities

### Modified

- `assistant-conversation-dispatch`

## Impact

Configuration, Interaction LiteLLM adapter, private Assistant LiteLLM image,
local Compose, acceptance tooling, operator documentation, and the active
production Web closeout evidence change.
```

- [ ] **Step 3: Write the OpenSpec design**

Use the approved design document as the source of truth. The OpenSpec `design.md` must
include these exact decisions:

```markdown
## Decisions

1. Create `kimi-code-assistant-openai-v1`; do not redefine the old profile ID.
2. Put optional typed request policy and deployment scope inside the profile digest.
3. Send `temperature=1` only for Kimi Code; omit temperature for DeepSeek.
4. Enforce `single_user_local_v1` before serving traffic.
5. Preserve old Run refs; mismatch fails with the existing model-policy conflict.
6. Keep the existing Kimi Code key file and endpoint; add no Platform surface.
7. Require real-provider and complete product acceptance before closeout.
```

Document rejected alternatives: in-place profile mutation and LiteLLM-only parameter
override.

- [ ] **Step 4: Write the delta specification**

Add a modified version of `Assistant model output is an untrusted typed proposal` and one
new requirement:

```markdown
## MODIFIED Requirements

### Requirement: Assistant model output is an untrusted typed proposal
The system SHALL route one exact server-owned model profile through LiteLLM per
AssistantRun, SHALL derive provider request parameters only from that profile's
typed and digest-bound request policy, and SHALL validate a versioned typed
AssistantDecision before it can change Conversation, readiness, TaskDraft,
intent, Task, or Worker state.

#### Scenario: Profile owns the Kimi Code request
- **WHEN** a new local AssistantRun uses `kimi-code-assistant-openai-v1`
- **THEN** the request uses `switchback-assistant-kimi-code` with
  `temperature=1`, and the Run records the exact profile revision and digest

#### Scenario: Profile does not declare temperature
- **WHEN** a selected supported profile has no temperature policy
- **THEN** the Backend omits `temperature` instead of applying a client default

#### Scenario: Selected provider fails
- **WHEN** the Assistant request times out, returns non-2xx, or violates the
  typed decision contract
- **THEN** the Run records a sanitized failure for the same profile and does
  not retry through another provider

## ADDED Requirements

### Requirement: Kimi Code Assistant is local-profile scoped
The system SHALL allow `kimi-code-assistant-openai-v1` only under
`single_user_local_v1`, using the mounted Kimi Code key and
`https://api.kimi.com/coding/v1`.

#### Scenario: Loopback local product selects Kimi Code
- **WHEN** the fixed local product loads the Kimi Code Assistant profile
- **THEN** startup accepts the profile without exposing the key in Backend
  configuration, profile JSON, Trace, or Frontier state

#### Scenario: Another deployment profile selects Kimi Code
- **WHEN** the Kimi Code Assistant profile scope differs from the active
  deployment profile
- **THEN** startup fails before the listener or provider client becomes active

#### Scenario: Configuration attempts Kimi Platform
- **WHEN** configuration introduces a Kimi Platform endpoint, key, or fallback
- **THEN** repository and configuration gates reject the unsupported surface
```

- [ ] **Step 5: Write implementation tasks and validate**

The OpenSpec tasks must map one-to-one to Tasks 15–19 in this plan and leave
synchronization/archive unchecked until user confirmation.

Run:

```bash
npx --yes @fission-ai/openspec@1.6.0 validate kimi-code-assistant-profile --strict
npx --yes @fission-ai/openspec@1.6.0 status --change kimi-code-assistant-profile
git diff --check
```

Expected: strict validation passes and the change is apply-ready.

- [ ] **Step 6: Commit**

```bash
git add openspec/changes/kimi-code-assistant-profile
git commit -m "docs: specify Kimi Code Assistant profile"
```

---

### Task 15: Add the immutable profile request policy

**Files:**
- Modify: `internal/platform/config/runtime_profiles.go`
- Modify: `internal/platform/config/runtime_profiles_test.go`
- Modify: `internal/platform/config/assistant.go`
- Modify: `internal/platform/config/assistant_test.go`
- Modify: `internal/localproduct/config.go`
- Modify: `internal/localproduct/config_test.go`
- Modify: `openspec/changes/kimi-code-assistant-profile/tasks.md`

**Interfaces:**
- Consumes: OpenSpec Task 14 contract
- Produces: `AssistantModelRequestPolicy`, `KimiCodeAssistantOpenAIV1`, digest-bound request policy, and deployment-scope validation

- [ ] **Step 1: Write failing registry and digest tests**

Add tests that require:

```go
const KimiCodeAssistantOpenAIV1 = "kimi-code-assistant-openai-v1"

profile, ok := config.ProductionProfileRegistry().Assistant(
	config.KimiCodeAssistantOpenAIV1,
)
if !ok ||
	profile.Revision != 1 ||
	profile.LiteLLMModel != "switchback-assistant-kimi-code" ||
	profile.DeploymentScope != config.SingleUserLocalProfileName ||
	profile.RequestPolicy == nil ||
	profile.RequestPolicy.Temperature == nil ||
	*profile.RequestPolicy.Temperature != 1 {
	t.Fatalf("unexpected Kimi Code Assistant profile: %#v", profile)
}
```

Also snapshot the current `deepseek-assistant-openai-v1` digest before the implementation
and assert it remains exact afterward. Add table tests for NaN, positive infinity,
negative temperature, temperature above `2`, non-empty fallback, and stale digest.

- [ ] **Step 2: Run RED configuration tests**

Run:

```bash
go test ./internal/platform/config ./internal/localproduct
```

Expected: FAIL because the new profile, fields, validation, and default do not exist.

- [ ] **Step 3: Implement the typed profile**

Add:

```go
type AssistantModelRequestPolicy struct {
	Temperature *float64 `json:"temperature,omitempty"`
}

type AssistantModelProfile struct {
	ID                         string                        `json:"id"`
	Revision                   uint64                        `json:"revision"`
	LiteLLMModel               string                        `json:"litellm_model"`
	ResponseSchema             string                        `json:"response_schema"`
	DeploymentScope            string                        `json:"deployment_scope,omitempty"`
	RequestPolicy              *AssistantModelRequestPolicy `json:"request_policy,omitempty"`
	AutomaticFallbackProfileID string                        `json:"automatic_fallback_profile_id,omitempty"`
	Digest                     string                        `json:"digest"`
}
```

`AssistantModelProfile.ValidateDigest` must first reject a non-finite or out-of-range
temperature and a non-empty fallback, then validate the digest. Use one helper:

```go
func validateAssistantRequestPolicy(policy *AssistantModelRequestPolicy) error
```

Create a stable `float64` value of `1`, store its address in the fixed Kimi Code profile,
and calculate the digest after every field is set.

- [ ] **Step 4: Change the default without mutating DeepSeek**

Change `LoadAssistantSettings` fallback to `KimiCodeAssistantOpenAIV1`. Remove the old
Kimi Assistant identity from the selectable registry. Leave the DeepSeek optional fields
empty so its JSON and digest remain unchanged.

- [ ] **Step 5: Enforce deployment scope**

In `localproduct.Config.Validate`, resolve `configuration.Assistant.ModelProfile` from the
fixed registry. When Assistant is enabled, require:

```go
if profile.DeploymentScope != "" &&
	profile.DeploymentScope != configuration.Profile.Name {
	return fmt.Errorf("Assistant model profile is not allowed for the deployment profile")
}
```

Tests must prove the Kimi Code profile accepts `single_user_local_v1` and rejects a
different profile name before URL or provider activity.

- [ ] **Step 6: Run GREEN tests and repository checks**

Run:

```bash
gofmt -w internal/platform/config/runtime_profiles.go \
  internal/platform/config/runtime_profiles_test.go \
  internal/platform/config/assistant.go \
  internal/platform/config/assistant_test.go \
  internal/localproduct/config.go \
  internal/localproduct/config_test.go
go test ./internal/platform/config ./internal/localproduct
make fmt-check
git diff --check
```

Expected: all commands pass.

- [ ] **Step 7: Mark the matching OpenSpec task and commit**

```bash
git add internal/platform/config internal/localproduct/config.go \
  internal/localproduct/config_test.go \
  openspec/changes/kimi-code-assistant-profile/tasks.md
git commit -m "feat(config): add Kimi Code Assistant profile"
```

---

### Task 16: Serialize exact profile-owned LiteLLM requests

**Files:**
- Modify: `internal/interaction/adapter/litellm/client.go`
- Modify: `internal/interaction/adapter/litellm/client_test.go`
- Modify: `internal/interaction/application/processor_test.go`
- Modify: `openspec/changes/kimi-code-assistant-profile/tasks.md`

**Interfaces:**
- Consumes: `AssistantModelProfile.RequestPolicy` from Task 15
- Produces: exact optional `temperature` JSON serialization and old-profile fail-closed evidence

- [ ] **Step 1: Write exact-payload failing tests**

Decode the request body into `map[string]json.RawMessage` and assert exact key sets:

```go
if diff := cmp.Diff(
	[]string{"messages", "model", "temperature"},
	sortedJSONKeys(payload),
); diff != "" {
	t.Fatalf("Kimi payload keys mismatch (-want +got):\n%s", diff)
}
if string(payload["temperature"]) != "1" {
	t.Fatalf("temperature = %s, want 1", payload["temperature"])
}
```

For the DeepSeek profile, require only `messages` and `model`; `temperature` must be
absent. Add a client-construction test that rejects invalid request policy even when a
caller manually builds a profile.

- [ ] **Step 2: Write the old-profile Run failure test**

In `processor_test.go`, construct a claimed Run with the retired Kimi profile identity and
the new fixed client profile. Assert:

- the model returns `ErrModelPolicyConflict`
- the processor commits the existing sanitized failed Run
- no readiness, TaskDraft, intent, Task, WorkerRun, or fallback is created

- [ ] **Step 3: Run RED Interaction tests**

Run:

```bash
go test ./internal/interaction/adapter/litellm \
  ./internal/interaction/application
```

Expected: payload and compatibility tests fail against the hard-coded `temperature=0`.

- [ ] **Step 4: Implement optional serialization**

Change the request type:

```go
type completionRequest struct {
	Model       string              `json:"model"`
	Messages    []completionMessage `json:"messages"`
	Temperature *float64            `json:"temperature,omitempty"`
}
```

Copy the immutable configured scalar into the payload rather than exposing the profile
pointer:

```go
func requestTemperature(profile config.AssistantModelProfile) *float64 {
	if profile.RequestPolicy == nil ||
		profile.RequestPolicy.Temperature == nil {
		return nil
	}
	value := *profile.RequestPolicy.Temperature
	return &value
}
```

`NewClient` must rely on the fixed profile validation and continue rejecting fallback.
`Complete` must use `requestTemperature(client.profile)` and must not invent any other
provider parameter.

- [ ] **Step 5: Run GREEN Interaction tests**

Run:

```bash
gofmt -w internal/interaction/adapter/litellm/client.go \
  internal/interaction/adapter/litellm/client_test.go \
  internal/interaction/application/processor_test.go
go test ./internal/interaction/adapter/litellm \
  ./internal/interaction/application
go test -race ./internal/interaction/adapter/litellm \
  ./internal/interaction/application
git diff --check
```

Expected: all commands pass and exact-payload tests prove provider separation.

- [ ] **Step 6: Mark the matching OpenSpec task and commit**

```bash
git add internal/interaction \
  openspec/changes/kimi-code-assistant-profile/tasks.md
git commit -m "feat(assistant): use profile-owned request policy"
```

---

### Task 17: Wire the Kimi Code sidecar and acceptance owner

**Files:**
- Modify: `images/assistant-litellm/config.yaml`
- Modify: `images/assistant-litellm/entrypoint.sh`
- Modify: `deploy/local/compose.yaml`
- Modify: `internal/architecture/assistant_litellm_image_test.go`
- Modify: `internal/architecture/local_compose_test.go`
- Modify: `scripts/run-m2-real-provider-acceptance`
- Modify: `docs/architecture/m2-assistant-task-dispatch.md`
- Modify: `docs/architecture/m2-assistant-task-dispatch-manual-acceptance.md`
- Modify: `openspec/changes/kimi-code-assistant-profile/tasks.md`

**Interfaces:**
- Consumes: new profile and payload contract from Tasks 15–16
- Produces: private `switchback-assistant-kimi-code` LiteLLM route and one canonical Kimi Code acceptance command

- [ ] **Step 1: Write failing architecture and shell-contract tests**

Require these exact sidecar fragments:

```yaml
model_name: switchback-assistant-kimi-code
model: openai/kimi-for-coding
api_base: https://api.kimi.com/coding/v1
api_key: os.environ/SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY
```

Require Compose to default to `kimi-code-assistant-openai-v1` and mount the existing
`SWITCHBACK_KIMI_KEY_FILE` as
`SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY_FILE`. Add negative assertions for:

```text
api.moonshot.cn
KIMI_PLATFORM
kimi-assistant-openai-v1
switchback-assistant-kimi
```

The last two negative assertions must use token boundaries so they do not reject the new
longer names as substrings.

- [ ] **Step 2: Run RED architecture tests**

Run:

```bash
go test ./internal/architecture
sh -n scripts/run-m2-real-provider-acceptance
```

Expected: architecture tests fail on the old alias, env names, and default.

- [ ] **Step 3: Update the private sidecar and Compose**

Rename only Assistant-side environment variables:

```sh
kimi_code_key_file=${SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY_FILE:?Assistant Kimi Code key file is required}
SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY=$(cat "$kimi_code_key_file")
export SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY
```

Keep `SWITCHBACK_KIMI_KEY_FILE` as the existing host file owner and leave the Worker
mounts unchanged. Do not add a host port to `assistant-litellm`.

- [ ] **Step 4: Make the acceptance script select the new profile**

Set:

```sh
assistant_profile=${SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE:-kimi-code-assistant-openai-v1}
```

Allow `kimi-code-assistant-openai-v1` and `deepseek-assistant-openai-v1`; reject the
retired profile. Keep explicit user-level retry behavior and the known-token scan.

Add a preflight assertion that the running Backend exact profile equals the requested
profile and that the sidecar has no published host port.

- [ ] **Step 5: Update operator documentation**

The canonical Kimi command becomes:

```bash
SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=kimi-code-assistant-openai-v1 \
  make m2-real-provider-acceptance
```

Document that:

- the key is a Kimi Code key
- Kimi Platform is unsupported
- the profile is local-only
- old-profile Runs fail closed and explicit Retry creates a new Run
- no fallback or User-Agent spoofing is used

- [ ] **Step 6: Run architecture and static shields**

Run:

```bash
go test ./internal/architecture ./internal/platform/config
sh -n images/assistant-litellm/entrypoint.sh
sh -n scripts/run-m2-real-provider-acceptance
if git grep -n -E 'api\\.moonshot\\.cn|KIMI_PLATFORM' -- \
  ':!docs/superpowers/specs/2026-07-29-kimi-code-assistant-profile-design.md' \
  ':!docs/superpowers/plans/2026-07-29-kimi-code-assistant-profile.md' \
  ':!openspec/changes/kimi-code-assistant-profile/**'; then
  exit 1
fi
git diff --check
```

Expected: all commands pass.

- [ ] **Step 7: Mark the matching OpenSpec task and commit**

```bash
git add images/assistant-litellm deploy/local/compose.yaml \
  internal/architecture scripts/run-m2-real-provider-acceptance \
  docs/architecture/m2-assistant-task-dispatch.md \
  docs/architecture/m2-assistant-task-dispatch-manual-acceptance.md \
  openspec/changes/kimi-code-assistant-profile/tasks.md
git commit -m "feat(deploy): wire Kimi Code Assistant profile"
```

---

### Task 18: Prove the real Kimi Code product flow

**Files:**
- Modify: `docs/status/status-2026-07-28-production-web-core-experience.md`
- Modify: `openspec/changes/kimi-code-assistant-profile/tasks.md`
- Modify: `openspec/changes/production-web-core-experience/tasks.md`
- Test: full repository, Web, Compose, and real-provider gates

**Interfaces:**
- Consumes: fully reviewed implementation from Tasks 14–17
- Produces: exact real-provider evidence, `production-web-core-experience` task 6.3 completion when justified, and a synchronization-ready change set

- [ ] **Step 1: Run focused automated gates**

Run:

```bash
go test ./internal/platform/config ./internal/localproduct \
  ./internal/interaction/adapter/litellm ./internal/interaction/application \
  ./internal/architecture
go test -race ./internal/interaction/adapter/litellm \
  ./internal/interaction/application
npx --yes @fission-ai/openspec@1.6.0 validate \
  kimi-code-assistant-profile --strict
git diff --check
```

Expected: all commands pass.

- [ ] **Step 2: Run full automated gates**

Start a disposable PostgreSQL 16 container on an unused loopback port, export only
`SWITCHBACK_TEST_DATABASE_URL`, and run:

```bash
make verify
npm --prefix web test
npm --prefix web run build
npm --prefix web run test:browser
```

Expected: Go format/migration/generated/image/unit/race/vet/integration/secret/OpenSpec
gates pass, Web has all tests green, production build succeeds, and Playwright passes.
Stop and remove only the explicitly named disposable database container.

- [ ] **Step 3: Run the real Kimi Code acceptance**

Run:

```bash
SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=kimi-code-assistant-openai-v1 \
  make m2-real-provider-acceptance
```

Expected:

```text
m2_real_provider_summary=/absolute/path/to/runtime-data/.../m2-real-provider-summary.json
```

The ignored summary must have `schema_version` equal to
`switchback.m2-real-provider-acceptance.v1` and `outcome` equal to `passed`.

- [ ] **Step 4: Verify exact provider and product evidence**

Without printing credentials or raw model bodies, verify:

- active Backend profile is `kimi-code-assistant-openai-v1`
- LiteLLM model alias is `switchback-assistant-kimi-code`
- ambiguous request produces one clarification
- exact request converges to a current ready draft
- confirmation creates exactly one Task
- Worker delivers Report and Patch with checksum/provenance
- rework, accept, and standalone follow-up keep existing Task/Session semantics
- final Home and browser-facing Worker/Artifact projections update
- no provider envelope, authorization, API-key field, or hidden reasoning is persisted
- `web-demo/` has zero task diff

- [ ] **Step 5: Update status and task truth**

Append a dated resolution section to the existing status document. Preserve the original
HTTP 400 as historical evidence and record:

- before/after profile IDs
- exact commands and exit codes
- image digest
- real Conversation, Task, WorkerSession, Run, Turn, and Artifact refs
- acceptance summary path
- proof that no Kimi Platform surface was used
- remaining non-blocking build advisories

Check `production-web-core-experience` task `6.3` only after every matching gate passes.
Do not check `6.5` and do not sync/archive either change yet.

- [ ] **Step 6: Validate and commit evidence**

Run:

```bash
npx --yes @fission-ai/openspec@1.6.0 validate \
  kimi-code-assistant-profile --strict
npx --yes @fission-ai/openspec@1.6.0 validate \
  production-web-core-experience --strict
npx --yes @fission-ai/openspec@1.6.0 list --json
git diff --check
git diff --quiet b77360b..HEAD -- web-demo
```

Expected: both changes validate; provider implementation tasks are complete except
sync/archive; production Web reports `30/31` with only `6.5` open; committed
`web-demo/` diff is zero.

Commit:

```bash
git add docs/status/status-2026-07-28-production-web-core-experience.md \
  openspec/changes/kimi-code-assistant-profile/tasks.md \
  openspec/changes/production-web-core-experience/tasks.md
git commit -m "test(assistant): prove Kimi Code product flow"
```

---

### Task 19: Synchronize and archive the completed changes

**Files:**
- Modify/Create: `openspec/specs/assistant-conversation-dispatch/spec.md`
- Create: `openspec/specs/production-web-core-experience/spec.md`
- Move: `openspec/changes/kimi-code-assistant-profile/`
- Move: `openspec/changes/production-web-core-experience/`
- Modify: `.superpowers/sdd/progress.md`

**Interfaces:**
- Consumes: Task 18 complete evidence plus explicit user approval of the exact delta impact
- Produces: synchronized main specs, archived changes, and final `31/31` production Web closeout

- [ ] **Step 1: Present synchronization impact and obtain approval**

Show the exact delta:

- `assistant-conversation-dispatch` gains profile-owned typed request policy and the
  local-only Kimi Code requirement
- `production-web-core-experience` becomes a new main capability spec
- no other main spec changes

Do not run synchronization or archive commands before explicit approval.

- [ ] **Step 2: Mark final task truth**

After approval, check the provider sync/archive task and
`production-web-core-experience` task `6.5`. Re-run both strict validations before
moving files.

- [ ] **Step 3: Synchronize delta specs**

Use the OpenSpec sync workflow for both changes, then inspect the resulting main specs.
The synchronized Assistant requirement must preserve all existing scenarios plus the new
profile-owned and local Kimi Code scenarios.

- [ ] **Step 4: Archive in dependency order**

Archive `kimi-code-assistant-profile` first, then
`production-web-core-experience`. Run:

```bash
npx --yes @fission-ai/openspec@1.6.0 validate --all --strict
npx --yes @fission-ai/openspec@1.6.0 list --json
git diff --check
```

Expected: both changes are absent from the active list, their dated archive directories
exist, all main specs validate, and no unrelated active change moved.

- [ ] **Step 5: Run final verification**

Run:

```bash
make verify
npm --prefix web test
npm --prefix web run build
npm --prefix web run test:browser
git diff --quiet b77360b..HEAD -- web-demo
git status --short
```

Expected: all gates pass; `git status --short` contains only the user's original
`web-demo/` modifications and untracked files.

- [ ] **Step 6: Record progress and commit**

Append one durable ledger line:

```text
Task 19: complete (Kimi Code profile and production Web changes synchronized and archived after explicit delta approval; final independent review and parent full gates passed).
```

Commit only tracked OpenSpec, status, and documentation changes:

```bash
git add openspec docs/status
git commit -m "docs: archive Kimi Code and Web closeout changes"
```
