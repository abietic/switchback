# B3-G parser diagnostic implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Preserve the dirty checkout; do not commit.

**Goal:** 在不改变旧采集或准入行为的前提下，交付独立失败 v2 API、命令、纯 mapper，以及可区分 B3-F 歧义的离线证据。

**Architecture:** 新有界 backend 装饰器供原 diagnostic/core 使用，原调用清理结束后用原 parser 检查同轮 read 快照。新命令输出旧成功 v1 或新失败 v2；纯 Python mapper 只识别 v2，不接入 carrier。

**Tech Stack:** Existing Go module and stdlib; existing golang.org/x/sys/unix only through unchanged native backend; Python stdlib.

## Global Constraints

工作目录固定 /path/to/switchback。唯一已接受的正式设计为 docs/superpowers/specs/2026-10-21-m1-deployment-parser-diagnostic-b3g-design.md，SHA256 d03fd984857a44a35b3091ffdb549de2a818028ecaa70736dd421539228b0e7f。
- 只新增已登记文件，不修改既有源码、依赖、部署配置、旧 scratch 或 spec；没有任何 Git stage/commit/push/reset/clean。
- 原 collector、诊断 API、成功 DTO、失败 v1、共享 parser 和原命令均不修改。
- 操作参数、返回值、error、调用顺序、FD 获取/close 次数不变；它不调用时钟、不再读 proc、不重试、不追加 native 操作。纯检查发生在原 collector 返回并完成原 cleanup 后。
- caller 和 pid1 合计最多保存 16384 bytes：各自 stat/cgroup 每份最多 4096 bytes。映射最多 16 项，超出时使后续 parse_checks 不可用，仍原样转发 collector。
- 不进入 guest，不启动 VM/unit/Engine/PG/Worker/Backend/publisher/provider，不授予实际 capability，不构建部署 ELF。capacity_ready=false、execution_enabled=false、paid_consumed=false。
- 所有测试仅 synthetic；公开 native API、main 不执行。Linux amd64/arm64 只 compile-only。GOPROXY/GOSUMDB=off，隔离 Go cache/tmp 读取本 phase test-runtime.json；测试窗口串行，根代理统一跑最终全量。
- 受保护基线为 git-visible-before.json 的 1787 文件和 protected-before.json 的 458 产物；新 spec/plan/8 个 Go 文件为唯一 Git-visible 增量。旧 native window/once 不写入，不创建任何新 native window。

---

### Task 1: Bounded v2 DTO and observation API

Read /path/to/switchback/.scratch/runtime-m1-deployment-parser-diagnostic-b3g-1021/approved-design.md and /path/to/switchback/.scratch/runtime-m1-deployment-parser-diagnostic-b3g-1021/global-constraints.md first; these are binding requirements. This task owns only six new files:
- /path/to/switchback/internal/platform/capacityprobe/deployment_parse_diagnostic_types.go
- /path/to/switchback/internal/platform/capacityprobe/deployment_parse_diagnostic_types_test.go
- /path/to/switchback/internal/platform/capacityprobe/deployment_parse_diagnostic.go
- /path/to/switchback/internal/platform/capacityprobe/deployment_parse_diagnostic_linux.go
- /path/to/switchback/internal/platform/capacityprobe/deployment_parse_diagnostic_other.go
- /path/to/switchback/internal/platform/capacityprobe/deployment_parse_diagnostic_test.go

**Consumes:** unchanged deploymentBackend, observeDeploymentDiagnostic, DeploymentObservation, DeploymentPrimitiveObservation, DeploymentFailureObservation.Validate, poolUnified and poolProcess. Existing deploymentFixture is synthetic; original operations/read snapshots are reference evidence.

**Produces:** exact public names and Go fields below. Their JSON tags are fixed:
```go
const DeploymentParseFailureSchema = "switchback.capacity.deployment_failure.v2"
type DeploymentProcessParseChecks struct {
    Owner string `json:"owner"`
    StatRead string `json:"stat_read"`
    CgroupRead string `json:"cgroup_read"`
    CgroupCheck string `json:"cgroup_check"`
    ProcessCheck string `json:"process_check"`
}
type DeploymentParseFailureObservation struct {
    Schema string `json:"schema"`
    Kind string `json:"kind"`
    Stage string `json:"stage"`
    ErrorClass string `json:"error_class"`
    LastPrimitive *DeploymentPrimitiveObservation `json:"last_primitive,omitempty"`
    ParseChecks *DeploymentProcessParseChecks `json:"parse_checks,omitempty"`
}
```
Implement (v DeploymentParseFailureObservation) Validate() error, private observeDeploymentParseDiagnostic(ctx context.Context, backend deploymentBackend) (DeploymentObservation,*DeploymentParseFailureObservation), and public ObserveDeploymentParseDiagnostic(ctx context.Context) (DeploymentObservation,*DeploymentParseFailureObservation).

- [x] **Step 1: Add red tests at the real private observation seam.** Use newDeploymentFixture. For PID1 zero start time, mutate only read:leaf:1:1 by replacing "18 99 20\n" with "18 0 20\n"; expect StatRead=bounded, CgroupRead=bounded, CgroupCheck=accepted, ProcessCheck=rejected. For read:leaf:2:1 return "0::/\nextra\n" or empty bytes; expect bounded/rejected/rejected or empty/not_checked/not_checked. Root "0::/\n" succeeds. Also change canonical third PID1 stat to a different nonzero ticks: core fails identity comparison and both checks must be accepted. Exercise caller TID 307 versus PID 301 to catch wrong expected-ID binding. Save test output as task-1-red.json/stdout/stderr using the task-owned evidence prefix; no native API call.
- [x] **Step 2: Implement DTO closed validation.** Delegate original stage/class/primitive validation by building a v1 value with its original schema. v2 schema/kind must be exact. Checks require observe/invalid, read/returned with no errno, owner matching caller.cgroup or pid1.cgroup, stat_read=bounded. Accept exactly five tuples from the spec; preserve the accepted/accepted tuple for identity drift. Deep-copy primitive, errno and returned checks.
- [x] **Step 3: Implement bounded same-attempt capture.** Embed the original backend and override only ids/openProc/openLeaf/read/close. Invalidate an owner's slot before every status open attempt. Track process owner, private attempt ID and stat-before-group order, record bounded copied bytes only on read success, record empty/oversize shape without oversized allocation, and retain the copied current slot after leaf close while deleting FD mappings. IDs come from the existing ids result; PID1 expected TID is 1. Limit maps to 16; disable eligibility after overflow and still forward results. Nil backend/context must be checked before wrapping. Invoke old observeDeploymentDiagnostic exactly once. After it returns/cleans up, attach facts only for matching eligible failed last cgroup read; use poolUnified(rawGroup) and poolProcess(rawStat,rawGroup,expectedTID) without substitution. Drop actual ticks/paths. Return unchanged success or copied v2 failure.
- [x] **Step 4: Add native platform bindings and compatibility matrix.** Linux amd64/arm64 locks OS thread around composition using deploymentLinux; unsupported platform never constructs it. Test nil/already-cancelled before IO; full success and namespace EPERM/EACCES; original each-operation pre/post error and cancellation matrix with fd+late error. Compare old-v1 projection, exact operations list, FD maximum and close counts with original API. Cover multi-owner/round, repeated status attempt, canonical/FD reuse, failed and oversize reads, alias mutation, retained buffers <=16384, map overflow, and independent returned values. Raw/error canaries must not appear in JSON. All tests are fixtures; binding source can be AST-checked.
- [x] **Step 5: Run focused green and self-review.** With env from test-runtime.json, run go test -mod=readonly -count=1 -run 'TestDeploymentParse' -v ./internal/platform/capacityprobe, then one related package suite if justified. Save structured argv/env/exit/output receipts under task-1-* and report exact method/subtest counts. Run gofmt only on your six new files. Do not stage/commit. Write task-1-report.md with red/green, changed paths, hashes, limits and concerns.

### Task 2: Fixed command and non-authorizing mapper

Read /path/to/switchback/.scratch/runtime-m1-deployment-parser-diagnostic-b3g-1021/approved-design.md and /path/to/switchback/.scratch/runtime-m1-deployment-parser-diagnostic-b3g-1021/global-constraints.md first. This task starts after Task 1 is reviewed; it owns only four new files:
- /path/to/switchback/cmd/switchback-deployment-parse-diagnostic/main.go
- /path/to/switchback/cmd/switchback-deployment-parse-diagnostic/main_test.go
- /path/to/switchback/.scratch/runtime-m1-deployment-parser-diagnostic-b3g-1021/parse_output_mapper.py
- /path/to/switchback/.scratch/runtime-m1-deployment-parser-diagnostic-b3g-1021/test_parse_output_mapper.py

**Consumes:** Task 1 public ObserveDeploymentParseDiagnostic(context.Context), DeploymentParseFailureObservation, DeploymentParseFailureSchema, DeploymentProcessParseChecks with fields Owner/StatRead/CgroupRead/CgroupCheck/ProcessCheck. Original success and namespace DTO are unchanged. JSON tags and five valid tuples are exactly the approved design.

**Produces:** independent no-arg Go executable source and Python assess_parse_failure(stdout: bytes, stderr: bytes) -> dict; old CLI and carriers remain untouched.

- [x] **Step 1: Write red command and mapper tests.** In Go inject an observer into private run; never invoke main or public native observer. Assert parsed stdout schema v2 and status1 for a v2 failure; root success v1 status0, namespace-denied v1 status2. Python fixture is {schema:"switchback.capacity.deployment_failure.v2",kind:"diagnostic_only",stage:"observe",error_class:"invalid",last_primitive:{operation:"read",target:"pid1.cgroup",outcome:"returned"},parse_checks:{owner:"pid1",stat_read:"bounded",cgroup_read:"bounded",cgroup_check:"accepted",process_check:"rejected"}}. Assert classification=failure_packet and packet equals fixture for stdout and stderr individually, with other stream empty. Save red receipts without running native.
- [x] **Step 2: Implement command protocol.** main binds only new API. run rejects args/nil observer before observe; uses exactly 5-second context, always cancel on return. Handle zero success/non-nil failure and full success/nil failure; contradictory or invalid schema yields finite observe/internal_invalid_result. Marshal old success v1 <=32768 bytes including LF; failures v2 <=4096. Namespace EPERM/EACCES succeeds status2. Encoding failure, oversize and write failure are finite v2 packets. Each stream has at most one Write, short/error stdout may trigger one fixed stderr write_failed and no retry. Tests cover nil writers, negative/overlong n, full+error, short+nil/error, stderr failure, and prove binding without executing native. Use small focused helpers; do not duplicate entire old runner verbatim or extract legacy sealed files.
- [x] **Step 3: Implement strict pure mapper.** Input exact bytes, each <=32768, else ValueError("invalid_capture") before hashing. Normal output contains schema for the assessment, source bytes/sha256, kind=diagnostic_only, authority=none, execution_linkage=unproven and classification. Two nonempty inputs are ambiguous. Both empty or a lone unrecognized input are unrecognized. A single valid complete v2 <=4096 including optional LF is failure_packet, expected_probe_exit_from_packet=1, packet retains only validated fields. Reject duplicate/unknown keys, non-finite JSON, wrong scalar types (errno bool is invalid), unknown enums, invalid stage/class/primitive/check tuple. No raw exception content, IO, subprocess, environment, or authority verdicts. Stage/class and primitive vocabulary match unchanged v1; schema is v2. Do not recognize success v1 or failure v1.
- [x] **Step 4: Exhaustive output tests and real capture regression.** Go enumerate stage/class combinations, valid longest failure size, success 32768/32769 boundaries, encoding failure and write cross product, nil/cancelled/invalid results, pointer consistency. Python cover five check tuples, all illegal cross combinations and qualification, nesting/wrong-key/duplicate/type/nonfinite/truncation/bounds, empty/both streams, hashing, canaries. B3-F's saved probe-failure.json must be unrecognized and gain no checks; load frozen B3-F old mapper only for its original failure classification, with input source hash verified. No guest transport.
- [x] **Step 5: Run focused green and self-review.** Run go test -mod=readonly -count=1 -v ./cmd/switchback-deployment-parse-diagnostic and /usr/bin/python3 -I -B -m unittest discover -s .scratch/runtime-m1-deployment-parser-diagnostic-b3g-1021 -p test_parse_output_mapper.py -v. Use isolated env/cache from test-runtime.json, gofmt only the new Go files, PYTHONDONTWRITEBYTECODE or -B for Python. Save task-2-* receipts and task-2-report.md with exact tests, outputs and concerns. No stage/commit.

## Root final verification and closure

- [x] Generate per-task uncommitted review packages using git diff --no-index against /dev/null for the task's owned new files, with hashes and baseline HEAD; user explicitly forbids commits. Independent task reviewer reads brief/report/package, supplies both Spec and Quality verdicts. Resolve every concrete Important/Critical finding before next task; record any Minor and cannot-verify items.
- [x] One whole-change review uses all ten new sources, design and evidence; read-only broad current change review, not whole dirty branch. Most capable model per SDD. Any final fixes get covering tests and re-review.
- [x] Root freeze source bytes, then run one final Go JSON suite for ./internal/platform/capacityprobe and ./cmd/switchback-deployment-diagnostic and ./cmd/switchback-deployment-parse-diagnostic; run applicable -race for those packages, and GOOS=linux GOARCH=amd64/arm64 CGO_ENABLED=0 go test -c for the new API package and new command (four compile-only receipts).
- [x] Run new Python mapper and frozen B3-F output mapper related tests once; do not run native host/window. Count method/subtest outcomes from actual output. Check formatting on eight new Go files.
- [x] Verify all 1787 baseline files and 458 protected artifacts, exact Git-visible increment (spec, plan, eight Go files), untouched HEAD/branch/index and old once markers. Report offline_complete only when evidence and reviews support it; original native root cause remains unresolved.
- [x] Save source snapshots, final receipts, review reports, report/outcome, plan/ledger snapshots and a phase seal; read back each bound hash and exact phase file set. No native candidate or live permission is implied.
