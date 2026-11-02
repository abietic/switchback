# Private CI and ACP Merge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有私仓建立确定性远端 CI，然后只合入已经审查的 ACP 切片。

**Architecture:** CI 从 master 独立成 PR，包含五个检查 Job 和 fail-closed Required。集成证据在 CLI seam 检查完整 package/test 生命周期；工作流在声明 seam 与真实 Required 命令校验。CI 合入并确认 master push CI 后，控制器从该 master 回移七个 scoped ACP commits，再独立审查、验证与合入。

**Tech Stack:** Bash、jq、Go 1.26.4、已存在的 gopkg.in/yaml.v3、Node 24.17.0、GitHub Actions、Ubuntu 24.04、PostgreSQL、Playwright Chromium。

## Global Constraints

- 现有私有 abietic/switchback 可见性不变；不公开、不创建公仓、不重写历史、不升级订阅、不使用 --admin 或直接 push master。
- 仅当前 managed worktree /Users/example/.codex/worktrees/codex-acp-deepening/Switchback；保留原 ACP ref、主工作树 dirty 改动与其他工作树/ref；本地 Go 使用 GOMAXPROCS=2、GOFLAGS=-p=1，不使用本地 DB/Docker/供应商凭据。
- CI 触发所有 pull_request 与 master push；不设 paths 过滤、不使用 pull_request_target；顶层 permissions 为 contents: read；actions 固定完整 SHA；同 PR superseded runs 可取消。
- Go 1.26.4、Node 24.17.0、ubuntu-24.04；CI PostgreSQL 固定 postgres@sha256:dbbeb22a65db2503050cdbbe5e78f017478f10a1002a226463f049dbb017e99b（linux/amd64，PG_VERSION=17.10-1.pgdg13+1）。
- 五个 Job IDs 为 static、go、integration、web、browser；Required 的 needs 恰为这五个，if 为 always()，仅 exact 五个结果均为 success 时通过。
- 集成执行 go test -json -count=1 -tags=integration ./...；拒绝 malformed、fail、test-level skip、缺失/不一致 package/test terminal；允许无测试 package 的 start + skip；不使用真实 Codex/YHC 或模型凭据。
- 测试结果不复用：Go Job 设 GOFLAGS=-count=1，integration 已有显式 -count=1；static 的 setup-go cache=false 防止 worker-image-check 恢复旧测试结果（generated protocol 已显式 -count=1）。Node 仅缓存 npm 依赖，不缓存测试输出。
- 不加入 publication-check、test-publication-ready、Factory/profile/YHC 实现或观测 PR #2；不修改生产行为；基础失效仅在实际失败证据后处理 fixture/CI 兼容问题。
- CLI seam 为 scripts/check-integration-json <go-test-jsonl>，0 表示完整无 skip 成功，1 表示无效/不完整/失败证据，2 表示用法错误；工作流声明与 Required 命令是第二个已批准测试 seam。
- 新提交使用 abietic <25390418+abietic@users.noreply.github.com>；独立任务审查、最终全分支审查后才推送；合入时 exact HEAD 五 Job + Required 全绿，API 读回 merge，并确认 master push CI。403 不宣称服务器保护完成。

---

## 文件职责

| 文件 | 职责 |
| --- | --- |
| scripts/check-integration-json | 有界于一次 CI 输出的 JSON 事件生命周期校验 |
| scripts/test-integration-json | 独立字面量日志与退出码契约测试 |
| .github/workflows/ci.yml | 私仓确定性执行、资源和权限声明 |
| internal/architecture/private_ci_test.go | 工作流 seam 与 Required 真命令的契约回归 |

### Task 1: 集成日志完整性检查与 CLI 契约

**Files:** Create `scripts/check-integration-json`, `scripts/test-integration-json`。

**Interfaces:** 输入单个 JSONL 文件；退出码遵循 Global Constraints。后续 workflow 直接执行两个入口，不引入第二个实现。Go 1.26 的 test2json 允许 start/run/pause/cont/output/pass/skip/fail；本 pipeline 不运行 benchmark。build-output 可在 Package 之外出现，build-fail 一律拒绝。

- [ ] **Step 1: 首个 CLI 成功 tracer bullet，RED。** 先仅加入下面测试程序中的 passed fixture 与 assert_exit，运行 `bash scripts/test-integration-json`，因目标脚本尚不存在而实际退出 127，不满足期望 0。不得一次预写全部负例。
- [ ] **Step 2: 最小 GREEN。** 创建入口及参数检查，先通过一个完整成功 package。然后逐个添加下面字面量负例，每个都先观察错误接受，再增补最小生命周期规则；最后收敛为下列实现。

```bash
#!/usr/bin/env bash
set -euo pipefail
if test "$#" -ne 1; then
  printf 'usage: %s <go-test-jsonl>\n' "$0" >&2
  exit 2
fi
if ! jq -e -s '
  def text: type == "string" and length > 0;
  reduce .[] as $e ({packages: {}, tests: {}};
    if ($e | type) != "object" or ($e.Action | text | not) then
      error("invalid event")
    elif $e.Action == "build-fail" or $e.Action == "fail" then
      error("failed integration event")
    elif $e.Action == "build-output" then
      if ($e.ImportPath | text) and ($e.Output | type == "string") then .
      else error("invalid build output") end
    else
      if ($e.Package | text | not) or
         ($e.Test != null and ($e.Test | text | not)) then
        error("invalid package/test identity")
      else . end |
      $e.Package as $p | ([$p, $e.Test] | tojson) as $key |
      if $e.Action == "start" then
        if $e.Test != null or .packages[$p] != null then error("duplicate/invalid package start")
        else .packages[$p] = {state: "running", tests: 0} end
      elif .packages[$p].state != "running" then error("event outside active package")
      elif $e.Test == null then
        if $e.Action == "output" and ($e.Output | type == "string") then .
        elif $e.Action == "pass" or $e.Action == "skip" then
          if any(.tests[]; .package == $p and .state != "pass") or
             ($e.Action == "skip" and .packages[$p].tests != 0) then
            error("incomplete or skipped package")
          else .packages[$p].state = $e.Action end
        else error("invalid package action") end
      elif $e.Action == "skip" then error("skipped integration test")
      elif $e.Action == "run" then
        if .tests[$key] != null then error("duplicate test run")
        else .tests[$key] = {package: $p, state: "running"} | .packages[$p].tests += 1 end
      elif $e.Action == "output" and ($e.Output | type == "string") and
           (.tests[$key].state == "running" or .tests[$key].state == "paused") then .
      elif $e.Action == "pause" and .tests[$key].state == "running" then .tests[$key].state = "paused"
      elif $e.Action == "cont" and .tests[$key].state == "paused" then .tests[$key].state = "running"
      elif $e.Action == "pass" and .tests[$key].state == "running" then .tests[$key].state = "pass"
      else error("invalid or unmatched test action") end
    end
  ) |
  (.packages | length > 0) and
  all(.packages[]; .state == "pass" or .state == "skip") and
  all(.tests[]; .state == "pass")
' "$1" >/dev/null; then
  printf 'invalid or incomplete integration evidence\n' >&2
  exit 1
fi
printf 'integration evidence complete; no test-level skips\n'
```

最终测试脚本内容如下；fixtures 的判定来自设计契约，不复制 jq 判定算法：

```bash
#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture_dir=$(mktemp -d)
trap 'rm -rf "$fixture_dir"' EXIT
validator="$root/scripts/check-integration-json"
assert_exit() {
  local expected=$1 actual=0
  shift
  "$@" >"$fixture_dir/output" 2>&1 || actual=$?
  if test "$actual" -ne "$expected"; then
    printf 'expected exit %s, got %s: %s\n' "$expected" "$actual" "$*" >&2
    sed -n '1,30p' "$fixture_dir/output" >&2
    exit 1
  fi
}
check() {
  local name=$1 expected=$2
  shift 2
  printf '%s\n' "$@" >"$fixture_dir/$name.jsonl"
  assert_exit "$expected" "$validator" "$fixture_dir/$name.jsonl"
}
start='{"Action":"start","Package":"p"}'
run='{"Action":"run","Package":"p","Test":"TestDB"}'
pass='{"Action":"pass","Package":"p","Test":"TestDB"}'
terminal='{"Action":"pass","Package":"p"}'
check passed 0 "$start" "$run" "$pass" "$terminal"
check no_tests 0 "$start" '{"Action":"output","Package":"p","Output":"no test files\n"}' '{"Action":"skip","Package":"p"}'
check interleaved_subtests 0 "$start" "$run" '{"Action":"start","Package":"q"}' \
  '{"Action":"run","Package":"p","Test":"TestDB/parallel"}' \
  '{"Action":"pause","Package":"p","Test":"TestDB/parallel"}' \
  '{"Action":"pass","Package":"q"}' \
  '{"Action":"cont","Package":"p","Test":"TestDB/parallel"}' \
  '{"Action":"output","Package":"p","Test":"TestDB/parallel","Output":"ok\n"}' \
  '{"Action":"pass","Package":"p","Test":"TestDB/parallel"}' "$pass" "$terminal"
check build_output 0 '{"Action":"build-output","ImportPath":"p","Output":"compiler diagnostic\n"}' "$start" "$terminal"
check skipped 1 "$start" "$run" '{"Action":"skip","Package":"p","Test":"TestDB"}' "$terminal"
check failed 1 "$start" "$run" '{"Action":"fail","Package":"p","Test":"TestDB"}' '{"Action":"fail","Package":"p"}'
check build_failed 1 '{"Action":"build-fail","ImportPath":"p"}'
check malformed 1 'not-json'
check non_object 1 '[]'
check boolean_test 1 "$start" '{"Action":"run","Package":"p","Test":true}' "$terminal"
check empty 1
check package_truncated 1 "$start" "$run" "$pass"
check test_truncated 1 "$start" "$run" "$terminal"
check unmatched_terminal 1 "$start" "$pass" "$terminal"
check mismatched_terminal 1 "$start" "$run" '{"Action":"pass","Package":"q","Test":"TestDB"}' "$terminal"
check duplicate_test 1 "$start" "$run" "$run" "$pass" "$terminal"
check duplicate_terminal 1 "$start" "$terminal" "$terminal"
check no_start 1 "$terminal"
check unknown_action 1 "$start" '{"Action":"future","Package":"p"}' "$terminal"
check late_event 1 "$start" "$terminal" "$run"
check skipped_executed_package 1 "$start" "$run" "$pass" '{"Action":"skip","Package":"p"}'
assert_exit 1 "$validator" "$fixture_dir/missing.jsonl"
assert_exit 2 "$validator"
assert_exit 2 "$validator" a b
printf 'integration JSONL contract tests passed\n'
```

- [ ] **Step 3: 完整验证。** `chmod +x scripts/check-integration-json scripts/test-integration-json`；`bash -n scripts/check-integration-json scripts/test-integration-json`；`./scripts/test-integration-json`；真实日志用 `GOMAXPROCS=2 go test -json -p 1 -count=1 ./internal/architecture -run '^TestWorkerImageProvenanceMatchesBuildAndDependencyLocks$' | tee /private/tmp/switchback-acp-deepening.m4LNP0/ci-real-jsonl.log`，然后 `./scripts/check-integration-json /private/tmp/switchback-acp-deepening.m4LNP0/ci-real-jsonl.log`。所有退出 0；报告保存实际 RED/GREEN 与完整验证输出。
- [ ] **Step 4: 提交并自审。** `git add scripts/check-integration-json scripts/test-integration-json`；`git -c user.name=abietic -c user.email=25390418+abietic@users.noreply.github.com commit -m 'ci: reject incomplete integration evidence'`。

### Task 2: 私仓工作流与 Required 声明契约

**Files:** Create `.github/workflows/ci.yml`, `internal/architecture/private_ci_test.go`。

**Interfaces:** 消费 Task 1 的两个可执行入口。yaml.v3 是现有依赖，不修改 go.mod。Required 的 Bash 命令通过真实 jq 执行验证，不另写判定逻辑。仅实现已批准工作流，不修改产品源码或现有测试 fixture。

- [ ] **Step 1: 工作流 tracer bullet，RED。** 先加入以下 TestPrivateCIContract 的读取与事件/permissions 断言，运行 `GOMAXPROCS=2 go test -p 1 ./internal/architecture -run '^TestPrivateCI'`，因缺失 workflow 失败。创建最小正确事件权限声明后 GREEN，再逐个扩展 Job/pin/gate 断言与配置，最后到下列完整工作流与测试。Required 每个新增拒绝输入先确认失败，再补齐相应精确结果门禁。

`.github/workflows/ci.yml`：

```yaml
name: CI
on:
  pull_request:
  push:
    branches: [master]
permissions:
  contents: read
concurrency:
  group: ci-${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true
jobs:
  static:
    name: Static & Contracts
    runs-on: ubuntu-24.04
    timeout-minutes: 15
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: 1.26.4
          cache: false
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0
        with:
          node-version: 24.17.0
          cache: npm
          cache-dependency-path: web/package-lock.json
      - name: Run static and contract checks
        run: make fmt-check migration-check generated-protocol-check worker-image-check vet secret-scan openspec-check
      - name: Characterize integration evidence validator
        run: ./scripts/test-integration-json
  go:
    name: Go Unit & Race
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    env:
      GOFLAGS: -count=1
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: 1.26.4
          cache: true
      - run: make unit
      - run: make race
  integration:
    name: PostgreSQL Integration
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    services:
      postgres:
        image: postgres@sha256:dbbeb22a65db2503050cdbbe5e78f017478f10a1002a226463f049dbb017e99b
        env:
          POSTGRES_USER: postgres
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: postgres
        ports: [5432:5432]
        options: >-
          --health-cmd "pg_isready -U postgres -d postgres"
          --health-interval 3s --health-timeout 3s --health-retries 20
    env:
      SWITCHBACK_TEST_DATABASE_URL: postgresql://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: 1.26.4
          cache: true
      - name: Check PostgreSQL connectivity
        run: |
          if ! command -v psql >/dev/null; then
            sudo apt-get update
            sudo apt-get install -y postgresql-client
          fi
          psql "$SWITCHBACK_TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -c 'select 1'
      - name: Run integration tests without skip fallback
        shell: bash
        run: |
          set -euo pipefail
          integration_log="$RUNNER_TEMP/go-integration.jsonl"
          go test -json -count=1 -tags=integration ./... | tee "$integration_log"
          ./scripts/check-integration-json "$integration_log"
  web:
    name: Production Web
    runs-on: ubuntu-24.04
    timeout-minutes: 20
    defaults:
      run:
        working-directory: web
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0
        with:
          node-version: 24.17.0
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: npm ci
      - run: npm test
      - run: npm run build
  browser:
    name: Browser
    runs-on: ubuntu-24.04
    timeout-minutes: 25
    defaults:
      run:
        working-directory: web
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0
        with:
          node-version: 24.17.0
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: npm ci
      - run: npx playwright install --with-deps chromium
      - run: npm run test:browser
      - name: Upload browser failure evidence
        if: failure()
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: playwright-failure-${{ github.run_id }}
          path: web/test-results
          if-no-files-found: ignore
          retention-days: 7
  required:
    name: Required
    if: always()
    needs: [static, go, integration, web, browser]
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    permissions: {}
    steps:
      - name: Require every quality job
        env:
          JOB_RESULTS: ${{ toJSON(needs) }}
        run: jq -e 'keys == ["browser","go","integration","static","web"] and all(.[]; .result == "success")' <<<"$JOB_RESULTS"
```

`internal/architecture/private_ci_test.go`（使用现有外部测试 package）：

```go
package architecture_test

import (
    "os"
    "os/exec"
    "path/filepath"
    "reflect"
    "regexp"
    "sort"
    "strings"
    "testing"

    "gopkg.in/yaml.v3"
)

type privateCIWorkflow struct {
    On map[string]any `yaml:"on"`
    Permissions map[string]string `yaml:"permissions"`
    Jobs map[string]struct {
        If string `yaml:"if"`
        Needs []string `yaml:"needs"`
        RunsOn string `yaml:"runs-on"`
        Env map[string]string `yaml:"env"`
        Steps []struct {
            Uses string `yaml:"uses"`
            Run string `yaml:"run"`
            Env map[string]string `yaml:"env"`
            With map[string]any `yaml:"with"`
        } `yaml:"steps"`
    } `yaml:"jobs"`
}

func loadPrivateCI(t *testing.T) (privateCIWorkflow, string) {
    t.Helper()
    data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
    if err != nil { t.Fatal(err) }
    var workflow privateCIWorkflow
    if err := yaml.Unmarshal(data, &workflow); err != nil { t.Fatal(err) }
    return workflow, string(data)
}

func TestPrivateCIContract(t *testing.T) {
    workflow, source := loadPrivateCI(t)
    if len(workflow.On) != 2 { t.Fatal("CI must have exactly PR and master push triggers") }
    if _, ok := workflow.On["pull_request"]; !ok { t.Fatal("all PR trigger missing") }
    push, ok := workflow.On["push"].(map[string]any)
    if !ok || !reflect.DeepEqual(push["branches"], []any{"master"}) || len(push) != 1 { t.Fatal("master push trigger mismatch") }
    if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents":"read"}) { t.Fatal("CI token is not read-only") }
    if len(workflow.Jobs) != 6 { t.Fatal("CI job set mismatch") }
    required := workflow.Jobs["required"]
    sort.Strings(required.Needs)
    if required.If != "always()" || !reflect.DeepEqual(required.Needs, []string{"browser","go","integration","static","web"}) { t.Fatal("Required dependency contract mismatch") }
    pin := regexp.MustCompile(`^actions/(checkout|setup-go|setup-node|upload-artifact)@[0-9a-f]{40}$`)
    for name, job := range workflow.Jobs {
        if job.RunsOn != "ubuntu-24.04" || (name != "required" && job.If != "") { t.Fatalf("job %s is conditional or uses wrong runner", name) }
        for _, step := range job.Steps {
            if step.Uses != "" && !pin.MatchString(step.Uses) { t.Fatalf("unapproved or mutable action: %s", step.Uses) }
        }
    }
    if workflow.Jobs["go"].Env["GOFLAGS"] != "-count=1" { t.Fatal("unit/race must execute without test-result reuse") }
    staticCacheDisabled := false
    for _, step := range workflow.Jobs["static"].Steps {
        if strings.HasPrefix(step.Uses, "actions/setup-go@") { staticCacheDisabled = step.With["cache"] == false }
    }
    if !staticCacheDisabled { t.Fatal("static test cache must not be restored") }
    for _, text := range []string{
        "cancel-in-progress: true", "go-version: 1.26.4", "node-version: 24.17.0",
        "make fmt-check migration-check generated-protocol-check worker-image-check vet secret-scan openspec-check",
        "./scripts/test-integration-json", "make unit", "make race", "--health-cmd", "select 1",
        "set -euo pipefail", "go test -json -count=1 -tags=integration ./...", "./scripts/check-integration-json",
        "SWITCHBACK_TEST_DATABASE_URL:", "working-directory: web", "npm ci", "npm test", "npm run build",
        "npx playwright install --with-deps chromium", "npm run test:browser", "retention-days: 7",
        "postgres@sha256:dbbeb22a65db2503050cdbbe5e78f017478f10a1002a226463f049dbb017e99b",
    } { if !strings.Contains(source, text) { t.Errorf("CI lacks %q", text) } }
    for _, text := range []string{"pull_request_target", "paths:", "paths-ignore:", "continue-on-error", "publication-check", "test-publication-ready", "secrets."} {
        if strings.Contains(source, text) { t.Errorf("CI contains forbidden %q", text) }
    }
}

func TestPrivateCIRequiredRejectsIncompleteOrUnsuccessfulResults(t *testing.T) {
    workflow, _ := loadPrivateCI(t)
    steps := workflow.Jobs["required"].Steps
    if len(steps) != 1 || steps[0].Env["JOB_RESULTS"] != "${{ toJSON(needs) }}" { t.Fatal("Required must consume real needs") }
    const successful = `{"static":{"result":"success"},"go":{"result":"success"},"integration":{"result":"success"},"web":{"result":"success"},"browser":{"result":"success"}}`
    cases := map[string]string{
        "complete":successful,
        "empty":`{}`,
        "missing":`{"static":{"result":"success"}}`,
        "malformed":`not-json`,
        "unexpected":strings.Replace(successful, `"browser"`, `"other"`, 1),
    }
    for _, job := range []string{"static","go","integration","web","browser"} {
        for _, status := range []string{"failure","cancelled","skipped",""} {
            cases[job+"/"+status] = strings.Replace(successful, `"`+job+`":{"result":"success"}`, `"`+job+`":{"result":"`+status+`"}`, 1)
        }
    }
    for name, input := range cases { t.Run(name, func(t *testing.T) {
        command := exec.Command("bash", "-euo", "pipefail", "-c", steps[0].Run)
        command.Env = append(os.Environ(), "JOB_RESULTS="+input)
        output, err := command.CombinedOutput()
        if (err == nil) != (name == "complete") { t.Fatalf("unexpected Required result: %v; %s", err, output) }
    }) }
}
```

- [ ] **Step 2: GREEN 与契约验证。** `gofmt -w internal/architecture/private_ci_test.go`；`GOMAXPROCS=2 go test -p 1 -count=1 ./internal/architecture -run '^TestPrivateCI'`；`./scripts/test-integration-json`；`git diff --check`。报告实际 RED/GREEN；YAML 由 yaml.v3 拒绝重复 key；远端真实 Actions 执行仍是下一道门，不宣称本地契约等于远端绿测。
- [ ] **Step 3: 提交并自审。** `git add .github/workflows/ci.yml internal/architecture/private_ci_test.go`；`git -c user.name=abietic -c user.email=25390418+abietic@users.noreply.github.com commit -m 'ci: add deterministic private repository quality gates'`。

### Task 3: 已复现的 Assistant 审计上下文 fixture 时间兼容

**Files:** Modify `internal/interaction/adapter/litellm/client_test.go`，仅 `TestAssistantRunProcessorCarriesPolicyRevisionInAuditedModelContext`。

**Interfaces:** 既有 ProcessAssistantRun + httptest 审计 seam，保留全部字段断言。设计授权仅在实际基线失败后修复 fixture；本次 `make unit` 和单测均失败于 model-visible policy_revision missing，其他 package 通过。固定 2026-07-30 的 claim.TimeoutAt 经 processor.go:57 的真实 context.WithDeadline 传到 HTTP client，是最高优先级假设。原 fake Clock 与真实 HTTP clock 不一致，不能通过改生产代码绕开 deadline。

- [ ] **Step 1: 强化诊断断言，RED。** 在 ProcessAssistantRun 返回后、现有 model.calls/context packs 断言前加入：

```go
if model.err != nil {
    t.Fatalf("model request failed before audited context inspection: %v", model.err)
}
```

运行 `GOMAXPROCS=2 go test -p 1 -count=1 ./internal/interaction/adapter/litellm -run '^TestAssistantRunProcessorCarriesPolicyRevisionInAuditedModelContext$'`。期望实际失败错误为 ErrModelTimeout；若不是，停止并报告，不执行预设修复。原始 RED 已为 client_test.go:360 unexpected end of JSON input，exit 1。

- [ ] **Step 2: 单变量 GREEN。** 仅该测试 `now` 改成：

```go
now := time.Now().UTC()
```

fake Clock、claim 与真实 HTTP deadline 使用同一次 current now；保留新错误断言。运行同 focused 命令应通过，并记录原始输出。

- [ ] **Step 3: 覆盖验证。** `gofmt -w internal/interaction/adapter/litellm/client_test.go`；`GOMAXPROCS=2 go test -p 1 -count=1 ./internal/interaction/adapter/litellm`；`GOMAXPROCS=2 go test -race -p 1 -count=1 ./internal/interaction/adapter/litellm`；`git diff --check`。不得改其他固定时间的负例或复制主工作树 dirty 文件。
- [ ] **Step 4: 提交、自审与报告。** 只 stage 该文件，用 noreply 提交 `test: keep audited Assistant context deadline current`；独立 task review。记录根因、RED/GREEN/普通/race/无 debug instrumentation。控制器随后重新跑 make unit。

### Task 4: 已批准的 CI PostgreSQL 平台 pin 修正

**授权与证据：** 2026-10-05 用户批准仅 CI 的同版本、runner 兼容 digest 修正，取代本计划此前写死的 ARM64 pin。PR3/run37279664554 的镜像 pull 成功，但日志明确显示 linux/arm64/v8 与 AMD64 runner 不匹配、entrypoint exec format error；PG tests 尚未运行。旧 manifest 的实际配置为 PG_VERSION=17.10-1.pgdg13+1、linux/arm64。官方 [17.10-trixie tag 元数据](https://hub.docker.com/v2/repositories/library/postgres/tags/17.10-trixie) 的 linux/amd64 manifest 为 dbbeb22a65db2503050cdbbe5e78f017478f10a1002a226463f049dbb017e99b，其 manifest 与 config 内容哈希已核验，实际 PG_VERSION 同为17.10-1.pgdg13+1。候选与旧镜像构建日期不同，不能宣称仅 CPU 字段不同或整个 OS/image 内容相同。

**Files:** Modify `.github/workflows/ci.yml`, `internal/architecture/private_ci_test.go`, `docs/superpowers/specs/2026-10-05-private-ci-and-acp-merge-design.md`。本计划已由控制器先更新 pin 与修正步骤；不修改 `deploy/local/compose.yaml`、其他工作树或未提交改动。

**Interfaces:** 继承已批准 workflow declaration/Required 命令 seam；在实际 integration 服务字段校验固定镜像，不通过整份 YAML 的残留字符串判定该 pin。新行为仅为 AMD64 runner 可启动同版本 PG；五组检查与 Required、版本、权限、测试命令、无 skip/no-cache 策略均不降级。远端真实 CI 才能证明容器运行/PG tests 成功。

- [ ] **Step 1: typed pin 契约，RED。** 在 `privateCIWorkflow.Jobs` 的 struct 中加入：

```go
Services map[string]struct {
    Image string `yaml:"image"`
} `yaml:"services"`
```

在 `TestPrivateCIContract` 中、现有 GOFLAGS 检查后加入：

```go
if workflow.Jobs["integration"].Services["postgres"].Image != "postgres@sha256:dbbeb22a65db2503050cdbbe5e78f017478f10a1002a226463f049dbb017e99b" {
    t.Fatal("PostgreSQL CI must pin the verified linux/amd64 PG17.10 image")
}
```

从该测试 whole-source text slice 删除旧 PostgreSQL image 条目，其余断言保留。`gofmt -w internal/architecture/private_ci_test.go`；运行 `GOMAXPROCS=2 go test -p 1 -count=1 ./internal/architecture -run '^TestPrivateCIContract$'`，保留旧 workflow 时必须实际 RED，错误为上述 typed pin 不匹配。不要先改 workflow。

- [ ] **Step 2: 单变量 GREEN。** 只将实际 integration service image 一行替换为：

```yaml
image: postgres@sha256:dbbeb22a65db2503050cdbbe5e78f017478f10a1002a226463f049dbb017e99b # PG17.10-1.pgdg13+1, linux/amd64
```

运行同 focused 命令，必须 GREEN。不得改 runner、healthcheck、service credentials/port、timeout、权限、needs、测试/Required 命令或其他 job。

- [ ] **Step 3: 同步短设计。** 在短设计 CI 架构段之后加入本任务授权日期、run37279664554 的已确认平台失败、同 PG_VERSION 的新 AMD64 digest、官方 tag 元数据链接和“构建日期不同，仅数据库版本相同；local Compose 保留 ARM64 pin”说明。保持“远端全绿后才合入”和服务器保护403限制，不将元数据核验宣称为 CI 已通过。
- [ ] **Step 4: 覆盖验证。** `GOMAXPROCS=2 go test -p 1 -count=1 ./internal/architecture -run '^TestPrivateCI'`；`GOMAXPROCS=2 go test -p 1 -count=1 -short ./internal/architecture`；`./scripts/test-integration-json`；`git diff --check`。测试不访问本地 DB/Docker/真实 Agent；只在当前工作树低并发执行，不与控制器测试同时跑。
- [ ] **Step 5: 提交与报告。** 只 stage 上述三个文件，用 noreply 身份提交 `ci: pin runner-compatible PostgreSQL 17.10 image`。报告实际 RED/GREEN、覆盖命令/输出、metadata 证据、source scope、构建差异限制和未运行的远端门禁。独立任务审查、更新后的最终全分支审查和控制器验证之后才 push；重新跑新 exact HEAD 全部五组+Required，旧 head 四组成功不可代替。

### Task 5: 实际 CI 暴露的异步 Trace 与 PostgreSQL 时间夹具兼容

**授权与证据：** 继承已批准设计的“实际失败后仅修复 fixture/CI 兼容问题”，不扩展生产行为。PR3/run37301420819 在 f3fb1bb 的 PG 初始化和连通性成功，但 Race 与两个集成测试失败。完整 Race 堆栈是 stdout pump 写入 Trace、生命周期测试在关闭前读 `bytes.Buffer.Bytes()`；生产 recorder 已串行化写入。两个 PG 失败分别是纳秒 durableAnchor 与数据库微秒精度不一致，以及 pgx 扫描时间和 JSON 重放时间的 location 表示导致 `reflect.DeepEqual` 不相等。无数据库的当前锁定 pgx Encode/Scan + JSON 诊断分别 actual RED；微秒 fixture + UTC-canonicalized 完整比较 GREEN。完整证据在 `/private/tmp/switchback-acp-deepening.m4LNP0/ci-f3-diagnosis.md`。本机生命周期 Race 重复30/1000及包100次未复现，不能冒称本机实际 adapter RED；Linux CI 堆栈是实际 RED。

**Files:** Modify only `internal/workerruntime/adapter/codexacp/adapter_test.go`, `internal/localproduct/runtime_startup_integration_test.go`, `internal/workerruntime/adapter/postgres/runtime_first_heartbeat_integration_test.go`。不改产品源码、工作流/门禁、schema/migrations、其他固定时间负例或主工作树。控制器另记诊断和计划，不要求实现者再写设计文档。

**Interfaces:** 延续既有已批准 fixture 修复范围；测试继续经 AgentAdapter 生命周期/Trace io.Writer、runtimeRunner startup 与 RuntimeFirstHeartbeatService/Store 的原调用边界观察。只改变测试拥有的同步和时间表示，不新增产品 API/private 测试 seam。保留全部 redaction、unknown-update、session method、identity/revision、boundary、recovery 幂等性与 audit 断言。严禁时间容差、删 EvidenceAt、忽略非时间字段、skip 或重跑至偶然绿色。

- [ ] **Step 1: 同步 Trace 夹具。** 在 adapter_test.go 增加唯一测试 helper（已有 sync、bytes import）：

```go
type testTraceBuffer struct {
    mu sync.Mutex
    buffer bytes.Buffer
}

func (b *testTraceBuffer) Write(data []byte) (int, error) {
    b.mu.Lock()
    defer b.mu.Unlock()
    return b.buffer.Write(data)
}

func (b *testTraceBuffer) Bytes() []byte {
    b.mu.Lock()
    defer b.mu.Unlock()
    return append([]byte(nil), b.buffer.Bytes()...)
}
```

仅把使用 newTestAdapter/newTestAdapterWithResume 的六个异步生命周期 fixture（包括 protocol/identity table）改为 `var trace testTraceBuffer`。同步 raw-tee 的 bytes.Buffer 保留。两 helper 的 trace 参数从 `*bytes.Buffer` 改为 `io.Writer`，透传原 trace；所有现有 Bytes 断言保留。Write 与 Bytes 共享锁且返回副本，不能只锁后返回 alias；不提前 Close/删 Trace 断言，也不修改 adapter 生产锁或 shutdown。

- [ ] **Step 2: PG 微秒夹具。** 仅在 startup 集成测试原 durableAnchor 赋值上改为 `time.Now().UTC().Truncate(time.Microsecond).Add(-30 * time.Second)`。原 Equal 与其他 startup/first-heartbeat 断言保持，不给容差、不修改生产 clock/codec。
- [ ] **Step 3: 完整 recovery 语义比较。** 在 BoundariesOpenOneStableRecovery 的 replay Evaluate 后，先明确断言 replayed.OpenRecovery 非 nil；复制 firstClassification 与 replayed 两个 recovery 值，只将两份副本的 EvidenceAt 分别 `.UTC()` 后继续 `reflect.DeepEqual` 完整结构，同时保留 replayed.Applied 必须 false。不得修改原命令、跳过时间比较或修改任何非时间字段：UTC 保留 exact instant，只去掉不同 location/monotonic 表示。失败信息展示两个 recovery 值而非仅指针，方便辨认非时间差异。其他断言原样保留。
- [ ] **Step 4: 验证并诚实区分证据。** `gofmt -w` 三文件；`GOMAXPROCS=2 go test -p=1 -count=1 ./internal/workerruntime/adapter/codexacp`；`GOMAXPROCS=2 go test -race -p=1 -count=100 ./internal/workerruntime/adapter/codexacp`；`GOMAXPROCS=2 go test -p=1 -count=1 -short ./internal/localproduct ./internal/workerruntime/adapter/postgres`；`GOMAXPROCS=2 go test -p=1 -count=1 -tags=integration ./internal/localproduct ./internal/workerruntime/adapter/postgres -run '^$'`（仅编译，不冒称 DB测试执行）；`git diff --check`。所有覆盖命令须实际执行、记录输出与退出码；不启动本地数据库/Docker。真实集成 GREEN 与 Linux Race GREEN 等待新 exact-head 远端 CI；不能补造本机 adapter RED/DB GREEN。
- [ ] **Step 5: 提交、自审与报告。** 只 stage 三个测试文件，用 noreply 身份提交 `test: synchronize live ACP trace and canonicalize database time assertions`。报告 CI实际RED、hermetic编解码RED/GREEN和本机覆盖范围/限制；实际 code diff、独立task审查与全分支重审后控制器验证/推送。任何非时间字段差异或生产缺陷停止并上报，不扩展此任务。

## 控制器发布与后续步骤（不是实现任务）

- [ ] 生成 Task 1/2/3 独立 review packages，审查 clean 后记录；最强模型最终 whole-branch review 从 origin/master 到候选 HEAD，评审未消化 Minor、脚本实际语义与 workflow 权限/触发/组合；修复后重新验证。
- [ ] 控制器独立执行 `GOMAXPROCS=2 GOFLAGS=-p=1 make fmt-check migration-check generated-protocol-check worker-image-check vet secret-scan openspec-check`、两个新契约入口与 `make unit`；不借用本地 PG。Race/Browser/Web/PG 全量以精确 HEAD 的远端运行补足。发现基础失败先诊断，不把失败 gate 删除。
- [ ] `git push -u origin codex/private-ci-quality-gate`；`gh pr create --repo abietic/switchback --base master --head codex/private-ci-quality-gate --title 'ci: add deterministic private repository quality gates' --body-file <reviewed-body>`；创建后立即 attach_artifact。PR 描述清楚 CI 证据、私仓范围和保护 403。
- [ ] 读取 `gh api repos/abietic/switchback/pulls/<number>`、`gh api repos/abietic/switchback/actions/runs?head_sha=<HEAD>`、`gh api repos/abietic/switchback/commits/<HEAD>/check-runs`；必须核实 CI workflow 最新 completed/success、PR exact HEAD、所有五 Job 和 Required 成功、base 未前移、mergeable=true。PR run 的 tested merge SHA 与 head_sha 分别记录。
- [ ] `gh pr merge <number> --repo abietic/switchback --squash --match-head-commit <HEAD>`；读回 merged、merge_commit_sha 与 master ref；等待该 master SHA 的 push CI 同样全绿。任一外部阻塞保留 PR，不绕过。
- [ ] fetch master 后在同一 managed worktree 创建 `codex/acp-codex-only-mainline`；只按顺序 cherry-pick `60a9f36125880d5630ee4fb89f227223d403b8be 9c0a49357d1933897e332623016b7bbd56642747 16de97067b729b04e92a1808d0e41c65dd8b6944 4f4ae5b8bcc30242e63cac52d25112376e9be9ce 2aca90b563e5940e76c7a40f24966cde84c91b57 0d09bf22b000ff3b38b01bb720dc52440f676aad 43510da800cb5baeccd5c14fe3455a7018bc70e0`。冲突读取 resolving-merge-conflicts skill，不直接 checkout/clean/stash；保留原 ref。
- [ ] 核对 ACP file scope/source equality；新 HEAD 跑 ACP ordinary/race、workerruntime + architecture short、vet、shim/backend build、fmt/diff；主线组合审查、独立 ACP PR、exact HEAD CI、受控 squash merge、master push CI 读回均如上一协议。不能沿用原开发基线的绿测替代新 HEAD。
- [ ] 向观测 owner 同步实际 merge tuple；继续只读组合 diff 评审与协调回归，保留其 capture_cancelled 未解释结论，不实现 Factory/YHC 或自动合入 PR #2。

## 自审记录

设计每项范围、版本、权限、五 Job、完整终态、Required、私仓限制分别落到两个任务或控制器协议；无新依赖。脚本/测试 signatures 与 workflow 入口一致，fixture 使用独立日志。既有弱 validator 与空集合 Required 的误判已在本方案显式补齐。测试 seam 继承已批准设计；同会话 SDD 连续执行已获授权，不重复询问执行方式。
