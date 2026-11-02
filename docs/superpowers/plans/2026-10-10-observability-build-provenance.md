# Observability build provenance implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在验收工具中记录并只读核对 exact source → Backend / Worker base / controlled Worker 三项实际构建来源，不修改产品逻辑或 Dockerfile。

**Architecture:** 一个独立 test-only Go CLI 生成 claim-owned 快照与实际构建 receipts；同一 CLI 的 check 模式只读重新验证原始材料和 Git 对象。Shell 入口只负责固定工作目录与低并发参数。它只交付来源证据，不承担双门禁/物理验收或 Ready 决策。

**Tech Stack:** Go 1.26.4 标准库、Git、Docker CLI；不新增依赖。

## Global Constraints

- 工作目录仅为 `.worktrees/observability-master-integration-1009`；指定 master 基线 `8fdbe6b556a0602b8392a2eed0e09b9e007542d1` 不变。
- 遵循已书面确认的[第三阶段补充规格](../specs/2026-10-10-observability-two-gates-design.md#-第三阶段补充构建来源记录)。不改产品逻辑、任何 Dockerfile、依赖锁或旧证据。
- 保留 Task 1/2；不修改原 strict checker、CI/测试断言/标签、250ms/30s 预算；不关闭 capture_cancelled 或勾选 OpenSpec 3.2。
- root 独占真实资源。agent 仅使用临时 Git 仓库和 fake Docker 命令边界；所有 Go 命令 GOMAXPROCS=2、GOFLAGS=-p=1、-p=1、GOWORK=off。不访问真实 Docker/PG/Agent/provider。
- 公共 seam 已获补充规格确认：build/check 工具入口及其可读原始材料；Git/文件系统真实，只有外部 Docker 命令可替代。不得 mock 自有源码验证逻辑。
- build 只接受当前干净 HEAD 的完整 commit，核验它包含指定 master 基线；Git 每次执行都清除 ambient GIT_*，禁用 replacement 与全局/system config。不能被 GIT_DIR/WORK_TREE/INDEX_FILE/config-count 重定向。
- 三次固定构建；三个目标 claim tag 已存在时拒绝覆盖；controlled WORKER_BASE 只能是本次新基础镜像。使用原有缓存，--pull=false，基础镜像缺失时先失败，不自动拉取。
- 证据 exclusive create；claim/build 重复、symlink、路径逃逸、非普通文件、超限或身份/摘要不匹配一律失败。失败目录、日志和已生成的镜像保留；无自动删除或清理。
- check 不写文件、不执行 Docker、HTTP、SQL、构建或 verifier writer；返回来源校验结果不代表 actual fixture 已运行或物理门禁通过。
- 不 push、Ready、合入、全分支合入审查或真实 provider；源记录是受信任本地主机执行记录，不是防宿主攻击者的签名。

---

## 📦 文件与接口

| 文件 | 职责 |
| --- | --- |
| `test/acceptance/observabilitybuild/main.go` | CLI、参数/claim 校验、受控 Git/Docker 外部命令边界、选定字段 receipts |
| `source.go` / `source_test.go` | Git 对象清单、独占完整 snapshot、内容/mode 核对 |
| `build.go` / `build_test.go` | 固定三次构建、实际 argv/log/IID/inspect receipts |
| `verify.go` / `verify_test.go` | 只读重算源码和 receipts，生成 JSON 结果 |
| `main_test.go` | 真实 Git fixture、外部 Docker fake、工具入口及 negative behavior |
| `scripts/build-observability-images` | 固定 build wrapper |
| `scripts/check-observability-build` | 固定 check wrapper |

模块 `github.com/abietic/switchback`；独立 package main，不修改既有 `test/acceptance/observability` 包，从而不干扰原函数体守恒。

```text
scripts/build-observability-images COMMIT PROJECT CLAIM_ROOT
scripts/check-observability-build COMMIT PROJECT CLAIM_ROOT
go run ./test/acceptance/observabilitybuild build|check COMMIT PROJECT CLAIM_ROOT
```

参数错 exit2；claim/source/preflight/build/evidence 任一失败 exit1；成功 exit0，stdout 仅 JSON。check 的 JSON 使用 `schema_version=observability.build_provenance_verified.v1`，带 source_commit、source_tree、source_manifest_sha256、project、output 和 backend/worker_base/controlled_worker image ID。不输出 gates_passed 或 Ready。

### Task 1: 独立来源构建与只读验证工具

**Files:** 新增上述八个 Go 文件与两个脚本；不要改本计划外的代码。报告写 `.superpowers/sdd/gates1010-provenance-task1-report.md`，详细 RED/GREEN 日志存 agent 独占临时目录并报告 exact paths，不覆盖任何既有文件。

**Interfaces:** main 通过下述入口调用；测试通过同一工具 seam 观察 exit/stdout/artifacts，不测 private helpers。DockerCommand 是唯一可替代的外部边界。可用真实 Git + 临时文件构造最小候选，baseline 在运行时固定，不暴露 caller override。

```go
type DockerCommand func(context.Context, []string, io.Writer, io.Writer) error

// Production main obtains the canonical repository from its cwd using sanitized
// Git, and invokes Run with exec.CommandContext for Docker only.
func Run(ctx context.Context, repository string, args []string,
    stdout, stderr io.Writer, docker DockerCommand) int

const integrationBase = "8fdbe6b556a0602b8392a2eed0e09b9e007542d1"
```

- [x] Step 1: 在 main_test.go 先建立真实 Git fixture 与第一个工具反例，不先批量写完测试。Git fixture 仅修改 t.TempDir：以本仓为 readonly object source 执行 `git clone --shared --no-checkout`，`git read-tree --empty`，写最小 Dockerfile/lock/source/ignore 文件再 commit，parent 保留真实 baseline ancestry；只检查 out-of-tree 临时候选。清除所有 GIT_*、全局/system config；各 fixture 使用新 project/root，claim.json 含匹配 project/output 与合成 token，不读取真实 acceptance.env。

第一个行为测试的完整断言如下；fixture 方法是 main_test.go 中的测试驱动，`invoke` 只调用 Run，`writeTrackedFileAndCommit` 用实际 Git/file API，`docker` 是后述外部命令 fake：

```go
func TestBuildRejectsNonHeadCommitBeforeDocker(t *testing.T) {
    f := newFixture(t)
    previous := f.commit
    f.writeTrackedFileAndCommit("source.txt", "changed\n")
    code, _, message := f.invoke("build", previous, f.project, f.output)
    if code != 1 || !strings.Contains(message, "HEAD") {
        t.Fatalf("stale candidate: exit=%d stderr=%q", code, message)
    }
    if f.dockerCalled {
        t.Fatal("stale candidate reached Docker boundary")
    }
}
```

运行 `GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go test -p=1 -count=1 -run '^TestBuildRejectsNonHeadCommitBeforeDocker$' ./test/acceptance/observabilitybuild`，保存预期 RED（缺入口/未拒绝陈旧 commit），再最小实现入口校验获得 GREEN。

- [x] Step 2: 逐个 RED/GREEN 增加合法源码快照、dirty 候选、错误 baseline、poisoned GIT_DIR/WORK_TREE/INDEX_FILE/config-count、claim mismatch、重复输出、symlink/submodule/path escape。Run build 的源码前置步骤固定如下，不调用 Docker：验证 canonical repository/root、完整 40 hex commit 与 HEAD/ancestry、`git status --porcelain=v1 --untracked-files=all` 为空；只读 regular claim.json，project 符合 `^switchback-obsv-[a-z0-9]{8,24}$`，output exact canonical root，token 为 64 hex；独占 `build/` 0700；只从 Git commit 枚举 mode 100644/100755 的 blobs，不从工作目录读源码。

源码实现选择 `git ls-tree -r -z` + `git archive --format=tar`，不是每个文件启动一次 git。用 tar 标准库流式读取，原路径必须与 ls-tree 一一匹配，mode/size 与原 Git blob 相符；自行重算 Git blob ID 确认 archive/export-subst 未改字节。目录仅为文件父目录，拒绝 extras、duplicates、links、submodules、非 regular；特殊 Git archive global PAX comment 不是源码文件。相对路径只能 clean、非 absolute、不含 ..、反斜线、NUL 或 .git component。最多 12000 文件、单文件 16MiB、累计 256MiB，拒绝截断。source 文件只读（保留 executable bit），目录私有且可由本次 owner 管理；构建前后遍历必须与 manifest 完全一致。私有性由外层 claim/build/source root 的 0700 保证；文件只移除 Git mode 的写位，保持 0444/0555，内层目录保留 0755 可遍历性，避免 Docker COPY 后 LiteLLM UID10001 无法读取配置。Git 命令输出必须在读取阶段限长，archive 使用 stdout pipe 流式处理并回收子进程，不能先 Output 全部 bytes 后才限长。

`build/source.json` schema `observability.build_source.v1`，包含 project/output/source_commit/source_tree/source_manifest_sha256、entries（排序 path/mode/bytes/sha256）；manifest 摘要为 entries 的规范 JSON 的 SHA256。`build/source/` 是唯一实际 Docker context。验证重新从 Git 对象计算 entries、tree/digest，并和 snapshot 实际 bytes/mode/完整文件集合比较，不能只比较 source.json 内字段。

- [x] Step 3: 先用外部 fake 支持实际固定命令 shape，再逐个 RED/GREEN 覆盖缺 image、非本地 builder、既有目标 tag、构建失败、错误 IID/base/inspect、快照构建中变化。builder 只允许单 node Docker driver、endpoint 为已记录本地 Unix-socket context；不 bootstrap/create builder，不运行容器。保留 `docker context show/inspect`、selected-field info 和 `buildx inspect` 原始 receipts，build 显式 `--builder CONTEXT --load`，避免 ambient remote builder 或仅导出缓存。context/info/builder 不完整或不一致失败。

原基础 images（四个唯一引用）从快照 Dockerfiles 的默认 ARG 读取并要求 `@sha256:` 固定：GO_IMAGE、DEBIAN_IMAGE、NODE_IMAGE、PYTHON_IMAGE。三份 Dockerfile 的相关 ARG 不得矛盾。构建前只读 image inspect，全部要求缓存存在、linux 与本地 amd64/arm64 架构。构建目标按顺序固定：`${project}-backend:acceptance`、`${project}-worker-base:acceptance`、`${project}-worker:controlled`；任何目标已存在先失败。selected inspect 仅保存 ID/OS/architecture，不写 Config.Env 等含潜在凭据的 full inspect。

构建 argv 用以下完整函数定义生成，role/index 对应上面的固定顺序，DockerCommand 接收的 args 不含可执行名 docker：

```go
func buildArguments(contextName, platform, output, project, role string) []string {
    dockerfile, tag := map[string]string{
        "backend": "images/backend/Dockerfile",
        "worker_base": "images/worker/Dockerfile",
        "controlled_worker": "test/acceptance/observability/Dockerfile.worker",
    }[role], map[string]string{
        "backend": project + "-backend:acceptance",
        "worker_base": project + "-worker-base:acceptance",
        "controlled_worker": project + "-worker:controlled",
    }[role]
    source := filepath.Join(output, "build", "source")
    args := []string{"--context", contextName, "build", "--builder", contextName,
        "--load", "--pull=false", "--progress=plain", "--platform", platform,
        "--iidfile", filepath.Join(output, "build", role+".iid"),
        "-t", tag, "-f", filepath.Join(source, filepath.FromSlash(dockerfile))}
    if role == "controlled_worker" {
        args = append(args, "--build-arg", "WORKER_BASE="+project+"-worker-base:acceptance")
    }
    return append(args, source)
}
```

每次 build stdout/stderr 直接写 exclusive 0600 `<role>.stdout.log` / `<role>.stderr.log`，真实返回后保存 `<role>.command.json`（schema `observability.build_command.v1`、role、argv、started_at/completed_at、exit_code、两个日志 byte count/SHA256）。Docker 异常的 command receipt 仍保存，成功 proof 不生成；无清理。IID 要求完整 `sha256:`64 hex+可选换行，与本次 selected image inspect 的 ID/OS/architecture 严格一致；image inspect 和 base-before/base-after 各保留原 JSON、receipt/hash。controlled 前后 base tag 必须等于本次 worker_base IID，且这次实际 build 退出0。每次前后核对源码快照，Run 无外部 secret/env/build args 透传参数。

fake 仅模拟 Docker：可读 context/Engine/builder/image metadata，并根据 `--iidfile` 把固定 role image ID 写到实际目标，输出固定真实字节日志；其恶意变体改变 ID/base、返回 nonzero、构建时改 snapshot。测试读取工具产物验证，不能构造完整绿色 source/build JSON 代替调用 build seam。

- [x] Step 4: 增加 check seam 的第一个合法 build→check round-trip，再逐个 RED/GREEN 反例。成功 build 最后独占写 `build/provenance.json`（schema `observability.build_provenance.v1`、project/output/source commit/tree/digest、context/Engine/platform identity、精确三 roles、所有原始输入的 relative name/byte count/SHA256）。check 不要求当前 HEAD 等于历史 candidate，但要求 caller commit 正好等于 proof commit、祖先包含 integrationBase、该 Git 对象存在；不得让其它 commit 相同 source digest 自动通过。

所有 JSON/日志/IID/snapshot 使用 bounded regular-file reads，每级 lstat 检查、打开后 fstat same file；read once 缓存原 bytes/摘要，不接受 FIFO、symlink、外部路径、重复 name/role 或遗漏材料。JSON identity/时间必须有效且顺序一致；command argv 由固定 buildArguments 重算。完整重算 Git source、snapshot、三次退出码、日志 byte/hash、IID/selected inspect、base-before/base-after/worker_base 关系、fixed base refs/platform/Engine/builder。拒绝 unknown schema、截断日志、self-filled passed、missing roles、错 commit/tree/claim、source 改动、新文件、旧 image/base、nonzero build、错 argv、跨目录/claim、伪造常量摘要、重复输出、oversize/非 regular。

check 退出0打印 verified JSON，退出1只 stderr 原因，不能写 PASS 文件。测试给 DockerCommand 一个立即 `t.Fatal` 的实现，比较整个 claim 文件清单/内容/权限在 check 前后不变；包括失败路径。正常 zero-byte stderr 合法，empty stdout 需要与 receipt 字节数一致，不依靠日志关键词判成功。

- [x] Step 5: 加完整 wrapper 和参数/只读 black-box coverage。参数 count、commit/project 字面格式、output 非绝对或结尾斜线均属于 usage exit2；canonical/存在性/claim/source 失败为 preflight exit1。两者 executable，不接受额外 env 透传参数。build wrapper 完整内容：

```bash
#!/usr/bin/env bash
set -euo pipefail
umask 077
root=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$root"
if test "$#" -ne 3; then
  printf 'usage: %s COMMIT PROJECT CLAIM_ROOT\n' "$0" >&2
  exit 2
fi
if ! [[ "$1" =~ ^[a-f0-9]{40}$ && "$2" =~ ^switchback-obsv-[a-z0-9]{8,24}$ && "$3" == /* && "$3" != */ ]]; then
  printf 'invalid commit, project or absolute claim path\n' >&2
  exit 2
fi
exec env GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off GOENV=off \
  go run -p=1 ./test/acceptance/observabilitybuild build "$@"
```

check wrapper 完整内容：

```bash
#!/usr/bin/env bash
set -euo pipefail
umask 077
root=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$root"
if test "$#" -ne 3; then
  printf 'usage: %s COMMIT PROJECT CLAIM_ROOT\n' "$0" >&2
  exit 2
fi
if ! [[ "$1" =~ ^[a-f0-9]{40}$ && "$2" =~ ^switchback-obsv-[a-z0-9]{8,24}$ && "$3" == /* && "$3" != */ ]]; then
  printf 'invalid commit, project or absolute claim path\n' >&2
  exit 2
fi
exec env GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off GOENV=off \
  go run -p=1 ./test/acceptance/observabilitybuild check "$@"
```

Go main 的 `os.Exit(Run(...))` 使已编译 CLI 保持 exit2/1/0；`go run` 本身会把子进程非零折为 shell exit1，因此 wrapper 必须先判断全部 usage 条件。编译 CLI 的 black-box tests 覆盖 usage2、preflight1、fake round-trip0；wrapper 的 invalid args 和 missing claim 不得实际访问 Docker。

- [x] Step 6: 正式工具收尾（非物理）一次运行下列 exact commands，完整保存 exit/output；source AST 守恒不可因为新工具放宽，失败立即诊断原由。报告注明 external fake 数量、每个 RED/GREEN、pure vs physical，不写 actual build PASS。

```bash
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go test -p=1 -race -count=1 ./test/acceptance/observabilitybuild
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go test -p=1 -count=1 ./test/acceptance/observability ./test/acceptance/observabilitygroups ./internal/architecture
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off scripts/check-observability-test-groups 2ee4c24bfd43621dae7acc73445fece17e73f5ae
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go vet -p=1 ./test/acceptance/observabilitybuild
bash -n scripts/build-observability-images scripts/check-observability-build
git diff --check
```

- [x] Step 7: 只 stage 八个明确 Go 文件和两个脚本、本地提交 `test: bind observability images to captured source builds`，自审后报告。root 比较实际 diff 与规格、使用此任务 base..head 的完整 review package 交独立 reviewer；Critical/Important 修复并重新验其相关 tests/review。root 重跑精确候选工具测试，完成本任务后 checkpoint 明确“工具已实现，真实构建/双门禁/物理/capture 仍待”。

工具交付范围为 `51102a3..739e30a`，最终 fresh 任务审查 Approved，无 Critical/Important。root 在代码提交 `739e30a` 独立执行上述验证均 exit0；仅真实临时 Git/文件与外部 fake Docker，不等于真实镜像构建。初轮两份中间失败日志覆盖的审计缺口继续保留，未用重跑补称原始证据。实际状态和下一阶段见[检查点](2026-10-10-observability-gates-checkpoint.md)。

## 📍 后续边界

本计划是原双门禁 Task 3 的来源记录子交付，不替代原 Task 3 全量验收。来源工具独立审查后，原只读双门禁汇总接入 verified IDs 与实际 Backend/fake-model/Worker capture，再重算 PG/native/G3/cold/matrix/loss/retention；不能用本任务 fake 作为绿色物理材料。实际 root build 之前另行申报 exact context、镜像 tags、claim、CPU/内存、允许的依赖下载网络和完整物理资源窗口；不会自动使用本次已确认的纯工具窗口。
