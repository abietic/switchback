# CodeGraph 与 Agent 导航设计

日期：2026-07-26
状态：已批准，待实施

## 目标

为 Switchback 仓库建立可重复使用的 CodeGraph 索引入口，并给 Coding Agent 一条从“任务问题”到“源码、调用路径、影响范围和验证命令”的短路径。

成功标准：

1. 当前工作树可以被 CodeGraph 完整索引，索引状态可检查。
2. Agent 查找符号、调用链和影响范围时优先使用 CodeGraph，而不是从全仓库反复 `grep`。
3. 新 Agent 能从一个短导航文档定位核心模块、权威资料和项目门禁。
4. CodeGraph 仅作为导航与分析工具；源码、测试、运行配置和 OpenSpec 仍是最终证据。

## 当前约束

- CodeGraph CLI 1.4.1 已安装，Codex 全局 MCP 配置已存在。
- 仓库尚未初始化 `.codegraph/`。
- 工作树存在大量用户未提交改动；本次只能做范围明确的增量编辑，不能格式化、回滚或顺带修改其他内容。
- 本次不升级全局 CodeGraph，不重构业务模块，不把本地索引数据库提交到 Git。

## 设计

### 1. 本地索引

在仓库根目录运行 `codegraph init .`，生成 `.codegraph/`。

- `.codegraph/codegraph.db` 是本地派生产物，必须由 `.codegraph/.gitignore` 忽略。
- `.codegraph/.gitignore` 可以纳入版本控制，使其他开发者初始化后不会误提交数据库。
- 索引应覆盖当前工作树，包括未提交源码；这符合 Agent 面向当前真实状态工作的目标。

### 2. 稳定命令入口

在现有 `Makefile` 中增加：

- `codegraph-init`：初始化或刷新项目索引。
- `codegraph-status`：显示索引状态。
- `codegraph-smoke`：先检查状态，再执行一条横跨 Task Orchestration 与 Worker Runtime 的真实查询。

命令通过 `CODEGRAPH ?= codegraph` 保留本地覆盖能力。目标失败时沿用 CodeGraph 的非零退出码，不隐藏缺少 CLI、索引损坏或查询失败。

### 3. 仓库级 Agent 规则

在 `AGENTS.md` 的协作约束附近增加精简 CodeGraph 规则：

- 存在 `.codegraph/` 时，理解或定位代码应先查询 CodeGraph。
- 适用问题包括符号源码、调用路径、动态分派和 blast radius。
- 精确文本、非代码文件和 CodeGraph 未覆盖内容再使用 `rg` 或直接读取。
- 图查询结果必须回到当前源码、测试和配置核验，不能把索引当业务事实源。
- 使用 Makefile 目标检查或刷新索引。

该规则只定义工具选择顺序，不复制全局 Codex 配置，也不改变项目工程边界。

### 4. Agent 导航文档

新增 `docs/agent-navigation.md`，保持为短入口而不是第二份架构设计。内容包括：

- 权威来源的阅读顺序：当前源码和测试、OpenSpec active change、核心协议/架构文档、背景性说明。
- 按问题类型映射到主要源码目录，例如 Interaction、Task Orchestration、Worker Runtime、Eventing Projection、Frontier、Artifact/Trace 和 Local Product composition root。
- 常用 Makefile 门禁。
- Switchback 专用 CodeGraph 查询示例，至少覆盖：
  - TaskSpec 如何进入 Task 并启动 WorkerRun；
  - Runtime heartbeat/loss 如何进入 recovery；
  - Worker/Task 事件如何投影到 Frontier 展示。
- 明确文档可能滞后，关键结论必须由当前实现和测试验证。

`AGENTS.md` 链接该文档，保证入口可发现。

## 数据流

```text
Agent 问题
  -> AGENTS.md 选择 CodeGraph-first
  -> make codegraph-status
  -> codegraph explore 获取相关源码、调用路径和影响范围
  -> docs/agent-navigation.md 补充领域入口与验证命令
  -> 读取当前源码/测试/配置核验
  -> 执行范围匹配的 Makefile 门禁
```

CodeGraph 不进入 Switchback 运行时，也不产生产品数据；它只在开发工作区建立本地代码知识图。

## 错误处理

- CLI 不存在：Makefile 直接失败并显示 shell 的 command-not-found，不自动联网安装。
- 项目未初始化或索引不可用：`codegraph-status`/`codegraph-smoke` 失败，使用 `make codegraph-init` 修复。
- 查询结果为空或不完整：缩小到具体符号或文件，再用 `rg` 和直接源码读取补证。
- 索引与工作树不一致：重新执行 `make codegraph-init`，随后再次检查状态。
- 任何初始化失败都不得触碰或清理现有源码改动。

## 验收

实施后执行：

1. `make codegraph-init`
2. `make codegraph-status`
3. `make codegraph-smoke`
4. 分别查询 Task 创建、Runtime recovery、Frontier projection 三条路径，并确认结果包含当前仓库的真实源码。
5. `git check-ignore .codegraph/codegraph.db`
6. `git diff --check`
7. 检查 Git diff，确认只包含本设计声明的文件和本地索引目录。

Go、TypeScript 和运行时行为均不改变，因此不要求运行完整 `make verify`；如果实施时意外触及业务代码，则必须升级为相应项目门禁。

## 非目标

- 不升级或重新安装全局 CodeGraph。
- 不提交 `.codegraph/codegraph.db`。
- 不为 CodeGraph 增加 CI 强制门禁。
- 不基于首次图查询直接重构 DDD 模块。
- 不复制 `TECHNICAL_DESIGN.md`、`CONTEXT.md` 或 OpenSpec 的大段内容。
