# 单任务正式首报验收

2026-11-23，公开基线 `fc26dd37` 叠加本轮首报修复，在独立 ARM64 Lima VM 完成一次正式首报。原 Core 保持健康。验收覆盖一个 Task、一个 WorkerRun 和一个 Runtime。

| 验收项 | 结果 |
| --- | --- |
| 容量准入 | PostgreSQL `000027` 在派发事务内预留容量。运行中的 Worker 写层对应 XFS 项目，硬限制为 10 GiB。 |
| 请求限次 | 假上游演练中，第二次客户端请求返回 `409`，内部重试也被拒绝，假上游收到一次。正式窗口通过 relay 发出一次 DeepSeek V4 Pro 请求，返回 `200 text/event-stream`，无上游重试。 |
| 产物和观测 | 正式报告正文、Trace manifest 和三个 Trace 段的校验和均通过。报告和一条状态为 `ok` 的模型观测关联到同一 Task、Run 和 Turn。 |
| 页面验收 | 生产 Web 提交一次 Accept，返回 `202`。浏览器刷新后，同一 Task 在任务列表仍为 `completed`，投影 revision 为 `5`。 |

首报 profile 仅在专用配置下启用，并为该隔离部署保留单个 Engine 容量预留。验收结束后已回收隔离容器、网络和临时凭据。原始回执、报告正文、截图与 Trace 内容保留在本地。

Langfuse 导出在本次运行中为 `pending`，独立跟踪。
