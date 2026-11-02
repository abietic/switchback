# Trusted Full Network Profile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 SingleUserLocalProfile 实现服务器拥有的 `trusted_full_network_v1`，让 Worker 通过 named Docker bridge 直通公网、私网、VPN、LAN 和宿主开发服务，同时保留容器、挂载、凭证和入站端口隔离。

**Architecture:** `internal/platform/config` 中新增一个具体的深模块，负责当前 profile 解析、deployment compatibility 与 legacy evidence 分类；TaskSpec 只保存已解析证据，不能选择 authority。Runtime owner 把同一份 profile evidence 原子写入 WorkerRunStartSnapshot、RuntimeInstance、Docker labels 和 Trace identity，Docker adapter 只负责落实 named bridge 与非网络隔离。

**Tech Stack:** Go 1.26.4、PostgreSQL、Moby Docker Engine client、Docker Compose、JSON Schema、OpenSpec 1.6.0、React/TypeScript。

## Global Constraints

- `trusted_full_network_v1` 只允许 `deployment_profile=single_user_local_v1`，且只能由服务端解析。
- 出站目的地、协议和端口不做 allowlist、CIDR filter、redirect revalidation、payload inspection 或 per-target ApprovalGrant。
- Worker 仍不得使用 Docker Socket、privileged mode、host network mode、任意客户端宿主挂载、ambient infrastructure credentials 或 published host ports。
- `m1_public_web_restricted_v1` 只用于不可变历史读取和已准入非终态 Task 的 compatibility execution；新准入必须拒绝它。
- 历史 TaskSpec JSON 不迁移、不回写；compatibility execution 必须标记 `legacy_unenforced`。
- TaskSpec、WorkerRunStartSnapshot、RuntimeInstance 和 Trace 必须携带相同的 profile id、revision、digest。
- 网络 profile 不记录目的地或 payload telemetry。
- 所有实现按纵向 TDD 循环推进：先证明测试失败，再写最小实现，再证明测试通过。
- 当前 worktree 有既存未提交修改；不得 reset、checkout、stash 或把无关修改纳入本变更提交。

---

## Target File Structure

```text
internal/platform/config/
  runtime_network_profiles.go
  runtime_network_profiles_test.go
internal/contracts/v1/
  taskspec.go
  taskspec_network_test.go
internal/taskorchestration/application/
  direct_admission.go
  direct_admission_test.go
internal/interaction/application/
  draft.go
  draft_test.go
internal/gateway/adapter/postgres/commands.go
internal/localproduct/
  config.go
  config_test.go
  trusted_network_config_test.go
  app.go
  assistant_runner.go
  runtime_environment.go
  runtime_environment_test.go
  runtime_runner.go
  runtime_runner_test.go
internal/workerruntime/application/
  ports.go
  start_run.go
  start_run_test.go
  read_models.go
internal/workerruntime/adapter/postgres/
  start_run.go
  start_run_network_integration_test.go
  read.go
internal/workerruntime/adapter/docker/
  provisioner.go
  provisioner_test.go
internal/artifacttrace/domain/
  turn_evidence.go
  turn_evidence_test.go
migrations/
  000012_trusted_full_network_profile.sql
api/schema/v1/
  task-spec.schema.json
web/src/api/
  contracts.ts
  assistantContracts.ts
  assistantContracts.test.ts
internal/architecture/
  trusted_network_compose_test.go
test/acceptance/networkprofile/
  main.go
scripts/
  run-network-profile-acceptance
docs/adr/
  0066-trust-full-worker-network-in-single-user-local-profile.md
docs/architecture/
  m1-worker-network-operations.md
  m1-worker-network-manual-acceptance.md
```

## Core Interfaces And Test Seams

The server-owned profile module exposes one small interface:

```go
const (
	TrustedFullNetworkV1          = "trusted_full_network_v1"
	LegacyPublicWebRestrictedV1   = "m1_public_web_restricted_v1"
	NetworkEnforcementActive      = "active"
	NetworkEnforcementLegacy      = "legacy_unenforced"
)

type RuntimeNetworkProfile struct {
	ID                string `json:"id"`
	Revision          uint64 `json:"revision"`
	DeploymentProfile string `json:"deployment_profile"`
	OutboundMode      string `json:"outbound_mode"`
	Digest            string `json:"digest"`
}

type RuntimeNetworkEvidence struct {
	ID                string `json:"id"`
	Revision          uint64 `json:"revision"`
	Digest            string `json:"digest"`
	EnforcementStatus string `json:"enforcement_status"`
}

func ProductionRuntimeNetworkProfiles() RuntimeNetworkProfileRegistry
func (RuntimeNetworkProfileRegistry) ResolveForDeployment(string) (RuntimeNetworkProfile, error)
func (RuntimeNetworkProfileRegistry) ResolveStored(RuntimeNetworkEvidence) (RuntimeNetworkEvidence, error)
```

Confirmed test seams:

1. TaskSpec decoding and direct/Assistant TaskSpec production.
2. `RuntimeProvisioner.Provision` and the resulting Docker create/inspect configuration.
3. Persisted RuntimeInstance/start evidence and finalized Trace identity.

No test may depend on private digest helpers, private Docker helper call order, or destination telemetry.

## Task 1: Server-Owned Profile And Startup Compatibility

**Files:**

- Create: `internal/platform/config/runtime_network_profiles.go`
- Create: `internal/platform/config/runtime_network_profiles_test.go`
- Modify: `internal/localproduct/config.go`
- Create: `internal/localproduct/trusted_network_config_test.go`
- Create: `docs/adr/0066-trust-full-worker-network-in-single-user-local-profile.md`

**Interfaces:**

- Consumes: `config.SingleUserLocalProfileName`
- Produces: `RuntimeNetworkProfile`, `RuntimeNetworkEvidence`, `ResolveForDeployment`, `ResolveStored`

- [ ] **Step 1: Write failing profile and startup tests**

```go
func TestTrustedFullNetworkResolvesOnlyForSingleUserLocalProfile(t *testing.T) {
	registry := config.ProductionRuntimeNetworkProfiles()
	profile, err := registry.ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != config.TrustedFullNetworkV1 || profile.Revision != 1 ||
		profile.OutboundMode != "unrestricted" || profile.Digest == "" {
		t.Fatalf("profile = %+v", profile)
	}
	if _, err := registry.ResolveForDeployment("multi_user_remote_v1"); err == nil {
		t.Fatal("trusted full network escaped SingleUserLocalProfile")
	}
}

func TestStoredLegacyNetworkEvidenceIsReadableButUnenforced(t *testing.T) {
	got, err := config.ProductionRuntimeNetworkProfiles().ResolveStored(config.RuntimeNetworkEvidence{
		ID: config.LegacyPublicWebRestrictedV1,
	})
	if err != nil || got.EnforcementStatus != config.NetworkEnforcementLegacy {
		t.Fatalf("legacy evidence = %+v, %v", got, err)
	}
}
```

`internal/localproduct/config_test.go` must additionally assert that `LoadConfig` always derives the current profile from `Profile.Name` and that `Config.Validate` rejects a profile copied from an unsupported deployment.

- [ ] **Step 2: Run tests and confirm red**

Run:

```bash
go test ./internal/platform/config ./internal/localproduct -run 'TestTrustedFullNetwork|TestStoredLegacyNetwork|TestLoadConfig.*Network|TestConfig.*Network' -count=1
```

Expected: compile failure because the network profile module and `Config.NetworkProfile` do not exist.

- [ ] **Step 3: Implement the concrete profile registry**

The current profile is exactly:

```go
profile := RuntimeNetworkProfile{
	ID:                TrustedFullNetworkV1,
	Revision:          1,
	DeploymentProfile: SingleUserLocalProfileName,
	OutboundMode:      "unrestricted",
}
profile.Digest = runtimeNetworkProfileDigest(profile)
```

`ResolveStored` accepts only:

```go
switch evidence.ID {
case TrustedFullNetworkV1:
	// revision and digest must exactly match the current server profile.
case LegacyPublicWebRestrictedV1:
	// revision must be zero, digest must be empty, status becomes legacy_unenforced.
default:
	// reject unknown identity.
}
```

`localproduct.Config` stores `NetworkProfile config.RuntimeNetworkProfile`; `LoadConfig` resolves it from `profile.Name`, and `Validate` revalidates id/revision/digest/deployment compatibility.

- [ ] **Step 4: Run focused tests and confirm green**

Run:

```bash
go test ./internal/platform/config ./internal/localproduct -run 'TestTrustedFullNetwork|TestStoredLegacyNetwork|TestLoadConfig.*Network|TestConfig.*Network' -count=1
```

Expected: PASS.

- [ ] **Step 5: Record the superseding decision**

ADR 0066 must explicitly supersede ADR 0060 for SingleUserLocalProfile, retain ADR 0060 as immutable history, document unrestricted outbound/operator trust, and retain the six non-network isolation controls from Global Constraints.

## Task 2: TaskSpec Contract And Server Producers

**Files:**

- Modify: `internal/contracts/v1/taskspec.go`
- Create: `internal/contracts/v1/taskspec_network_test.go`
- Modify: `internal/taskorchestration/application/direct_admission.go`
- Modify: `internal/taskorchestration/application/direct_admission_test.go`
- Modify: `internal/interaction/application/draft.go`
- Modify: `internal/interaction/application/draft_test.go`
- Modify: `internal/gateway/adapter/postgres/commands.go`
- Modify: `internal/localproduct/app.go`
- Modify: `internal/localproduct/assistant_runner.go`
- Modify: `api/schema/v1/task-spec.schema.json`
- Modify: `web/src/api/contracts.ts`
- Modify: `web/src/api/assistantContracts.ts`
- Modify: `web/src/api/assistantContracts.test.ts`

**Interfaces:**

- Consumes: `config.RuntimeNetworkProfile`
- Produces: additive `RuntimeConfig.NetworkPolicyRevision` and `RuntimeConfig.NetworkPolicyDigest`; frontend fields remain optional in the TypeScript read model only so immutable legacy projections without those fields remain readable

- [ ] **Step 1: Write failing current/legacy contract tests**

```go
func TestTaskSpecAcceptsCurrentNetworkEvidence(t *testing.T) {
	spec := validTaskSpec()
	profile, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	spec.RuntimeConfig = contractsv1.RuntimeConfig{
		NetworkPolicy:         profile.ID,
		NetworkPolicyRevision: profile.Revision,
		NetworkPolicyDigest:   profile.Digest,
	}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskSpecReadsLegacyNetworkEvidenceWithoutInventingEnforcement(t *testing.T) {
	spec := validTaskSpec()
	spec.RuntimeConfig = contractsv1.RuntimeConfig{NetworkPolicy: config.LegacyPublicWebRestrictedV1}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}
```

The same test file rejects unknown ids, current evidence without revision/digest, and legacy evidence carrying invented revision/digest.

- [ ] **Step 2: Run contract tests and confirm red**

Run:

```bash
go test ./internal/contracts/v1 -run 'TestTaskSpec.*Network' -count=1
```

Expected: compile failure because the additive evidence fields are absent.

- [ ] **Step 3: Add the additive contract and strict validation**

```go
type RuntimeConfig struct {
	NetworkPolicy         string `json:"network_policy"`
	NetworkPolicyRevision uint64 `json:"network_policy_revision,omitempty"`
	NetworkPolicyDigest   string `json:"network_policy_digest,omitempty"`
}
```

Current evidence requires revision `1` or greater plus a valid `sha256:` digest. Legacy evidence requires revision `0` and empty digest. `DecodeTaskSpec` remains able to read the unchanged historical `internal/contracts/v1/testdata/task-spec.json`.

- [ ] **Step 4: Make direct and Assistant builders consume the resolved profile**

Replace string policy fields with:

```go
NetworkProfile config.RuntimeNetworkProfile
```

Every server builder emits:

```go
contractsv1.RuntimeConfig{
	NetworkPolicy:         policy.NetworkProfile.ID,
	NetworkPolicyRevision: policy.NetworkProfile.Revision,
	NetworkPolicyDigest:   policy.NetworkProfile.Digest,
}
```

Configuration validation calls `ProductionRuntimeNetworkProfiles().ResolveStored` with `EnforcementStatus: active` and rejects legacy or client-provided profiles for new admission. `localproduct` resolves the profile once and passes the same value to Gateway and Assistant composition.

- [ ] **Step 5: Prove producer authority and legacy immutability**

Run:

```bash
go test ./internal/contracts/v1 ./internal/taskorchestration/application ./internal/interaction/application ./internal/gateway/adapter/postgres -count=1
```

Expected: PASS, including assertions that direct and Assistant outputs contain the same id/revision/digest and the old JSON fixture remains unchanged on disk.

- [ ] **Step 6: Update nested TypeScript parsing**

The frontend contract becomes:

```ts
runtime_config: {
  network_policy: string;
  network_policy_revision?: number;
  network_policy_digest?: string;
};
```

当前 `trusted_full_network_v1` 必须同时提供 revision/digest；仅历史 `m1_public_web_restricted_v1` 可省略两项并按 legacy evidence 读取。

`assistantContracts.ts` must require positive integer revision and a `sha256:` plus 64-lowercase-hex digest for newly projected Assistant drafts. Update fixtures to `trusted_full_network_v1` and the exact server digest.

Run:

```bash
npm --prefix web test -- --run src/api/assistantContracts.test.ts
```

Expected: PASS.

## Task 3: Runtime Persistence, Start Snapshot And Trace Evidence

**Files:**

- Create: `migrations/000012_trusted_full_network_profile.sql`
- Modify: `internal/workerruntime/application/start_run.go`
- Create: `internal/workerruntime/application/start_run_test.go`
- Modify: `internal/workerruntime/adapter/postgres/start_run.go`
- Create: `internal/workerruntime/adapter/postgres/start_run_network_integration_test.go`
- Modify: `internal/workerruntime/application/ports.go`
- Modify: `internal/localproduct/runtime_environment.go`
- Modify: `internal/localproduct/runtime_environment_test.go`
- Modify: `internal/localproduct/runtime_runner.go`
- Modify: `internal/artifacttrace/domain/turn_evidence.go`
- Modify: `internal/artifacttrace/domain/turn_evidence_test.go`

**Interfaces:**

- Consumes: TaskSpec `RuntimeConfig`
- Produces: immutable RuntimeInstance and Trace `RuntimeNetworkEvidence`

- [ ] **Step 1: Write failing start/persistence/Trace tests**

`StartWorkerRunCommand` tests must reject missing revision/digest/status. The PostgreSQL integration test starts one current profile Run and one already-admitted legacy Run, then asserts:

```sql
SELECT network_policy_id, network_policy_revision,
       network_policy_digest, network_enforcement_status
FROM runtime_instances
WHERE runtime_instance_id = $1
```

Expected current tuple: `trusted_full_network_v1`, `1`, exact digest, `active`. Expected legacy tuple: `m1_public_web_restricted_v1`, `0`, empty digest, `legacy_unenforced`.

Trace tests assert `TraceRecord.Identity.RuntimeNetwork` equals the persisted tuple and JSON contains no `destination`, `host`, `url`, `payload_body`, or `network_decision`.

- [ ] **Step 2: Run focused tests and confirm red**

Run:

```bash
go test ./internal/workerruntime/application ./internal/artifacttrace/domain -run 'Test.*Network' -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TestStartWorkerRun.*Network' -count=1
```

Expected: compile or schema failure because evidence fields and migration are absent.

- [ ] **Step 3: Add additive RuntimeInstance columns**

Migration 000012 adds:

```sql
ALTER TABLE runtime_instances
    ADD COLUMN network_policy_revision bigint NOT NULL DEFAULT 0,
    ADD COLUMN network_policy_digest text NOT NULL DEFAULT '',
    ADD COLUMN network_enforcement_status text NOT NULL DEFAULT 'legacy_unenforced',
    ADD CONSTRAINT runtime_instances_network_profile_shape CHECK (
        (network_enforcement_status = 'active'
         AND network_policy_revision > 0
         AND network_policy_digest ~ '^sha256:[0-9a-f]{64}$')
        OR
        (network_enforcement_status = 'legacy_unenforced'
         AND network_policy_revision = 0
         AND network_policy_digest = '')
    );
```

No UPDATE rewrites historical rows.

- [ ] **Step 4: Carry one evidence value through runtime ownership**

Add these fields to `StartWorkerRunCommand`, `RuntimeProvisionRequest`, `pendingTurn`, and `artifactdomain.TurnIdentity`:

```go
NetworkPolicyRevision    uint64
NetworkPolicyDigest      string
NetworkEnforcementStatus string
```

The start snapshot contains:

```json
{
  "runtime_network_profile": {
    "id": "trusted_full_network_v1",
    "revision": 1,
    "digest": "sha256:...",
    "enforcement_status": "active"
  }
}
```

The Runtime owner resolves stored TaskSpec evidence once before `Start`, passes that resolved evidence to PostgreSQL and provisioning, and never accepts a client-supplied enforcement status.

- [ ] **Step 5: Run focused persistence and Trace tests**

Run:

```bash
go test ./internal/workerruntime/application ./internal/localproduct ./internal/artifacttrace/domain -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TestStartWorkerRun.*Network' -count=1
```

Expected: PASS.

## Task 4: Docker Named-Bridge Enforcement And Inspection

**Files:**

- Modify: `internal/workerruntime/adapter/docker/provisioner.go`
- Modify: `internal/workerruntime/adapter/docker/provisioner_test.go`
- Create: `internal/architecture/trusted_network_compose_test.go`
- Modify: `deploy/local/compose.yaml`

**Interfaces:**

- Consumes: `RuntimeProvisionRequest.NetworkProfile`
- Produces: deterministic Docker configuration and labels

- [ ] **Step 1: Extend the public Provision seam test**

The Docker create assertion must require:

```go
if create.HostConfig.NetworkMode != "switchback-m1-worker-control" ||
	!slices.Contains(create.HostConfig.ExtraHosts, "host.docker.internal:host-gateway") {
	t.Fatalf("trusted network config = %+v", create.HostConfig)
}
```

It must also assert the four labels:

```text
com.switchback.network-policy-id
com.switchback.network-policy-revision
com.switchback.network-policy-digest
com.switchback.network-enforcement-status
```

Existing negative assertions for Docker Socket, privileged mode, host network, arbitrary mounts, ambient credential environment and published ports remain unchanged.

- [ ] **Step 2: Run Docker adapter tests and confirm red**

Run:

```bash
go test ./internal/workerruntime/adapter/docker -run 'TestProvisionCreatesOneFixedIsolatedWorkerContainer|TestProvisionRejectsArbitraryMountAndProfileOverrides' -count=1
```

Expected: FAIL because host-gateway and network labels are absent.

- [ ] **Step 3: Implement deterministic Docker configuration**

`createOptions` sets:

```go
ExtraHosts: []string{"host.docker.internal:host-gateway"},
```

`runtimeLabels` includes the resolved immutable network evidence. `verifyExisting` compares Config labels, named NetworkMode, ExtraHosts, mounts, resource limits, port bindings and privilege state. `validateRequest` uses the server registry to accept exact current evidence or exact legacy compatibility evidence only.

- [ ] **Step 4: Verify Compose topology**

`trusted_network_compose_test.go` must parse `deploy/local/compose.yaml` and prove `worker-control` has a stable name and does not set `internal: true`; `switchback-data`, `observability`, `assistant-model` and `langfuse-data` remain internal. The Worker container is still dynamically created and therefore no Worker service publishes ports.

Run:

```bash
go test ./internal/workerruntime/adapter/docker ./internal/architecture -run 'TestProvisionCreatesOneFixedIsolatedWorkerContainer|TestTrustedNetworkCompose' -count=1
```

Expected: PASS.

## Task 5: Projection, Compatibility And Real Reachability Acceptance

**Files:**

- Modify: `internal/workerruntime/adapter/postgres/start_run.go`
- Modify: `internal/workerruntime/application/read_models.go`
- Modify: `internal/workerruntime/adapter/postgres/read.go`
- Create: `test/acceptance/networkprofile/main.go`
- Create: `scripts/run-network-profile-acceptance`

**Interfaces:**

- Consumes: persisted RuntimeInstance network evidence
- Produces: operations-readable evidence and a JSON acceptance report

- [ ] **Step 1: Add failing projection test**

`WorkerRunStarted.v1` payload and the Runtime read model must expose exactly:

```json
{
  "network_profile": {
    "id": "trusted_full_network_v1",
    "revision": 1,
    "digest": "sha256:...",
    "enforcement_status": "active"
  }
}
```

The legacy test expects the stored id plus `legacy_unenforced`; neither payload contains a destination list or an enforcement claim for the old profile.

- [ ] **Step 2: Implement the projection and prove green**

Run:

```bash
go test ./internal/workerruntime/application ./internal/workerruntime/adapter/postgres -run 'Test.*NetworkEvidence|Test.*LegacyNetwork' -count=1
```

Expected: PASS.

- [ ] **Step 3: Implement the real network probe**

`scripts/run-network-profile-acceptance` requires `SWITCHBACK_WORKER_IMAGE` and `SWITCHBACK_WORKER_NETWORK`. It starts host TCP and UDP echo listeners, then runs the immutable Worker image with:

```text
--network ${SWITCHBACK_WORKER_NETWORK}
--add-host host.docker.internal:host-gateway
--entrypoint /usr/local/bin/python
```

The Python probe must:

1. resolve DNS and complete HTTPS to `https://example.com`;
2. exchange a TCP payload with `host.docker.internal`;
3. exchange a UDP payload with `host.docker.internal`;
4. connect to optional `SWITCHBACK_PRIVATE_TCP_TARGET` and `SWITCHBACK_VPN_TCP_TARGET` when supplied;
5. inspect itself through a separate Backend-side Docker query and fail if privileged, host-networked, port-published, Docker-Socket-mounted or carrying forbidden credential environment variables.

The program writes one JSON report with profile id/revision/digest, host platform, Docker server version, positive checks, skipped optional routes and negative isolation checks. It never records application payload contents or credentials.

- [ ] **Step 4: Run real acceptance**

Run:

```bash
./scripts/run-network-profile-acceptance
```

Expected: public HTTPS, host TCP, host UDP and all isolation checks PASS. Private/VPN checks must PASS when their explicit targets are configured; otherwise the report records them as not exercised and OpenSpec task 5.1 remains incomplete.

## Task 6: Documentation, OpenSpec Tracking And Full Verification

**Files:**

- Modify: `REQUIREMTENTS.md`
- Modify: `TECHNICAL_DESIGN.md`
- Modify: `CONTEXT.md`
- Modify: `AGENTS.md`
- Modify: `docs/architecture/context-map.md`
- Modify: `docs/contracts/core-protocols.md`
- Modify: `docs/domains/task-worker-runtime.md`
- Create: `docs/architecture/m1-worker-network-operations.md`
- Create: `docs/architecture/m1-worker-network-manual-acceptance.md`
- Modify: `openspec/changes/trusted-full-network-profile/tasks.md`

**Interfaces:**

- Consumes: verified code and acceptance evidence
- Produces: truthful current documentation and closed task checklist

- [ ] **Step 1: Replace current normative claims without rewriting history**

Current requirements, technical design, context map, core protocols, domain docs and AGENTS must name `trusted_full_network_v1`. ADR 0060, archived OpenSpec artifacts and immutable historical fixture remain unchanged and are linked as superseded history.

- [ ] **Step 2: Document operations and rollback**

Operations documentation must state:

- Docker Desktop/Linux/VPN routing is environment-dependent.
- `host.docker.internal:host-gateway` is the Docker Engine API form of the supported host-service alias.
- unauthenticated privileged TCP services are reachable by design.
- rollback stops new admission but keeps both stored profile readers.
- no destination telemetry, DLP or per-target ApprovalGrant exists in this profile.

- [ ] **Step 3: Mark only evidence-backed OpenSpec tasks**

Change each completed checkbox from `- [ ]` to `- [x]` immediately after its focused command passes. Leave private/VPN acceptance unchecked when no target was exercised.

- [ ] **Step 4: Run full verification**

Run:

```bash
gofmt -w internal test
npm --prefix web test -- --run
make verify
npx --yes @fission-ai/openspec@1.6.0 validate trusted-full-network-profile --strict
git diff --check
```

Expected: all commands exit 0. Record exact counts from command output; do not infer unrun network matrix entries.

## Self-Review

- Spec coverage: server authority, full outbound, preserved isolation, legacy read truth, producer migration, Runtime evidence, Trace identity, operations projection, documentation and real acceptance each map to Tasks 1-6.
- Placeholder scan: this plan contains no deferred implementation markers; optional private/VPN targets are explicitly represented as unexercised acceptance, not assumed success.
- Type consistency: `RuntimeNetworkProfile` is the server definition; `RuntimeNetworkEvidence` is the immutable cross-module value; TaskSpec uses id/revision/digest fields and Runtime adds enforcement status after server resolution.

## Execution Choice

The user already requested continued implementation in the current task. Execute inline with `openspec-apply-change`; do not spawn subagents unless the user explicitly switches to subagent-driven execution.
