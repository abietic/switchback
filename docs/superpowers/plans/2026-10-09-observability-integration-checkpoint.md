# 可观测性组合检查点

_2026-10-09 · 本地组合候选，不是修复完成或可合入证明_

---

## 📋 结论

指定 master `8fdbe6b` 与旧观测 HEAD `29098fd8` 已组合成本地提交 `3b1b8eb`。唯一测试冲突保留了双方的时钟、错误和上下文断言，独立 scoped 审查通过。没有修改捕获逻辑、超时或发布断言。

## 📊 已执行

| 检查 | 结果 |
| --- | --- |
| 默认 GOMAXPROCS 原取消子例 | 基线及 10/10 重放通过 |
| 全项目 `-p2 -race -tags=integration` | 48 个有测试的包、2364 个测试/子测试通过；7 个测试级 SKIP |
| master 严格 CI 证据检查 | 失败：拒绝这些 SKIP，不能称 CI 全绿 |
| 全项目 short、vet、静态门禁、Backend/shim 编译 | 通过 |
| 仓库固定 OpenSpec 1.6 严格验证 | 13/13；CLI 统计上报网络告警保留 |

完整回归中 22 条 WARN/ERROR 属于 16 个最终通过的测试，日志保留；不宣称输出无警告。继承的 `.superpowers/sdd/g3-correlation-task-1-brief.md` EOF 空行仍使全分支 diff-check 非零，冲突文件本身检查通过。

## ⚠️ 仍未闭合

本轮没有复现 `capture_cancelled`，不能建立根因、称已修复或抹去历史失败。默认配置重放和历史低并发采样不是因果证明。已有 cold/matrix/loss/retention 证据保留，但没有在新组合提交上重做物理全栈验收；生产 Web/browser 和远端组合 CI 本轮也未重跑。

组合后，5 个原生架构测试/容器 helper、1 个已发布 claim 读取测试、1 个原生 Worker 收集测试需要显式 fixture，却与 master 的“测试不得 SKIP”门禁冲突。严格验证器实际 exit1；未做豁免、删断言或变更 CI。

建议确认后划分严格 PostgreSQL 自动门禁与独立物理验收门禁，二者都必须通过才允许 Ready。另一种做法是默认 CI 部署全部所需物理 fixture，资源与执行成本更高。按 `brainstorming` 的设计确认要求，测试分组/门禁范围变更当前等待用户选择；不是等待额外绿测。

## 🔐 保留与边界

架构会话已确认原候选路径和新增候选测试/CI路径无写入冲突。主 checkout、旧观测工作树脏文件及六份 frozen evidence 均核对未变。所有测试使用新 loopback19309 fixture；只回收本轮 exact 容器，保留数据卷和完整日志，不动宿主常驻 PostgreSQL。

OpenSpec 仍为 5/6，3.2 未勾选；[PR #2](https://github.com/abietic/switchback/pull/2) 本轮读回仍为 Draft，没有 push/Ready/合入。ACP/Factory/profile/YHC 和执行权威无新增行为。

私有详细证据位于当前工作树 `.superpowers/private-integration1009/`：原始日志、两个 parent diff、blob 比对、scoped review、完整命令/exit 的 `root-verification.json`、`claim.json`、cleanup receipt；旧证据仍在原工作树，没有复制成新验收。
