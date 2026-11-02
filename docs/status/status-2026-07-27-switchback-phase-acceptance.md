# Switchback 阶段验收报告

日期：2026-07-27

## 验收结论

本轮可以阶段性验收以下范围：

- 产品身份已按 hard cut 策略统一为 **Switchback**，代码模块、命令、环境变量、HTTP Header、事件主题、数据库 schema、镜像、Compose 资源、Web/Demo 文案和工程文档不再保留旧标识兼容层。
- Assistant / Worker Workbench、Artifact Review、Timeline、Assistant 持久化派发主链、Worker Runtime 恢复基础设施、可信网络配置和本地 Compose 产品面已完成当前实现。
- 全量 Go 门禁、Web 测试、Demo lint/build、Python callback 测试、Spike 非 E2E 测试、CodeGraph smoke 和 OpenSpec strict validation 均通过。
- 全新 Switchback 数据卷上的 Compose 已完成冷构建和重启；Backend、Web、Assistant LiteLLM、PostgreSQL、Langfuse 依赖均已拉起，18 个迁移连续执行成功。

本轮不能宣称以下范围已经最终完成：

- Single Runtime Recovery 的 active OpenSpec 当前完成 `43/58`；本地 Compose 默认开关仍为 `SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=0`。
- Web IDE / SessionProcess 当前为设计就绪，实施任务为 `0/35`。
- 本轮未发起真实外部模型调用，避免把供应商费用、凭证状态和网络副作用混入本地阶段验收。

因此，本轮验收口径是：**Switchback 产品身份、当前工作台与本地运行基线可验收；Runtime Recovery 完整故障闭环和 Web IDE 不在本轮完成声明内。**

## 交付进度

| 能力切片 | OpenSpec / 任务状态 | 当前事实 | 阶段判断 |
| --- | ---: | --- | --- |
| Worker Workbench Interaction Hardening | `34/34`，已归档 | Assistant/Worker 工作台、Artifact Review、Timeline 和交互硬化已实现并通过测试 | 完成 |
| M2 Assistant Task Dispatch | `34/34` | 持久化 Conversation、TaskDraft、Readiness、TaskSpec 与 Worker dispatch 主链已接入产品入口 | 当前切片完成 |
| Trusted Full Network Profile | `25/25` | `trusted_full_network_v1` 的契约、快照、运行配置和验证已实现 | 当前切片完成 |
| Single Runtime Recovery | `43/58` | 恢复 incident、generation fencing、settlement/outbox 等基础设施已落地；完整故障与 Backend restart 证明仍缺失 | 未完成，默认关闭 |
| Worker Web IDE / SessionProcess | `0/35` | 方案和任务已定义，尚未进入实现 | 设计阶段 |

Single Runtime Recovery 剩余 15 项集中在四类：

1. 容器/进程退出与 ACP EOF 的 incident 触发边界。
2. Deadline、Session expiry 和安全终止对 recovery 的抢占与 fencing。
3. fixed-image、真实故障、Backend restart、未知 update 和 no-prompt-replay 验收。
4. Runtime authority、双向 cursor、replay barrier、Backend rehydrate 与 terminal settlement 的持久化 exactly-once 证明。

## 原子提交审计

相对 `origin/master` 的实现与重构 payload 共 25 个提交。提交按“可独立解释、可独立审阅、单一交付意图”拆分；hard cut 重命名虽然文件数较大，但分别限定在 runtime identifier、deployment resource 和 documentation identity 三个原子关注点。

| Commit | 主题 | 文件数 |
| --- | --- | ---: |
| `f36a111` | `feat: export complete langfuse model content` | 18 |
| `66e2f95` | `feat: serve local web through compose` | 9 |
| `8659a5c` | `docs: archive m1 worker delivery change` | 17 |
| `16dbd93` | `docs: propose m2 assistant task dispatch` | 7 |
| `4ec8d91` | `docs: specify worker workbench hardening` | 5 |
| `a5096d9` | `docs: design optional worker web ide` | 6 |
| `a78fb55` | `docs: propose trusted network and runtime recovery` | 11 |
| `3ba6b50` | `docs: design CodeGraph agent navigation` | 1 |
| `1971da7` | `docs: specify Switchback hard-cut rename` | 1 |
| `25487ae` | `docs: plan Switchback hard-cut delivery` | 1 |
| `8d75ffd` | `chore(agent): add CodeGraph navigation workflow` | 5 |
| `eed538f` | `feat(web): add Assistant and Worker workbenches` | 39 |
| `4d5db63` | `feat(workbench): add artifact review and timeline primitives` | 19 |
| `68e13d5` | `feat(assistant): add durable conversation and dispatch core` | 69 |
| `ec570ec` | `feat(runtime): define trusted network profile contract` | 8 |
| `542ef6c` | `feat(runtime): make recovery and turn settlement durable` | 126 |
| `460b7f1` | `feat(demo): add interactive product workbench prototype` | 87 |
| `788123e` | `docs(openspec): archive worker workbench hardening` | 11 |
| `f9709a9` | `feat(platform): wire assistant dispatch and runtime recovery` | 83 |
| `ab6e05c` | `docs(assistant): record M2 dispatch acceptance` | 4 |
| `782c87d` | `docs(runtime): capture recovery and network operations` | 19 |
| `1b2fabb` | `docs: align platform baseline with implemented slices` | 8 |
| `e636512` | `refactor!: rename runtime identifiers to Switchback` | 332 |
| `cf11af1` | `build!: rename local deployment resources to Switchback` | 104 |
| `afaf3d9` | `docs: adopt Switchback as the sole product identity` | 133 |

本报告作为独立的阶段验收证据另行提交，不与实现或重命名提交混合。

## 验证证据

### 仓库门禁

- `make verify`：通过。
  - format / migration / generated protocol / worker image provenance
  - Go unit / race / vet / integration
  - secret scan
  - 8 个 OpenSpec change strict validation
- Web：15 个 test file、54 个测试通过。
- Web Demo：lint 和 production build 通过。
- Python callbacks：两组测试分别为 25 和 23 个，通过。
- Spike：非 E2E package 测试通过。
- CodeGraph：status 和 smoke 通过。
- tracked file 内容与路径的旧产品标识扫描：无残留。

### Compose 运行验收

启动命令：

```bash
make local-up
```

运行结果：

- 9 个长期服务处于 `Up`，迁移容器以 `Exited (0)` 正常结束。
- Backend：`http://127.0.0.1:8080/healthz` 返回：

  ```json
  {"service":"switchback-backend","status":"ok"}
  ```

- Web：`http://127.0.0.1:4173/` 返回成功，容器 health 为 healthy。
- Langfuse：`http://127.0.0.1:3000/` 已启动。
- PostgreSQL schema migration：`000001` 至 `000018` 连续存在。
- Backend 容器只暴露 `SWITCHBACK_*` 产品配置；恢复开关实际值为 `0`。
- Docker 容器、卷和镜像的旧产品标识扫描无残留。

本轮自建镜像：

| Image | Digest |
| --- | --- |
| `switchback-web:local` | `sha256:a7593d733104e88556d872c17b77e75e4cd242b9e272f28eea84d451b57b0a8f` |
| `switchback-backend:local` | `sha256:5e1174338dd2e61fe269ba7fa504fc2eae3ad9ba9f1177a654dfe500919e1340` |
| `switchback-worker:local` | `sha256:8b943dc85b48291a10c8e1fe611594971b7d1e6c2768cc9d20628d405ed7164c` |
| `switchback-assistant-litellm:local` | `sha256:9190c1c0be2d02d67a5e6c58f19c18401c8192d92ca0328e8c518b3290f3bf50` |

## Hard cut 本地状态处理

- 旧 Compose volumes、应用镜像和测试容器已删除，避免旧数据库 schema、镜像 tag 或资源名被新版本误复用。
- 旧本地环境配置已可恢复地移动到 `/private/tmp/switchback-local-env-pre-hard-cut-20260727`。
- 旧运行态目录已可恢复地移动到 `/private/tmp/pre-switchback-runtime-20260727`。
- 新运行态使用 `.switchback/local` 和新的 Switchback Compose resources。
- 仓库 checkout 的本地目录名不属于产品运行契约，本轮未移动 Git 工作区。

## 下一轮建议

下一轮优先完成 Single Runtime Recovery，而不是立即进入 Web IDE：

1. 先补 `2.3` 与 `6.4`，冻结 incident 触发和抢占/fencing 边界。
2. 再实现 `9.1` 至 `9.10` 的 durable cursor、replay barrier、Backend rehydrate 与 settlement exactly-once。
3. 最后执行 `5.7`、`8.6`、`8.7` 的 fixed-image 和真实故障验收，通过后再把本地 recovery 默认开关从 `0` 提升为 `1`。

原因是 Runtime Recovery 决定正在执行的交付能否在故障后保持同一 Session/Run/Turn 事实；在这条可靠性底座完成前增加 Web IDE，会扩大需要恢复和 fencing 的状态面。
