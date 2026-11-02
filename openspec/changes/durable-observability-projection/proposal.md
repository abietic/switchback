## Why

本变更最初解决同步 Langfuse HTTP 占用 Turn 收尾路径及内存观测不可恢复的问题。该 v2 路径已在分支实现，但 2026-09-10 真实 Docker smoke 证明 terminal cut 并非模型 callback 的完整性边界：seq 7 terminal 后的合法 seq 8 模型被永久排除，Langfuse 恢复也不能修复原 marker。用户于 2026-09-13 确认新增不可变补充来源的书面设计；当前变更继续落实该修订，不把 v2 过程门禁当成最终通过。

## What Changes

- 保留旧 source/TraceManifest/checkpoint/v2 job 的冻结内容；新增独立 `observation.fragment.v1` Run/Turn basis 和每逻辑模型一份 supplement，零模型不阻断 basis。
- 单 owner 应用接收入口处理 active、idle、后续 Turn 和 replay 下的合法模型事件；原 scope 扫描证明与 accepted Apply 或首次 basis freeze 同事务保存，ObjectStore/HTTP 全部后台执行。
- 使用独立 `langfuse.projection.v3`、Run activation 排除目录、exact source publication/job 与有界 fenced lease；不自动回灌已存在 Run，不复用 v2 encoder/ID 语义。
- 停止 v2 自动投影，保留只读证据；模型级去重与冲突拒绝、完整 I/O 编码及精确 readback 防止重复统计和晚到批次覆盖。
- Runtime 清理以 exact durable fencing 为前提，不等待远端投影；缺原扫描证明的历史或 forced-stop basis 明确安全阻断。Coverage 只对明确已接受集合陈述，不证明未来无 callback。
- 后台投影循环独立于 Runtime terminal / next-turn 与 Backend readiness；Compose 不依赖 Langfuse 健康启动 Backend。
- 本变更只实现已批准设计的第一片；Prometheus / Loki / Grafana、Tool/MCP 深化和 YHC Harness 各在后续变更实现。

## Capabilities

### New Capabilities

- `durable-observability-projection`: 基于已封存执行证据的异步、可恢复、版本隔离的 Langfuse 派生视图。

### Modified Capabilities

- `runtime-execution-core`: 将 Langfuse 改为独立后台投影，Run 根不再保存会随后续 Turn 变化的最终输出；完整输出由对应 Turn span 承载。既有 Runtime 执行和授权状态机不增加业务状态。

## Impact

涉及 artifacttrace、WorkerControl 应用/事务、LocalProduct 装配与 Runtime secret cleanup、PostgreSQL 增量迁移及本地验收。v3 不向旧 Manifest 追加或改写 segment，不新增 Task/Run 状态、请求 inventory、fallback transport 或公开重放 API。应用接收接口保持传输无关，当前只实现已有 gRPC 路径。Langfuse 客户端沿用锁定部署版本；供应商升级、历史导出执行、主 spec 同步/归档单独授权。PostgreSQL / ObjectStore 仍为核心依赖，其故障不被误报为纯观测故障。
