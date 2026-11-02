# Observability readonly verifiers implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为已有 cold/matrix 验证提供不修改证据的入口，供原 Task 3 汇总复用；本任务不交付整个汇总或物理验收。

**Architecture:** 只把既有验证函数的最终结果输出分离为一个 writer seam。原 writer 命令仍独占创建原 PASS 文件，新 check 命令输出同一 scoped JSON 到 stdout；两者运行同一验证逻辑，不读取已有 PASS 文件判通过。

**Tech Stack:** Go 标准库、现有合成 cold/matrix fixtures 与实际编译 CLI；不新增依赖。

## 📋 Global Constraints

- 只在 `.worktrees/observability-master-integration-1009` 工作；固定 master 基线 `8fdbe6b556a0602b8392a2eed0e09b9e007542d1`，本子任务起点为干净文档交接 `ac50e05ecea5d2757a6e9a460d958529d32f063d`。
- 遵循[已确认设计](../specs/2026-10-10-observability-two-gates-design.md)“汇总复用其只读验证逻辑，保留既有写入模式与断言”；这不是新增产品决策，不重新请求同一设计审批。
- 不修改产品、Dockerfile、依赖锁、CI、原 strict/native JSON checker、原有测试函数/断言、250ms/30s 捕获预算。用户已确认唯一范围扩展：为固定 `2ee4c24bfd43621dae7acc73445fece17e73f5ae` 的测试守恒工具显式登记本轮新增13项，不改变旧基线或自动接受任意新增。
- 原 cold nine-service、两 Turn、five-second 上限，以及 matrix 20 pairs / 80 unique Turns / 1000ms p95 increase、exact resident Worker/continuous subscription/source proof 断言全部保留。
- 原 `verify-full-stack-cold` / `verify-full-stack-matrix` 的文件名、JSON schema/字段/作用域、O_EXCL 和重复写失败保持不变。
- 新 `check-full-stack-cold` / `check-full-stack-matrix` 只读 raw input，stdout 返回同一 scoped JSON；不创建/修改/删除任何 claim 文件，不连接 Docker、HTTP、SQL、观测服务或 provider。
- 合法读取已有 PASS 文件的目录也不覆盖它；旧 PASS 的内容不作为成功输入。失败不得输出成功 JSON。stdout 写错误或 short write 必须返回错误。
- 架构会话已确认下列 exact files、root docs 与独立 sdd ledger 无交叉占用；测试只允许 GOMAXPROCS=2/-p=1、临时合成文件、实际编译 CLI/子进程和既有 httptest 临时 loopback。没有真实 Docker/PG/native/全栈窗口，不访问旧 claim。
- 每个 RED/GREEN 保存独立新文件和实际 exit；不能覆盖先前失败日志。所有 Git 调用清除 GIT_* 并禁用 global/system config 与 replacement objects。
- 保留旧六份冻结证据及主/旧工作树 dirty。不 push、Ready、合入或勾选 OpenSpec 3.2；本工具不处置 `capture_cancelled`。

---

## 📦 文件与接口

| 文件 | 本任务职责 |
| --- | --- |
| `test/acceptance/observability/full_stack_cold.go` | 最终输出 seam；验证主体不变 |
| `test/acceptance/observability/full_stack_matrix_report.go` | 同上 |
| `test/acceptance/observability/driver.go` | 增加两项显式 check mode |
| `test/acceptance/observability/full_stack_readonly.go` | 只读路由、claim 前检、stdout 输出 |
| `test/acceptance/observability/full_stack_readonly_test.go` | 新行为/CLI/目录不变性反例 |
| `test/acceptance/observabilitygroups/main.go` | 固定基线外增加经过登记的身份/摘要/分组校验 |
| `test/acceptance/observabilitygroups/registered_additions.go` | 内建本轮13项固定内容和九个默认测试的分组声明 |
| `test/acceptance/observabilitygroups/main_test.go` | 实际 CLI 的登记正例和保护反例，保留原负例 |

root 单独维护本计划、原实施计划/checkpoint、`.superpowers/sdd/observability-gates1010-progress.md` 和任务独立 brief/report/review；实现者只改上述八个 Go 文件和自己的私有报告。原五文件暂存候选tree `5baa4309997ab87ec05ba01d3e3a6a808a0cff93` 保留；新增守恒适配作为同一 Task 1 的已授权修订，不重做原RED/GREEN。

### Task 1: cold/matrix 只读验证入口

**Interfaces:** 保留 `verifyFullStackCold(project, output string) error` 与 `verifyFullStackMatrix(project, output string) error`。新增 `checkFullStackEvidence(kind, project, output string, stdout io.Writer) error`，kind 仅为 `cold` / `matrix`。内部 `fullStackEvidenceWriter func(output, name string, value any) error` 只用于最终结果，不替代内部校验。CLI 为：

```bash
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go run ./test/acceptance/observability \
  -mode check-full-stack-cold -project switchback-obsv-example1 -output /private/tmp/example-claim
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go run ./test/acceptance/observability \
  -mode check-full-stack-matrix -project switchback-obsv-example1 -output /private/tmp/example-claim
```

示例路径不是实际 claim，不执行上述示例。成功 stdout JSON、exit0；失败 stderr、exit1、无成功 JSON；原 writer modes 仍不向 stdout 发成功 JSON。check modes 和旧 verifier 一样拒绝非空 `-backend` / `-report`。测试 seam 为上述调用接口和实际编译 CLI，已确认 Task 3 的只读 verifier/证据接口；不测试内部 callback 调用计数。

- [x] Step 1: 新文件只写第一个 cold 合成行为反例。复用 `fullStackColdFixture(t)`；调用只读入口，断言 scoped schema/project、目录文件名/bytes/mode/mtime 均不变，无新 PASS 文件。先保存缺入口编译 RED，再做最小 cold 输出抽取获得 GREEN。核心行为断言如下，snapshot helper 用 filepath.WalkDir + Lstat + ReadFile 保存元数据与实际 bytes，不更改权限或文件：

```go
func TestFullStackReadOnlyColdPreservesEvidence(t *testing.T) {
    project, output := fullStackColdFixture(t)
    before := readonlyDirectorySnapshot(t, output)
    var stdout bytes.Buffer
    if err := checkFullStackEvidence("cold", project, output, &stdout); err != nil {
        t.Fatal(err)
    }
    var result struct {
        Schema string `json:"schema_version"`
        Project string `json:"project"`
        Scope string `json:"scope"`
        Status string `json:"status"`
    }
    if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
        t.Fatal(err)
    }
    if result.Schema != "observability.full_stack_cold.v1" || result.Project != project || result.Scope != "cold_start_only" || result.Status != "passed" {
        t.Fatalf("wrong scoped output: %+v", result)
    }
    if !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
        t.Fatal("readonly verification changed evidence")
    }
}
```

Run `GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go test -p=1 -count=1 -run '^TestFullStackReadOnlyColdPreservesEvidence$' ./test/acceptance/observability`。

- [x] Step 2: 机械抽取：原两个 verifier 保留 wrapper，原完整验证主体分别移为 `verifyFullStackColdWithWriter` / `verifyFullStackMatrixWithWriter`，仅将末尾 `writeEvidence(...)` 换成同参数 `save(...)`；所有其它读取和业务判定保持原样，不复制验证主体。先 cold 后 matrix 各自测试。wrapper 完整代码：

```go
type fullStackEvidenceWriter func(output, name string, value any) error

func verifyFullStackCold(project, output string) error {
    return verifyFullStackColdWithWriter(project, output, writeEvidence)
}

func verifyFullStackMatrix(project, output string) error {
    return verifyFullStackMatrixWithWriter(project, output, writeEvidence)
}
```

两个 WithWriter 接受 `(project, output string, save fullStackEvidenceWriter) error`。保留原完整函数体，cold 最后是 `return save(output, "full-stack-cold-passed.json", result)`，matrix 最后是 `return save(output, "full-stack-matrix-passed.json", result)`。

只读路由先验证 kind、非 nil stdout、合法 project/canonical absolute output；在原 readClaim 之前用现有 `readFullStackInput(output, "claim.json")` 检查 bounded regular 文件并 decode project/output 匹配，拒绝静态 symlink/FIFO/超限/截断/跨 claim，避免旧 readClaim 的不受限读取先触发。此附加前检仅在新 check seam，不改变旧 writer 语义。受信任本地 fixture 模型不宣称解决所有旧 reader 的 TOCTOU/宿主并发篡改问题，汇总仍须独立绑定原始材料。

最终 stdout writer 完整行为：

```go
save := func(_ string, _ string, value any) error {
    raw, err := json.MarshalIndent(value, "", "  ")
    if err != nil { return err }
    raw = append(raw, '\n')
    n, err := stdout.Write(raw)
    if err != nil { return err }
    if n != len(raw) { return io.ErrShortWrite }
    return nil
}
```

kind cold 调用 cold WithWriter，matrix 调用 matrix WithWriter。未知 kind/nil stdout 在任何读取前返回错误；不能给未知模式一个成功空对象。

- [x] Step 3: 按 vertical slice 增加 matrix 合成正例，断言 `observability.full_stack_matrix.v1`、`paired_outage_only`、20 pairs 和目录不变；再增加已有 PASS sentinel 的 cold/matrix 检查：旧文件存在、故意内容为 `not acceptance proof`，check 仍从 raw 重算且 sentinel bytes/mode/mtime 不变。原 writer 再调用仍 O_EXCL 失败；新 check 可重复调用，输出确定性同值而目录不变。

- [x] Step 4: 不修改旧负例测试。新增两模式入口反例逐个 RED/GREEN：缺 raw input（删除 cold-before 或 recipe）、wrong project、坏 JSON、cold 第九服务缺失、matrix 少一pair/复用Turn、ACK 早于第二Turn、resident Worker label mismatch、interval非连续、超5秒、p95超过1000ms。使用现有 fixture/rewrite helper 修改临时输入，check 必须 error、stdout长度0、目录 snapshot 在调用前后不变。已由旧共享逻辑拒绝的反例首先运行可为 characterization GREEN，不虚构 RED。一个独立 test 以返回 sentinel error 的 io.Writer 和返回 `len(p)-1,nil` 的 Writer 证明输出错误/short write 返回非nil且目录不变。

- [x] Step 5: 逐项 claim 前检反例：canonical alias、claim.json static symlink、FIFO、16MiB+1、截断 JSON、wrong output、非合法 project；不读真实环境。FIFO 使用 syscall.Mkfifo 创建在 t.TempDir 内；测试命令具有有限超时且新前检必须在 open 前拒绝。snapshot helper 对非regular文件只记录 Lstat，不 ReadFile；所有反例 error/stdout空/文件不变。

- [x] Step 6: 接 CLI 两 modes，与旧 case 相同拒绝 backend/report，再调用 `checkFullStackEvidence("cold"|"matrix", *project, *output, os.Stdout)`。更新 help mode 列表，不改其它 routes。新测试一次 `go build -p=1 -o <t.TempDir>/observability .` 获真实 binary，使用 exec.CommandContext 的有界时间运行两正例和缺输入/backend/report反例；检查实际 exit0/1、stdout JSON/空及证据目录不变。给子进程设置仅该测试的 Docker command trap，Docker调用将记录 marker 并失败；case 完成确认无 marker。trap 仅替代进程边界，不模拟内部 verifier，也不接真实 Docker/DB/HTTP。

- [x] Step 7: 先完成下列守恒适配，再一次正式验证，独立日志名称+noclobber保存命令/exit/output，失败保留。此前guard exit1保留为实际计划冲突，不回写为通过。不得将合成 fixture 称作真实物理 PASS。原验收测试不删除/改弱，原 writer 正例仍实际创建 scoped 文件。

守恒适配保持 CLI `BASE_COMMIT` 参数、exit0/1/2 和五组实际编译不变；git resolve 后只在完整 SHA 等于上述固定基线时使用内建登记，其他基线仍按原始严格集合相等规则检查。旧函数缺失/变化仍报错；登记身份不得已存在于旧基线。不新增运行时manifest/env开关，不读当前HEAD生成期望值。

新增数据结构与内容来源：

```go
type registeredAddition struct {
    ID string
    SHA256 string
    Groups []string // 顶层测试固定为 {"", "integration"}，辅助函数/method为nil
}
const registeredBaseline = "2ee4c24bfd43621dae7acc73445fece17e73f5ae"
```

内建表恰好登记 `test/acceptance/observability/` 下九个Test：`TestFullStackReadOnlyCLI`、`TestFullStackReadOnlyClaimPrecheck`、`TestFullStackReadOnlyColdPreservesEvidence`、`TestFullStackReadOnlyColdRejectsBrokenEvidence`、`TestFullStackReadOnlyExistingPassAndRepeat`、`TestFullStackReadOnlyMatrixPreservesEvidence`、`TestFullStackReadOnlyMatrixRejectsBrokenEvidence`、`TestFullStackReadOnlyOutputFailure`、`TestFullStackReadOnlyRejectsRouteBeforeReading`；三个helper：`readonlyDirectorySnapshot`、`readonlyFixture`、`readonlyReject`；method：`Write/readonlyOutputWriter`。SHA256来自已暂存测试文件的既有规范化AST token字符串（format.Node + tokens），一次采集后作为代码常量提交，独立review核对；以后内容改动必须重新显式审查，不能在运行时更新。哈希是本地审核快照，不声称防御能同时改源码和工具的宿主攻击者。

验证过程：复制before到expected；逐项拒绝空ID/重复ID/非法64位小写hex摘要/旧基线碰撞/非法分组/缺函数/摘要不一致；仅对匹配固定摘要的已登记函数把实际token值加入expected，最后维持 `reflect.DeepEqual(expected, after)` 拒绝其他变化。ordinary default/integration集合仅加入登记的九个顶层测试；helpers不冒充测试，不因名称类似Test就改变分组。native/native-only/fixture组与五组 `go list -export -test` 保持原判定。核心比较必须先核对hash再增加expected：

```go
actual, ok := after[entry.ID]
if !ok { return fmt.Errorf("registered addition missing: %s", entry.ID) }
sum := sha256.Sum256([]byte(actual))
if fmt.Sprintf("%x", sum) != entry.SHA256 {
    return fmt.Errorf("registered addition changed: %s", entry.ID)
}
expected[entry.ID] = actual
```

从公开CLI seam补测试，沿用临时真实Git、真实Go build/list，不mock内部比较。小仓库通过在临时编译目录复制实际checker源码并仅替换内建登记数据来测试审核配置边界：固定一个literal新测试和helper及其独立已知token摘要，绑定临时基线的真实SHA；不新增产品CLI允许用户传入登记。原CLI全部反例继续运行。第一正例先RED（原集合比较拒绝登记的新函数），最小接入后GREEN；再逐个覆盖：旧测试删除/断言变化、未登记Test/helper、已登记Test/helper删除或内容变化、默认测试移入integration/native、错误基线绑定、重复身份、非法digest、非法分组、注册碰撞。拒绝case断言实际exit1和具体诊断，不能仅凭编译错误当保护生效；有效case exit0并包含原完整成功消息。每次迭代新日志，继承旧逻辑已拒绝的case明确为characterization，不虚构RED。最后实际脚本在当前完整工作树、固定2ee基线必须通过，证明登记13项与真实候选匹配。

```bash
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go test -p=1 -race -count=1 ./test/acceptance/observability
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go test -p=1 -count=1 ./test/acceptance/observabilitygroups ./internal/architecture
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off scripts/check-observability-test-groups 2ee4c24bfd43621dae7acc73445fece17e73f5ae
GOMAXPROCS=2 GOFLAGS=-p=1 GOWORK=off go vet -p=1 ./test/acceptance/observability ./test/acceptance/observabilitygroups
gofmt -l test/acceptance/observability/full_stack_cold.go test/acceptance/observability/full_stack_matrix_report.go test/acceptance/observability/driver.go test/acceptance/observability/full_stack_readonly.go test/acceptance/observability/full_stack_readonly_test.go
git diff --check
```

- [x] Step 8: 仅 stage 表中八个 Go 文件，本地提交 `test: add readonly cold and matrix evidence checks`，自审实际 net diff 的验证主体/旧JSON/O_EXCL不变；完整报告追加原独立路径。root 对原始 task base `c3c57f078fd8df74da83ccb5dfc2969cfa86976f`..final head 生成 review package，fresh reviewer 给 Spec/quality 两结论，修复 Critical/Important 并复审；root 精确候选独立验证及旧 evidence/dirty 保全，checkpoint 记录本子交付而不把原 Task3/3.2勾选。root公共文档不随实现者Go提交混入。

## ✅ 子交付结果

受测代码提交 `0208668e95300b7d4f805e6c5b583885a7791e45`，tree `5e252341a82aa2b9d0704c3d7877a966579de23c`；完整任务审查范围为 `c3c57f0..0208668`，独立审查 Spec compliant / quality Approved，无 Critical/Important。root 在同一干净候选独立执行两包 race、三个默认包、固定 `2ee4c24` 守恒与五组编译、vet、格式、脚本语法及差异检查，均 exit0。13项显式登记的增删/改体/移组、未登记新增及旧测试变化均有实际 CLI 拒绝反例。

旧 writer 的 O_EXCL、原读取/断言/JSON 保留；六份冻结旧验收证据与其他 dirty 工作树摘要未变。实现者此前模块缓存 warning 和原守恒冲突 exit1 日志保留，root 本次无 warning 不消除历史材料。以上仅是工具子交付，不是实际构建、PG/native 或物理验收；本地文档交接不改变受测 Go 代码。

## 📍 后续仍开放

本任务只解决 cold/matrix 校验时的写副作用。来源工具与 fixture captures 的对齐、PG/native日志identity绑定、G3 query原始响应、loss/retention便携只读核验仍需接入原汇总，不能用这两个 scoped结果或手填passed代替。真实构建/新claim/PG/native/物理组合回归另行协调，`capture_cancelled` 根因/处置与全分支审查仍未闭合。
