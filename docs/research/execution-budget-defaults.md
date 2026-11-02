# 执行预算默认值与硬上限调研

_Switchback P0 Worker 执行预算研究记录，证据截面截至 2026-07-11；只采用厂商官方文档与一手产品资料_

> 历史状态：本报告记录被采用过的分层预算方案，但该方案已于 2026-07-15 被 [ADR 0055](../adr/0055-use-a-fixed-agentturn-hard-deadline.md) 取代。当前 P0 只使用每个 AgentTurn 独立、固定 24 小时且任何状态不暂停的 wall-clock hard deadline；下文数值不再是实现基线。

---

## 🎯 Executive recommendation

Switchback P0 不应复制任一 CI 平台的单一 `timeout`。主流系统实际在治理三种不同风险：阶段执行无限占用、Agent 单轮失控，以及单个命令或外部调用挂起。结合 GitHub Actions、GitLab CI、CircleCI、Kubernetes Job、GitHub Copilot cloud agent、Codex cloud、Cloud Run Jobs 和 AWS CodeBuild 的官方口径，建议采用下列初始策略：

| Switchback 控制 | P0 默认值 | P0 hard ceiling | 计时方式 | 到期语义 |
| --- | ---: | ---: | --- | --- |
| `WorkerRunDeadline` | 60 分钟 active time | 6 小时 active time | 模型推理、Tool 执行和 `active_quiet` 计入；排队、`waiting_user`、确认的平台故障与 runtime recovery 暂停 | 交互式任务请求用户决策；仅显式无人值守策略可自动取消 |
| WorkerRun wall cap | 24 小时 | 固定 24 小时 | 从第一次进入 active execution 起连续 wall-clock，任何状态都不暂停 | 不再允许续期；保存 checkpoint、Trace 和阶段报告后结束当前 Run |
| `AgentTurnDeadline` | 30 分钟 active time | 60 分钟 active time | 与 WorkerRun 使用相同 active-time 状态，但同时受 WorkerRun 剩余预算约束 | 请求用户继续、转向或结束；不得隐式延长 WorkerRun |
| `ToolTimeout` | 10 分钟 monotonic wall time | 60 分钟 monotonic wall time | 每次 Tool invocation 独立计时，等待 I/O 和无输出时间均计入，不暂停 | 终止或 fencing 当前调用，返回结构化 timeout，不直接取消 Turn/Run |
| `build_test` Tool profile | 30 分钟 monotonic wall time | 60 分钟 monotonic wall time | 仅用于已识别的构建、测试、静态分析等长命令 | 与普通 ToolTimeout 相同 |

这组值的证据基础不是“行业统一标准”，而是三个稳定信号：通用 CI 的常见默认集中在 60 分钟；云端 coding agent 的公开硬边界在约 1 小时；长任务平台通常允许数小时，但会把排队、审批或单次 attempt 与实际运行分开治理。数值仍需通过 Switchback 固定镜像 spike 和早期生产分位数校准。

## 📐 Scope and terminology

本报告只比较能够映射到 Switchback 三层预算的公开语义：

| 术语 | 本报告中的含义 | 不应混入的概念 |
| --- | --- | --- |
| 默认超时 | 用户未显式指定时，平台实际采用的运行预算 | 典型完成时间、计费周期、保留期 |
| hard ceiling | 用户、项目或普通管理员不能突破的执行上限 | 可配置默认、软告警、推荐值 |
| active time | 实际执行模型、Agent 或工具工作的累计时间 | 排队、审批、等待用户、平台恢复 |
| wall cap | 从首次启动起不暂停的自然时间上限 | WorkerSession 48 小时 idle timeout、14 天 absolute lifetime |
| no-output timeout | 进程持续无日志输出的挂起启发式 | Tool 总运行时长、Agent 无 token 输出 |

不同厂商没有统一暴露 active-time 计数器。凡官方文档只写“run”“execution time”或“duration”，本报告保留原语义，不自行宣称它会在哪些内部状态暂停。

## 📊 Official evidence matrix

| 平台 | 默认值 | 可配置/硬上限 | 计时与暂停语义 | 对 Switchback 的直接价值 |
| --- | --- | --- | --- | --- |
| GitHub Actions | Job `timeout-minutes` 默认 360 分钟[^1] | GitHub-hosted Job 最长执行 6 小时；self-hosted Job 最长 5 天；单 Step `timeout-minutes` 最大 360 分钟[^1][^2] | 官方把 Job execution 与 queue 分开；环境保护规则通过前 Job 不启动，wait timer 不计 billable time[^2][^3] | 支持将排队/审批与 active execution 分开，并把 P0 通用 runtime ceiling 控制在小时级 |
| GitLab CI | 新项目 Job timeout 默认 60 分钟[^4] | Project timeout 至少 10 分钟且小于 1 个月；Runner maximum timeout 更短时优先；Job 可覆盖项目值但不能超过 Runner 值[^4][^5][^6] | Pending Job 24 小时无活动后丢弃；运行中 1 小时无输出会被丢弃，不论总 timeout[^4] | 提供 60 分钟默认基准，但其 no-output 规则不能照搬给无 token 输出也可能健康的 Agent |
| Kubernetes Job | 未配置 `activeDeadlineSeconds` 时无默认 deadline，Job 默认持续运行直至完成或失败策略终止[^7] | 官方 Job 文档未给出平台级 hard maximum[^7] | Deadline 覆盖整个 Job 及重试；Job suspend 时计时器停止，resume 时重新计时[^7] | 支持“总预算跨重试”和显式暂停，但 Switchback 不应在 resume 时重置已经消耗的预算 |
| CircleCI | `run.no_output_timeout` 默认 10 分钟[^8] | Job 按套餐硬限 1/3/5 小时；no-output 值受 Job 最大时长约束[^8] | Approval 是不执行步骤的独立 Job，Workflow 在此暂停；非终态 Workflow 90 天后自动取消[^9] | 支持 10 分钟作为挂起保护候选和 5–6 小时级总 ceiling，但 no-output 不是 Tool 总时长 |
| GitHub Copilot cloud agent | 官方未公开可单独配置的默认 session 时长[^10] | 每个 session 最长执行 59 分钟，不能延长或绕过；可配置更短值[^10] | 在 GitHub Actions 驱动的临时环境执行；官方未公开 session 内部的暂停计时规则[^10] | 是 `AgentTurnDeadline` 采用 30 分钟默认、60 分钟 ceiling 的最直接公开参照 |
| OpenAI Codex cloud | 当前官方 cloud 与 environment 文档未找到默认 timeout 或 hard ceiling[^11][^12] | 未找到[^11][^12] | 2025 年首发资料称任务典型完成时间为 1–30 分钟，但这是观测描述，不是限制[^13] | 只支持 30 分钟作为 AgentTurn 初始预算候选，不能据此声称 Codex 的硬上限 |
| Cloud Run Jobs | 每个 Task attempt 默认 10 分钟[^14] | 非 GPU Task 最长 168 小时；GPU Task 最长 1 小时；Job execution 本身无显式 timeout[^14] | timeout 对每次 attempt 独立生效，重试会获得新的 attempt timeout[^14] | 支持把 ToolTimeout 定义为 invocation/attempt 局部预算，而不是 WorkerRun 总预算 |
| AWS CodeBuild | Build 默认 60 分钟[^15] | Build 可配置 5 分钟至 36 小时；Queue timeout 独立配置[^15][^16] | Build queue 有独立 timeout，不消耗 Build timeout 的同一字段[^16] | 再次支持 60 分钟运行默认，并证明排队预算应独立于执行预算 |

## 🔍 Findings and inferences

以下内容是从官方事实得到的推论，不是厂商承诺。

### Sixty minutes is the strongest default anchor

GitLab 和 AWS CodeBuild 都采用 60 分钟默认，CircleCI Free 与 GitHub Copilot cloud agent 的硬边界也约为 1 小时。GitHub Actions 的 360 分钟默认明显偏向通用 CI 上限，而不是一次交互式 Agent turn。由此推论：Switchback 将 60 分钟作为 WorkerRun 初始 active budget，比直接采用 6 小时更利于成本控制和阶段性反馈。

### Agent turn should be shorter than worker run

WorkerRun 可以包含多个 AgentTurn、Tool 调用、重试和阶段总结。若 Turn 默认值与 Run 相同，一个静默 Turn 可以耗尽全部阶段预算。Codex 首发资料的典型 1–30 分钟和 Copilot 的 59 分钟硬上限共同支持“30 分钟默认、60 分钟 ceiling”，但这仍是 Switchback 的产品判断。

### No-output timeout is not Agent liveness

CircleCI 的 10 分钟和 GitLab 的 1 小时规则都依赖日志输出。Switchback 已确认模型推理或 Tool 执行可以合法进入 `active_quiet`，所以不能用无 token/Trace 直接宣告 Agent 故障。10 分钟只适合作为普通 Tool invocation 的总预算起点，真正的 runtime failure 仍由 heartbeat、process exit、stdio EOF 或 cancel grace 判定。

### Retries consume parent budget

Kubernetes 的 Job deadline 覆盖重试，而 Cloud Run 为每个 attempt 重新应用 Task timeout。Switchback 应组合两者：每个 Tool retry 有自己的 ToolTimeout，但所有 attempt 的活动时间继续消耗同一个 AgentTurn 和 WorkerRun 剩余预算。这样既允许局部重试，也不会通过无限重试绕过总 ceiling。

### Pausing must preserve remaining budget

CI 平台通常在执行前用 queue、approval 或 environment gate 表达等待；Kubernetes suspend/resume 则会重置 deadline timer。Switchback 的会话需要长时间等待用户，因此应暂停 active-time 累计，但恢复后只能继续使用原剩余预算，不能像 Kubernetes Job 那样重置整段额度。24 小时 wall cap 则始终运行，防止审批或恢复状态无限占用一个 WorkerRun。

## 🧭 Switchback P0 policy proposal

### WorkerRunDeadline

- `default_active_seconds: 3600`
- `max_active_seconds: 21600`
- `wall_clock_cap_seconds: 86400`
- Active time 包含 `running_model`、`running_tool`、`active_quiet`、`cancelling`
- Active time 排除 `queued`、`waiting_user`、`platform_outage`、`runtime_recovering`
- Retry、Agent replacement 和 Runtime generation 变化不得重置已消耗 active time
- `ExecutionBudgetGrant` 可以在 6 小时 ceiling 内追加 active budget，不能延长 24 小时 wall cap

选择 6 小时 ceiling，是因为它与 GitHub-hosted Actions 的硬限制一致，并略高于 CircleCI Scale 的 5 小时，同时远低于 self-hosted/通用 batch 平台的多日上限。对 P0 用户级并发 10 个常驻 WorkerSession 来说，这是比 36 小时或 7 天更可控的成本边界。

### AgentTurnDeadline

- `default_active_seconds: 1800`
- `max_active_seconds: 3600`
- 实际 Turn 预算为 `min(resolved_turn_budget, worker_run_remaining_active)`
- 不设置独立 wall cap；它继承 WorkerRun 24 小时 wall cap
- Turn 到达默认预算时，交互式任务进入用户决策；在 ceiling 内可追加 Grant
- 到达 hard ceiling 后不得继续当前 Turn；需要基于 checkpoint 创建新的 AgentTurn

新的 AgentTurn 不是规避预算的手段：它仍消耗同一 WorkerRun 的剩余 active budget。该拆分只是强制 Agent 在约一小时内产生可审查的阶段边界。

### ToolTimeout

- `default_timeout_seconds: 600`
- `build_test_profile_seconds: 1800`
- `max_timeout_seconds: 3600`
- 使用后端或 Shim monotonic clock，从 Tool invocation 被接受并开始执行时计时
- I/O 等待、子进程运行和 `active_quiet` 均不暂停 ToolTimeout
- Timeout 后先终止本地进程组或取消远端调用，再 fencing invocation id；迟到结果只写 Trace，不得改变已完成状态
- 更大 timeout 的重试是新的 invocation，必须由 TaskSpec 预授权或 `ExecutionBudgetGrant` 允许

10 分钟来自 Cloud Run Task 默认和 CircleCI no-output 默认的共同量级，但 Switchback 将它定义为总 Tool wall time，而不是无输出启发式。构建和测试经常合法超过 10 分钟，因此 P0 提供显式 30 分钟 profile；超过 60 分钟的操作应拆成可 checkpoint 的后台任务，而不是继续占用一个 AgentTurn Tool call。

## ⚠️ Risks and validation plan

| 风险 | 影响 | P0 验证与调整信号 |
| --- | --- | --- |
| 60 分钟 WorkerRun 默认过短 | 大仓构建或多仓任务频繁请求 Grant | 统计 `budget_extension_requested`、首次阶段结果时间和 P95 Run active time |
| 30 分钟 AgentTurn 打断推理 | Agent 在可交付结果前被迫停止 | 统计 Turn timeout 前最后一次 durable checkpoint、恢复成功率和用户继续率 |
| 10 分钟 Tool 默认过短 | 测试/依赖安装误超时 | 按 Tool kind 统计 P50/P95/P99，并检查 `build_test` profile 命中率 |
| 24 小时 wall cap 过短 | 跨时区审批后只能新建 Run | 统计 wall-cap 到期时状态及 `waiting_user` 占比 |
| active-time 状态归类错误 | 成本泄漏或用户预算被平台故障消耗 | 对每次状态迁移记录 monotonic timestamp、reason 和 policy revision，做账本重放测试 |
| cancel 不彻底 | Tool 超时后后台进程继续写 WorkingCopy | Docker spike 验证 process-group cleanup、late-result fencing 和旧 generation 写租约撤销 |

数值冻结前应至少执行固定 Codex/ACP/LiteLLM 镜像的四类 spike：无输出长模型调用、12 分钟普通命令、35 分钟 build/test profile、Tool timeout 后子进程与网络调用清理。上线后先以 policy revision 管理这些值，至少收集两周分布数据，再决定是否按仓库规模、任务类型或用户级别增加 profile。

## 📚 References

[^1]: GitHub. "Workflow syntax for GitHub Actions." _GitHub Docs_. https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax

[^2]: GitHub. "Actions limits." _GitHub Docs_. https://docs.github.com/en/actions/reference/limits

[^3]: GitHub. "Deployments and environments." _GitHub Docs_. https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments

[^4]: GitLab. "Customize pipeline configuration: Set a limit for how long jobs can run." _GitLab Docs_. https://docs.gitlab.com/ci/pipelines/settings/#set-a-limit-for-how-long-jobs-can-run

[^5]: GitLab. "CI/CD YAML syntax reference: timeout." _GitLab Docs_. https://docs.gitlab.com/ci/yaml/#timeout

[^6]: GitLab. "Configuring runners: Set the maximum job timeout." _GitLab Docs_. https://docs.gitlab.com/ci/runners/configure_runners/#set-the-maximum-job-timeout

[^7]: Kubernetes. "Jobs." _Kubernetes Documentation_. https://kubernetes.io/docs/concepts/workloads/controllers/job/

[^8]: CircleCI. "Configuration reference." _CircleCI Docs_. https://circleci.com/docs/reference/configuration-reference/

[^9]: CircleCI. "Workflow orchestration." _CircleCI Docs_. https://circleci.com/docs/guides/orchestrate/workflows/

[^10]: GitHub. "About GitHub Copilot cloud agent." _GitHub Docs_. https://docs.github.com/en/enterprise-cloud@latest/copilot/concepts/agents/cloud-agent/about-cloud-agent

[^11]: OpenAI. "Codex cloud." _OpenAI Developer Documentation_. https://developers.openai.com/codex/cloud

[^12]: OpenAI. "Cloud environments." _OpenAI Developer Documentation_. https://developers.openai.com/codex/cloud/environments

[^13]: OpenAI. (2025). "Introducing Codex." _OpenAI_. https://openai.com/index/introducing-codex/

[^14]: Google Cloud. "Set task timeout for jobs." _Cloud Run Documentation_. https://cloud.google.com/run/docs/configuring/task-timeout

[^15]: Amazon Web Services. "Create a build project in AWS CodeBuild." _AWS CodeBuild User Guide_. https://docs.aws.amazon.com/codebuild/latest/userguide/create-project.html

[^16]: Amazon Web Services. "Builds in AWS CodeBuild." _AWS CodeBuild User Guide_. https://docs.aws.amazon.com/codebuild/latest/userguide/builds-working.html
