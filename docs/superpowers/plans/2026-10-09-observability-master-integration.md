# 可观测性主线组合与取消诊断计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Root owns runtime resources and diagnostic verdicts.

**Goal:** 在 master `8fdbe6b556a0602b8392a2eed0e09b9e007542d1` 上组合已提交观测代码，收敛 `capture_cancelled` 并验证组合结果。

**Architecture:** 新独立工作树从指定 master 创建，组合旧观测 HEAD `29098fd8f5dd0e4fdc86f20b41ea97665b30a2a7`。先形成可追溯的组合提交，再用原行为断言诊断；不把局部绿测当作根因或全栈通过。

**Tech Stack:** Go/race、PostgreSQL 17.10、现有 OpenSpec 与观测验收工具。

## 🔐 Global constraints

- 原 basis/model 捕获 250ms、fixture 30s、已有 SQL/发布断言不得放宽
- PostgreSQL settlement + TraceManifest/ObjectStore Trace JSONL 仍为权威；观测故障不阻塞执行
- ACP、六方法 AgentAdapter、Factory/profile/YHC、WorkerControl/settlement 不新增行为
- 主 checkout 和旧观测工作树未提交改动不触碰；旧日志/数据卷不覆盖、追加或删除
- 架构会话已确认本轮三个 localproduct 候选路径和新 fixture/Go race 窗口无占用；扩大范围先同步
- 只提交本地组合候选，不 push、Ready、合入 PR #2 或改 master
- OpenSpec 3.2 只有实际最终验收与独立审查完成后才可勾选

## ✍️ 组合实施

### Task 1: 形成已提交组合候选

**Files:** Modify `internal/interaction/adapter/litellm/client_test.go` 的唯一合并冲突；其余文件仅 Git 三方组合。

**Interfaces:** 输入 exact master/observability commits；输出两父组合提交。保留 master 的 CI、ACP 与 PG 测试夹具，同时保留已提交观测能力。

- [ ] 执行 `git merge --no-ff --no-commit 29098fd8f5dd0e4fdc86f20b41ea97665b30a2a7`；记录实际冲突，若超过已知一文件先上报
- [ ] 用 `apply_patch` 解决冲突，保留双方时钟与错误检查意图；目标片段如下（周边 profile/ContextPack 断言不动）

```go
// The processor passes TimeoutAt to a real HTTP context deadline.
now := time.Now().UTC()
claim := retiredKimiRunClaim(now)
claim.ModelProfileRevision = profile.Revision
claim.ModelProfileDigest = profile.Digest
processor, _, model, _ := newRetiredKimiRunProcessor(t, client, now)
contexts := processor.Context.Repository.(*retiredKimiContextRecorder)
if err := processor.ProcessAssistantRun(context.Background(), claim); err != nil {
    t.Fatal(err)
}
if model.err != nil {
    t.Fatalf("model request failed before audited context inspection: %v", model.err)
}
if model.calls != 1 || len(contexts.packs) != 1 {
    t.Fatalf("processor model/persistence calls = %d/%d, want 1/1", model.calls, len(contexts.packs))
}
```

- [ ] 验证 `git diff --check`、`git ls-files -u` 无冲突，以下命令退出 0；完整输出分别留存，不修改测试预算

```bash
GOMAXPROCS=2 go test -p 1 ./internal/interaction/adapter/litellm ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/codexacp -count=1
GOMAXPROCS=2 go test -p 1 -race ./internal/interaction/adapter/litellm ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/codexacp -count=1
```

- [ ] 检查 ACP/CI/两个 PG fixture blob 与 master 相等；`runtime_observation_basis.go`、`runtime_model_observation.go`、`observation_supplements_fixture_integration_test.go` 与旧观测 HEAD 相等
- [ ] 完成 merge commit（不包含这份计划或私有报告），写报告：两个 parents、tree、实际 resolution、命令/退出码/原始日志及风险
- [ ] 独立 task Spec/Quality 审查冲突处理及继承一致性；不把 inherited 349 文件重新实施或宣称已审核

## 🔍 取消诊断

### Task 2: 原断言的有限取消重现

Root 控制新 loopback `19309` PostgreSQL fixture；不得复用宿主服务或旧 volume。每条测试保留完整日志、exit、commit/tree、真实执行配置；失败即记录，不覆盖后重试。

- [ ] 组合提交上原锚定 race/integration 子例 baseline，随后最多 10 次默认 GOMAXPROCS 的 `-failfast` 重现

```bash
SWITCHBACK_TEST_DATABASE_URL='postgres://fixture:integration-fixture-only@127.0.0.1:19309/switchback?sslmode=disable' go test -race -tags=integration ./internal/localproduct -run '^TestImmutableModelSupplement$/^first_turn_marker_subsequent_turn_normal$' -count=1 -timeout=2m -v
SWITCHBACK_TEST_DATABASE_URL='postgres://fixture:integration-fixture-only@127.0.0.1:19309/switchback?sslmode=disable' go test -race -tags=integration ./internal/localproduct -run '^TestImmutableModelSupplement$/^first_turn_marker_subsequent_turn_normal$' -count=10 -timeout=5m -failfast -v
```

- [ ] 区分 model/capture_cancelled、run_basis/capture_cancelled、parent_basis_unavailable、facts/replay 和环境失败
- [ ] RED 后单变量最小化，再列可证伪假设；需要 probe/fix 时另写 exact patch/test 步骤及独立 review，不先猜修复
- [ ] 若有界尝试仍不能重现，明确报告没有根因/修复，停止无目标重复；不得宣布历史取消关闭

## 📊 验证交付

### Task 3: 组合回归与交付

- [ ] 组合候选上运行原完整 localproduct race/integration，再运行 ordinary/short/race/vet/build 和静态门禁（逐条保留结果，失败不洗掉）
- [ ] 阅读已有 full-stack verifier/claim 的实际入口，按新组合 commit 选择需要重跑的物理组合项；保留此前矩阵/retention 原证据并核对适用范围，不冒称新组合已通过
- [ ] 独立审查本轮变更、未闭合失败和验收矩阵；OpenSpec 3.2 未全部满足时保持未勾选
- [ ] exact fixture 资源验证后 stop/rm container（不带 `-v`），保留新旧数据/日志；再次核验旧证据 hash 和 dirty 文件未变，向架构会话释放窗口
- [ ] 提供短检查点：完成项、失败项、根因是否证实、组合 commit、下一步；不擅自合入 PR
